// SPDX-License-Identifier: Apache-2.0
// SPDX-FileCopyrightText: The Pumi Authors

package workflow

import (
	"context"
	"io/fs"
	"os"
	"slices"
	"strings"
	"testing"

	"getpumi.dev/pumi/api/events"
)

// load loads the fixture name from testdata.
func load(t *testing.T, name string) (*Workflow, error) {
	t.Helper()
	return Load(os.DirFS("testdata"), name+".txtpb")
}

func TestAValidWorkflowLoads(t *testing.T) {
	w, err := load(t, "valid")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	def := w.Definition()
	if def.GetName() != "small" || def.GetVersion() != "1" || def.GetInitial() != "planning" || len(def.GetStates()) != 7 {
		t.Errorf("Definition() = %v", def)
	}
	def.Name = "changed"
	if w.Definition().GetName() != "small" {
		t.Error("Definition returns the loader's own message, which an importer can change")
	}
}

func TestModelRolesListsTheRolesOfTheAgentRuns(t *testing.T) {
	w, err := load(t, "valid")
	if err != nil {
		t.Fatal(err)
	}
	if got := w.ModelRoles(); !slices.Equal(got, []string{"frontier"}) {
		t.Errorf("ModelRoles() = %q, want [frontier]", got)
	}
}

func TestTheTaskTextsAreReadWithoutTheirSPDXHeader(t *testing.T) {
	w, err := load(t, "valid")
	if err != nil {
		t.Fatal(err)
	}
	for task, want := range map[string]string{"plan": "Write the plan for the change.\n", "review-plan": "Review the plan.\n"} {
		got, ok := w.Task(task)
		if !ok || got != want {
			t.Errorf("Task(%q) = %q, %v, want %q", task, got, ok, want)
		}
	}
	if _, ok := w.Task("missing"); ok {
		t.Error(`Task("missing") is found`)
	}
}

func TestTheLoaderRefuses(t *testing.T) {
	// Each fixture is the valid one with one change, and its error names
	// the state or transition and the reason (decisions 0168 to 0170).
	tests := []struct {
		fixture string
		want    []string
	}{
		{"duplicate-state", []string{`state "review"`, "declared more than once"}},
		{"unknown-target", []string{`state "planning", transition 1`, `unknown state "nowhere"`}},
		{"unknown-parent", []string{`state "planning"`, `unknown parent "nowhere"`}},
		{"unknown-initial", []string{`initial state "nowhere"`, "unknown"}},
		{"parent-cycle", []string{`state "active"`, "parent cycle"}},
		{"unreachable-state", []string{`state "review"`, "cannot be reached from the initial state"}},
		{"no-transitions", []string{`state "planning-escalated"`, "not final and has no transitions"}},
		{"unknown-action", []string{`state "planning", transition 1`, `Cedar action "jump"`, "pumi.cedarschema"}},
		{"unknown-output-message", []string{`state "planning"`, `output message "google.protobuf.Empty"`, "api module"}},
		{"missing-task-text", []string{`state "planning"`, `task "missing"`, "missing.md"}},
		{"unknown-trigger", []string{`state "planning-escalated", transition 1`, `unknown trigger "dev.getpumi.escalation.answered"`}},
		{"last-not-always-allowed", []string{`state "planning", transition 3`, `last transition on "result"`, `"advance"`, "always allowed"}},
		{"superstate-target", []string{`state "planning", transition 2`, `superstate "active"`}},
		{"superstate-entry", []string{`state "active"`, "superstate", "waits"}},
		{"final-with-transitions", []string{`state "done"`, "final", "has transitions"}},
		{"result-without-invoke", []string{`state "planning-escalated", transition 2`, `"result"`, "starts no invocation"}},
		{"no-entry", []string{`state "planning-escalated"`, "no entry"}},
		{"no-trigger", []string{`state "planning", transition 1`, "no trigger"}},
		{"unspecified-role", []string{`state "planning"`, "no role"}},
		{"unparsable", []string{"unparsable.txtpb"}},
		{"in-doubt-missing", []string{`state "planning"`, "agent run", `no transition on "in-doubt"`}},
		{"in-doubt-on-check", []string{`state "review", transition 4`, `"in-doubt"`, "check", "runs again"}},
		{"in-doubt-without-invoke", []string{`state "planning-escalated", transition 3`, `"in-doubt"`, "starts no invocation"}},
		{"cap-reached-missing", []string{`state "planning"`, "starts an invocation", `no transition on "cap-reached"`}},
		{"cap-reached-without-invoke", []string{`state "planning-escalated", transition 3`, `"cap-reached"`, "starts no invocation"}},
		{"failed-missing", []string{`state "planning"`, "starts an invocation", `no transition on "failed"`}},
		{"failed-without-invoke", []string{`state "planning-escalated", transition 3`, `"failed"`, "starts no invocation"}},
		{"model-role-missing", []string{`state "planning"`, "harness", "no model role"}},
		{"model-role-on-check", []string{`state "review"`, "check", `model role "frontier"`}},
	}
	for _, tt := range tests {
		t.Run(tt.fixture, func(t *testing.T) {
			w, err := load(t, tt.fixture)
			if err == nil {
				t.Fatalf("Load = %v, want an error", w)
			}
			for _, want := range tt.want {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("the error %q does not contain %q", err, want)
				}
			}
		})
	}
}

