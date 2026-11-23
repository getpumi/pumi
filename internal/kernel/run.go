// SPDX-License-Identifier: Apache-2.0
// SPDX-FileCopyrightText: The Pumi Authors

package kernel

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"strings"

	"google.golang.org/protobuf/proto"

	"getpumi.dev/pumi/api/events"
	cloudeventsv1 "getpumi.dev/pumi/api/gen/io/cloudevents/v1"
	controlplanev1 "getpumi.dev/pumi/api/gen/pumi/controlplane/v1"
	"getpumi.dev/pumi/api/gen/pumi/controlplane/v1/controlplanev1connect"
	eventsv1 "getpumi.dev/pumi/api/gen/pumi/events/v1"
	harnessv1 "getpumi.dev/pumi/api/gen/pumi/harness/v1"
	runnerv1 "getpumi.dev/pumi/api/gen/pumi/runner/v1"
	workflowv1 "getpumi.dev/pumi/api/gen/pumi/workflow/v1"
	"getpumi.dev/pumi/internal/kernel/workflow"
)

// Source is the source attribute of the events that the kernel appends
// (decision 0133).
const Source = "/kernel"

// InDoubt is the trigger that a transition.fired names when the kernel
// escalates a plan whose invocation was requested and has no result when
// the kernel starts (decisions 0019 and 0182).
const InDoubt = "in-doubt"

// escalate is the Cedar action of the transitions that escalate a plan to a
// person; firing one appends escalation.raised (decision 0169).
const escalate = "escalate"

// Config is what the wiring gives the kernel to run one plan (decision
// 0180).
type Config struct {
	// Client is the kernel's client of the control plane, with the
	// kernel's identity.
	Client controlplanev1connect.ControlPlaneServiceClient
	// Workflow is the workflow the plan runs on.
	Workflow *workflow.Workflow
	// Plan is the plan, and Track the track it joins.
	Plan  string
	Track string
	// Base is the part of each invocation document that the wiring sets,
	// such as the component's name and version, the repository and its
	// tree, the image, the capabilities and the budget. The kernel sets the
	// invocation, the idempotency key, the role, the kind of component, the
	// task and the output message from the state that invokes.
	Base *runnerv1.Invocation
	// Idle, when set, is called each time the kernel has handled every
	// event up to head, the head of the log, and waits for more. The golden
	// cases use it to know when the kernel waits (decision 0183).
	Idle func(head uint64)
	// Wrote, when set, is called after each write of the kernel to the log,
	// an Append or a Decide, with the position of its last event. The
	// golden cases restart at each of them (decision 0182).
	Wrote func(position uint64)
}

// fired is what the kernel keeps of a transition.fired of the plan.
type fired struct {
	id string
	to string
}

// decision is what the kernel keeps of a decision.made.
type decision struct {
	id    string
	allow bool
}

// planRun is the state of the run of one plan: the plan's state and what
// the log says about it, which the kernel projects from the log (decision
// 0018).
type planRun struct {
	cfg    Config
	states map[string]*workflowv1.State
	m      *workflow.Machine

	// added is whether the log has the plan's plan.added, and state the
	// plan's state.
	added bool
	state string
	// invocation is the plan's last invocation and requested the id of its
	// invocation.requested; requests counts the plan's invocations.
	invocation string
	requested  string
	requests   int
	// finished holds the plan's invocations that finished.
	finished map[string]bool
	// firedBy holds the plan's transitions by the id of their cause.
	firedBy map[string]fired
	// decisions holds the decisions by the id of their cause, in log
	// order, and positionOf the position of each event the kernel has read.
	decisions  map[string][]decision
	positionOf map[string]uint64
	// last is the position of the last event the kernel has handled.
	last uint64
}

