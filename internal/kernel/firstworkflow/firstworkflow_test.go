// SPDX-License-Identifier: Apache-2.0
// SPDX-FileCopyrightText: The Pumi Authors

package firstworkflow

import (
	"context"
	"flag"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"regexp"
	"slices"
	"strings"
	"testing"

	"getpumi.dev/pumi/api/events"
	configv1 "getpumi.dev/pumi/api/gen/pumi/config/v1"
	workflowv1 "getpumi.dev/pumi/api/gen/pumi/workflow/v1"
	"getpumi.dev/pumi/api/policy"
	"getpumi.dev/pumi/internal/kernel/workflow"
)

// update rewrites testdata/workflow.dot from the workflow.
var update = flag.Bool("update", false, "rewrite testdata/workflow.dot from the workflow")

// load loads the embedded workflow.
func load(t *testing.T) *workflow.Workflow {
	t.Helper()
	w, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	return w
}

// stages are the states of decision 0171, in order, without the superstate
// and the escalated states.
var stages = []string{
	"spec", "planning", "second-opinion", "plan-review", "implementing", "archiving", "checking",
	"scope", "budgets", "dco", "outside-checks", "report-coverage", "conformance-audit",
	"archived-checking", "archived-scope", "archived-budgets", "archived-dco", "archived-outside-checks",
	"awaiting-approval", "ready-to-merge", "merging", "merged", "cancelled",
}

func TestTheLoaderReadsTheEmbeddedWorkflow(t *testing.T) {
	def := load(t).Definition()
	if def.GetName() != "spec-to-merge" || def.GetVersion() != "1" || def.GetInitial() != "spec" {
		t.Errorf("the workflow is %q version %q from %q, want spec-to-merge version 1 from spec", def.GetName(), def.GetVersion(), def.GetInitial())
	}
	names := []string{}
	for _, s := range def.GetStates() {
		names = append(names, s.GetName())
	}
	for _, want := range append([]string{"active"}, stages...) {
		if !slices.Contains(names, want) {
			t.Errorf("the workflow has no state %q: %q", want, names)
		}
	}
}

func TestEveryTransitionNamesACedarActionOfTheSchema(t *testing.T) {
	// Read the actions from pumi.cedarschema itself, so that the test does
	// not rely only on the list the loader checks against.
	declared := []string{}
	for _, m := range regexp.MustCompile(`(?m)^\s*action (\w+) appliesTo \{`).FindAllStringSubmatch(policy.Schema, -1) {
		declared = append(declared, m[1])
	}
	if len(declared) == 0 {
		t.Fatal("found no actions in pumi.cedarschema")
	}
	ts := load(t).Transitions()
	if len(ts) == 0 {
		t.Fatal("the workflow has no transitions")
	}
	for _, tr := range ts {
		if !slices.Contains(declared, tr.Action) {
			t.Errorf("state %q, %q to %q, is guarded by %q, which pumi.cedarschema does not declare", tr.State, tr.Trigger, tr.Target, tr.Action)
		}
	}
}

func TestEveryStateHasAComment(t *testing.T) {
	src, err := fs.ReadFile(files, File)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(string(src), "\n")
	blocks := 0
	for i, line := range lines {
		if line != "states {" {
			continue
		}
		blocks++
		if i == 0 || !strings.HasPrefix(lines[i-1], "#") {
			t.Errorf("the state at line %d has no comment on the line above it", i+1)
		}
	}
	if want := len(load(t).Definition().GetStates()); blocks != want {
		t.Errorf("found %d lines %q, want one for each of the %d states", blocks, "states {", want)
	}
}

func TestEveryActiveStateIsInsideTheSuperstate(t *testing.T) {
	for _, s := range load(t).Definition().GetStates() {
		_, final := s.GetEntry().(*workflowv1.State_Final)
		switch {
		case s.GetName() == "active":
			if s.GetParent() != "" {
				t.Errorf("the superstate has the parent %q", s.GetParent())
			}
		case final:
			if s.GetParent() != "" {
				t.Errorf("the final state %q is inside %q", s.GetName(), s.GetParent())
			}
		case s.GetParent() != "active":
			t.Errorf("state %q has the parent %q, want active", s.GetName(), s.GetParent())
		}
	}
}

