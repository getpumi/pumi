// SPDX-License-Identifier: Apache-2.0
// SPDX-FileCopyrightText: The Pumi Authors

package runner

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strings"
)

// repository is a git repository that holds the trees of the invocations,
// such as the user's clone in embedded mode. The runner checks a tree out
// into a workspace with an index of its own, and computes the resulting
// tree from the workspace the same way, so the repository's own working
// tree, index and refs are never changed. Only new objects are written to
// it.
//
// Every git command has constant arguments: what varies is given in the
// environment (GIT_DIR, GIT_WORK_TREE and GIT_INDEX_FILE), on standard
// input or as the working directory.
type repository struct {
	// gitDir is the absolute path of the git directory.
	gitDir string
	// hashLength is the length in hexadecimal digits of an object name of
	// the repository's object format.
	hashLength int
	// emptyTree is the name of the empty tree.
	emptyTree string
}

// openRepository opens the repository that the directory dir is in.
func openRepository(ctx context.Context, dir string) (repository, error) {
	cmd := exec.CommandContext(ctx, "git", "rev-parse", "--absolute-git-dir", "--show-object-format")
	cmd.Dir = dir
	out, err := output(cmd, nil)
	if err != nil {
		return repository{}, fmt.Errorf("the directory %s is not in a git repository: %w", dir, err)
	}
	lines := strings.Fields(out)
	if len(lines) != 2 {
		return repository{}, fmt.Errorf("git rev-parse in %s printed %q", dir, out)
	}
	r := repository{gitDir: lines[0]}
	switch lines[1] {
	case "sha1":
		r.hashLength = 40
	case "sha256":
		r.hashLength = 64
	default:
		return repository{}, fmt.Errorf("the repository %s has the object format %q", r.gitDir, lines[1])
	}
	// git mktree with no entries writes the empty tree and prints its name.
	empty, err := r.run(exec.CommandContext(ctx, "git", "mktree"), "", "", nil)
	if err != nil {
		return repository{}, err
	}
	r.emptyTree = strings.TrimSpace(empty)
	return r, nil
}

// run runs the git command cmd on the repository, with the work tree work
// and the index file index when they are not empty, the working directory
// work or else the git directory, and stdin on standard input. It returns
// the output without its last newline.
func (r repository) run(cmd *exec.Cmd, work, index string, stdin []byte) (string, error) {
	cmd.Env = append(os.Environ(), "GIT_DIR="+r.gitDir)
	cmd.Dir = r.gitDir
	if work != "" {
		cmd.Env = append(cmd.Env, "GIT_WORK_TREE="+work)
		cmd.Dir = work
	}
	if index != "" {
		cmd.Env = append(cmd.Env, "GIT_INDEX_FILE="+index)
	}
	return output(cmd, stdin)
}

// output runs cmd with stdin on its standard input and returns its
// standard output, or an error that holds its standard error.
func output(cmd *exec.Cmd, stdin []byte) (string, error) {
	var stdout, stderr bytes.Buffer
	cmd.Stdin, cmd.Stdout, cmd.Stderr = bytes.NewReader(stdin), &stdout, &stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("%s: %w: %s", strings.Join(cmd.Args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return stdout.String(), nil
}

// checkTree refuses a tree that is not the full name of a tree object in
// the repository, so that a name such as HEAD cannot stand for a tree.
func (r repository) checkTree(ctx context.Context, tree string) error {
	if !regexp.MustCompile(fmt.Sprintf("^[0-9a-f]{%d}$", r.hashLength)).MatchString(tree) {
		return fmt.Errorf("%q is not the name of a tree", tree)
	}
	out, err := r.run(exec.CommandContext(ctx, "git", "cat-file", "--batch-check=%(objecttype)"), "", "", []byte(tree+"\n"))
	if err != nil {
		return err
	}
	if strings.TrimSpace(out) != "tree" {
		return fmt.Errorf("the repository has no tree %s", tree)
	}
	return nil
}

// checkout checks the tree out into the new directory work, with the
// index file index, which must be outside work. The workspace has no .git.
func (r repository) checkout(ctx context.Context, tree, work, index string) error {
	if err := r.checkTree(ctx, tree); err != nil {
		return err
	}
	// The raw diff from the empty tree lists every file of the tree, with
	// its mode and object, which git update-index takes on standard input.
	raw, err := r.run(exec.CommandContext(ctx, "git", "diff-tree", "--stdin", "-r", "-z", "--no-renames"),
		"", "", []byte(r.emptyTree+" "+tree+"\n"))
	if err != nil {
		return err
	}
	info, err := indexInfo(raw)
	if err != nil {
		return err
	}
	if err := os.Mkdir(work, 0o750); err != nil {
		return err
	}
	if _, err := r.run(exec.CommandContext(ctx, "git", "update-index", "-z", "--index-info"), work, index, info); err != nil {
		return err
	}
	_, err = r.run(exec.CommandContext(ctx, "git", "checkout-index", "--all", "--quiet"), work, index, nil)
	return err
}

// indexInfo turns the output of git diff-tree --stdin -r -z from the empty
// tree into the input of git update-index -z --index-info. The output
// starts with the input line, ended by a newline; then each file has a
// field ":<old mode> <new mode> <old object> <new object> A" and its path,
// each ended by a NUL.
func indexInfo(raw string) ([]byte, error) {
	_, rest, ok := strings.Cut(raw, "\n")
	if !ok {
		return nil, fmt.Errorf("git diff-tree printed %q", raw)
	}
	fields := strings.Split(strings.TrimSuffix(rest, "\x00"), "\x00")
	if rest == "" {
		fields = nil
	}
	if len(fields)%2 != 0 {
		return nil, errors.New("git diff-tree printed an odd number of fields")
	}
	var info bytes.Buffer
	for i := 0; i < len(fields); i += 2 {
		status := strings.Fields(fields[i])
		if len(status) != 5 || status[4] != "A" {
			return nil, fmt.Errorf("git diff-tree printed the field %q", fields[i])
		}
		fmt.Fprintf(&info, "%s %s\t%s\x00", status[1], status[3], fields[i+1])
	}
	return info.Bytes(), nil
}

// writeTree records every file of the workspace work in the index file
// index, as git add --all does, which leaves out the files that the
// .gitignore files ignore, writes the tree and returns its name.
func (r repository) writeTree(ctx context.Context, work, index string) (string, error) {
	if _, err := r.run(exec.CommandContext(ctx, "git", "add", "--all"), work, index, nil); err != nil {
		return "", err
	}
	tree, err := r.run(exec.CommandContext(ctx, "git", "write-tree"), work, index, nil)
	return strings.TrimSpace(tree), err
}

// changed returns the paths of the files that differ between the trees
// from and to, added, changed or deleted, in git's order.
func (r repository) changed(ctx context.Context, from, to string) ([]string, error) {
	out, err := r.run(exec.CommandContext(ctx, "git", "diff-tree", "--stdin", "-r", "-z", "--name-only", "--no-renames"),
		"", "", []byte(from+" "+to+"\n"))
	if err != nil {
		return nil, err
	}
	_, rest, ok := strings.Cut(out, "\n")
	if !ok {
		return nil, fmt.Errorf("git diff-tree printed %q", out)
	}
	if rest == "" {
		return []string{}, nil
	}
	return strings.Split(strings.TrimSuffix(rest, "\x00"), "\x00"), nil
}
