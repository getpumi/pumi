// SPDX-License-Identifier: Apache-2.0
// SPDX-FileCopyrightText: The Pumi Authors

package main

import (
	"os"
	"path/filepath"
	"testing"

	"getpumi.dev/pumi/api/events"
	cloudeventsv1 "getpumi.dev/pumi/api/gen/io/cloudevents/v1"
	eventsv1 "getpumi.dev/pumi/api/gen/pumi/events/v1"
	runnerv1 "getpumi.dev/pumi/api/gen/pumi/runner/v1"
	"getpumi.dev/pumi/internal/controlplane/embeddedlog"
	"getpumi.dev/pumi/internal/kernel"
	"getpumi.dev/pumi/internal/kernel/workflow"
)

// cancelStep cancels plan as the person, with a cancel.requested that names
// the plan and no invocation (decision 0192).
func cancelStep(plan string) step {
	return func(p *pumi) {
		e, err := events.New(&eventsv1.CancelRequested{Plan: plan, RequestedBy: personName},
			events.Attributes{ID: "cancel-" + plan, Source: personSource, Subject: plan, Tenant: embeddedlog.Tenant})
		if err != nil {
			p.t.Fatal(err)
		}
		p.appendAsPerson(e)
	}
}

// reportCancelledOf reports the claimed invocation with the outcome
// cancelled, as the runner does after it stopped the harness on a cancel
// request (decision 0035).
func reportCancelledOf(invocation string) step {
	return func(p *pumi) {
		c, ok := p.claims[invocation]
		if !ok {
			p.t.Fatalf("the seed reports %s, which the runner did not claim", invocation)
		}
		result := &eventsv1.InvocationResult{Invocation: invocation, Usage: underCaps, Tree: "tree-0"}
		p.report(result, eventsv1.InvocationFinished_OUTCOME_CANCELLED, c.GetLeaseNumber())
	}
}

// cancelPumi starts the control plane and the kernel on the workflow of
// internal/kernel/testdata/<dir>, with the settings s.
func cancelPumi(t *testing.T, dir string, s caseSettings) *pumi {
	t.Helper()
	wf, err := workflow.Load(os.DirFS(filepath.Join(workflowsDir, dir)), "workflow.txtpb")
	if err != nil {
		t.Fatal(err)
	}
	p := newPumi(t, wf, s, nil, 0)
	p.startPlane()
	p.startKernel()
	p.waitIdle()
	return p
}

// doSteps does each step, after the kernel waits.
func (p *pumi) doSteps(steps ...step) {
	p.t.Helper()
	for _, s := range steps {
		s(p)
		p.waitIdle()
	}
}

// kernelEvents returns the events of evs that the kernel appended, of type
// typ, about plan.
func kernelEvents(evs []*cloudeventsv1.CloudEvent, typ, plan string) []*cloudeventsv1.CloudEvent {
	var out []*cloudeventsv1.CloudEvent
	for _, e := range evs {
		if e.GetSource() == kernel.Source && e.GetType() == typ && subject(e) == plan {
			out = append(out, e)
		}
	}
	return out
}

// subject returns the subject attribute of e.
func subject(e *cloudeventsv1.CloudEvent) string {
	return e.GetAttributes()["subject"].GetCeString()
}

// checkCancelled fails the test unless the last transition of plan in evs
// goes to cancelled, caused by the person's cancel of the plan.
func checkCancelled(t *testing.T, evs []*cloudeventsv1.CloudEvent, plan string) {
	t.Helper()
	fired := kernelEvents(evs, events.TypeTransitionFired, plan)
	if len(fired) == 0 {
		t.Fatalf("the kernel fired no transition of %s after the cancel", plan)
	}
	last := fired[len(fired)-1]
	if to := dataOf(t, last, &eventsv1.TransitionFired{}).GetToState(); to != "cancelled" {
		t.Errorf("the last transition of %s goes to %s, want cancelled", plan, to)
	}
	if cause := last.GetAttributes()[events.ExtensionCause].GetCeString(); cause != "cancel-"+plan {
		t.Errorf("the transition to cancelled has the cause %q, want the person's cancel %q", cause, "cancel-"+plan)
	}
}

// invocationCancels returns the invocations that the kernel's
// cancel.requested events about plan name.
func invocationCancels(t *testing.T, evs []*cloudeventsv1.CloudEvent, plan string) []*eventsv1.CancelRequested {
	t.Helper()
	var out []*eventsv1.CancelRequested
	for _, e := range kernelEvents(evs, events.TypeCancelRequested, plan) {
		out = append(out, dataOf(t, e, &eventsv1.CancelRequested{}))
	}
	return out
}

