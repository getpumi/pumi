// SPDX-License-Identifier: Apache-2.0
// SPDX-FileCopyrightText: The Pumi Authors

package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// withWritePaths lets the runs of the clone's configuration write under
// src/, and gives the clone's git user the name Person, for the commits of
// pumi branch.
func (c *commandClone) withWritePaths() *commandClone {
	c.t.Helper()
	b, err := os.ReadFile(c.config)
	if err != nil {
		c.t.Fatal(err)
	}
	text := strings.Replace(string(b), "capabilities { network", `capabilities { write_paths: "src/**" network`, 1)
	if text == string(b) {
		c.t.Fatal("the configuration file has no capabilities to add write paths to")
	}
	writeFile(c.t, filepath.Dir(c.config), filepath.Base(c.config), text)
	git(c.t, c.dir, exec.CommandContext(c.t.Context(), "git", "config", "user.name", "Person"))
	return c
}

// TestTrunkHeadReadsTheCommitAndItsTree: the wiring gives the kernel the
// trunk's commit with its tree (decision 0313), and an error for a trunk
// the clone does not have.
func TestTrunkHeadReadsTheCommitAndItsTree(t *testing.T) {
	dir := newClone(t)
	head, err := trunkHead(t.Context(), dir, "main")
	if err != nil {
		t.Fatal(err)
	}
	commit := gitOutput(t, dir, exec.CommandContext(t.Context(), "git", "rev-parse", "main"))
	tree := gitOutput(t, dir, exec.CommandContext(t.Context(), "git", "rev-parse", "main^{tree}"))
	if head.Commit != commit || head.Tree != tree {
		t.Errorf("trunkHead = %+v, want the commit %s and the tree %s", head, commit, tree)
	}
	if _, err := trunkHead(t.Context(), dir, "trunk"); err == nil || !strings.Contains(err.Error(), "no branch trunk") {
		t.Errorf("trunkHead of a branch the clone does not have = %v, want an error that names it", err)
	}
}

// TestBranchWritesThePlansLastTreeOnABranch: after a plan's run changes
// a file, pumi branch writes the result's tree as a commit whose parent is
// the trunk commit the plan started from, also when the trunk moved since,
// with a message that names the plan, on the new branch of the clone
// (decisions 0289, 0313 and 0314).
func TestBranchWritesThePlansLastTreeOnABranch(t *testing.T) {
	c := newCommandClone(t, "tracks", adapterWritesAFile).withWritePaths()
	base := gitOutput(t, c.dir, exec.CommandContext(t.Context(), "git", "rev-parse", "main"))
	r := c.start()
	c.must("add", "--track", "a", "a1")
	c.waitState(r, "a1", "merged")
	r.stop(t)
	git(t, c.dir, exec.CommandContext(t.Context(), "git", "commit", "--quiet", "--allow-empty", "--message=the trunk moves"))
	out := c.must("branch", "--config", c.config, "a1", "pumi/a1")
	if !strings.Contains(out, "pumi/a1") || !strings.Contains(out, "a1/1") {
		t.Errorf("pumi branch wrote %q, want a line that names the branch and the invocation", out)
	}
	if got := gitOutput(t, c.dir, exec.CommandContext(t.Context(), "git", "rev-parse", "pumi/a1^")); got != base {
		t.Errorf("the branch's parent is %s, want %s, the trunk commit a1 started from", got, base)
	}
	if got := gitOutput(t, c.dir, exec.CommandContext(t.Context(), "git", "show", "pumi/a1:"+writtenFilePath)); got+"\n" != writtenFileContent {
		t.Errorf("the branch's %s holds %q, want the adapter's %q", writtenFilePath, got, writtenFileContent)
	}
	if got := gitOutput(t, c.dir, exec.CommandContext(t.Context(), "git", "log", "-1", "--format=%B", "pumi/a1")); strings.TrimSpace(got) != "Plan a1\n\nTrack: a\nInvocation: a1/1" {
		t.Errorf("the commit's message is %q, want the plan, its track and the invocation", got)
	}
	if got := gitOutput(t, c.dir, exec.CommandContext(t.Context(), "git", "log", "-1", "--format=%ae %ce", "pumi/a1")); got != testPerson+" "+testPerson {
		t.Errorf("the commit's author and committer are %q, want the clone's git user, %s", got, testPerson)
	}
	if out, err := c.pumi("branch", "--config", c.config, "a1", "pumi/a1"); err == nil || !strings.Contains(err.Error(), "the branch pumi/a1 exists") {
		t.Errorf("pumi branch onto the branch it wrote = %q, %v; want an error that says the branch exists", out, err)
	}
}

// TestBranchRefusesABranchThatExists: pumi branch does not move a branch
// that exists, such as the trunk, and says so.
func TestBranchRefusesABranchThatExists(t *testing.T) {
	c := newCommandClone(t, "tracks", adapterWritesAFile).withWritePaths()
	main := gitOutput(t, c.dir, exec.CommandContext(t.Context(), "git", "rev-parse", "main"))
	r := c.start()
	c.must("add", "--track", "a", "a1")
	c.waitState(r, "a1", "merged")
	if out, err := c.pumi("branch", "a1", "main"); err == nil || !strings.Contains(err.Error(), "the branch main exists") {
		t.Errorf("pumi branch a1 main = %q, %v; want an error that says the branch main exists", out, err)
	}
	if got := gitOutput(t, c.dir, exec.CommandContext(t.Context(), "git", "rev-parse", "main")); got != main {
		t.Errorf("main moved to %s, want it at %s", got, main)
	}
}
