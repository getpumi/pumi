// SPDX-License-Identifier: Apache-2.0
// SPDX-FileCopyrightText: The Pumi Authors

package conformance

import (
	"context"
	"slices"
	"testing"

	"connectrpc.com/connect/v2"
	"google.golang.org/protobuf/proto"

	"getpumi.dev/pumi/api/events"
	cloudeventsv1 "getpumi.dev/pumi/api/gen/io/cloudevents/v1"
	controlplanev1 "getpumi.dev/pumi/api/gen/pumi/controlplane/v1"
	eventsv1 "getpumi.dev/pumi/api/gen/pumi/events/v1"
	harnessv1 "getpumi.dev/pumi/api/gen/pumi/harness/v1"
	runnerv1 "getpumi.dev/pumi/api/gen/pumi/runner/v1"
	"getpumi.dev/pumi/api/gen/pumi/runner/v1/runnerv1connect"
)

// runnerTests are the cases of RunnerService (decisions 0024, 0035, 0152,
// 0153, 0156 and 0163).
var runnerTests = []struct {
	name string
	test func(t *testing.T, p Plane)
}{
	{"ClaimGrantsALeaseAndStartsTheInvocation", claimGrantsALeaseAndStartsTheInvocation},
	{"TwoRunnersClaimingOneInvocation", twoRunnersClaimingOneInvocation},
	{"ClaimTakesOnlyTheRunnersRoles", claimTakesOnlyTheRunnersRoles},
	{"ClaimRefusesProtocolVersionsItDoesNotSupport", claimRefusesProtocolVersionsItDoesNotSupport},
	{"OnlyARunnerCallsRunnerService", onlyARunnerCallsRunnerService},
	{"HeartbeatRenewsTheLease", heartbeatRenewsTheLease},
	{"HeartbeatReturnsACancel", heartbeatReturnsACancel},
	{"APlanCancelReachesThePlansInvocations", aPlanCancelReachesThePlansInvocations},
	{"AnExpiredLeaseIsReclaimed", anExpiredLeaseIsReclaimed},
	{"ALateReportWithAnOldLeaseNumberIsRefused", aLateReportWithAnOldLeaseNumberIsRefused},
	{"ReportWritesTheResultTheEventsAndTheEnd", reportWritesTheResultTheEventsAndTheEnd},
	{"AnAgentRoleInvocationIsRefusedWhenItReportsAVerdict", anAgentRoleInvocationIsRefusedWhenItReportsAVerdict},
	{"ReportRefusesAnInvalidReport", reportRefusesAnInvalidReport},
	{"ALogBatchWithAnOldLeaseNumberIsRefused", aLogBatchWithAnOldLeaseNumberIsRefused},
}

// version1 is the protocol version that the suite's runners support.
var version1 = []*runnerv1.ProtocolVersion{{Major: 1, Minor: 0}}

// plan is the plan of the suite's invocations.
const plan = "plan-a"

// document returns the invocation document of invocation in role, of a
// check in the gate role and an agent run in the agent role.
func document(invocation string, role Role) *runnerv1.Invocation {
	doc := &runnerv1.Invocation{
		Invocation:     invocation,
		IdempotencyKey: "key-of-" + invocation,
		Component:      &harnessv1.Component{Kind: harnessv1.Component_KIND_CHECK, Name: "tests", Version: "1"},
		Input:          &harnessv1.Input{Repo: "repo", Tree: "tree-1", Task: "run the tests"},
		Role:           harnessv1.Role_ROLE_GATE,
		ImageDigest:    "sha256:image",
		Budget:         &harnessv1.Usage{Tokens: 1000, CostMicrodollars: 1000, Seconds: 60},
	}
	if role == Agent {
		doc.Component = &harnessv1.Component{Kind: harnessv1.Component_KIND_AGENT_RUN, Name: "agent", Version: "1"}
		doc.Role = harnessv1.Role_ROLE_AGENT
	}
	return doc
}

