// SPDX-License-Identifier: Apache-2.0
// SPDX-FileCopyrightText: The Pumi Authors

package runner

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	eventsv1 "getpumi.dev/pumi/api/gen/pumi/events/v1"
	harnessv1 "getpumi.dev/pumi/api/gen/pumi/harness/v1"
	"google.golang.org/protobuf/proto"
)

// The fake gateway's URL, the token it opens and the totals it counts for
// the fixture's invocation.
const (
	fakeGatewayURL = "http://127.0.0.1:1"
	testToken      = "pumi-gateway-token-0123456789abcdef"
	gatewayTokens  = 1_234
	gatewayCost    = 5_678
)

// fakeGateway is the model gateway in the tests: it opens one token for
// each invocation and gives totals set by the test.
type fakeGateway struct {
	mu          sync.Mutex
	keepsSignIn bool
	// signIn are the adapters whose harness keeps its sign-in, beside
	// every adapter when keepsSignIn is set.
	signIn map[string]bool
	// opener are the adapters of the invocations opened.
	opener []string
	totals GatewayTotals
	// openErr is the error of every Open when set.
	openErr error
	// opened are the invocations opened, with their budgets, and open the
	// tokens not closed.
	opened  []string
	budgets []*harnessv1.Usage
	open    map[string]bool
	// read is whether Totals was called while the token was open.
	read bool
	// line, when set, is written to the invocation's log that Open gets
	// when the runner reads the totals, as the gateway writes a fallback or
	// a retry there during the run.
	line string
	log  io.Writer
}

func (g *fakeGateway) URL() string { return fakeGatewayURL }

func (g *fakeGateway) KeepsSignIn(adapter string) bool { return g.keepsSignIn || g.signIn[adapter] }

func (g *fakeGateway) Open(invocation, adapter string, budget *harnessv1.Usage, log io.Writer) (string, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.openErr != nil {
		return "", g.openErr
	}
	g.opener = append(g.opener, adapter)
	g.log = log
	g.opened = append(g.opened, invocation)
	g.budgets = append(g.budgets, proto.CloneOf(budget))
	if g.open == nil {
		g.open = map[string]bool{}
	}
	g.open[testToken] = true
	return testToken, nil
}

func (g *fakeGateway) Totals(token string) (GatewayTotals, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if !g.open[token] {
		return GatewayTotals{}, fmt.Errorf("the token %q is not open", token)
	}
	g.read = true
	if g.line != "" && g.log != nil {
		_, _ = io.WriteString(g.log, g.line)
	}
	return g.totals, nil
}

func (g *fakeGateway) Close(token string) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	if !g.open[token] {
		return fmt.Errorf("the token %q is not open", token)
	}
	delete(g.open, token)
	return nil
}

// state returns the invocations opened and whether any token is still
// open.
func (g *fakeGateway) state() (opened []string, stillOpen bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	return slices.Clone(g.opened), len(g.open) > 0
}

// adapters returns the adapters of the invocations opened.
func (g *fakeGateway) adapters() []string {
	g.mu.Lock()
	defer g.mu.Unlock()
	return slices.Clone(g.opener)
}

// checkNotStarted fails the test when the fake adapter started.
func (f *fixture) checkNotStarted(t *testing.T) {
	t.Helper()
	if _, err := os.Stat(filepath.Join(f.pidDir, "pid")); err == nil {
		t.Error("the runner started the adapter")
	}
}

// TestTheRunnerOpensTheInvocationWithTheGateway: before the adapter
// starts, the runner opens the invocation with its budget, passes the base
// URL, the token and whether the harness keeps its sign-in in the harness
// invocation, reads the totals after the adapter exits and closes the
// token (decision 0231).
func TestTheRunnerOpensTheInvocationWithTheGateway(t *testing.T) {
	for _, keeps := range []bool{false, true} {
		t.Run(fmt.Sprintf("keeps sign-in %v", keeps), func(t *testing.T) { checkGatewayOpened(t, keeps) })
	}
}

