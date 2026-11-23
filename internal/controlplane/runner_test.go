// SPDX-License-Identifier: Apache-2.0
// SPDX-FileCopyrightText: The Pumi Authors

package controlplane_test

import (
	"bytes"
	"testing"
	"time"

	"connectrpc.com/connect/v2"
	"connectrpc.com/connect/v2/connectinprocess"

	"getpumi.dev/pumi/api/events"
	cloudeventsv1 "getpumi.dev/pumi/api/gen/io/cloudevents/v1"
	controlplanev1 "getpumi.dev/pumi/api/gen/pumi/controlplane/v1"
	"getpumi.dev/pumi/api/gen/pumi/controlplane/v1/controlplanev1connect"
	harnessv1 "getpumi.dev/pumi/api/gen/pumi/harness/v1"
	runnerv1 "getpumi.dev/pumi/api/gen/pumi/runner/v1"
	"getpumi.dev/pumi/api/gen/pumi/runner/v1/runnerv1connect"
	"getpumi.dev/pumi/internal/controlplane"
	"getpumi.dev/pumi/internal/controlplane/embeddedlog"
)

// plane is an embedded control plane on a fake clock, with a kernel client
// and a gate runner client in-process.
type plane struct {
	svc    *controlplane.Service
	clock  *fakeClock
	kernel controlplanev1connect.ControlPlaneServiceClient
	runner runnerv1connect.RunnerServiceClient
}

// newPlane returns an embedded control plane on log with a fake clock.
func newPlane(t *testing.T, log *embeddedlog.Log) plane {
	t.Helper()
	clock := newFakeClock()
	cfg := config()
	cfg.Clock = clock
	svc, err := controlplane.New(log, cfg)
	if err != nil {
		t.Fatal(err)
	}
	dial := func(id controlplane.Identity) *connect.Client {
		server, err := svc.Server(id)
		if err != nil {
			t.Fatal(err)
		}
		return connect.NewClient(connectinprocess.New(server))
	}
	return plane{
		svc: svc, clock: clock,
		kernel: controlplanev1connect.NewControlPlaneServiceClient(dial(identity(controlplane.CallerKernel))),
		runner: runnerv1connect.NewRunnerServiceClient(dial(identity(controlplane.CallerRunner))),
	}
}

// requestCheck has the kernel request a check in the gate role named
// invocation.
func (p plane) requestCheck(t *testing.T, invocation string) {
	t.Helper()
	p.request(t, invocation, &harnessv1.Component{Kind: harnessv1.Component_KIND_CHECK, Name: "tests", Version: "1"})
}

