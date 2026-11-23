// SPDX-License-Identifier: Apache-2.0
// SPDX-FileCopyrightText: The Pumi Authors

package projections_test

import (
	"fmt"
	"slices"
	"testing"

	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"

	"getpumi.dev/pumi/api/events"
	"getpumi.dev/pumi/api/facts"
	cloudeventsv1 "getpumi.dev/pumi/api/gen/io/cloudevents/v1"
	eventsv1 "getpumi.dev/pumi/api/gen/pumi/events/v1"
	factsv1 "getpumi.dev/pumi/api/gen/pumi/facts/v1"
	harnessv1 "getpumi.dev/pumi/api/gen/pumi/harness/v1"
	runnerv1 "getpumi.dev/pumi/api/gen/pumi/runner/v1"
	workflowv1 "getpumi.dev/pumi/api/gen/pumi/workflow/v1"
	"getpumi.dev/pumi/internal/controlplane/projections"
)

// event returns an event with data, numbered n so that ids differ.
func event(t *testing.T, n int, data proto.Message) *cloudeventsv1.CloudEvent {
	t.Helper()
	e, err := events.New(data, events.Attributes{ID: fmt.Sprintf("e%d", n), Source: "/test", Tenant: "local"})
	if err != nil {
		t.Fatal(err)
	}
	return e
}

// fired returns a transition.fired event into the gate of plan.
func fired(t *testing.T, n int, plan, gate string) *cloudeventsv1.CloudEvent {
	t.Helper()
	return event(t, n, &eventsv1.TransitionFired{Plan: plan, FromState: "a", ToState: "b", Trigger: "t", Gate: gate})
}

// byFact returns the registered projection of the fact name.
func byFact(t *testing.T, name string) projections.Projection {
	t.Helper()
	for _, p := range projections.All() {
		if p.Fact == name {
			return p
		}
	}
	t.Fatalf("no projection is registered for %s", name)
	return projections.Projection{}
}

// project runs p on evs with keys, keeping only the events of p's types,
// as the control plane does, and fails the test on an error.
func project(t *testing.T, p projections.Projection, keys projections.Keys, evs []*cloudeventsv1.CloudEvent) proto.Message {
	t.Helper()
	kept := []*cloudeventsv1.CloudEvent{}
	for _, e := range evs {
		if slices.Contains(p.Types, e.GetType()) {
			kept = append(kept, e)
		}
	}
	got, err := p.Project(keys, kept)
	if err != nil {
		t.Fatalf("projecting %s: %v", p.Fact, err)
	}
	return got
}

func TestAttemptsCountsTheTransitionsIntoTheGateOfThePlan(t *testing.T) {
	p := byFact(t, "attempts")
	keys := projections.Keys{Plan: "plan-a", Gate: "dco"}
	log := []*cloudeventsv1.CloudEvent{
		fired(t, 1, "plan-a", "dco"),
		fired(t, 2, "plan-a", "spec"),
		fired(t, 3, "plan-b", "dco"),
		event(t, 4, &eventsv1.PlanAdded{Plan: "plan-a", Track: "main"}),
		fired(t, 5, "plan-a", "dco"),
		fired(t, 6, "plan-a", ""),
	}
	want := &factsv1.Attempts{Plan: "plan-a", Gate: "dco", Value: 2}
	if got := project(t, p, keys, log); !proto.Equal(got, want) {
		t.Fatalf("attempts = %v, want %v", got, want)
	}
	if got := project(t, p, projections.Keys{Plan: "plan-a", Gate: "scope"}, log); !proto.Equal(got, &factsv1.Attempts{Plan: "plan-a", Gate: "scope"}) {
		t.Fatalf("attempts of an added plan that never entered the gate = %v, want 0 (decision 0174)", got)
	}
	if got := project(t, p, projections.Keys{Plan: "plan-b", Gate: "dco"}, log); got != nil {
		t.Fatalf("attempts of a plan that was never added = %v, want no fact", got)
	}
	if got := project(t, p, projections.Keys{Plan: "plan-a"}, log); got != nil {
		t.Fatalf("attempts with no gate = %v, want no fact", got)
	}
}

