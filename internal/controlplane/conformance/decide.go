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
// Nothing in the log gives the Boolean and record facts that merge reads,
// so the basis holds only the facts that start at zero or empty once the
// plan exists (decisions 0174 and 0175).
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
	empty := map[string]bool{
		"pumi.facts.v1.ApprovedBy": true, "pumi.facts.v1.Attempts": true, "pumi.facts.v1.Overrides": true,
		"pumi.facts.v1.QuestionsOpen": true, "pumi.facts.v1.Usage": true, "pumi.facts.v1.WarningsOpen": true,
	}
	for _, f := range d.GetBasis().GetFacts() {
		if name := string(f.MessageName()); !empty[name] {
			t.Errorf("the basis has the fact %s, which nothing in the log gives", name)
		}
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
		{Person, build(t, &eventsv1.CancelRequested{Plan: "plan-a", RequestedBy: "someone"}, "plan-a")},
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

// theWorkflowActionsAreDecided: Decide knows the five workflow actions
// (decisions 0169 and 0171) and records each decision. escalate reads no
// facts, and a core policy permits it for the kernel. retry reads the
// attempts of the gate of the state the plan is in, which its last
// transition entered.
func theWorkflowActionsAreDecided(t *testing.T, c Clients) {
	mustAppend(t, c, Kernel, []*cloudeventsv1.CloudEvent{
		planAdded(t, "plan-b"), fired(t, "plan-b", "planReview"), fired(t, "plan-b", "planReview"),
	})
	decide := func(action string) (*controlplanev1.DecideResponse, *eventsv1.DecisionMade) {
		t.Helper()
		res, err := c(Kernel).Decide(t.Context(), &controlplanev1.DecideRequest{
			Action: action, Plan: "plan-b", Keys: &controlplanev1.FactKeys{Invocation: "invocation-1"},
		})
		if err != nil {
			t.Fatalf("Decide %s: %v", action, err)
		}
		d, e := decisionAt(t, c, res.GetPosition())
		checkRecorded(t, d, e, res.GetOutcome())
		return res, d
	}
	for _, action := range []string{"advance", "resume", "ask"} {
		decide(action)
	}
	res, _ := decide("escalate")
	if res.GetOutcome() != eventsv1.DecisionMade_OUTCOME_ALLOW || !slices.Contains(res.GetDeterminingPolicies(), "core.escalate") {
		t.Errorf("escalate: %v by %q, want an allow by core.escalate", res.GetOutcome(), res.GetDeterminingPolicies())
	}
	_, d := decide("retry")
	want := &factsv1.Attempts{Plan: "plan-b", Gate: "planReview", Value: 2}
	if got := attemptsOf(t, d); !proto.Equal(got, want) {
		t.Errorf("retry: the basis is %v, want the attempts %v", d.GetBasis(), want)
	}
}

// attemptsOf returns the attempts fact in the basis of d, or nil.
func attemptsOf(t *testing.T, d *eventsv1.DecisionMade) *factsv1.Attempts {
	t.Helper()
	for _, f := range d.GetBasis().GetFacts() {
		var a factsv1.Attempts
		if f.MessageIs(&a) {
			if err := f.UnmarshalTo(&a); err != nil {
				t.Fatal(err)
			}
			return &a
		}
	}
	return nil
}

// decideFor has the kernel decide action for plan with keys, checks that
// the decision is recorded, and returns the response and the decision.
func decideFor(t *testing.T, c Clients, action, plan string, keys *controlplanev1.FactKeys) (*controlplanev1.DecideResponse, *eventsv1.DecisionMade) {
	t.Helper()
	res, err := c(Kernel).Decide(t.Context(), &controlplanev1.DecideRequest{Action: action, Plan: plan, Keys: keys})
	if err != nil {
		t.Fatalf("Decide %s: %v", action, err)
	}
	d, e := decisionAt(t, c, res.GetPosition())
	checkRecorded(t, d, e, res.GetOutcome())
	return res, d
}

// aFreshlyAddedPlanMayMove: once a plan is added, its count and set facts
// are 0 or empty, and its usage with no invocation named is zero
// (decisions 0174 and 0175), so escalate and advance are allowed with no
// other event, and a retry once the plan has entered a gate (decision
// 0169). ask reads questions.open, 0, so it is denied. A gate's decision
// shows its empty facts in the basis.
func aFreshlyAddedPlanMayMove(t *testing.T, c Clients) {
	mustAppend(t, c, Kernel, []*cloudeventsv1.CloudEvent{planAdded(t, "plan-f")})
	keys := &controlplanev1.FactKeys{HeadTree: "tree-1", HeadCommit: "commit-1"}
	for _, action := range []string{"escalate", "advance"} {
		if res, d := decideFor(t, c, action, "plan-f", keys); res.GetOutcome() != eventsv1.DecisionMade_OUTCOME_ALLOW {
			t.Errorf("%s of a freshly added plan: %v by %q, basis %v; want allow", action, res.GetOutcome(), res.GetDeterminingPolicies(), d.GetBasis())
		}
	}
	if res, _ := decideFor(t, c, "ask", "plan-f", keys); res.GetOutcome() != eventsv1.DecisionMade_OUTCOME_DENY ||
		!slices.Contains(res.GetDeterminingPolicies(), "core.questions-open") {
		t.Errorf("ask with no open question: %v by %q, want a deny by core.questions-open", res.GetOutcome(), res.GetDeterminingPolicies())
	}
	_, d := decideFor(t, c, "spec", "plan-f", keys)
	wantFacts(t, d, &factsv1.Attempts{Plan: "plan-f", Gate: "spec"}, &factsv1.Overrides{Plan: "plan-f", Tree: "tree-1"},
		&factsv1.WarningsOpen{Project: "pumi"}, &factsv1.Usage{Plan: "plan-f", Value: &factsv1.Usage_Totals{}})
	mustAppend(t, c, Kernel, []*cloudeventsv1.CloudEvent{fired(t, "plan-f", "planReview")})
	res, d := decideFor(t, c, "retry", "plan-f", keys)
	if res.GetOutcome() != eventsv1.DecisionMade_OUTCOME_ALLOW {
		t.Errorf("retry of a plan in its first attempt of a gate: %v by %q, basis %v; want allow", res.GetOutcome(), res.GetDeterminingPolicies(), d.GetBasis())
	}
	wantFacts(t, d, &factsv1.Attempts{Plan: "plan-f", Gate: "planReview", Value: 1})
}

// aPlanNeverAddedIsDenied: a plan with no plan.added is unknown, so
// escalate, advance and retry are denied and recorded, even with a
// transition of the plan into a gate in the log (decisions 0143 and 0174).
func aPlanNeverAddedIsDenied(t *testing.T, c Clients) {
	mustAppend(t, c, Kernel, []*cloudeventsv1.CloudEvent{planAdded(t, "plan-a"), fired(t, "plan-z", "planReview")})
	for _, action := range []string{"escalate", "advance", "retry"} {
		res, d := decideFor(t, c, action, "plan-z", &controlplanev1.FactKeys{HeadTree: "tree-1"})
		if res.GetOutcome() != eventsv1.DecisionMade_OUTCOME_DENY {
			t.Errorf("%s of a plan never added: %v by %q, want deny", action, res.GetOutcome(), res.GetDeterminingPolicies())
		}
		if n := len(d.GetBasis().GetFacts()); n != 0 {
			t.Errorf("%s of a plan never added: the basis has %d facts, want none", action, n)
		}
	}
}

// wantFacts checks that the basis of d holds each fact of want.
func wantFacts(t *testing.T, d *eventsv1.DecisionMade, want ...proto.Message) {
	t.Helper()
	for _, w := range want {
		found := false
		for _, f := range d.GetBasis().GetFacts() {
			got, err := f.UnmarshalNew()
			if err == nil && proto.Equal(got, w) {
				found = true
			}
		}
		if !found {
			t.Errorf("the basis %v has no fact %v", d.GetBasis(), w)
		}
	}
}
