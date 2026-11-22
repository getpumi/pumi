// SPDX-License-Identifier: Apache-2.0
// SPDX-FileCopyrightText: The Pumi Authors

// Package workflow loads the workflow file and builds the state machine
// that the kernel runs a plan on (decisions 0018 and 0166 to 0170).
//
// Load reads a workflow in protobuf text format into
// pumi.workflow.v1.Workflow, reads the text file of each task it names and
// refuses a file that breaks a rule of decision 0168 or 0169. A Machine
// lists a state's transitions for a trigger and fires one of them by its
// index. It never decides and never calls the control plane: the kernel
// asks the control plane about each transition in order and fires the
// first one allowed (decision 0169).
package workflow

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"path"
	"regexp"
	"slices"

	"getpumi.dev/pumi/api/events"
	cloudeventsv1 "getpumi.dev/pumi/api/gen/io/cloudevents/v1"
	controlplanev1 "getpumi.dev/pumi/api/gen/pumi/controlplane/v1"
	eventsv1 "getpumi.dev/pumi/api/gen/pumi/events/v1"
	factsv1 "getpumi.dev/pumi/api/gen/pumi/facts/v1"
	harnessv1 "getpumi.dev/pumi/api/gen/pumi/harness/v1"
	runnerv1 "getpumi.dev/pumi/api/gen/pumi/runner/v1"
	workflowv1 "getpumi.dev/pumi/api/gen/pumi/workflow/v1"
	"getpumi.dev/pumi/api/policy"
	"google.golang.org/protobuf/encoding/prototext"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
)

// Trigger is what fires a transition (decision 0170): Result, Entered or
// the CloudEvents type of an event, as Event gives it.
type Trigger string

const (
	// Result fires on the invocation.finished event of the invocation that
	// the state started.
	Result Trigger = "result"
	// Entered fires as the state is entered, for a state that only decides.
	Entered Trigger = "entered"
)

// Event returns the trigger of the event type typ, such as
// events.TypeApprovalRecorded.
func Event(typ string) Trigger { return Trigger(typ) }

// Transition is one transition of the workflow.
type Transition struct {
	// Index is the transition's position among all the transitions of the
	// workflow, in file order. Machine.Fire takes it.
	Index int
	// State is the state that declares the transition, which is the state
	// the plan is in or one of its superstates.
	State string
	// Trigger is what fires the transition.
	Trigger Trigger
	// Target is the state the transition goes to.
	Target string
	// Action is the Cedar action that guards the transition.
	Action string
}

// Workflow is a loaded workflow.
type Workflow struct {
	def *workflowv1.Workflow
	// states are the states by name.
	states map[string]*workflowv1.State
	// transitions are every transition, in file order.
	transitions []Transition
	// own are the indexes of the transitions each state declares.
	own map[string][]int
	// tasks are the task texts, by task id.
	tasks map[string]string
}

// apiFiles are the protobuf files of the api module, in which an output
// message must be (decision 0168). A test checks that the list has every
// file of api/proto.
var apiFiles = []protoreflect.FileDescriptor{
	cloudeventsv1.File_io_cloudevents_v1_cloudevents_proto,
	controlplanev1.File_pumi_controlplane_v1_controlplane_proto,
	eventsv1.File_pumi_events_v1_basis_proto,
	eventsv1.File_pumi_events_v1_forge_proto,
	eventsv1.File_pumi_events_v1_gates_proto,
	eventsv1.File_pumi_events_v1_invocations_proto,
	eventsv1.File_pumi_events_v1_leases_proto,
	eventsv1.File_pumi_events_v1_people_proto,
	eventsv1.File_pumi_events_v1_plans_proto,
	factsv1.File_pumi_facts_v1_code_proto,
	factsv1.File_pumi_facts_v1_forge_proto,
	factsv1.File_pumi_facts_v1_options_proto,
	factsv1.File_pumi_facts_v1_review_proto,
	factsv1.File_pumi_facts_v1_workflow_proto,
	harnessv1.File_pumi_harness_v1_harness_proto,
	runnerv1.File_pumi_runner_v1_runner_proto,
	workflowv1.File_pumi_workflow_v1_outputs_proto,
	workflowv1.File_pumi_workflow_v1_workflow_proto,
}

