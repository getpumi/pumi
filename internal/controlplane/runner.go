// SPDX-License-Identifier: Apache-2.0
// SPDX-FileCopyrightText: The Pumi Authors

package controlplane

import (
	"context"
	"fmt"
	"slices"

	"connectrpc.com/connect/v2"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/known/durationpb"

	"getpumi.dev/pumi/api/events"
	cloudeventsv1 "getpumi.dev/pumi/api/gen/io/cloudevents/v1"
	controlplanev1 "getpumi.dev/pumi/api/gen/pumi/controlplane/v1"
	eventsv1 "getpumi.dev/pumi/api/gen/pumi/events/v1"
	harnessv1 "getpumi.dev/pumi/api/gen/pumi/harness/v1"
	runnerv1 "getpumi.dev/pumi/api/gen/pumi/runner/v1"
	"getpumi.dev/pumi/api/gen/pumi/runner/v1/runnerv1connect"
)

// The roles that a runner's identity may name: the roles of the
// invocations it takes (decision 0024).
const (
	// RoleGate is the gate role.
	RoleGate = "gate"
	// RoleAgent is the agent role.
	RoleAgent = "agent"
)

// roleOf gives the name of each role of the invocation document.
var roleOf = map[harnessv1.Role]string{
	harnessv1.Role_ROLE_GATE:  RoleGate,
	harnessv1.Role_ROLE_AGENT: RoleAgent,
}

// supportedVersions are the versions of the runner protocol that the
// control plane supports, the latest two minor versions (decision 0041),
// newest first.
var supportedVersions = []*runnerv1.ProtocolVersion{{Major: 1, Minor: 0}}

// runnerHandler serves RunnerService to the callers with one identity.
type runnerHandler struct {
	runnerv1connect.UnimplementedRunnerServiceHandler

	svc *Service
	id  Identity
}

// checkRunnerRoles refuses a runner identity with no role or with a role
// other than gate and agent.
func checkRunnerRoles(id Identity) error {
	if len(id.Roles) == 0 {
		return fmt.Errorf("the identity of the runner %s names no role", id.Principal)
	}
	for _, role := range id.Roles {
		if role != RoleGate && role != RoleAgent {
			return fmt.Errorf("the identity of the runner %s names the role %q; a runner takes only %s and %s",
				id.Principal, role, RoleGate, RoleAgent)
		}
	}
	return nil
}

// refuseOthers returns the error of every call of RunnerService by a
// caller that is not a runner, and nil for a runner (decision 0156).
func (h *runnerHandler) refuseOthers() error {
	if h.id.Caller != CallerRunner {
		return connect.Errorf(connect.CodePermissionDenied,
			"only a runner calls RunnerService, not the %s (decision 0156)", h.id.Caller)
	}
	return nil
}

// chooseVersion returns the newest protocol version that the request and
// the control plane both support.
func chooseVersion(requested []*runnerv1.ProtocolVersion) (*runnerv1.ProtocolVersion, error) {
	if len(requested) == 0 {
		return nil, connect.NewError(connect.CodeInvalidArgument, "the claim names no protocol version")
	}
	for _, v := range supportedVersions {
		if slices.ContainsFunc(requested, func(r *runnerv1.ProtocolVersion) bool { return proto.Equal(r, v) }) {
			return proto.CloneOf(v), nil
		}
	}
	return nil, connect.Errorf(connect.CodeFailedPrecondition,
		"the control plane supports none of the protocol versions of the claim; it supports %v", supportedVersions)
}

