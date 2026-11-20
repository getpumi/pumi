// SPDX-License-Identifier: Apache-2.0
// SPDX-FileCopyrightText: The Pumi Authors

// Command cedar runs the policy check (decision 0146) on the repository in
// the working directory, which is what make cedar does. It runs the pinned
// Rust Cedar CLI on the schema, the core invariants and every other policy
// set and run-tests file, and prints one line for each step. It exits with
// status 1 when a step fails, and with status 2 when it cannot run.
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"

	"getpumi.dev/pumi/internal/checks/cedar"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

// run returns the exit status: 0 when every step passes, 1 when any fails,
// and 2 when the check cannot run. The messages on stderr are the last
// report of a result that the exit status already carries, so an error from
// writing them is not checked.
func run(args []string, stdout, stderr io.Writer) int {
	if len(args) != 0 {
		_, _ = fmt.Fprintln(stderr, "usage: cedar")
		return 2
	}
	err := cedar.Check(context.Background(), ".", stdout)
	var findings cedar.Findings
	switch {
	case errors.As(err, &findings):
		for _, f := range findings {
			_, _ = fmt.Fprintf(stderr, "cedar: %s\n", f)
		}
		return 1
	case err != nil:
		_, _ = fmt.Fprintf(stderr, "cedar: %v\n", err)
		return 2
	}
	return 0
}
