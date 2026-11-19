// SPDX-License-Identifier: Apache-2.0
// SPDX-FileCopyrightText: The Pumi Authors

package spdx

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"slices"
	"strings"
)

// Files runs git in dir, the root of a repository, and returns the files of
// the tree as git sees them, sorted: the tracked files and the untracked files
// that .gitignore does not exclude. A tracked file that was deleted
// from the working tree is listed, and Check skips it.
func Files(ctx context.Context, dir string) ([]string, error) {
	out, err := run(dir, "", exec.CommandContext(ctx, "git", "ls-files", "-z", "--cached", "--others", "--exclude-standard", "--deduplicate"))
	if err != nil {
		return nil, err
	}
	files := split(out)
	slices.Sort(files)
	return files, nil
}

// ChangedFiles runs git in dir, the root of a repository, and returns the
// files that were added or changed between the commit base and HEAD, sorted
// by git. Deleted files are left out, and a renamed file is listed by its new
// name. In a pull request's merge commit, with base the base commit of the
// pull request, these are the files that the pull request adds or changes.
//
// git resolves base and HEAD through standard input, so every command has
// constant arguments and base cannot add an option to one.
func ChangedFiles(ctx context.Context, dir, base string) ([]string, error) {
	switch {
	case base == "":
		return nil, errors.New("the base commit is empty")
	case strings.ContainsAny(base, " \t\r\n"):
		return nil, fmt.Errorf("the base commit %q has white space", base)
	case strings.HasPrefix(base, "-"):
		return nil, fmt.Errorf("the base commit %q starts with %q", base, "-")
	}
	out, err := run(dir, base+"^{tree}\nHEAD^{tree}\n", exec.CommandContext(ctx, "git", "cat-file", "--batch-check=%(objectname) %(objecttype)"))
	if err != nil {
		return nil, err
	}
	var trees []string
	for _, line := range strings.Split(strings.TrimSuffix(string(out), "\n"), "\n") {
		name, kind, _ := strings.Cut(line, " ")
		if kind != "tree" {
			return nil, fmt.Errorf("the base commit %q does not name a commit: git cat-file: %s", base, line)
		}
		trees = append(trees, name)
	}
	if len(trees) != 2 {
		return nil, fmt.Errorf("git cat-file: want 2 trees, got %q", out)
	}
	out, err = run(dir, trees[0]+" "+trees[1]+"\n", exec.CommandContext(ctx, "git", "diff-tree", "--stdin", "-r", "-z", "--name-only", "--no-renames", "--diff-filter=d"))
	if err != nil {
		return nil, err
	}
	// With --stdin, git diff-tree first prints the line that it read, ended
	// by a newline even with -z.
	rest, ok := bytes.CutPrefix(out, []byte(trees[0]+" "+trees[1]+"\n"))
	if !ok {
		return nil, fmt.Errorf("git diff-tree: unexpected output %q", out)
	}
	return split(rest), nil
}

// run runs cmd in dir with stdin as its standard input, and returns its
// standard output. Each caller builds cmd with constant arguments, because
// gosec reports a command built from variables (G204).
func run(dir, stdin string, cmd *exec.Cmd) ([]byte, error) {
	cmd.Dir = dir
	cmd.Stdin = strings.NewReader(stdin)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("%s: %w: %s", strings.Join(cmd.Args[:2], " "), err, strings.TrimSpace(stderr.String()))
	}
	return stdout.Bytes(), nil
}

// split splits NUL-terminated output into its fields.
func split(out []byte) []string {
	var fields []string
	for field := range strings.SplitSeq(string(out), "\x00") {
		if field != "" {
			fields = append(fields, field)
		}
	}
	return fields
}
