// SPDX-License-Identifier: Apache-2.0
// SPDX-FileCopyrightText: The Pumi Authors

package runner

import (
	"slices"
	"strings"
	"testing"

	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"

	"getpumi.dev/pumi/api/events"
	eventsv1 "getpumi.dev/pumi/api/gen/pumi/events/v1"
	harnessv1 "getpumi.dev/pumi/api/gen/pumi/harness/v1"
	runnerv1 "getpumi.dev/pumi/api/gen/pumi/runner/v1"
)

// The tests of the fallback of a model role to another adapter, which is
// a circuit breaker (decision 0246).

// The fallback of the fixture's model role: an adapter of its own, the
// test binary copied to a file of this name, and its model, and the
// role's own model.
const (
	fallbackAdapterName = "fallback"
	fallbackAdapterFile = "fallback-adapter"
	fallbackModel       = "astra-1"
	roleModel           = "claude-fable-5-2"
)

// withFallback gives the fixture's model role, frontier, a fallback on a
// second adapter, in the sign-in mode when signIn is set, and returns the
// path of that adapter.
func (f *fixture) withFallback(t *testing.T, signIn bool) string {
	t.Helper()
	path := copyAdapter(t, fallbackAdapterFile)
	f.cfg.Adapters[fallbackAdapterName] = Adapter{Path: path, SHA256: fakeAdapterHash(t), SignIn: signIn}
	f.cfg.Fallbacks = map[string]Fallback{"frontier": {Adapter: fallbackAdapterName, Model: fallbackModel}}
	f.cfg.Models = map[string]Models{"frontier": {Model: roleModel}}
	if signIn {
		f.cfg.Price = testPrice
	}
	return path
}

// startedPaths returns the paths that the fake adapter says it was
// started as, from the log, in order.
func startedPaths(log string) []string {
	var paths []string
	for line := range strings.SplitSeq(log, "\n") {
		if p, ok := strings.CutPrefix(line, fakeAdapterPrefix+"started as "); ok {
			paths = append(paths, p)
		}
	}
	return paths
}

// sentInvocations returns the harness invocations that the fake adapter
// logged, in order.
func sentInvocations(t *testing.T, log string) []*harnessv1.Invocation {
	t.Helper()
	var out []*harnessv1.Invocation
	for line := range strings.Lines(log) {
		if rest, ok := strings.CutPrefix(line, fakeAdapterPrefix+"invocation "); ok {
			inv := &harnessv1.Invocation{}
			if err := protojson.Unmarshal([]byte(rest), inv); err != nil {
				t.Fatal(err)
			}
			out = append(out, inv)
		}
	}
	return out
}

// switches returns the model_role.switched events of the report.
func switches(t *testing.T, report *runnerv1.ReportRequest) []*eventsv1.ModelRoleSwitched {
	t.Helper()
	var out []*eventsv1.ModelRoleSwitched
	for _, e := range report.GetEvents() {
		if e.GetType() != events.TypeModelRoleSwitched {
			t.Errorf("the report carries a %s, want only model_role.switched", e.GetType())
			continue
		}
		if err := events.Validate(e); err != nil {
			t.Errorf("the switch is not a valid event: %v", err)
		}
		s := &eventsv1.ModelRoleSwitched{}
		if err := e.GetProtoData().UnmarshalTo(s); err != nil {
			t.Fatal(err)
		}
		out = append(out, s)
	}
	return out
}

// wantSwitch is the switch of the fixture's model role to its fallback
// after the limit stopped the run of plan-1/1 on the role's own adapter.
var wantSwitch = &eventsv1.ModelRoleSwitched{
	Role: "frontier", Adapter: fallbackAdapterName, Model: fallbackModel, Invocation: "plan-1/1",
	Limit: &eventsv1.ModelRoleSwitched_Limit{Adapter: fakeAdapterName, Model: roleModel},
}

// checkOneSwitch fails the test unless the report carries exactly the
// switch wantSwitch.
func checkOneSwitch(t *testing.T, report *runnerv1.ReportRequest) {
	t.Helper()
	got := switches(t, report)
	if len(got) != 1 || !proto.Equal(got[0], wantSwitch) {
		t.Errorf("the report carries the switches %v, want only %v", got, wantSwitch)
	}
}

