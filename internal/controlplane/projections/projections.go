// SPDX-License-Identifier: Apache-2.0
// SPDX-FileCopyrightText: The Pumi Authors

// Package projections holds the projection of each fact of the catalog
// that the control plane can give a decision (decision 0155). A projection
// reads events of the log as of one head and returns the fact as its
// catalog message, with its keys and its value, or nothing when the events
// do not give it. A count or set fact is 0 or empty once its key's subject
// exists and none of its source events do; a fact whose key is unknown is
// missing (decision 0174). A fact with no projection, or none from its
// events, is missing from the decision's context, so the decision is a
// deny (decision 0143). The projection of each fact is added with the work
// that produces the fact.
package projections

import (
	"fmt"
	"slices"
	"strings"

	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"

	"getpumi.dev/pumi/api/events"
	cloudeventsv1 "getpumi.dev/pumi/api/gen/io/cloudevents/v1"
	eventsv1 "getpumi.dev/pumi/api/gen/pumi/events/v1"
	factsv1 "getpumi.dev/pumi/api/gen/pumi/facts/v1"
	workflowv1 "getpumi.dev/pumi/api/gen/pumi/workflow/v1"
)

// Keys are what the facts of one decision are looked up by: the keys of
// the Decide request (decision 0155), the plan, the gate the decision is
// for and the project. A key that does not apply is empty.
type Keys struct {
	// Plan is the plan.
	Plan string
	// Gate is the gate, which is the Cedar action of the decision, or, for a
	// retry, the gate of the state the plan is in, which it retries
	// (decision 0169).
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
	// Head is the position of the last event of the log as of which the
	// facts are projected, 0 when the log is empty. The project exists once
	// its log has any event (decision 0174).
	Head uint64
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
	all := []Projection{attempts(), overrides(), warningsOpen(), approvedBy(), usage(), questionsOpen()}
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

// planned reports whether evs hold the plan.added event of plan, which is
// when the plan exists (decision 0174).
func planned(plan string, evs []*cloudeventsv1.CloudEvent) (bool, error) {
	if plan == "" {
		return false, nil
	}
	for _, e := range evs {
		if e.GetType() != events.TypePlanAdded {
			continue
		}
		var a eventsv1.PlanAdded
		if err := data(e, &a); err != nil {
			return false, err
		}
		if a.GetPlan() == plan {
			return true, nil
		}
	}
	return false, nil
}

// each calls f with the data of each event of evs of the type typ, in
// order, read into a new message of type T.
func each[T any, P interface {
	*T
	proto.Message
}](evs []*cloudeventsv1.CloudEvent, typ string, f func(P)) error {
	for _, e := range evs {
		if e.GetType() != typ {
			continue
		}
		msg := P(new(T))
		if err := data(e, msg); err != nil {
			return err
		}
		f(msg)
	}
	return nil
}

// InDoubt is the gate key of the attempts of a retry from a state with no
// gate, which retries an invocation in doubt, and the trigger of the
// transitions that recover one (decisions 0200 and 0201).
const InDoubt = "in-doubt"

// Failed is the trigger of the transitions of a failed invocation, whose
// runs again from a state with no gate count with the in-doubt attempts
// (decision 0235).
const Failed = "failed"

// attempts projects attempts: the number of transition.fired events into
// the gate of the plan (decision 0136), 0 for a plan that exists and never
// entered the gate (decision 0174). For the gate key InDoubt, it is the
// number of the plan's transitions on in-doubt or failed back into the
// state they left: the times it ran an invocation in doubt or a failed
// invocation again from a state with no gate (decisions 0201 and 0235).
func attempts() Projection {
	return Projection{
		Fact:  "attempts",
		Types: []string{events.TypePlanAdded, events.TypeTransitionFired},
		Project: func(keys Keys, evs []*cloudeventsv1.CloudEvent) (proto.Message, error) {
			if ok, err := planned(keys.Plan, evs); err != nil || !ok || keys.Gate == "" {
				return nil, err
			}
			n := int64(0)
			err := each(evs, events.TypeTransitionFired, func(fired *eventsv1.TransitionFired) {
				switch {
				case fired.GetPlan() != keys.Plan:
				case keys.Gate == InDoubt:
					if (fired.GetTrigger() == InDoubt || fired.GetTrigger() == Failed) && fired.GetToState() == fired.GetFromState() {
						n++
					}
				case fired.GetGate() == keys.Gate:
					n++
				}
			})
			if err != nil {
				return nil, err
			}
			return &factsv1.Attempts{Plan: keys.Plan, Gate: keys.Gate, Value: n}, nil
		},
	}
}

// overrides projects overrides: the gates that override.recorded events
// name for the plan and the head tree (decision 0139), sorted, empty for a
// plan that exists with none (decision 0174).
func overrides() Projection {
	return Projection{
		Fact:  "overrides",
		Types: []string{events.TypePlanAdded, events.TypeOverrideRecorded},
		Project: func(keys Keys, evs []*cloudeventsv1.CloudEvent) (proto.Message, error) {
			if ok, err := planned(keys.Plan, evs); err != nil || !ok || keys.HeadTree == "" {
				return nil, err
			}
			gates := []string{}
			err := each(evs, events.TypeOverrideRecorded, func(o *eventsv1.OverrideRecorded) {
				if o.GetPlan() == keys.Plan && o.GetTree() == keys.HeadTree {
					gates = append(gates, o.GetGate())
				}
			})
			if err != nil {
				return nil, err
			}
			slices.Sort(gates)
			return &factsv1.Overrides{Plan: keys.Plan, Tree: keys.HeadTree, Value: slices.Compact(gates)}, nil
		},
	}
}

// warningsOpen projects warnings.open: the warning.raised events of the
// project whose id no warning.reviewed event names, 0 once the project's
// log has any event (decision 0174).
func warningsOpen() Projection {
	return Projection{
		Fact:  "warnings.open",
		Types: []string{events.TypeWarningRaised, events.TypeWarningReviewed},
		Project: func(keys Keys, evs []*cloudeventsv1.CloudEvent) (proto.Message, error) {
			if keys.Project == "" || keys.Head == 0 {
				return nil, nil
			}
			n, err := openWarnings(keys.Project, evs)
			if err != nil {
				return nil, err
			}
			return &factsv1.WarningsOpen{Project: keys.Project, Value: n}, nil
		},
	}
}

// openWarnings returns the number of warning.raised events of project in
// evs whose id no warning.reviewed event of evs names.
func openWarnings(project string, evs []*cloudeventsv1.CloudEvent) (int64, error) {
	reviewed := map[string]bool{}
	if err := each(evs, events.TypeWarningReviewed, func(r *eventsv1.WarningReviewed) {
		reviewed[r.GetWarning()] = true
	}); err != nil {
		return 0, err
	}
	n := int64(0)
	for _, e := range evs {
		if e.GetType() != events.TypeWarningRaised {
			continue
		}
		var w eventsv1.WarningRaised
		if err := data(e, &w); err != nil {
			return 0, err
		}
		if w.GetProject() == project && !reviewed[e.GetId()] {
			n++
		}
	}
	return n, nil
}

// approvedBy projects approved.by: the people whose last approval.recorded
// for the head tree approved it, sorted, so that a later rejection takes a
// person's approval back. It is empty for a tree nobody approved, once the
// plan of the decision exists (decision 0174).
func approvedBy() Projection {
	return Projection{
		Fact:  "approved.by",
		Types: []string{events.TypePlanAdded, events.TypeApprovalRecorded},
		Project: func(keys Keys, evs []*cloudeventsv1.CloudEvent) (proto.Message, error) {
			if ok, err := planned(keys.Plan, evs); err != nil || !ok || keys.HeadTree == "" {
				return nil, err
			}
			approved := map[string]bool{}
			err := each(evs, events.TypeApprovalRecorded, func(a *eventsv1.ApprovalRecorded) {
				if a.GetTree() == keys.HeadTree {
					approved[a.GetPerson()] = a.GetAnswer() == eventsv1.ApprovalRecorded_ANSWER_APPROVED
				}
			})
			if err != nil {
				return nil, err
			}
			people := []string{}
			for person, yes := range approved {
				if yes {
					people = append(people, person)
				}
			}
			slices.Sort(people)
			return &factsv1.ApprovedBy{Tree: keys.HeadTree, Value: people}, nil
		},
	}
}

// invocationTypes are the event types that the facts of an invocation of a
// plan read: the plan, the invocation's request, which names its plan and
// carries its document, and its result.
var invocationTypes = []string{events.TypePlanAdded, events.TypeInvocationRequested, events.TypeInvocationResult}

// ofPlan returns the request and the last result of the invocation in evs,
// when the plan exists and its request names the plan (decision 0175). ok
// is false when the plan does not exist, or the invocation was never
// requested or was requested for another plan. The result is nil when the
// invocation has none.
func ofPlan(plan, invocation string, evs []*cloudeventsv1.CloudEvent) (req *eventsv1.InvocationRequested, res *eventsv1.InvocationResult, ok bool, err error) {
	if ok, err := planned(plan, evs); err != nil || !ok {
		return nil, nil, false, err
	}
	err = each(evs, events.TypeInvocationRequested, func(r *eventsv1.InvocationRequested) {
		if r.GetInvocation() == invocation && req == nil {
			req = r
		}
	})
	if err != nil || req == nil || req.GetPlan() != plan {
		return nil, nil, false, err
	}
	err = each(evs, events.TypeInvocationResult, func(r *eventsv1.InvocationResult) {
		if r.GetInvocation() == invocation {
			res = r
		}
	})
	if err != nil {
		return nil, nil, false, err
	}
	return req, res, true, nil
}

// usage projects usage: what the named invocation of the plan used, from
// its invocation.result. With no invocation named, the usage of a plan
// that exists is zero (decision 0175). An invocation in doubt, whose last
// lease ended with no result, counts as having spent its whole budget, the
// budget of its document (decisions 0191 and 0199). Any other invocation
// that has no result yet, or one that is not the plan's, has no usage.
func usage() Projection {
	return Projection{
		Fact:  "usage",
		Types: slices.Concat(invocationTypes, leaseTypes),
		Project: func(keys Keys, evs []*cloudeventsv1.CloudEvent) (proto.Message, error) {
			if keys.Invocation == "" {
				if ok, err := planned(keys.Plan, evs); err != nil || !ok {
					return nil, err
				}
				return &factsv1.Usage{Plan: keys.Plan, Value: &factsv1.Usage_Totals{}}, nil
			}
			return invocationUsage(keys, evs)
		},
	}
}

// invocationUsage returns the usage of the invocation of keys: what its
// result says it used or, when it is in doubt, its whole budget.
func invocationUsage(keys Keys, evs []*cloudeventsv1.CloudEvent) (proto.Message, error) {
	req, res, ok, err := ofPlan(keys.Plan, keys.Invocation, evs)
	if err != nil || !ok {
		return nil, err
	}
	if res != nil {
		u := res.GetUsage()
		return totals(keys, u.GetTokens(), u.GetCostMicrodollars(), u.GetSeconds()), nil
	}
	ended, err := leaseEnded(keys.Invocation, evs)
	if err != nil || !ended {
		return nil, err
	}
	doc, err := events.Document(req)
	if err != nil {
		return nil, err
	}
	b := doc.GetBudget()
	return totals(keys, b.GetTokens(), b.GetCostMicrodollars(), b.GetSeconds()), nil
}

// leaseTypes are the event types of the leases of invocations.
var leaseTypes = []string{events.TypeLeaseGranted, events.TypeLeaseReleased, events.TypeLeaseExpired}

// totals returns the usage fact of the invocation of keys with the totals
// tokens, cost and seconds.
func totals(keys Keys, tokens, cost, seconds int64) *factsv1.Usage {
	return &factsv1.Usage{Invocation: keys.Invocation, Plan: keys.Plan, Value: &factsv1.Usage_Totals{
		Tokens: tokens, CostMicrodollars: cost, Seconds: seconds,
	}}
}

// leaseEnded reports whether the last lease of invocation in evs ended,
// released or expired (decision 0199). An invocation with no lease has
// not run, and one whose last lease is current is running.
func leaseEnded(invocation string, evs []*cloudeventsv1.CloudEvent) (bool, error) {
	ended, leased := false, false
	for _, e := range evs {
		lease, granted, err := leaseOf(e)
		if err != nil {
			return false, err
		}
		if lease != invocation {
			continue
		}
		leased, ended = leased || granted, !granted
	}
	return leased && ended, nil
}

// leaseOf returns the lease that e, a lease event, names, and whether e
// grants it. It returns no lease for an event of another type.
func leaseOf(e *cloudeventsv1.CloudEvent) (string, bool, error) {
	var msg interface {
		proto.Message
		GetLease() string
	}
	switch e.GetType() {
	case events.TypeLeaseGranted:
		msg = &eventsv1.LeaseGranted{}
	case events.TypeLeaseReleased:
		msg = &eventsv1.LeaseReleased{}
	case events.TypeLeaseExpired:
		msg = &eventsv1.LeaseExpired{}
	default:
		return "", false, nil
	}
	if err := data(e, msg); err != nil {
		return "", false, err
	}
	return msg.GetLease(), e.GetType() == events.TypeLeaseGranted, nil
}

// planningOutput is the output message of a planning invocation, whose
// questions questions.open counts.
var planningOutput = (&workflowv1.PlanningOutput{}).ProtoReflect().Descriptor().FullName()

// questionsOpen projects questions.open: the number of questions in the
// planning output of the named invocation of the plan (decision 0171). It
// is 0 for a plan that exists when no invocation is named, when the
// invocation has no result yet, and when its output is not a planning
// output (decision 0174). An invocation that is not the plan's has none,
// and an output that does not parse is an error, so the fact is missing.
func questionsOpen() Projection {
	return Projection{
		Fact:  "questions.open",
		Types: invocationTypes,
		Project: func(keys Keys, evs []*cloudeventsv1.CloudEvent) (proto.Message, error) {
			if keys.Invocation == "" {
				if ok, err := planned(keys.Plan, evs); err != nil || !ok {
					return nil, err
				}
				return &factsv1.QuestionsOpen{Plan: keys.Plan}, nil
			}
			req, res, ok, err := ofPlan(keys.Plan, keys.Invocation, evs)
			if err != nil || !ok {
				return nil, err
			}
			n, err := questionsIn(req, res)
			if err != nil {
				return nil, fmt.Errorf("the invocation %s: %w", keys.Invocation, err)
			}
			return &factsv1.QuestionsOpen{Plan: keys.Plan, Invocation: keys.Invocation, Value: n}, nil
		},
	}
}

// questionsIn returns the number of questions in res, the result of the
// invocation requested by req, when its document names the planning
// output as its output message, and 0 when it does not or res is nil.
func questionsIn(req *eventsv1.InvocationRequested, res *eventsv1.InvocationResult) (int64, error) {
	doc, err := events.Document(req)
	if err != nil {
		return 0, err
	}
	if res == nil || protoreflect.FullName(doc.GetOutputMessage()) != planningOutput {
		return 0, nil
	}
	var out workflowv1.PlanningOutput
	if err := protojson.Unmarshal([]byte(res.GetOutput()), &out); err != nil {
		return 0, fmt.Errorf("the planning output does not parse: %w", err)
	}
	return int64(len(out.GetQuestions())), nil
}
