// Copyright (C) 2019 Storj Labs, Inc.
// See LICENSE for copying information.

package orders

import (
	"bytes"
	"context"
	"math"
	mathrand "math/rand"
	"sync"
	"time"

	"github.com/zeebo/errs"
	"go.uber.org/zap"

	"storj.io/common/pb"
	"storj.io/common/signing"
	"storj.io/common/storj"
	"storj.io/common/uuid"
	"storj.io/storj/satellite/overlay"
	"storj.io/uplink/private/eestream"
)

// ErrDownloadFailedNotEnoughPieces is returned when download failed due to missing pieces.
var ErrDownloadFailedNotEnoughPieces = errs.Class("not enough pieces for download")

// Config is a configuration struct for orders Service.
type Config struct {
	Expiration                   time.Duration              `help:"how long until an order expires" default:"48h"` // 2 days
	SettlementBatchSize          int                        `help:"how many orders to batch per transaction" default:"250"`
	FlushBatchSize               int                        `help:"how many items in the rollups write cache before they are flushed to the database" devDefault:"20" releaseDefault:"10000"`
	FlushInterval                time.Duration              `help:"how often to flush the rollups write cache to the database" devDefault:"30s" releaseDefault:"1m"`
	ReportedRollupsReadBatchSize int                        `help:"how many records to read in a single transaction when calculating billable bandwidth" default:"1000"`
	NodeStatusLogging            bool                       `hidden:"true" help:"deprecated, log the offline/disqualification status of nodes" default:"false"`
	WindowEndpointRolloutPhase   WindowEndpointRolloutPhase `help:"rollout phase for the windowed endpoint" default:"phase1"`
}

// BucketsDB returns information about buckets.
type BucketsDB interface {
	// GetBucketID returns an existing bucket id.
	GetBucketID(ctx context.Context, bucketName []byte, projectID uuid.UUID) (id uuid.UUID, err error)
}

// Service for creating order limits.
//
// architecture: Service
type Service struct {
	log              *zap.Logger
	satellite        signing.Signer
	overlay          *overlay.Service
	orders           DB
	buckets          BucketsDB
	satelliteAddress *pb.NodeAddress
	orderExpiration  time.Duration
	rngMu            sync.Mutex
	rng              *mathrand.Rand
}

// NewService creates new service for creating order limits.
func NewService(
	log *zap.Logger, satellite signing.Signer, overlay *overlay.Service,
	orders DB, buckets BucketsDB,
	orderExpiration time.Duration, satelliteAddress *pb.NodeAddress,
) *Service {
	return &Service{
		log:              log,
		satellite:        satellite,
		overlay:          overlay,
		orders:           orders,
		buckets:          buckets,
		satelliteAddress: satelliteAddress,
		orderExpiration:  orderExpiration,

		rng: mathrand.New(mathrand.NewSource(time.Now().UnixNano())),
	}
}

// VerifyOrderLimitSignature verifies that the signature inside order limit belongs to the satellite.
func (service *Service) VerifyOrderLimitSignature(ctx context.Context, signed *pb.OrderLimit) (err error) {
	defer mon.Task()(&ctx)(&err)
	return signing.VerifyOrderLimitSignature(ctx, service.satellite, signed)
}

func (service *Service) saveSerial(ctx context.Context, serialNumber storj.SerialNumber, bucketID []byte, expiresAt time.Time) (err error) {
	defer mon.Task()(&ctx)(&err)
	return service.orders.CreateSerialInfo(ctx, serialNumber, bucketID, expiresAt)
}

func (service *Service) updateBandwidth(ctx context.Context, projectID uuid.UUID, bucketName []byte, addressedOrderLimits ...*pb.AddressedOrderLimit) (err error) {
	defer mon.Task()(&ctx)(&err)
	if len(addressedOrderLimits) == 0 {
		return nil
	}

	var action pb.PieceAction

	var bucketAllocation int64

	for _, addressedOrderLimit := range addressedOrderLimits {
		if addressedOrderLimit != nil && addressedOrderLimit.Limit != nil {
			orderLimit := addressedOrderLimit.Limit
			action = orderLimit.Action
			bucketAllocation += orderLimit.Limit
		}
	}

	now := time.Now().UTC()
	intervalStart := time.Date(now.Year(), now.Month(), now.Day(), now.Hour(), 0, 0, 0, now.Location())

	// TODO: all of this below should be a single db transaction. in fact, this whole function should probably be part of an existing transaction
	if err := service.orders.UpdateBucketBandwidthAllocation(ctx, projectID, bucketName, action, bucketAllocation, intervalStart); err != nil {
		return Error.Wrap(err)
	}

	return nil
}