// request has the kernel append invocation.requested for doc, about the
// suite's plan, and returns doc.
func request(t *testing.T, p Plane, doc *runnerv1.Invocation) *runnerv1.Invocation {
	t.Helper()
	data, err := events.InvocationRequested(plan, doc)
	if err != nil {
		t.Fatal(err)
	}
	mustAppend(t, p.Clients, Kernel, []*cloudeventsv1.CloudEvent{build(t, data, plan)})
	return doc
}

// claim has r claim an invocation, which must be ready, and returns the
// response.
func claim(t *testing.T, r runnerv1connect.RunnerServiceClient) *runnerv1.ClaimResponse {
	t.Helper()
	res, err := r.Claim(t.Context(), &runnerv1.ClaimRequest{ProtocolVersions: version1})
	if err != nil {
		t.Fatalf("Claim: %v", err)
	}
	if res.GetInvocation() == nil {
		t.Fatal("Claim returned no invocation, want the one that is ready")
	}
	return res
}

// wantInvocationEvents fails the test unless the events about
// invocation-1 are of the types want, in order.
func wantInvocationEvents(t *testing.T, p Plane, want ...string) {
	t.Helper()
	got := []string{}
	for _, e := range subjectEvents(t, p, "invocation-1") {
		got = append(got, e.GetType())
	}
	if !slices.Equal(got, want) {
		t.Fatalf("the events about invocation-1 are %q, want %q", got, want)
	}
}

// subjectEvents returns the events about subject, in log order.
func subjectEvents(t *testing.T, p Plane, subject string) []*cloudeventsv1.CloudEvent {
	t.Helper()
	res, err := p.Clients(Kernel).Query(t.Context(), &controlplanev1.QueryRequest{Filter: &controlplanev1.Filter{Subject: subject}})
	if err != nil {
		t.Fatal(err)
	}
	out := []*cloudeventsv1.CloudEvent{}
	for _, e := range res.GetEvents() {
		out = append(out, e.GetEvent())
	}
	return out
}

// typeCount returns the number of events of typ in the log.
func typeCount(t *testing.T, p Plane, typ string) int {
	t.Helper()
	res, err := p.Clients(Kernel).Query(t.Context(), &controlplanev1.QueryRequest{Filter: &controlplanev1.Filter{Types: []string{typ}}})
	if err != nil {
		t.Fatal(err)
	}
	return len(res.GetEvents())
}

// dataOf returns the data of e in msg.
func dataOf[M proto.Message](t *testing.T, e *cloudeventsv1.CloudEvent, msg M) M {
	t.Helper()
	if err := e.GetProtoData().UnmarshalTo(msg); err != nil {
		t.Fatalf("reading the data of the %s: %v", e.GetType(), err)
	}
	return msg
}

// claimGrantsALeaseAndStartsTheInvocation: Claim returns the invocation
// document, the protocol version and lease number 1, and appends
// lease.granted and invocation.started about the invocation (decisions
// 0152 and 0153).
func claimGrantsALeaseAndStartsTheInvocation(t *testing.T, p Plane) {
	doc := request(t, p, document("invocation-1", Gate))
	res := claim(t, p.Runner("runner-a", Gate))
	if !proto.Equal(res.GetInvocation(), doc) {
		t.Errorf("Claim returned the document %v, want %v", res.GetInvocation(), doc)
	}
	if res.GetLeaseNumber() != 1 {
		t.Errorf("the lease number is %d, want 1", res.GetLeaseNumber())
	}
	if res.GetLeaseTimeout().AsDuration() <= 0 {
		t.Errorf("the lease timeout is %v, want a positive duration", res.GetLeaseTimeout().AsDuration())
	}
	if v := res.GetProtocolVersion(); v.GetMajor() != 1 || v.GetMinor() != 0 {
		t.Errorf("the protocol version is %v, want 1.0", v)
	}
	evs := subjectEvents(t, p, "invocation-1")
	wantInvocationEvents(t, p, events.TypeLeaseGranted, events.TypeInvocationStarted)
	granted := dataOf(t, evs[0], &eventsv1.LeaseGranted{})
	if granted.GetLease() != "invocation-1" || granted.GetHolder() != "runner-a" || granted.GetNumber() != 1 ||
		granted.GetTimeout().AsDuration() != res.GetLeaseTimeout().AsDuration() {
		t.Errorf("lease.granted is %v, want the lease of invocation-1, number 1, held by runner-a", granted)
	}
	started := dataOf(t, evs[1], &eventsv1.InvocationStarted{})
	if started.GetInvocation() != "invocation-1" || started.GetRunner() != "runner-a" || started.GetLeaseNumber() != 1 {
		t.Errorf("invocation.started is %v, want invocation-1 started by runner-a under lease 1", started)
	}
}

