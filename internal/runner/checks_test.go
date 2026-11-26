// SPDX-License-Identifier: Apache-2.0
// SPDX-FileCopyrightText: The Pumi Authors

package runner

import (
	"slices"
	"strings"
	"testing"

	eventsv1 "getpumi.dev/pumi/api/gen/pumi/events/v1"
	harnessv1 "getpumi.dev/pumi/api/gen/pumi/harness/v1"
	workflowv1 "getpumi.dev/pumi/api/gen/pumi/workflow/v1"
	"google.golang.org/protobuf/encoding/protojson"
)

// specCheckOutput is the structured output of the fake check program: a
// valid spec, with the change's narrative (decisions 0277 and 0279).
const specCheckOutput = `{"specValid":{"tree":"tree-1","specAdapterVersion":"1.0.0","value":true},"changeNarrative":"Why the change."}`

// specCheck is the component name of the fixture's check.
const specCheck = "spec"

// newCheckFixture returns a fixture whose invocation is the check spec, in
// the gate role, which the check program at a copy of the test binary runs
// in mode. The invocation keeps the fixture's model role, which the
// runner leaves out of a check's harness invocation.
func newCheckFixture(t *testing.T, mode string) (*fixture, string) {
	t.Helper()
	program := copyAdapter(t, "spec-check")
	f := newFixture(t, mode)
	f.cfg.Checks = map[string]Check{specCheck: {Path: program, SHA256: fakeAdapterHash(t)}}
	doc := f.service.claim.Invocation
	doc.Component = &harnessv1.Component{Kind: harnessv1.Component_KIND_CHECK, Name: specCheck}
	doc.Role = harnessv1.Role_ROLE_GATE
	doc.OutputMessage = "pumi.workflow.v1.SpecCheckOutput"
	doc.Capabilities.Network.Access = harnessv1.Capabilities_Network_ACCESS_NONE
	return f, program
}

// TestACheckProgramReportsAFact: the runner starts the program that the
// configuration names for the check's component, not the check adapter,
// with the harness invocation and no gateway, model or model role; its
// output is the fact, its file changes are dropped, and its standard
// error is the log, which the blob store keeps (decisions 0272 and 0282).
func TestACheckProgramReportsAFact(t *testing.T) {
	f, program := newCheckFixture(t, "spec check")
	report := f.runOne(t)
	log := f.service.log()
	if report.GetOutcome() != eventsv1.InvocationFinished_OUTCOME_SUCCEEDED {
		t.Fatalf("the outcome is %v, want succeeded; the log is\n%s", report.GetOutcome(), log)
	}
	if got := startedPath(t, log); got != program {
		t.Errorf("the runner started %s, want the check program %s", got, program)
	}
	want := runnerPrefix + "the check program " + specCheck + " (" + program + ", SHA-256 " + fakeAdapterHash(t) + ") runs the invocation plan-1/1"
	if !strings.Contains(log, want) {
		t.Errorf("the log does not hold %q; it is\n%s", want, log)
	}
	out := &workflowv1.SpecCheckOutput{}
	if err := protojson.Unmarshal([]byte(report.GetResult().GetOutput()), out); err != nil || !out.GetSpecValid().GetValue() {
		t.Errorf("the output %q parses to %v, %v; want spec.valid true", report.GetResult().GetOutput(), out, err)
	}
	if tree := report.GetResult().GetTree(); tree != "" {
		t.Errorf("the check's result has the tree %s, want none (decision 0282)", tree)
	}
	inv := sentInvocation(t, log)
	if inv.GetGateway() != nil || inv.GetModel() != "" || inv.GetFallbackModel() != "" || inv.GetModelRole() != "" {
		t.Errorf("the check program got the gateway %v, the models %q and %q and the model role %q; want none (decision 0272)",
			inv.GetGateway(), inv.GetModel(), inv.GetFallbackModel(), inv.GetModelRole())
	}
	if inv.GetRole() != harnessv1.Role_ROLE_GATE || inv.GetComponent().GetName() != specCheck {
		t.Errorf("the check program got the role %v and the component %v, want the gate role and %s", inv.GetRole(), inv.GetComponent(), specCheck)
	}
	if opened, _ := f.gateway.state(); len(opened) > 0 {
		t.Errorf("the runner opened %q with the gateway, want nothing", opened)
	}
	if u := report.GetResult().GetUsage(); u.GetTokens() != 0 || u.GetCostMicrodollars() != 0 {
		t.Errorf("the check's usage is %v, want no tokens or cost", u)
	}
	stored := f.blobs.stored()[report.GetResult().GetLogBlobHash()]
	if !strings.Contains(stored, fakeAdapterPrefix+"invocation ") {
		t.Errorf("the stored log does not hold the check program's standard error; it is\n%s", stored)
	}
	checkEmpty(t, f.cfg.WorkDir)
}

