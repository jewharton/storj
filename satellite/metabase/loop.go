// Copyright (C) 2021 Storj Labs, Inc.
// See LICENSE for copying information.

package metabase

import (
	"bytes"
	"context"
	"sort"
	"time"

	"github.com/zeebo/errs"

	"storj.io/common/storj"
	"storj.io/common/uuid"
	"storj.io/private/dbutil/pgutil"
	"storj.io/private/tagsql"
)

const loopIteratorBatchSizeLimit = 2500

// IterateLoopObjects contains arguments necessary for listing objects in metabase.
type IterateLoopObjects struct {
	BatchSize int

	AsOfSystemTime     time.Time
	AsOfSystemInterval time.Duration
}

// Verify verifies get object request fields.
func (opts *IterateLoopObjects) Verify() error {
	if opts.BatchSize < 0 {
		return ErrInvalidRequest.New("BatchSize is negative")
	}
	return nil
}

// LoopObjectsIterator iterates over a sequence of LoopObjectEntry items.
type LoopObjectsIterator interface {
	Next(ctx context.Context, item *LoopObjectEntry) bool
}

// LoopObjectEntry contains information about object needed by metainfo loop.
type LoopObjectEntry struct {
	ObjectStream                       // metrics, repair, tally
	Status                ObjectStatus // verify
	CreatedAt             time.Time    // temp used by metabase-createdat-migration
	ExpiresAt             *time.Time   // tally
	SegmentCount          int32        // metrics
	TotalEncryptedSize    int64        // tally
	EncryptedMetadataSize int          // tally
}

// Expired checks if object is expired relative to now.
func (o LoopObjectEntry) Expired(now time.Time) bool {
	return o.ExpiresAt != nil && o.ExpiresAt.Before(now)
}

// IterateLoopObjects iterates through all objects in metabase.
func (db *DB) IterateLoopObjects(ctx context.Context, opts IterateLoopObjects, fn func(context.Context, LoopObjectsIterator) error) (err error) {
	defer mon.Task()(&ctx)(&err)

	if err := opts.Verify(); err != nil {
		return err
	}

	it := &loopIterator{
		db: db,

		batchSize: opts.BatchSize,

		curIndex:           0,
		cursor:             loopIterateCursor{},
		asOfSystemTime:     opts.AsOfSystemTime,
		asOfSystemInterval: opts.AsOfSystemInterval,
	}

	// ensure batch size is reasonable
	if it.batchSize <= 0 || it.batchSize > loopIteratorBatchSizeLimit {
		it.batchSize = loopIteratorBatchSizeLimit
	}

	it.curRows, err = it.doNextQuery(ctx)
	if err != nil {
		return err
	}

	defer func() {
		if rowsErr := it.curRows.Err(); rowsErr != nil {
			err = errs.Combine(err, rowsErr)
		}
		err = errs.Combine(err, it.failErr, it.curRows.Close())
	}()

	return fn(ctx, it)
}

// loopIterator enables iteration of all objects in metabase.
type loopIterator struct {
	db *DB

	batchSize          int
	asOfSystemTime     time.Time
	asOfSystemInterval time.Duration

	curIndex int
	curRows  tagsql.Rows
	cursor   loopIterateCursor

	// failErr is set when either scan or next query fails during iteration.
	failErr error
}

type loopIterateCursor struct {
	ProjectID  uuid.UUID
	BucketName string
	ObjectKey  ObjectKey
	Version    Version
}

