// SPDX-License-Identifier: Apache-2.0
// SPDX-FileCopyrightText: The Pumi Authors

package conformance

import (
	"sync"
	"testing"

	"connectrpc.com/connect/v2"
	"google.golang.org/protobuf/proto"

	"getpumi.dev/pumi/api/events"
	cloudeventsv1 "getpumi.dev/pumi/api/gen/io/cloudevents/v1"
	controlplanev1 "getpumi.dev/pumi/api/gen/pumi/controlplane/v1"
	eventsv1 "getpumi.dev/pumi/api/gen/pumi/events/v1"
)

// appendAssignsDensePositionsInOrder: positions count from 1 in the order
// of writing, with no gaps, across appends (decision 0154).
func appendAssignsDensePositionsInOrder(t *testing.T, c Clients) {
	equal(t, "the positions of the first append",
		mustAppend(t, c, Kernel, []*cloudeventsv1.CloudEvent{escalationRaised(t, "plan-a"), transitionFired(t, "plan-a")}),
		[]uint64{1, 2})
	equal(t, "the positions of the second append",
		mustAppend(t, c, Kernel, []*cloudeventsv1.CloudEvent{escalationRaised(t, "plan-b")}), []uint64{3})
	sent := []*cloudeventsv1.CloudEvent{transitionFired(t, "plan-b"), transitionFired(t, "plan-a"), escalationRaised(t, "plan-c")}
	equal(t, "the positions of the third append", mustAppend(t, c, Kernel, sent), []uint64{4, 5, 6})

	res, err := c(Kernel).Query(t.Context(), &controlplanev1.QueryRequest{After: 3})
	if err != nil {
		t.Fatal(err)
	}
	if res.GetHead() != 6 {
		t.Fatalf("the head is %d, want 6", res.GetHead())
	}
	if len(res.GetEvents()) != len(sent) {
		t.Fatalf("Query after 3 returned %d events, want %d", len(res.GetEvents()), len(sent))
	}
	for i, e := range res.GetEvents() {
		if e.GetPosition() != uint64(4+i) || e.GetEvent().GetId() != sent[i].GetId() {
			t.Fatalf("event %d is %s at %d, want %s at %d",
				i, e.GetEvent().GetId(), e.GetPosition(), sent[i].GetId(), 4+i)
		}
	}
}

// appendWritesTenantAndPrevhash: the control plane writes the tenant from
// the caller's identity and the prevhash that chains the log, whatever the
// request carries (decisions 0153 and 0156).
func appendWritesTenantAndPrevhash(t *testing.T, c Clients) {
	noTenant := escalationRaised(t, "plan-b")
	delete(noTenant.Attributes, events.ExtensionTenant)
	delete(noTenant.Attributes, events.ExtensionPrevHash)
	mustAppend(t, c, Kernel, []*cloudeventsv1.CloudEvent{escalationRaised(t, "plan-a"), noTenant})
	mustAppend(t, c, Kernel, []*cloudeventsv1.CloudEvent{transitionFired(t, "plan-a")})

	res, err := c(Person).Query(t.Context(), &controlplanev1.QueryRequest{})
	if err != nil {
		t.Fatal(err)
	}
	tenant := ""
	for i, e := range res.GetEvents() {
		attrs := e.GetEvent().GetAttributes()
		got := attrs[events.ExtensionTenant].GetCeString()
		if got == "" || got == forgedTenant {
			t.Fatalf("event %d has the tenant %q, want the caller's", e.GetPosition(), got)
		}
		if i > 0 && got != tenant {
			t.Fatalf("event %d has the tenant %q, and event 1 has %q", e.GetPosition(), got, tenant)
		}
		tenant = got
		prev, ok := attrs[events.ExtensionPrevHash]
		switch {
		case i == 0 && ok:
			t.Fatalf("the first event has the prevhash %q, want none", prev.GetCeString())
		case i > 0 && (!ok || prev.GetCeString() == forgedPrevHash):
			t.Fatalf("event %d has the prevhash %q, want the hash of the event before it", e.GetPosition(), prev.GetCeString())
		}
	}
}