// TestTheArchiveStepReturnsTheTree: a check whose document says that it
// returns a tree, as the archive step's does, gets a result with the
// workspace's tree (decisions 0278 and 0282).
func TestTheArchiveStepReturnsTheTree(t *testing.T) {
	f, _ := newCheckFixture(t, "archive")
	f.service.claim.Invocation.ReturnsTree = true
	report := f.runOne(t)
	if report.GetOutcome() != eventsv1.InvocationFinished_OUTCOME_SUCCEEDED {
		t.Fatalf("the outcome is %v, want succeeded; the log is\n%s", report.GetOutcome(), f.service.log())
	}
	if got := f.changed(t, report.GetResult().GetTree()); !slices.Equal(got, []string{"internal/runner/specs/spec.md"}) {
		t.Errorf("the resulting tree changes %q, want internal/runner/specs/spec.md", got)
	}
}

// TestTheArchiveStepIsConfinedToTheWritePaths: a check that returns a tree
// fails when it writes outside the allowed paths, as an agent run does
// (decisions 0159 and 0282).
func TestTheArchiveStepIsConfinedToTheWritePaths(t *testing.T) {
	f, _ := newCheckFixture(t, "archive outside")
	f.service.claim.Invocation.ReturnsTree = true
	report := f.runOne(t)
	if report.GetOutcome() != eventsv1.InvocationFinished_OUTCOME_FAILED || report.GetResult().GetTree() != "" {
		t.Errorf("the outcome is %v with the tree %q, want failed with none; the log is\n%s",
			report.GetOutcome(), report.GetResult().GetTree(), f.service.log())
	}
	if log := f.service.log(); !strings.Contains(log, "README.md") || !strings.Contains(log, "0159") {
		t.Errorf("the log does not name the write outside the paths; it is\n%s", log)
	}
}

// TestACheckProgramWhoseHashDiffersIsRefused: the runner checks the
// program's SHA-256 before it starts it and refuses one that does not
// match, naming the check and both hashes (decisions 0245 and 0272).
func TestACheckProgramWhoseHashDiffersIsRefused(t *testing.T) {
	f, program := newCheckFixture(t, "spec check")
	pinned := strings.Repeat("0", 64)
	f.cfg.Checks[specCheck] = Check{Path: program, SHA256: pinned}
	report := f.runOne(t)
	if report.GetOutcome() != eventsv1.InvocationFinished_OUTCOME_FAILED {
		t.Errorf("the outcome is %v, want failed", report.GetOutcome())
	}
	log := f.service.log()
	for _, want := range []string{"check program " + specCheck, pinned, fakeAdapterHash(t)} {
		if !strings.Contains(log, want) {
			t.Errorf("the log does not hold %q; it is\n%s", want, log)
		}
	}
	f.checkNotStarted(t)
}

// TestACheckProgramThatWritesNoResultFails: a check program that exits
// without a result fails the invocation, as an adapter does (decision
// 0158).
func TestACheckProgramThatWritesNoResultFails(t *testing.T) {
	f, _ := newCheckFixture(t, "no result")
	report := f.runOne(t)
	if report.GetOutcome() != eventsv1.InvocationFinished_OUTCOME_FAILED {
		t.Errorf("the outcome is %v, want failed; the log is\n%s", report.GetOutcome(), f.service.log())
	}
	if report.GetResult().GetOutput() != "" || report.GetResult().GetTree() != "" {
		t.Errorf("the failed check's result is %v, want no output or tree", report.GetResult())
	}
}

