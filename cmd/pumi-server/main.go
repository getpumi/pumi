// SPDX-License-Identifier: Apache-2.0
// SPDX-FileCopyrightText: The Pumi Authors

// Command pumi-server runs the control-plane service.
//
// A main package under cmd/ is the only kind of package that may import more
// than one component, and only to wire them together. For now it builds the
// control plane's decider from the embedded schema, core invariants and core
// policies, prints its name and version and exits.
package main

import (
	"fmt"
	"io"
	"os"

	"getpumi.dev/pumi/api/policy"
	"getpumi.dev/pumi/internal/controlplane"
	"getpumi.dev/pumi/internal/shared"
)

// core are the policy files that every policy set holds: the core
// invariants (decision 0142) and the core policies of the workflow actions
// (decision 0169).
var core = []controlplane.PolicyFile{
	{Name: "invariants.cedar", Text: policy.Invariants},
	{Name: "core.cedar", Text: policy.Core},
}

func main() {
	if err := run(os.Stdout, policy.Schema, core...); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

// run builds the decider for the schema and policy files, so the server
// does not start with policies that it cannot decide, and then writes the
// name and version.
func run(w io.Writer, schema string, files ...controlplane.PolicyFile) error {
	if _, err := controlplane.NewDecider(schema, files...); err != nil {
		return fmt.Errorf("cannot build the decider: %w", err)
	}
	return shared.WriteVersion(w, "pumi-server")
}