// added returns a plan.added event of plan.
func added(t *testing.T, n int, plan string) *cloudeventsv1.CloudEvent {
	t.Helper()
	return event(t, n, &eventsv1.PlanAdded{Plan: plan, Track: "main"})
}

// override returns an override.recorded event of gate for plan and tree.
func override(t *testing.T, n int, plan, tree, gate string) *cloudeventsv1.CloudEvent {
	t.Helper()
	return event(t, n, &eventsv1.OverrideRecorded{Person: "someone", Decision: "d", Gate: gate, Tree: tree, Reason: "r", Plan: plan})
}

func TestOverridesAreTheGatesOverriddenForThePlanAndTree(t *testing.T) {
	p := byFact(t, "overrides")
	log := []*cloudeventsv1.CloudEvent{
		added(t, 1, "plan-a"),
		override(t, 2, "plan-a", "tree-1", "scope"),
		override(t, 3, "plan-a", "tree-1", "dco"),
		override(t, 4, "plan-a", "tree-2", "budgets"),
		override(t, 5, "plan-b", "tree-1", "spec"),
		override(t, 6, "plan-a", "tree-1", "scope"),
	}
	want := &factsv1.Overrides{Plan: "plan-a", Tree: "tree-1", Value: []string{"dco", "scope"}}
	if got := project(t, p, projections.Keys{Plan: "plan-a", HeadTree: "tree-1"}, log); !proto.Equal(got, want) {
		t.Fatalf("overrides = %v, want %v", got, want)
	}
	if got := project(t, p, projections.Keys{Plan: "plan-a", HeadTree: "tree-3"}, log); !proto.Equal(got, &factsv1.Overrides{Plan: "plan-a", Tree: "tree-3"}) {
		t.Fatalf("overrides of a tree with none = %v, want the empty set (decision 0174)", got)
	}
	if got := project(t, p, projections.Keys{Plan: "plan-b", HeadTree: "tree-1"}, log); got != nil {
		t.Fatalf("overrides of a plan that was never added = %v, want no fact", got)
	}
	if got := project(t, p, projections.Keys{Plan: "plan-a"}, log); got != nil {
		t.Fatalf("overrides with no tree = %v, want no fact", got)
	}
}

// warning returns a warning.raised event with the id w, in project.
func warning(t *testing.T, w, project string) *cloudeventsv1.CloudEvent {
	t.Helper()
	e, err := events.New(&eventsv1.WarningRaised{Gate: "budgets", Tree: "tree-1", Message: "m", Project: project},
		events.Attributes{ID: w, Source: "/test", Tenant: "local"})
	if err != nil {
		t.Fatal(err)
	}
	return e
}

func TestWarningsOpenAreTheRaisedWarningsOfTheProjectNotReviewed(t *testing.T) {
	p := byFact(t, "warnings.open")
	log := []*cloudeventsv1.CloudEvent{
		warning(t, "w1", "project-a"),
		warning(t, "w2", "project-a"),
		warning(t, "w3", "project-b"),
		warning(t, "w4", "project-a"),
		event(t, 5, &eventsv1.WarningReviewed{Warning: "w2", Person: "someone"}),
		event(t, 6, &eventsv1.WarningReviewed{Warning: "w2", Person: "someone else"}),
		event(t, 7, &eventsv1.WarningReviewed{Warning: "w3", Person: "someone"}),
	}
	want := &factsv1.WarningsOpen{Project: "project-a", Value: 2}
	if got := project(t, p, projections.Keys{Project: "project-a", Head: 7}, log); !proto.Equal(got, want) {
		t.Fatalf("warnings.open = %v, want %v", got, want)
	}
	if got := project(t, p, projections.Keys{Project: "project-c", Head: 1}, nil); !proto.Equal(got, &factsv1.WarningsOpen{Project: "project-c"}) {
		t.Fatalf("warnings.open of a project whose log has events but no warnings = %v, want 0 (decision 0174)", got)
	}
	if got := project(t, p, projections.Keys{Project: "project-c"}, nil); got != nil {
		t.Fatalf("warnings.open of a project whose log is empty = %v, want no fact", got)
	}
	if got := project(t, p, projections.Keys{Head: 7}, log); got != nil {
		t.Fatalf("warnings.open with no project = %v, want no fact", got)
	}
}

