// SPDX-License-Identifier: Apache-2.0
// SPDX-FileCopyrightText: The Pumi Authors

package main

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"getpumi.dev/pumi/internal/kernel/workflow"
)

// movingTrunk is a trunk whose tree the test moves.
type movingTrunk struct {
	mu   sync.Mutex
	tree string
}

func (tt *movingTrunk) set(tree string) {
	tt.mu.Lock()
	defer tt.mu.Unlock()
	tt.tree = tree
}

func (tt *movingTrunk) read(context.Context) (string, error) {
	tt.mu.Lock()
	defer tt.mu.Unlock()
	return tt.tree, nil
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