// CreateGetOrderLimits creates the order limits for downloading the pieces of pointer.
func (service *Service) CreateGetOrderLimits(ctx context.Context, bucketID []byte, pointer *pb.Pointer) (_ []*pb.AddressedOrderLimit, privateKey storj.PiecePrivateKey, err error) {
	defer mon.Task()(&ctx)(&err)

	rootPieceID := pointer.GetRemote().RootPieceId
	pieceExpiration := pointer.ExpirationDate
	orderExpiration := time.Now().Add(service.orderExpiration)

	piecePublicKey, piecePrivateKey, err := storj.NewPieceKey()
	if err != nil {
		return nil, storj.PiecePrivateKey{}, Error.Wrap(err)
	}

	serialNumber, err := createSerial(orderExpiration)
	if err != nil {
		return nil, storj.PiecePrivateKey{}, Error.Wrap(err)
	}

	redundancy, err := eestream.NewRedundancyStrategyFromProto(pointer.GetRemote().GetRedundancy())
	if err != nil {
		return nil, storj.PiecePrivateKey{}, Error.Wrap(err)
	}

	pieceSize := eestream.CalcPieceSize(pointer.GetSegmentSize(), redundancy)

	nodeIDs := make([]storj.NodeID, len(pointer.GetRemote().GetRemotePieces()))
	for i, piece := range pointer.GetRemote().GetRemotePieces() {
		nodeIDs[i] = piece.NodeId
	}

	nodes, err := service.overlay.GetOnlineNodesForGetDelete(ctx, nodeIDs)
	if err != nil {
		service.log.Debug("error getting nodes from overlay", zap.Error(err))
		return nil, storj.PiecePrivateKey{}, Error.Wrap(err)
	}

	var nodeErrors errs.Group
	var limits []*pb.AddressedOrderLimit
	for _, piece := range pointer.GetRemote().GetRemotePieces() {
		node, ok := nodes[piece.NodeId]
		if !ok {
			nodeErrors.Add(errs.New("node %q is not reliable", piece.NodeId))
			continue
		}

		orderLimit := &pb.OrderLimit{
			SerialNumber:     serialNumber,
			SatelliteId:      service.satellite.ID(),
			SatelliteAddress: service.satelliteAddress,
			UplinkPublicKey:  piecePublicKey,
			StorageNodeId:    piece.NodeId,
			PieceId:          rootPieceID.Derive(piece.NodeId, piece.PieceNum),
			Action:           pb.PieceAction_GET,
			Limit:            pieceSize,
			PieceExpiration:  pieceExpiration,
			OrderCreation:    time.Now(),
			OrderExpiration:  orderExpiration,
		}

		// use the lastIP that we have on record to avoid doing extra DNS resolutions
		if node.LastIPPort != "" {
			node.Address.Address = node.LastIPPort
		}
		limits = append(limits, &pb.AddressedOrderLimit{
			Limit:              orderLimit,
			StorageNodeAddress: node.Address,
		})
	}

	if len(limits) < redundancy.RequiredCount() {
		mon.Meter("download_failed_not_enough_pieces_uplink").Mark(1) //locked
		err = Error.New("not enough nodes available: got %d, required %d", len(limits), redundancy.RequiredCount())
		return nil, storj.PiecePrivateKey{}, ErrDownloadFailedNotEnoughPieces.Wrap(errs.Combine(err, nodeErrors.Err()))
	}

	err = service.saveSerial(ctx, serialNumber, bucketID, orderExpiration)
	if err != nil {
		return nil, storj.PiecePrivateKey{}, Error.Wrap(err)
	}

	neededLimits := pb.NewRedundancySchemeToStorj(pointer.GetRemote().GetRedundancy()).DownloadNodes()
	if int(neededLimits) < redundancy.RequiredCount() {
		err = Error.New("not enough needed node orderlimits: got %d, required %d", neededLimits, redundancy.RequiredCount())
		return nil, storj.PiecePrivateKey{}, ErrDownloadFailedNotEnoughPieces.Wrap(errs.Combine(err, nodeErrors.Err()))
	}
	// an orderLimit was created for each piece, but lets only use
	// the number of orderLimits actually needed to do the download
	limits, err = service.RandomSampleOfOrderLimits(limits, int(neededLimits))
	if err != nil {
		return nil, storj.PiecePrivateKey{}, Error.Wrap(err)
	}

	for i, limit := range limits {
		if limit == nil {
			continue
		}
		orderLimit, err := signing.SignOrderLimit(ctx, service.satellite, limit.Limit)
		if err != nil {
			return nil, storj.PiecePrivateKey{}, Error.Wrap(err)
		}
		limits[i].Limit = orderLimit
	}
	projectID, bucketName, err := SplitBucketID(bucketID)
	if err != nil {
		return nil, storj.PiecePrivateKey{}, Error.Wrap(err)
	}
	if err := service.updateBandwidth(ctx, projectID, bucketName, limits...); err != nil {
		return nil, storj.PiecePrivateKey{}, Error.Wrap(err)
	}

	return limits, piecePrivateKey, nil
}

