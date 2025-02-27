// Copyright (C) 2019 Storj Labs, Inc.
// See LICENSE for copying information.

package repairer

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/ioutil"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/calebcase/tmpfile"
	"github.com/vivint/infectious"
	"github.com/zeebo/errs"
	"go.uber.org/zap"

	"storj.io/common/errs2"
	"storj.io/common/pb"
	"storj.io/common/pkcrypto"
	"storj.io/common/rpc"
	"storj.io/common/signing"
	"storj.io/common/storj"
	"storj.io/common/sync2"
	"storj.io/uplink/private/eestream"
	"storj.io/uplink/private/piecestore"
)

// ErrPieceHashVerifyFailed is the errs class when a piece hash downloaded from storagenode fails to match the original hash.
var ErrPieceHashVerifyFailed = errs.Class("piece hashes don't match")

// ECRepairer allows the repairer to download, verify, and upload pieces from storagenodes.
type ECRepairer struct {
	log             *zap.Logger
	dialer          rpc.Dialer
	satelliteSignee signing.Signee
	downloadTimeout time.Duration
	inmemory        bool
}

// NewECRepairer creates a new repairer for interfacing with storagenodes.
func NewECRepairer(log *zap.Logger, dialer rpc.Dialer, satelliteSignee signing.Signee, downloadTimeout time.Duration, inmemory bool) *ECRepairer {
	return &ECRepairer{
		log:             log,
		dialer:          dialer,
		satelliteSignee: satelliteSignee,
		downloadTimeout: downloadTimeout,
		inmemory:        inmemory,
	}
}

func (ec *ECRepairer) dialPiecestore(ctx context.Context, n storj.NodeURL) (*piecestore.Client, error) {
	return piecestore.Dial(ctx, ec.dialer, n, piecestore.DefaultConfig)
}

// Get downloads pieces from storagenodes using the provided order limits, and decodes those pieces into a segment.
// It attempts to download from the minimum required number based on the redundancy scheme.
// After downloading a piece, the ECRepairer will verify the hash and original order limit for that piece.
// If verification fails, another piece will be downloaded until we reach the minimum required or run out of order limits.
// If piece hash verification fails, it will return all failed node IDs.
func (ec *ECRepairer) Get(ctx context.Context, limits []*pb.AddressedOrderLimit, cachedIPsAndPorts map[storj.NodeID]string, privateKey storj.PiecePrivateKey, es eestream.ErasureScheme, dataSize int64) (_ io.ReadCloser, failedPieces []*pb.RemotePiece, err error) {
	defer mon.Task()(&ctx)(&err)

	if len(limits) != es.TotalCount() {
		return nil, nil, Error.New("number of limits slice (%d) does not match total count (%d) of erasure scheme", len(limits), es.TotalCount())
	}

	nonNilLimits := nonNilCount(limits)

	if nonNilLimits < es.RequiredCount() {
		return nil, nil, Error.New("number of non-nil limits (%d) is less than required count (%d) of erasure scheme", nonNilCount(limits), es.RequiredCount())
	}

	pieceSize := eestream.CalcPieceSize(dataSize, es)

	var successfulPieces, inProgress int
	unusedLimits := nonNilLimits
	pieceReaders := make(map[int]io.ReadCloser)

	limiter := sync2.NewLimiter(es.RequiredCount())
	cond := sync.NewCond(&sync.Mutex{})

	var errlist errs.Group
	var mu sync.Mutex

	for currentLimitIndex, limit := range limits {
		if limit == nil {
			continue
		}

		currentLimitIndex, limit := currentLimitIndex, limit
		limiter.Go(ctx, func() {
			cond.L.Lock()
			defer cond.Signal()
			defer cond.L.Unlock()

			for {
				if successfulPieces >= es.RequiredCount() {
					// already downloaded minimum number of pieces
					cond.Broadcast()
					return
				}
				if successfulPieces+inProgress+unusedLimits < es.RequiredCount() {
					// not enough available limits left to get required number of pieces
					cond.Broadcast()
					return
				}

				if successfulPieces+inProgress >= es.RequiredCount() {
					cond.Wait()
					continue
				}

				unusedLimits--
				inProgress++
				cond.L.Unlock()

				lastIPPort := cachedIPsAndPorts[limit.GetLimit().StorageNodeId]
				address := limit.GetStorageNodeAddress().GetAddress()
				var triedLastIPPort bool
				if lastIPPort != "" && lastIPPort != address {
					address = lastIPPort
					triedLastIPPort = true
				}

				pieceReadCloser, err := ec.downloadAndVerifyPiece(ctx, limit, address, privateKey, pieceSize)

				// if piecestore dial with last ip:port failed try again with node address
				if triedLastIPPort && piecestore.Error.Has(err) {
					pieceReadCloser, err = ec.downloadAndVerifyPiece(ctx, limit, limit.GetStorageNodeAddress().GetAddress(), privateKey, pieceSize)
				}
				cond.L.Lock()
				inProgress--
				if err != nil {
					// gather nodes where the calculated piece hash doesn't match the uplink signed piece hash
					if ErrPieceHashVerifyFailed.Has(err) {
						ec.log.Info("audit failed", zap.Stringer("node ID", limit.GetLimit().StorageNodeId),
							zap.String("reason", err.Error()))
						failedPieces = append(failedPieces, &pb.RemotePiece{
							PieceNum: int32(currentLimitIndex),
							NodeId:   limit.GetLimit().StorageNodeId,
						})
					} else {
						ec.log.Debug("Failed to download pieces for repair",
							zap.Error(err))
					}
					mu.Lock()
					errlist.Add(fmt.Errorf("node id: %s, error: %w", limit.GetLimit().StorageNodeId.String(), err))
					mu.Unlock()
					return
				}

				pieceReaders[currentLimitIndex] = pieceReadCloser
				successfulPieces++

				return
			}
		})
	}

	limiter.Wait()

	if successfulPieces < es.RequiredCount() {
		mon.Meter("download_failed_not_enough_pieces_repair").Mark(1) //mon:locked
		return nil, failedPieces, &irreparableError{
			piecesAvailable: int32(successfulPieces),
			piecesRequired:  int32(es.RequiredCount()),
			errlist:         errlist,
		}
	}

	fec, err := infectious.NewFEC(es.RequiredCount(), es.TotalCount())
	if err != nil {
		return nil, failedPieces, Error.Wrap(err)
	}

	esScheme := eestream.NewUnsafeRSScheme(fec, es.ErasureShareSize())
	expectedSize := pieceSize * int64(es.RequiredCount())

	ctx, cancel := context.WithCancel(ctx)
	decodeReader := eestream.DecodeReaders2(ctx, cancel, pieceReaders, esScheme, expectedSize, 0, false)

	return decodeReader, failedPieces, nil
}

