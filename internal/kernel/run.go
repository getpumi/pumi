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
	"getpumi.dev/pumi/internal/shared"
)

// Source is the source attribute of the events that the kernel appends
// (decision 0133).
const Source = "/kernel"

// InDoubt is the trigger that a transition.fired names when the kernel
// recovers an invocation in doubt, whose last lease ended with no result
// (decisions 0191, 0199 and 0200).
const InDoubt = string(workflow.InDoubt)

// escalate is the Cedar action of the transitions that escalate a plan to a
// person; firing one appends escalation.raised (decision 0169).
const escalate = "escalate"

// Config is what the wiring gives the kernel to run the plans of the log
// (decisions 0180 and 0198).
type Config struct {
	// Client is the kernel's client of the control plane, with the
	// kernel's identity.
	Client controlplanev1connect.ControlPlaneServiceClient
	// Workflow is the workflow the plans run on.
	Workflow *workflow.Workflow
	// Base is the part of each invocation document that the wiring sets,
	// such as the component's name and version, the repository and its
	// tree, the image, the capabilities and the budget. The kernel sets the
	// invocation, the idempotency key, the role, the kind of component, the
	// task and the output message from the state that invokes.
	Base *runnerv1.Invocation
	// Parallelism is the parallelism setting of the control plane: how many
	// plans run at once across all tracks (decisions 0172 and 0189). 0
	// counts as 1.
	Parallelism int
	// Resolve asks the adapter of an external action in doubt whether the
	// action happened (decision 0202). The forge adapter of epic E11 gives
	// the first answer; the golden cases give a fake one.
	Resolve func(ctx context.Context, invocation *runnerv1.Invocation) (happened bool, err error)
	// Idle, when set, is called each time the kernel has handled every
	// event up to head, the head of the log, and waits for more. The golden
	// cases use it to know when the kernel waits (decision 0183).
	Idle func(head uint64)
	// Trunk, when set, returns the tree of the trunk branch. The kernel
	// calls it when it starts a plan, and that tree is the input tree of
	// the plan's invocations, in place of Base's (decision 0209). After a
	// restart a plan keeps the tree of its first invocation.requested.
	Trunk func(ctx context.Context) (string, error)
	// Project is the project section of the configuration, which the
	// kernel adds to the task of every agent run (decision 0250).
	Project Project
	// HasFile reports whether the tree holds a file at path, relative to
	// the root of the repository. The kernel asks it about each convention
	// file of Project in the input tree of an agent run, so that it can
	// name a missing one as missing. It is required when Project lists
	// convention files.
	HasFile func(ctx context.Context, tree, path string) (bool, error)
	// Wrote, when set, is called after each write of the kernel to the log,
	// an Append or a Decide, with the position of its last event. The
	// golden cases restart at each of them (decision 0182).
	Wrote func(position uint64)
}

// fired is what the kernel keeps of a transition.fired of a plan.
type fired struct {
	id   string
	plan string
	to   string
}

// decision is what the kernel keeps of a decision.made.
type decision struct {
	id    string
	allow bool
}

// kernel is the state of the run of every plan of the log, which the
// kernel projects from the log (decision 0018).
type kernel struct {
	cfg    Config
	states map[string]*workflowv1.State

	// plans holds the plans that the log adds, by name, and order the same
	// plans in the order they were added.
	plans map[string]*planRun
	order []*planRun
	// invocations holds the plan of each invocation that the log requests,
	// and finished the invocations that finished.
	invocations map[string]*planRun
	finished    map[string]bool
	// refusals holds the refusals that the harness reported in the
	// invocation.result of each invocation that has any (decision 0249).
	refusals map[string][]*harnessv1.Refusal
	// documents holds the document of each invocation that the log
	// requests, and resolved the external actions in doubt whose
	// invocation.finished the kernel appended.
	documents map[string]*runnerv1.Invocation
	resolved  map[string]bool
	// ended holds the invocations whose last lease ended, released or
	// expired, by the event that ended it.
	ended map[string]bool
	// firedBy holds the transitions of the plans by the id of their cause,
	// and requestedBy the ids of the causes of the kernel's
	// invocation.requested events.
	firedBy     map[string]fired
	requestedBy map[string]bool
	// decisions holds the decisions by the id of their cause, in log
	// order, and positionOf the position of each event the kernel has read.
	decisions  map[string][]decision
	positionOf map[string]uint64
	// last is the position of the last event the kernel has handled.
	last uint64
}

