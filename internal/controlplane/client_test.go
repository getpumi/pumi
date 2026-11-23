// SPDX-License-Identifier: Apache-2.0
// SPDX-FileCopyrightText: The Pumi Authors

package controlplane_test

import (
	"testing"

	"getpumi.dev/pumi/api/gen/pumi/controlplane/v1/controlplanev1connect"
	runnerv1 "getpumi.dev/pumi/api/gen/pumi/runner/v1"
	"getpumi.dev/pumi/api/gen/pumi/runner/v1/runnerv1connect"
	"getpumi.dev/pumi/internal/controlplane"
)

// TestClientCallsTheServicesInProcess: Client gives the wiring of embedded
// mode a client of both services, in-process, with the identity it names,
// so that cmd/pumi wires the components without a transport of its own
// (decisions 0152 and 0161).
func TestClientCallsTheServicesInProcess(t *testing.T) {
	svc, err := controlplane.New(openLog(t), config())
	if err != nil {
		t.Fatal(err)
	}
	kernel, err := svc.Client(identity(controlplane.CallerKernel))
	if err != nil {
		t.Fatalf("Client for the kernel: %v", err)
	}
	p := plane{svc: svc, kernel: controlplanev1connect.NewControlPlaneServiceClient(kernel)}
	p.requestCheck(t, "invocation-1")
	if h := p.head(t); h != 1 {
		t.Fatalf("the head is %d after the kernel's append, want 1", h)
	}
	runner, err := svc.Client(identity(controlplane.CallerRunner))
	if err != nil {
		t.Fatalf("Client for the runner: %v", err)
	}
	res, err := runnerv1connect.NewRunnerServiceClient(runner).Claim(t.Context(),
		&runnerv1.ClaimRequest{ProtocolVersions: []*runnerv1.ProtocolVersion{{Major: 1}}})
	if err != nil || res.GetInvocation().GetInvocation() != "invocation-1" {
		t.Fatalf("the runner's Claim through Client = %v, %v; want invocation-1", res, err)
	}
	if _, err := svc.Client(controlplane.Identity{Caller: controlplane.CallerKernel}); err == nil {
		t.Error("Client gave a client to an identity with no principal")
	}
}
