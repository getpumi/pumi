// SPDX-License-Identifier: Apache-2.0
// SPDX-FileCopyrightText: The Pumi Authors

package main

import (
	"testing"

	"getpumi.dev/pumi/api/events"
	cloudeventsv1 "getpumi.dev/pumi/api/gen/io/cloudevents/v1"
	eventsv1 "getpumi.dev/pumi/api/gen/pumi/events/v1"
	"getpumi.dev/pumi/internal/controlplane/embeddedlog"
)

// The adapters and models of the golden cases of the circuit breaker
// (decision 0246).
const (
	caseAdapter         = "claude-code"
	caseModel           = "claude-fable-5-2"
	caseFallbackAdapter = "codex"
	caseFallbackModel   = "astra-1"
)

// reportSwitchedOf reports the claimed invocation with the outcome and the
// switch of its model role, implementer, to its fallback, as the runner does
// when the provider's limit stopped the run on the role's own adapter and
// it ran the fallback (decision 0246). A failed invocation's result holds
// only the usage.
func reportSwitchedOf(invocation string, outcome eventsv1.InvocationFinished_Outcome) step {
	return func(p *pumi) {
		c, ok := p.claims[invocation]
		if !ok {
			p.t.Fatalf("the seed reports %s, which the runner did not claim", invocation)
		}
		switched, err := events.New(&eventsv1.ModelRoleSwitched{
			Role: "implementer", Adapter: caseFallbackAdapter, Model: caseFallbackModel, Invocation: invocation,
			Limit: &eventsv1.ModelRoleSwitched_Limit{Adapter: caseAdapter, Model: caseModel},
		}, events.Attributes{ID: invocation + "/model-role-switched", Source: "/runner", Subject: "implementer", Tenant: embeddedlog.Tenant})
		if err != nil {
			p.t.Fatal(err)
		}
		result := &eventsv1.InvocationResult{Invocation: invocation, Usage: underCaps}
		if outcome == eventsv1.InvocationFinished_OUTCOME_SUCCEEDED {
			result.Tree, result.Output = "tree-1", `{"summary":"Implemented the plan."}`
		}
		p.report(result, outcome, c.GetLeaseNumber(), switched)
	}
}

// resetStep switches the model role back as the person, to the adapter and
// model that the limit stopped (decision 0246).
func resetStep(role string) step {
	return func(p *pumi) {
		e, err := events.New(&eventsv1.ModelRoleSwitched{
			Role: role, Adapter: caseAdapter, Model: caseModel, SwitchedBy: personName,
			Limit: &eventsv1.ModelRoleSwitched_Limit{Adapter: caseAdapter, Model: caseModel},
		}, events.Attributes{ID: "reset-" + role, Source: personSource, Subject: role, Tenant: embeddedlog.Tenant})
		if err != nil {
			p.t.Fatal(err)
		}
		p.appendAsPerson(e)
	}
}

// switchedClaims returns, for each of the invocations, whether its claim
// said that its model role is on its fallback.
func switchedClaims(t *testing.T, p *pumi, invocations ...string) map[string]bool {
	t.Helper()
	out := map[string]bool{}
	for _, inv := range invocations {
		c, ok := p.claims[inv]
		if !ok {
			t.Fatalf("the runner did not claim %s", inv)
		}
		out[inv] = c.GetModelRoleSwitched()
	}
	return out
}

// TestAFallbackSwitchesTheRoleForTheNextRun: plan-a/1 fails with the
// switch of its model role, and plan-a/2, which runs again under the
// in-doubt attempts, is claimed on the fallback; the kernel does not
// handle the switch, which changes only which adapter the runner starts
// (decisions 0235 and 0246).
func TestAFallbackSwitchesTheRoleForTheNextRun(t *testing.T) {
	p := cancelPumi(t, "spike", failedSettings)
	p.doSteps(addPlanA, claimStep, reportSwitchedOf("plan-a/1", eventsv1.InvocationFinished_OUTCOME_FAILED), claimStep)
	if got := switchedClaims(t, p, "plan-a/1", "plan-a/2"); got["plan-a/1"] || !got["plan-a/2"] {
		t.Errorf("the claims say the role is switched: %v, want only for plan-a/2", got)
	}
	evs := p.events()
	if n := len(ofType(evs, events.TypeModelRoleSwitched)); n != 1 {
		t.Errorf("the log holds %d switches, want 1", n)
	}
	checkSwitchBeforeFinished(t, evs)
}

// checkSwitchBeforeFinished fails the test unless the switch is between
// the result and the end of its invocation, as the Report writes them.
func checkSwitchBeforeFinished(t *testing.T, evs []*cloudeventsv1.CloudEvent) {
	t.Helper()
	for i, e := range evs {
		if e.GetType() != events.TypeModelRoleSwitched {
			continue
		}
		if i == 0 || i+1 >= len(evs) || evs[i-1].GetType() != events.TypeInvocationResult || evs[i+1].GetType() != events.TypeInvocationFinished {
			t.Errorf("the switch at line %d is not between a result and the end of its invocation", i+1)
		}
	}
}