// TestAPersonCancelsARunningPlan: while the plan's agent run runs, the
// person's cancel moves the plan to cancelled, and the kernel appends
// cancel.requested for the running invocation, which the runner's next
// heartbeat carries; the runner's report of the outcome cancelled changes
// nothing more (decision 0192).
func TestAPersonCancelsARunningPlan(t *testing.T) {
	p := cancelPumi(t, "spike", caseSettings{})
	p.doSteps(addPlanA, claimStep, cancelStep(casePlan))
	evs := p.events()
	checkCancelled(t, evs, casePlan)
	cancels := invocationCancels(t, evs, casePlan)
	if len(cancels) != 1 {
		t.Fatalf("the kernel appended %d cancel requests for the plan, want 1: %v", len(cancels), cancels)
	}
	if c := cancels[0]; c.GetInvocation() != "plan-a/1" || c.GetPlan() != casePlan || c.GetRequestedBy() != personName {
		t.Errorf("the kernel's cancel request is %v, want one for plan-a/1 of %s requested by %s", c, casePlan, personName)
	}
	claim := p.claims["plan-a/1"]
	hb, err := p.runner.Heartbeat(t.Context(), &runnerv1.HeartbeatRequest{Invocation: "plan-a/1", LeaseNumber: claim.GetLeaseNumber()})
	if err != nil {
		t.Fatal(err)
	}
	if hb.GetCancel() == nil {
		t.Error("the heartbeat of plan-a/1 after the cancel carries no cancel request")
	}
	before := len(p.events())
	p.doSteps(reportCancelledOf("plan-a/1"))
	checkNothingAfterTheReport(t, p, before)
}

// TestAResultAfterTheCancelChangesNothing: a runner that finishes the
// agent run before it learns of the cancel reports it succeeded, and the
// plan stays cancelled (decision 0192).
func TestAResultAfterTheCancelChangesNothing(t *testing.T) {
	p := cancelPumi(t, "spike", caseSettings{})
	p.doSteps(addPlanA, claimStep, cancelStep(casePlan))
	before := len(p.events())
	p.doSteps(reportOf("plan-a/1", underCaps))
	checkNothingAfterTheReport(t, p, before)
	checkCancelled(t, p.events(), casePlan)
}

// checkNothingAfterTheReport fails the test unless the log holds only the
// report's invocation.result, invocation.finished and lease.released after
// its first before events.
func checkNothingAfterTheReport(t *testing.T, p *pumi, before int) {
	t.Helper()
	evs := p.events()
	want := []string{events.TypeInvocationResult, events.TypeInvocationFinished, events.TypeLeaseReleased}
	if len(evs) != before+len(want) {
		t.Fatalf("the log has %d events after the report, want %d", len(evs), before+len(want))
	}
	for i, typ := range want {
		if got := evs[before+i].GetType(); got != typ {
			t.Errorf("event %d after the report is %s, want %s", i+1, got, typ)
		}
	}
}

// TestAPersonCancelsAWaitingPlan: with parallelism 1, b1 waits while a1
// runs; the person's cancel moves b1 to cancelled, and b1 does not start
// when a1 has merged (decisions 0189 and 0192).
func TestAPersonCancelsAWaitingPlan(t *testing.T) {
	p := cancelPumi(t, "tracks", caseSettings{Parallelism: 1})
	p.doSteps(addStep("a1", "a"), addStep("b1", "b"), cancelStep("b1"))
	checkCancelled(t, p.events(), "b1")
	p.doSteps(claimStep, reportOf("a1/1", underCaps))
	evs := p.events()
	if got := kernelEvents(evs, events.TypeInvocationRequested, "b1"); len(got) != 0 {
		t.Errorf("the kernel requested %d invocations of the cancelled b1, want none", len(got))
	}
	if got := invocationCancels(t, evs, "b1"); len(got) != 0 {
		t.Errorf("the kernel appended the cancel requests %v for b1, which had no invocation", got)
	}
	checkCancelled(t, evs, "b1")
}

// TestAPersonCancelsAnEscalatedPlan: the plan waits in its escalated state,
// with no running invocation; the person's cancel moves it to cancelled,
// and the kernel asks no invocation to stop (decision 0192).
func TestAPersonCancelsAnEscalatedPlan(t *testing.T) {
	p := cancelPumi(t, "spike", caseSettings{})
	p.doSteps(addPlanA, claimStep, reportStep(overCaps), cancelStep(casePlan))
	evs := p.events()
	checkCancelled(t, evs, casePlan)
	fired := kernelEvents(evs, events.TypeTransitionFired, casePlan)
	if from := dataOf(t, fired[len(fired)-1], &eventsv1.TransitionFired{}).GetFromState(); from != "implementing-escalated" {
		t.Errorf("the plan was cancelled from %s, want implementing-escalated", from)
	}
	if got := invocationCancels(t, evs, casePlan); len(got) != 0 {
		t.Errorf("the kernel appended the cancel requests %v, but the plan's invocation had finished", got)
	}
}