// approval returns an approval.recorded event of person for tree.
func approval(t *testing.T, n int, person, tree string, answer eventsv1.ApprovalRecorded_Answer) *cloudeventsv1.CloudEvent {
	t.Helper()
	return event(t, n, &eventsv1.ApprovalRecorded{Person: person, Tree: tree, Answer: answer})
}

func TestApprovedByAreThePeopleWhoseLastAnswerForTheTreeApproved(t *testing.T) {
	p := byFact(t, "approved.by")
	yes, no := eventsv1.ApprovalRecorded_ANSWER_APPROVED, eventsv1.ApprovalRecorded_ANSWER_REJECTED
	log := []*cloudeventsv1.CloudEvent{
		added(t, 1, "plan-a"),
		approval(t, 2, "carol", "tree-1", yes),
		approval(t, 3, "alice", "tree-1", yes),
		approval(t, 4, "bob", "tree-1", yes),
		approval(t, 5, "bob", "tree-1", no),
		approval(t, 6, "dan", "tree-2", yes),
		approval(t, 7, "erin", "tree-1", no),
		approval(t, 8, "alice", "tree-1", yes),
	}
	want := &factsv1.ApprovedBy{Tree: "tree-1", Value: []string{"alice", "carol"}}
	if got := project(t, p, projections.Keys{Plan: "plan-a", HeadTree: "tree-1"}, log); !proto.Equal(got, want) {
		t.Fatalf("approved.by = %v, want %v", got, want)
	}
	if got := project(t, p, projections.Keys{Plan: "plan-a", HeadTree: "tree-3"}, log); !proto.Equal(got, &factsv1.ApprovedBy{Tree: "tree-3"}) {
		t.Fatalf("approved.by of a tree nobody approved = %v, want the empty set (decision 0174)", got)
	}
	if got := project(t, p, projections.Keys{Plan: "plan-b", HeadTree: "tree-1"}, log); got != nil {
		t.Fatalf("approved.by for a plan that was never added = %v, want no fact", got)
	}
	if got := project(t, p, projections.Keys{Plan: "plan-a"}, log); got != nil {
		t.Fatalf("approved.by with no tree = %v, want no fact", got)
	}
}

// requested returns an invocation.requested event of invocation for plan,
// whose document names output as its output message.
func requested(t *testing.T, n int, plan, invocation, output string) *cloudeventsv1.CloudEvent {
	t.Helper()
	data, err := events.InvocationRequested(plan, &runnerv1.Invocation{
		Invocation: invocation, IdempotencyKey: "key-" + invocation, Role: harnessv1.Role_ROLE_AGENT,
		Component:     &harnessv1.Component{Kind: harnessv1.Component_KIND_AGENT_RUN, Name: "agent", Version: "1"},
		OutputMessage: output,
	})
	if err != nil {
		t.Fatal(err)
	}
	return event(t, n, data)
}

// result returns an invocation.result event of invocation with usage and
// the structured output out, written as its protojson when it is set.
func result(t *testing.T, n int, invocation string, usage *eventsv1.InvocationResult_Usage, out proto.Message) *cloudeventsv1.CloudEvent {
	t.Helper()
	data := &eventsv1.InvocationResult{Invocation: invocation, Usage: usage}
	if out != nil {
		raw, err := protojson.Marshal(out)
		if err != nil {
			t.Fatal(err)
		}
		data.Output = string(raw)
	}
	return event(t, n, data)
}

// planning is the output message of a planning invocation.
const planning = "pumi.workflow.v1.PlanningOutput"