// checkGatewayOpened runs the fixture's invocation with a gateway that
// keeps the harness's sign-in or not, and checks what the runner did with
// the gateway.
func checkGatewayOpened(t *testing.T, keeps bool) {
	t.Helper()
	f := newFixture(t, "success")
	f.gateway.keepsSignIn = keeps
	f.runOne(t)
	inv := sentInvocation(t, f.service.log())
	if g := inv.GetGateway(); g.GetBaseUrl() != fakeGatewayURL || g.GetToken() != maskedCredential || g.GetKeepsSignIn() != keeps {
		t.Errorf("the gateway is %v, want %s, the masked token and keeps_sign_in %v", g, fakeGatewayURL, keeps)
	}
	opened, stillOpen := f.gateway.state()
	if !slices.Equal(opened, []string{"plan-1/1"}) {
		t.Errorf("the runner opened %q with the gateway, want plan-1/1", opened)
	}
	if len(f.gateway.budgets) != 1 || !proto.Equal(f.gateway.budgets[0], f.doc.GetBudget()) {
		t.Errorf("the runner opened it with the budgets %v, want the document's", f.gateway.budgets)
	}
	if !f.gateway.read || stillOpen {
		t.Errorf("the runner read the totals %v and left the token open %v; want read and closed", f.gateway.read, stillOpen)
	}
}

// TestTheUsageIsTheGatewaysTotals: the usage of the result is the
// gateway's totals with the measured wall time, and the adapter's own
// figures go to the log (decision 0230).
func TestTheUsageIsTheGatewaysTotals(t *testing.T) {
	for _, mode := range []string{"success", "failed", "exit 3"} {
		t.Run(mode, func(t *testing.T) {
			f := newFixture(t, mode)
			report := f.runOne(t)
			u := report.GetResult().GetUsage()
			if u.GetTokens() != gatewayTokens || u.GetCostMicrodollars() != gatewayCost || u.GetSeconds() < 1 {
				t.Errorf("the usage is %v, want the gateway's totals and the measured seconds", u)
			}
			// An adapter that exits with a status other than 0 wrote no
			// result to read the figures from.
			if mode != "exit 3" && !strings.Contains(f.service.log(), runnerPrefix+"the adapter reported 10 tokens, 20 microdollars and 1 s") {
				t.Errorf("the log does not hold the adapter's own figures; it is\n%s", f.service.log())
			}
		})
	}
}

// TestAGatewayCapIsCapReached: when the gateway refused a call of the
// invocation at a cap, the outcome is cap reached, whatever the adapter
// reports (decision 0228).
func TestAGatewayCapIsCapReached(t *testing.T) {
	for _, mode := range []string{"success", "failed", "exit 3", "not json"} {
		t.Run(mode, func(t *testing.T) {
			f := newFixture(t, mode)
			f.gateway.totals.CapReached = true
			report := f.runOne(t)
			if report.GetOutcome() != eventsv1.InvocationFinished_OUTCOME_CAP_REACHED {
				t.Errorf("the outcome is %v, want cap reached; the log is\n%s", report.GetOutcome(), f.service.log())
			}
			if u := report.GetResult().GetUsage(); u.GetTokens() != gatewayTokens || u.GetCostMicrodollars() != gatewayCost {
				t.Errorf("the usage is %v, want the gateway's totals", u)
			}
			if !strings.Contains(f.service.log(), runnerPrefix+"the model gateway refused a call of plan-1/1 at its token or cost cap") {
				t.Errorf("the log does not say that the gateway refused a call at a cap; it is\n%s", f.service.log())
			}
		})
	}
}

// TestACancelOutranksAGatewayCap: a cancelled invocation stays cancelled
// when the gateway also refused a call at a cap.
func TestACancelOutranksAGatewayCap(t *testing.T) {
	f := newFixture(t, "stop")
	f.gateway.totals.CapReached = true
	f.cancelOnceWritten("pid")
	report := f.runOne(t)
	if report.GetOutcome() != eventsv1.InvocationFinished_OUTCOME_CANCELLED {
		t.Errorf("the outcome is %v, want cancelled; the log is\n%s", report.GetOutcome(), f.service.log())
	}
}