// planRun is the state of the run of one plan: the plan's state and what
// the log says about it.
type planRun struct {
	k *kernel
	m *workflow.Machine

	// plan, track and dependsOn are what its plan.added says, and added
	// is the id of that event.
	plan      string
	track     string
	dependsOn []string
	added     string
	// state is the plan's state. started is whether the kernel has started
	// the plan: handled the entry into its initial state, which it does
	// only when the plans it depends on have merged and a slot is free
	// (decision 0189).
	state   string
	started bool
	// invocation is the plan's last invocation and requested the id of its
	// invocation.requested; requests counts the plan's invocations.
	invocation string
	requested  string
	requests   int
	// tree is the input tree of the plan's invocations: the trunk's tree
	// when the plan started (decision 0209), or empty when the wiring
	// gives no trunk.
	tree string
	// declared are the declared paths of the plan's last planning output,
	// and planned is whether it has one (decision 0250).
	declared []string
	planned  bool
	// noTest are the requirements that the plan's last planning output
	// gives no test, with the reasons (decision 0295).
	noTest []*workflowv1.PlanningOutput_NoTest
	// narratives is the output of the plan's last spec check, whose
	// narratives the kernel adds to the tasks that ask for them, and
	// coverage the last coverage report, or nil when there is none yet
	// (decisions 0279 and 0294).
	narratives *workflowv1.SpecCheckOutput
	coverage   *workflowv1.CoverageReport
}

// Run runs every plan that a plan.added of the log adds on the workflow
// until ctx is done, and returns ctx's error (decisions 0169, 0170, 0182,
// 0189 and 0198). It reads the log with Subscribe. On start it reads the
// log up to its head and rebuilds each plan's state from it, handling any
// trigger that has no transition yet. Then it handles each new event in
// log order. It recovers an invocation in doubt, one whose lease ended
// with no result, when it starts and when the lease ends (decisions 0191
// and 0200 to 0202).
//
// A plan starts when every plan it depends on is in a final state that has
// merged (decision 0197) and fewer plans than the parallelism setting run,
// where a plan runs from its start until it is final; when more plans are
// ready, they start in the order they were added. Starting a plan handles
// the entry into its initial state: invocation.requested when the state
// invokes, caused by the plan.added, and the state's transitions on entry.
// The slots are counted from the log, with no lease events (decision
// 0190). The kernel handles no trigger of a plan that has not started,
// except a cancel.requested, which cancels a plan that waits to start
// (decision 0192).
//
// For a trigger of a plan's state the kernel asks the control plane to
// decide each of the state's transitions for it in file order and fires
// the first one allowed: it appends transition.fired, with the trigger as
// its cause, and, on entry to a state that invokes, invocation.requested
// naming the plan, and, for a transition guarded by escalate,
// escalation.raised. Each event fires at most once: an event is the cause
// of at most one transition.fired, a decision already made for a trigger
// is not made again, and a plan.added is the cause of at most one
// invocation.requested.
func Run(ctx context.Context, cfg Config) error {
	k, err := newKernel(cfg)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	head, err := k.head(ctx)
	if err != nil {
		return err
	}
	stream, err := cfg.Client.Subscribe(ctx, &controlplanev1.SubscribeRequest{})
	if err != nil {
		return fmt.Errorf("cannot subscribe to the log: %w", err)
	}
	defer func() { _ = stream.Close() }()
	known, err := k.receiveUpTo(ctx, stream, head)
	if err != nil {
		return err
	}
	if err := k.start(ctx, known); err != nil {
		return err
	}
	for {
		if err := k.reportIdle(ctx); err != nil {
			return err
		}
		res, err := stream.Receive()
		if err != nil {
			return k.streamError(ctx, err)
		}
		k.index(res.GetEntry())
		if err := k.handle(ctx, res.GetEntry()); err != nil {
			return err
		}
	}
}

// receiveUpTo receives the events of stream up to the position head.
func (k *kernel) receiveUpTo(ctx context.Context, stream controlplanev1connect.ControlPlaneServiceSubscribeClientStream,
	head uint64,
) ([]*controlplanev1.Entry, error) {
	var known []*controlplanev1.Entry
	for head > 0 && (len(known) == 0 || known[len(known)-1].GetPosition() < head) {
		res, err := stream.Receive()
		if err != nil {
			return nil, k.streamError(ctx, err)
		}
		known = append(known, res.GetEntry())
	}
	return known, nil
}

// newKernel returns the kernel of cfg, which has not read the log yet.
func newKernel(cfg Config) (*kernel, error) {
	switch {
	case cfg.Client == nil:
		return nil, errors.New("the kernel has no client of the control plane")
	case cfg.Workflow == nil:
		return nil, errors.New("the kernel has no workflow")
	case cfg.Parallelism < 0:
		return nil, fmt.Errorf("the parallelism setting is %d, want 0 or more", cfg.Parallelism)
	case len(cfg.Project.Conventions) > 0 && cfg.HasFile == nil:
		return nil, errors.New("the project lists convention files and the kernel has no HasFile to read whether the input tree holds them (decision 0250)")
	}
	k := &kernel{
		cfg: cfg, states: map[string]*workflowv1.State{}, plans: map[string]*planRun{},
		invocations: map[string]*planRun{}, finished: map[string]bool{}, refusals: map[string][]*harnessv1.Refusal{},
		documents: map[string]*runnerv1.Invocation{}, resolved: map[string]bool{}, ended: map[string]bool{}, firedBy: map[string]fired{},
		requestedBy: map[string]bool{}, decisions: map[string][]decision{}, positionOf: map[string]uint64{},
	}
	for _, s := range cfg.Workflow.Definition().GetStates() {
		k.states[s.GetName()] = s
	}
	return k, nil
}

