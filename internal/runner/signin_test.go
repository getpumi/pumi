// SPDX-License-Identifier: Apache-2.0
// SPDX-FileCopyrightText: The Pumi Authors

package runner

import (
	"strings"
	"testing"
	"time"

	eventsv1 "getpumi.dev/pumi/api/gen/pumi/events/v1"
	harnessv1 "getpumi.dev/pumi/api/gen/pumi/harness/v1"
)

// The tests of the sign-in path, where the harness talks to the provider
// directly and the runner counts, prices and caps the usage figures that
// the adapter passes (decisions 0240 and 0241).

// The models of the fixture's model role on the sign-in path, and a model
// that the test price table does not list.
const (
	signInModel    = "claude-opus-5-5"
	signInFallback = "claude-haiku-5-5"
	unlistedModel  = "a-model-nobody-prices"
)

// figureLimit is how long the fake adapter in the mode "figures until
// stopped" passes figures before it gives up waiting for the runner.
const figureLimit = 20 * time.Second

// The fake adapter's figure has 3 + 5 + 7 + 11 + 13 tokens. The test price
// table charges 2 microdollars for each token and 1 picodollar for the
// figure, so that the cost of the figures is rounded up.
const (
	oneFigureTokens   = 39
	figurePicodollars = oneFigureTokens*2_000_000 + 1
)

// testPrice is the test price table: it lists every model but
// unlistedModel.
func testPrice(u *harnessv1.UsageFigure) (int64, bool) {
	if u.GetModel() == unlistedModel {
		return 0, false
	}
	return (u.GetInputTokens()+u.GetFiveMinuteCacheWriteTokens()+u.GetOneHourCacheWriteTokens()+u.GetCacheReadTokens()+u.GetOutputTokens())*2_000_000 + 1, true
}

// microdollars returns picodollars rounded up to whole microdollars.
func microdollars(picodollars int64) int64 { return (picodollars + 999_999) / 1_000_000 }

// signIn puts the fixture's adapter in the sign-in mode, with the test
// price table and the models of its model role.
func (f *fixture) signIn() {
	a := f.cfg.Adapters[fakeAdapterName]
	a.SignIn = true
	f.cfg.Adapters[fakeAdapterName] = a
	f.cfg.Price = testPrice
	f.cfg.Models = map[string]Models{"frontier": {Model: signInModel, Fallback: signInFallback}}
}

// checkNoGateway fails the test when the runner opened an invocation with
// the gateway.
func (f *fixture) checkNoGateway(t *testing.T) {
	t.Helper()
	if opened, stillOpen := f.gateway.state(); len(opened) > 0 || stillOpen {
		t.Errorf("the runner opened %q with the gateway, want nothing on the sign-in path (decision 0240)", opened)
	}
}

// TestASignInRunStartsNoGateway: the runner opens nothing with the gateway,
// and the adapter gets no gateway address or token but the role's models
// (decisions 0240 and 0254). A runner with no gateway at all runs it too.
func TestASignInRunStartsNoGateway(t *testing.T) {
	for _, withGateway := range []bool{true, false} {
		t.Run(map[bool]string{true: "beside a gateway", false: "with no gateway"}[withGateway], func(t *testing.T) {
			f := newFixture(t, "success")
			f.signIn()
			if !withGateway {
				f.cfg.Gateway = nil
			}
			report := f.runOne(t)
			if report.GetOutcome() != eventsv1.InvocationFinished_OUTCOME_SUCCEEDED {
				t.Errorf("the outcome is %v, want succeeded; the log is\n%s", report.GetOutcome(), f.service.log())
			}
			inv := sentInvocation(t, f.service.log())
			if inv.Gateway != nil {
				t.Errorf("the adapter got the gateway %v, want none", inv.GetGateway())
			}
			if inv.GetModel() != signInModel || inv.GetFallbackModel() != signInFallback {
				t.Errorf("the adapter got the models %q and %q, want %q and %q", inv.GetModel(), inv.GetFallbackModel(), signInModel, signInFallback)
			}
			f.checkNoGateway(t)
		})
	}
}