func TestEveryGateHasAnEscalatedState(t *testing.T) {
	def := load(t).Definition()
	byName := map[string]*workflowv1.State{}
	for _, s := range def.GetStates() {
		byName[s.GetName()] = s
	}
	gates := 0
	for _, s := range def.GetStates() {
		if s.GetGate() == "" {
			continue
		}
		gates++
		esc, ok := byName[s.GetName()+"-escalated"]
		if !ok {
			t.Errorf("the gate state %q has no escalated state", s.GetName())
			continue
		}
		if _, wait := esc.GetEntry().(*workflowv1.State_Wait); !wait {
			t.Errorf("the escalated state %q does not wait", esc.GetName())
		}
		escalates := false
		for _, tr := range s.GetTransitions() {
			escalates = escalates || (tr.GetAction() == "escalate" && tr.GetTarget() == esc.GetName())
		}
		if !escalates {
			t.Errorf("the gate state %q has no escalate transition to %q", s.GetName(), esc.GetName())
		}
	}
	if gates != 14 {
		t.Errorf("the workflow has %d gate states, want 14", gates)
	}
}

// TestEveryTaskHasItsTextFile: the task of each state that starts an agent
// run or an external action has a text file, and a check's task has none,
// because its text is its task id (decision 0297).
func TestEveryTaskHasItsTextFile(t *testing.T) {
	w := load(t)
	tasks := []string{}
	for _, s := range w.Definition().GetStates() {
		task := s.GetInvoke().GetTask()
		if task == "" {
			continue
		}
		if s.GetInvoke().GetKind() == workflowv1.Invoke_KIND_CHECK {
			checkTaskIsItsID(t, w, s.GetName(), task)
			continue
		}
		tasks = append(tasks, task)
		if text, ok := w.Task(task); !ok || strings.TrimSpace(text) == "" {
			t.Errorf("the task %q of state %q has no text", task, s.GetName())
		}
	}
	texts, err := fs.Glob(files, "*.md")
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range texts {
		if !slices.Contains(tasks, strings.TrimSuffix(name, ".md")) {
			t.Errorf("the text file %s is embedded, but no state names its task", name)
		}
	}
	if len(texts) != 6 {
		t.Errorf("%d task texts are embedded, want 6: %q", len(texts), texts)
	}
}

// checkTaskIsItsID checks that the task of the check state is its task id
// (decision 0297).
func checkTaskIsItsID(t *testing.T, w *workflow.Workflow, state, task string) {
	t.Helper()
	if text, ok := w.Task(task); !ok || text != task {
		t.Errorf("the task of the check %q is %q, %v, want its task id %q", state, text, ok, task)
	}
}

// storage is the state of one plan, kept outside the machine.
type storage struct{ state string }

func (s *storage) load(context.Context) (string, error) { return s.state, nil }

func (s *storage) store(_ context.Context, state string) error {
	s.state = state
	return nil
}

// step is one trigger, and the transitions the fake decider allows while
// the trigger and every entered trigger after it are decided, as
// "state:action" of the state that declares the transition. escalate and
// cancel are always allowed, as the core policies allow them (decision
// 0177).
type step struct {
	trigger workflow.Trigger
	allow   []string
}

// Triggers of the steps.
var (
	result    = workflow.Result
	approval  = workflow.Event(events.TypeApprovalRecorded)
	override  = workflow.Event(events.TypeOverrideRecorded)
	resolved  = workflow.Event(events.TypeEscalationResolved)
	cancel    = workflow.Event(events.TypeCancelRequested)
	ingested  = workflow.Event(events.TypeCheckIngested)
	allPassed = []string{
		"spec:spec", "planning:advance", "plan-review:planReview", "implementing:advance",
	}
)

