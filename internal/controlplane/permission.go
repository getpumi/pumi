// SPDX-License-Identifier: Apache-2.0
// SPDX-FileCopyrightText: The Pumi Authors

package controlplane

import (
	"context"
	"errors"

	"connectrpc.com/connect/v2"
	"google.golang.org/protobuf/proto"

	"getpumi.dev/pumi/api/events"
	cloudeventsv1 "getpumi.dev/pumi/api/gen/io/cloudevents/v1"
	controlplanev1 "getpumi.dev/pumi/api/gen/pumi/controlplane/v1"
	eventsv1 "getpumi.dev/pumi/api/gen/pumi/events/v1"
	"getpumi.dev/pumi/internal/controlplane/embeddedlog"
	"getpumi.dev/pumi/internal/controlplane/projections"
)

// permissionOf gives the Cedar action that is decided before an event of
// each permission type is appended (decision 0156).
var permissionOf = map[string]string{
	events.TypeVerdictRecorded:  "writeVerdict",
	events.TypeApprovalRecorded: "approve",
	events.TypeOverrideRecorded: "override",
	events.TypeCancelRequested:  "cancel",
}

// permission is one event of an append whose permission is decided: its
// index in the append and the question to decide.
type permission struct {
	index int
	q     question
}

// permissionsOf returns the permissions to decide for evs, in order. The
// plan, which is the Cedar resource, is the plan the data names, for a
// verdict, an override and a cancel that names one, and the event's
// subject, for an approval and a cancel that names none. It refuses, with invalid argument, a
// permission event with no plan.
func permissionsOf(evs []*cloudeventsv1.CloudEvent) ([]permission, error) {
	var out []permission
	for i, e := range evs {
		action, ok := permissionOf[e.GetType()]
		if !ok {
			continue
		}
		data, err := events.Data(e.GetType())
		if err != nil {
			return nil, connect.Errorf(connect.CodeInvalidArgument, "%v", err)
		}
		if err := e.GetProtoData().UnmarshalTo(data); err != nil {
			return nil, connect.Errorf(connect.CodeInvalidArgument, "event %d of the append: %v", i+1, err)
		}
		q := question{action: action, plan: e.GetAttributes()["subject"].GetCeString(), cause: e.GetId()}
		q.keys = keysOf(data, &q.plan)
		if q.plan == "" {
			return nil, connect.Errorf(connect.CodeInvalidArgument,
				"event %d of the append, a %s, names no plan, which its permission %s is decided for", i+1, e.GetType(), action)
		}
		out = append(out, permission{index: i, q: q})
	}
	return out, nil
}

// keysOf returns the keys of the facts of a permission event's data, and
// sets *plan to the plan the data names, when it names one. A verdict and
// an override always name one.
func keysOf(data proto.Message, plan *string) projections.Keys {
	switch d := data.(type) {
	case *eventsv1.VerdictRecorded:
		*plan = d.GetPlan()
		return projections.Keys{HeadTree: d.GetTree(), Invocation: d.GetInvocation()}
	case *eventsv1.OverrideRecorded:
		*plan = d.GetPlan()
		return projections.Keys{HeadTree: d.GetTree()}
	case *eventsv1.ApprovalRecorded:
		return projections.Keys{HeadTree: d.GetTree()}
	case *eventsv1.CancelRequested:
		if d.GetPlan() != "" {
			*plan = d.GetPlan()
		}
		return projections.Keys{Invocation: d.GetInvocation()}
	}
	return projections.Keys{}
}

