// SPDX-License-Identifier: Apache-2.0
// SPDX-FileCopyrightText: The Pumi Authors

package kernel

import (
	"context"
	"errors"
	"testing"

	controlplanev1 "getpumi.dev/pumi/api/gen/pumi/controlplane/v1"
)

// decideRecorder is a client of the control plane that records each
// Decide request and fails it, so that the kernel goes no further.
type decideRecorder struct {
	fakeClient
	requests []*controlplanev1.DecideRequest
}

var errRecorded = errors.New("recorded")

func (d *decideRecorder) Decide(_ context.Context, req *controlplanev1.DecideRequest) (*controlplanev1.DecideResponse, error) {
	d.requests = append(d.requests, req)
	return nil, errRecorded
}

// TestADecisionNamesTheInputTreeAsTheHeadTree: the kernel's Decide names,
// beside the plan's last invocation, that invocation's input tree as the
// head tree, so that the facts keyed by tree, such as overrides and
// spec.valid, are keyed to the tree it ran on (decision 0300).
func TestADecisionNamesTheInputTreeAsTheHeadTree(t *testing.T) {
	k, r := firstWorkflowKernel(t, Project{}, treeFiles{})
	rec := &decideRecorder{}
	k.cfg.Client = rec
	result(t, k, r, "spec", `{}`)
	if _, err := r.decide(t.Context(), "spec", "cause"); err == nil {
		t.Fatal("the recorder's error did not reach the kernel")
	}
	if len(rec.requests) != 1 {
		t.Fatalf("the kernel sent %d Decide requests, want 1", len(rec.requests))
	}
	keys := rec.requests[0].GetKeys()
	if keys.GetInvocation() != r.invocation || keys.GetHeadTree() != inputTree {
		t.Errorf("the Decide keys are %v; want the invocation %s and the head tree %s", keys, r.invocation, inputTree)
	}
}