// twoRunnersClaimingOneInvocation: two runners wait in Claim for one
// invocation; exactly one claims it, and the other keeps waiting (decision
// 0153).
func twoRunnersClaimingOneInvocation(t *testing.T, p Plane) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	type claimed struct {
		res *runnerv1.ClaimResponse
		err error
	}
	results := make(chan claimed, 2)
	for _, name := range []string{"runner-a", "runner-b"} {
		r := p.Runner(name, Gate)
		go func() {
			res, err := r.Claim(ctx, &runnerv1.ClaimRequest{ProtocolVersions: version1})
			results <- claimed{res, err}
		}()
	}
	request(t, p, document("invocation-1", Gate))
	first := <-results
	if first.err != nil || first.res.GetInvocation().GetInvocation() != "invocation-1" {
		t.Fatalf("the first Claim to return gave %v, %v; want invocation-1", first.res, first.err)
	}
	cancel()
	if second := <-results; second.res.GetInvocation() != nil {
		t.Fatalf("both runners claimed the invocation; the second has lease %d", second.res.GetLeaseNumber())
	}
	if n := typeCount(t, p, events.TypeInvocationStarted); n != 1 {
		t.Fatalf("the log holds %d invocation.started events, want 1", n)
	}
}

// claimTakesOnlyTheRunnersRoles: a runner claims only invocations in its
// roles, because gate work and agent work use separate identities
// (decision 0024).
func claimTakesOnlyTheRunnersRoles(t *testing.T, p Plane) {
	request(t, p, document("agent-work", Agent))
	request(t, p, document("gate-work", Gate))
	if got := claim(t, p.Runner("gate-runner", Gate)).GetInvocation().GetInvocation(); got != "gate-work" {
		t.Errorf("the gate runner claimed %q, want gate-work", got)
	}
	if got := claim(t, p.Runner("agent-runner", Agent)).GetInvocation().GetInvocation(); got != "agent-work" {
		t.Errorf("the agent runner claimed %q, want agent-work", got)
	}
}

// claimRefusesProtocolVersionsItDoesNotSupport: a Claim with no protocol
// version is invalid, and one with none that the server supports fails
// with failed_precondition (decision 0041).
func claimRefusesProtocolVersionsItDoesNotSupport(t *testing.T, p Plane) {
	request(t, p, document("invocation-1", Gate))
	r := p.Runner("runner-a", Gate)
	_, err := r.Claim(t.Context(), &runnerv1.ClaimRequest{})
	wantCode(t, err, connect.CodeInvalidArgument)
	_, err = r.Claim(t.Context(), &runnerv1.ClaimRequest{ProtocolVersions: []*runnerv1.ProtocolVersion{{Major: 2}}})
	wantCode(t, err, connect.CodeFailedPrecondition)
	if n := typeCount(t, p, events.TypeInvocationStarted); n != 0 {
		t.Fatalf("the log holds %d invocation.started events after the refused claims, want none", n)
	}
}

