// Copyright (C) 2021 Storj Labs, Inc.
// See LICENSE for copying information.

package planneddowntime

import (
	"context"
	"time"

	"github.com/zeebo/errs"
	"go.uber.org/zap"

	"storj.io/common/pb"
	"storj.io/common/rpc"
	rand "storj.io/common/testrand"
	"storj.io/storj/storagenode/internalpb"
	"storj.io/storj/storagenode/satellites"
	"storj.io/storj/storagenode/trust"
)

// Endpoint implements private inspector for planned downtime.
type Endpoint struct {
	internalpb.DRPCNodePlannedDowntimeUnimplementedServer

	log        *zap.Logger
	trust      *trust.Pool
	satellites satellites.DB
	dialer     rpc.Dialer
	service    Service
}

// NewEndpoint creates a new planned downtime endpoint.
func NewEndpoint(log *zap.Logger, trust *trust.Pool, satellites satellites.DB, dialer rpc.Dialer, service Service) *Endpoint {
	return &Endpoint{
		log:        log,
		trust:      trust,
		satellites: satellites,
		dialer:     dialer,
		service:    service,
	}
}

// Add adds a new planned downtime on the satellites and the local db.
func (e *Endpoint) Add(ctx context.Context, req *internalpb.AddRequest) (_ *internalpb.AddResponse, err error) {
	e.log.Debug("initialize planned downtime: Add")

	// get all trusted satellites
	trustedSatellites := e.trust.GetSatellites(ctx)

	for _, trusted := range trustedSatellites {
		// get domain name
		saturl, err := e.trust.GetNodeURL(ctx, trusted)
		if err != nil {
			e.log.Error("planned downtime: get satellite address", zap.Stringer("Satellite ID", trusted), zap.Error(err))
			return &internalpb.AddResponse{}, errs.Wrap(err)
		}
		conn, err := e.dialer.DialNodeURL(ctx, saturl)
		if err != nil {
			e.log.Error("planned downtime: connect to satellite", zap.Stringer("Satellite ID", trusted), zap.Error(err))
			return &internalpb.AddResponse{}, errs.Wrap(err)
		}
		defer func() {
			err = errs.Combine(err, conn.Close())
		}()

		client := pb.NewDRPCPlannedDowntimeClient(conn)

		_, err = client.ScheduleDowntime(ctx, &pb.ScheduleDowntimeRequest{
			Timeframe: &pb.Timeframe{
				Start: req.Start,
				End:   req.Start.Add(time.Duration(req.DurationHours) * time.Hour),
			},
		})
		if err != nil {
			return &internalpb.AddResponse{}, errs.Wrap(err)
		}
	}
	err = e.service.Add(ctx, Entry{
		ID:          rand.BytesInt(32),
		Start:       req.Start,
		End:         req.Start.Add(time.Duration(req.DurationHours) * time.Hour),
		ScheduledAt: time.Now(),
	})
	if err != nil {
		return &internalpb.AddResponse{}, errs.Wrap(err)
	}

	return &internalpb.AddResponse{}, nil
}

// GetScheduled gets a list of existing planned downtimes.
func (e *Endpoint) GetScheduled(ctx context.Context, req *internalpb.GetScheduledRequest) (_ *internalpb.GetScheduledResponse, err error) {
	e.log.Debug("initialize planned downtime: GetScheduled")

	list, err := e.service.GetScheduled(ctx, time.Now())
	if err != nil {
		return &internalpb.GetScheduledResponse{}, errs.Wrap(err)
	}

	pbEntries := []*internalpb.Entry{}
	for _, item := range list {
		pbEntries = append(pbEntries, &internalpb.Entry{
			Id:    item.ID,
			Start: item.Start,
			End:   item.End,
		})
	}
	return &internalpb.GetScheduledResponse{
		Entries: pbEntries,
	}, nil
}