// RandomSampleOfOrderLimits returns a random sample of the order limits.
func (service *Service) RandomSampleOfOrderLimits(limits []*pb.AddressedOrderLimit, sampleSize int) ([]*pb.AddressedOrderLimit, error) {
	service.rngMu.Lock()
	perm := service.rng.Perm(len(limits))
	service.rngMu.Unlock()

	// the sample slice is the same size as the limits slice since that represents all
	// of the pieces of a pointer in the correct order and we want to maintain the order
	var sample = make([]*pb.AddressedOrderLimit, len(limits))
	for _, i := range perm {
		limit := limits[i]
		sample[i] = limit

		sampleSize--
		if sampleSize <= 0 {
			break
		}
	}
	return sample, nil
}

// CreatePutOrderLimits creates the order limits for uploading pieces to nodes.
func (service *Service) CreatePutOrderLimits(ctx context.Context, bucketID []byte, nodes []*overlay.SelectedNode, pieceExpiration time.Time, maxPieceSize int64) (_ storj.PieceID, _ []*pb.AddressedOrderLimit, privateKey storj.PiecePrivateKey, err error) {
	defer mon.Task()(&ctx)(&err)

	orderCreation := time.Now()
	orderExpiration := orderCreation.Add(service.orderExpiration)

	signer, err := NewSignerPut(service, pieceExpiration, orderCreation, orderExpiration, maxPieceSize)
	if err != nil {
		return storj.PieceID{}, nil, storj.PiecePrivateKey{}, Error.Wrap(err)
	}

	for pieceNum, node := range nodes {
		address := node.Address.Address
		if node.LastIPPort != "" {
			address = node.LastIPPort
		}
		_, err := signer.Sign(ctx, storj.NodeURL{ID: node.ID, Address: address}, int32(pieceNum))
		if err != nil {
			return storj.PieceID{}, nil, storj.PiecePrivateKey{}, Error.Wrap(err)
		}
	}

	err = service.saveSerial(ctx, signer.Serial, bucketID, signer.OrderExpiration)
	if err != nil {
		return storj.PieceID{}, nil, storj.PiecePrivateKey{}, Error.Wrap(err)
	}

	projectID, bucketName, err := SplitBucketID(bucketID)
	if err != nil {
		return storj.PieceID{}, nil, storj.PiecePrivateKey{}, Error.Wrap(err)
	}
	if err := service.updateBandwidth(ctx, projectID, bucketName, signer.AddressedLimits...); err != nil {
		return storj.PieceID{}, nil, storj.PiecePrivateKey{}, Error.Wrap(err)
	}

	return signer.RootPieceID, signer.AddressedLimits, signer.PrivateKey, nil
}