// onlyARunnerCallsRunnerService: the kernel and a person may not call
// RunnerService (decision 0156).
func onlyARunnerCallsRunnerService(t *testing.T, p Plane) {
	request(t, p, document("invocation-1", Gate))
	for _, caller := range []Caller{Kernel, Person} {
		_, err := p.RunnerServiceAs(caller).Claim(t.Context(), &runnerv1.ClaimRequest{ProtocolVersions: version1})
		wantCode(t, err, connect.CodePermissionDenied)
		_, err = p.RunnerServiceAs(caller).Heartbeat(t.Context(), &runnerv1.HeartbeatRequest{Invocation: "invocation-1", LeaseNumber: 1})
		wantCode(t, err, connect.CodePermissionDenied)
	}
	if n := typeCount(t, p, events.TypeInvocationStarted); n != 0 {
		t.Fatalf("the log holds %d invocation.started events, want none", n)
	}
}

// heartbeat sends a heartbeat for res's invocation and lease with r.
func heartbeat(t *testing.T, r runnerv1connect.RunnerServiceClient, res *runnerv1.ClaimResponse) (*runnerv1.HeartbeatResponse, error) {
	t.Helper()
	return r.Heartbeat(t.Context(), &runnerv1.HeartbeatRequest{
		Invocation: res.GetInvocation().GetInvocation(), LeaseNumber: res.GetLeaseNumber(),
	})
}

// heartbeatRenewsTheLease: each heartbeat gives the lease its full timeout
// again, and a lease that is not renewed expires (decision 0020).
func heartbeatRenewsTheLease(t *testing.T, p Plane) {
	request(t, p, document("invocation-1", Gate))
	r := p.Runner("runner-a", Gate)
	res := claim(t, r)
	timeout := res.GetLeaseTimeout().AsDuration()
	for range 3 {
		p.Advance(timeout * 3 / 4)
		hb, err := heartbeat(t, r, res)
		if err != nil {
			t.Fatalf("Heartbeat: %v", err)
		}
		if hb.GetLeaseTimeout().AsDuration() != timeout {
			t.Errorf("the renewed lease lasts %v, want %v", hb.GetLeaseTimeout().AsDuration(), timeout)
		}
	}
	if n := typeCount(t, p, events.TypeLeaseExpired); n != 0 {
		t.Fatalf("a renewed lease expired")
	}
	p.Advance(timeout)
	wantInvocationEvents(t, p, events.TypeLeaseGranted, events.TypeInvocationStarted, events.TypeLeaseExpired)
	_, err := heartbeat(t, r, res)
	wantCode(t, err, connect.CodeAborted)
}

// heartbeatReturnsACancel: once cancel.requested names the invocation, the
// heartbeat carries it (decision 0035).
func heartbeatReturnsACancel(t *testing.T, p Plane) {
	mustAppend(t, p.Clients, Person, []*cloudeventsv1.CloudEvent{planAdded(t, plan)})
	request(t, p, document("invocation-1", Gate))
	r := p.Runner("runner-a", Gate)
	res := claim(t, r)
	hb, err := heartbeat(t, r, res)
	if err != nil {
		t.Fatalf("Heartbeat: %v", err)
	}
	if hb.GetCancel() != nil {
		t.Fatalf("the heartbeat carries the cancel %v before any was requested", hb.GetCancel())
	}
	mustAppend(t, p.Clients, Person, []*cloudeventsv1.CloudEvent{
		build(t, &eventsv1.CancelRequested{Invocation: "invocation-1", RequestedBy: "someone"}, plan),
	})
	hb, err = heartbeat(t, r, res)
	if err != nil {
		t.Fatalf("Heartbeat: %v", err)
	}
	if hb.GetCancel().GetRequestedBy() != "someone" {
		t.Fatalf("the heartbeat carries the cancel %v, want one requested by someone", hb.GetCancel())
	}
}

