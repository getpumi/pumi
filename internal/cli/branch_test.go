// SPDX-License-Identifier: Apache-2.0
// SPDX-FileCopyrightText: The Pumi Authors

package cli

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"testing"

	"getpumi.dev/pumi/api/events"
	cloudeventsv1 "getpumi.dev/pumi/api/gen/io/cloudevents/v1"
	controlplanev1 "getpumi.dev/pumi/api/gen/pumi/controlplane/v1"
	"getpumi.dev/pumi/api/gen/pumi/controlplane/v1/controlplanev1connect"
	eventsv1 "getpumi.dev/pumi/api/gen/pumi/events/v1"
	harnessv1 "getpumi.dev/pumi/api/gen/pumi/harness/v1"
	runnerv1 "getpumi.dev/pumi/api/gen/pumi/runner/v1"
)

// The tests of pumi branch read a log that a fake control plane holds, and
// record the branch that the wiring would write (decisions 0289 and 0314).

// fakeLog is a control plane whose Query returns its events. It has no
// other method.
type fakeLog struct {
	controlplanev1connect.ControlPlaneServiceClient
	t       *testing.T
	entries []*controlplanev1.Entry
}

func (f *fakeLog) Query(_ context.Context, req *controlplanev1.QueryRequest) (*controlplanev1.QueryResponse, error) {
	types := map[string]bool{}
	for _, typ := range req.GetFilter().GetTypes() {
		types[typ] = true
	}
	res := &controlplanev1.QueryResponse{}
	for _, e := range f.entries {
		if e.GetPosition() > req.GetAfter() && (len(types) == 0 || types[e.GetEvent().GetType()]) {
			res.Events = append(res.Events, e)
		}
	}
	return res, nil
}

// add appends the event e, which events.New returned with err, to the log.
func (f *fakeLog) add(e *cloudeventsv1.CloudEvent, err error) {
	f.t.Helper()
	if err != nil {
		f.t.Fatal(err)
	}
	f.entries = append(f.entries, &controlplanev1.Entry{Position: uint64(len(f.entries) + 1), Event: e})
}

// next returns the attributes of the log's next event.
func (f *fakeLog) next() events.Attributes {
	return events.Attributes{ID: fmt.Sprintf("event-%d", len(f.entries)+1), Source: "/test", Tenant: "local"}
}

// requested returns the invocation.requested of the invocation inv of plan,
// with the base tree and commit of the plan.
func requested(t *testing.T, plan, inv, baseTree, baseCommit string) *eventsv1.InvocationRequested {
	t.Helper()
	req, err := events.InvocationRequested(plan, &runnerv1.Invocation{
		Invocation: inv, IdempotencyKey: inv, Role: harnessv1.Role_ROLE_AGENT,
		Component: &harnessv1.Component{Kind: harnessv1.Component_KIND_AGENT_RUN},
		Input:     &harnessv1.Input{Tree: baseTree, BaseTree: baseTree}, BaseCommit: baseCommit,
	})
	if err != nil {
		t.Fatal(err)
	}
	return req
}

// branchRun runs pumi branch with args on log, and returns the branch that
// the wiring was asked to write, its output and its error.
func branchRun(t *testing.T, log *fakeLog, args ...string) (*Branch, string, error) {
	t.Helper()
	var wrote *Branch
	var out, stderr bytes.Buffer
	err := Main(t.Context(), append([]string{"branch"}, args...), Env{
		Stdout: &out, Stderr: &stderr,
		Connect: func(context.Context, string) (*Session, error) {
			return &Session{Client: log, Person: "person@example.com", Close: func() error { return nil }}, nil
		},
		Branch: func(_ context.Context, b Branch) (string, error) {
			wrote = &b
			return "commit-1", nil
		},
	})
	return wrote, out.String() + stderr.String(), err
}