// walk runs the machine from the state from through steps, as the kernel
// does: for a trigger it decides the transitions in order with the fake
// decider and fires the first allowed one, and after each transition it
// decides the entered trigger of a state that only decides. It returns the
// states visited, from included.
func walk(t *testing.T, w *workflow.Workflow, archive configv1.Project_ArchiveStep, from string, steps []step) []string {
	t.Helper()
	known := map[string]bool{}
	for _, tr := range w.Transitions() {
		known[tr.State+":"+tr.Action] = true
	}
	s := &storage{state: from}
	m := w.NewMachine(archive, s.load, s.store)
	visited := []string{from}
	for i, st := range steps {
		for _, a := range st.allow {
			if !known[a] {
				t.Fatalf("step %d allows %q, which is no transition of the workflow", i+1, a)
			}
		}
		allowed := func(tr workflow.Transition) bool {
			return slices.Contains(policy.AlwaysAllowed(), tr.Action) || slices.Contains(st.allow, tr.State+":"+tr.Action)
		}
		if !fire(t, m, st.trigger, allowed) {
			t.Fatalf("step %d: the state %q has no transition on %q; visited %q", i+1, s.state, st.trigger, visited)
		}
		visited = append(visited, s.state)
		for range 100 {
			if !fire(t, m, workflow.Entered, allowed) {
				break
			}
			visited = append(visited, s.state)
		}
	}
	return visited
}

// fire fires the first allowed transition on trigger, and reports whether
// the state had any transition on it.
func fire(t *testing.T, m *workflow.Machine, trigger workflow.Trigger, allowed func(workflow.Transition) bool) bool {
	t.Helper()
	ctx := context.Background()
	ts, err := m.Transitions(ctx, trigger)
	if err != nil {
		t.Fatal(err)
	}
	if len(ts) == 0 {
		return false
	}
	for _, tr := range ts {
		if allowed(tr) {
			if err := m.Fire(ctx, tr.Index); err != nil {
				t.Fatal(err)
			}
			return true
		}
	}
	t.Fatalf("no transition on %q is allowed, but the last one is always allowed: %v", trigger, ts)
	return false
}