// aPlanCancelReachesThePlansInvocations: a cancel that names a plan and no
// invocation cancels the plan's invocations, which name their plan in
// invocation.requested (decisions 0170 and 0175), and no other plan's.
func aPlanCancelReachesThePlansInvocations(t *testing.T, p Plane) {
	mustAppend(t, p.Clients, Person, []*cloudeventsv1.CloudEvent{planAdded(t, plan), planAdded(t, "plan-b")})
	request(t, p, document("invocation-1", Gate))
	other, err := events.InvocationRequested("plan-b", document("invocation-2", Gate))
	if err != nil {
		t.Fatal(err)
	}
	mustAppend(t, p.Clients, Kernel, []*cloudeventsv1.CloudEvent{build(t, other, "plan-b")})
	r := p.Runner("runner-a", Gate)
	first, second := claim(t, r), claim(t, r)
	mustAppend(t, p.Clients, Person, []*cloudeventsv1.CloudEvent{
		build(t, &eventsv1.CancelRequested{Plan: plan, RequestedBy: "someone"}, plan),
	})
	for _, res := range []*runnerv1.ClaimResponse{first, second} {
		hb, err := heartbeat(t, r, res)
		if err != nil {
			t.Fatalf("Heartbeat: %v", err)
		}
		inv := res.GetInvocation().GetInvocation()
		switch {
		case inv == "invocation-1" && hb.GetCancel().GetRequestedBy() != "someone":
			t.Errorf("the heartbeat of %s, of the cancelled plan, carries the cancel %v, want one requested by someone", inv, hb.GetCancel())
		case inv == "invocation-2" && hb.GetCancel() != nil:
			t.Errorf("the heartbeat of %s, of another plan, carries the cancel %v, want none", inv, hb.GetCancel())
		}
	}
}

// expire claims invocation as name, lets the lease expire and returns the
// response of the claim.
func expire(t *testing.T, p Plane, name, invocation string) *runnerv1.ClaimResponse {
	t.Helper()
	request(t, p, document(invocation, Gate))
	res := claim(t, p.Runner(name, Gate))
	p.Advance(res.GetLeaseTimeout().AsDuration())
	return res
}

// anExpiredLeaseIsReclaimed: when a lease is not renewed in time, the
// control plane appends lease.expired, and another runner can claim the
// invocation under the next lease number (decisions 0020 and 0156).
func anExpiredLeaseIsReclaimed(t *testing.T, p Plane) {
	expire(t, p, "runner-a", "invocation-1")
	evs := subjectEvents(t, p, "invocation-1")
	wantInvocationEvents(t, p, events.TypeLeaseGranted, events.TypeInvocationStarted, events.TypeLeaseExpired)
	if expired := dataOf(t, evs[2], &eventsv1.LeaseExpired{}); expired.GetLease() != "invocation-1" || expired.GetNumber() != 1 {
		t.Errorf("lease.expired is %v, want lease 1 of invocation-1", expired)
	}
	res := claim(t, p.Runner("runner-b", Gate))
	if res.GetInvocation().GetInvocation() != "invocation-1" || res.GetLeaseNumber() != 2 {
		t.Fatalf("the second claim gave %q under lease %d, want invocation-1 under lease 2",
			res.GetInvocation().GetInvocation(), res.GetLeaseNumber())
	}
	wantInvocationEvents(t, p, events.TypeLeaseGranted, events.TypeInvocationStarted, events.TypeLeaseExpired,
		events.TypeLeaseGranted, events.TypeInvocationStarted)
}

// report returns a report of res's invocation with outcome succeeded and
// the events evs.
func report(res *runnerv1.ClaimResponse, evs ...*cloudeventsv1.CloudEvent) *runnerv1.ReportRequest {
	invocation := res.GetInvocation().GetInvocation()
	return &runnerv1.ReportRequest{
		Invocation:  invocation,
		LeaseNumber: res.GetLeaseNumber(),
		Result: &eventsv1.InvocationResult{
			Invocation: invocation, LogBlobHash: "sha256:log", Usage: &eventsv1.InvocationResult_Usage{Tokens: 10, Seconds: 2},
		},
		Events:  evs,
		Outcome: eventsv1.InvocationFinished_OUTCOME_SUCCEEDED,
	}
}