// Run runs the plan on the workflow until the plan is in a final state or
// ctx is done (decisions 0169, 0170 and 0182). It reads the log with
// Subscribe. On start it reads the log up to its head and rebuilds the
// plan's state from it, handling any trigger that has no transition yet;
// it escalates an invocation of the plan that was requested and has no
// result, and adds the plan when the log does not have it. Then it handles
// each new event in log order. For a trigger of the plan's state it asks
// the control plane to decide each of the state's transitions for it in
// file order and fires the first one allowed: it appends transition.fired,
// with the trigger as its cause, and, on entry to a state that invokes,
// invocation.requested naming the plan, and, for a transition guarded by
// escalate, escalation.raised. Each event fires at most once: an event is
// the cause of at most one transition.fired, and a decision already made
// for a trigger is not made again. It returns nil when the plan is final
// and ctx's error when ctx is done.
func Run(ctx context.Context, cfg Config) error {
	r, err := newPlanRun(cfg)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	head, err := r.head(ctx)
	if err != nil {
		return err
	}
	stream, err := cfg.Client.Subscribe(ctx, &controlplanev1.SubscribeRequest{})
	if err != nil {
		return fmt.Errorf("cannot subscribe to the log: %w", err)
	}
	defer func() { _ = stream.Close() }()
	known, err := r.receiveUpTo(ctx, stream, head)
	if err != nil {
		return err
	}
	if err := r.start(ctx, known); err != nil {
		return err
	}
	for {
		if r.final() {
			return nil
		}
		if err := r.reportIdle(ctx); err != nil {
			return err
		}
		res, err := stream.Receive()
		if err != nil {
			return r.streamError(ctx, err)
		}
		r.index(res.GetEntry())
		if err := r.handle(ctx, res.GetEntry()); err != nil {
			return err
		}
	}
}

// receiveUpTo receives the events of stream up to the position head.
func (r *planRun) receiveUpTo(ctx context.Context, stream controlplanev1connect.ControlPlaneServiceSubscribeClientStream,
	head uint64,
) ([]*controlplanev1.Entry, error) {
	var known []*controlplanev1.Entry
	for head > 0 && (len(known) == 0 || known[len(known)-1].GetPosition() < head) {
		res, err := stream.Receive()
		if err != nil {
			return nil, r.streamError(ctx, err)
		}
		known = append(known, res.GetEntry())
	}
	return known, nil
}

// newPlanRun returns the run of the plan of cfg, which has not read the
// log yet.
func newPlanRun(cfg Config) (*planRun, error) {
	switch {
	case cfg.Client == nil:
		return nil, errors.New("the kernel has no client of the control plane")
	case cfg.Workflow == nil:
		return nil, errors.New("the kernel has no workflow")
	case cfg.Plan == "":
		return nil, errors.New("the kernel has no plan to run")
	}
	r := &planRun{
		cfg: cfg, states: map[string]*workflowv1.State{}, finished: map[string]bool{},
		firedBy: map[string]fired{}, decisions: map[string][]decision{}, positionOf: map[string]uint64{},
	}
	for _, s := range cfg.Workflow.Definition().GetStates() {
		r.states[s.GetName()] = s
	}
	r.m = cfg.Workflow.NewMachine(
		func(context.Context) (string, error) { return r.state, nil },
		func(_ context.Context, s string) error { r.state = s; return nil },
	)
	return r, nil
}

// streamError returns the error that ends the run when the stream of
// Subscribe ends with err: ctx's error when ctx is done.
func (r *planRun) streamError(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return fmt.Errorf("the subscription to the log ended: %w", err)
}

// head returns the head of the log.
func (r *planRun) head(ctx context.Context) (uint64, error) {
	res, err := r.cfg.Client.Query(ctx, &controlplanev1.QueryRequest{Limit: 1})
	if err != nil {
		return 0, fmt.Errorf("cannot read the head of the log: %w", err)
	}
	return res.GetHead(), nil
}

// reportIdle calls Idle when the kernel has handled every event of the log.
func (r *planRun) reportIdle(ctx context.Context) error {
	if r.cfg.Idle == nil {
		return nil
	}
	head, err := r.head(ctx)
	if err != nil {
		return err
	}
	if head == r.last {
		r.cfg.Idle(head)
	}
	return nil
}

