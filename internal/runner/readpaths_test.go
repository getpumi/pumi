// SPDX-License-Identifier: Apache-2.0
// SPDX-FileCopyrightText: The Pumi Authors

package runner

import (
	"slices"
	"testing"

	eventsv1 "getpumi.dev/pumi/api/gen/pumi/events/v1"
)

// TestTheAdapterGetsTheReadPathsOfItsConfiguration: the runner passes the
// read paths of the adapter's configuration in the invocation, so that a
// harness that runs commands in a sandbox can let them read a toolchain
// outside the workspace (decision 0269).
func TestTheAdapterGetsTheReadPathsOfItsConfiguration(t *testing.T) {
	f := newFixture(t, "success")
	a := f.cfg.Adapters[fakeAdapterName]
	a.ReadPaths = []string{"/home/someone/go", "/opt/toolchain"}
	f.cfg.Adapters[fakeAdapterName] = a
	report := f.runOne(t)
	if report.GetOutcome() != eventsv1.InvocationFinished_OUTCOME_SUCCEEDED {
		t.Fatalf("the outcome is %v, want succeeded; the log is\n%s", report.GetOutcome(), f.service.log())
	}
	if got := sentInvocation(t, f.service.log()).GetReadPaths(); !slices.Equal(got, a.ReadPaths) {
		t.Errorf("the adapter got the read paths %q, want %q", got, a.ReadPaths)
	}
}

// TestAHarnessInvocationSharesNoReadPathWithTheRunner: changing the read
// paths of the invocation changes nothing that the runner resolved.
func TestAHarnessInvocationSharesNoReadPathWithTheRunner(t *testing.T) {
	r := resolved()
	r.ReadPaths = []string{"/home/someone/go"}
	inv, err := HarnessInvocation(document(), r)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(inv.GetReadPaths(), []string{"/home/someone/go"}) {
		t.Fatalf("the read paths are %q, want the resolved ones", inv.GetReadPaths())
	}
	inv.ReadPaths[0] = "/"
	if r.ReadPaths[0] != "/home/someone/go" {
		t.Error("changing the harness invocation changed the runner's read paths")
	}
}
