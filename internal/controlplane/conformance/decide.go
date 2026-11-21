// SPDX-License-Identifier: Apache-2.0
// SPDX-FileCopyrightText: The Pumi Authors

package conformance

import (
	"slices"
	"testing"

	"connectrpc.com/connect/v2"
	"google.golang.org/protobuf/proto"

	"getpumi.dev/pumi/api/events"
	cloudeventsv1 "getpumi.dev/pumi/api/gen/io/cloudevents/v1"
	controlplanev1 "getpumi.dev/pumi/api/gen/pumi/controlplane/v1"
	eventsv1 "getpumi.dev/pumi/api/gen/pumi/events/v1"
	factsv1 "getpumi.dev/pumi/api/gen/pumi/facts/v1"
)

// fired returns a dev.getpumi.transition.fired event of plan into the
// state of gate.
func fired(t *testing.T, plan, gate string) *cloudeventsv1.CloudEvent {
	t.Helper()
	return build(t, &eventsv1.TransitionFired{Plan: plan, FromState: "a", ToState: gate, Trigger: "t", Gate: gate}, plan)
}

// at returns the event at position p of the log.
func at(t *testing.T, c Clients, p uint64) *cloudeventsv1.CloudEvent {
	t.Helper()
	res, err := c(Kernel).Query(t.Context(), &controlplanev1.QueryRequest{After: p - 1, Limit: 1})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.GetEvents()) != 1 || res.GetEvents()[0].GetPosition() != p {
		t.Fatalf("the log has no event at %d", p)
	}
	return res.GetEvents()[0].GetEvent()
}

// decisionAt returns the data of the decision.made event at position p,
// and the event.
func decisionAt(t *testing.T, c Clients, p uint64) (*eventsv1.DecisionMade, *cloudeventsv1.CloudEvent) {
	t.Helper()
	e := at(t, c, p)
	if e.GetType() != events.TypeDecisionMade {
		t.Fatalf("the event at %d is %s, want %s", p, e.GetType(), events.TypeDecisionMade)
	}
	var d eventsv1.DecisionMade
	if err := e.GetProtoData().UnmarshalTo(&d); err != nil {
		t.Fatal(err)
	}
	return &d, e
}

// prevhash returns the prevhash attribute of e.
func prevhash(e *cloudeventsv1.CloudEvent) string {
	return e.GetAttributes()[events.ExtensionPrevHash].GetCeString()
}

// checkRecorded checks what every recorded decision holds: the outcome,
// a policy set digest, the enforced mode, and the log head it was made at,
// which is the hash of the event before it when nothing was appended in
// between.
func checkRecorded(t *testing.T, d *eventsv1.DecisionMade, e *cloudeventsv1.CloudEvent, outcome eventsv1.DecisionMade_Outcome) {
	t.Helper()
	if d.GetOutcome() != outcome {
		t.Errorf("the decision's outcome is %v, want %v", d.GetOutcome(), outcome)
	}
	if d.GetPolicySetDigest() == "" {
		t.Error("the decision has no policy set digest")
	}
	if d.GetMode() != eventsv1.DecisionMade_MODE_ENFORCED {
		t.Errorf("the decision's mode is %v, want enforced", d.GetMode())
	}
	if d.GetLogHead() != prevhash(e) {
		t.Errorf("the decision was made at the head %q, want %q, the hash of the event before it", d.GetLogHead(), prevhash(e))
	}
}

// decisionIsRecordedWithItsBasis: Decide projects the facts that the
// action's context declares from the log, appends decision.made with the
// facts it found as the basis and the log head, and returns the outcome
// and the event's position (decisions 0015 and 0155). The dco gate reads
// attempts, which the plan's transitions into the gate give, and facts
// that nothing in this log gives, so it is denied.
func decisionIsRecordedWithItsBasis(t *testing.T, c Clients) {
	mustAppend(t, c, Kernel, []*cloudeventsv1.CloudEvent{
		planAdded(t, "plan-a"), fired(t, "plan-a", "dco"), fired(t, "plan-a", "spec"), fired(t, "plan-a", "dco"),
	})
	res, err := c(Kernel).Decide(t.Context(), &controlplanev1.DecideRequest{
		Action: "dco", Plan: "plan-a", Keys: &controlplanev1.FactKeys{HeadTree: "tree-1", HeadCommit: "commit-1"},
	})
	if err != nil {
		t.Fatalf("Decide: %v", err)
	}
	if res.GetPosition() != 5 {
		t.Fatalf("the decision is at %d, want 5, after the four events", res.GetPosition())
	}
	if res.GetOutcome() != eventsv1.DecisionMade_OUTCOME_DENY {
		t.Fatalf("the outcome is %v, want deny, because dco.signed and usage are missing", res.GetOutcome())
	}
	d, e := decisionAt(t, c, res.GetPosition())
	checkRecorded(t, d, e, res.GetOutcome())
	if !slices.Equal(d.GetDeterminingPolicies(), res.GetDeterminingPolicies()) {
		t.Errorf("the recorded determining policies are %q, and Decide returned %q", d.GetDeterminingPolicies(), res.GetDeterminingPolicies())
	}
	want := &factsv1.Attempts{Plan: "plan-a", Gate: "dco", Value: 2}
	found := false
	for _, f := range d.GetBasis().GetFacts() {
		var a factsv1.Attempts
		if f.MessageIs(&a) {
			if err := f.UnmarshalTo(&a); err != nil {
				t.Fatal(err)
			}
			if !proto.Equal(&a, want) {
				t.Errorf("the basis has the attempts %v, want %v", &a, want)
			}
			found = true
		}
	}
	if !found {
		t.Errorf("the basis %v has no attempts fact, want %v", d.GetBasis(), want)
	}
}