func TestEveryFixtureIsTested(t *testing.T) {
	names, err := fs.Glob(os.DirFS("testdata"), "*.txtpb")
	if err != nil {
		t.Fatal(err)
	}
	if len(names) != 30 {
		t.Errorf("testdata has %d workflow fixtures, want the valid one and 29 refusals: %q", len(names), names)
	}
}

func TestEveryProtoFileOfTheAPIModuleIsSearchedForOutputMessages(t *testing.T) {
	var want []string
	err := fs.WalkDir(os.DirFS("../../../api/proto"), ".", func(p string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() && strings.HasSuffix(p, ".proto") {
			want = append(want, p)
		}
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	got := []string{}
	for _, fd := range apiFiles {
		got = append(got, fd.Path())
	}
	slices.Sort(got)
	slices.Sort(want)
	if !slices.Equal(got, want) {
		t.Errorf("apiFiles = %q, want every file of api/proto %q", got, want)
	}
}

// storage is the state of one plan, kept outside the machine.
type storage struct{ state string }

func (s *storage) load(context.Context) (string, error) { return s.state, nil }

func (s *storage) store(_ context.Context, state string) error {
	s.state = state
	return nil
}

// machine loads the valid fixture and returns a machine in state.
func machine(t *testing.T, state string) (*Machine, *storage) {
	t.Helper()
	w, err := load(t, "valid")
	if err != nil {
		t.Fatal(err)
	}
	s := &storage{state: state}
	return w.NewMachine(s.load, s.store), s
}

// summary writes transitions as "state:action>target" for comparison.
func summary(ts []Transition) []string {
	out := []string{}
	for _, tr := range ts {
		out = append(out, tr.State+":"+tr.Action+">"+tr.Target)
	}
	return out
}

func TestTransitionsAreTheStatesOwnThenItsSuperstatesInFileOrder(t *testing.T) {
	m, _ := machine(t, "review")
	ctx := context.Background()
	tests := []struct {
		trigger Trigger
		want    []string
	}{
		{Result, []string{"review:planReview>done", "review:retry>planning", "review:escalate>review-escalated"}},
		{Event(events.TypeCancelRequested), []string{"active:cancel>cancelled"}},
		{Event(events.TypeApprovalRecorded), []string{}},
		{Entered, []string{}},
		{InDoubt, []string{"review:retry>review", "review:escalate>review-escalated"}},
		{CapReached, []string{"review:escalate>review-escalated"}},
		{Failed, []string{"review:retry>review", "review:escalate>review-escalated"}},
	}
	for _, tt := range tests {
		got, err := m.Transitions(ctx, tt.trigger)
		if err != nil {
			t.Fatal(err)
		}
		if !slices.Equal(summary(got), tt.want) {
			t.Errorf("Transitions(%q) = %q, want %q", tt.trigger, summary(got), tt.want)
		}
		for _, tr := range got {
			if tr.Trigger != tt.trigger {
				t.Errorf("%v has the trigger %q, want %q", tr, tr.Trigger, tt.trigger)
			}
		}
	}
}

func TestEachTransitionHasItsOwnIndex(t *testing.T) {
	w, err := load(t, "valid")
	if err != nil {
		t.Fatal(err)
	}
	ts := w.Transitions()
	if len(ts) != 22 {
		t.Fatalf("the workflow has %d transitions, want 22", len(ts))
	}
	for i, tr := range ts {
		if tr.Index != i {
			t.Errorf("transition %d has the index %d", i, tr.Index)
		}
	}
}

func TestFireTakesTheTransitionByItsIndex(t *testing.T) {
	// The kernel decides and then fires the transition it chose; the machine
	// has no guards, so firing a later transition, such as escalate, needs no
	// decision of the earlier ones (decision 0169).
	m, s := machine(t, "review")
	ctx := context.Background()
	ts, err := m.Transitions(ctx, Result)
	if err != nil {
		t.Fatal(err)
	}
	if err := m.Fire(ctx, ts[2].Index); err != nil {
		t.Fatalf("Fire escalate: %v", err)
	}
	if s.state != "review-escalated" {
		t.Errorf("the state is %q, want review-escalated", s.state)
	}
}

func TestFireReentersAStateThatTransitionsToItself(t *testing.T) {
	m, s := machine(t, "review-escalated")
	ctx := context.Background()
	ts, err := m.Transitions(ctx, Event(events.TypeOverrideRecorded))
	if err != nil {
		t.Fatal(err)
	}
	if err := m.Fire(ctx, ts[1].Index); err != nil {
		t.Fatalf("Fire escalate to itself: %v", err)
	}
	if s.state != "review-escalated" {
		t.Errorf("the state is %q, want review-escalated", s.state)
	}
}

func TestFireTakesASuperstatesTransitionFromASubstate(t *testing.T) {
	m, s := machine(t, "planning")
	ctx := context.Background()
	ts, err := m.Transitions(ctx, Event(events.TypeCancelRequested))
	if err != nil || len(ts) != 1 {
		t.Fatalf("Transitions(cancel.requested) = %v, %v", ts, err)
	}
	if err := m.Fire(ctx, ts[0].Index); err != nil {
		t.Fatalf("Fire cancel: %v", err)
	}
	if s.state != "cancelled" {
		t.Errorf("the state is %q, want cancelled", s.state)
	}
}

func TestFireRefusesATransitionOfAnotherState(t *testing.T) {
	m, s := machine(t, "planning")
	ctx := context.Background()
	planning, err := m.Transitions(ctx, Result)
	if err != nil {
		t.Fatal(err)
	}
	s.state = "review-escalated"
	err = m.Fire(ctx, planning[0].Index)
	if err == nil || !strings.Contains(err.Error(), `"review-escalated"`) {
		t.Errorf("Fire of a transition of planning in review-escalated: %v, want an error that names the state", err)
	}
	if s.state != "review-escalated" {
		t.Errorf("the state is %q, want it unchanged", s.state)
	}
	for _, i := range []int{-1, 22} {
		if err := m.Fire(ctx, i); err == nil {
			t.Errorf("Fire(%d) succeeded, want an error", i)
		}
	}
}

func TestAFinalStateHasNoTransitions(t *testing.T) {
	m, _ := machine(t, "done")
	ctx := context.Background()
	ts, err := m.Transitions(ctx, Event(events.TypeCancelRequested))
	if err != nil || len(ts) != 0 {
		t.Errorf("Transitions in done = %v, %v, want none", ts, err)
	}
	if err := m.Fire(ctx, 0); err == nil || !strings.Contains(err.Error(), "final") {
		t.Errorf("Fire in done: %v, want an error that says the state is final", err)
	}
}

func TestAnUnknownStateIsAnError(t *testing.T) {
	m, _ := machine(t, "elsewhere")
	if _, err := m.Transitions(context.Background(), Result); err == nil || !strings.Contains(err.Error(), `"elsewhere"`) {
		t.Errorf("Transitions in an unknown state: %v, want an error that names it", err)
	}
}

func TestGraphIsTheDOTOfTheMachineInItsInitialState(t *testing.T) {
	// Each transition is labeled with its trigger, the event type without
	// its dev.getpumi. prefix, and the Cedar action that guards it, so that
	// a change to the workflow shows up as a readable diff of the graph.
	w, err := load(t, "valid")
	if err != nil {
		t.Fatal(err)
	}
	g := w.Graph()
	for _, want := range []string{
		"digraph {",
		`init -> planning;`,
		`planning -> review [label="result / advance"];`,
		`review -> planning [label="result / retry"];`,
		`"review-escalated" -> done [label="override.recorded / planReview"];`,
		`active -> cancelled [label="cancel.requested / cancel"`,
	} {
		if !strings.Contains(g, want) {
			t.Errorf("the graph has no %q:\n%s", want, g)
		}
	}
	if w.Graph() != g {
		t.Error("Graph is not the same each time")
	}
}
