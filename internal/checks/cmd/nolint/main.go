// SPDX-License-Identifier: Apache-2.0
// SPDX-FileCopyrightText: The Pumi Authors

// Command nolint runs the nolint check (decision 0110) on a directory tree,
// the working directory by default. make lint runs it on the repository root.
// It prints one line for each nolint comment and exits with status 1 when it
// finds any, and with status 2 when it cannot run.
package main

import (
	"errors"
	"fmt"
	"io"
	"os"

	"getpumi.dev/pumi/internal/checks/nolint"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

// errUsage is returned for wrong arguments. run prints it without the
// "nolint: " prefix.
var errUsage = errors.New("usage: nolint [directory]")

// run returns the exit status: 0 when the tree has no nolint comments, 1 when
// it has some, and 2 when the check cannot run or cannot write its findings.
// The messages on stderr are the last report of a result that the exit status
// already carries, so an error from writing them is not checked.
func run(args []string, stdout, stderr io.Writer) int {
	n, err := check(args, stdout)
	switch {
	case errors.Is(err, errUsage):
		_, _ = fmt.Fprintln(stderr, err)
		return 2
	case err != nil:
		_, _ = fmt.Fprintf(stderr, "nolint: %v\n", err)
		return 2
	case n > 0:
		_, _ = fmt.Fprintf(stderr, "nolint: nolint comments found: %d\n", n)
		return 1
	}
	return 0
}

// check writes one line to stdout for each nolint comment in the directory
// named by args, the working directory by default, and returns their number.
func check(args []string, stdout io.Writer) (int, error) {
	dir := "."
	switch len(args) {
	case 0:
	case 1:
		dir = args[0]
	default:
		return 0, errUsage
	}
	findings, err := nolint.Check(os.DirFS(dir))
	if err != nil {
		return 0, err
	}
	for _, f := range findings {
		if _, err := fmt.Fprintln(stdout, f); err != nil {
			return 0, err
		}
	}
	return len(findings), nil
}