// Next returns true if there was another item and copy it in item.
func (it *loopIterator) Next(ctx context.Context, item *LoopObjectEntry) bool {
	next := it.curRows.Next()
	if !next {
		if it.curIndex < it.batchSize {
			return false
		}

		if it.curRows.Err() != nil {
			return false
		}

		rows, err := it.doNextQuery(ctx)
		if err != nil {
			it.failErr = errs.Combine(it.failErr, err)
			return false
		}

		if closeErr := it.curRows.Close(); closeErr != nil {
			it.failErr = errs.Combine(it.failErr, closeErr, rows.Close())
			return false
		}

		it.curRows = rows
		it.curIndex = 0
		if !it.curRows.Next() {
			return false
		}
	}

	err := it.scanItem(item)
	if err != nil {
		it.failErr = errs.Combine(it.failErr, err)
		return false
	}

	it.curIndex++
	it.cursor.ProjectID = item.ProjectID
	it.cursor.BucketName = item.BucketName
	it.cursor.ObjectKey = item.ObjectKey
	it.cursor.Version = item.Version

	return true
}

func (it *loopIterator) doNextQuery(ctx context.Context) (_ tagsql.Rows, err error) {
	defer mon.Task()(&ctx)(&err)

	return it.db.db.QueryContext(ctx, `
		SELECT
			project_id, bucket_name,
			object_key, stream_id, version,
			status,
			created_at, expires_at,
			segment_count, total_encrypted_size,
			LENGTH(COALESCE(encrypted_metadata,''))
		FROM objects
		`+it.db.asOfTime(it.asOfSystemTime, it.asOfSystemInterval)+`
		WHERE (project_id, bucket_name, object_key, version) > ($1, $2, $3, $4)
		ORDER BY project_id ASC, bucket_name ASC, object_key ASC, version ASC
		LIMIT $5
		`, it.cursor.ProjectID, []byte(it.cursor.BucketName),
		[]byte(it.cursor.ObjectKey), int(it.cursor.Version),
		it.batchSize,
	)
}

// scanItem scans doNextQuery results into LoopObjectEntry.
func (it *loopIterator) scanItem(item *LoopObjectEntry) error {
	return it.curRows.Scan(
		&item.ProjectID, &item.BucketName,
		&item.ObjectKey, &item.StreamID, &item.Version,
		&item.Status,
		&item.CreatedAt, &item.ExpiresAt,
		&item.SegmentCount, &item.TotalEncryptedSize,
		&item.EncryptedMetadataSize,
	)
}

// IterateLoopStreams contains arguments necessary for listing multiple streams segments.
type IterateLoopStreams struct {
	StreamIDs []uuid.UUID

	AsOfSystemTime     time.Time
	AsOfSystemInterval time.Duration
}

// SegmentIterator returns the next segment.
type SegmentIterator func(ctx context.Context, segment *LoopSegmentEntry) bool

// LoopSegmentEntry contains information about segment metadata needed by metainfo loop.
type LoopSegmentEntry struct {
	StreamID      uuid.UUID
	Position      SegmentPosition
	CreatedAt     time.Time // non-nillable
	ExpiresAt     *time.Time
	RepairedAt    *time.Time // repair
	RootPieceID   storj.PieceID
	EncryptedSize int32 // size of the whole segment (not a piece)
	PlainOffset   int64 // verify
	PlainSize     int32 // verify
	Redundancy    storj.RedundancyScheme
	Pieces        Pieces
}

// Inline returns true if segment is inline.
func (s LoopSegmentEntry) Inline() bool {
	return s.Redundancy.IsZero() && len(s.Pieces) == 0
}