// TestASignInRunUnderItsCapIsTheRunnersCount: the usage of the result is
// the runner's count and price of the adapter's figures, with the measured
// wall time, not the result's own figures (decision 0241).
func TestASignInRunUnderItsCapIsTheRunnersCount(t *testing.T) {
	f := newFixture(t, "figures")
	f.signIn()
	report := f.runOne(t)
	if report.GetOutcome() != eventsv1.InvocationFinished_OUTCOME_SUCCEEDED {
		t.Errorf("the outcome is %v, want succeeded; the log is\n%s", report.GetOutcome(), f.service.log())
	}
	u := report.GetResult().GetUsage()
	if u.GetTokens() != 2*oneFigureTokens || u.GetCostMicrodollars() != microdollars(2*figurePicodollars) || u.GetSeconds() < 1 {
		t.Errorf("the usage is %v, want the runner's count of two figures, %d tokens and %d microdollars, and the measured seconds",
			u, 2*oneFigureTokens, microdollars(2*figurePicodollars))
	}
	if want := runnerPrefix + "the adapter reported 10 tokens, 20 microdollars and 1 s; the usage is the runner's count of its usage figures (decision 0241)"; !strings.Contains(f.service.log(), want) {
		t.Errorf("the log does not hold %q; it is\n%s", want, f.service.log())
	}
	f.checkNoGateway(t)
}

// TestASignInRunIsStoppedAtItsCap: once the figures reach the token or the
// cost cap, the runner closes the adapter's standard input and reports
// cap reached, with the runner's count (decision 0241).
func TestASignInRunIsStoppedAtItsCap(t *testing.T) {
	for name, budget := range map[string]*harnessv1.Usage{
		"tokens": {Tokens: 5 * oneFigureTokens},
		"cost":   {CostMicrodollars: microdollars(5 * figurePicodollars)},
	} {
		t.Run(name, func(t *testing.T) { checkStoppedAtCap(t, budget) })
	}
}

// checkStoppedAtCap runs an adapter that passes figures until it is
// stopped, with budget, and checks that the runner stopped it at the cap.
func checkStoppedAtCap(t *testing.T, budget *harnessv1.Usage) {
	t.Helper()
	f := newFixture(t, "figures until stopped")
	f.signIn()
	f.doc.Budget = budget
	report := f.runOne(t)
	if report.GetOutcome() != eventsv1.InvocationFinished_OUTCOME_CAP_REACHED {
		t.Errorf("the outcome is %v, want cap reached; the log is\n%s", report.GetOutcome(), f.service.log())
	}
	log := f.service.log()
	if !strings.Contains(log, fakeAdapterPrefix+"standard input closed") {
		t.Errorf("the runner did not close the adapter's standard input; the log is\n%s", log)
	}
	if !strings.Contains(log, runnerPrefix+"the invocation plan-1/1 reached its token or cost cap with ") {
		t.Errorf("the log does not say that the run reached its cap; it is\n%s", log)
	}
	u := report.GetResult().GetUsage()
	if u.GetTokens() < 5*oneFigureTokens || u.GetTokens()%oneFigureTokens != 0 {
		t.Errorf("the usage has %d tokens, want the count of at least five figures", u.GetTokens())
	}
	if n := u.GetTokens() / oneFigureTokens; u.GetCostMicrodollars() != microdollars(n*figurePicodollars) {
		t.Errorf("the usage costs %d microdollars, want the price of %d figures, %d", u.GetCostMicrodollars(), n, microdollars(n*figurePicodollars))
	}
}

// TestASignInRunWithoutFiguresIsCheckedWhenItEnds: an adapter that passes
// no figures, as the Codex adapter does, is counted by the usage of its
// result, and the caps are checked when the run ends (decisions 0241 and
// 0243).
func TestASignInRunWithoutFiguresIsCheckedWhenItEnds(t *testing.T) {
	for _, c := range []struct {
		name    string
		budget  *harnessv1.Usage
		outcome eventsv1.InvocationFinished_Outcome
	}{
		{"under its cap", &harnessv1.Usage{Tokens: 50_000, CostMicrodollars: 70_000}, eventsv1.InvocationFinished_OUTCOME_SUCCEEDED},
		{"past its token cap", &harnessv1.Usage{Tokens: 5}, eventsv1.InvocationFinished_OUTCOME_CAP_REACHED},
		{"past its cost cap", &harnessv1.Usage{CostMicrodollars: 19}, eventsv1.InvocationFinished_OUTCOME_CAP_REACHED},
	} {
		t.Run(c.name, func(t *testing.T) { checkEndOfRun(t, c.budget, c.outcome) })
	}
}