// TestALimitSwitchesTheRoleToItsFallback: the provider's limit stops the
// run of the model role's own adapter. The runner starts the role's
// fallback adapter once, in a fresh checkout of the same input tree, with
// the budget that the first run left, and reports that run's result with
// both runs' usage, both runs in the log and the switch of the role
// (decision 0246).
func TestALimitSwitchesTheRoleToItsFallback(t *testing.T) {
	f := newFixture(t, "limited but the fallback")
	fallback := f.withFallback(t, false)
	report := f.runOne(t)
	log := f.service.log()
	if report.GetOutcome() != eventsv1.InvocationFinished_OUTCOME_SUCCEEDED {
		t.Fatalf("the outcome is %v, want succeeded; the log is\n%s", report.GetOutcome(), log)
	}
	checkOneSwitch(t, report)
	if got := startedPaths(log); !slices.Equal(got, []string{"/proc/self/exe", fallback}) {
		t.Errorf("the runner started %q, want the role's adapter and then its fallback adapter %s", got, fallback)
	}
	if got := f.gateway.adapters(); !slices.Equal(got, []string{fakeAdapterName, fallbackAdapterName}) {
		t.Errorf("the runner opened the gateway for %q, want %s and then %s", got, fakeAdapterName, fallbackAdapterName)
	}
	if !strings.Contains(log, runnerPrefix+"the model role frontier switches to its fallback, the model "+fallbackModel+" on the adapter "+fallbackAdapterName) {
		t.Errorf("the log does not say that the role switched; it is\n%s", log)
	}
	if got := f.changed(t, report.GetResult().GetTree()); !slices.Equal(got, []string{"internal/runner/b.go"}) {
		t.Errorf("the resulting tree changes %q, want only the fallback run's internal/runner/b.go", got)
	}
	if u := report.GetResult().GetUsage(); u.GetTokens() != 2*gatewayTokens || u.GetCostMicrodollars() != 2*gatewayCost {
		t.Errorf("the usage is %v, want both runs' totals, %d tokens and %d microdollars", u, 2*gatewayTokens, 2*gatewayCost)
	}
	left := &harnessv1.Usage{Tokens: 50_000 - gatewayTokens, CostMicrodollars: 70_000 - gatewayCost}
	f.gateway.mu.Lock()
	budgets := f.gateway.budgets
	f.gateway.mu.Unlock()
	if len(budgets) != 2 || !proto.Equal(budgets[1], left) {
		t.Errorf("the gateway opened the runs with the budgets %v, want the fallback run's to be what the first left, %v", budgets, left)
	}
	if invs := sentInvocations(t, log); len(invs) != 2 || !proto.Equal(invs[1].GetBudget(), left) {
		t.Errorf("the harness invocations are %v, want the fallback run's budget to be %v", invs, left)
	}
}

// TestASignInFallbackGetsTheFallbacksModel: on the sign-in path the
// fallback adapter's harness invocation names the fallback's model, with
// no fallback of its own (decisions 0246 and 0254).
func TestASignInFallbackGetsTheFallbacksModel(t *testing.T) {
	f := newFixture(t, "limited but the fallback")
	f.withFallback(t, true)
	report := f.runOne(t)
	log := f.service.log()
	if report.GetOutcome() != eventsv1.InvocationFinished_OUTCOME_SUCCEEDED {
		t.Fatalf("the outcome is %v, want succeeded; the log is\n%s", report.GetOutcome(), log)
	}
	invs := sentInvocations(t, log)
	if len(invs) != 2 || invs[1].GetModel() != fallbackModel || invs[1].GetFallbackModel() != "" || invs[1].GetGateway() != nil {
		t.Fatalf("the harness invocations are %v, want the second with the model %s, no fallback model and no gateway", invs, fallbackModel)
	}
	if got := f.gateway.adapters(); !slices.Equal(got, []string{fakeAdapterName}) {
		t.Errorf("the runner opened the gateway for %q, want only %s", got, fakeAdapterName)
	}
	checkOneSwitch(t, report)
}

// TestALimitOnTheFallbackFailsTheInvocation: the provider's limit also
// stops the fallback's run. The invocation fails with the limit as its
// reason, the role stays switched, and the runner does not fall back
// again (decision 0246).
func TestALimitOnTheFallbackFailsTheInvocation(t *testing.T) {
	f := newFixture(t, "limited")
	fallback := f.withFallback(t, false)
	report := f.runOne(t)
	log := f.service.log()
	if report.GetOutcome() != eventsv1.InvocationFinished_OUTCOME_FAILED {
		t.Fatalf("the outcome is %v, want failed; the log is\n%s", report.GetOutcome(), log)
	}
	checkOneSwitch(t, report)
	if got := startedPaths(log); !slices.Equal(got, []string{"/proc/self/exe", fallback}) {
		t.Errorf("the runner started %q, want the role's adapter and its fallback adapter once each", got)
	}
	if n := strings.Count(log, "the provider's usage or rate limit stopped the run"); n != 2 {
		t.Errorf("the log names the limit %d times, want once for each run; it is\n%s", n, log)
	}
}