// downloadAndVerifyPiece downloads a piece from a storagenode,
// expects the original order limit to have the correct piece public key,
// and expects the hash of the data to match the signed hash provided by the storagenode.
func (ec *ECRepairer) downloadAndVerifyPiece(ctx context.Context, limit *pb.AddressedOrderLimit, address string, privateKey storj.PiecePrivateKey, pieceSize int64) (pieceReadCloser io.ReadCloser, err error) {
	defer mon.Task()(&ctx)(&err)

	// contact node
	downloadCtx, cancel := context.WithTimeout(ctx, ec.downloadTimeout)
	defer cancel()

	ps, err := ec.dialPiecestore(downloadCtx, storj.NodeURL{
		ID:      limit.GetLimit().StorageNodeId,
		Address: address,
	})
	if err != nil {
		return nil, err
	}
	defer func() { err = errs.Combine(err, ps.Close()) }()

	downloader, err := ps.Download(downloadCtx, limit.GetLimit(), privateKey, 0, pieceSize)
	if err != nil {
		return nil, err
	}
	defer func() { err = errs.Combine(err, downloader.Close()) }()

	hashWriter := pkcrypto.NewHash()
	downloadReader := io.TeeReader(downloader, hashWriter)
	var downloadedPieceSize int64

	if ec.inmemory {
		pieceBytes, err := ioutil.ReadAll(downloadReader)
		if err != nil {
			return nil, err
		}
		downloadedPieceSize = int64(len(pieceBytes))
		pieceReadCloser = ioutil.NopCloser(bytes.NewReader(pieceBytes))
	} else {
		tempfile, err := tmpfile.New("", "satellite-repair-*")
		if err != nil {
			return nil, err
		}
		defer func() {
			// close and remove file if there is some error
			if err != nil {
				err = errs.Combine(err, tempfile.Close())
			}
		}()

		downloadedPieceSize, err = io.Copy(tempfile, downloadReader)
		if err != nil {
			return nil, err
		}

		// seek to beginning of file so the repair job starts at the beginning of the piece
		_, err = tempfile.Seek(0, io.SeekStart)
		if err != nil {
			return nil, err
		}
		pieceReadCloser = tempfile
	}

	mon.Meter("repair_bytes_downloaded").Mark64(downloadedPieceSize) //mon:locked

	if downloadedPieceSize != pieceSize {
		return nil, Error.New("didn't download the correct amount of data, want %d, got %d", pieceSize, downloadedPieceSize)
	}

	// get signed piece hash and original order limit
	hash, originalLimit := downloader.GetHashAndLimit()
	if hash == nil {
		return nil, Error.New("hash was not sent from storagenode")
	}
	if originalLimit == nil {
		return nil, Error.New("original order limit was not sent from storagenode")
	}

	// verify order limit from storage node is signed by the satellite
	if err := verifyOrderLimitSignature(ctx, ec.satelliteSignee, originalLimit); err != nil {
		return nil, err
	}

	// verify the hashes from storage node
	calculatedHash := hashWriter.Sum(nil)
	if err := verifyPieceHash(ctx, originalLimit, hash, calculatedHash); err != nil {

		return nil, ErrPieceHashVerifyFailed.Wrap(err)
	}

	return pieceReadCloser, nil
}