// Claim waits for an invocation in one of the runner's roles that has not
// finished and has no current lease, and claims the first one requested
// by appending lease.granted and invocation.started on the condition that
// the invocation is still at the version it read (decision 0153). It
// returns no invocation when none was ready before the wait ended.
func (h *runnerHandler) Claim(ctx context.Context, req *runnerv1.ClaimRequest) (*runnerv1.ClaimResponse, error) {
	if err := h.refuseOthers(); err != nil {
		return nil, err
	}
	version, err := chooseVersion(req.GetProtocolVersions())
	if err != nil {
		return nil, err
	}
	wait, stop := context.WithCancel(ctx)
	defer stop()
	timer := h.svc.runs.clock.AfterFunc(h.svc.runs.claimWait, stop)
	defer timer.Stop()
	for {
		head := h.svc.log.Head()
		res, err := h.svc.claim(h.id)
		if err != nil {
			return nil, err
		}
		if res != nil {
			res.ProtocolVersion = version
			return res, nil
		}
		if err := h.svc.log.Wait(wait, head); err != nil {
			if ctx.Err() != nil {
				return nil, connect.NewError(connect.CodeCanceled, "the claim was cancelled").WithCause(err)
			}
			if wait.Err() != nil {
				return &runnerv1.ClaimResponse{ProtocolVersion: version}, nil
			}
			return nil, logError(err)
		}
	}
}

// claim claims the first open invocation in one of id's roles that a
// runner may claim, and returns nil when there is none.
func (s *Service) claim(id Identity) (*runnerv1.ClaimResponse, error) {
	open, err := s.openInvocations()
	if err != nil {
		return nil, logError(err)
	}
	for _, doc := range open {
		if !slices.Contains(id.Roles, roleOf[doc.GetRole()]) {
			continue
		}
		res, err := s.claimOne(doc, id.Principal)
		if err != nil || res != nil {
			return res, err
		}
	}
	return nil, nil
}

// claimOne claims doc's invocation for holder when a runner may claim it,
// reading its state again when another append changed it first. A runner
// may claim an invocation that has not finished and has no current lease,
// and an agent run only when it has had no lease.
func (s *Service) claimOne(doc *runnerv1.Invocation, holder string) (*runnerv1.ClaimResponse, error) {
	for {
		st, err := s.state(doc.GetInvocation())
		if err != nil {
			return nil, logError(err)
		}
		if st.finished {
			s.closeInvocation(doc.GetInvocation())
		}
		if !st.claimable() || (st.grant != nil && !idempotent(doc)) {
			return nil, nil
		}
		res, ok, err := s.grant(doc, holder, st)
		if err != nil {
			return nil, logError(err)
		}
		if ok {
			return res, nil
		}
	}
}

// idempotent reports whether doc's component may run again after a lease
// of it ended without a result. An agent run is not idempotent, so it is
// granted only once: an agent run whose lease expired may have run, and the
// kernel escalates it when pumi starts again (decisions 0019 and 0182).
func idempotent(doc *runnerv1.Invocation) bool {
	return doc.GetComponent().GetKind() != harnessv1.Component_KIND_AGENT_RUN
}

// Heartbeat renews the runner's lease of the invocation and returns the
// lease timeout and the invocation's cancel request, if one was appended
// (decision 0035). A lease that is not current is aborted.
func (h *runnerHandler) Heartbeat(_ context.Context, req *runnerv1.HeartbeatRequest) (*runnerv1.HeartbeatResponse, error) {
	if err := h.refuseOthers(); err != nil {
		return nil, err
	}
	if _, err := h.svc.current(req.GetInvocation(), req.GetLeaseNumber(), h.id.Principal); err != nil {
		return nil, err
	}
	timeout, err := h.svc.renew(req.GetInvocation(), req.GetLeaseNumber())
	if err != nil {
		return nil, err
	}
	cancel, err := h.svc.cancelOf(req.GetInvocation())
	if err != nil {
		return nil, err
	}
	return &runnerv1.HeartbeatResponse{Cancel: cancel, LeaseTimeout: durationpb.New(timeout)}, nil
}