// checkEndOfRun runs an adapter that passes no figures, with budget, and
// checks the usage and that the outcome is want.
func checkEndOfRun(t *testing.T, budget *harnessv1.Usage, want eventsv1.InvocationFinished_Outcome) {
	t.Helper()
	f := newFixture(t, "success")
	f.signIn()
	f.doc.Budget = budget
	report := f.runOne(t)
	log := f.service.log()
	if report.GetOutcome() != want {
		t.Errorf("the outcome is %v, want %v; the log is\n%s", report.GetOutcome(), want, log)
	}
	if u := report.GetResult().GetUsage(); u.GetTokens() != 10 || u.GetCostMicrodollars() != 20 || u.GetSeconds() < 1 {
		t.Errorf("the usage is %v, want the result's 10 tokens and 20 microdollars with the measured seconds", u)
	}
	if note := runnerPrefix + "the adapter reported 10 tokens, 20 microdollars and 1 s; it passed no usage figures, so the usage is the result's and the caps are checked when the run ends (decision 0241)"; !strings.Contains(log, note) {
		t.Errorf("the log does not hold %q; it is\n%s", note, log)
	}
	reached := strings.Contains(log, runnerPrefix+"the usage of plan-1/1 reached its token or cost cap when the run ended")
	if reached != (want == eventsv1.InvocationFinished_OUTCOME_CAP_REACHED) {
		t.Errorf("the log says the cap was reached: %v; the log is\n%s", reached, log)
	}
	// The cap does not hide the work: the tree is in the result.
	if report.GetResult().GetTree() == "" {
		t.Error("the result has no tree")
	}
}

// TestAFigureOfAnUnlistedModelFailsASignInRun: the runner cannot price a
// figure whose model the price table does not list, so it stops the run
// and fails it, as the gateway refuses such a call.
func TestAFigureOfAnUnlistedModelFailsASignInRun(t *testing.T) {
	f := newFixture(t, "unlisted figure")
	f.signIn()
	report := f.runOne(t)
	if report.GetOutcome() != eventsv1.InvocationFinished_OUTCOME_FAILED {
		t.Errorf("the outcome is %v, want failed; the log is\n%s", report.GetOutcome(), f.service.log())
	}
	if want := "the list-price table does not list the model \"" + unlistedModel + "\""; !strings.Contains(f.service.log(), want) {
		t.Errorf("the log does not say %q; it is\n%s", want, f.service.log())
	}
	if !strings.Contains(f.service.log(), fakeAdapterPrefix+"standard input closed") {
		t.Errorf("the runner did not stop the adapter; the log is\n%s", f.service.log())
	}
}

// TestTheConfinementCheckHoldsOnTheSignInPath: a write outside the
// allowed paths fails a run on the sign-in path as it does through the
// gateway (decisions 0159 and 0241).
func TestTheConfinementCheckHoldsOnTheSignInPath(t *testing.T) {
	f := newFixture(t, "outside")
	f.signIn()
	report := f.runOne(t)
	if report.GetOutcome() != eventsv1.InvocationFinished_OUTCOME_FAILED || report.GetResult().GetTree() != "" {
		t.Errorf("the outcome is %v with the tree %q, want failed with none", report.GetOutcome(), report.GetResult().GetTree())
	}
	if !strings.Contains(f.service.log(), "outside the allowed paths") {
		t.Errorf("the log does not name the write outside the paths; it is\n%s", f.service.log())
	}
}

// TestASignInRunIsRefused: an agent run on the sign-in path with no
// network access, or with no model for its role, fails before the adapter
// starts; and a runner with a sign-in adapter but no price table is
// refused (decisions 0226, 0241 and 0254).
func TestASignInRunIsRefused(t *testing.T) {
	for _, c := range []struct {
		name   string
		setup  func(f *fixture)
		reason string
	}{
		{"with no network access", func(f *fixture) {
			f.service.claim.Invocation.Capabilities.Network.Access = harnessv1.Capabilities_Network_ACCESS_NONE
		}, "ACCESS_NONE"},
		{"with no model for its role", func(f *fixture) { f.cfg.Models = nil }, "0254"},
	} {
		t.Run(c.name, func(t *testing.T) {
			f := newFixture(t, "success")
			f.signIn()
			c.setup(f)
			report := f.runOne(t)
			if report.GetOutcome() != eventsv1.InvocationFinished_OUTCOME_FAILED || !strings.Contains(f.service.log(), c.reason) {
				t.Errorf("the outcome is %v, want failed with %q in the log; the log is\n%s", report.GetOutcome(), c.reason, f.service.log())
			}
			f.checkNotStarted(t)
			f.checkNoGateway(t)
		})
	}
	t.Run("with no price table", func(t *testing.T) {
		f := newFixture(t, "success")
		f.signIn()
		f.cfg.Price = nil
		if _, err := RunOne(t.Context(), f.cfg); err == nil || !strings.Contains(err.Error(), "price") {
			t.Errorf("RunOne = %v, want an error about the price table", err)
		}
	})
}