// aLateReportWithAnOldLeaseNumberIsRefused: after a lease expires and
// another runner claims the invocation, a report, a heartbeat and a log
// batch under the old lease number are refused with aborted, and nothing is
// written; the holder of the current lease reports (decision 0035).
func aLateReportWithAnOldLeaseNumberIsRefused(t *testing.T, p Plane) {
	old := expire(t, p, "runner-a", "invocation-1")
	current := claim(t, p.Runner("runner-b", Gate))
	late := p.Runner("runner-a", Gate)
	_, err := late.Report(t.Context(), report(old))
	wantCode(t, err, connect.CodeAborted)
	_, err = heartbeat(t, late, old)
	wantCode(t, err, connect.CodeAborted)
	_, err = late.Report(t.Context(), report(current))
	wantCode(t, err, connect.CodeAborted)
	if n := typeCount(t, p, events.TypeInvocationResult); n != 0 {
		t.Fatalf("the log holds %d results after the refused reports, want none", n)
	}
	if _, err := p.Runner("runner-b", Gate).Report(t.Context(), report(current)); err != nil {
		t.Fatalf("the report of the current lease: %v", err)
	}
	if n := typeCount(t, p, events.TypeInvocationResult); n != 1 {
		t.Fatalf("the log holds %d results, want 1", n)
	}
}

// verdict returns a verdict.recorded event of invocation about the plan.
func verdict(t *testing.T, invocation string) *cloudeventsv1.CloudEvent {
	t.Helper()
	return build(t, &eventsv1.VerdictRecorded{
		Gate: "tests", GateVersion: "1", Tree: "tree-1", Verdict: eventsv1.VerdictRecorded_VERDICT_PASS,
		Plan: plan, Invocation: invocation,
	}, plan)
}

// reportWritesTheResultTheEventsAndTheEnd: a report of a gate-role
// invocation writes, after the decision of writeVerdict for its verdict,
// invocation.result, the verdict, invocation.finished and lease.released,
// and ends the lease (decisions 0035 and 0156).
func reportWritesTheResultTheEventsAndTheEnd(t *testing.T, p Plane) {
	mustAppend(t, p.Clients, Person, []*cloudeventsv1.CloudEvent{planAdded(t, plan)})
	request(t, p, document("invocation-1", Gate))
	r := p.Runner("runner-a", Gate)
	res := claim(t, r)
	v := verdict(t, "invocation-1")
	out, err := r.Report(t.Context(), report(res, v))
	if err != nil {
		t.Fatalf("Report: %v", err)
	}
	if len(out.GetPositions()) != 4 {
		t.Fatalf("Report wrote the positions %v, want four: the result, the verdict, the end and the release", out.GetPositions())
	}
	want := []string{
		events.TypeInvocationResult, events.TypeVerdictRecorded, events.TypeInvocationFinished, events.TypeLeaseReleased,
	}
	for i, pos := range out.GetPositions() {
		if got := at(t, p.Clients, pos).GetType(); got != want[i] {
			t.Errorf("the event at %d is %s, want %s", pos, got, want[i])
		}
	}
	if got := at(t, p.Clients, out.GetPositions()[1]).GetId(); got != v.GetId() {
		t.Errorf("the verdict written has the id %q, want %q", got, v.GetId())
	}
	d, e := decisionAt(t, p.Clients, out.GetPositions()[0]-1)
	checkRecorded(t, d, e, eventsv1.DecisionMade_OUTCOME_ALLOW)
	if got := e.GetAttributes()[events.ExtensionCause].GetCeString(); got != v.GetId() {
		t.Errorf("the decision before the report has the cause %q, want the verdict %q", got, v.GetId())
	}
	finished := dataOf(t, at(t, p.Clients, out.GetPositions()[2]), &eventsv1.InvocationFinished{})
	if finished.GetInvocation() != "invocation-1" || finished.GetOutcome() != eventsv1.InvocationFinished_OUTCOME_SUCCEEDED {
		t.Errorf("invocation.finished is %v, want invocation-1 succeeded", finished)
	}
	_, err = heartbeat(t, r, res)
	wantCode(t, err, connect.CodeAborted)
	_, err = r.Report(t.Context(), report(res))
	wantCode(t, err, connect.CodeAborted)
}