// wrote calls Wrote with position.
func (r *planRun) wrote(position uint64) {
	if r.cfg.Wrote != nil {
		r.cfg.Wrote(position)
	}
}

// final reports whether the plan is in a final state.
func (r *planRun) final() bool {
	_, ok := r.states[r.state].GetEntry().(*workflowv1.State_Final)
	return r.added && ok
}

// start handles the events of the log up to its head, known, which it
// indexes first, so that it knows which triggers already fired a
// transition. It then escalates an invocation in doubt and adds the plan
// when the log does not have it.
func (r *planRun) start(ctx context.Context, known []*controlplanev1.Entry) error {
	for _, e := range known {
		r.index(e)
	}
	for _, e := range known {
		if err := r.handle(ctx, e); err != nil {
			return err
		}
	}
	if err := r.escalateInDoubt(ctx); err != nil {
		return err
	}
	if !r.added {
		return r.add(ctx)
	}
	return nil
}

// index records what the kernel needs to know of e ahead of handling it:
// its position, and, for a transition.fired of the plan and a decision,
// the event that caused it.
func (r *planRun) index(entry *controlplanev1.Entry) {
	e := entry.GetEvent()
	if _, seen := r.positionOf[e.GetId()]; seen {
		return
	}
	r.positionOf[e.GetId()] = entry.GetPosition()
	cause := e.GetAttributes()[events.ExtensionCause].GetCeString()
	switch e.GetType() {
	case events.TypeTransitionFired:
		var tf eventsv1.TransitionFired
		if e.GetSource() == Source && unpack(e, &tf) == nil && tf.GetPlan() == r.cfg.Plan && cause != "" {
			r.firedBy[cause] = fired{id: e.GetId(), to: tf.GetToState()}
		}
	case events.TypeDecisionMade:
		var d eventsv1.DecisionMade
		if cause != "" && unpack(e, &d) == nil {
			r.decisions[cause] = append(r.decisions[cause], decision{
				id: e.GetId(), allow: d.GetOutcome() == eventsv1.DecisionMade_OUTCOME_ALLOW,
			})
		}
	}
}

// subjectOf returns the subject attribute of e.
func subjectOf(e *cloudeventsv1.CloudEvent) string {
	return e.GetAttributes()["subject"].GetCeString()
}

// unpack reads the data of e into data.
func unpack(e *cloudeventsv1.CloudEvent, data proto.Message) error {
	return e.GetProtoData().UnmarshalTo(data)
}

// handle handles the event of entry in log order: it follows the plan's
// state and invocations, and fires the transition of a trigger of the
// plan's state. A trigger that already fired a transition moves the plan
// to its target at once, as when the kernel fired it.
func (r *planRun) handle(ctx context.Context, entry *controlplanev1.Entry) error {
	if entry.GetPosition() <= r.last {
		return nil
	}
	r.last = entry.GetPosition()
	e := entry.GetEvent()
	if err := r.follow(e); err != nil {
		return err
	}
	if f, ok := r.firedBy[e.GetId()]; ok {
		r.state = f.to
		return nil
	}
	trigger, ok := r.triggerOf(e)
	if !ok {
		return nil
	}
	ts, err := r.m.Transitions(ctx, trigger)
	if err != nil || len(ts) == 0 {
		return err
	}
	return r.fire(ctx, e, string(trigger), ts, nil)
}

// follow follows the plan's additions, invocations and their ends.
func (r *planRun) follow(e *cloudeventsv1.CloudEvent) error {
	switch e.GetType() {
	case events.TypePlanAdded:
		var a eventsv1.PlanAdded
		if err := unpack(e, &a); err != nil {
			return fmt.Errorf("the data of the event %s: %w", e.GetId(), err)
		}
		if a.GetPlan() == r.cfg.Plan && !r.added {
			r.added = true
			r.state = r.cfg.Workflow.Definition().GetInitial()
		}
	case events.TypeInvocationRequested:
		var req eventsv1.InvocationRequested
		if err := unpack(e, &req); err != nil {
			return fmt.Errorf("the data of the event %s: %w", e.GetId(), err)
		}
		if req.GetPlan() == r.cfg.Plan {
			r.invocation, r.requested = req.GetInvocation(), e.GetId()
			r.requests++
		}
	case events.TypeInvocationFinished:
		var f eventsv1.InvocationFinished
		if err := unpack(e, &f); err != nil {
			return fmt.Errorf("the data of the event %s: %w", e.GetId(), err)
		}
		r.finished[f.GetInvocation()] = true
	}
	return nil
}