// taskID is the form of a task id, which is also the base name of its text
// file, so that an id cannot name a file outside the workflow's directory.
var taskID = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)

// taskHeader is the SPDX header that every task text file starts with
// (decision 0068). It is not part of the task's text.
const taskHeader = "<!--\nSPDX-License-Identifier: Apache-2.0\nSPDX-FileCopyrightText: The Pumi Authors\n-->\n"

// Load reads the workflow file at name in fsys and the text of each task it
// names, the file <task>.md in the same directory, and checks the
// workflow. It refuses, with an error for each problem that names the
// state or transition: a duplicate or unknown state, a parent cycle, a
// state that cannot be reached from the initial state, a state that is not
// final and has no transitions, a Cedar action that pumi.cedarschema does
// not declare, an output message that the api module does not have, a
// task with no text file, an unknown trigger, and a trigger whose last
// transition in a state is not always allowed (decisions 0168 and 0169).
// It also refuses what the machine could not run: a state with no entry, a
// transition with no trigger, an invocation with no role or kind, a
// superstate that does more than wait or that a transition or the initial
// state names, a final state with transitions, and a result trigger in a
// state that starts no invocation.
func Load(fsys fs.FS, name string) (*Workflow, error) {
	src, err := fs.ReadFile(fsys, name)
	if err != nil {
		return nil, fmt.Errorf("cannot read the workflow: %w", err)
	}
	def := &workflowv1.Workflow{}
	if err := prototext.Unmarshal(src, def); err != nil {
		return nil, fmt.Errorf("cannot parse the workflow %s: %w", name, err)
	}
	l := &loader{fsys: fsys, dir: path.Dir(name), def: def, w: &Workflow{
		def:    def,
		states: map[string]*workflowv1.State{},
		own:    map[string][]int{},
		tasks:  map[string]string{},
	}}
	l.check()
	if err := errors.Join(l.problems...); err != nil {
		return nil, fmt.Errorf("the workflow %s is refused: %w", name, err)
	}
	return l.w, nil
}

// Definition returns a copy of the workflow as the file declares it.
func (w *Workflow) Definition() *workflowv1.Workflow {
	return proto.CloneOf(w.def)
}

// Task returns the text of the task id, without its SPDX header.
func (w *Workflow) Task(id string) (string, bool) {
	text, ok := w.tasks[id]
	return text, ok
}

// Transitions returns every transition of the workflow, in file order.
func (w *Workflow) Transitions() []Transition {
	return slices.Clone(w.transitions)
}

// loader checks one workflow and collects its problems.
type loader struct {
	fsys     fs.FS
	dir      string
	def      *workflowv1.Workflow
	w        *Workflow
	problems []error
	// children are the substates of each state.
	children map[string][]string
}

// problem records a problem.
func (l *loader) problem(format string, args ...any) {
	l.problems = append(l.problems, fmt.Errorf(format, args...))
}

// check runs every check, in an order where each can rely on the earlier
// ones: the names first, then the parents, then each state's entry and
// transitions, then reachability.
func (l *loader) check() {
	if l.def.GetName() == "" {
		l.problem("the workflow has no name")
	}
	if l.def.GetVersion() == "" {
		l.problem("the workflow has no version")
	}
	l.checkNames()
	if _, ok := l.w.states[l.def.GetInitial()]; !ok {
		l.problem("the initial state %q is unknown", l.def.GetInitial())
	}
	if !l.checkParents() {
		return
	}
	if _, ok := l.children[l.def.GetInitial()]; ok {
		l.problem("the initial state %q is a superstate; the initial state must be a state with no substates", l.def.GetInitial())
	}
	for _, s := range l.def.GetStates() {
		l.checkEntry(s)
	}
	for _, s := range l.def.GetStates() {
		l.checkTransitions(s)
	}
	if len(l.problems) == 0 {
		l.checkReachable()
	}
}