// cancelOf returns the first cancel request in the log that names
// invocation, or that names the invocation's plan and no invocation, which
// cancels the plan's invocations (decisions 0170 and 0175), or nil when
// there is none.
func (s *Service) cancelOf(invocation string) (*runnerv1.HeartbeatResponse_Cancel, error) {
	found, _, err := s.log.Read(filter(&controlplanev1.Filter{Types: []string{events.TypeInvocationRequested, events.TypeCancelRequested}}, 0, 0))
	if err != nil {
		return nil, logError(err)
	}
	plan := ""
	for _, e := range found {
		r := &eventsv1.InvocationRequested{}
		if e.Event.GetType() == events.TypeInvocationRequested && e.Event.GetProtoData().UnmarshalTo(r) == nil && r.GetInvocation() == invocation {
			plan = r.GetPlan()
			break
		}
	}
	for _, e := range found {
		c := &eventsv1.CancelRequested{}
		if e.Event.GetType() != events.TypeCancelRequested || e.Event.GetProtoData().UnmarshalTo(c) != nil {
			continue
		}
		if c.GetInvocation() == invocation || (c.GetInvocation() == "" && plan != "" && c.GetPlan() == plan) {
			return &runnerv1.HeartbeatResponse_Cancel{RequestedBy: c.GetRequestedBy()}, nil
		}
	}
	return nil, nil
}

// SendLog keeps a batch of the invocation's log output with its recent
// output, outside the event log (decisions 0034 and 0163). A lease that is
// not current is aborted, and the batch is dropped.
func (h *runnerHandler) SendLog(_ context.Context, req *runnerv1.SendLogRequest) (*runnerv1.SendLogResponse, error) {
	if err := h.refuseOthers(); err != nil {
		return nil, err
	}
	if _, err := h.svc.current(req.GetInvocation(), req.GetLeaseNumber(), h.id.Principal); err != nil {
		return nil, err
	}
	if err := h.svc.keepOutput(req.GetInvocation(), req.GetLeaseNumber(), req.GetOutput()); err != nil {
		return nil, err
	}
	return &runnerv1.SendLogResponse{}, nil
}

// reportOf returns the appender of the table of decision 0156 for a report
// of doc's invocation, and the role of the runner's Cedar principal: an
// external action reports as one, whatever its role, and every other
// invocation reports in its role.
func reportOf(doc *runnerv1.Invocation) (events.Appender, string) {
	role := roleOf[doc.GetRole()]
	switch {
	case doc.GetComponent().GetKind() == harnessv1.Component_KIND_EXTERNAL_ACTION:
		return events.AppenderRunnerReportExternalAction, role
	case doc.GetRole() == harnessv1.Role_ROLE_GATE:
		return events.AppenderRunnerReportGateRole, role
	}
	return events.AppenderRunnerReportAgentRole, role
}

// reportedOutcomes are the outcomes that a runner reports. The kernel
// resolves an invocation on recovery, not a runner.
var reportedOutcomes = []eventsv1.InvocationFinished_Outcome{
	eventsv1.InvocationFinished_OUTCOME_SUCCEEDED,
	eventsv1.InvocationFinished_OUTCOME_FAILED,
	eventsv1.InvocationFinished_OUTCOME_CANCELLED,
	eventsv1.InvocationFinished_OUTCOME_CAP_REACHED,
}

// Report appends invocation.result, the events the invocation's kind and
// role allow, invocation.finished and lease.released, all or none, while
// the runner's lease is current, and ends the lease (decisions 0035 and
// 0156). A verdict is decided with writeVerdict before it is written. A
// lease that is not current is aborted, and nothing is written.
func (h *runnerHandler) Report(ctx context.Context, req *runnerv1.ReportRequest) (*runnerv1.ReportResponse, error) {
	if err := h.refuseOthers(); err != nil {
		return nil, err
	}
	inv, number := req.GetInvocation(), req.GetLeaseNumber()
	st, err := h.svc.current(inv, number, h.id.Principal)
	if err != nil {
		return nil, err
	}
	doc, err := h.svc.document(inv)
	if err != nil {
		return nil, logError(err)
	}
	appender, role := reportOf(doc)
	evs, err := h.reported(req, appender, st)
	if err != nil {
		return nil, err
	}
	p := principal{caller: CallerRunner, id: h.id.Principal, role: role}
	for {
		positions, err := h.svc.appendAs(ctx, p, evs, []*controlplanev1.Condition{atVersion(inv, st.version)})
		if connect.CodeOf(err) == connect.CodeAborted {
			if st, err = h.svc.current(inv, number, h.id.Principal); err != nil {
				return nil, err
			}
			continue
		}
		if err != nil {
			return nil, err
		}
		h.svc.endLease(inv, number)
		h.svc.closeInvocation(inv)
		return &runnerv1.ReportResponse{Positions: positions}, nil
	}
}