// request has the kernel request an invocation of component in the gate
// role named invocation.
func (p plane) request(t *testing.T, invocation string, component *harnessv1.Component) {
	t.Helper()
	data, err := events.InvocationRequested("plan-a", &runnerv1.Invocation{
		Invocation: invocation, IdempotencyKey: "key-" + invocation, Role: harnessv1.Role_ROLE_GATE, Component: component,
	})
	if err != nil {
		t.Fatal(err)
	}
	e, err := events.New(data, events.Attributes{ID: "request-" + invocation, Source: "/kernel", Subject: "plan-a", Tenant: "x"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.kernel.Append(t.Context(), &controlplanev1.AppendRequest{Events: []*cloudeventsv1.CloudEvent{e}}); err != nil {
		t.Fatal(err)
	}
}

// claim claims an invocation that is ready.
func (p plane) claim(t *testing.T) *runnerv1.ClaimResponse {
	t.Helper()
	res, err := p.runner.Claim(t.Context(), &runnerv1.ClaimRequest{ProtocolVersions: []*runnerv1.ProtocolVersion{{Major: 1}}})
	if err != nil || res.GetInvocation() == nil {
		t.Fatalf("Claim = %v, %v; want an invocation", res, err)
	}
	return res
}

// head returns the head of the log.
func (p plane) head(t *testing.T) uint64 {
	t.Helper()
	res, err := p.kernel.Query(t.Context(), &controlplanev1.QueryRequest{})
	if err != nil {
		t.Fatal(err)
	}
	return res.GetHead()
}

// openLogIn opens the embedded log of the clone dir and closes it when the
// test ends.
func openLogIn(t *testing.T, dir string) *embeddedlog.Log {
	t.Helper()
	log, err := embeddedlog.Open(t.Context(), dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = log.Close() })
	return log
}

// count returns the number of events of typ in the log.
func (p plane) count(t *testing.T, typ string) int {
	t.Helper()
	res, err := p.kernel.Query(t.Context(), &controlplanev1.QueryRequest{Filter: &controlplanev1.Filter{Types: []string{typ}}})
	if err != nil {
		t.Fatal(err)
	}
	return len(res.GetEvents())
}

// TestClaimReturnsNoInvocationWhenTheWaitEnds: Claim is a long poll that
// returns no invocation, with the protocol version, when none is ready
// before the wait of the wiring ends.
func TestClaimReturnsNoInvocationWhenTheWaitEnds(t *testing.T) {
	p := newPlane(t, openLog(t))
	type claimed struct {
		res *runnerv1.ClaimResponse
		err error
	}
	done := make(chan claimed, 1)
	go func() {
		res, err := p.runner.Claim(t.Context(), &runnerv1.ClaimRequest{ProtocolVersions: []*runnerv1.ProtocolVersion{{Major: 1}}})
		done <- claimed{res, err}
	}()
	<-p.clock.added
	p.clock.Advance(controlplane.DefaultClaimWait)
	got := <-done
	if got.err != nil || got.res.GetInvocation() != nil || got.res.GetProtocolVersion().GetMajor() != 1 {
		t.Fatalf("Claim = %v, %v; want no invocation and the protocol version 1.0", got.res, got.err)
	}
}

// TestSendLogKeepsTheRecentOutputOfARunningInvocation: the control plane
// keeps the latest output of a running invocation outside the event log,
// and drops it when the invocation ends (decision 0034).
func TestSendLogKeepsTheRecentOutputOfARunningInvocation(t *testing.T) {
	p := newPlane(t, openLog(t))
	p.requestCheck(t, "invocation-1")
	res := p.claim(t)
	head := p.head(t)
	for _, line := range []string{"first\n", "second\n"} {
		if _, err := p.runner.SendLog(t.Context(), &runnerv1.SendLogRequest{
			Invocation: "invocation-1", LeaseNumber: res.GetLeaseNumber(), Output: []byte(line),
		}); err != nil {
			t.Fatal(err)
		}
	}
	if got := p.svc.RecentLog("invocation-1"); string(got) != "first\nsecond\n" {
		t.Errorf("RecentLog = %q, want both batches", got)
	}
	if n := p.head(t); n != head {
		t.Errorf("the log has %d events after the batches, want %d: the output stays out of it", n, head)
	}
	big := bytes.Repeat([]byte("x"), 1<<20)
	if _, err := p.runner.SendLog(t.Context(), &runnerv1.SendLogRequest{
		Invocation: "invocation-1", LeaseNumber: res.GetLeaseNumber(), Output: append(big, '\n'),
	}); err != nil {
		t.Fatal(err)
	}
	if got := p.svc.RecentLog("invocation-1"); len(got) != 1<<20 || got[len(got)-1] != '\n' {
		t.Errorf("RecentLog holds %d bytes, want the last 1 MiB", len(got))
	}
	p.clock.Advance(res.GetLeaseTimeout().AsDuration())
	if got := p.svc.RecentLog("invocation-1"); got != nil {
		t.Errorf("RecentLog after the lease expired = %d bytes, want none", len(got))
	}
}

// TestALeaseInTheLogExpiresAfterARestart: a control plane that starts on a
// log with a current lease sets its timer again, so the lease still
// expires when its runner is gone.
func TestALeaseInTheLogExpiresAfterARestart(t *testing.T) {
	dir := newClone(t)
	log, err := embeddedlog.Open(t.Context(), dir)
	if err != nil {
		t.Fatal(err)
	}
	first := newPlane(t, log)
	first.requestCheck(t, "invocation-1")
	res := first.claim(t)
	if err := log.Close(); err != nil {
		t.Fatal(err)
	}
	second := newPlane(t, openLogIn(t, dir))
	second.clock.Advance(res.GetLeaseTimeout().AsDuration() - time.Second)
	if n := second.count(t, events.TypeLeaseExpired); n != 0 {
		t.Fatalf("the lease expired before its timeout")
	}
	second.clock.Advance(time.Second)
	if n := second.count(t, events.TypeLeaseExpired); n != 1 {
		t.Fatalf("the log holds %d lease.expired events after the timeout, want 1", n)
	}
	if got := second.claim(t); got.GetLeaseNumber() != 2 {
		t.Fatalf("the reclaim has lease %d, want 2", got.GetLeaseNumber())
	}
}

// TestServerRefusesARunnerWithoutItsRoles: a runner's identity names the
// roles of the invocations it takes, gate or agent (decision 0024).
func TestServerRefusesARunnerWithoutItsRoles(t *testing.T) {
	svc := newService(t)
	for name, roles := range map[string][]string{
		"no role":         nil,
		"a person's role": {"maintainer"},
	} {
		id := controlplane.Identity{Caller: controlplane.CallerRunner, Principal: "r", Roles: roles, Tenant: embeddedlog.Tenant}
		if _, err := svc.Server(id); err == nil {
			t.Errorf("Server for a runner with %s succeeded, want an error", name)
		}
	}
}

// TestAnAgentRunOrExternalActionIsNotClaimedAgainAfterItsLeaseExpires: an
// agent run or an external action whose lease expired without a result may
// have run, so no runner claims it again. The kernel recovers it: an agent
// run runs again as a new invocation, and the kernel asks the adapter of an
// external action whether the action happened (decision 0191). A check is
// claimed again, as TestALeaseInTheLogExpiresAfterARestart shows.
func TestAnAgentRunOrExternalActionIsNotClaimedAgainAfterItsLeaseExpires(t *testing.T) {
	for name, kind := range map[string]harnessv1.Component_Kind{
		"agent run":       harnessv1.Component_KIND_AGENT_RUN,
		"external action": harnessv1.Component_KIND_EXTERNAL_ACTION,
	} {
		t.Run(name, func(t *testing.T) {
			p := newPlane(t, openLog(t))
			p.request(t, "invocation-1", &harnessv1.Component{Kind: kind, Name: "component", Version: "1"})
			res := p.claim(t)
			p.clock.Advance(res.GetLeaseTimeout().AsDuration())
			if n := p.count(t, events.TypeLeaseExpired); n != 1 {
				t.Fatalf("the log holds %d lease.expired events after the timeout, want 1", n)
			}
			for len(p.clock.added) > 0 {
				<-p.clock.added
			}
			type claimed struct {
				res *runnerv1.ClaimResponse
				err error
			}
			done := make(chan claimed, 1)
			go func() {
				res, err := p.runner.Claim(t.Context(), &runnerv1.ClaimRequest{ProtocolVersions: []*runnerv1.ProtocolVersion{{Major: 1}}})
				done <- claimed{res, err}
			}()
			<-p.clock.added
			p.clock.Advance(controlplane.DefaultClaimWait)
			if got := <-done; got.err != nil || got.res.GetInvocation() != nil {
				t.Fatalf("Claim = %v, %v; want no invocation: the %s's lease expired", got.res, got.err, name)
			}
			if n := p.count(t, events.TypeLeaseGranted); n != 1 {
				t.Errorf("the log holds %d lease.granted events, want 1", n)
			}
		})
	}
}