// IterateLoopStreams lists multiple streams segments.
func (db *DB) IterateLoopStreams(ctx context.Context, opts IterateLoopStreams, handleStream func(ctx context.Context, streamID uuid.UUID, next SegmentIterator) error) (err error) {
	defer mon.Task()(&ctx)(&err)

	if len(opts.StreamIDs) == 0 {
		return ErrInvalidRequest.New("StreamIDs list is empty")
	}

	sort.Slice(opts.StreamIDs, func(i, k int) bool {
		return bytes.Compare(opts.StreamIDs[i][:], opts.StreamIDs[k][:]) < 0
	})

	// TODO do something like pgutil.UUIDArray()
	bytesIDs := make([][]byte, len(opts.StreamIDs))
	for i, streamID := range opts.StreamIDs {
		if streamID.IsZero() {
			return ErrInvalidRequest.New("StreamID missing: index %d", i)
		}
		id := streamID
		bytesIDs[i] = id[:]
	}

	rows, err := db.db.QueryContext(ctx, `
		SELECT
			stream_id, position,
			created_at, expires_at, repaired_at,
			root_piece_id,
			encrypted_size,
			plain_offset, plain_size,
			redundancy,
			remote_alias_pieces
		FROM segments
		`+db.asOfTime(opts.AsOfSystemTime, opts.AsOfSystemInterval)+`
		WHERE
		    -- this turns out to be a little bit faster than stream_id IN (SELECT unnest($1::BYTEA[]))
			stream_id = ANY ($1::BYTEA[])
		ORDER BY stream_id ASC, position ASC
	`, pgutil.ByteaArray(bytesIDs))
	if err != nil {
		return Error.Wrap(err)
	}
	defer func() { err = errs.Combine(err, rows.Err(), rows.Close()) }()

	var noMoreData bool
	var nextSegment *LoopSegmentEntry
	for _, streamID := range opts.StreamIDs {
		streamID := streamID
		var internalError error
		err := handleStream(ctx, streamID, func(ctx context.Context, output *LoopSegmentEntry) bool {
			mon.TaskNamed("handleStreamCB-SegmentIterator")(&ctx)(nil)
			if nextSegment != nil {
				if nextSegment.StreamID != streamID {
					return false
				}
				*output = *nextSegment
				nextSegment = nil
				return true
			}

			if noMoreData {
				return false
			}
			if !rows.Next() {
				noMoreData = true
				return false
			}

			var segment LoopSegmentEntry
			var aliasPieces AliasPieces
			err = rows.Scan(
				&segment.StreamID, &segment.Position,
				&segment.CreatedAt, &segment.ExpiresAt, &segment.RepairedAt,
				&segment.RootPieceID,
				&segment.EncryptedSize,
				&segment.PlainOffset, &segment.PlainSize,
				redundancyScheme{&segment.Redundancy},
				&aliasPieces,
			)
			if err != nil {
				internalError = Error.New("failed to scan segments: %w", err)
				return false
			}

			segment.Pieces, err = db.aliasCache.ConvertAliasesToPieces(ctx, aliasPieces)
			if err != nil {
				internalError = Error.New("failed to convert aliases to pieces: %w", err)
				return false
			}

			if segment.StreamID != streamID {
				nextSegment = &segment
				return false
			}

			*output = segment
			return true
		})
		if internalError != nil || err != nil {
			return Error.Wrap(errs.Combine(internalError, err))
		}
	}

	if !noMoreData {
		return Error.New("expected rows to be completely read")
	}

	return nil
}

// LoopSegmentsIterator iterates over a sequence of LoopSegmentEntry items.
type LoopSegmentsIterator interface {
	Next(ctx context.Context, item *LoopSegmentEntry) bool
}

// IterateLoopSegments contains arguments necessary for listing segments in metabase.
type IterateLoopSegments struct {
	BatchSize          int
	AsOfSystemTime     time.Time
	AsOfSystemInterval time.Duration
}

// Verify verifies segments request fields.
func (opts *IterateLoopSegments) Verify() error {
	if opts.BatchSize < 0 {
		return ErrInvalidRequest.New("BatchSize is negative")
	}
	return nil
}