// CreateDeleteOrderLimits creates the order limits for deleting the pieces of pointer.
func (service *Service) CreateDeleteOrderLimits(ctx context.Context, bucketID []byte, pointer *pb.Pointer) (_ []*pb.AddressedOrderLimit, _ storj.PiecePrivateKey, err error) {
	defer mon.Task()(&ctx)(&err)

	nodeIDs := make([]storj.NodeID, len(pointer.GetRemote().GetRemotePieces()))
	for i, piece := range pointer.GetRemote().GetRemotePieces() {
		nodeIDs[i] = piece.NodeId
	}

	nodes, err := service.overlay.GetOnlineNodesForGetDelete(ctx, nodeIDs)
	if err != nil {
		service.log.Debug("error getting nodes from overlay", zap.Error(err))
		return nil, storj.PiecePrivateKey{}, Error.Wrap(err)
	}

	orderCreation := time.Now()
	orderExpiration := orderCreation.Add(service.orderExpiration)

	signer, err := NewSignerDelete(service, pointer.GetRemote().RootPieceId, pointer.ExpirationDate, orderCreation, orderExpiration)
	if err != nil {
		return nil, storj.PiecePrivateKey{}, Error.Wrap(err)
	}

	var nodeErrors errs.Group
	for _, piece := range pointer.GetRemote().GetRemotePieces() {
		node, ok := nodes[piece.NodeId]
		if !ok {
			nodeErrors.Add(errs.New("node %q is not reliable", piece.NodeId))
			continue
		}

		address := node.Address.Address
		if node.LastIPPort != "" {
			address = node.LastIPPort
		}
		_, err := signer.Sign(ctx, storj.NodeURL{ID: piece.NodeId, Address: address}, piece.PieceNum)
		if err != nil {
			return nil, storj.PiecePrivateKey{}, Error.Wrap(err)
		}
	}

	if len(signer.AddressedLimits) == 0 {
		return nil, storj.PiecePrivateKey{}, Error.New("failed creating order limits: %w", nodeErrors.Err())
	}

	err = service.saveSerial(ctx, signer.Serial, bucketID, signer.OrderExpiration)
	if err != nil {
		return nil, storj.PiecePrivateKey{}, Error.Wrap(err)
	}

	return signer.AddressedLimits, signer.PrivateKey, nil
}

// CreateAuditOrderLimits creates the order limits for auditing the pieces of pointer.
func (service *Service) CreateAuditOrderLimits(ctx context.Context, bucketID []byte, pointer *pb.Pointer, skip map[storj.NodeID]bool) (_ []*pb.AddressedOrderLimit, _ storj.PiecePrivateKey, err error) {
	defer mon.Task()(&ctx)(&err)

	redundancy := pointer.GetRemote().GetRedundancy()
	shareSize := redundancy.GetErasureShareSize()
	totalPieces := redundancy.GetTotal()

	orderCreation := time.Now()
	orderExpiration := orderCreation.Add(service.orderExpiration)

	nodeIDs := make([]storj.NodeID, len(pointer.GetRemote().GetRemotePieces()))
	for i, piece := range pointer.GetRemote().GetRemotePieces() {
		nodeIDs[i] = piece.NodeId
	}

	nodes, err := service.overlay.GetOnlineNodesForGetDelete(ctx, nodeIDs)
	if err != nil {
		service.log.Debug("error getting nodes from overlay", zap.Error(err))
		return nil, storj.PiecePrivateKey{}, Error.Wrap(err)
	}

	signer, err := NewSignerAudit(service, pointer.GetRemote().RootPieceId, orderCreation, orderExpiration, int64(shareSize))
	if err != nil {
		return nil, storj.PiecePrivateKey{}, Error.Wrap(err)
	}

	var nodeErrors errs.Group
	var limitsCount int32
	limits := make([]*pb.AddressedOrderLimit, totalPieces)
	for _, piece := range pointer.GetRemote().GetRemotePieces() {
		if skip[piece.NodeId] {
			continue
		}
		node, ok := nodes[piece.NodeId]
		if !ok {
			nodeErrors.Add(errs.New("node %q is not reliable", piece.NodeId))
			continue
		}

		limit, err := signer.Sign(ctx, storj.NodeURL{
			ID:      piece.NodeId,
			Address: node.Address.Address,
		}, piece.PieceNum)
		if err != nil {
			return nil, storj.PiecePrivateKey{}, Error.Wrap(err)
		}

		limits[piece.GetPieceNum()] = limit
		limitsCount++
	}

	if limitsCount < redundancy.GetMinReq() {
		err = Error.New("not enough nodes available: got %d, required %d", limitsCount, redundancy.GetMinReq())
		return nil, storj.PiecePrivateKey{}, errs.Combine(err, nodeErrors.Err())
	}

	err = service.saveSerial(ctx, signer.Serial, bucketID, signer.OrderExpiration)
	if err != nil {
		return nil, storj.PiecePrivateKey{}, Error.Wrap(err)
	}

	projectID, bucketName, err := SplitBucketID(bucketID)
	if err != nil {
		return limits, storj.PiecePrivateKey{}, Error.Wrap(err)
	}
	if err := service.updateBandwidth(ctx, projectID, bucketName, limits...); err != nil {
		return nil, storj.PiecePrivateKey{}, Error.Wrap(err)
	}

	return limits, signer.PrivateKey, nil
}