// checkNames indexes the states by name and refuses a state with no name
// or one declared more than once.
func (l *loader) checkNames() {
	for i, s := range l.def.GetStates() {
		switch _, dup := l.w.states[s.GetName()]; {
		case s.GetName() == "":
			l.problem("state %d has no name", i+1)
		case dup:
			l.problem("state %q is declared more than once", s.GetName())
		default:
			l.w.states[s.GetName()] = s
		}
	}
}

// checkParents refuses an unknown parent and a parent cycle, and records
// the substates of each state. It reports whether the parents are sound,
// since the later checks walk them.
func (l *loader) checkParents() bool {
	sound := true
	l.children = map[string][]string{}
	for _, s := range l.def.GetStates() {
		p := s.GetParent()
		if p == "" {
			continue
		}
		if _, ok := l.w.states[p]; !ok {
			l.problem("state %q has the unknown parent %q", s.GetName(), p)
			sound = false
			continue
		}
		l.children[p] = append(l.children[p], s.GetName())
	}
	if !sound {
		return false
	}
	for _, s := range l.def.GetStates() {
		seen := map[string]bool{}
		for at := s.GetName(); at != ""; at = l.w.states[at].GetParent() {
			if seen[at] {
				l.problem("state %q is in or below a parent cycle", s.GetName())
				sound = false
				break
			}
			seen[at] = true
		}
	}
	return sound
}

// checkEntry checks what s does on entry, and reads the text of the task
// of an invocation.
func (l *loader) checkEntry(s *workflowv1.State) {
	name := s.GetName()
	_, super := l.children[name]
	switch e := s.GetEntry().(type) {
	case nil:
		l.problem("state %q has no entry: it must invoke, wait or be final", name)
	case *workflowv1.State_Wait:
	case *workflowv1.State_Final:
		if super {
			l.problem("state %q is a superstate and final; a superstate only waits", name)
		}
	case *workflowv1.State_Invoke:
		if super {
			l.problem("state %q is a superstate and starts an invocation; a superstate only waits", name)
		}
		l.checkInvoke(name, e.Invoke)
	}
}

// checkInvoke checks the invocation that state starts.
func (l *loader) checkInvoke(state string, inv *workflowv1.Invoke) {
	if inv.GetRole() == workflowv1.Invoke_ROLE_UNSPECIFIED {
		l.problem("state %q starts an invocation with no role", state)
	}
	if inv.GetKind() == workflowv1.Invoke_KIND_UNSPECIFIED {
		l.problem("state %q starts an invocation with no kind", state)
	}
	if !hasMessage(inv.GetOutputMessage()) {
		l.problem("state %q expects the output message %q, which the api module does not have", state, inv.GetOutputMessage())
	}
	l.readTask(state, inv.GetTask())
}

// hasMessage reports whether name is the full name of a message in a file
// of the api module.
func hasMessage(name string) bool {
	full := protoreflect.FullName(name)
	if !full.IsValid() {
		return false
	}
	for _, fd := range apiFiles {
		if findMessage(fd.Messages(), full) {
			return true
		}
	}
	return false
}

// findMessage reports whether ms or a message nested in them is named full.
func findMessage(ms protoreflect.MessageDescriptors, full protoreflect.FullName) bool {
	for i := range ms.Len() {
		m := ms.Get(i)
		if m.FullName() == full || findMessage(m.Messages(), full) {
			return true
		}
	}
	return false
}

// readTask reads the text file of task, which state names.
func (l *loader) readTask(state, task string) {
	if !taskID.MatchString(task) {
		l.problem("state %q names the task %q, which is not an id of lowercase letters, digits and hyphens", state, task)
		return
	}
	if _, ok := l.w.tasks[task]; ok {
		return
	}
	file := path.Join(l.dir, task+".md")
	b, err := fs.ReadFile(l.fsys, file)
	if err != nil {
		l.problem("state %q names the task %q, which has no text file %s: %w", state, task, file, err)
		return
	}
	l.w.tasks[task] = string(bytes.TrimPrefix(b, []byte(taskHeader)))
}

