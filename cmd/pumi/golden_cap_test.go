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
	p := cancelPumi(t, "spike", caseSettings{Budget: capBudget})
	p.doSteps(addPlanA, claimStep, reportCapReachedOf("plan-a/1"))
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
	if q := dataOf(t, raised[0], &eventsv1.EscalationRaised{}).GetQuestion(); !strings.Contains(q, "wall-time limit") {
		t.Errorf("the question %q does not say that the invocation reached its wall-time limit", q)
	}
}