// triggerOf returns the trigger of the plan's state that e is, if any
// (decision 0170): the result of the state's invocation, the entry into the
// state, as the plan.added or transition.fired that entered it, or an
// event about the plan from someone other than the kernel.
func (r *planRun) triggerOf(e *cloudeventsv1.CloudEvent) (workflow.Trigger, bool) {
	if !r.added {
		return "", false
	}
	switch e.GetType() {
	case events.TypeInvocationFinished:
		var f eventsv1.InvocationFinished
		if unpack(e, &f) == nil && f.GetInvocation() == r.invocation && r.invocation != "" {
			return workflow.Result, true
		}
		return "", false
	case events.TypePlanAdded:
		return workflow.Entered, subjectOf(e) == r.cfg.Plan
	case events.TypeTransitionFired:
		var tf eventsv1.TransitionFired
		entered := e.GetSource() == Source && unpack(e, &tf) == nil && tf.GetPlan() == r.cfg.Plan && tf.GetToState() == r.state
		return workflow.Entered, entered
	}
	if e.GetSource() == Source || subjectOf(e) != r.cfg.Plan {
		return "", false
	}
	return workflow.Event(e.GetType()), true
}

// fire decides the transitions ts for the trigger e in order and fires the
// first one allowed. A decision that the log already has for e is used
// instead of a new one, so that a kernel that stopped between a decision
// and its transition makes no decision twice (decision 0187). When every
// transition is denied, nothing fires. denied lists the actions that were
// denied before ts, for the question of an escalation.
func (r *planRun) fire(ctx context.Context, e *cloudeventsv1.CloudEvent, trigger string, ts []workflow.Transition, denied []string) error {
	made := r.madeFor(e.GetId())
	for i, t := range ts {
		var d decision
		if i < len(made) {
			d = made[i]
		} else {
			var err error
			if d, err = r.decide(ctx, t.Action, e.GetId()); err != nil {
				return err
			}
		}
		if d.allow {
			return r.transition(ctx, e, trigger, t, d, denied)
		}
		denied = append(denied, t.Action)
	}
	return nil
}

// madeFor returns the decisions that the log has for the trigger id, made
// after it. A permission's decision names the event it allows and comes
// before it, so it is not one of them (decision 0156).
func (r *planRun) madeFor(id string) []decision {
	var out []decision
	at := r.positionOf[id]
	for _, d := range r.decisions[id] {
		if r.positionOf[d.id] > at {
			out = append(out, d)
		}
	}
	return out
}

// decide asks the control plane to decide action for the plan, with the
// plan's last invocation as the key of its facts and cause as the
// decision's cause, and returns the decision.
func (r *planRun) decide(ctx context.Context, action, cause string) (decision, error) {
	res, err := r.cfg.Client.Decide(ctx, &controlplanev1.DecideRequest{
		Action: action, Plan: r.cfg.Plan, Cause: cause,
		Keys: &controlplanev1.FactKeys{Invocation: r.invocation},
	})
	if err != nil {
		return decision{}, fmt.Errorf("cannot decide %s for the plan %s: %w", action, r.cfg.Plan, err)
	}
	r.wrote(res.GetPosition())
	q, err := r.cfg.Client.Query(ctx, &controlplanev1.QueryRequest{After: res.GetPosition() - 1, Limit: 1})
	if err != nil {
		return decision{}, fmt.Errorf("cannot read the decision of %s at %d: %w", action, res.GetPosition(), err)
	}
	if len(q.GetEvents()) != 1 {
		return decision{}, fmt.Errorf("the log has no decision of %s at %d", action, res.GetPosition())
	}
	r.index(q.GetEvents()[0])
	return decision{
		id: q.GetEvents()[0].GetEvent().GetId(), allow: res.GetOutcome() == eventsv1.DecisionMade_OUTCOME_ALLOW,
	}, nil
}