// anAgentRoleInvocationIsRefusedWhenItReportsAVerdict: an agent-role
// invocation reports nothing beyond its result and its end, so a report
// with a verdict is refused with permission denied and writes nothing; the
// runner still holds the lease and can report without it (decision 0156).
func anAgentRoleInvocationIsRefusedWhenItReportsAVerdict(t *testing.T, p Plane) {
	request(t, p, document("invocation-1", Agent))
	r := p.Runner("runner-a", Agent)
	res := claim(t, r)
	_, err := r.Report(t.Context(), report(res, verdict(t, "invocation-1")))
	wantCode(t, err, connect.CodePermissionDenied)
	for _, typ := range []string{events.TypeInvocationResult, events.TypeVerdictRecorded, events.TypeInvocationFinished} {
		if n := typeCount(t, p, typ); n != 0 {
			t.Fatalf("the log holds %d %s events after the refused report, want none", n, typ)
		}
	}
	if _, err := r.Report(t.Context(), report(res)); err != nil {
		t.Fatalf("the report without the verdict: %v", err)
	}
}

// reportRefusesAnInvalidReport: a report with no outcome, with a result of
// another invocation, with an event of another invocation or with an event
// that the invocation's kind and role may not write is refused, and nothing
// is written (decision 0156).
func reportRefusesAnInvalidReport(t *testing.T, p Plane) {
	request(t, p, document("invocation-1", Gate))
	r := p.Runner("runner-a", Gate)
	res := claim(t, r)
	noOutcome := report(res)
	noOutcome.Outcome = eventsv1.InvocationFinished_OUTCOME_UNSPECIFIED
	otherResult := report(res)
	otherResult.Result.Invocation = "invocation-2"
	merge := build(t, &eventsv1.MergePerformed{Commit: "c"}, plan)
	for _, tt := range []struct {
		name string
		req  *runnerv1.ReportRequest
		code connect.Code
	}{
		{"no outcome", noOutcome, connect.CodeInvalidArgument},
		{"a result of another invocation", otherResult, connect.CodeInvalidArgument},
		{"a verdict of another invocation", report(res, verdict(t, "invocation-2")), connect.CodeInvalidArgument},
		{"a merge from a gate-role check", report(res, merge), connect.CodePermissionDenied},
	} {
		_, err := r.Report(t.Context(), tt.req)
		if connect.CodeOf(err) != tt.code {
			t.Errorf("a report with %s: %v, want the code %v", tt.name, err, tt.code)
		}
	}
	if n := typeCount(t, p, events.TypeInvocationResult); n != 0 {
		t.Fatalf("the log holds %d results after the refused reports, want none", n)
	}
}

// aLogBatchWithAnOldLeaseNumberIsRefused: SendLog takes batches under the
// current lease and refuses, with aborted, a batch under a lease that
// expired (decision 0163).
func aLogBatchWithAnOldLeaseNumberIsRefused(t *testing.T, p Plane) {
	request(t, p, document("invocation-1", Gate))
	a := p.Runner("runner-a", Gate)
	old := claim(t, a)
	batch := func(r runnerv1connect.RunnerServiceClient, res *runnerv1.ClaimResponse) error {
		_, err := r.SendLog(t.Context(), &runnerv1.SendLogRequest{
			Invocation: "invocation-1", LeaseNumber: res.GetLeaseNumber(), Output: []byte("line\n"),
		})
		return err
	}
	if err := batch(a, old); err != nil {
		t.Fatalf("SendLog under the current lease: %v", err)
	}
	p.Advance(old.GetLeaseTimeout().AsDuration())
	b := p.Runner("runner-b", Gate)
	current := claim(t, b)
	wantCode(t, batch(a, old), connect.CodeAborted)
	if err := batch(b, current); err != nil {
		t.Fatalf("SendLog under the new lease: %v", err)
	}
}