func TestWalks(t *testing.T) {
	// The cases of issue #66. Each names the transitions the fake decider
	// allows at each step and the states the plan visits.
	tests := []struct {
		name    string
		archive configv1.Project_ArchiveStep
		from    string
		steps   []step
		want    []string
	}{
		{
			name:    "every gate passes, archiving after implementing",
			archive: configv1.Project_ARCHIVE_STEP_AFTER_IMPLEMENTING,
			steps: []step{
				{result, []string{"spec:spec"}},
				{result, []string{"planning:advance"}},
				{result, []string{"plan-review:planReview"}},
				{result, []string{"implementing:advance"}},
				{result, []string{"archiving:advance"}},
				{result, []string{"checking:advance", "scope:scope", "budgets:budgets", "dco:dco", "outside-checks:outsideChecks"}},
				{result, []string{"report-coverage:advance"}},
				{result, []string{"conformance-audit:conformance"}},
				{approval, []string{"awaiting-approval:mergeApproval", "ready-to-merge:merge"}},
				{result, []string{"merging:advance"}},
			},
			want: []string{
				"spec", "planning", "plan-review", "implementing", "archiving", "checking",
				"scope", "budgets", "dco", "outside-checks", "report-coverage", "conformance-audit",
				"awaiting-approval", "ready-to-merge", "merging", "merged",
			},
		},
		{
			name:    "every gate passes, archiving after the conformance audit, and the checks run again",
			archive: configv1.Project_ARCHIVE_STEP_AFTER_CONFORMANCE,
			from:    "implementing",
			steps: []step{
				{result, []string{"implementing:advance"}},
				{result, []string{"checking:advance", "scope:scope", "budgets:budgets", "dco:dco", "outside-checks:outsideChecks"}},
				{result, []string{"report-coverage:advance"}},
				{result, []string{"conformance-audit:conformance"}},
				{result, []string{"archiving:advance"}},
				{result, []string{"archived-checking:advance", "archived-scope:scope", "archived-budgets:budgets", "archived-dco:dco", "archived-outside-checks:outsideChecks"}},
				{approval, []string{"awaiting-approval:mergeApproval", "ready-to-merge:merge"}},
			},
			want: []string{
				"implementing", "checking", "scope", "budgets", "dco", "outside-checks", "report-coverage",
				"conformance-audit", "archiving", "archived-checking", "archived-scope", "archived-budgets",
				"archived-dco", "archived-outside-checks", "awaiting-approval", "ready-to-merge", "merging",
			},
		},
		{
			name:    "every gate passes with no archive step",
			archive: configv1.Project_ARCHIVE_STEP_OFF,
			from:    "implementing",
			steps: []step{
				{result, []string{"implementing:advance"}},
				{result, []string{"checking:advance", "scope:scope", "budgets:budgets", "dco:dco", "outside-checks:outsideChecks"}},
				{result, []string{"report-coverage:advance"}},
				{result, []string{"conformance-audit:conformance"}},
			},
			want: []string{
				"implementing", "checking", "scope", "budgets", "dco", "outside-checks", "report-coverage",
				"conformance-audit", "awaiting-approval",
			},
		},
		{
			name:    "a check that fails on the archived tree goes back to implementing",
			archive: configv1.Project_ARCHIVE_STEP_AFTER_CONFORMANCE,
			from:    "archived-checking",
			steps: []step{
				{result, []string{"archived-checking:advance", "archived-scope:retry"}},
				{result, []string{"implementing:advance"}},
			},
			want: []string{"archived-checking", "archived-scope", "implementing", "checking"},
		},
		{
			name:    "an override of the audit goes to the archive step after the conformance audit",
			archive: configv1.Project_ARCHIVE_STEP_AFTER_CONFORMANCE,
			from:    "conformance-audit",
			steps: []step{
				{result, nil},
				{override, []string{"conformance-audit-escalated:conformance"}},
			},
			want: []string{"conformance-audit", "conformance-audit-escalated", "archiving"},
		},
		{
			name:    "an archive step that fails escalates, and the answer runs it again",
			archive: configv1.Project_ARCHIVE_STEP_AFTER_IMPLEMENTING,
			from:    "archiving",
			steps: []step{
				{workflow.Failed, nil},
				{resolved, []string{"archiving-escalated:resume"}},
				{result, []string{"archiving:advance"}},
			},
			want: []string{"archiving", "archiving-escalated", "archiving", "checking"},
		},
		{
			name: "plan review retries to planning",
			steps: []step{
				{result, []string{"spec:spec"}},
				{result, []string{"planning:advance"}},
				{result, []string{"plan-review:retry"}},
				{result, []string{"planning:advance"}},
				{result, []string{"plan-review:planReview"}},
			},
			want: []string{"spec", "planning", "plan-review", "planning", "plan-review", "implementing"},
		},
		{
			name: "an escalation and an override that goes on",
			from: "checking",
			steps: []step{
				{result, []string{"checking:advance"}},
				{override, []string{"scope-escalated:scope", "budgets:budgets", "dco:dco", "outside-checks:outsideChecks"}},
			},
			want: []string{"checking", "scope", "scope-escalated", "budgets", "dco", "outside-checks", "report-coverage"},
		},
		{
			name: "an override that the gate still denies stays escalated",
			from: "plan-review",
			steps: []step{
				{result, nil},
				{override, nil},
			},
			want: []string{"plan-review", "plan-review-escalated", "plan-review-escalated"},
		},
		{
			name: "an escalation resolved back to work",
			from: "plan-review",
			steps: []step{
				{result, nil},
				{resolved, []string{"plan-review-escalated:resume"}},
				{result, []string{"planning:advance"}},
			},
			want: []string{"plan-review", "plan-review-escalated", "planning", "plan-review"},
		},
		{
			name: "the spec escalates, and the person's fix is checked again",
			steps: []step{
				{result, nil},
				{resolved, []string{"spec-escalated:resume"}},
				{result, []string{"spec:spec"}},
			},
			want: []string{"spec", "spec-escalated", "spec", "planning"},
		},
		{
			name: "a second opinion that agrees",
			from: "planning",
			steps: []step{
				{result, []string{"planning:ask"}},
				{result, []string{"second-opinion:ambiguity"}},
				{result, []string{"planning:advance"}},
			},
			want: []string{"planning", "second-opinion", "planning", "plan-review"},
		},
		{
			name: "a second opinion that escalates",
			from: "planning",
			steps: []step{
				{result, []string{"planning:ask"}},
				{result, nil},
				{resolved, []string{"second-opinion-escalated:resume"}},
			},
			want: []string{"planning", "second-opinion", "second-opinion-escalated", "planning"},
		},
		{
			name: "a rejected approval",
			from: "conformance-audit",
			steps: []step{
				{result, []string{"conformance-audit:conformance"}},
				{approval, []string{"awaiting-approval:resume"}},
			},
			want: []string{"conformance-audit", "awaiting-approval", "implementing"},
		},
		{
			name: "a merge denied at ready to merge",
			from: "awaiting-approval",
			steps: []step{
				{approval, []string{"awaiting-approval:mergeApproval", "ready-to-merge:retry"}},
			},
			want: []string{"awaiting-approval", "ready-to-merge", "implementing"},
		},
		{
			name: "a merge denied with no attempts left escalates",
			from: "awaiting-approval",
			steps: []step{
				{approval, []string{"awaiting-approval:mergeApproval"}},
				{resolved, []string{"ready-to-merge-escalated:resume"}},
			},
			want: []string{"awaiting-approval", "ready-to-merge", "ready-to-merge-escalated", "implementing"},
		},
		{
			name: "a failed merge is decided again after the person answers",
			from: "merging",
			steps: []step{
				{result, nil},
				{resolved, []string{"merging-escalated:resume", "ready-to-merge:merge"}},
				{result, []string{"merging:advance"}},
			},
			want: []string{"merging", "merging-escalated", "ready-to-merge", "merging", "merged"},
		},
		{
			name: "pending outside checks escalate and go on when they pass",
			from: "dco",
			steps: []step{
				{workflow.Entered, []string{"dco:dco"}},
				{ingested, nil},
				{ingested, []string{"outside-checks-escalated:outsideChecks"}},
			},
			want: []string{"dco", "outside-checks", "outside-checks-escalated", "outside-checks-escalated", "report-coverage"},
		},
		{
			name: "the hard caps stop a plan between stages",
			from: "implementing",
			steps: []step{
				{result, nil},
				{resolved, []string{"implementing-escalated:resume"}},
			},
			want: []string{"implementing", "implementing-escalated", "implementing"},
		},
		{
			name: "a cancel in the middle of a run",
			steps: []step{
				{result, allPassed},
				{result, allPassed},
				{cancel, nil},
			},
			want: []string{"spec", "planning", "plan-review", "cancelled"},
		},
	}
	w := load(t)
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			from := tt.from
			if from == "" {
				from = "spec"
			}
			if got := walk(t, w, tt.archive, from, tt.steps); !slices.Equal(got, tt.want) {
				t.Errorf("visited %q, want %q", got, tt.want)
			}
		})
	}
}

