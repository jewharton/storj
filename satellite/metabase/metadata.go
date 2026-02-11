// Copyright (C) 2020 Storj Labs, Inc.
// See LICENSE for copying information.

package metabase

import (
	"context"
	"errors"

	"cloud.google.com/go/spanner"
	"github.com/jackc/pgx/v5"
	"github.com/zeebo/errs"
	"google.golang.org/api/iterator"

	"storj.io/common/storj"
	"storj.io/common/uuid"
)

// ErrInsufficientMetadataIncludes is used to indicate that a provided EncryptedUserDataIncludes
// was not sufficient for an operation to succeed.
var ErrInsufficientMetadataIncludes = errs.Class("insufficient metadata includes")

// EncryptedUserData contains user data that has been encrypted with the nonce and key.
type EncryptedUserData struct {
	EncryptedMetadata             []byte
	EncryptedMetadataNonce        []byte
	EncryptedMetadataEncryptedKey []byte
	EncryptedETag                 []byte

	Checksum Checksum
}

// Checksum contains an object's checksum properties.
type Checksum struct {
	Algorithm      storj.ObjectChecksumAlgorithm
	IsComposite    bool
	EncryptedValue []byte
}

// VerifyEncryptedUserData contains options for EncryptedUserData.Verify.
type VerifyEncryptedUserData struct {
	// AllowEmptyChecksumValue, if true, allows an EncryptedUserData to contain an empty
	// checksum value when the checksum algorithm is set. Otherwise, an empty checksum
	// value will only be allowed if a checksum algorithm is set.
	AllowEmptyChecksumValue bool
}

// Verify checks whether the fields have been set correctly.
func (userData EncryptedUserData) Verify(opts VerifyEncryptedUserData) error {
	hasEncryptedData := userData.EncryptedMetadata != nil || userData.EncryptedETag != nil || userData.Checksum.EncryptedValue != nil
	err := verifyUserDataEncryptionParams(userData.EncryptedMetadataEncryptedKey, userData.EncryptedMetadataNonce, hasEncryptedData)
	if err != nil {
		return err
	}

	hasChecksumAlgo := userData.Checksum.Algorithm != storj.ObjectChecksumAlgorithmNone
	if userData.Checksum.Algorithm < storj.ObjectChecksumAlgorithmNone || userData.Checksum.Algorithm > storj.ObjectChecksumAlgorithmSHA256 {
		return ErrInvalidRequest.New("Checksum.Algorithm is invalid")
	}
	if !hasChecksumAlgo {
		if userData.Checksum.EncryptedValue != nil {
			return ErrInvalidRequest.New("Checksum.Algorithm must be set if Checksum.EncryptedValue is set")
		}
		if userData.Checksum.IsComposite {
			return ErrInvalidRequest.New("Checksum.Algorithm must be set if Checksum.IsComposite is set")
		}
	}

	if userData.Checksum.EncryptedValue == nil && hasChecksumAlgo && !opts.AllowEmptyChecksumValue {
		return ErrInvalidRequest.New("Checksum.EncryptedValue must be set if Checksum.Algorithm is set")
	}

	return nil
}

func verifyUserDataEncryptionParams(encryptedMetadataEncryptedKey, encryptedMetadataNonce []byte, hasEncryptedData bool) error {
	if (encryptedMetadataNonce == nil) != (encryptedMetadataEncryptedKey == nil) {
		return ErrInvalidRequest.New("EncryptedMetadataNonce and EncryptedMetadataEncryptedKey must always be set together")
	}

	hasEncryptionParams := encryptedMetadataNonce != nil && encryptedMetadataEncryptedKey != nil

	switch {
	case hasEncryptedData && !hasEncryptionParams:
		return ErrInvalidRequest.New("EncryptedMetadataNonce and EncryptedMetadataEncryptedKey must be set when EncryptedMetadata, EncryptedETag, or Checksum.EncryptedValue are set")
	case !hasEncryptedData && hasEncryptionParams:
		return ErrInvalidRequest.New("EncryptedMetadataNonce and EncryptedMetadataEncryptedKey must be empty when EncryptedMetadata, EncryptedETag, and Checksum.EncryptedValue are empty")
	}

	return nil
}

// UpdateObjectLastCommittedMetadata contains arguments necessary for replacing an object's user data.
type UpdateObjectLastCommittedMetadata struct {
	ObjectLocation
	StreamID uuid.UUID

	EncryptedUserData

	// Includes indicates which fields of the object's user data should be set. Because partially replacing
	// user data is not allowed, if the object's user data contains populated fields that are not included
	// by Includes, the operation will fail.
	Includes EncryptedUserDataIncludes
}