// transition fires t for the trigger e, allowed by d: it appends
// transition.fired with e as its cause, invocation.requested when the
// target invokes, and escalation.raised when t escalates, and moves the
// plan to the target.
func (r *planRun) transition(ctx context.Context, e *cloudeventsv1.CloudEvent, trigger string, t workflow.Transition,
	d decision, denied []string,
) error {
	target := r.states[t.Target]
	tf, err := r.event(&eventsv1.TransitionFired{
		Plan: r.cfg.Plan, FromState: r.state, ToState: t.Target, Trigger: trigger, Decision: d.id, Gate: target.GetGate(),
	}, e.GetId())
	if err != nil {
		return err
	}
	evs := []*cloudeventsv1.CloudEvent{tf}
	if inv, ok := target.GetEntry().(*workflowv1.State_Invoke); ok {
		req, err := r.request(t.Target, inv.Invoke, tf.GetId())
		if err != nil {
			return err
		}
		evs = append(evs, req)
	}
	if t.Action == escalate {
		raised, err := r.event(&eventsv1.EscalationRaised{Plan: r.cfg.Plan, Question: r.question(trigger, denied)}, tf.GetId())
		if err != nil {
			return err
		}
		evs = append(evs, raised)
	}
	if err := r.append(ctx, evs); err != nil {
		return err
	}
	r.firedBy[e.GetId()] = fired{id: tf.GetId(), to: t.Target}
	return r.m.Fire(ctx, t.Index)
}

// question returns the question of the escalation of the plan from its
// state on trigger, after the actions denied were denied.
func (r *planRun) question(trigger string, denied []string) string {
	if trigger == InDoubt {
		return fmt.Sprintf("The invocation %s of the state %s was requested and has no result after pumi started again, so it may have run. It is not run again (decision 0182).",
			r.invocation, r.state)
	}
	if len(denied) == 0 {
		return fmt.Sprintf("The plan escalated from the state %s on %s.", r.state, trigger)
	}
	return fmt.Sprintf("The plan escalated from the state %s on %s: %s denied.", r.state, trigger, strings.Join(denied, ", then "))
}

// request returns the invocation.requested of the plan's next invocation,
// which the state starts with inv, caused by cause (decisions 0165 and
// 0175). The invocation is <plan>/<n> for the plan's nth invocation, and its
// idempotency key is the invocation, both derived from the log (decision
// 0019).
func (r *planRun) request(state string, inv *workflowv1.Invoke, cause string) (*cloudeventsv1.CloudEvent, error) {
	task, ok := r.cfg.Workflow.Task(inv.GetTask())
	if !ok {
		return nil, fmt.Errorf("the state %s names the task %q, which the workflow does not have", state, inv.GetTask())
	}
	doc := proto.CloneOf(r.cfg.Base)
	if doc == nil {
		doc = &runnerv1.Invocation{}
	}
	id := fmt.Sprintf("%s/%d", r.cfg.Plan, r.requests+1)
	doc.Invocation, doc.IdempotencyKey = id, id
	doc.Role = roleOf[inv.GetRole()]
	if doc.Component == nil {
		doc.Component = &harnessv1.Component{}
	}
	doc.Component.Kind = kindOf[inv.GetKind()]
	if doc.Input == nil {
		doc.Input = &harnessv1.Input{}
	}
	doc.Input.Task = task
	doc.OutputMessage = inv.GetOutputMessage()
	data, err := events.InvocationRequested(r.cfg.Plan, doc)
	if err != nil {
		return nil, fmt.Errorf("the invocation of the state %s: %w", state, err)
	}
	return r.event(data, cause)
}