// CreateAuditOrderLimit creates an order limit for auditing a single the piece from a pointer.
func (service *Service) CreateAuditOrderLimit(ctx context.Context, bucketID []byte, nodeID storj.NodeID, pieceNum int32, rootPieceID storj.PieceID, shareSize int32) (limit *pb.AddressedOrderLimit, _ storj.PiecePrivateKey, err error) {
	// TODO reduce number of params ?
	defer mon.Task()(&ctx)(&err)

	orderCreation := time.Now()
	orderExpiration := orderCreation.Add(service.orderExpiration)

	node, err := service.overlay.Get(ctx, nodeID)
	if err != nil {
		return nil, storj.PiecePrivateKey{}, Error.Wrap(err)
	}
	if node.Disqualified != nil {
		return nil, storj.PiecePrivateKey{}, overlay.ErrNodeDisqualified.New("%v", nodeID)
	}
	if node.ExitStatus.ExitFinishedAt != nil {
		return nil, storj.PiecePrivateKey{}, overlay.ErrNodeFinishedGE.New("%v", nodeID)
	}
	if !service.overlay.IsOnline(node) {
		return nil, storj.PiecePrivateKey{}, overlay.ErrNodeOffline.New("%v", nodeID)
	}

	signer, err := NewSignerAudit(service, rootPieceID, orderCreation, orderExpiration, int64(shareSize))
	if err != nil {
		return nil, storj.PiecePrivateKey{}, Error.Wrap(err)
	}

	orderLimit, err := signer.Sign(ctx, storj.NodeURL{
		ID:      nodeID,
		Address: node.Address.Address,
	}, pieceNum)
	if err != nil {
		return nil, storj.PiecePrivateKey{}, Error.Wrap(err)
	}

	err = service.saveSerial(ctx, signer.Serial, bucketID, signer.OrderExpiration)
	if err != nil {
		return nil, storj.PiecePrivateKey{}, Error.Wrap(err)
	}

	projectID, bucketName, err := SplitBucketID(bucketID)
	if err != nil {
		return orderLimit, storj.PiecePrivateKey{}, Error.Wrap(err)
	}
	if err := service.updateBandwidth(ctx, projectID, bucketName, limit); err != nil {
		return nil, storj.PiecePrivateKey{}, Error.Wrap(err)
	}

	return orderLimit, signer.PrivateKey, nil
}

// CreateGetRepairOrderLimits creates the order limits for downloading the
// healthy pieces of pointer as the source for repair.
//
// The length of the returned orders slice is the total number of pieces of the
// segment, setting to null the ones which don't correspond to a healthy piece.
// CreateGetRepairOrderLimits creates the order limits for downloading the healthy pieces of pointer as the source for repair.
func (service *Service) CreateGetRepairOrderLimits(ctx context.Context, bucketID []byte, pointer *pb.Pointer, healthy []*pb.RemotePiece) (_ []*pb.AddressedOrderLimit, _ storj.PiecePrivateKey, err error) {
	defer mon.Task()(&ctx)(&err)

	redundancy, err := eestream.NewRedundancyStrategyFromProto(pointer.GetRemote().GetRedundancy())
	if err != nil {
		return nil, storj.PiecePrivateKey{}, Error.Wrap(err)
	}

	pieceSize := eestream.CalcPieceSize(pointer.GetSegmentSize(), redundancy)
	totalPieces := redundancy.TotalCount()
	orderCreation := time.Now()
	orderExpiration := orderCreation.Add(service.orderExpiration)

	nodeIDs := make([]storj.NodeID, len(pointer.GetRemote().GetRemotePieces()))
	for i, piece := range pointer.GetRemote().GetRemotePieces() {
		nodeIDs[i] = piece.NodeId
	}

	nodes, err := service.overlay.GetOnlineNodesForGetDelete(ctx, nodeIDs)
	if err != nil {
		service.log.Debug("error getting nodes from overlay", zap.Error(err))
		return nil, storj.PiecePrivateKey{}, Error.Wrap(err)
	}

	signer, err := NewSignerRepairGet(service, pointer.GetRemote().RootPieceId, pointer.ExpirationDate, orderCreation, orderExpiration, pieceSize)
	if err != nil {
		return nil, storj.PiecePrivateKey{}, Error.Wrap(err)
	}

	var nodeErrors errs.Group
	var limitsCount int
	limits := make([]*pb.AddressedOrderLimit, totalPieces)
	for _, piece := range healthy {
		node, ok := nodes[piece.NodeId]
		if !ok {
			nodeErrors.Add(errs.New("node %q is not reliable", piece.NodeId))
			continue
		}

		limit, err := signer.Sign(ctx, storj.NodeURL{
			ID:      piece.NodeId,
			Address: node.Address.Address,
		}, piece.PieceNum)
		if err != nil {
			return nil, storj.PiecePrivateKey{}, Error.Wrap(err)
		}

		limits[piece.GetPieceNum()] = limit
		limitsCount++
	}

	if limitsCount < redundancy.RequiredCount() {
		err = Error.New("not enough nodes available: got %d, required %d", limitsCount, redundancy.RequiredCount())
		return nil, storj.PiecePrivateKey{}, errs.Combine(err, nodeErrors.Err())
	}

	err = service.saveSerial(ctx, signer.Serial, bucketID, signer.OrderExpiration)
	if err != nil {
		return nil, storj.PiecePrivateKey{}, Error.Wrap(err)
	}

	projectID, bucketName, err := SplitBucketID(bucketID)
	if err != nil {
		return limits, storj.PiecePrivateKey{}, Error.Wrap(err)
	}
	if err := service.updateBandwidth(ctx, projectID, bucketName, limits...); err != nil {
		return nil, storj.PiecePrivateKey{}, Error.Wrap(err)
	}

	return limits, signer.PrivateKey, nil
}