func TestACancelGoesToCancelledFromEveryActiveState(t *testing.T) {
	w := load(t)
	n := 0
	for _, s := range w.Definition().GetStates() {
		if s.GetParent() != "active" {
			continue
		}
		n++
		if got := walk(t, w, configv1.Project_ARCHIVE_STEP_AFTER_IMPLEMENTING, s.GetName(), []step{{cancel, nil}}); !slices.Equal(got, []string{s.GetName(), "cancelled"}) {
			t.Errorf("a cancel in %q visited %q, want it to go to cancelled", s.GetName(), got)
		}
	}
	if n < 14 {
		t.Errorf("only %d states are inside active", n)
	}
	for _, final := range []string{"merged", "cancelled"} {
		ts, err := w.NewMachine(configv1.Project_ARCHIVE_STEP_AFTER_IMPLEMENTING, (&storage{state: final}).load, (&storage{}).store).Transitions(context.Background(), cancel)
		if err != nil || len(ts) != 0 {
			t.Errorf("a cancel in %q: %v, %v, want no transition", final, ts, err)
		}
	}
}

// TestAFailedInvocationRunsAgainThenEscalates: every state that starts an
// invocation goes back to itself on failed, guarded by retry, and to its
// escalated state when retry is denied (decision 0235).
func TestAFailedInvocationRunsAgainThenEscalates(t *testing.T) {
	w := load(t)
	n := 0
	for _, s := range w.Definition().GetStates() {
		if s.GetInvoke() == nil {
			continue
		}
		n++
		name := s.GetName()
		ts, err := w.NewMachine(configv1.Project_ARCHIVE_STEP_AFTER_IMPLEMENTING, (&storage{state: name}).load, (&storage{}).store).Transitions(context.Background(), workflow.Failed)
		if err != nil {
			t.Fatal(err)
		}
		got := []string{}
		for _, tr := range ts {
			got = append(got, tr.Action+">"+tr.Target)
		}
		if want := []string{"retry>" + name, "escalate>" + name + "-escalated"}; !slices.Equal(got, want) {
			t.Errorf("the transitions of %q on failed are %q, want %q", name, got, want)
		}
	}
	if n != 11 {
		t.Errorf("%d states start an invocation, want 11", n)
	}
}

