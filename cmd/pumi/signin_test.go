// SPDX-License-Identifier: Apache-2.0
// SPDX-FileCopyrightText: The Pumi Authors

package main

import (
	"strings"
	"testing"

	"getpumi.dev/pumi/api/events"
	eventsv1 "getpumi.dev/pumi/api/gen/pumi/events/v1"
	"getpumi.dev/pumi/internal/gateway"
)

// TestASignInRunTalksToTheProviderDirectly: with the adapter in the sign-in
// mode, pumi run serves no gateway, the harness calls the provider with its
// own sign-in and the model of its role, and the usage of the result is the
// runner's count and price of the figures the adapter passed (decisions
// 0240, 0241 and 0254).
func TestASignInRunTalksToTheProviderDirectly(t *testing.T) {
	c := newCommandCloneWith(t, "tracks", adapterSucceeds, testModels(t, signInMode))
	r := c.start()
	c.must("add", "--track", "a", "a1")
	c.waitState(r, "a1", "merged")
	r.stop(t)
	out := r.out.String()
	if !strings.Contains(out, "No model gateway runs: the harness of "+testAdapterName+" talks to the provider directly with the person's own sign-in.") || gatewayLine.MatchString(out) {
		t.Errorf("the output of pumi run does not say that no gateway runs; it is\n%s", out)
	}
	calls := c.provider.received()
	if len(calls) != 1 {
		t.Fatalf("the provider received %d calls, want 1", len(calls))
	}
	h := calls[0].header
	if h.Get("Authorization") != harnessSignIn || h.Get(gateway.InvocationHeader) != "" || h.Get("X-Api-Key") != "" {
		t.Errorf("the provider received Authorization %q, %s %q and X-Api-Key %q; want the harness's sign-in only",
			h.Get("Authorization"), gateway.InvocationHeader, h.Get(gateway.InvocationHeader), h.Get("X-Api-Key"))
	}
	if !strings.Contains(string(calls[0].body), `"model":"`+testModel+`"`) {
		t.Errorf("the call asked for %s, want the role's model %s", calls[0].body, testModel)
	}
	u := c.resultOf(t).GetUsage()
	if u.GetTokens() != testInputTokens+testOutputTokens || u.GetCostMicrodollars() != callCost(testOutputTokens) || u.GetSeconds() < 1 {
		t.Errorf("the usage is %v, want the runner's count of one call, %d tokens and %d microdollars, and the measured seconds",
			u, testInputTokens+testOutputTokens, callCost(testOutputTokens))
	}
}

// TestASignInRunAtItsCostCapEscalates: on the sign-in path the runner
// stops the run once the figures reach the budget's cost, reports cap
// reached with its count of every call the provider answered, and the plan
// escalates (decisions 0203 and 0241).
func TestASignInRunAtItsCostCapEscalates(t *testing.T) {
	c := newCommandCloneWith(t, "tracks", adapterCallsUntilRefused, testModels(t, signInMode))
	r := c.start()
	c.must("add", "--track", "a", "a1")
	c.waitState(r, "a1", "implementing-escalated")
	r.stop(t)
	finished := ofType(c.log(), events.TypeInvocationFinished)
	if len(finished) != 1 || dataOf(t, finished[0], &eventsv1.InvocationFinished{}).GetOutcome() != eventsv1.InvocationFinished_OUTCOME_CAP_REACHED {
		t.Fatalf("the log has the invocation.finished events %v, want one with the outcome cap reached", finished)
	}
	// $1 for each call: the fifth reaches the $5 of the budget.
	n := int64(len(c.provider.received()))
	u := c.resultOf(t).GetUsage()
	if n < 5 || u.GetTokens() != n*(testInputTokens+capCallOutputTokens) || u.GetCostMicrodollars() != n*callCost(capCallOutputTokens) {
		t.Errorf("the usage is %v after %d calls, want the runner's count of every call, at least five", u, n)
	}
	if want := testAdapterPrefix + "the runner stopped the run after call 5"; !strings.Contains(r.out.String(), want) {
		t.Errorf("the output of pumi run has no line %q; it is\n%s", want, r.out.String())
	}
}
