// SPDX-License-Identifier: Apache-2.0
// SPDX-FileCopyrightText: The Pumi Authors

// Package projections holds the projection of each fact of the catalog
// that the control plane can give a decision (decision 0155). A projection
// reads events of the log as of one head and returns the fact as its
// catalog message, with its keys and its value, or nothing when the events
// do not give it. A fact with no projection, or none from its events, is
// missing from the decision's context, so the decision is a deny (decision
// 0143). The projection of each fact is added with the work that produces
// the fact.
package projections

import (
	"fmt"
	"slices"
	"strings"

	"google.golang.org/protobuf/proto"

	"getpumi.dev/pumi/api/events"
	cloudeventsv1 "getpumi.dev/pumi/api/gen/io/cloudevents/v1"
	eventsv1 "getpumi.dev/pumi/api/gen/pumi/events/v1"
	factsv1 "getpumi.dev/pumi/api/gen/pumi/facts/v1"
)

// Keys are what the facts of one decision are looked up by: the keys of
// the Decide request (decision 0155), the plan, the gate the decision is
// for and the project. A key that does not apply is empty.
type Keys struct {
	// Plan is the plan.
	Plan string
	// Gate is the gate, which is the Cedar action of the decision.
	Gate string
	// Project is the project.
	Project string
	// BaseTree is the base tree.
	BaseTree string
	// HeadTree is the head tree.
	HeadTree string
	// HeadCommit is the head commit.
	HeadCommit string
	// Invocation is the invocation, where the action is about one.
	Invocation string
}

// Projection projects one fact.
type Projection struct {
	// Fact is the fact's name in the catalog, such as attempts.
	Fact string
	// Types are the event types it reads.
	Types []string
	// Project returns the fact for keys from evs, the events of Types in
	// the log up to the head, in log order, or nil when they do not give
	// it. It reads nothing else, so the same events give the same fact.
	Project func(keys Keys, evs []*cloudeventsv1.CloudEvent) (proto.Message, error)
}

// All returns the registered projections, sorted by fact.
func All() []Projection {
	all := []Projection{attempts()}
	slices.SortFunc(all, func(a, b Projection) int { return strings.Compare(a.Fact, b.Fact) })
	return all
}

// data reads the data of e into msg.
func data(e *cloudeventsv1.CloudEvent, msg proto.Message) error {
	if err := e.GetProtoData().UnmarshalTo(msg); err != nil {
		return fmt.Errorf("the data of the event %s (%s): %w", e.GetId(), e.GetType(), err)
	}
	return nil
}

// attempts projects attempts: the number of transition.fired events into
// the gate of the plan (decision 0136). A plan that never entered the gate
// has no attempts fact.
func attempts() Projection {
	return Projection{
		Fact:  "attempts",
		Types: []string{events.TypeTransitionFired},
		Project: func(keys Keys, evs []*cloudeventsv1.CloudEvent) (proto.Message, error) {
			if keys.Plan == "" || keys.Gate == "" {
				return nil, nil
			}
			n := int64(0)
			for _, e := range evs {
				var fired eventsv1.TransitionFired
				if err := data(e, &fired); err != nil {
					return nil, err
				}
				if fired.GetPlan() == keys.Plan && fired.GetGate() == keys.Gate {
					n++
				}
			}
			if n == 0 {
				return nil, nil
			}
			return &factsv1.Attempts{Plan: keys.Plan, Gate: keys.Gate, Value: n}, nil
		},
	}
}