// TestOnlyMergedHasMerged: a plan that depends on another starts only when
// that plan is in a final state that has merged (decision 0197), which in
// the first workflow is merged and not cancelled.
func TestOnlyMergedHasMerged(t *testing.T) {
	merged := map[string]bool{}
	for _, s := range load(t).Definition().GetStates() {
		if f := s.GetFinal(); f != nil {
			merged[s.GetName()] = f.GetMerged()
		}
	}
	if want := map[string]bool{"merged": true, "cancelled": false}; !maps.Equal(merged, want) {
		t.Errorf("the final states and their merged flags are %v, want %v", merged, want)
	}
}

// graphHeader is the SPDX header of testdata/workflow.dot (decision 0068).
const graphHeader = "// SPDX-License-Identifier: Apache-2.0\n// SPDX-FileCopyrightText: The Pumi Authors\n\n" +
	"// The state machine of the first workflow, written by the test\n" +
	"// TestTheGraphIsCurrent with -update, so that a change to the workflow\n" +
	"// shows up as a diff of this graph.\n"

func TestTheGraphIsCurrent(t *testing.T) {
	got := graphHeader + load(t).Graph()
	const name = "testdata/workflow.dot"
	if *update {
		if err := os.WriteFile(name, []byte(got), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(name)
	if err != nil {
		t.Fatalf("%v; run go test ./internal/kernel/firstworkflow -run TestTheGraphIsCurrent -update", err)
	}
	if got != string(want) {
		t.Errorf("%s is not the graph of the workflow; run go test ./internal/kernel/firstworkflow -run TestTheGraphIsCurrent -update and review the diff", name)
	}
}

// TestEachAgentStateHasAModelRoleOfItsOwn: the five states that start a
// harness invocation each name their own model role (decision 0256).
func TestEachAgentStateHasAModelRoleOfItsOwn(t *testing.T) {
	want := map[string]string{
		"planning": "planner", "second-opinion": "second-opinion", "plan-review": "plan-reviewer",
		"implementing": "implementer", "conformance-audit": "auditor",
	}
	got := map[string]string{}
	for _, s := range load(t).Definition().GetStates() {
		if r := s.GetInvoke().GetModelRole(); r != "" {
			got[s.GetName()] = r
		}
	}
	if !maps.Equal(got, want) {
		t.Errorf("the states' model roles are %v, want %v", got, want)
	}
}

// TestTheChecksNameTheirComponents: the spec state runs the spec check
// program, whose output holds spec.valid and the narratives, and the
// checking state runs the program of the checks (decisions 0272 and 0279).
func TestTheChecksNameTheirComponents(t *testing.T) {
	w := load(t)
	if got := w.Checks(); !slices.Equal(got, []string{"checks", "spec"}) {
		t.Errorf("Checks() = %q, want [checks spec]", got)
	}
	for _, s := range w.Definition().GetStates() {
		if s.GetName() != "spec" {
			continue
		}
		inv := s.GetInvoke()
		if inv.GetComponent() != "spec" || inv.GetOutputMessage() != "pumi.workflow.v1.SpecCheckOutput" {
			t.Errorf("the spec state runs %q with the output %q, want spec with pumi.workflow.v1.SpecCheckOutput", inv.GetComponent(), inv.GetOutputMessage())
		}
	}
}

// TestTheArchiveAndCoverageStatesRunTheSpecCheckProgram: the archive step
// and the coverage report run the spec check's program with their own
// tasks (decisions 0278, 0284 and 0294); the archive step's result carries
// the workspace's tree and no output (decision 0282), and the coverage
// report's output is a CoverageReport. The checks run again on the
// archived tree with the checks program.
func TestTheArchiveAndCoverageStatesRunTheSpecCheckProgram(t *testing.T) {
	want := map[string]string{
		"spec":              "spec validate-spec pumi.workflow.v1.SpecCheckOutput false",
		"archiving":         "spec archive-change  true",
		"checking":          "checks run-checks pumi.workflow.v1.CheckingOutput false",
		"report-coverage":   "spec report-coverage pumi.workflow.v1.CoverageReport false",
		"archived-checking": "checks run-checks pumi.workflow.v1.CheckingOutput false",
	}
	got := map[string]string{}
	for _, s := range load(t).Definition().GetStates() {
		if inv := s.GetInvoke(); inv.GetKind() == workflowv1.Invoke_KIND_CHECK {
			got[s.GetName()] = fmt.Sprintf("%s %s %s %v", inv.GetComponent(), inv.GetTask(), inv.GetOutputMessage(), inv.GetReturnsTree())
		}
	}
	if !maps.Equal(got, want) {
		t.Errorf("the checks are %q, want %q", got, want)
	}
}

// TestTheNarrativeIsTheFinalAuthorityInTheTasksThatGetIt: the task text of
// each state that gets the narratives says that the narrative is the final
// authority when the spec can be read more than one way (decision 0271),
// and the audit's says to judge the coverage report (decision 0280).
func TestTheNarrativeIsTheFinalAuthorityInTheTasksThatGetIt(t *testing.T) {
	w := load(t)
	n := 0
	for _, s := range w.Definition().GetStates() {
		inv := s.GetInvoke()
		if !inv.GetNarratives() {
			continue
		}
		n++
		text, _ := w.Task(inv.GetTask())
		if !strings.Contains(text, "the narrative is the final authority") {
			t.Errorf("the task %q of %q does not say that the narrative is the final authority:\n%s", inv.GetTask(), s.GetName(), text)
		}
		if inv.GetCoverage() && !strings.Contains(text, "coverage report") {
			t.Errorf("the task %q of %q gets the coverage report and does not say what to do with it:\n%s", inv.GetTask(), s.GetName(), text)
		}
	}
	if n != 3 {
		t.Errorf("%d states get the narratives, want 3", n)
	}
}