func verifyPieceHash(ctx context.Context, limit *pb.OrderLimit, hash *pb.PieceHash, expectedHash []byte) (err error) {
	defer mon.Task()(&ctx)(&err)

	if limit == nil || hash == nil || len(expectedHash) == 0 {
		return Error.New("invalid arguments")
	}
	if limit.PieceId != hash.PieceId {
		return Error.New("piece id changed")
	}
	if !bytes.Equal(hash.Hash, expectedHash) {
		return Error.New("hash from storage node, %x, does not match calculated hash, %x", hash.Hash, expectedHash)
	}

	if err := signing.VerifyUplinkPieceHashSignature(ctx, limit.UplinkPublicKey, hash); err != nil {
		return Error.New("invalid piece hash signature")
	}

	return nil
}

func verifyOrderLimitSignature(ctx context.Context, satellite signing.Signee, limit *pb.OrderLimit) (err error) {
	if err := signing.VerifyOrderLimitSignature(ctx, satellite, limit); err != nil {
		return Error.New("invalid order limit signature: %v", err)
	}

	return nil
}

// Repair takes a provided segment, encodes it with the provided redundancy strategy,
// and uploads the pieces in need of repair to new nodes provided by order limits.
func (ec *ECRepairer) Repair(ctx context.Context, limits []*pb.AddressedOrderLimit, privateKey storj.PiecePrivateKey, rs eestream.RedundancyStrategy, data io.Reader, timeout time.Duration, successfulNeeded int) (successfulNodes []*pb.Node, successfulHashes []*pb.PieceHash, err error) {
	defer mon.Task()(&ctx)(&err)

	pieceCount := len(limits)
	if pieceCount != rs.TotalCount() {
		return nil, nil, Error.New("size of limits slice (%d) does not match total count (%d) of erasure scheme", pieceCount, rs.TotalCount())
	}

	if !unique(limits) {
		return nil, nil, Error.New("duplicated nodes are not allowed")
	}

	readers, err := eestream.EncodeReader2(ctx, ioutil.NopCloser(data), rs)
	if err != nil {
		return nil, nil, err
	}

	// info contains data about a single piece transfer
	type info struct {
		i    int
		err  error
		hash *pb.PieceHash
	}
	// this channel is used to synchronize concurrently uploaded pieces with the overall repair
	infos := make(chan info, pieceCount)

	psCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	for i, addressedLimit := range limits {
		go func(i int, addressedLimit *pb.AddressedOrderLimit) {
			hash, err := ec.putPiece(psCtx, ctx, addressedLimit, privateKey, readers[i])
			infos <- info{i: i, err: err, hash: hash}
		}(i, addressedLimit)
	}
	ec.log.Debug("Starting a timer for repair so that the number of pieces will be closer to the success threshold",
		zap.Duration("Timer", timeout),
		zap.Int("Node Count", nonNilCount(limits)),
		zap.Int("Optimal Threshold", rs.OptimalThreshold()),
	)

	var successfulCount, failureCount, cancellationCount int32
	timer := time.AfterFunc(timeout, func() {
		if !errors.Is(ctx.Err(), context.Canceled) {
			ec.log.Debug("Timer expired. Canceling the long tail...",
				zap.Int32("Successfully repaired", atomic.LoadInt32(&successfulCount)),
			)
			cancel()
		}
	})

	successfulNodes = make([]*pb.Node, pieceCount)
	successfulHashes = make([]*pb.PieceHash, pieceCount)

	for range limits {
		info := <-infos

		if limits[info.i] == nil {
			continue
		}

		if info.err != nil {
			if !errs2.IsCanceled(info.err) {
				failureCount++
				ec.log.Warn("Repair to a storage node failed",
					zap.Stringer("Node ID", limits[info.i].GetLimit().StorageNodeId),
					zap.Error(info.err),
				)
			} else {
				cancellationCount++
				ec.log.Debug("Repair to storage node cancelled",
					zap.Stringer("Node ID", limits[info.i].GetLimit().StorageNodeId),
					zap.Error(info.err),
				)
			}
			continue
		}

		successfulNodes[info.i] = &pb.Node{
			Id:      limits[info.i].GetLimit().StorageNodeId,
			Address: limits[info.i].GetStorageNodeAddress(),
		}
		successfulHashes[info.i] = info.hash
		successfulCount++

		if successfulCount >= int32(successfulNeeded) {
			ec.log.Debug("Number of successful uploads met. Canceling the long tail...",
				zap.Int32("Successfully repaired", atomic.LoadInt32(&successfulCount)),
			)
			cancel()
		}
	}

	// Ensure timer is stopped
	_ = timer.Stop()

	// TODO: clean up the partially uploaded segment's pieces
	defer func() {
		select {
		case <-ctx.Done():
			err = Error.New("repair cancelled")
		default:
		}
	}()

	if successfulCount == 0 {
		return nil, nil, Error.New("repair to all nodes failed")
	}

	ec.log.Debug("Successfully repaired",
		zap.Int32("Success Count", atomic.LoadInt32(&successfulCount)),
	)

	mon.IntVal("repair_segment_pieces_total").Observe(int64(pieceCount))           //mon:locked
	mon.IntVal("repair_segment_pieces_successful").Observe(int64(successfulCount)) //mon:locked
	mon.IntVal("repair_segment_pieces_failed").Observe(int64(failureCount))        //mon:locked
	mon.IntVal("repair_segment_pieces_canceled").Observe(int64(cancellationCount)) //mon:locked

	return successfulNodes, successfulHashes, nil
}