// EncryptedUserDataIncludes represents the parts of an object's user data that an operation should affect.
type EncryptedUserDataIncludes struct {
	// Metadata represents the part of an object's user data dedicated to storing the object's encrypted metadata.
	Metadata bool
	// Metadata represents the part of an object's user data dedicated to storing the object's encrypted ETag.
	ETag bool
	// Checksum represents the part of an object's user data dedicated to storing the object's checksum information:
	// the checksum algorithm, checksum type, and encrypted checksum value.
	Checksum bool
}

// Without returns an EncryptedUserDataIncludes that includes only what is included in the receiver
// and not included in the provided EncryptedUserDataIncludes.
func (includes EncryptedUserDataIncludes) Without(other EncryptedUserDataIncludes) EncryptedUserDataIncludes {
	return EncryptedUserDataIncludes{
		Metadata: includes.Metadata && !other.Metadata,
		ETag:     includes.ETag && !other.ETag,
		Checksum: includes.Checksum && !other.Checksum,
	}
}

// EncryptedUserDataIncludesAll returns an EncryptedUserDataIncludes indicating that the complete set
// of object metadata should be included.
func EncryptedUserDataIncludesAll() EncryptedUserDataIncludes {
	return EncryptedUserDataIncludes{
		Metadata: true,
		ETag:     true,
		Checksum: true,
	}
}

// Verify object stream fields.
func (opts *UpdateObjectLastCommittedMetadata) Verify() error {
	if err := opts.ObjectLocation.Verify(); err != nil {
		return err
	}
	if opts.StreamID.IsZero() {
		return ErrInvalidRequest.New("StreamID missing")
	}
	if err := opts.EncryptedUserData.Verify(VerifyEncryptedUserData{}); err != nil {
		return err
	}
	if opts.Includes == (EncryptedUserDataIncludes{}) {
		return ErrInvalidRequest.New("Includes is missing")
	}
	return nil
}

// UpdateObjectLastCommittedMetadata updates an object's metadata.
func (db *DB) UpdateObjectLastCommittedMetadata(ctx context.Context, opts UpdateObjectLastCommittedMetadata) (err error) {
	defer mon.Task()(&ctx)(&err)

	if err := opts.Verify(); err != nil {
		return err
	}

	err = db.ChooseAdapter(opts.ProjectID).UpdateObjectLastCommittedMetadata(ctx, opts)
	if err != nil {
		return err
	}

	mon.Meter("object_update_metadata").Mark(1)

	return nil
}

// UpdateObjectLastCommittedMetadata updates an object's metadata.
func (p *PostgresAdapter) UpdateObjectLastCommittedMetadata(ctx context.Context, opts UpdateObjectLastCommittedMetadata) (err error) {
	defer mon.Task()(&ctx)(&err)

	// TODO: So the issue is that during a multipart upload of an object,
	// uplink can update object metadata. If we add the arguments EncryptedMetadata
	// to CommitObject, they will need to account for them being optional.
	// Leading to scenarios where uplink calls update metadata, but wants to clear them
	// during commit object.

	row := p.db.QueryRowContext(ctx, `
		WITH last_committed AS (
			SELECT stream_id, version, status
			FROM objects
			WHERE
				(project_id, bucket_name, object_key) = (@project_id, @bucket_name, @object_key)
				AND status <> `+statusPending+`
				AND (expires_at IS NULL OR expires_at > now())
			ORDER BY version DESC
			LIMIT 1
		),
		updated AS (
			UPDATE objects
			SET
				`+opts.getUpdateFields()+`
			WHERE
				(project_id, bucket_name, object_key) = (@project_id, @bucket_name, @object_key)
				AND version IN (SELECT version FROM last_committed)
				AND stream_id = @stream_id
				AND status IN `+statusesCommitted+` -- Reject delete markers
				`+opts.getFormatFilter()+`
			RETURNING 1
		)
		SELECT
			(SELECT stream_id FROM last_committed),
			(SELECT status FROM last_committed),
			EXISTS(SELECT 1 FROM updated)`,
		pgx.StrictNamedArgs(opts.getQueryArgs()))

	var (
		lastCommittedStreamID uuid.NullUUID
		lastCommittedStatus   NullableObjectStatus
		updated               bool
	)
	if err := row.Scan(&lastCommittedStreamID, &lastCommittedStatus, &updated); err != nil {
		return Error.New("unable to update object metadata: %w", err)
	}

	if !updated {
		exists := lastCommittedStreamID.Valid && lastCommittedStatus.Valid
		streamIDMismatch := lastCommittedStreamID.UUID != opts.StreamID
		if !exists || streamIDMismatch || lastCommittedStatus.ObjectStatus.IsDeleteMarker() {
			return ErrObjectNotFound.New("")
		}
		return ErrInsufficientMetadataIncludes.New("the object's metadata contains populated fields not included in the provided includes")
	}

	return nil
}

