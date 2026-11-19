// SPDX-License-Identifier: Apache-2.0
// SPDX-FileCopyrightText: The Pumi Authors

// Command spdx runs the SPDX header check (decision 0068) in the repository
// in the working directory. With no argument it checks every file of the
// tree, which is what make spdx does. With a base commit it checks only the
// files added or changed between that commit and HEAD, which is what CI does
// for a pull request. It prints one line for each file that fails and exits
// with status 1 when any fails, and with status 2 when it cannot run.
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"

	"getpumi.dev/pumi/internal/checks/spdx"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

// errUsage is returned for wrong arguments. run prints it without the
// "spdx: " prefix.
var errUsage = errors.New("usage: spdx [base-commit]")

// run returns the exit status: 0 when every file passes, 1 when any fails,
// and 2 when the check cannot run or cannot write its findings. The messages
// on stderr are the last report of a result that the exit status already
// carries, so an error from writing them is not checked.
func run(args []string, stdout, stderr io.Writer) int {
	failed, total, err := check(context.Background(), args, stdout)
	switch {
	case errors.Is(err, errUsage):
		_, _ = fmt.Fprintln(stderr, err)
		return 2
	case err != nil:
		_, _ = fmt.Fprintf(stderr, "spdx: %v\n", err)
		return 2
	case failed > 0:
		_, _ = fmt.Fprintf(stderr, "spdx: files that fail the SPDX check (decision 0068): %d of %d\n", failed, total)
		return 1
	}
	return 0
}

// check writes one line to stdout for each file that fails, or a summary line
// when none fails. It returns the number of failing files and the number of
// files listed.
func check(ctx context.Context, args []string, stdout io.Writer) (failed, total int, err error) {
	var paths []string
	switch len(args) {
	case 0:
		paths, err = spdx.Files(ctx, ".")
	case 1:
		paths, err = spdx.ChangedFiles(ctx, ".", args[0])
	default:
		return 0, 0, errUsage
	}
	if err != nil {
		return 0, 0, err
	}
	findings, err := spdx.Check(os.DirFS("."), paths)
	if err != nil {
		return 0, 0, err
	}
	for _, f := range findings {
		if _, err := fmt.Fprintln(stdout, f); err != nil {
			return 0, 0, err
		}
	}
	if len(findings) == 0 {
		if _, err := fmt.Fprintf(stdout, "spdx: files checked: %d, none fails\n", len(paths)); err != nil {
			return 0, 0, err
		}
	}
	return len(findings), len(paths), nil
}