func (ec *ECRepairer) putPiece(ctx, parent context.Context, limit *pb.AddressedOrderLimit, privateKey storj.PiecePrivateKey, data io.ReadCloser) (hash *pb.PieceHash, err error) {
	defer mon.Task()(&ctx)(&err)

	nodeName := "nil"
	if limit != nil {
		nodeName = limit.GetLimit().StorageNodeId.String()[0:8]
	}
	defer mon.Task()(&ctx, "node: "+nodeName)(&err)
	defer func() { err = errs.Combine(err, data.Close()) }()

	if limit == nil {
		_, _ = io.Copy(ioutil.Discard, data)
		return nil, nil
	}

	storageNodeID := limit.GetLimit().StorageNodeId
	pieceID := limit.GetLimit().PieceId
	ps, err := ec.dialPiecestore(ctx, storj.NodeURL{
		ID:      storageNodeID,
		Address: limit.GetStorageNodeAddress().Address,
	})
	if err != nil {
		ec.log.Debug("Failed dialing for putting piece to node",
			zap.Stringer("Piece ID", pieceID),
			zap.Stringer("Node ID", storageNodeID),
			zap.Error(err),
		)
		return nil, err
	}
	defer func() { err = errs.Combine(err, ps.Close()) }()

	hash, err = ps.UploadReader(ctx, limit.GetLimit(), privateKey, data)
	if err != nil {
		if errors.Is(ctx.Err(), context.Canceled) {
			// Canceled context means the piece upload was interrupted by user or due
			// to slow connection. No error logging for this case.
			if errors.Is(parent.Err(), context.Canceled) {
				ec.log.Debug("Upload to node canceled by user",
					zap.Stringer("Node ID", storageNodeID))
			} else {
				ec.log.Debug("Node cut from upload due to slow connection",
					zap.Stringer("Node ID", storageNodeID))
			}

			// make sure context.Canceled is the primary error in the error chain
			// for later errors.Is/errs2.IsCanceled checking
			err = errs.Combine(context.Canceled, err)
		} else {
			nodeAddress := "nil"
			if limit.GetStorageNodeAddress() != nil {
				nodeAddress = limit.GetStorageNodeAddress().GetAddress()
			}

			ec.log.Debug("Failed uploading piece to node",
				zap.Stringer("Piece ID", pieceID),
				zap.Stringer("Node ID", storageNodeID),
				zap.String("Node Address", nodeAddress),
				zap.Error(err),
			)
		}
	}

	return hash, err
}

func nonNilCount(limits []*pb.AddressedOrderLimit) int {
	total := 0
	for _, limit := range limits {
		if limit != nil {
			total++
		}
	}
	return total
}

func unique(limits []*pb.AddressedOrderLimit) bool {
	if len(limits) < 2 {
		return true
	}
	ids := make(storj.NodeIDList, len(limits))
	for i, addressedLimit := range limits {
		if addressedLimit != nil {
			ids[i] = addressedLimit.GetLimit().StorageNodeId
		}
	}

	// sort the ids and check for identical neighbors
	sort.Sort(ids)
	// sort.Slice(ids, func(i, k int) bool { return ids[i].Less(ids[k]) })
	for i := 1; i < len(ids); i++ {
		if ids[i] != (storj.NodeID{}) && ids[i] == ids[i-1] {
			return false
		}
	}

	return true
}
