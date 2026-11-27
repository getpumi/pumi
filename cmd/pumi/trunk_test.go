// SPDX-License-Identifier: Apache-2.0
// SPDX-FileCopyrightText: The Pumi Authors

package main

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"

	eventsv1 "getpumi.dev/pumi/api/gen/pumi/events/v1"
	"getpumi.dev/pumi/internal/kernel"
	"getpumi.dev/pumi/internal/kernel/workflow"
)

// movingTrunk is a trunk whose tree the test moves. Its commit is the
// tree's name with "commit-of-" before it.
type movingTrunk struct {
	mu   sync.Mutex
	tree string
}

func (tt *movingTrunk) set(tree string) {
	tt.mu.Lock()
	defer tt.mu.Unlock()
	tt.tree = tree
}

func (tt *movingTrunk) read(context.Context) (kernel.TrunkHead, error) {
	tt.mu.Lock()
	defer tt.mu.Unlock()
	return kernel.TrunkHead{Tree: tt.tree, Commit: "commit-of-" + tt.tree}, nil
}

// treeOf returns the input tree of the claimed invocation.
func (p *pumi) treeOf(invocation string) string {
	p.t.Helper()
	c, ok := p.claims[invocation]
	if !ok {
		p.t.Fatalf("the runner did not claim %s", invocation)
	}
	return c.GetInvocation().GetInput().GetTree()
}

// TestAPlanStartsFromTheTrunksTree: the kernel reads the trunk's tree when
// it starts a plan, so a2, which starts after a1 merged, starts from the
// trunk as it is then; a2's invocation in doubt runs again from the tree a2
// started from, also after a restart (decision 0209).
func TestAPlanStartsFromTheTrunksTree(t *testing.T) {
	wf, err := workflow.Load(os.DirFS(filepath.Join(workflowsDir, "tracks")), "workflow.txtpb")
	if err != nil {
		t.Fatal(err)
	}
	trunk := &movingTrunk{tree: "trunk-1"}
	p := newPumi(t, wf, caseSettings{Parallelism: 1, MaxAttempts: map[string]int64{"in-doubt": 1}}, nil, 0)
	p.trunk = trunk.read
	p.startPlane()
	p.startKernel()
	addStep("a1", "a")(p)
	addStep("a2", "a", "a1")(p)
	p.waitIdle()
	p.claim()
	if got := p.treeOf("a1/1"); got != "trunk-1" {
		t.Errorf("a1/1 starts from %q, want the trunk's tree trunk-1", got)
	}
	trunk.set("trunk-2")
	reportOf("a1/1", underCaps)(p)
	p.waitIdle()
	p.claim()
	if got := p.treeOf("a2/1"); got != "trunk-2" {
		t.Errorf("a2/1 starts from %q, want trunk-2, the trunk's tree when a2 started", got)
	}
	p.restart()
	p.waitIdle()
	trunk.set("trunk-3")
	p.expire()
	p.waitIdle()
	p.claim()
	if got := p.treeOf("a2/2"); got != "trunk-2" {
		t.Errorf("a2/2, which runs a2/1 again, starts from %q, want trunk-2, the tree a2 started from", got)
	}
}

// TestTheInputNamesThePlansBaseTree: every invocation of a plan names the
// trunk's tree when the plan started as its base tree, also after the
// trunk moves (decision 0285).
func TestTheInputNamesThePlansBaseTree(t *testing.T) {
	wf, err := workflow.Load(os.DirFS(filepath.Join(workflowsDir, "tracks")), "workflow.txtpb")
	if err != nil {
		t.Fatal(err)
	}
	trunk := &movingTrunk{tree: "trunk-1"}
	p := newPumi(t, wf, caseSettings{Parallelism: 1, MaxAttempts: map[string]int64{"in-doubt": 1}}, nil, 0)
	p.trunk = trunk.read
	p.startPlane()
	p.startKernel()
	addStep("a1", "a")(p)
	p.waitIdle()
	p.claim()
	trunk.set("trunk-2")
	p.expire()
	p.waitIdle()
	p.claim()
	for _, invocation := range []string{"a1/1", "a1/2"} {
		if got := p.claims[invocation].GetInvocation().GetInput().GetBaseTree(); got != "trunk-1" {
			t.Errorf("%s has the base tree %q, want trunk-1, the trunk's tree when a1 started", invocation, got)
		}
	}
}

