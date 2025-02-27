// Copyright (C) 2019 Storj Labs, Inc.
// See LICENSE for copying information.

package audit

import (
	"context"

	"github.com/zeebo/errs"
	"go.uber.org/zap"

	"storj.io/common/storj"
	"storj.io/storj/satellite/reputation"
)

// Reporter records audit reports in overlay and implements the reporter interface.
//
// architecture: Service
type Reporter struct {
	log              *zap.Logger
	reputations      *reputation.Service
	containment      Containment
	maxRetries       int
	maxReverifyCount int32
}

// Report contains audit result lists for nodes that succeeded, failed, were offline, have pending audits, or failed for unknown reasons.
type Report struct {
	Successes     storj.NodeIDList
	Fails         storj.NodeIDList
	Offlines      storj.NodeIDList
	PendingAudits []*PendingAudit
	Unknown       storj.NodeIDList
}

// NewReporter instantiates a reporter.
func NewReporter(log *zap.Logger, reputations *reputation.Service, containment Containment, maxRetries int, maxReverifyCount int32) *Reporter {
	return &Reporter{
		log:              log,
		reputations:      reputations,
		containment:      containment,
		maxRetries:       maxRetries,
		maxReverifyCount: maxReverifyCount}
}

// RecordAudits saves audit results to overlay. When no error, it returns
// nil for both return values, otherwise it returns the report with the fields
// set to the values which have been saved and the error.
func (reporter *Reporter) RecordAudits(ctx context.Context, req Report) (_ Report, err error) {
	defer mon.Task()(&ctx)(&err)

	successes := req.Successes
	fails := req.Fails
	unknowns := req.Unknown
	offlines := req.Offlines
	pendingAudits := req.PendingAudits

	reporter.log.Debug("Reporting audits",
		zap.Int("successes", len(successes)),
		zap.Int("failures", len(fails)),
		zap.Int("unknowns", len(unknowns)),
		zap.Int("offlines", len(offlines)),
		zap.Int("pending", len(pendingAudits)),
	)

	var errlist errs.Group

	tries := 0
	for tries <= reporter.maxRetries {
		if len(successes) == 0 && len(fails) == 0 && len(unknowns) == 0 && len(offlines) == 0 && len(pendingAudits) == 0 {
			return Report{}, nil
		}

		errlist = errs.Group{}

		if len(successes) > 0 {
			successes, err = reporter.recordAuditSuccessStatus(ctx, successes)
			if err != nil {
				errlist.Add(err)
			}
		}
		if len(fails) > 0 {
			fails, err = reporter.recordAuditFailStatus(ctx, fails)
			if err != nil {
				errlist.Add(err)
			}
		}
		if len(unknowns) > 0 {
			unknowns, err = reporter.recordAuditUnknownStatus(ctx, unknowns)
			if err != nil {
				errlist.Add(err)
			}
		}
		if len(offlines) > 0 {
			offlines, err = reporter.recordOfflineStatus(ctx, offlines)
			if err != nil {
				errlist.Add(err)
			}
		}
		if len(pendingAudits) > 0 {
			pendingAudits, err = reporter.recordPendingAudits(ctx, pendingAudits)
			if err != nil {
				errlist.Add(err)
			}
		}

		tries++
	}

	err = errlist.Err()
	if tries >= reporter.maxRetries && err != nil {
		return Report{
			Successes:     successes,
			Fails:         fails,
			Offlines:      offlines,
			Unknown:       unknowns,
			PendingAudits: pendingAudits,
		}, errs.Combine(Error.New("some nodes failed to be updated in overlay"), err)
	}
	return Report{}, nil
}

// recordAuditFailStatus updates nodeIDs in overlay with isup=true, auditoutcome=fail.
func (reporter *Reporter) recordAuditFailStatus(ctx context.Context, failedAuditNodeIDs storj.NodeIDList) (failed storj.NodeIDList, err error) {
	defer mon.Task()(&ctx)(&err)

	var errors error
	for _, nodeID := range failedAuditNodeIDs {
		err = reporter.reputations.ApplyAudit(ctx, nodeID, reputation.AuditFailure)
		if err != nil {
			failed = append(failed, nodeID)
			errors = errs.Combine(Error.New("failed to record some audit fail statuses in overlay"), err)
		}
	}
	return failed, errors
}