// IterateLoopSegments iterates through all segments in metabase.
func (db *DB) IterateLoopSegments(ctx context.Context, opts IterateLoopSegments, fn func(context.Context, LoopSegmentsIterator) error) (err error) {
	defer mon.Task()(&ctx)(&err)

	if err := opts.Verify(); err != nil {
		return err
	}

	it := &loopSegmentIterator{
		db: db,

		asOfSystemTime:     opts.AsOfSystemTime,
		asOfSystemInterval: opts.AsOfSystemInterval,
		batchSize:          opts.BatchSize,

		curIndex: 0,
		cursor:   loopSegmentIteratorCursor{},
	}

	// ensure batch size is reasonable
	if it.batchSize <= 0 || it.batchSize > loopIteratorBatchSizeLimit {
		it.batchSize = loopIteratorBatchSizeLimit
	}

	it.curRows, err = it.doNextQuery(ctx)
	if err != nil {
		return err
	}

	defer func() {
		if rowsErr := it.curRows.Err(); rowsErr != nil {
			err = errs.Combine(err, rowsErr)
		}
		err = errs.Combine(err, it.failErr, it.curRows.Close())
	}()

	return fn(ctx, it)
}

// loopSegmentIterator enables iteration of all segments in metabase.
type loopSegmentIterator struct {
	db *DB

	batchSize          int
	asOfSystemTime     time.Time
	asOfSystemInterval time.Duration

	curIndex int
	curRows  tagsql.Rows
	cursor   loopSegmentIteratorCursor

	// failErr is set when either scan or next query fails during iteration.
	failErr error
}

type loopSegmentIteratorCursor struct {
	StreamID uuid.UUID
	Position SegmentPosition
}

// Next returns true if there was another item and copy it in item.
func (it *loopSegmentIterator) Next(ctx context.Context, item *LoopSegmentEntry) bool {
	next := it.curRows.Next()
	if !next {
		if it.curIndex < it.batchSize {
			return false
		}

		if it.curRows.Err() != nil {
			return false
		}

		rows, err := it.doNextQuery(ctx)
		if err != nil {
			it.failErr = errs.Combine(it.failErr, err)
			return false
		}

		if failErr := it.curRows.Close(); failErr != nil {
			it.failErr = errs.Combine(it.failErr, failErr, rows.Close())
			return false
		}

		it.curRows = rows
		it.curIndex = 0
		if !it.curRows.Next() {
			return false
		}
	}

	err := it.scanItem(ctx, item)
	if err != nil {
		it.failErr = errs.Combine(it.failErr, err)
		return false
	}

	it.curIndex++
	it.cursor.StreamID = item.StreamID
	it.cursor.Position = item.Position

	return true
}

func (it *loopSegmentIterator) doNextQuery(ctx context.Context) (_ tagsql.Rows, err error) {
	defer mon.Task()(&ctx)(&err)

	return it.db.db.QueryContext(ctx, `
		SELECT
			stream_id, position,
			created_at, expires_at, repaired_at,
			root_piece_id,
			encrypted_size,
			plain_offset, plain_size,
			redundancy,
			remote_alias_pieces
		FROM segments
		`+it.db.asOfTime(it.asOfSystemTime, it.asOfSystemInterval)+`
		WHERE
			(stream_id, position) > ($1, $2)
		ORDER BY (stream_id, position) ASC
		LIMIT $3
		`, it.cursor.StreamID, it.cursor.Position,
		it.batchSize,
	)
}

// scanItem scans doNextQuery results into LoopSegmentEntry.
func (it *loopSegmentIterator) scanItem(ctx context.Context, item *LoopSegmentEntry) error {
	var aliasPieces AliasPieces
	err := it.curRows.Scan(
		&item.StreamID, &item.Position,
		&item.CreatedAt, &item.ExpiresAt, &item.RepairedAt,
		&item.RootPieceID,
		&item.EncryptedSize,
		&item.PlainOffset, &item.PlainSize,
		redundancyScheme{&item.Redundancy},
		&aliasPieces,
	)
	if err != nil {
		return Error.New("failed to scan segments: %w", err)
	}

	item.Pieces, err = it.db.aliasCache.ConvertAliasesToPieces(ctx, aliasPieces)
	if err != nil {
		return Error.New("failed to convert aliases to pieces: %w", err)
	}

	return nil
}