// planLog is the log of the plan p of the track t: p/1 succeeded with the
// tree tree-1, p/2 failed with the tree tree-2, and p/3 succeeded with no
// tree, as a check does.
func planLog(t *testing.T, baseCommit string) *fakeLog {
	t.Helper()
	ok, failed := eventsv1.InvocationFinished_OUTCOME_SUCCEEDED, eventsv1.InvocationFinished_OUTCOME_FAILED
	f := &fakeLog{t: t}
	f.add(events.New(&eventsv1.PlanAdded{Plan: "p", Track: "t"}, f.next()))
	for _, inv := range []struct {
		name, tree string
		outcome    eventsv1.InvocationFinished_Outcome
	}{{"p/1", "tree-1", ok}, {"p/2", "tree-2", failed}, {"p/3", "", ok}} {
		f.add(events.New(requested(t, "p", inv.name, "tree-0", baseCommit), f.next()))
		f.add(events.New(&eventsv1.InvocationResult{Invocation: inv.name, Tree: inv.tree}, f.next()))
		f.add(events.New(&eventsv1.InvocationFinished{Invocation: inv.name, Outcome: inv.outcome}, f.next()))
	}
	return f
}

// oneRun is the log of the plan q, whose one invocation q/1 succeeded with
// the result tree tree, and whose base commit is baseCommit.
func oneRun(t *testing.T, tree, baseCommit string) *fakeLog {
	t.Helper()
	f := &fakeLog{t: t}
	f.add(events.New(&eventsv1.PlanAdded{Plan: "q", Track: "t"}, f.next()))
	f.add(events.New(requested(t, "q", "q/1", "tree-0", baseCommit), f.next()))
	f.add(events.New(&eventsv1.InvocationResult{Invocation: "q/1", Tree: tree}, f.next()))
	f.add(events.New(&eventsv1.InvocationFinished{Invocation: "q/1", Outcome: eventsv1.InvocationFinished_OUTCOME_SUCCEEDED}, f.next()))
	return f
}

// TestBranchWritesThePlansLastTree: pumi branch asks the wiring for a
// commit of the newest tree that a succeeded result of the plan carries,
// with the plan's base commit as its parent and a message that names the
// plan, its track and the invocation (decisions 0313 to 0315).
func TestBranchWritesThePlansLastTree(t *testing.T) {
	b, out, err := branchRun(t, planLog(t, "commit-0"), "p", "pumi/p")
	if err != nil {
		t.Fatalf("pumi branch p pumi/p: %v\n%s", err, out)
	}
	want := Branch{Name: "pumi/p", Tree: "tree-1", Parent: "commit-0", Message: "Plan p\n\nTrack: t\nInvocation: p/1\n"}
	if b == nil || *b != want {
		t.Fatalf("pumi branch asked for %+v, want %+v", b, want)
	}
	for _, s := range []string{"p", "pumi/p", "commit-1", "p/1"} {
		if !strings.Contains(out, s) {
			t.Errorf("pumi branch wrote %q, want a line that names %s", out, s)
		}
	}
}

// TestBranchRefuses: pumi branch refuses a plan that the log does not
// hold, a plan with no result tree and a plan with no base commit, each
// with an error that says which, and writes no branch (decisions 0289 and
// 0313).
func TestBranchRefuses(t *testing.T) {
	for name, tc := range map[string]struct {
		log  *fakeLog
		want string
	}{
		"a plan the log does not hold": {log: planLog(t, "commit-0"), want: "the log has no plan q"},
		"a plan with no result tree":   {log: oneRun(t, "", "commit-0"), want: "the plan q has no result tree"},
		"a plan with no base commit":   {log: oneRun(t, "tree-1", ""), want: "the log records no base commit for the plan q"},
	} {
		t.Run(name, func(t *testing.T) {
			b, out, err := branchRun(t, tc.log, "q", "pumi/q")
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("pumi branch q pumi/q = %v, want an error that says %q\n%s", err, tc.want, out)
			}
			if b != nil {
				t.Errorf("pumi branch asked for %+v, want no branch", b)
			}
		})
	}
}

// TestBranchTakesAPlanAndABranch: pumi branch takes two arguments.
func TestBranchTakesAPlanAndABranch(t *testing.T) {
	if _, out, err := branchRun(t, planLog(t, "commit-0"), "p"); err == nil {
		t.Errorf("pumi branch p succeeded, want an error for the missing branch\n%s", out)
	}
}