// appendChecksSubjectVersions: a condition holds when its subject has no
// events or is at the version given, and otherwise the append fails with
// the subject's current version (decision 0153).
func appendChecksSubjectVersions(t *testing.T, c Clients) {
	mustAppend(t, c, Kernel, []*cloudeventsv1.CloudEvent{escalationRaised(t, "plan-a")}, noEvents("plan-a"))
	mustAppend(t, c, Kernel, []*cloudeventsv1.CloudEvent{escalationRaised(t, "plan-b")}, noEvents("plan-b"), atVersion("plan-a", 1))
	mustAppend(t, c, Kernel, []*cloudeventsv1.CloudEvent{transitionFired(t, "plan-a")}, atVersion("plan-a", 1))

	for name, cond := range map[string]*controlplanev1.Condition{
		"an old version":                     atVersion("plan-a", 1),
		"a version after the current one":    atVersion("plan-a", 3),
		"no events of a subject with events": noEvents("plan-a"),
	} {
		_, err := c(Kernel).Append(t.Context(), &controlplanev1.AppendRequest{
			Events: []*cloudeventsv1.CloudEvent{transitionFired(t, "plan-a")}, Conditions: []*controlplanev1.Condition{cond},
		})
		if got := conflict(t, err); len(got) != 1 || got["plan-a"] != 2 {
			t.Fatalf("the conflict of %s is %v, want plan-a at version 2", name, got)
		}
	}
	_, err := c(Kernel).Append(t.Context(), &controlplanev1.AppendRequest{
		Events: []*cloudeventsv1.CloudEvent{escalationRaised(t, "plan-c")}, Conditions: []*controlplanev1.Condition{atVersion("plan-c", 1)},
	})
	if got := conflict(t, err); len(got) != 1 || got["plan-c"] != 0 {
		t.Fatalf("the conflict of a version of a subject with no events is %v, want plan-c at version 0", got)
	}
	if h := head(t, c); h != 3 {
		t.Fatalf("the head is %d after the failed appends, want 3", h)
	}
}

// appendConflictNamesEveryFailedSubject: the error names each subject
// whose condition failed, and only those.
func appendConflictNamesEveryFailedSubject(t *testing.T, c Clients) {
	mustAppend(t, c, Kernel, []*cloudeventsv1.CloudEvent{escalationRaised(t, "plan-a"), escalationRaised(t, "plan-b"), escalationRaised(t, "plan-c")})
	_, err := c(Kernel).Append(t.Context(), &controlplanev1.AppendRequest{
		Events: []*cloudeventsv1.CloudEvent{transitionFired(t, "plan-a")},
		Conditions: []*controlplanev1.Condition{
			noEvents("plan-a"), atVersion("plan-b", 1), atVersion("plan-c", 2), noEvents("plan-d"),
		},
	})
	got := conflict(t, err)
	if len(got) != 2 || got["plan-a"] != 1 || got["plan-c"] != 1 {
		t.Fatalf("the conflict is %v, want plan-a and plan-c at version 1", got)
	}
}

// batchWhoseSecondConditionFailsWritesNothing: an append writes all its
// events or none (decision 0153).
func batchWhoseSecondConditionFailsWritesNothing(t *testing.T, c Clients) {
	mustAppend(t, c, Kernel, []*cloudeventsv1.CloudEvent{escalationRaised(t, "plan-a")})
	_, err := c(Kernel).Append(t.Context(), &controlplanev1.AppendRequest{
		Events:     []*cloudeventsv1.CloudEvent{transitionFired(t, "plan-a"), escalationRaised(t, "plan-b")},
		Conditions: []*controlplanev1.Condition{atVersion("plan-a", 1), atVersion("plan-b", 4)},
	})
	if got := conflict(t, err); len(got) != 1 || got["plan-b"] != 0 {
		t.Fatalf("the conflict is %v, want plan-b at version 0", got)
	}
	positions, h := query(t, c, &controlplanev1.QueryRequest{})
	equal(t, "the positions after the failed append", positions, []uint64{1})
	if h != 1 {
		t.Fatalf("the head is %d, want 1", h)
	}
	equal(t, "the next positions",
		mustAppend(t, c, Kernel, []*cloudeventsv1.CloudEvent{escalationRaised(t, "plan-b")}, noEvents("plan-b")), []uint64{2})
}

// racers is the number of appends that race on one subject version.
const racers = 8

