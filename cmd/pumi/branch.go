// SPDX-License-Identifier: Apache-2.0
// SPDX-FileCopyrightText: The Pumi Authors

package main

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strings"

	"getpumi.dev/pumi/internal/cli"
)

// writeBranch writes b's tree as a commit with b's parent and message, by
// the clone's git user, on the new branch b.Name of the clone that dir is
// in, and returns the commit (decisions 0289, 0313 and 0314). It fails when
// the branch exists. The git arguments are constant, because gosec reports
// a command built from variables (G204); the names and the commit go on
// standard input.
func writeBranch(ctx context.Context, dir string, b cli.Branch) (string, error) {
	if strings.HasPrefix(b.Name, "-") {
		return "", fmt.Errorf("%q is not a branch name", b.Name)
	}
	ref := "refs/heads/" + b.Name
	types, err := gitRun(dir, b.Tree+"^{tree}\n"+b.Parent+"^{commit}\n"+ref+"\n",
		exec.CommandContext(ctx, "git", "cat-file", "--batch-check=%(objecttype)"))
	if err != nil {
		return "", err
	}
	switch lines := strings.Split(types, "\n"); {
	case len(lines) != 3:
		return "", fmt.Errorf("git cat-file wrote %q", types)
	case lines[0] != "tree":
		return "", fmt.Errorf("the clone has no tree %s", b.Tree)
	case lines[1] != "commit":
		return "", fmt.Errorf("the clone has no commit %s", b.Parent)
	case lines[2] != ref+" missing":
		return "", fmt.Errorf("the branch %s exists", b.Name)
	}
	author, err := gitRun(dir, "", exec.CommandContext(ctx, "git", "var", "GIT_AUTHOR_IDENT"))
	if err != nil {
		return "", err
	}
	committer, err := gitRun(dir, "", exec.CommandContext(ctx, "git", "var", "GIT_COMMITTER_IDENT"))
	if err != nil {
		return "", err
	}
	object := fmt.Sprintf("tree %s\nparent %s\nauthor %s\ncommitter %s\n\n%s", b.Tree, b.Parent, author, committer, b.Message)
	commit, err := gitRun(dir, object, exec.CommandContext(ctx, "git", "hash-object", "-t", "commit", "-w", "--stdin"))
	if err != nil {
		return "", err
	}
	// create fails when the branch exists, also when it was made after the
	// check above.
	if _, err := gitRun(dir, "create "+ref+" "+commit+"\n", exec.CommandContext(ctx, "git", "update-ref", "--stdin")); err != nil {
		if strings.Contains(err.Error(), "reference already exists") {
			return "", fmt.Errorf("the branch %s exists", b.Name)
		}
		return "", err
	}
	return commit, nil
}

// gitRun runs the git command cmd, whose arguments are constant, in dir,
// with stdin on its standard input, and returns its standard output
// without the last newline.
func gitRun(dir, stdin string, cmd *exec.Cmd) (string, error) {
	cmd.Dir = dir
	cmd.Stdin = strings.NewReader(stdin)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("git %s: %w: %s", cmd.Args[1], err, strings.TrimSpace(stderr.String()))
	}
	return strings.TrimSuffix(stdout.String(), "\n"), nil
}