// TestWithNoTrunkThereIsNoBaseTree: when the wiring gives no trunk, an
// invocation names no base tree (decision 0285).
func TestWithNoTrunkThereIsNoBaseTree(t *testing.T) {
	wf, err := workflow.Load(os.DirFS(filepath.Join(workflowsDir, "tracks")), "workflow.txtpb")
	if err != nil {
		t.Fatal(err)
	}
	p := newPumi(t, wf, caseSettings{Parallelism: 1}, nil, 0)
	p.startPlane()
	p.startKernel()
	addStep("a1", "a")(p)
	p.waitIdle()
	p.claim()
	if got := p.claims["a1/1"].GetInvocation().GetInput().GetBaseTree(); got != "" {
		t.Errorf("a1/1 has the base tree %q, want none", got)
	}
}

// TestTheInputNamesThePlansBaseCommit: every invocation of a plan names
// the trunk's commit when the plan started as its base commit, also after
// the trunk moves and after a restart (decisions 0313 and 0316).
func TestTheInputNamesThePlansBaseCommit(t *testing.T) {
	wf, err := workflow.Load(os.DirFS(filepath.Join(workflowsDir, "tracks")), "workflow.txtpb")
	if err != nil {
		t.Fatal(err)
	}
	trunk := &movingTrunk{tree: "trunk-1"}
	p := newPumi(t, wf, caseSettings{Parallelism: 1, MaxAttempts: map[string]int64{"in-doubt": 2}}, nil, 0)
	p.trunk = trunk.read
	p.startPlane()
	p.startKernel()
	addStep("a1", "a")(p)
	p.waitIdle()
	p.claim()
	trunk.set("trunk-2")
	p.expire()
	p.waitIdle()
	p.claim()
	p.restart()
	p.waitIdle()
	p.expire()
	p.waitIdle()
	p.claim()
	for _, invocation := range []string{"a1/1", "a1/2", "a1/3"} {
		if got := p.claims[invocation].GetInvocation().GetBaseCommit(); got != "commit-of-trunk-1" {
			t.Errorf("%s has the base commit %q, want commit-of-trunk-1, the trunk's commit when a1 started", invocation, got)
		}
	}
}

// TestALaterInvocationStartsFromThePlansLastResultTree: the archive step
// starts from the tree that implementing's result carries, and the next
// invocation from the archive step's tree, while the base tree stays the
// trunk's (decision 0315).
func TestALaterInvocationStartsFromThePlansLastResultTree(t *testing.T) {
	wf, err := workflow.Load(os.DirFS(filepath.Join(workflowsDir, "archive")), "workflow.txtpb")
	if err != nil {
		t.Fatal(err)
	}
	trunk := &movingTrunk{tree: "trunk-1"}
	p := newPumi(t, wf, caseSettings{ArchiveStep: "ARCHIVE_STEP_AFTER_IMPLEMENTING"}, nil, 0)
	p.trunk = trunk.read
	p.startPlane()
	p.startKernel()
	for _, s := range []step{addPlanA, claimStep, reportOf("plan-a/1", underCaps), claimStep, reportTreeOf("plan-a/2"), claimStep} {
		s(p)
		p.waitIdle()
	}
	for invocation, want := range map[string]string{"plan-a/1": "trunk-1", "plan-a/2": "tree-1", "plan-a/3": "tree-2"} {
		if got := p.treeOf(invocation); got != want {
			t.Errorf("%s starts from %q, want %s", invocation, got, want)
		}
		if got := p.claims[invocation].GetInvocation().GetInput().GetBaseTree(); got != "trunk-1" {
			t.Errorf("%s has the base tree %q, want trunk-1", invocation, got)
		}
	}
}

// TestAFailedResultsTreeIsNotThePlansTree: a retry after a failed
// invocation starts from the tree the failed one started from, not from
// the tree of its result, because only a result that fires the result
// trigger moves the plan's tree (decisions 0235 and 0315).
func TestAFailedResultsTreeIsNotThePlansTree(t *testing.T) {
	wf, err := workflow.Load(os.DirFS(filepath.Join(workflowsDir, "tracks")), "workflow.txtpb")
	if err != nil {
		t.Fatal(err)
	}
	trunk := &movingTrunk{tree: "trunk-1"}
	p := newPumi(t, wf, caseSettings{Parallelism: 1, MaxAttempts: map[string]int64{"in-doubt": 2}}, nil, 0)
	p.trunk = trunk.read
	p.startPlane()
	p.startKernel()
	addStep("a1", "a")(p)
	p.waitIdle()
	p.claim()
	p.report(&eventsv1.InvocationResult{Invocation: "a1/1", Usage: underCaps, Tree: "tree-failed"},
		eventsv1.InvocationFinished_OUTCOME_FAILED, p.claims["a1/1"].GetLeaseNumber())
	p.waitIdle()
	p.claim()
	if got := p.treeOf("a1/2"); got != "trunk-1" {
		t.Errorf("a1/2, the retry of a failed a1/1, starts from %q, want trunk-1", got)
	}
}