// TestACheckThatNamesNoProgramFails: a check whose component the
// configuration names no program for fails before anything starts; the
// runner does not fall back to the check adapter (decision 0272).
func TestACheckThatNamesNoProgramFails(t *testing.T) {
	f, _ := newCheckFixture(t, "spec check")
	f.service.claim.Invocation.Component.Name = "scope"
	report := f.runOne(t)
	if report.GetOutcome() != eventsv1.InvocationFinished_OUTCOME_FAILED {
		t.Errorf("the outcome is %v, want failed", report.GetOutcome())
	}
	if log := f.service.log(); !strings.Contains(log, `check "scope"`) || !strings.Contains(log, "0272") {
		t.Errorf("the log does not name the check scope and decision 0272; it is\n%s", log)
	}
	f.checkNotStarted(t)
}

// TestOnlyExternalActionsStartTheCheckAdapter: an external action names no
// model role and still runs on the check adapter (decisions 0253 and
// 0272).
func TestOnlyExternalActionsStartTheCheckAdapter(t *testing.T) {
	adapter := copyAdapter(t, "check-adapter")
	f, _ := newCheckFixture(t, "unchanged")
	f.cfg.Adapters["actions"] = Adapter{Path: adapter, SHA256: fakeAdapterHash(t)}
	f.cfg.CheckAdapter = "actions"
	doc := f.service.claim.Invocation
	doc.Component = &harnessv1.Component{Kind: harnessv1.Component_KIND_EXTERNAL_ACTION, Name: "forge"}
	doc.OutputMessage = "pumi.workflow.v1.ImplementationOutput"
	doc.ModelRole = ""
	report := f.runOne(t)
	if report.GetOutcome() != eventsv1.InvocationFinished_OUTCOME_SUCCEEDED {
		t.Fatalf("the outcome is %v, want succeeded; the log is\n%s", report.GetOutcome(), f.service.log())
	}
	if got := startedPath(t, f.service.log()); got != adapter {
		t.Errorf("the runner started %s, want the check adapter %s", got, adapter)
	}
}

// TestTheLimitsApplyToCheckPrograms: the wall-time limit and a cancel stop
// a check program as they stop an adapter (decisions 0035 and 0193).
func TestTheLimitsApplyToCheckPrograms(t *testing.T) {
	for name, tc := range map[string]struct {
		set  func(f *fixture)
		want eventsv1.InvocationFinished_Outcome
	}{
		"wall time": {func(f *fixture) { f.withLimit(1) }, eventsv1.InvocationFinished_OUTCOME_CAP_REACHED},
		"cancel":    {func(f *fixture) { f.service.cancelAt = 2 }, eventsv1.InvocationFinished_OUTCOME_CANCELLED},
	} {
		t.Run(name, func(t *testing.T) {
			f, _ := newCheckFixture(t, "stop")
			f.service.claim.Invocation.OutputMessage = "pumi.workflow.v1.ImplementationOutput"
			tc.set(f)
			report := f.runOne(t)
			if report.GetOutcome() != tc.want {
				t.Errorf("the outcome is %v, want %v; the log is\n%s", report.GetOutcome(), tc.want, f.service.log())
			}
			if !strings.Contains(f.service.log(), fakeAdapterPrefix+"standard input closed") {
				t.Errorf("the check program did not see standard input close; the log is\n%s", f.service.log())
			}
		})
	}
}

// TestABadCheckProgramIsRefused: the runner refuses a configuration with a
// check program whose path is not absolute or whose SHA-256 is not 64
// lowercase hexadecimal digits, before it claims anything.
func TestABadCheckProgramIsRefused(t *testing.T) {
	for name, c := range map[string]Check{
		"relative path": {Path: "spec-check", SHA256: strings.Repeat("a", 64)},
		"bad hash":      {Path: "/opt/spec-check", SHA256: "ABC"},
	} {
		t.Run(name, func(t *testing.T) {
			f, _ := newCheckFixture(t, "spec check")
			f.cfg.Checks[specCheck] = c
			ran, err := RunOne(t.Context(), f.cfg)
			if err == nil || !strings.Contains(err.Error(), `check program "spec"`) {
				t.Errorf("RunOne = %v, %v; want an error that names the check program spec", ran, err)
			}
			if f.service.claim == nil {
				t.Error("the runner claimed the invocation, want no claim")
			}
		})
	}
}
