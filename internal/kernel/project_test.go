// SPDX-License-Identifier: Apache-2.0
// SPDX-FileCopyrightText: The Pumi Authors

package kernel

import (
	"context"
	"errors"
	"io/fs"
	"maps"
	"slices"
	"strings"
	"testing"

	"google.golang.org/protobuf/proto"

	"getpumi.dev/pumi/api/events"
	cloudeventsv1 "getpumi.dev/pumi/api/gen/io/cloudevents/v1"
	"getpumi.dev/pumi/api/gen/pumi/controlplane/v1/controlplanev1connect"
	eventsv1 "getpumi.dev/pumi/api/gen/pumi/events/v1"
	harnessv1 "getpumi.dev/pumi/api/gen/pumi/harness/v1"
	runnerv1 "getpumi.dev/pumi/api/gen/pumi/runner/v1"
	workflowv1 "getpumi.dev/pumi/api/gen/pumi/workflow/v1"
	"getpumi.dev/pumi/internal/kernel/firstworkflow"
	"getpumi.dev/pumi/internal/kernel/workflow"
)

// inputTree is the input tree of the plans of these tests.
const inputTree = "tree-1"

// treeFiles is a fake input tree: the paths of its files.
type treeFiles map[string]bool

func (t treeFiles) has(_ context.Context, tree, path string) (bool, error) {
	if tree != inputTree {
		return false, errors.New("the kernel asked about the tree " + tree + ", not the plan's input tree")
	}
	return t[path], nil
}

// testProject is a project with every list of its section.
var testProject = Project{
	Commands:       []string{"make test", "GOWORK=off make lint"},
	Conventions:    []string{"AGENTS.md", "docs/style.md"},
	ProtectedPaths: []string{"docs/constitution.md", "openspec/specs/**"},
}