// newPlan returns the run of the plan that a's plan.added, the event id,
// adds, in the workflow's initial state.
func (k *kernel) newPlan(a *eventsv1.PlanAdded, id string) *planRun {
	r := &planRun{
		k: k, plan: a.GetPlan(), track: a.GetTrack(), dependsOn: a.GetDependsOn(), added: id,
		state: k.cfg.Workflow.Definition().GetInitial(),
	}
	// The plan moves only on the transitions that hold under the project's
	// archive step (decision 0296).
	r.m = k.cfg.Workflow.NewMachine(k.cfg.Project.ArchiveStep,
		func(context.Context) (string, error) { return r.state, nil },
		func(_ context.Context, s string) error { r.state = s; return nil },
	)
	return r
}

// streamError returns the error that ends the run when the stream of
// Subscribe ends with err: ctx's error when ctx is done.
func (k *kernel) streamError(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return fmt.Errorf("the subscription to the log ended: %w", err)
}

// head returns the head of the log.
func (k *kernel) head(ctx context.Context) (uint64, error) {
	res, err := k.cfg.Client.Query(ctx, &controlplanev1.QueryRequest{Limit: 1})
	if err != nil {
		return 0, fmt.Errorf("cannot read the head of the log: %w", err)
	}
	return res.GetHead(), nil
}

// reportIdle calls Idle when the kernel has handled every event of the log.
func (k *kernel) reportIdle(ctx context.Context) error {
	if k.cfg.Idle == nil {
		return nil
	}
	head, err := k.head(ctx)
	if err != nil {
		return err
	}
	if head == k.last {
		k.cfg.Idle(head)
	}
	return nil
}

// wrote calls Wrote with position.
func (k *kernel) wrote(position uint64) {
	if k.cfg.Wrote != nil {
		k.cfg.Wrote(position)
	}
}

// final reports whether the plan is in a final state.
func (r *planRun) final() bool {
	return r.k.states[r.state].GetFinal() != nil
}

// merged reports whether the plan is in a final state that has merged
// (decision 0197).
func (r *planRun) merged() bool {
	return r.k.states[r.state].GetFinal().GetMerged()
}

// running reports whether the plan holds a slot: it started and is not
// final (decision 0189).
func (r *planRun) running() bool {
	return r.started && !r.final()
}

// start handles the events of the log up to its head, known, which it
// indexes first, so that it knows which triggers already fired a
// transition. It then recovers each invocation in doubt that handling
// the log did not.
func (k *kernel) start(ctx context.Context, known []*controlplanev1.Entry) error {
	for _, e := range known {
		k.index(e)
	}
	for _, e := range known {
		if err := k.handle(ctx, e); err != nil {
			return err
		}
	}
	for _, r := range k.order {
		if err := r.recoverInDoubt(ctx); err != nil {
			return err
		}
	}
	return k.schedule(ctx)
}