// TestAnAgentRunIsRefused: an agent run with no gateway, or whose network
// access is none or unspecified, fails before the adapter starts, and the
// gateway opens nothing (decision 0226).
func TestAnAgentRunIsRefused(t *testing.T) {
	for _, c := range []struct {
		name   string
		setup  func(f *fixture)
		reason string
	}{
		{"with no gateway", func(f *fixture) { f.cfg.Gateway = nil }, "no model gateway"},
		{"with no network access", func(f *fixture) {
			f.service.claim.Invocation.Capabilities.Network.Access = harnessv1.Capabilities_Network_ACCESS_NONE
		}, "ACCESS_NONE"},
		{"with unspecified network access", func(f *fixture) { f.service.claim.Invocation.Capabilities.Network = nil }, "ACCESS_UNSPECIFIED"},
		{"when the gateway cannot open it", func(f *fixture) { f.gateway.openErr = errors.New("the gateway is closed") }, "the gateway is closed"},
	} {
		t.Run(c.name, func(t *testing.T) {
			f := newFixture(t, "success")
			c.setup(f)
			report := f.runOne(t)
			if report.GetOutcome() != eventsv1.InvocationFinished_OUTCOME_FAILED {
				t.Errorf("the outcome is %v, want failed", report.GetOutcome())
			}
			if log := f.service.log(); !strings.Contains(log, c.reason) {
				t.Errorf("the log does not say %q; it is\n%s", c.reason, log)
			}
			f.checkNotStarted(t)
			if opened, stillOpen := f.gateway.state(); len(opened) > 0 || stillOpen {
				t.Errorf("the gateway opened %q and left a token open %v, want nothing", opened, stillOpen)
			}
		})
	}
}

// TestACheckRunsWithoutTheGateway: a check makes no model calls, so the
// runner opens no gateway for it; its usage is the measured wall time.
func TestACheckRunsWithoutTheGateway(t *testing.T) {
	f, _ := newCheckFixture(t, "success")
	f.service.claim.Invocation.OutputMessage = "pumi.workflow.v1.ImplementationOutput"
	f.cfg.Gateway = nil
	report := f.runOne(t)
	if report.GetOutcome() != eventsv1.InvocationFinished_OUTCOME_SUCCEEDED {
		t.Errorf("the outcome is %v, want succeeded; the log is\n%s", report.GetOutcome(), f.service.log())
	}
	if inv := sentInvocation(t, f.service.log()); inv.Gateway != nil {
		t.Errorf("the check got the gateway %v, want none", inv.GetGateway())
	}
	if u := report.GetResult().GetUsage(); u.GetTokens() != 0 || u.GetCostMicrodollars() != 0 || u.GetSeconds() < 1 {
		t.Errorf("the usage is %v, want no tokens, no cost and the measured seconds", u)
	}
}

// TestTheGatewayIsClosedWhenTheRunnerStops: a run that ends without a
// report still closes its token.
func TestTheGatewayIsClosedWhenTheRunnerStops(t *testing.T) {
	f := newFixture(t, "stop")
	f.service.heartbeatErr = errors.New("the lease is not current")
	if _, err := RunOne(t.Context(), f.cfg); err == nil {
		t.Fatal("RunOne returned no error after the heartbeat failed")
	}
	if _, stillOpen := f.gateway.state(); stillOpen {
		t.Error("the runner left the gateway token open")
	}
}

// TestTheGatewayWritesToTheInvocationsLog: what the gateway writes to the
// log that Open gets, such as a fallback, is in the invocation's log, with
// the token masked (decisions 0232 and 0233).
func TestTheGatewayWritesToTheInvocationsLog(t *testing.T) {
	f := newFixture(t, "success")
	f.gateway.line = "pumi gateway: 2026-11-24T12:00:00Z: the call fell back with the token " + testToken + "\n"
	report := f.runOne(t)
	want := "pumi gateway: 2026-11-24T12:00:00Z: the call fell back with the token " + maskedCredential + "\n"
	if log := f.service.log(); !strings.Contains(log, want) {
		t.Errorf("the log does not hold the gateway's line %q; it is\n%s", want, log)
	}
	f.checkStoredLog(t, report)
}
