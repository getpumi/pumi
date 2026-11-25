// SPDX-License-Identifier: Apache-2.0
// SPDX-FileCopyrightText: The Pumi Authors

package main

import (
	"strings"
	"testing"

	"getpumi.dev/pumi/api/events"
	cloudeventsv1 "getpumi.dev/pumi/api/gen/io/cloudevents/v1"
	eventsv1 "getpumi.dev/pumi/api/gen/pumi/events/v1"
	harnessv1 "getpumi.dev/pumi/api/gen/pumi/harness/v1"
)

// failedSettings are the settings of the cases in which an agent run
// fails: one run again, counted with the in-doubt attempts (decisions 0235
// and 0237).
var failedSettings = caseSettings{MaxAttempts: map[string]int64{"in-doubt": 1}}

// reportFailedOf reports the claimed invocation with the outcome failed, as
// the runner does when the adapter's result says that the run failed. A
// failed invocation's result holds at most the usage.
func reportFailedOf(invocation string) step {
	return func(p *pumi) {
		c, ok := p.claims[invocation]
		if !ok {
			p.t.Fatalf("the seed reports %s, which the runner did not claim", invocation)
		}
		result := &eventsv1.InvocationResult{Invocation: invocation, Usage: underCaps}
		p.report(result, eventsv1.InvocationFinished_OUTCOME_FAILED, c.GetLeaseNumber())
	}
}

// reportFailedWithRefusalsOf reports the claimed invocation with the
// outcome failed and the refusals the harness reported, as the runner does
// when the adapter's result has refusals (decision 0249).
func reportFailedWithRefusalsOf(invocation string) step {
	return func(p *pumi) {
		c, ok := p.claims[invocation]
		if !ok {
			p.t.Fatalf("the seed reports %s, which the runner did not claim", invocation)
		}
		result := &eventsv1.InvocationResult{Invocation: invocation, Usage: underCaps, Refusals: []*harnessv1.Refusal{
			{Tool: harnessv1.Capabilities_TOOL_KIND_EDIT, Asked: &harnessv1.Refusal_Path{Path: "README.md"}},
			{Tool: harnessv1.Capabilities_TOOL_KIND_RUN_COMMANDS, Asked: &harnessv1.Refusal_Command{Command: "GOFLAGS=-x go test ./..."}},
			{Tool: harnessv1.Capabilities_TOOL_KIND_WEB, Asked: &harnessv1.Refusal_Host{Host: "example.com"}},
		}}
		p.report(result, eventsv1.InvocationFinished_OUTCOME_FAILED, c.GetLeaseNumber())
	}
}

// lastFired returns the data of the kernel's last transition.fired of the
// case's plan in evs.
func lastFired(t *testing.T, evs []*cloudeventsv1.CloudEvent) *eventsv1.TransitionFired {
	t.Helper()
	fired := kernelEvents(evs, events.TypeTransitionFired, casePlan)
	if len(fired) == 0 {
		t.Fatal("the kernel fired no transition")
	}
	return dataOf(t, fired[len(fired)-1], &eventsv1.TransitionFired{})
}

// TestAFailedAgentRunRunsAgain: the runner reports the agent run as
// failed, with the usage under the hard caps. The state's failed
// transitions fire, not its result transitions, so the plan does not
// advance to done; retry is allowed, and the run is requested again as a
// new invocation (decision 0235).
func TestAFailedAgentRunRunsAgain(t *testing.T) {
	p := cancelPumi(t, "spike", failedSettings)
	p.doSteps(addPlanA, claimStep, reportFailedOf("plan-a/1"))
	evs := p.events()
	last := lastFired(t, evs)
	if last.GetFromState() != "implementing" || last.GetToState() != "implementing" || last.GetTrigger() != "failed" {
		t.Errorf("the last transition is %v, want one from implementing back to implementing on failed", last)
	}
	requested := kernelEvents(evs, events.TypeInvocationRequested, casePlan)
	if len(requested) != 2 {
		t.Fatalf("the kernel requested %d invocations, want 2", len(requested))
	}
	if inv := dataOf(t, requested[1], &eventsv1.InvocationRequested{}).GetInvocation(); inv != "plan-a/2" {
		t.Errorf("the second invocation is %q, want plan-a/2", inv)
	}
}

// TestAFailedAgentRunWhoseRetryIsDeniedEscalates: the run again fails as
// well. The retry has no attempts left, so it is denied, and the plan
// escalates with a question that says the run failed (decisions 0169 and
// 0235).
func TestAFailedAgentRunWhoseRetryIsDeniedEscalates(t *testing.T) {
	p := cancelPumi(t, "spike", failedSettings)
	p.doSteps(addPlanA, claimStep, reportFailedOf("plan-a/1"), claimStep, reportFailedOf("plan-a/2"))
	evs := p.events()
	last := lastFired(t, evs)
	if last.GetToState() != "implementing-escalated" || last.GetTrigger() != "failed" {
		t.Errorf("the last transition is %v, want one to implementing-escalated on failed", last)
	}
	raised := kernelEvents(evs, events.TypeEscalationRaised, casePlan)
	if len(raised) != 1 {
		t.Fatalf("the kernel raised %d escalations, want 1", len(raised))
	}
	if q := dataOf(t, raised[0], &eventsv1.EscalationRaised{}).GetQuestion(); !strings.Contains(q, "plan-a/2") || !strings.Contains(q, "failed") || !strings.Contains(q, "retry denied") {
		t.Errorf("the question %q does not say that plan-a/2 failed and that retry was denied", q)
	}
}

// TestTheReasonOfAnEscalationNamesTheRefusals: the run again fails with
// refusals that the harness reported, and the question of the escalation
// names each of them (decision 0249). The refusals of the first run, which
// is not the one that escalates, are not named.
func TestTheReasonOfAnEscalationNamesTheRefusals(t *testing.T) {
	p := cancelPumi(t, "spike", failedSettings)
	p.doSteps(addPlanA, claimStep, reportFailedWithRefusalsOf("plan-a/1"), claimStep, reportFailedOf("plan-a/2"))
	raised := kernelEvents(p.events(), events.TypeEscalationRaised, casePlan)
	if len(raised) != 1 {
		t.Fatalf("the kernel raised %d escalations, want 1", len(raised))
	}
	if q := dataOf(t, raised[0], &eventsv1.EscalationRaised{}).GetQuestion(); strings.Contains(q, "refused") {
		t.Errorf("the question %q names refusals, but plan-a/2 had none", q)
	}

	p = cancelPumi(t, "spike", failedSettings)
	p.doSteps(addPlanA, claimStep, reportFailedOf("plan-a/1"), claimStep, reportFailedWithRefusalsOf("plan-a/2"))
	raised = kernelEvents(p.events(), events.TypeEscalationRaised, casePlan)
	if len(raised) != 1 {
		t.Fatalf("the kernel raised %d escalations, want 1", len(raised))
	}
	q := dataOf(t, raised[0], &eventsv1.EscalationRaised{}).GetQuestion()
	want := "The harness refused: editing the path README.md; running the command GOFLAGS=-x go test ./...; using the web at the host example.com (decision 0249)."
	if !strings.Contains(q, "plan-a/2") || !strings.Contains(q, want) {
		t.Errorf("the question %q does not name the refusals of plan-a/2 as %q", q, want)
	}
}