// appendAs appends evs, which are checked and carry the caller's tenant,
// for p, all or none, when every condition holds. Before it appends a
// verdict, an approval, an override or a cancel, it decides the matching
// permission with the log as of the head (decision 0156). When every
// permission is allowed, it writes the decision.made events and then evs
// in one append, only while the head is still where it decided, and
// decides again when it has moved. When one is denied, it writes the
// decisions that denied, refuses the append with permission denied and
// writes none of evs. A plan.added is checked against the log as of the
// same head (decision 0196). It returns the positions of evs.
func (s *Service) appendAs(ctx context.Context, p principal, evs []*cloudeventsv1.CloudEvent,
	conditions []*controlplanev1.Condition,
) ([]uint64, error) {
	perms, err := permissionsOf(evs)
	if err != nil {
		return nil, err
	}
	added, err := addedPlans(evs)
	if err != nil {
		return nil, err
	}
	check := func(version func(string) uint64) error { return conflicts(conditions, version) }
	if len(perms) == 0 && len(added) == 0 {
		positions, err := s.log.Append(evs, check)
		return positions, appendError(err)
	}
	for {
		if err := ctx.Err(); err != nil {
			return nil, connect.NewError(connect.CodeCanceled, "the append was cancelled").WithCause(err)
		}
		positions, err := s.appendAtHead(p, evs, perms, added, check)
		if errors.Is(err, embeddedlog.ErrHeadMoved) {
			continue
		}
		return positions, err
	}
}

// appendAtHead checks added and decides perms with the log as of its head,
// and appends the decisions and evs while the head is still there. It
// returns embeddedlog.ErrHeadMoved when the head moved.
func (s *Service) appendAtHead(p principal, evs []*cloudeventsv1.CloudEvent, perms []permission,
	added map[int]*eventsv1.PlanAdded, check func(version func(string) uint64) error,
) ([]uint64, error) {
	at, hash := s.log.Snapshot()
	if err := s.checkAdded(at, added, len(evs)); err != nil {
		return nil, err
	}
	decisions, denied, err := s.decidePermissions(at, hash, p, perms)
	if err != nil {
		return nil, err
	}
	if len(denied) > 0 {
		return nil, s.refuse(decisions, perms, denied)
	}
	if s.trace != nil {
		s.trace("decided")
	}
	positions, err := s.log.AppendAt(at, append(decisions, evs...), check)
	if errors.Is(err, embeddedlog.ErrHeadMoved) {
		return nil, err
	}
	if err != nil {
		return nil, appendError(err)
	}
	return positions[len(decisions):], nil
}

// decidePermissions decides perms for p with the log as of at, whose line
// has the hash hash, and returns the decision.made events and the indexes
// in perms of those that were denied.
func (s *Service) decidePermissions(at uint64, hash string, p principal, perms []permission) ([]*cloudeventsv1.CloudEvent, []int, error) {
	decisions := make([]*cloudeventsv1.CloudEvent, 0, len(perms))
	var denied []int
	for i, perm := range perms {
		d, err := s.decide(at, hash, p, perm.q)
		if err != nil {
			return nil, nil, connect.NewError(connect.CodeInternal, "a permission could not be decided").WithCause(err)
		}
		decisions = append(decisions, d.event)
		if !d.decision.Allow {
			denied = append(denied, i)
		}
	}
	return decisions, denied, nil
}

// refuse writes the decisions that denied, decisions[i] for each i of
// denied, and returns the error of the refused append, which names the
// first denied event and where its decision is.
func (s *Service) refuse(decisions []*cloudeventsv1.CloudEvent, perms []permission, denied []int) error {
	written := make([]*cloudeventsv1.CloudEvent, 0, len(denied))
	for _, i := range denied {
		written = append(written, decisions[i])
	}
	positions, err := s.log.Append(written, nil)
	if err != nil {
		return logError(err)
	}
	first := perms[denied[0]]
	return connect.Errorf(connect.CodePermissionDenied,
		"event %d of the append: the control plane denied %s for the plan %s; the decision is at position %d (decision 0156)",
		first.index+1, first.q.action, first.q.plan, positions[0])
}

// appendError returns the Connect error of a failed append of the log.
func appendError(err error) error {
	if err == nil {
		return nil
	}
	var conflict *conflictError
	if errors.As(err, &conflict) {
		return conflict.connectError()
	}
	return logError(err)
}