// aMissingFactDenies: a fact that nothing has produced is missing from the
// context, and the decision is a deny that is recorded (decision 0143).
// Nothing in the log gives the facts that merge reads.
func aMissingFactDenies(t *testing.T, c Clients) {
	mustAppend(t, c, Kernel, []*cloudeventsv1.CloudEvent{planAdded(t, "plan-a")})
	res, err := c(Kernel).Decide(t.Context(), &controlplanev1.DecideRequest{
		Action: "merge", Plan: "plan-a", Keys: &controlplanev1.FactKeys{BaseTree: "tree-0", HeadTree: "tree-1", HeadCommit: "commit-1"},
	})
	if err != nil {
		t.Fatalf("Decide: %v", err)
	}
	if res.GetOutcome() != eventsv1.DecisionMade_OUTCOME_DENY {
		t.Fatalf("the outcome is %v, want deny", res.GetOutcome())
	}
	d, e := decisionAt(t, c, res.GetPosition())
	checkRecorded(t, d, e, eventsv1.DecisionMade_OUTCOME_DENY)
	if n := len(d.GetBasis().GetFacts()); n != 0 {
		t.Errorf("the basis has %d facts, want none", n)
	}
}

// decideRefusesARequestItCannotDecide: a request with no plan or an action
// that the schema does not have is refused with invalid argument, and
// nothing is recorded.
func decideRefusesARequestItCannotDecide(t *testing.T, c Clients) {
	for name, req := range map[string]*controlplanev1.DecideRequest{
		"no plan":           {Action: "merge"},
		"no action":         {Plan: "plan-a"},
		"an unknown action": {Action: "launch", Plan: "plan-a"},
	} {
		_, err := c(Kernel).Decide(t.Context(), req)
		if connect.CodeOf(err) != connect.CodeInvalidArgument {
			t.Errorf("Decide with %s: %v, want the code invalid_argument", name, err)
		}
	}
	_, err := c(Runner).Decide(t.Context(), &controlplanev1.DecideRequest{Action: "merge", Plan: "plan-a"})
	wantCode(t, err, connect.CodePermissionDenied)
	if h := head(t, c); h != 0 {
		t.Fatalf("the head is %d after the refused requests, want 0", h)
	}
}

// aVerdictFromOutsideTheGateRoleIsRefused: only a runner in the gate role
// writes a verdict (decisions 0142 and 0156), so the kernel and a person
// cannot append one, and nothing is written.
func aVerdictFromOutsideTheGateRoleIsRefused(t *testing.T, c Clients) {
	verdict := &eventsv1.VerdictRecorded{
		Gate: "planReview", GateVersion: "1", Tree: "tree-1", Verdict: eventsv1.VerdictRecorded_VERDICT_PASS,
		Plan: "plan-a", Invocation: "invocation-1",
	}
	for _, caller := range []Caller{Kernel, Person} {
		_, err := c(caller).Append(t.Context(), &controlplanev1.AppendRequest{
			Events: []*cloudeventsv1.CloudEvent{build(t, verdict, "plan-a")},
		})
		if connect.CodeOf(err) != connect.CodePermissionDenied {
			t.Errorf("a verdict from the %s: %v, want the code permission_denied", caller, err)
		}
	}
	if n := len(eventsExcept(t, c, events.TypeDecisionMade)); n != 0 {
		t.Fatalf("the log holds %d events besides decisions after the refused verdicts, want none", n)
	}
}

// aPermissionIsDecidedAndRecordedBeforeItsEvent: before it appends an
// approval, an override or a cancel, the control plane decides the
// matching permission and appends the decision with the event, just
// before it, with the event's id as its cause (decision 0156).
func aPermissionIsDecidedAndRecordedBeforeItsEvent(t *testing.T, c Clients) {
	mustAppend(t, c, Kernel, []*cloudeventsv1.CloudEvent{planAdded(t, "plan-a")})
	cases := []struct {
		caller Caller
		event  *cloudeventsv1.CloudEvent
	}{
		{Person, build(t, &eventsv1.ApprovalRecorded{
			Person: "someone", Tree: "tree-1", Answer: eventsv1.ApprovalRecorded_ANSWER_APPROVED,
		}, "plan-a")},
		{Person, build(t, &eventsv1.OverrideRecorded{
			Person: "someone", Decision: "d", Gate: "dco", Tree: "tree-1", Reason: "the conformance suite", Plan: "plan-a",
		}, "plan-a")},
		{Person, build(t, &eventsv1.CancelRequested{Invocation: "invocation-1", RequestedBy: "someone"}, "plan-a")},
		{Kernel, build(t, &eventsv1.CancelRequested{Invocation: "invocation-2", RequestedBy: "kernel"}, "plan-a")},
	}
	for _, tt := range cases {
		positions := mustAppend(t, c, tt.caller, []*cloudeventsv1.CloudEvent{tt.event})
		if len(positions) != 1 {
			t.Fatalf("the append of a %s returned the positions %v, want one", tt.event.GetType(), positions)
		}
		if got := at(t, c, positions[0]); got.GetId() != tt.event.GetId() {
			t.Fatalf("the event at %d is %s, want %s", positions[0], got.GetId(), tt.event.GetId())
		}
		d, e := decisionAt(t, c, positions[0]-1)
		checkRecorded(t, d, e, eventsv1.DecisionMade_OUTCOME_ALLOW)
		if got := e.GetAttributes()[events.ExtensionCause].GetCeString(); got != tt.event.GetId() {
			t.Errorf("the decision before the %s has the cause %q, want the event's id %q", tt.event.GetType(), got, tt.event.GetId())
		}
	}
}
