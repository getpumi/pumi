// SPDX-License-Identifier: Apache-2.0
// SPDX-FileCopyrightText: The Pumi Authors

package main

import (
	"bytes"
	"slices"
	"strings"
	"testing"

	"getpumi.dev/pumi/api/policy"
	"getpumi.dev/pumi/internal/controlplane"
)

func TestRunWritesNameAndVersion(t *testing.T) {
	var buf bytes.Buffer
	if err := run(&buf, policy.Schema, core...); err != nil {
		t.Fatalf("run() error = %v", err)
	}
	if got := buf.String(); !strings.HasPrefix(got, "pumi-server ") || !strings.HasSuffix(got, "\n") {
		t.Errorf("run() wrote %q, want a line starting with %q", got, "pumi-server ")
	}
}

func TestRunRefusesPoliciesThatTheDeciderRefuses(t *testing.T) {
	var buf bytes.Buffer
	bad := controlplane.PolicyFile{Name: "bad.cedar", Text: "permit (principal, action, resource);\n"}
	err := run(&buf, policy.Schema, append(slices.Clone(core), bad)...)
	if err == nil || !strings.Contains(err.Error(), "bad.cedar") {
		t.Errorf("run() = %v, want an error that names bad.cedar", err)
	}
	if buf.Len() != 0 {
		t.Errorf("run() wrote %q before it refused the policies", buf.String())
	}
}

func TestTheServerDecidesWithTheInvariantsAndTheCore(t *testing.T) {
	// The core invariants (decision 0142) and the core policies of the
	// workflow actions, such as the permit of escalate (decision 0169).
	want := []controlplane.PolicyFile{
		{Name: "invariants.cedar", Text: policy.Invariants},
		{Name: "core.cedar", Text: policy.Core},
	}
	if !slices.Equal(core, want) {
		t.Errorf("the server's policy files are %v, want the invariants and the core", core)
	}
}
