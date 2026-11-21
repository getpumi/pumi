// SPDX-License-Identifier: Apache-2.0
// SPDX-FileCopyrightText: The Pumi Authors

package projections_test

import (
	"fmt"
	"slices"
	"testing"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"

	"getpumi.dev/pumi/api/events"
	"getpumi.dev/pumi/api/facts"
	cloudeventsv1 "getpumi.dev/pumi/api/gen/io/cloudevents/v1"
	eventsv1 "getpumi.dev/pumi/api/gen/pumi/events/v1"
	factsv1 "getpumi.dev/pumi/api/gen/pumi/facts/v1"
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
	if got := project(t, p, projections.Keys{Plan: "plan-c", Gate: "dco"}, log); got != nil {
		t.Fatalf("attempts of a plan that never entered the gate = %v, want no fact", got)
	}
	if got := project(t, p, projections.Keys{Plan: "plan-a"}, log); got != nil {
		t.Fatalf("attempts with no gate = %v, want no fact", got)
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
		if len(p.Types) == 0 || p.Project == nil {
			t.Errorf("the projection of %s reads no event types or has no function", p.Fact)
		}
		for _, typ := range p.Types {
			if !slices.Contains(events.Types(), typ) {
				t.Errorf("the projection of %s reads %q, which is not an event type", p.Fact, typ)
			}
		}
		// No source events: the fact is absent (decisions 0143 and 0155).
		if got, err := p.Project(projections.Keys{Plan: "p", Gate: "g", HeadTree: "t"}, nil); err != nil || got != nil {
			t.Errorf("the projection of %s with no events = %v, %v; want no fact", p.Fact, got, err)
		}
		checkMessage(t, p, f)
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
		return []*cloudeventsv1.CloudEvent{fired(t, 1, "p", "g")}, projections.Keys{Plan: "p", Gate: "g"}
	},
}