// CreatePutRepairOrderLimits creates the order limits for uploading the repaired pieces of pointer to newNodes.
func (service *Service) CreatePutRepairOrderLimits(ctx context.Context, bucketID []byte, pointer *pb.Pointer, getOrderLimits []*pb.AddressedOrderLimit, newNodes []*overlay.SelectedNode, optimalThresholdMultiplier float64) (_ []*pb.AddressedOrderLimit, _ storj.PiecePrivateKey, err error) {
	defer mon.Task()(&ctx)(&err)

	orderCreation := time.Now()
	orderExpiration := orderCreation.Add(service.orderExpiration)

	// Create the order limits for being used to upload the repaired pieces
	redundancy, err := eestream.NewRedundancyStrategyFromProto(pointer.GetRemote().GetRedundancy())
	if err != nil {
		return nil, storj.PiecePrivateKey{}, Error.Wrap(err)
	}
	pieceSize := eestream.CalcPieceSize(pointer.GetSegmentSize(), redundancy)

	totalPieces := redundancy.TotalCount()
	totalPiecesAfterRepair := int(math.Ceil(float64(redundancy.OptimalThreshold()) * optimalThresholdMultiplier))
	if totalPiecesAfterRepair > totalPieces {
		totalPiecesAfterRepair = totalPieces
	}

	var numCurrentPieces int
	for _, o := range getOrderLimits {
		if o != nil {
			numCurrentPieces++
		}
	}

	totalPiecesToRepair := totalPiecesAfterRepair - numCurrentPieces

	limits := make([]*pb.AddressedOrderLimit, totalPieces)
	signer, err := NewSignerRepairPut(service, pointer.GetRemote().RootPieceId, pointer.ExpirationDate, orderCreation, orderExpiration, pieceSize)
	if err != nil {
		return nil, storj.PiecePrivateKey{}, Error.Wrap(err)
	}

	var pieceNum int32
	for _, node := range newNodes {
		for int(pieceNum) < totalPieces && getOrderLimits[pieceNum] != nil {
			pieceNum++
		}

		if int(pieceNum) >= totalPieces { // should not happen
			return nil, storj.PiecePrivateKey{}, Error.New("piece num greater than total pieces: %d >= %d", pieceNum, totalPieces)
		}

		limit, err := signer.Sign(ctx, storj.NodeURL{
			ID:      node.ID,
			Address: node.Address.Address,
		}, pieceNum)
		if err != nil {
			return nil, storj.PiecePrivateKey{}, Error.Wrap(err)
		}

		limits[pieceNum] = limit
		pieceNum++
		totalPiecesToRepair--

		if totalPiecesToRepair == 0 {
			break
		}
	}

	err = service.saveSerial(ctx, signer.Serial, bucketID, orderExpiration)
	if err != nil {
		return nil, storj.PiecePrivateKey{}, Error.Wrap(err)
	}

	projectID, bucketName, err := SplitBucketID(bucketID)
	if err != nil {
		return limits, storj.PiecePrivateKey{}, Error.Wrap(err)
	}
	if err := service.updateBandwidth(ctx, projectID, bucketName, limits...); err != nil {
		return nil, storj.PiecePrivateKey{}, Error.Wrap(err)
	}

	return limits, signer.PrivateKey, nil
}