// recordAuditUnknownStatus updates nodeIDs in overlay with isup=true, auditoutcome=unknown.
func (reporter *Reporter) recordAuditUnknownStatus(ctx context.Context, unknownAuditNodeIDs storj.NodeIDList) (failed storj.NodeIDList, err error) {
	defer mon.Task()(&ctx)(&err)

	var errors error
	for _, nodeID := range unknownAuditNodeIDs {
		err = reporter.reputations.ApplyAudit(ctx, nodeID, reputation.AuditUnknown)
		if err != nil {
			failed = append(failed, nodeID)
			errors = errs.Combine(Error.New("failed to record some audit unknown statuses in overlay"), err)
		}
	}
	return failed, errors
}

// recordOfflineStatus updates nodeIDs in overlay with isup=false, auditoutcome=offline.
func (reporter *Reporter) recordOfflineStatus(ctx context.Context, offlineNodeIDs storj.NodeIDList) (failed storj.NodeIDList, err error) {
	defer mon.Task()(&ctx)(&err)

	var errors error
	for _, nodeID := range offlineNodeIDs {
		err = reporter.reputations.ApplyAudit(ctx, nodeID, reputation.AuditOffline)
		if err != nil {
			failed = append(failed, nodeID)
			errors = errs.Combine(Error.New("failed to record some audit offline statuses in overlay"), err)
		}
	}
	return failed, errors
}

// recordAuditSuccessStatus updates nodeIDs in overlay with isup=true, auditoutcome=success.
func (reporter *Reporter) recordAuditSuccessStatus(ctx context.Context, successNodeIDs storj.NodeIDList) (failed storj.NodeIDList, err error) {
	defer mon.Task()(&ctx)(&err)

	var errors error
	for _, nodeID := range successNodeIDs {
		err = reporter.reputations.ApplyAudit(ctx, nodeID, reputation.AuditSuccess)
		if err != nil {
			failed = append(failed, nodeID)
			errors = errs.Combine(Error.New("failed to record some audit success statuses in overlay"), err)
		}
	}
	return failed, errors
}

// recordPendingAudits updates the containment status of nodes with pending audits.
func (reporter *Reporter) recordPendingAudits(ctx context.Context, pendingAudits []*PendingAudit) (failed []*PendingAudit, err error) {
	defer mon.Task()(&ctx)(&err)
	var errlist errs.Group

	for _, pendingAudit := range pendingAudits {
		if pendingAudit.ReverifyCount < reporter.maxReverifyCount {
			err := reporter.containment.IncrementPending(ctx, pendingAudit)
			if err != nil {
				failed = append(failed, pendingAudit)
				errlist.Add(err)
			}
			reporter.log.Info("Audit pending",
				zap.Stringer("Piece ID", pendingAudit.PieceID),
				zap.Stringer("Node ID", pendingAudit.NodeID))
		} else {
			// record failure -- max reverify count reached
			reporter.log.Info("max reverify count reached (audit failed)", zap.Stringer("Node ID", pendingAudit.NodeID))
			err = reporter.reputations.ApplyAudit(ctx, pendingAudit.NodeID, reputation.AuditFailure)
			if err != nil {
				errlist.Add(err)
				failed = append(failed, pendingAudit)
			} else {
				_, err = reporter.containment.Delete(ctx, pendingAudit.NodeID)
				if err != nil && !ErrContainedNotFound.Has(err) {
					errlist.Add(err)
				}
			}
		}
	}

	if len(failed) > 0 {
		for _, v := range failed {
			reporter.log.Debug("failed to record Pending Nodes ",
				zap.Stringer("NodeID", v.NodeID),
				zap.String("Segment StreamID", v.StreamID.String()),
				zap.Uint64("Segment Position", v.Position.Encode()))
		}
		return failed, errs.Combine(Error.New("failed to record some pending audits"), errlist.Err())
	}
	return nil, nil
}
