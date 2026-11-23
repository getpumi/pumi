// SPDX-License-Identifier: Apache-2.0
// SPDX-FileCopyrightText: The Pumi Authors

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"getpumi.dev/pumi/api/events"
	eventsv1 "getpumi.dev/pumi/api/gen/pumi/events/v1"
	runnerv1 "getpumi.dev/pumi/api/gen/pumi/runner/v1"
	"getpumi.dev/pumi/internal/kernel/workflow"
)

// TestALateResultOfTheLostRunIsRefused: after the kernel ran an agent run
// in doubt again as a new invocation, a report of the lost run under its
// old lease number is refused, and the log does not change (decisions 0035
// and 0191).
func TestALateResultOfTheLostRunIsRefused(t *testing.T) {
	wf, err := workflow.Load(os.DirFS(filepath.Join(workflowsDir, "recovery")), "workflow.txtpb")
	if err != nil {
		t.Fatal(err)
	}
	p := newPumi(t, wf, readSettings(t, filepath.Join(goldenDir, "recovery", "agent-run-runs-again")), nil, 0)
	p.startPlane()
	p.startKernel()
	p.waitIdle()
	addPlanA(p)
	p.waitIdle()
	p.claim()
	p.expire()
	p.waitIdle()
	before := p.events()
	last := before[len(before)-1]
	if last.GetType() != events.TypeInvocationRequested {
		t.Fatalf("the last event after the expiry is %s, want the invocation.requested of the run again", last.GetType())
	}
	lost := p.claims["plan-a/1"]
	_, err = p.runner.Report(t.Context(), &runnerv1.ReportRequest{
		Invocation: "plan-a/1", LeaseNumber: lost.GetLeaseNumber(), Outcome: eventsv1.InvocationFinished_OUTCOME_SUCCEEDED,
		Result: &eventsv1.InvocationResult{Invocation: "plan-a/1", Usage: underCaps, Tree: "tree-1", Output: `{"summary":"Late."}`},
	})
	// cmd/pumi may not import connect (decision 0064), so the code is read
	// from the error's text, which Connect starts with the code.
	if err == nil || !strings.HasPrefix(err.Error(), "aborted: ") {
		t.Errorf("the late Report of the lost run: %v, want the code aborted", err)
	}
	if got := len(p.events()); got != len(before) {
		t.Errorf("the log has %d events after the late report, want %d", got, len(before))
	}
}