// CreateGracefulExitPutOrderLimit creates an order limit for graceful exit put transfers.
func (service *Service) CreateGracefulExitPutOrderLimit(ctx context.Context, bucketID []byte, nodeID storj.NodeID, pieceNum int32, rootPieceID storj.PieceID, shareSize int32) (limit *pb.AddressedOrderLimit, _ storj.PiecePrivateKey, err error) {
	defer mon.Task()(&ctx)(&err)

	orderCreation := time.Now().UTC()
	orderExpiration := orderCreation.Add(service.orderExpiration)

	// should this use KnownReliable or similar?
	node, err := service.overlay.Get(ctx, nodeID)
	if err != nil {
		return nil, storj.PiecePrivateKey{}, Error.Wrap(err)
	}
	if node.Disqualified != nil {
		return nil, storj.PiecePrivateKey{}, overlay.ErrNodeDisqualified.New("%v", nodeID)
	}
	if !service.overlay.IsOnline(node) {
		return nil, storj.PiecePrivateKey{}, overlay.ErrNodeOffline.New("%v", nodeID)
	}

	signer, err := NewSignerGracefulExit(service, rootPieceID, orderCreation, orderExpiration, shareSize)
	if err != nil {
		return nil, storj.PiecePrivateKey{}, Error.Wrap(err)
	}

	nodeURL := storj.NodeURL{ID: nodeID, Address: node.Address.Address}
	limit, err = signer.Sign(ctx, nodeURL, pieceNum)
	if err != nil {
		return nil, storj.PiecePrivateKey{}, Error.Wrap(err)
	}

	err = service.saveSerial(ctx, signer.Serial, bucketID, signer.OrderExpiration)
	if err != nil {
		return nil, storj.PiecePrivateKey{}, Error.Wrap(err)
	}

	projectID, bucketName, err := SplitBucketID(bucketID)
	if err != nil {
		return limit, storj.PiecePrivateKey{}, Error.Wrap(err)
	}
	if err := service.updateBandwidth(ctx, projectID, bucketName, limit); err != nil {
		return nil, storj.PiecePrivateKey{}, Error.Wrap(err)
	}

	return limit, signer.PrivateKey, nil
}

// UpdateGetInlineOrder updates amount of inline GET bandwidth for given bucket.
func (service *Service) UpdateGetInlineOrder(ctx context.Context, projectID uuid.UUID, bucketName []byte, amount int64) (err error) {
	defer mon.Task()(&ctx)(&err)
	now := time.Now().UTC()
	intervalStart := time.Date(now.Year(), now.Month(), now.Day(), now.Hour(), 0, 0, 0, now.Location())

	return service.orders.UpdateBucketBandwidthInline(ctx, projectID, bucketName, pb.PieceAction_GET, amount, intervalStart)
}

// UpdatePutInlineOrder updates amount of inline PUT bandwidth for given bucket.
func (service *Service) UpdatePutInlineOrder(ctx context.Context, projectID uuid.UUID, bucketName []byte, amount int64) (err error) {
	defer mon.Task()(&ctx)(&err)
	now := time.Now().UTC()
	intervalStart := time.Date(now.Year(), now.Month(), now.Day(), now.Hour(), 0, 0, 0, now.Location())

	return service.orders.UpdateBucketBandwidthInline(ctx, projectID, bucketName, pb.PieceAction_PUT, amount, intervalStart)
}

// SplitBucketID takes a bucketID, splits on /, and returns a projectID and bucketName.
func SplitBucketID(bucketID []byte) (projectID uuid.UUID, bucketName []byte, err error) {
	pathElements := bytes.Split(bucketID, []byte("/"))
	if len(pathElements) > 1 {
		bucketName = pathElements[1]
	}
	projectID, err = uuid.FromString(string(pathElements[0]))
	if err != nil {
		return uuid.UUID{}, nil, err
	}
	return projectID, bucketName, nil
}