// roleOf gives the role of the invocation document for each role of the
// workflow.
var roleOf = map[workflowv1.Invoke_Role]harnessv1.Role{
	workflowv1.Invoke_ROLE_AGENT: harnessv1.Role_ROLE_AGENT,
	workflowv1.Invoke_ROLE_GATE:  harnessv1.Role_ROLE_GATE,
}

// kindOf gives the kind of component of the invocation document for each
// kind of the workflow.
var kindOf = map[workflowv1.Invoke_Kind]harnessv1.Component_Kind{
	workflowv1.Invoke_KIND_HARNESS:         harnessv1.Component_KIND_AGENT_RUN,
	workflowv1.Invoke_KIND_CHECK:           harnessv1.Component_KIND_CHECK,
	workflowv1.Invoke_KIND_EXTERNAL_ACTION: harnessv1.Component_KIND_EXTERNAL_ACTION,
}

// event returns the envelope of an event of the kernel about the plan,
// with a new id and the cause cause.
func (r *planRun) event(data proto.Message, cause string) (*cloudeventsv1.CloudEvent, error) {
	e, err := events.New(data, events.Attributes{
		ID: "kernel-" + rand.Text(), Source: Source, Subject: r.cfg.Plan, Tenant: "kernel", Cause: cause,
	})
	if err != nil {
		return nil, fmt.Errorf("cannot build the kernel's event: %w", err)
	}
	return e, nil
}

// append appends evs.
func (r *planRun) append(ctx context.Context, evs []*cloudeventsv1.CloudEvent) error {
	res, err := r.cfg.Client.Append(ctx, &controlplanev1.AppendRequest{Events: evs})
	if err != nil {
		return fmt.Errorf("cannot append the kernel's events for the plan %s: %w", r.cfg.Plan, err)
	}
	if n := len(res.GetPositions()); n > 0 {
		r.wrote(res.GetPositions()[n-1])
	}
	return nil
}

// add appends the plan's plan.added and, when the initial state invokes,
// the invocation.requested of its invocation, caused by the plan.added.
// The plan is in the initial state once the kernel reads the plan.added.
func (r *planRun) add(ctx context.Context) error {
	added, err := r.event(&eventsv1.PlanAdded{Plan: r.cfg.Plan, Track: r.cfg.Track}, "")
	if err != nil {
		return err
	}
	evs := []*cloudeventsv1.CloudEvent{added}
	initial := r.cfg.Workflow.Definition().GetInitial()
	if inv, ok := r.states[initial].GetEntry().(*workflowv1.State_Invoke); ok {
		req, err := r.request(initial, inv.Invoke, added.GetId())
		if err != nil {
			return err
		}
		evs = append(evs, req)
	}
	return r.append(ctx, evs)
}

// escalateInDoubt escalates the plan when the state invokes and its
// invocation was requested and has no result: the invocation may have
// run, and an agent run is not idempotent, so it is not run again
// (decisions 0019 and 0182). The transition is the state's first on result
// that escalate guards, with the invocation.requested as its cause, and
// InDoubt as its trigger.
func (r *planRun) escalateInDoubt(ctx context.Context) error {
	if !r.added || r.invocation == "" || r.finished[r.invocation] {
		return nil
	}
	if _, ok := r.states[r.state].GetEntry().(*workflowv1.State_Invoke); !ok {
		return nil
	}
	if _, ok := r.firedBy[r.requested]; ok {
		return nil
	}
	ts, err := r.m.Transitions(ctx, workflow.Result)
	if err != nil {
		return err
	}
	for _, t := range ts {
		if t.Action == escalate {
			req := &cloudeventsv1.CloudEvent{Id: r.requested}
			return r.fire(ctx, req, InDoubt, []workflow.Transition{t}, nil)
		}
	}
	return fmt.Errorf("the invocation %s of the state %s is in doubt, and the state has no transition on result that %s guards",
		r.invocation, r.state, escalate)
}