func TestUsageIsWhatTheNamedInvocationOfThePlanUsed(t *testing.T) {
	p := byFact(t, "usage")
	log := []*cloudeventsv1.CloudEvent{
		added(t, 1, "plan-a"),
		added(t, 2, "plan-b"),
		requested(t, 3, "plan-a", "inv-1", planning),
		requested(t, 4, "plan-b", "inv-2", planning),
		requested(t, 5, "plan-a", "inv-3", planning),
		result(t, 6, "inv-1", &eventsv1.InvocationResult_Usage{Tokens: 10, CostMicrodollars: 20, Seconds: 30}, nil),
		result(t, 7, "inv-2", &eventsv1.InvocationResult_Usage{Tokens: 1, CostMicrodollars: 2, Seconds: 3}, nil),
	}
	want := &factsv1.Usage{Invocation: "inv-1", Plan: "plan-a", Value: &factsv1.Usage_Totals{Tokens: 10, CostMicrodollars: 20, Seconds: 30}}
	if got := project(t, p, projections.Keys{Plan: "plan-a", Invocation: "inv-1"}, log); !proto.Equal(got, want) {
		t.Fatalf("usage = %v, want %v", got, want)
	}
	zero := &factsv1.Usage{Plan: "plan-a", Value: &factsv1.Usage_Totals{}}
	if got := project(t, p, projections.Keys{Plan: "plan-a"}, log); !proto.Equal(got, zero) {
		t.Fatalf("usage of an added plan with no invocation named = %v, want zeros (decision 0175)", got)
	}
	for name, keys := range map[string]projections.Keys{
		"another plan's invocation":          {Plan: "plan-a", Invocation: "inv-2"},
		"an invocation that has no result":   {Plan: "plan-a", Invocation: "inv-3"},
		"an invocation that was never asked": {Plan: "plan-a", Invocation: "inv-9"},
		"a plan that was never added":        {Plan: "plan-c"},
	} {
		if got := project(t, p, keys, log); got != nil {
			t.Errorf("usage of %s = %v, want no fact", name, got)
		}
	}
}

// requestedWithBudget returns an invocation.requested event of invocation
// for plan-a whose document has budget.
func requestedWithBudget(t *testing.T, n int, invocation string, budget *harnessv1.Usage) *cloudeventsv1.CloudEvent {
	t.Helper()
	data, err := events.InvocationRequested("plan-a", &runnerv1.Invocation{
		Invocation: invocation, IdempotencyKey: invocation, Role: harnessv1.Role_ROLE_AGENT,
		Component: &harnessv1.Component{Kind: harnessv1.Component_KIND_AGENT_RUN, Name: "agent", Version: "1"},
		Budget:    budget,
	})
	if err != nil {
		t.Fatal(err)
	}
	return event(t, n, data)
}

func TestUsageOfAnInvocationInDoubtIsItsWholeBudget(t *testing.T) {
	p := byFact(t, "usage")
	budget := &harnessv1.Usage{Tokens: 500, CostMicrodollars: 600, Seconds: 700}
	log := []*cloudeventsv1.CloudEvent{
		added(t, 1, "plan-a"),
		requestedWithBudget(t, 2, "expired", budget),
		requestedWithBudget(t, 3, "released", budget),
		requestedWithBudget(t, 4, "current", budget),
		requestedWithBudget(t, 5, "unclaimed", budget),
		requestedWithBudget(t, 6, "granted-again", budget),
		event(t, 7, &eventsv1.LeaseGranted{Lease: "expired", Number: 1}),
		event(t, 8, &eventsv1.LeaseExpired{Lease: "expired", Number: 1}),
		event(t, 9, &eventsv1.LeaseGranted{Lease: "released", Number: 1}),
		event(t, 10, &eventsv1.LeaseReleased{Lease: "released", Number: 1}),
		event(t, 11, &eventsv1.LeaseGranted{Lease: "current", Number: 1}),
		event(t, 12, &eventsv1.LeaseGranted{Lease: "granted-again", Number: 1}),
		event(t, 13, &eventsv1.LeaseExpired{Lease: "granted-again", Number: 1}),
		event(t, 14, &eventsv1.LeaseGranted{Lease: "granted-again", Number: 2}),
	}
	whole := &factsv1.Usage_Totals{Tokens: 500, CostMicrodollars: 600, Seconds: 700}
	for _, inv := range []string{"expired", "released"} {
		want := &factsv1.Usage{Invocation: inv, Plan: "plan-a", Value: whole}
		if got := project(t, p, projections.Keys{Plan: "plan-a", Invocation: inv}, log); !proto.Equal(got, want) {
			t.Errorf("usage of the invocation %s, whose last lease ended with no result, = %v, want its budget %v (decision 0191)", inv, got, want)
		}
	}
	for _, inv := range []string{"current", "unclaimed", "granted-again"} {
		if got := project(t, p, projections.Keys{Plan: "plan-a", Invocation: inv}, log); got != nil {
			t.Errorf("usage of the invocation %s, which is not in doubt, = %v, want no fact (decision 0199)", inv, got)
		}
	}
}