// twoAppendsRacingOnOneVersion: of appends that all expect the same
// version of a subject, exactly one succeeds, and the others fail with the
// version it made (decision 0153).
func twoAppendsRacingOnOneVersion(t *testing.T, c Clients) {
	mustAppend(t, c, Kernel, []*cloudeventsv1.CloudEvent{escalationRaised(t, "plan-a")})
	batches := make([][]*cloudeventsv1.CloudEvent, racers)
	for i := range batches {
		batches[i] = []*cloudeventsv1.CloudEvent{transitionFired(t, "plan-a"), transitionFired(t, "plan-a")}
	}
	start := make(chan struct{})
	errs := make([]error, racers)
	var wg sync.WaitGroup
	for i := range racers {
		wg.Go(func() {
			<-start
			_, errs[i] = c(Kernel).Append(t.Context(), &controlplanev1.AppendRequest{
				Events: batches[i], Conditions: []*controlplanev1.Condition{atVersion("plan-a", 1)},
			})
		})
	}
	close(start)
	wg.Wait()
	won := 0
	for _, err := range errs {
		if err == nil {
			won++
			continue
		}
		if got := conflict(t, err); len(got) != 1 || got["plan-a"] != 3 {
			t.Fatalf("a losing append's conflict is %v, want plan-a at version 3", got)
		}
	}
	if won != 1 {
		t.Fatalf("%d of %d racing appends succeeded, want exactly 1", won, racers)
	}
	positions, _ := query(t, c, &controlplanev1.QueryRequest{Filter: &controlplanev1.Filter{Subject: "plan-a"}})
	equal(t, "the positions of plan-a", positions, []uint64{1, 2, 3})
}

// appendRefusesAnInvalidRequest: a request with no events, an event that
// is not a valid Pumi event, or a condition that names no subject or no
// valid version fails with invalid argument and writes nothing.
func appendRefusesAnInvalidRequest(t *testing.T, c Clients) {
	unknownType := escalationRaised(t, "plan-a")
	unknownType.Type = events.TypePrefix + "plan.unknown"
	noID := escalationRaised(t, "plan-a")
	noID.Id = ""
	cases := map[string]*controlplanev1.AppendRequest{
		"no events":           {},
		"an unknown type":     {Events: []*cloudeventsv1.CloudEvent{escalationRaised(t, "plan-a"), unknownType}},
		"an event with no id": {Events: []*cloudeventsv1.CloudEvent{noID}},
		"a nil event":         {Events: []*cloudeventsv1.CloudEvent{escalationRaised(t, "plan-a"), nil}},
		"a condition with no subject": {
			Events: []*cloudeventsv1.CloudEvent{escalationRaised(t, "plan-a")}, Conditions: []*controlplanev1.Condition{noEvents("")},
		},
		"a condition with no expected version": {
			Events:     []*cloudeventsv1.CloudEvent{escalationRaised(t, "plan-a")},
			Conditions: []*controlplanev1.Condition{{Subject: "plan-a"}},
		},
		"a condition at version 0": {
			Events: []*cloudeventsv1.CloudEvent{escalationRaised(t, "plan-a")}, Conditions: []*controlplanev1.Condition{atVersion("plan-a", 0)},
		},
	}
	for name, req := range cases {
		_, err := c(Kernel).Append(t.Context(), req)
		if connect.CodeOf(err) != connect.CodeInvalidArgument {
			t.Errorf("an append with %s: %v, want the code invalid_argument", name, err)
		}
	}
	if h := head(t, c); h != 0 {
		t.Fatalf("the head is %d after the invalid appends, want 0", h)
	}
}

// appenderOf gives the appender of the table of decision 0156 that each
// caller appends as through ControlPlaneService. A runner appends only
// through RunnerService, so it has none here.
var appenderOf = map[Caller]events.Appender{
	Kernel: events.AppenderKernel,
	Person: events.AppenderPerson,
}

// appendFollowsTheTable: each caller may append the event types that the
// table of decision 0156 gives it, and no others. The types that only the
// control plane itself or a runner's RunnerService calls append are
// refused to every caller of Append. The decision.made events that the
// control plane writes before a permission's event are not counted.
func appendFollowsTheTable(t *testing.T, c Clients) {
	written := uint64(0)
	for _, caller := range Callers {
		for _, typ := range events.Types() {
			if appendOfType(t, c, caller, typ) {
				written++
			}
		}
	}
	if written == 0 {
		t.Fatal("no append was allowed")
	}
	if got := uint64(len(eventsExcept(t, c, events.TypeDecisionMade))); got != written {
		t.Fatalf("the log holds %d events besides decisions, want %d, the number of allowed appends", got, written)
	}
}

