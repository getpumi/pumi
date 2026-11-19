// SPDX-License-Identifier: Apache-2.0
// SPDX-FileCopyrightText: The Pumi Authors

// Command dco runs the DCO check (decisions 0070 and 0081) on the commits in a
// range, such as "<base>..<head>", in the repository in the working directory.
// It reads the agent identities from .pumi/agent-identities.json. make dco
// runs it, and CI runs make dco on the commits of each pull request. It prints
// one line for each commit that fails and exits with status 1 when any fails,
// and with status 2 when it cannot run.
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"

	"getpumi.dev/pumi/internal/checks/dco"
)

// identitiesFile is the list of agent identities (decisions 0083 and 0112),
// relative to the repository root.
const identitiesFile = ".pumi/agent-identities.json"

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

// errUsage is returned for wrong arguments. run prints it without the "dco: "
// prefix.
var errUsage = errors.New("usage: dco <base>..<head>")

// run returns the exit status: 0 when every commit passes, 1 when any fails,
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
		_, _ = fmt.Fprintf(stderr, "dco: %v\n", err)
		return 2
	case failed > 0:
		_, _ = fmt.Fprintf(stderr, "dco: commits that fail the DCO check (decisions 0070 and 0081): %d of %d\n", failed, total)
		return 1
	}
	return 0
}

// check writes one line to stdout for each commit in the range named by args
// that fails, or a summary line when none fails. It returns the number of
// failing commits and the number of commits checked. A range with no commits
// is an error, because a pull request always has a commit and an empty range
// means that the range is wrong.
func check(ctx context.Context, args []string, stdout io.Writer) (failed, total int, err error) {
	if len(args) != 1 {
		return 0, 0, errUsage
	}
	data, err := os.ReadFile(identitiesFile)
	if err != nil {
		return 0, 0, err
	}
	agents, err := dco.ParseIdentities(data)
	if err != nil {
		return 0, 0, fmt.Errorf("%s: %w", identitiesFile, err)
	}
	commits, err := dco.ReadCommits(ctx, ".", args[0])
	if err != nil {
		return 0, 0, err
	}
	if len(commits) == 0 {
		return 0, 0, fmt.Errorf("the range %q has no commits", args[0])
	}
	findings := dco.Check(commits, agents)
	for _, f := range findings {
		if _, err := fmt.Fprintln(stdout, f); err != nil {
			return 0, 0, err
		}
	}
	if len(findings) == 0 {
		if _, err := fmt.Fprintf(stdout, "dco: commits checked: %d, all signed off\n", len(commits)); err != nil {
			return 0, 0, err
		}
	}
	return len(findings), len(commits), nil
}