// firstWorkflowKernel returns a kernel of the first workflow with the
// project p, whose input tree holds files, and one plan, p1, added and
// started.
func firstWorkflowKernel(t *testing.T, p Project, files treeFiles) (*kernel, *planRun) {
	t.Helper()
	wf, err := firstworkflow.Load()
	if err != nil {
		t.Fatal(err)
	}
	k, err := newKernel(Config{
		Client: fakeClient{}, Workflow: wf, Project: p, HasFile: files.has,
		Base: &runnerv1.Invocation{
			Component: &harnessv1.Component{Name: "scripted", Version: "1"},
			Input:     &harnessv1.Input{Repo: "case", Tree: inputTree},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	follow(t, k, &eventsv1.PlanAdded{Plan: "p1", Track: "a"}, "add-p1")
	r := k.plans["p1"]
	r.started = true
	return k, r
}

// follow lets the kernel follow an event of a person with data.
func follow(t *testing.T, k *kernel, data proto.Message, id string) {
	t.Helper()
	e, err := events.New(data, events.Attributes{ID: id, Source: "/person", Subject: "p1", Tenant: "kernel"})
	if err != nil {
		t.Fatal(err)
	}
	if err := k.follow(e); err != nil {
		t.Fatal(err)
	}
}

// fakeClient is a client of the control plane that the tests never call.
type fakeClient struct {
	controlplanev1connect.ControlPlaneServiceClient
}

// agentStates returns the states of the first workflow that start an
// agent run, with their invoke entries.
func agentStates(t *testing.T, wf *workflow.Workflow) map[string]*workflowv1.Invoke {
	t.Helper()
	states := map[string]*workflowv1.Invoke{}
	for _, s := range wf.Definition().GetStates() {
		if inv, ok := s.GetEntry().(*workflowv1.State_Invoke); ok && inv.Invoke.GetKind() == workflowv1.Invoke_KIND_HARNESS {
			states[s.GetName()] = inv.Invoke
		}
	}
	return states
}

// taskOf returns the task of the invocation document of req.
func taskOf(t *testing.T, req *cloudeventsv1.CloudEvent) string {
	t.Helper()
	var data eventsv1.InvocationRequested
	if err := unpack(req, &data); err != nil {
		t.Fatal(err)
	}
	doc, err := events.Document(&data)
	if err != nil {
		t.Fatal(err)
	}
	return doc.GetInput().GetTask()
}

// taskText returns the text of the first workflow's task name.
func taskText(t *testing.T, wf *workflow.Workflow, name string) string {
	t.Helper()
	text, ok := wf.Task(name)
	if !ok {
		t.Fatalf("the first workflow has no task %q", name)
	}
	return text
}

// TestEveryAgentTaskOfTheFirstWorkflowHasTheProjectSection: the task of
// each state of the first workflow that starts an agent run is the
// workflow's own task text, unchanged, followed by the project section,
// with the plan's declared paths once its planning output gives them
// (decision 0250). Checks and external actions get no section.
func TestEveryAgentTaskOfTheFirstWorkflowHasTheProjectSection(t *testing.T) {
	files := treeFiles{"AGENTS.md": true, "docs/style.md": true}
	k, r := firstWorkflowKernel(t, testProject, files)
	states := agentStates(t, k.cfg.Workflow)
	want := []string{"conformance-audit", "implementing", "plan-review", "planning", "second-opinion"}
	if got := slices.Sorted(maps.Keys(states)); !slices.Equal(got, want) {
		t.Fatalf("the first workflow's agent states are %v, want %v", got, want)
	}
	before := strings.Join([]string{
		"## The project",
		"",
		"The commands that run the project's checks:",
		"",
		"- `make test`",
		"- `GOWORK=off make lint`",
		"",
		"The files that hold the project's conventions:",
		"",
		"- `AGENTS.md`",
		"- `docs/style.md`",
		"",
		"The protected paths, which no change may touch:",
		"",
		"- `docs/constitution.md`",
		"- `openspec/specs/**`",
		"",
		"The plan's declared paths, the only paths its change may touch:",
		"",
		"- none yet: the plan has not declared its paths",
		"",
	}, "\n")
	checkAgentTasks(t, r, states, before, "before planning")
	planned(t, k, r, `{"plan":"p","declaredPaths":["internal/a/**","cmd/a/main.go"]}`)
	after := strings.Replace(before, "- none yet: the plan has not declared its paths",
		"- `internal/a/**`\n- `cmd/a/main.go`", 1)
	checkAgentTasks(t, r, states, after, "after planning")
	checkOtherTasks(t, r)
}

// checkAgentTasks checks that the task of each of the agent states is its
// task text followed by section, and then by the narratives and the
// coverage report for a state that asks for them, which other tests check.
func checkAgentTasks(t *testing.T, r *planRun, states map[string]*workflowv1.Invoke, section, when string) {
	t.Helper()
	for name, inv := range states {
		req, err := r.request(t.Context(), name, inv, "cause")
		if err != nil {
			t.Fatal(err)
		}
		text := taskText(t, r.k.cfg.Workflow, inv.GetTask())
		got := taskOf(t, req)
		for _, heading := range []string{"\n## The narratives\n", "\n## The coverage report\n"} {
			if i := strings.Index(got, heading); i >= 0 {
				got = got[:i]
			}
		}
		if got != text+"\n"+section {
			t.Errorf("the task of %s %s =\n%s\nwant the task text and then\n%s", name, when, got, section)
		}
	}
}

// checkOtherTasks checks that the task of each state that invokes a check
// or an external action is its task text alone.
func checkOtherTasks(t *testing.T, r *planRun) {
	t.Helper()
	for _, s := range r.k.cfg.Workflow.Definition().GetStates() {
		inv, ok := s.GetEntry().(*workflowv1.State_Invoke)
		if !ok || inv.Invoke.GetKind() == workflowv1.Invoke_KIND_HARNESS {
			continue
		}
		req, err := r.request(t.Context(), s.GetName(), inv.Invoke, "cause")
		if err != nil {
			t.Fatal(err)
		}
		if got, text := taskOf(t, req), taskText(t, r.k.cfg.Workflow, inv.Invoke.GetTask()); got != text {
			t.Errorf("the task of %s, which starts no agent run, =\n%s\nwant only its task text", s.GetName(), got)
		}
	}
}

// planned lets the kernel follow the invocation.requested and the
// invocation.result of a planning invocation of r with the output out.
func planned(t *testing.T, k *kernel, r *planRun, out string) {
	t.Helper()
	inv := k.states["planning"].GetInvoke()
	req, err := r.request(t.Context(), "planning", inv, "cause")
	if err != nil {
		t.Fatal(err)
	}
	if err := k.follow(req); err != nil {
		t.Fatal(err)
	}
	res, err := events.New(&eventsv1.InvocationResult{Invocation: r.invocation, Output: out},
		events.Attributes{ID: "result-" + r.invocation, Source: "/runner", Subject: r.invocation, Tenant: "kernel"})
	if err != nil {
		t.Fatal(err)
	}
	if err := k.follow(res); err != nil {
		t.Fatal(err)
	}
}

// TestALaterPlanningOutputReplacesTheDeclaredPaths: when planning runs
// again, the section names the declared paths of its last output, also an
// output that declares none.
func TestALaterPlanningOutputReplacesTheDeclaredPaths(t *testing.T) {
	k, r := firstWorkflowKernel(t, Project{}, treeFiles{})
	planned(t, k, r, `{"plan":"p","declaredPaths":["internal/a/**"]}`)
	planned(t, k, r, `{"plan":"p"}`)
	req, err := r.request(t.Context(), "implementing", k.states["implementing"].GetInvoke(), "cause")
	if err != nil {
		t.Fatal(err)
	}
	if got := taskOf(t, req); !strings.HasSuffix(got, "The plan's declared paths, the only paths its change may touch:\n\n- none\n") {
		t.Errorf("the task after a planning output with no declared paths =\n%s\nwant the declared paths none", got)
	}
}

// TestAnEmptyProjectSectionSaysEachListIsEmpty: with no commands,
// convention files or protected paths, the section is still there and
// says that each list is empty, so the agent knows the project gives none.
func TestAnEmptyProjectSectionSaysEachListIsEmpty(t *testing.T) {
	k, r := firstWorkflowKernel(t, Project{}, treeFiles{})
	inv := k.states["planning"].GetInvoke()
	req, err := r.request(t.Context(), "planning", inv, "cause")
	if err != nil {
		t.Fatal(err)
	}
	want := taskText(t, k.cfg.Workflow, "plan") + "\n" + strings.Join([]string{
		"## The project",
		"",
		"The commands that run the project's checks:",
		"",
		"- none",
		"",
		"The files that hold the project's conventions:",
		"",
		"- none",
		"",
		"The protected paths, which no change may touch:",
		"",
		"- none",
		"",
		"The plan's declared paths, the only paths its change may touch:",
		"",
		"- none yet: the plan has not declared its paths",
		"",
	}, "\n")
	if got := taskOf(t, req); got != want {
		t.Errorf("the task with an empty project section =\n%s\nwant\n%s", got, want)
	}
}

// TestAMissingConventionFileIsNamedAsMissing: a convention file that the
// plan's input tree does not hold stays in the section, marked as missing.
func TestAMissingConventionFileIsNamedAsMissing(t *testing.T) {
	k, r := firstWorkflowKernel(t, Project{Conventions: []string{"AGENTS.md", "CONVENTIONS.md"}}, treeFiles{"AGENTS.md": true})
	req, err := r.request(t.Context(), "implementing", k.states["implementing"].GetInvoke(), "cause")
	if err != nil {
		t.Fatal(err)
	}
	want := "The files that hold the project's conventions:\n\n- `AGENTS.md`\n- `CONVENTIONS.md`: missing, the input tree has no such file\n"
	if got := taskOf(t, req); !strings.Contains(got, want) {
		t.Errorf("the task with a missing convention file =\n%s\nwant it to hold\n%s", got, want)
	}
}

// TestTheKernelNeedsAWayToReadTheTreeForConventionFiles: a project with
// convention files and no HasFile is refused, because the kernel could not
// say which of them are missing.
func TestTheKernelNeedsAWayToReadTheTreeForConventionFiles(t *testing.T) {
	wf, err := firstworkflow.Load()
	if err != nil {
		t.Fatal(err)
	}
	_, err = newKernel(Config{Client: fakeClient{}, Workflow: wf, Project: Project{Conventions: []string{"AGENTS.md"}}})
	if err == nil || !strings.Contains(err.Error(), "HasFile") {
		t.Errorf("newKernel with convention files and no HasFile = %v, want an error that names HasFile", err)
	}
}

// TestAnErrorReadingTheTreeFailsTheRequest: when HasFile fails, the kernel
// does not guess whether the file is there.
func TestAnErrorReadingTheTreeFailsTheRequest(t *testing.T) {
	k, r := firstWorkflowKernel(t, Project{Conventions: []string{"AGENTS.md"}}, treeFiles{})
	k.cfg.HasFile = func(context.Context, string, string) (bool, error) { return false, fs.ErrPermission }
	if _, err := r.request(t.Context(), "planning", k.states["planning"].GetInvoke(), "cause"); !errors.Is(err, fs.ErrPermission) {
		t.Errorf("request when HasFile fails = %v, want its error", err)
	}
}

// result lets the kernel follow the invocation.requested of the state of
// r and the invocation.result of that invocation with the output out.
func result(t *testing.T, k *kernel, r *planRun, state, out string) {
	t.Helper()
	req, err := r.request(t.Context(), state, k.states[state].GetInvoke(), "cause")
	if err != nil {
		t.Fatal(err)
	}
	if err := k.follow(req); err != nil {
		t.Fatal(err)
	}
	res, err := events.New(&eventsv1.InvocationResult{Invocation: r.invocation, Output: out},
		events.Attributes{ID: "result-" + r.invocation, Source: "/runner", Subject: r.invocation, Tenant: "kernel"})
	if err != nil {
		t.Fatal(err)
	}
	if err := k.follow(res); err != nil {
		t.Fatal(err)
	}
}

// taskOfState returns the task of the next invocation of state.
func taskOfState(t *testing.T, k *kernel, r *planRun, state string) string {
	t.Helper()
	req, err := r.request(t.Context(), state, k.states[state].GetInvoke(), "cause")
	if err != nil {
		t.Fatal(err)
	}
	return taskOf(t, req)
}

// TestTheNarrativesReachTheSecondOpinionPlanReviewAndTheAudit: the kernel
// keeps the narratives of the spec check's last result and adds them to
// the task of each state that asks for them, which in the first workflow
// are the second opinion, plan review and the conformance audit, after the
// project section; no other task gets them (decisions 0271, 0279 and
// 0298).
func TestTheNarrativesReachTheSecondOpinionPlanReviewAndTheAudit(t *testing.T) {
	k, r := firstWorkflowKernel(t, Project{}, treeFiles{})
	var asking []string
	for name, inv := range agentStates(t, k.cfg.Workflow) {
		if inv.GetNarratives() {
			asking = append(asking, name)
		}
	}
	slices.Sort(asking)
	if want := []string{"conformance-audit", "plan-review", "second-opinion"}; !slices.Equal(asking, want) {
		t.Fatalf("the states that ask for the narratives are %v, want %v", asking, want)
	}
	before := "\n## The narratives\n\nThe narrative is the final authority when the spec can be read more than one way.\n\n- none yet: the spec check has not given them\n"
	for _, state := range asking {
		if got := taskOfState(t, k, r, state); !strings.Contains(got, before) {
			t.Errorf("the task of %s before the spec check =\n%s\nwant it to hold\n%s", state, got, before)
		}
	}
	result(t, k, r, "spec", `{"specValid":{"value":true},"changeNarrative":"Plans carry\ntheir narrative.","capabilityNarratives":{"workflow":"The workflow runs plans.","kernel":"The kernel moves plans."}}`)
	after := strings.Join([]string{
		"",
		"## The narratives",
		"",
		"The narrative is the final authority when the spec can be read more than one way.",
		"",
		"The narrative of the plan's change:",
		"",
		"> Plans carry",
		"> their narrative.",
		"",
		"The narrative of the capability `kernel`:",
		"",
		"> The kernel moves plans.",
		"",
		"The narrative of the capability `workflow`:",
		"",
		"> The workflow runs plans.",
		"",
	}, "\n")
	for _, state := range asking {
		got := taskOfState(t, k, r, state)
		if !strings.Contains(got, after) || strings.Index(got, "## The project\n") > strings.Index(got, after) {
			t.Errorf("the task of %s after the spec check =\n%s\nwant the project section and then\n%s", state, got, after)
		}
	}
	for _, state := range []string{"planning", "implementing"} {
		if got := taskOfState(t, k, r, state); strings.Contains(got, "## The narratives") {
			t.Errorf("the task of %s holds the narratives:\n%s", state, got)
		}
	}
}

// TestALaterSpecCheckReplacesTheNarratives: a person's change to a
// narrative starts a new spec check, whose narratives replace the old ones
// (decision 0279).
func TestALaterSpecCheckReplacesTheNarratives(t *testing.T) {
	k, r := firstWorkflowKernel(t, Project{}, treeFiles{})
	result(t, k, r, "spec", `{"specValid":{"value":true},"changeNarrative":"Old.","capabilityNarratives":{"a":"Old a."}}`)
	result(t, k, r, "spec", `{"specValid":{"value":true},"changeNarrative":"New."}`)
	got := taskOfState(t, k, r, "plan-review")
	if !strings.HasSuffix(got, "The narrative of the plan's change:\n\n> New.\n") || strings.Contains(got, "> Old") {
		t.Errorf("the task after a second spec check =\n%s\nwant only the new narrative", got)
	}
}

// TestTheCoverageReportReachesTheAuditWithThePlansReasons: the kernel keeps
// the last coverage report and adds it to the task of each state that asks
// for it, which in the first workflow is the conformance audit, with the
// reason that the last planning output gives for each requirement it gives
// no test (decisions 0280, 0294, 0295 and 0298).
func TestTheCoverageReportReachesTheAuditWithThePlansReasons(t *testing.T) {
	k, r := firstWorkflowKernel(t, Project{}, treeFiles{})
	var asking []string
	for name, inv := range agentStates(t, k.cfg.Workflow) {
		if inv.GetCoverage() {
			asking = append(asking, name)
		}
	}
	if want := []string{"conformance-audit"}; !slices.Equal(asking, want) {
		t.Fatalf("the states that ask for the coverage report are %v, want %v", asking, want)
	}
	before := "\n## The coverage report\n\n- none yet: the coverage report has not run\n"
	if got := taskOfState(t, k, r, "conformance-audit"); !strings.HasSuffix(got, before) {
		t.Errorf("the audit's task before the report =\n%s\nwant it to end with\n%s", got, before)
	}
	planned(t, k, r, `{"plan":"p","noTest":[{"requirementId":"WFL-002","reason":"The golden cases\nshow it."},{"requirementId":"WFL-003","reason":"Shown by hand."},{"requirementId":"WFL-009","reason":"Not in the change."}]}`)
	result(t, k, r, "report-coverage", `{"requirements":[{"id":"WFL-001","testFiles":["a_test.go","b_test.go"]},{"id":"WFL-002"},{"id":"WFL-003","testFiles":["c_test.go"]},{"id":"WFL-004"}]}`)
	want := strings.Join([]string{
		"",
		"## The coverage report",
		"",
		"Each requirement that the change adds or modifies, with the test files whose `Verifies:` line names its ID and the plan's reason for having no test. Judge each requirement with neither, and each reason:",
		"",
		"- `WFL-001`: tested by `a_test.go`, `b_test.go`",
		"- `WFL-002`: no test; the plan's reason: The golden cases show it.",
		"- `WFL-003`: tested by `c_test.go`; the plan's reason for having no test: Shown by hand.",
		"- `WFL-004`: no test, and the plan gives no reason",
		"",
		"The plan gives a reason for having no test for these IDs, which the change does not add or modify:",
		"",
		"- `WFL-009`: Not in the change.",
		"",
	}, "\n")
	if got := taskOfState(t, k, r, "conformance-audit"); !strings.HasSuffix(got, want) {
		t.Errorf("the audit's task after the report =\n%s\nwant it to end with\n%s", got, want)
	}
	result(t, k, r, "report-coverage", `{}`)
	if got := taskOfState(t, k, r, "conformance-audit"); !strings.Contains(got, "- none: the change adds or modifies no requirement\n") {
		t.Errorf("the audit's task after an empty report =\n%s\nwant it to say the change has no requirement", got)
	}
}

// TestACheckGetsItsTaskIDAsItsTask: the task text of a check is its task
// id, which names the check program's operation, such as validate-spec
// (decisions 0284 and 0297).
func TestACheckGetsItsTaskIDAsItsTask(t *testing.T) {
	k, r := firstWorkflowKernel(t, testProject, treeFiles{"AGENTS.md": true, "docs/style.md": true})
	for state, want := range map[string]string{"spec": "validate-spec", "report-coverage": "report-coverage", "archiving": "archive-change", "checking": "run-checks"} {
		if got := taskOfState(t, k, r, state); got != want {
			t.Errorf("the task of %s = %q, want %q", state, got, want)
		}
	}
}