func TestQuestionsOpenCountsTheQuestionsOfThePlanningOutput(t *testing.T) {
	p := byFact(t, "questions.open")
	two := &workflowv1.PlanningOutput{Plan: "p", Questions: []*workflowv1.PlanningOutput_Question{
		{Id: "q1", Text: "a?", AgentAnswer: "a"}, {Id: "q2", Text: "b?", AgentAnswer: "b"},
	}}
	log := []*cloudeventsv1.CloudEvent{
		added(t, 1, "plan-a"),
		added(t, 2, "plan-b"),
		requested(t, 3, "plan-a", "inv-1", planning),
		requested(t, 4, "plan-b", "inv-2", planning),
		requested(t, 5, "plan-a", "inv-3", planning),
		requested(t, 6, "plan-a", "inv-4", "pumi.workflow.v1.SecondOpinion"),
		requested(t, 7, "plan-a", "inv-5", planning),
		result(t, 8, "inv-1", nil, two),
		result(t, 9, "inv-2", nil, two),
		result(t, 10, "inv-4", nil, &workflowv1.SecondOpinion{}),
		result(t, 11, "inv-5", nil, &workflowv1.PlanningOutput{Plan: "p"}),
	}
	want := &factsv1.QuestionsOpen{Plan: "plan-a", Invocation: "inv-1", Value: 2}
	if got := project(t, p, projections.Keys{Plan: "plan-a", Invocation: "inv-1"}, log); !proto.Equal(got, want) {
		t.Fatalf("questions.open = %v, want %v", got, want)
	}
	for name, keys := range map[string]projections.Keys{
		"no invocation named":                     {Plan: "plan-a"},
		"an invocation that has no result yet":    {Plan: "plan-a", Invocation: "inv-3"},
		"an invocation with another output":       {Plan: "plan-a", Invocation: "inv-4"},
		"a planning output with no open question": {Plan: "plan-a", Invocation: "inv-5"},
	} {
		zero := &factsv1.QuestionsOpen{Plan: "plan-a", Invocation: keys.Invocation}
		if got := project(t, p, keys, log); !proto.Equal(got, zero) {
			t.Errorf("questions.open with %s = %v, want 0 (decision 0174)", name, got)
		}
	}
	for name, keys := range map[string]projections.Keys{
		"another plan's invocation":          {Plan: "plan-a", Invocation: "inv-2"},
		"an invocation that was never asked": {Plan: "plan-a", Invocation: "inv-9"},
		"a plan that was never added":        {Plan: "plan-c"},
	} {
		if got := project(t, p, keys, log); got != nil {
			t.Errorf("questions.open of %s = %v, want no fact", name, got)
		}
	}
}

func TestQuestionsOpenOfAnOutputThatDoesNotParseIsAnError(t *testing.T) {
	p := byFact(t, "questions.open")
	bad := event(t, 3, &eventsv1.InvocationResult{Invocation: "inv-1", Output: "{not json"})
	log := []*cloudeventsv1.CloudEvent{added(t, 1, "plan-a"), requested(t, 2, "plan-a", "inv-1", planning), bad}
	if got, err := p.Project(projections.Keys{Plan: "plan-a", Invocation: "inv-1"}, log); err == nil {
		t.Fatalf("questions.open of an output that does not parse = %v, want an error", got)
	}
}

func TestEveryProjectionKeepsTheContract(t *testing.T) {
	catalog, err := facts.Catalog()
	if err != nil {
		t.Fatal(err)
	}
	byName := map[string]facts.Fact{}
	for _, f := range catalog {
		byName[f.Name] = f
	}
	seen := map[string]bool{}
	for _, p := range projections.All() {
		f, ok := byName[p.Fact]
		if !ok {
			t.Errorf("the projection of %s is not of a fact of the catalog", p.Fact)
			continue
		}
		if seen[p.Fact] {
			t.Errorf("two projections are registered for %s", p.Fact)
		}
		seen[p.Fact] = true
		checkTypes(t, p)
		// An empty log: no key is known, so the fact is missing (decisions
		// 0143 and 0174).
		if got, err := p.Project(projections.Keys{Plan: "p", Gate: "g", HeadTree: "t", Project: "j", Invocation: "i"}, nil); err != nil || got != nil {
			t.Errorf("the projection of %s with no events = %v, %v; want no fact", p.Fact, got, err)
		}
		checkMessage(t, p, f)
	}
}