// index records what the kernel needs to know of e ahead of handling it:
// its position, and, for a transition.fired, an invocation.requested of
// the kernel and a decision, the event that caused it, and, for an
// invocation.finished of the kernel, that it resolved the invocation, so
// that the lease.expired before it does not ask the adapter again.
func (k *kernel) index(entry *controlplanev1.Entry) {
	e := entry.GetEvent()
	if _, seen := k.positionOf[e.GetId()]; seen {
		return
	}
	k.positionOf[e.GetId()] = entry.GetPosition()
	cause := e.GetAttributes()[events.ExtensionCause].GetCeString()
	if cause == "" {
		return
	}
	switch e.GetType() {
	case events.TypeTransitionFired:
		var tf eventsv1.TransitionFired
		if e.GetSource() == Source && unpack(e, &tf) == nil {
			k.firedBy[cause] = fired{id: e.GetId(), plan: tf.GetPlan(), to: tf.GetToState()}
		}
	case events.TypeInvocationRequested:
		if e.GetSource() == Source {
			k.requestedBy[cause] = true
		}
	case events.TypeInvocationFinished:
		var f eventsv1.InvocationFinished
		if e.GetSource() == Source && unpack(e, &f) == nil {
			k.resolved[f.GetInvocation()] = true
		}
	case events.TypeDecisionMade:
		var d eventsv1.DecisionMade
		if unpack(e, &d) == nil {
			k.decisions[cause] = append(k.decisions[cause], decision{
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

// handle handles the event of entry in log order: it follows the plans,
// their states and invocations, fires the transition of a trigger of a
// started plan's state, and then starts the plans that may start. A
// trigger that already fired a transition moves the plan to its target at
// once, as when the kernel fired it.
func (k *kernel) handle(ctx context.Context, entry *controlplanev1.Entry) error {
	if entry.GetPosition() <= k.last {
		return nil
	}
	k.last = entry.GetPosition()
	e := entry.GetEvent()
	if err := k.follow(e); err != nil {
		return err
	}
	if err := k.fireFor(ctx, e); err != nil {
		return err
	}
	if err := k.recoverEnded(ctx, e); err != nil {
		return err
	}
	return k.schedule(ctx)
}

// recoverEnded recovers the invocation whose lease e ends, when e is a
// lease.released or a lease.expired and the invocation has no result: the
// invocation is in doubt.
func (k *kernel) recoverEnded(ctx context.Context, e *cloudeventsv1.CloudEvent) error {
	if e.GetType() != events.TypeLeaseReleased && e.GetType() != events.TypeLeaseExpired {
		return nil
	}
	r, ok := k.invocations[subjectOf(e)]
	if !ok {
		return nil
	}
	return r.recoverInDoubt(ctx)
}

// fireFor fires the transition of the trigger that e is, if any.
func (k *kernel) fireFor(ctx context.Context, e *cloudeventsv1.CloudEvent) error {
	if e.GetType() == events.TypePlanAdded {
		return nil
	}
	if f, ok := k.firedBy[e.GetId()]; ok {
		if r, ok := k.plans[f.plan]; ok {
			r.state = f.to
		}
		return nil
	}
	r, trigger, ok := k.triggerOf(e)
	if !ok {
		return nil
	}
	ts, err := r.m.Transitions(ctx, trigger)
	if err != nil || len(ts) == 0 {
		return err
	}
	return r.fire(ctx, e, string(trigger), ts, nil)
}

// follow follows the plans' additions, their invocations, the ends of the
// invocations and their leases.
func (k *kernel) follow(e *cloudeventsv1.CloudEvent) error {
	f, ok := followers[e.GetType()]
	if !ok {
		return nil
	}
	if err := f(k, e); err != nil {
		return fmt.Errorf("the data of the event %s: %w", e.GetId(), err)
	}
	return nil
}

// followers give, by event type, what the kernel follows of an event.
var followers = map[string]func(k *kernel, e *cloudeventsv1.CloudEvent) error{
	events.TypePlanAdded: func(k *kernel, e *cloudeventsv1.CloudEvent) error {
		var a eventsv1.PlanAdded
		if err := unpack(e, &a); err != nil {
			return err
		}
		if _, ok := k.plans[a.GetPlan()]; !ok && a.GetPlan() != "" {
			r := k.newPlan(&a, e.GetId())
			k.plans[r.plan] = r
			k.order = append(k.order, r)
		}
		return nil
	},
	events.TypeInvocationRequested: func(k *kernel, e *cloudeventsv1.CloudEvent) error {
		var req eventsv1.InvocationRequested
		if err := unpack(e, &req); err != nil {
			return err
		}
		doc, err := events.Document(&req)
		if err != nil {
			return err
		}
		k.documents[req.GetInvocation()] = doc
		if r, ok := k.plans[req.GetPlan()]; ok {
			r.invocation, r.requested = req.GetInvocation(), e.GetId()
			r.requests++
			k.invocations[req.GetInvocation()] = r
			if r.tree == "" && k.cfg.Trunk != nil {
				r.tree = doc.GetInput().GetTree()
			}
		}
		return nil
	},
	events.TypeInvocationFinished: func(k *kernel, e *cloudeventsv1.CloudEvent) error {
		var f eventsv1.InvocationFinished
		if err := unpack(e, &f); err != nil {
			return err
		}
		k.finished[f.GetInvocation()] = true
		return nil
	},
	events.TypeInvocationResult: func(k *kernel, e *cloudeventsv1.CloudEvent) error {
		var res eventsv1.InvocationResult
		if err := unpack(e, &res); err != nil {
			return err
		}
		if len(res.GetRefusals()) > 0 {
			k.refusals[res.GetInvocation()] = res.GetRefusals()
		}
		return k.followOutput(&res)
	},
	events.TypeLeaseGranted: func(k *kernel, e *cloudeventsv1.CloudEvent) error {
		var g eventsv1.LeaseGranted
		if err := unpack(e, &g); err != nil {
			return err
		}
		k.ended[g.GetLease()] = false
		return nil
	},
	events.TypeLeaseReleased: func(k *kernel, e *cloudeventsv1.CloudEvent) error {
		var l eventsv1.LeaseReleased
		if err := unpack(e, &l); err != nil {
			return err
		}
		k.ended[l.GetLease()] = true
		return nil
	},
	events.TypeLeaseExpired: func(k *kernel, e *cloudeventsv1.CloudEvent) error {
		var l eventsv1.LeaseExpired
		if err := unpack(e, &l); err != nil {
			return err
		}
		k.ended[l.GetLease()] = true
		return nil
	},
}

// triggerOf returns the started plan and the trigger of its state that e
// is, if any (decision 0170): the result of the state's invocation, or its
// failed or cap_reached by the invocation's outcome (decisions 0203 and
// 0235), the
// entry into the state, as the transition.fired that entered it, or an
// event about the plan from someone other than the kernel. The entry into
// the initial state is handled when the plan starts. Of a plan that has
// not started, only a cancel.requested is a trigger: a person may cancel a
// plan that waits to start (decision 0192).
func (k *kernel) triggerOf(e *cloudeventsv1.CloudEvent) (*planRun, workflow.Trigger, bool) {
	var r *planRun
	trigger := workflow.Trigger("")
	switch e.GetType() {
	case events.TypeInvocationFinished:
		r, trigger = k.resultOf(e), resultTrigger(e)
	case events.TypeTransitionFired:
		r, trigger = k.enteredBy(e), workflow.Entered
	default:
		if p, ok := k.plans[subjectOf(e)]; ok && e.GetSource() != Source {
			r, trigger = p, workflow.Event(e.GetType())
		}
	}
	if r == nil || trigger == "" || (!r.started && trigger != workflow.Event(events.TypeCancelRequested)) {
		return nil, "", false
	}
	return r, trigger, true
}

// resultTrigger returns the trigger that the invocation.finished e is, by
// its outcome: result when the invocation succeeded, or when it is an
// external action that happened, resolved on recovery (decisions 0202 and
// 0235); failed when it failed (decision 0235); and cap_reached when the
// runner stopped it at its wall-time limit or the model gateway refused
// its calls at its token or cost cap (decisions 0203 and 0228). A cancelled
// invocation is no trigger: its plan was cancelled first (decision 0192).
func resultTrigger(e *cloudeventsv1.CloudEvent) workflow.Trigger {
	var f eventsv1.InvocationFinished
	if unpack(e, &f) != nil {
		return ""
	}
	switch f.GetOutcome() {
	case eventsv1.InvocationFinished_OUTCOME_SUCCEEDED, eventsv1.InvocationFinished_OUTCOME_RESOLVED_ON_RECOVERY:
		return workflow.Result
	case eventsv1.InvocationFinished_OUTCOME_FAILED:
		return workflow.Failed
	case eventsv1.InvocationFinished_OUTCOME_CAP_REACHED:
		return workflow.CapReached
	case eventsv1.InvocationFinished_OUTCOME_CANCELLED, eventsv1.InvocationFinished_OUTCOME_UNSPECIFIED:
		return ""
	}
	return ""
}

// resultOf returns the plan whose current invocation the
// invocation.finished e ends, or nil.
func (k *kernel) resultOf(e *cloudeventsv1.CloudEvent) *planRun {
	var f eventsv1.InvocationFinished
	if unpack(e, &f) != nil || f.GetInvocation() == "" {
		return nil
	}
	if r, ok := k.invocations[f.GetInvocation()]; ok && r.invocation == f.GetInvocation() {
		return r
	}
	return nil
}

// enteredBy returns the plan whose current state the kernel's
// transition.fired e entered, or nil.
func (k *kernel) enteredBy(e *cloudeventsv1.CloudEvent) *planRun {
	var tf eventsv1.TransitionFired
	if e.GetSource() != Source || unpack(e, &tf) != nil {
		return nil
	}
	if r, ok := k.plans[tf.GetPlan()]; ok && tf.GetToState() == r.state {
		return r
	}
	return nil
}

// schedule starts, in the order they were added, the plans that may start
// while fewer plans than the parallelism setting run (decisions 0189 and
// 0190). A plan may start when every plan it depends on has merged. A plan
// that was cancelled before it started never starts.
func (k *kernel) schedule(ctx context.Context) error {
	limit := max(k.cfg.Parallelism, 1)
	for _, r := range k.order {
		if k.runningCount() >= limit {
			return nil
		}
		if r.started || r.final() || !k.mergedAll(r.dependsOn) {
			continue
		}
		if err := r.begin(ctx); err != nil {
			return err
		}
	}
	return nil
}

// runningCount returns how many plans hold a slot.
func (k *kernel) runningCount() int {
	n := 0
	for _, r := range k.order {
		if r.running() {
			n++
		}
	}
	return n
}

// mergedAll reports whether every plan of plans has merged.
func (k *kernel) mergedAll(plans []string) bool {
	for _, p := range plans {
		if r, ok := k.plans[p]; !ok || !r.merged() {
			return false
		}
	}
	return true
}

// begin starts the plan: it handles the entry into its initial state, with
// the plan.added as its trigger. When the state invokes, it appends the
// invocation.requested of its first invocation, caused by the plan.added,
// unless the log has it; then it fires the state's transitions on entry,
// as for any trigger.
func (r *planRun) begin(ctx context.Context) error {
	r.started = true
	if err := r.readTrunk(ctx); err != nil {
		return err
	}
	initial := r.k.states[r.state]
	if inv, ok := initial.GetEntry().(*workflowv1.State_Invoke); ok && !r.k.requestedBy[r.added] {
		req, err := r.request(ctx, r.state, inv.Invoke, r.added)
		if err != nil {
			return err
		}
		if err := r.append(ctx, []*cloudeventsv1.CloudEvent{req}); err != nil {
			return err
		}
		r.k.requestedBy[r.added] = true
	}
	if f, ok := r.k.firedBy[r.added]; ok {
		r.state = f.to
		return nil
	}
	ts, err := r.m.Transitions(ctx, workflow.Entered)
	if err != nil || len(ts) == 0 {
		return err
	}
	return r.fire(ctx, &cloudeventsv1.CloudEvent{Id: r.added}, string(workflow.Entered), ts, nil)
}

// readTrunk reads the trunk's tree for the plan, unless the wiring gives
// no trunk or the plan has its tree (decision 0209).
func (r *planRun) readTrunk(ctx context.Context) error {
	if r.tree != "" || r.k.cfg.Trunk == nil {
		return nil
	}
	tree, err := r.k.cfg.Trunk(ctx)
	if err != nil {
		return fmt.Errorf("cannot read the trunk's tree for the plan %s: %w", r.plan, err)
	}
	if tree == "" {
		return fmt.Errorf("the trunk has no tree for the plan %s", r.plan)
	}
	r.tree = tree
	return nil
}

// fire decides the transitions ts for the trigger e in order and fires the
// first one allowed. A decision that the log already has for e is used
// instead of a new one, so that a kernel that stopped between a decision
// and its transition makes no decision twice (decision 0187). When every
// transition is denied, nothing fires. denied lists the actions that were
// denied before ts, for the question of an escalation.
func (r *planRun) fire(ctx context.Context, e *cloudeventsv1.CloudEvent, trigger string, ts []workflow.Transition, denied []string) error {
	made := r.k.madeFor(e.GetId())
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
func (k *kernel) madeFor(id string) []decision {
	var out []decision
	at := k.positionOf[id]
	for _, d := range k.decisions[id] {
		if k.positionOf[d.id] > at {
			out = append(out, d)
		}
	}
	return out
}

// decide asks the control plane to decide action for the plan, with the
// plan's last invocation as the key of its facts and cause as the
// decision's cause, and returns the decision.
func (r *planRun) decide(ctx context.Context, action, cause string) (decision, error) {
	res, err := r.k.cfg.Client.Decide(ctx, &controlplanev1.DecideRequest{
		Action: action, Plan: r.plan, Cause: cause,
		Keys: &controlplanev1.FactKeys{Invocation: r.invocation},
	})
	if err != nil {
		return decision{}, fmt.Errorf("cannot decide %s for the plan %s: %w", action, r.plan, err)
	}
	r.k.wrote(res.GetPosition())
	q, err := r.k.cfg.Client.Query(ctx, &controlplanev1.QueryRequest{After: res.GetPosition() - 1, Limit: 1})
	if err != nil {
		return decision{}, fmt.Errorf("cannot read the decision of %s at %d: %w", action, res.GetPosition(), err)
	}
	if len(q.GetEvents()) != 1 {
		return decision{}, fmt.Errorf("the log has no decision of %s at %d", action, res.GetPosition())
	}
	r.k.index(q.GetEvents()[0])
	return decision{
		id: q.GetEvents()[0].GetEvent().GetId(), allow: res.GetOutcome() == eventsv1.DecisionMade_OUTCOME_ALLOW,
	}, nil
}

// transition fires t for the trigger e, allowed by d: it appends
// transition.fired with e as its cause, invocation.requested when the
// target invokes, cancel.requested for the plan's running invocation when
// the target is final, and escalation.raised when t escalates, and moves
// the plan to the target. The runner stops an invocation whose cancel it
// learns of in a heartbeat and reports it cancelled, and a result that
// comes after the plan is final fires nothing (decisions 0035 and 0192).
func (r *planRun) transition(ctx context.Context, e *cloudeventsv1.CloudEvent, trigger string, t workflow.Transition,
	d decision, denied []string,
) error {
	target := r.k.states[t.Target]
	tf, err := r.event(&eventsv1.TransitionFired{
		Plan: r.plan, FromState: r.state, ToState: t.Target, Trigger: trigger, Decision: d.id, Gate: target.GetGate(),
	}, e.GetId())
	if err != nil {
		return err
	}
	evs := []*cloudeventsv1.CloudEvent{tf}
	if inv, ok := target.GetEntry().(*workflowv1.State_Invoke); ok {
		req, err := r.request(ctx, t.Target, inv.Invoke, tf.GetId())
		if err != nil {
			return err
		}
		evs = append(evs, req)
	}
	if target.GetFinal() != nil && r.runningInvocation() {
		stop, err := r.event(&eventsv1.CancelRequested{
			Invocation: r.invocation, Plan: r.plan, RequestedBy: requestedBy(e),
		}, tf.GetId())
		if err != nil {
			return err
		}
		evs = append(evs, stop)
	}
	if t.Action == escalate {
		raised, err := r.event(&eventsv1.EscalationRaised{Plan: r.plan, Question: r.question(trigger, denied)}, tf.GetId())
		if err != nil {
			return err
		}
		evs = append(evs, raised)
	}
	if err := r.append(ctx, evs); err != nil {
		return err
	}
	r.k.firedBy[e.GetId()] = fired{id: tf.GetId(), plan: r.plan, to: t.Target}
	return r.m.Fire(ctx, t.Index)
}

// runningInvocation reports whether the plan's last invocation was
// requested and has not finished.
func (r *planRun) runningInvocation() bool {
	inv := r.invocation
	return inv != "" && !r.k.finished[inv] && !r.k.resolved[inv]
}

// requestedBy returns who asked for the cancel that the trigger e is, for
// the kernel's cancel of the plan's running invocation: the person who
// cancelled the plan, or the kernel when e is not a cancel.requested.
func requestedBy(e *cloudeventsv1.CloudEvent) string {
	var c eventsv1.CancelRequested
	if e.GetType() == events.TypeCancelRequested && unpack(e, &c) == nil && c.GetRequestedBy() != "" {
		return c.GetRequestedBy()
	}
	return "kernel"
}

// question returns the question of the escalation of the plan from its
// state on trigger, after the actions denied were denied. When the result
// of the plan's last invocation has refusals that the harness reported, it
// names them (decision 0249).
func (r *planRun) question(trigger string, denied []string) string {
	q := r.reason(trigger, denied)
	if refused := shared.Refusals(r.k.refusals[r.invocation]); refused != "" && trigger != InDoubt {
		q += " The harness refused: " + refused + " (decision 0249)."
	}
	return q
}

// reason returns the question of the escalation without the refusals.
func (r *planRun) reason(trigger string, denied []string) string {
	if trigger == InDoubt {
		if len(denied) == 0 {
			return fmt.Sprintf("The lease of the invocation %s of the state %s ended with no result, so it may have run (decisions 0191 and 0199).",
				r.invocation, r.state)
		}
		return fmt.Sprintf("The lease of the invocation %s of the state %s ended with no result, so it may have run, and running it again was denied: %s denied (decisions 0191 and 0201).",
			r.invocation, r.state, strings.Join(denied, ", then "))
	}
	if trigger == string(workflow.CapReached) {
		return fmt.Sprintf("The invocation %s of the state %s reached a cap of its budget: its wall-time limit, at which the runner stopped it, or its token or cost cap, at which the model gateway refused its calls (decisions 0193, 0203 and 0228).",
			r.invocation, r.state)
	}
	if trigger == string(workflow.Failed) {
		if len(denied) == 0 {
			return fmt.Sprintf("The invocation %s of the state %s failed (decision 0235).", r.invocation, r.state)
		}
		return fmt.Sprintf("The invocation %s of the state %s failed, and running it again was denied: %s denied (decision 0235).",
			r.invocation, r.state, strings.Join(denied, ", then "))
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
// 0019). The task of an agent run is the workflow's task text followed by
// the project section, so the text stays the same for every project
// (decision 0250), then the narratives and the coverage report when the
// state asks for them (decision 0298); an external action gets the text
// alone, and a check its task id, which names the check program's
// operation (decision 0297).
func (r *planRun) request(ctx context.Context, state string, inv *workflowv1.Invoke, cause string) (*cloudeventsv1.CloudEvent, error) {
	task, ok := r.k.cfg.Workflow.Task(inv.GetTask())
	if !ok {
		return nil, fmt.Errorf("the state %s names the task %q, which the workflow does not have", state, inv.GetTask())
	}
	doc := proto.CloneOf(r.k.cfg.Base)
	if doc == nil {
		doc = &runnerv1.Invocation{}
	}
	id := fmt.Sprintf("%s/%d", r.plan, r.requests+1)
	doc.Invocation, doc.IdempotencyKey = id, id
	doc.Role = roleOf[inv.GetRole()]
	if doc.Component == nil {
		doc.Component = &harnessv1.Component{}
	}
	doc.Component.Kind = kindOf[inv.GetKind()]
	if inv.GetKind() == workflowv1.Invoke_KIND_CHECK {
		// A check runs the program that the configuration names for its
		// component, whose version is the program's own (decision 0272).
		doc.Component.Name, doc.Component.Version = inv.GetComponent(), ""
		// The archive step's result carries the workspace's tree, and the
		// runner drops the file changes of any other check (decision 0282).
		doc.ReturnsTree = inv.GetReturnsTree()
	}
	if doc.Input == nil {
		doc.Input = &harnessv1.Input{}
	}
	if r.tree != "" {
		doc.Input.Tree = r.tree
	}
	// The base tree is the plan's tree, which a check such as the archive
	// step reads from the workspace's .git (decision 0285).
	doc.Input.BaseTree = r.tree
	doc.Input.Task = task
	if inv.GetKind() == workflowv1.Invoke_KIND_HARNESS {
		text, err := r.agentTask(ctx, task, inv, doc.GetInput().GetTree())
		if err != nil {
			return nil, fmt.Errorf("the invocation of the state %s: %w", state, err)
		}
		doc.Input.Task = text
	}
	doc.OutputMessage = inv.GetOutputMessage()
	doc.ModelRole = inv.GetModelRole()
	data, err := events.InvocationRequested(r.plan, doc)
	if err != nil {
		return nil, fmt.Errorf("the invocation of the state %s: %w", state, err)
	}
	return r.event(data, cause)
}

// agentTask returns the task of an agent run that the state starts with
// inv, on the input tree tree: the task text, the project section and,
// when the state asks for them, the narratives and the coverage report
// (decisions 0250 and 0298).
func (r *planRun) agentTask(ctx context.Context, task string, inv *workflowv1.Invoke, tree string) (string, error) {
	section, err := r.projectSection(ctx, tree)
	if err != nil {
		return "", err
	}
	if inv.GetNarratives() {
		section += "\n" + r.narrativesSection()
	}
	if inv.GetCoverage() {
		section += "\n" + r.coverageSection()
	}
	if !strings.HasSuffix(task, "\n") {
		task += "\n"
	}
	return task + "\n" + section, nil
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
		ID: "kernel-" + rand.Text(), Source: Source, Subject: r.plan, Tenant: "kernel", Cause: cause,
	})
	if err != nil {
		return nil, fmt.Errorf("cannot build the kernel's event: %w", err)
	}
	return e, nil
}

// append appends evs.
func (r *planRun) append(ctx context.Context, evs []*cloudeventsv1.CloudEvent) error {
	res, err := r.k.cfg.Client.Append(ctx, &controlplanev1.AppendRequest{Events: evs})
	if err != nil {
		return fmt.Errorf("cannot append the kernel's events for the plan %s: %w", r.plan, err)
	}
	if n := len(res.GetPositions()); n > 0 {
		r.k.wrote(res.GetPositions()[n-1])
	}
	return nil
}

// recoverInDoubt recovers the plan's invocation when the state invokes and
// the invocation was requested, its last lease ended and it has no result:
// it is in doubt and may have run (decisions 0191 and 0199). An invocation
// that no runner claimed has not run, and one whose lease is current may
// still report, so neither is in doubt; a lease that is current when pumi
// starts again gets its full timeout and expires if no runner renews it.
//
// A check in doubt runs again: the control plane grants it again, so the
// kernel does nothing. For an external action, the kernel asks its adapter
// whether the action happened (decision 0202); when it did, the kernel
// appends the invocation's invocation.finished, resolved on recovery, which
// is the state's result when the kernel reads it. Otherwise, and for an
// agent run, the kernel fires the state's transitions on in-doubt, with the
// invocation.requested as their cause (decision 0200): retry runs the
// invocation again as a new one, and escalate escalates.
func (r *planRun) recoverInDoubt(ctx context.Context) error {
	k := r.k
	inv := r.invocation
	if !r.started || inv == "" || k.finished[inv] || k.resolved[inv] || !k.ended[inv] {
		return nil
	}
	invoke, ok := k.states[r.state].GetEntry().(*workflowv1.State_Invoke)
	if !ok {
		return nil
	}
	if _, ok := k.firedBy[r.requested]; ok {
		return nil
	}
	switch invoke.Invoke.GetKind() {
	case workflowv1.Invoke_KIND_CHECK:
		return nil
	case workflowv1.Invoke_KIND_EXTERNAL_ACTION:
		happened, err := r.ask(ctx)
		if err != nil || happened {
			return err
		}
	case workflowv1.Invoke_KIND_HARNESS, workflowv1.Invoke_KIND_UNSPECIFIED:
		// An agent run in doubt runs again through the transitions on
		// in-doubt; the loader refuses a kind that is not given.
	}
	ts, err := r.m.Transitions(ctx, workflow.InDoubt)
	if err != nil {
		return err
	}
	if len(ts) == 0 {
		return fmt.Errorf("the invocation %s of the state %s is in doubt, and the state has no transition on %s (decision 0200)",
			inv, r.state, workflow.InDoubt)
	}
	return r.fire(ctx, &cloudeventsv1.CloudEvent{Id: r.requested}, InDoubt, ts, nil)
}

// ask asks the adapter of the plan's external action in doubt whether the
// action happened, and when it did, appends the invocation's
// invocation.finished with the outcome resolved on recovery (decision
// 0202). It reports whether the action happened. With no Resolve, the
// kernel cannot know whether running the action again is safe, so it
// returns an error.
func (r *planRun) ask(ctx context.Context) (bool, error) {
	k := r.k
	inv := r.invocation
	if k.cfg.Resolve == nil {
		return false, fmt.Errorf("the external action %s is in doubt, and the kernel has no adapter to ask whether it happened (decision 0202)", inv)
	}
	happened, err := k.cfg.Resolve(ctx, proto.CloneOf(k.documents[inv]))
	if err != nil {
		return false, fmt.Errorf("cannot ask whether the external action %s happened: %w", inv, err)
	}
	if !happened {
		return false, nil
	}
	e, err := events.New(&eventsv1.InvocationFinished{
		Invocation: inv, Outcome: eventsv1.InvocationFinished_OUTCOME_RESOLVED_ON_RECOVERY,
	}, events.Attributes{ID: "kernel-" + rand.Text(), Source: Source, Subject: inv, Tenant: "kernel", Cause: r.requested})
	if err != nil {
		return false, fmt.Errorf("cannot build the kernel's event: %w", err)
	}
	if err := r.append(ctx, []*cloudeventsv1.CloudEvent{e}); err != nil {
		return false, err
	}
	k.resolved[inv] = true
	return true, nil
}