// TestASwitchedRoleRunsOnItsFallback: the claim says that the model role
// is on its fallback, so the runner starts only the fallback adapter,
// with the fallback's model, and reports no switch (decision 0246).
func TestASwitchedRoleRunsOnItsFallback(t *testing.T) {
	f := newFixture(t, "limited but the fallback")
	fallback := f.withFallback(t, true)
	f.service.claim.ModelRoleSwitched = true
	report := f.runOne(t)
	log := f.service.log()
	if report.GetOutcome() != eventsv1.InvocationFinished_OUTCOME_SUCCEEDED {
		t.Fatalf("the outcome is %v, want succeeded; the log is\n%s", report.GetOutcome(), log)
	}
	if got := startedPaths(log); !slices.Equal(got, []string{fallback}) {
		t.Errorf("the runner started %q, want only the fallback adapter %s", got, fallback)
	}
	if invs := sentInvocations(t, log); len(invs) != 1 || invs[0].GetModel() != fallbackModel {
		t.Errorf("the harness invocations are %v, want one with the model %s", invs, fallbackModel)
	}
	if len(report.GetEvents()) != 0 {
		t.Errorf("the report carries %v, want no switch", report.GetEvents())
	}
	if !strings.Contains(log, "the model role frontier is on its fallback") {
		t.Errorf("the log does not say that the role is on its fallback; it is\n%s", log)
	}
}

// TestALimitOnASwitchedRoleFailsTheInvocation: the role is on its
// fallback, and the limit stops the fallback's run; there is no second
// fallback (decision 0246).
func TestALimitOnASwitchedRoleFailsTheInvocation(t *testing.T) {
	f := newFixture(t, "limited")
	fallback := f.withFallback(t, false)
	f.service.claim.ModelRoleSwitched = true
	report := f.runOne(t)
	if report.GetOutcome() != eventsv1.InvocationFinished_OUTCOME_FAILED {
		t.Fatalf("the outcome is %v, want failed", report.GetOutcome())
	}
	if got := startedPaths(f.service.log()); !slices.Equal(got, []string{fallback}) {
		t.Errorf("the runner started %q, want only the fallback adapter once", got)
	}
	if len(report.GetEvents()) != 0 {
		t.Errorf("the report carries %v, want no switch", report.GetEvents())
	}
}

// TestALimitOnARoleWithNoFallbackFailsTheInvocation: a model role with no
// fallback on another adapter does not switch; the invocation fails with
// the limit as its reason (decision 0246).
func TestALimitOnARoleWithNoFallbackFailsTheInvocation(t *testing.T) {
	f := newFixture(t, "limited")
	report := f.runOne(t)
	if report.GetOutcome() != eventsv1.InvocationFinished_OUTCOME_FAILED {
		t.Fatalf("the outcome is %v, want failed", report.GetOutcome())
	}
	if got := startedPaths(f.service.log()); len(got) != 1 {
		t.Errorf("the runner started %q, want one adapter", got)
	}
	if len(report.GetEvents()) != 0 {
		t.Errorf("the report carries %v, want no switch", report.GetEvents())
	}
}

// TestASwitchedRoleWithNoFallbackFailsTheInvocation: the log says that
// the model role is on its fallback, but the configuration gives it no
// fallback on another adapter. The runner does not switch it back by
// itself: the invocation fails before any adapter starts, and the log
// says how a person switches the role back (decision 0246).
func TestASwitchedRoleWithNoFallbackFailsTheInvocation(t *testing.T) {
	f := newFixture(t, "success")
	f.service.claim.ModelRoleSwitched = true
	report := f.runOne(t)
	if report.GetOutcome() != eventsv1.InvocationFinished_OUTCOME_FAILED {
		t.Fatalf("the outcome is %v, want failed", report.GetOutcome())
	}
	f.checkNotStarted(t)
	if log := f.service.log(); !strings.Contains(log, "pumi role reset frontier") {
		t.Errorf("the log does not name pumi role reset; it is\n%s", log)
	}
}

// TestACheckDoesNotFallBack: a check names no model role, so a limit that
// stops it fails it and switches nothing.
func TestACheckDoesNotFallBack(t *testing.T) {
	f := newFixture(t, "limited")
	f.withFallback(t, false)
	f.service.claim.Invocation.Component.Kind = harnessv1.Component_KIND_CHECK
	f.service.claim.Invocation.ModelRole = ""
	f.service.claim.Invocation.Capabilities.Network.Access = harnessv1.Capabilities_Network_ACCESS_NONE
	report := f.runOne(t)
	if report.GetOutcome() != eventsv1.InvocationFinished_OUTCOME_FAILED || len(report.GetEvents()) != 0 {
		t.Errorf("the check's report is %v with %v, want failed with no switch", report.GetOutcome(), report.GetEvents())
	}
}

// TestAFallbackOnAnAdapterTheRunnerDoesNotHaveIsRefused: the runner
// refuses a configuration whose fallback names an adapter it does not
// list.
func TestAFallbackOnAnAdapterTheRunnerDoesNotHaveIsRefused(t *testing.T) {
	f := newFixture(t, "success")
	f.cfg.Fallbacks = map[string]Fallback{"frontier": {Adapter: "missing", Model: fallbackModel}}
	if _, err := RunOne(t.Context(), f.cfg); err == nil || !strings.Contains(err.Error(), `"missing"`) {
		t.Errorf("RunOne = %v, want an error that names the adapter missing", err)
	}
}