// triggerOf returns the trigger of tr, or an error for one that is not
// result, entered or an event type.
func triggerOf(tr *workflowv1.Trigger) (Trigger, error) {
	switch k := tr.GetKind().(type) {
	case *workflowv1.Trigger_Result:
		return Result, nil
	case *workflowv1.Trigger_Entered:
		return Entered, nil
	case *workflowv1.Trigger_Event:
		if !slices.Contains(events.Types(), k.Event) {
			return "", fmt.Errorf("has the unknown trigger %q, which is not result, entered or an event type", k.Event)
		}
		return Event(k.Event), nil
	default:
		return "", errors.New("has no trigger")
	}
}

// checkTransitions checks the transitions of s and adds them to the
// workflow.
func (l *loader) checkTransitions(s *workflowv1.State) {
	name := s.GetName()
	_, final := s.GetEntry().(*workflowv1.State_Final)
	_, invokes := s.GetEntry().(*workflowv1.State_Invoke)
	switch {
	case final && len(s.GetTransitions()) > 0:
		l.problem("state %q is final and has transitions", name)
	case !final && len(s.GetTransitions()) == 0:
		l.problem("state %q is not final and has no transitions", name)
	}
	last := map[Trigger]int{}
	for i, t := range s.GetTransitions() {
		trig := l.checkTransition(fmt.Sprintf("state %q, transition %d", name, i+1), t, invokes)
		if trig != "" {
			last[trig] = i
		}
		l.w.own[name] = append(l.w.own[name], len(l.w.transitions))
		l.w.transitions = append(l.w.transitions, Transition{
			Index: len(l.w.transitions), State: name, Trigger: trig, Target: t.GetTarget(), Action: t.GetAction(),
		})
	}
	always := policy.AlwaysAllowed()
	for _, i := range slices.Sorted(maps.Values(last)) {
		t := s.GetTransitions()[i]
		if !slices.Contains(always, t.GetAction()) {
			trig, _ := triggerOf(t.GetTrigger())
			l.problem("state %q, transition %d is the last transition on %q and is guarded by %q, which is not always allowed; the last must be one of %q, so that the plan always has a move (decision 0169)",
				name, i+1, trig, t.GetAction(), always)
		}
	}
}

// checkTransition checks the transition t, which at names, of a state
// that starts an invocation when invokes is true, and returns its trigger,
// or "" when it has none that is known.
func (l *loader) checkTransition(at string, t *workflowv1.Transition, invokes bool) Trigger {
	trig, err := triggerOf(t.GetTrigger())
	if err != nil {
		l.problem("%s %w", at, err)
	}
	if trig == Result && !invokes {
		l.problem("%s is triggered by %q, but the state starts no invocation", at, Result)
	}
	if _, ok := l.w.states[t.GetTarget()]; !ok {
		l.problem("%s goes to the unknown state %q", at, t.GetTarget())
	} else if _, super := l.children[t.GetTarget()]; super {
		l.problem("%s goes to the superstate %q; a transition goes to a state with no substates", at, t.GetTarget())
	}
	if !slices.Contains(policy.Actions(), t.GetAction()) {
		l.problem("%s is guarded by the Cedar action %q, which pumi.cedarschema does not declare", at, t.GetAction())
	}
	return trig
}

// checkReachable refuses a state that cannot be reached from the initial
// state. A plan in a state is also in each of its superstates, so their
// transitions apply as well, and a superstate is reached when one of its
// substates is.
func (l *loader) checkReachable() {
	reached := map[string]bool{}
	queue := []string{l.def.GetInitial()}
	for len(queue) > 0 {
		at := queue[0]
		queue = queue[1:]
		if reached[at] {
			continue
		}
		reached[at] = true
		for s := at; s != ""; s = l.w.states[s].GetParent() {
			reached[s] = true
			for _, i := range l.w.own[s] {
				queue = append(queue, l.w.transitions[i].Target)
			}
		}
	}
	for _, s := range l.def.GetStates() {
		if !reached[s.GetName()] {
			l.problem("state %q cannot be reached from the initial state %q", s.GetName(), l.def.GetInitial())
		}
	}
}