// checkTypes checks that p has a function and reads event types that
// exist.
func checkTypes(t *testing.T, p projections.Projection) {
	t.Helper()
	if len(p.Types) == 0 || p.Project == nil {
		t.Errorf("the projection of %s reads no event types or has no function", p.Fact)
	}
	for _, typ := range p.Types {
		if !slices.Contains(events.Types(), typ) {
			t.Errorf("the projection of %s reads %q, which is not an event type", p.Fact, typ)
		}
	}
}

// checkMessage checks that p returns the fact's message, the same each
// time it runs on the same events: a projection is deterministic.
func checkMessage(t *testing.T, p projections.Projection, f facts.Fact) {
	t.Helper()
	sample, ok := samples[p.Fact]
	if !ok {
		t.Errorf("the contract test has no sample events for %s", p.Fact)
		return
	}
	evs, keys := sample(t)
	first := project(t, p, keys, evs)
	if first == nil {
		t.Errorf("the projection of %s gives no fact for its sample events", p.Fact)
		return
	}
	if got := first.ProtoReflect().Descriptor(); got.FullName() != f.Message.FullName() {
		t.Errorf("the projection of %s returns %s, want %s", p.Fact, got.FullName(), f.Message.FullName())
	}
	for range 3 {
		if again := project(t, p, keys, evs); !proto.Equal(first, again) {
			t.Errorf("the projection of %s gave %v and then %v for the same events", p.Fact, first, again)
		}
	}
	for _, key := range f.Keys {
		if first.ProtoReflect().Get(f.Message.Fields().ByName(protoreflect.Name(key))).String() == "" {
			t.Errorf("the fact %s of the projection has no %s key", p.Fact, key)
		}
	}
}

// samples give events and keys for which each registered projection gives
// its fact.
var samples = map[string]func(t *testing.T) ([]*cloudeventsv1.CloudEvent, projections.Keys){
	"attempts": func(t *testing.T) ([]*cloudeventsv1.CloudEvent, projections.Keys) {
		return []*cloudeventsv1.CloudEvent{added(t, 1, "p"), fired(t, 2, "p", "g")}, projections.Keys{Plan: "p", Gate: "g"}
	},
	"overrides": func(t *testing.T) ([]*cloudeventsv1.CloudEvent, projections.Keys) {
		return []*cloudeventsv1.CloudEvent{added(t, 1, "p"), override(t, 2, "p", "t", "g")}, projections.Keys{Plan: "p", HeadTree: "t"}
	},
	"warnings.open": func(t *testing.T) ([]*cloudeventsv1.CloudEvent, projections.Keys) {
		return []*cloudeventsv1.CloudEvent{warning(t, "w", "j")}, projections.Keys{Project: "j", Head: 1}
	},
	"approved.by": func(t *testing.T) ([]*cloudeventsv1.CloudEvent, projections.Keys) {
		return []*cloudeventsv1.CloudEvent{
			added(t, 1, "p"), approval(t, 2, "someone", "t", eventsv1.ApprovalRecorded_ANSWER_APPROVED),
		}, projections.Keys{Plan: "p", HeadTree: "t"}
	},
	"usage": func(t *testing.T) ([]*cloudeventsv1.CloudEvent, projections.Keys) {
		return []*cloudeventsv1.CloudEvent{
			added(t, 1, "p"), requested(t, 2, "p", "i", planning), result(t, 3, "i", &eventsv1.InvocationResult_Usage{Tokens: 1}, nil),
		}, projections.Keys{Plan: "p", Invocation: "i"}
	},
	"questions.open": func(t *testing.T) ([]*cloudeventsv1.CloudEvent, projections.Keys) {
		out := &workflowv1.PlanningOutput{Questions: []*workflowv1.PlanningOutput_Question{{Id: "q", Text: "?"}}}
		return []*cloudeventsv1.CloudEvent{
			added(t, 1, "p"), requested(t, 2, "p", "i", planning), result(t, 3, "i", nil, out),
		}, projections.Keys{Plan: "p", Invocation: "i"}
	},
}