// eventsExcept returns the events of the log whose type is not typ.
func eventsExcept(t *testing.T, c Clients, typ string) []*controlplanev1.Entry {
	t.Helper()
	res, err := c(Kernel).Query(t.Context(), &controlplanev1.QueryRequest{})
	if err != nil {
		t.Fatal(err)
	}
	out := []*controlplanev1.Entry{}
	for _, e := range res.GetEvents() {
		if e.GetEvent().GetType() != typ {
			out = append(out, e)
		}
	}
	return out
}

// appendOfType appends an event of typ as caller, checks that it is
// written when the table allows it and refused with permission denied when
// it does not, and reports whether it was written.
func appendOfType(t *testing.T, c Clients, caller Caller, typ string) bool {
	t.Helper()
	_, err := c(caller).Append(t.Context(), &controlplanev1.AppendRequest{Events: []*cloudeventsv1.CloudEvent{ofType(t, typ)}})
	a, ok := appenderOf[caller]
	if caller == Kernel && typ == events.TypeInvocationFinished {
		// The kernel finishes only an invocation in doubt, with the outcome
		// resolved on recovery, which TheKernelFinishesOnlyAnInvocationInDoubt
		// shows (decision 0202); this one has neither.
		if connect.CodeOf(err) != connect.CodeInvalidArgument {
			t.Errorf("a %s appending %s with no outcome: %v, want the code invalid_argument", caller, typ, err)
		}
		return false
	}
	if ok && events.MayAppend(typ, a) {
		if err != nil {
			t.Errorf("a %s appending %s: %v, want it written", caller, typ, err)
			return false
		}
		return true
	}
	if connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Errorf("a %s appending %s: %v, want the code permission_denied", caller, typ, err)
	}
	return false
}

// ofType returns a valid event of typ, with data that holds the minimum
// its type needs.
func ofType(t *testing.T, typ string) *cloudeventsv1.CloudEvent {
	t.Helper()
	data, err := events.Data(typ)
	if err != nil {
		t.Fatal(err)
	}
	data = proto.Clone(data)
	if a, ok := data.(*eventsv1.PlanAdded); ok {
		a.Plan, a.Track = "subject-of-"+typ, "main"
	}
	if o, ok := data.(*eventsv1.OverrideRecorded); ok {
		o.Reason = "the conformance suite"
		o.Plan = "plan-of-" + typ
	}
	if s, ok := data.(*eventsv1.ModelRoleSwitched); ok {
		// A person switches a model role back (decision 0246).
		s.Role, s.Adapter, s.Model, s.SwitchedBy = "frontier", "claude-code", "claude-fable-5-2", "the conformance suite"
		s.Limit = &eventsv1.ModelRoleSwitched_Limit{Adapter: "claude-code", Model: "claude-fable-5-2"}
	}
	if typ == events.TypeInvocationRequested {
		// A request carries a valid document (decision 0165).
		if data, err = events.InvocationRequested("plan-of-"+typ, document("invocation-of-"+typ, Gate)); err != nil {
			t.Fatal(err)
		}
	}
	return build(t, data, "subject-of-"+typ)
}

// batchWithARefusedTypeWritesNothing: an append whose events include one
// type the caller may not append writes none of them.
func batchWithARefusedTypeWritesNothing(t *testing.T, c Clients) {
	_, err := c(Kernel).Append(t.Context(), &controlplanev1.AppendRequest{
		Events: []*cloudeventsv1.CloudEvent{escalationRaised(t, "plan-a"), ofType(t, events.TypeApprovalRecorded)},
	})
	wantCode(t, err, connect.CodePermissionDenied)
	_, err = c(Person).Append(t.Context(), &controlplanev1.AppendRequest{
		Events: []*cloudeventsv1.CloudEvent{ofType(t, events.TypeApprovalRecorded), ofType(t, events.TypeLeaseGranted)},
	})
	wantCode(t, err, connect.CodePermissionDenied)
	if h := head(t, c); h != 0 {
		t.Fatalf("the head is %d after the refused appends, want 0", h)
	}
}

