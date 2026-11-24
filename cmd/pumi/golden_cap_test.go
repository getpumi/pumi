// SPDX-License-Identifier: Apache-2.0
// SPDX-FileCopyrightText: The Pumi Authors

package main

import (
	"strings"
	"testing"

	"getpumi.dev/pumi/api/events"
	eventsv1 "getpumi.dev/pumi/api/gen/pumi/events/v1"
	harnessv1 "getpumi.dev/pumi/api/gen/pumi/harness/v1"
)

// capBudget is the budget of the cases in which an agent run reaches its
// wall-time limit.
var capBudget = &harnessv1.Usage{Tokens: 200_000, CostMicrodollars: 3_000_000, Seconds: 1_200}

// reportCapReachedOf reports the claimed invocation with the outcome cap
// reached, as the runner does after it stopped the harness at the wall-time
// limit of its budget: the adapter was killed before it wrote a result, so
// the usage is the measured seconds and the tokens and cost of the budget
// (decisions 0193 and 0204).
func reportCapReachedOf(invocation string) step {
	return func(p *pumi) {
		c, ok := p.claims[invocation]
		if !ok {
			p.t.Fatalf("the seed reports %s, which the runner did not claim", invocation)
		}
		b := c.GetInvocation().GetBudget()
		result := &eventsv1.InvocationResult{Invocation: invocation, Usage: &eventsv1.InvocationResult_Usage{
			Tokens: b.GetTokens(), CostMicrodollars: b.GetCostMicrodollars(), Seconds: b.GetSeconds(),
		}}
		p.report(result, eventsv1.InvocationFinished_OUTCOME_CAP_REACHED, c.GetLeaseNumber())
	}
}

// TestAnAgentRunAtItsWallTimeLimitEscalates: the runner reports the agent
// run with the outcome cap reached, which fires the state's cap_reached
// transition, not its result transitions, so the plan escalates instead
// of advancing, although the usage is under the hard caps (decision 0203).
func TestAnAgentRunAtItsWallTimeLimitEscalates(t *testing.T) {
	checkCapEscalation(t, reportCapReachedOf("plan-a/1"), "wall-time limit")
}

// checkCapEscalation runs an agent run that report reports, and fails the
// test unless the plan escalates on cap_reached with a question that holds
// want.
func checkCapEscalation(t *testing.T, report step, want string) {
	t.Helper()
	p := cancelPumi(t, "spike", caseSettings{Budget: capBudget})
	p.doSteps(addPlanA, claimStep, report)
	evs := p.events()
	fired := kernelEvents(evs, events.TypeTransitionFired, casePlan)
	if len(fired) == 0 {
		t.Fatal("the kernel fired no transition after the report")
	}
	last := dataOf(t, fired[len(fired)-1], &eventsv1.TransitionFired{})
	if last.GetToState() != "implementing-escalated" || last.GetTrigger() != "cap-reached" {
		t.Errorf("the last transition is %v, want one to implementing-escalated on cap-reached", last)
	}
	raised := kernelEvents(evs, events.TypeEscalationRaised, casePlan)
	if len(raised) != 1 {
		t.Fatalf("the kernel raised %d escalations, want 1", len(raised))
	}
	if q := dataOf(t, raised[0], &eventsv1.EscalationRaised{}).GetQuestion(); !strings.Contains(q, want) {
		t.Errorf("the question %q does not hold %q", q, want)
	}
}

// tokenCapOvershoot is how far the usage of the case token-cap goes over
// the budget's tokens: the tokens of the call in progress when the
// invocation reached its cap, which the gateway let finish (decision 0228).
const tokenCapOvershoot = 12_345

// reportTokenCapOf reports the claimed invocation with the outcome cap
// reached, as the runner does when the model gateway refused a call of the
// invocation at its token cap: the usage is the gateway's totals, over the
// budget's tokens by one call, with the measured seconds, under the
// budget's (decisions 0228 and 0230).
func reportTokenCapOf(invocation string) step {
	return func(p *pumi) {
		c, ok := p.claims[invocation]
		if !ok {
			p.t.Fatalf("the seed reports %s, which the runner did not claim", invocation)
		}
		b := c.GetInvocation().GetBudget()
		result := &eventsv1.InvocationResult{Invocation: invocation, Usage: &eventsv1.InvocationResult_Usage{
			Tokens: b.GetTokens() + tokenCapOvershoot, CostMicrodollars: b.GetCostMicrodollars() / 2, Seconds: b.GetSeconds() / 4,
		}}
		p.report(result, eventsv1.InvocationFinished_OUTCOME_CAP_REACHED, c.GetLeaseNumber())
	}
}

// TestAnAgentRunAtItsTokenCapEscalates: the runner reports the agent run
// with the outcome cap reached after the gateway refused a call at its
// token cap, which fires the state's cap_reached transition, so the plan
// escalates, and the question names the token or cost cap (decisions 0203
// and 0228).
func TestAnAgentRunAtItsTokenCapEscalates(t *testing.T) {
	checkCapEscalation(t, reportTokenCapOf("plan-a/1"), "token or cost cap")
}