// UpdateObjectLastCommittedMetadata updates an object's metadata.
func (s *SpannerAdapter) UpdateObjectLastCommittedMetadata(ctx context.Context, opts UpdateObjectLastCommittedMetadata) (err error) {
	defer mon.Task()(&ctx)(&err)

	// TODO: So the issue is that during a multipart upload of an object,
	// uplink can update object metadata. If we add the arguments EncryptedMetadata
	// to CommitObject, they will need to account for them being optional.
	// Leading to scenarios where uplink calls update metadata, but wants to clear them
	// during commit object.

	_, err = s.client.ReadWriteTransactionWithOptions(ctx, func(ctx context.Context, tx *spanner.ReadWriteTransaction) error {
		params := opts.getQueryArgs()

		iter := tx.QueryWithOptions(ctx, spanner.Statement{
			SQL: `
				SELECT stream_id, status, version
				FROM objects
				WHERE
					(project_id, bucket_name, object_key) = (@project_id, @bucket_name, @object_key)
					AND bucket_name = @bucket_name
					AND object_key = @object_key
					AND status <> ` + statusPending + `
					AND (expires_at IS NULL OR expires_at > CURRENT_TIMESTAMP)
				ORDER BY version DESC
				LIMIT 1`,
			Params: params,
		}, spanner.QueryOptions{RequestTag: "update-object-last-committed-metadata-prequery"})
		defer iter.Stop()

		row, err := iter.Next()
		if err != nil {
			if errors.Is(err, iterator.Done) {
				return ErrObjectNotFound.New("")
			}
			return errs.New("unable to query last committed object info: %w", err)
		}

		var (
			lastStreamID uuid.UUID
			lastStatus   ObjectStatus
			version      int64
		)
		if err := row.Columns(&lastStreamID, &lastStatus, &version); err != nil {
			return errs.New("unable to scan last committed object info: %w", err)
		}

		if lastStreamID != opts.StreamID || lastStatus.IsDeleteMarker() {
			return ErrObjectNotFound.New("")
		}

		params["version"] = version

		affected, err := tx.UpdateWithOptions(ctx, spanner.Statement{
			SQL: `
				UPDATE objects
				SET
					` + opts.getUpdateFields() + `
				WHERE
					(project_id, bucket_name, object_key, version) = (@project_id, @bucket_name, @object_key, @version)
					AND stream_id = @stream_id
					AND status IN ` + statusesCommitted + `
					` + opts.getFormatFilter(),
			Params: params,
		}, spanner.QueryOptions{
			RequestTag: "update-object-last-committed-metadata-update",
		})
		if err != nil {
			return errs.New("unable to update object metadata: %w", err)
		}
		if affected == 0 {
			return ErrInsufficientMetadataIncludes.New("the object's metadata contains populated fields not included in the provided includes")
		}
		return nil
	}, spanner.TransactionOptions{
		TransactionTag:              "update-object-last-committed-metadata",
		ExcludeTxnFromChangeStreams: true,
	})

	if err != nil {
		if ErrObjectNotFound.Has(err) || ErrObjectStatus.Has(err) || ErrInsufficientMetadataIncludes.Has(err) {
			return err
		}
		return Error.Wrap(err)
	}
	return nil
}

func (opts UpdateObjectLastCommittedMetadata) getQueryArgs() map[string]any {
	args := map[string]any{
		"project_id":                       opts.ProjectID,
		"bucket_name":                      opts.BucketName,
		"object_key":                       opts.ObjectKey,
		"stream_id":                        opts.StreamID,
		"encrypted_metadata_nonce":         opts.EncryptedMetadataNonce,
		"encrypted_metadata_encrypted_key": opts.EncryptedMetadataEncryptedKey,
	}

	if opts.Includes.Metadata {
		args["encrypted_metadata"] = opts.EncryptedMetadata
	}

	if opts.Includes.ETag {
		args["encrypted_etag"] = opts.EncryptedETag
	}

	if opts.Includes.Checksum {
		args["checksum"] = opts.Checksum
	}

	return args
}

func (opts UpdateObjectLastCommittedMetadata) getUpdateFields() string {
	fields := `
		encrypted_metadata_nonce         = @encrypted_metadata_nonce,
		encrypted_metadata_encrypted_key = @encrypted_metadata_encrypted_key`

	if opts.Includes.Metadata {
		fields += ", encrypted_metadata = @encrypted_metadata"
	}

	if opts.Includes.ETag {
		fields += ", encrypted_etag = @encrypted_etag"
	}

	if opts.Includes.Checksum {
		fields += ", checksum = @checksum"
	}

	return fields
}

func (opts UpdateObjectLastCommittedMetadata) getFormatFilter() string {
	var filter string

	if !opts.Includes.Metadata {
		filter += " AND encrypted_metadata IS NULL"
	}

	if !opts.Includes.ETag {
		filter += " AND (encrypted_etag IS NULL OR length(encrypted_etag) = 0)"
	}

	if !opts.Includes.Checksum {
		filter += " AND checksum IS NULL"
	}

	return filter
}