// runnerMayNotCallControlPlaneService: a runner uses RunnerService only
// (decision 0152), so each verb of ControlPlaneService refuses it.
func runnerMayNotCallControlPlaneService(t *testing.T, c Clients) {
	mustAppend(t, c, Kernel, []*cloudeventsv1.CloudEvent{escalationRaised(t, "plan-a")})
	_, err := c(Runner).Append(t.Context(), &controlplanev1.AppendRequest{
		Events: []*cloudeventsv1.CloudEvent{ofType(t, events.TypeInvocationStarted)},
	})
	wantCode(t, err, connect.CodePermissionDenied)
	_, err = c(Runner).Query(t.Context(), &controlplanev1.QueryRequest{})
	wantCode(t, err, connect.CodePermissionDenied)
	stream, err := c(Runner).Subscribe(t.Context(), &controlplanev1.SubscribeRequest{})
	if err == nil {
		_, err = stream.Receive()
		_ = stream.Close()
	}
	wantCode(t, err, connect.CodePermissionDenied)
}

// addedTo returns a dev.getpumi.plan.added event that adds plan to track,
// depending on dependsOn.
func addedTo(t *testing.T, plan, track string, dependsOn ...string) *cloudeventsv1.CloudEvent {
	t.Helper()
	return build(t, &eventsv1.PlanAdded{Plan: plan, Track: track, DependsOn: dependsOn}, plan)
}

// appendChecksAddedPlans: a plan.added names a plan and a track and has
// the plan as its subject, or the append is refused with invalid
// argument. A plan.added of a plan that the log or the same append already
// adds, or that depends on a plan not added earlier in the same track, in
// the log or earlier in the same append, is refused with failed
// precondition, and nothing of the append is written (decision 0196).
func appendChecksAddedPlans(t *testing.T, c Clients) {
	equal(t, "the positions of the plans of two tracks",
		mustAppend(t, c, Person, []*cloudeventsv1.CloudEvent{
			addedTo(t, "a1", "a"), addedTo(t, "a2", "a", "a1"), addedTo(t, "b1", "b"),
		}), []uint64{1, 2, 3})
	equal(t, "the position of a plan that depends on two",
		mustAppend(t, c, Person, []*cloudeventsv1.CloudEvent{addedTo(t, "a3", "a", "a1", "a2")}), []uint64{4})

	other := addedTo(t, "a4", "a")
	other.Attributes["subject"] = stringValue("a5")
	invalid := map[string][]*cloudeventsv1.CloudEvent{
		"no plan":                        {build(t, &eventsv1.PlanAdded{Track: "a"}, "")},
		"no track":                       {addedTo(t, "a4", "")},
		"a subject that is not the plan": {other},
	}
	for name, evs := range invalid {
		_, err := c(Person).Append(t.Context(), &controlplanev1.AppendRequest{Events: evs})
		if connect.CodeOf(err) != connect.CodeInvalidArgument {
			t.Errorf("a plan.added with %s: %v, want the code invalid_argument", name, err)
		}
	}
	refused := map[string][]*cloudeventsv1.CloudEvent{
		"a plan the log adds":             {addedTo(t, "a1", "a")},
		"a plan the same append adds":     {addedTo(t, "c1", "c"), addedTo(t, "c1", "c")},
		"a dependency never added":        {addedTo(t, "a4", "a", "z")},
		"a dependency in another track":   {addedTo(t, "b2", "b", "a1")},
		"a dependency on itself":          {addedTo(t, "a4", "a", "a4")},
		"a dependency added after it":     {addedTo(t, "a4", "a", "a5"), addedTo(t, "a5", "a")},
		"a refused plan after a good one": {addedTo(t, "c2", "c"), addedTo(t, "a4", "a", "z")},
	}
	for name, evs := range refused {
		_, err := c(Person).Append(t.Context(), &controlplanev1.AppendRequest{Events: evs})
		if connect.CodeOf(err) != connect.CodeFailedPrecondition {
			t.Errorf("a plan.added with %s: %v, want the code failed_precondition", name, err)
		}
	}
	if h := head(t, c); h != 4 {
		t.Fatalf("the head is %d after the refused appends, want 4", h)
	}
}

// stringValue returns s as a string attribute.
func stringValue(s string) *cloudeventsv1.CloudEvent_CloudEventAttributeValue {
	return &cloudeventsv1.CloudEvent_CloudEventAttributeValue{Attr: &cloudeventsv1.CloudEvent_CloudEventAttributeValue_CeString{CeString: s}}
}