// reported checks the report req of an invocation at the state st and
// returns the events to write: the result, the reported events, the end
// and the release of the lease. It refuses a report with an outcome that a
// runner does not report, a result of another invocation and an event that
// is not a valid Pumi event or that names another invocation, with invalid
// argument, and an event that appender may not append, with permission
// denied.
func (h *runnerHandler) reported(req *runnerv1.ReportRequest, appender events.Appender, st invocationState) ([]*cloudeventsv1.CloudEvent, error) {
	inv := req.GetInvocation()
	if !slices.Contains(reportedOutcomes, req.GetOutcome()) {
		return nil, connect.Errorf(connect.CodeInvalidArgument, "the report has the outcome %v, which a runner does not report", req.GetOutcome())
	}
	result := proto.CloneOf(req.GetResult())
	if result == nil {
		result = &eventsv1.InvocationResult{}
	}
	if result.GetInvocation() != "" && result.GetInvocation() != inv {
		return nil, connect.Errorf(connect.CodeInvalidArgument, "the report of %q carries the result of %q", inv, result.GetInvocation())
	}
	result.Invocation = inv
	first, err := event(result, runnerSource, inv, st.started)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, "the result could not be written").WithCause(err)
	}
	evs := []*cloudeventsv1.CloudEvent{first}
	for i, original := range req.GetEvents() {
		e, err := h.reportedEvent(i, original, appender, inv)
		if err != nil {
			return nil, err
		}
		evs = append(evs, e)
	}
	finished, err := event(&eventsv1.InvocationFinished{Invocation: inv, Outcome: req.GetOutcome()}, runnerSource, inv, first.GetId())
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, "the end of the invocation could not be written").WithCause(err)
	}
	released, err := event(&eventsv1.LeaseReleased{Lease: inv, Number: req.GetLeaseNumber()}, source, inv, finished.GetId())
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, "the release of the lease could not be written").WithCause(err)
	}
	return append(evs, finished, released), nil
}

// reportedEvent checks event i of a report and returns a copy with the
// runner's tenant. Its data, when it has an invocation field, must name
// the invocation inv.
func (h *runnerHandler) reportedEvent(i int, original *cloudeventsv1.CloudEvent, appender events.Appender, inv string) (*cloudeventsv1.CloudEvent, error) {
	e, err := withTenant(i, original, h.id.Tenant)
	if err != nil {
		return nil, err
	}
	if !events.MayAppend(e.GetType(), appender) {
		return nil, connect.Errorf(connect.CodePermissionDenied,
			"event %d of the report: a %s may not append %s (decision 0156)", i+1, appender, e.GetType())
	}
	data, err := events.Data(e.GetType())
	if err != nil {
		return nil, connect.Errorf(connect.CodeInvalidArgument, "event %d of the report: %v", i+1, err)
	}
	if err := e.GetProtoData().UnmarshalTo(data); err != nil {
		return nil, connect.Errorf(connect.CodeInvalidArgument, "event %d of the report: %v", i+1, err)
	}
	if f := data.ProtoReflect().Descriptor().Fields().ByName("invocation"); f != nil && f.Kind() == protoreflect.StringKind {
		if got := data.ProtoReflect().Get(f).String(); got != inv {
			return nil, connect.Errorf(connect.CodeInvalidArgument,
				"event %d of the report of %q names the invocation %q", i+1, inv, got)
		}
	}
	return e, nil
}
