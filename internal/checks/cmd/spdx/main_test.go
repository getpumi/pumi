// SPDX-License-Identifier: Apache-2.0
// SPDX-FileCopyrightText: The Pumi Authors

package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

const header = "// SPDX-License-Identifier: Apache-2.0\n// SPDX-FileCopyrightText: The Pumi Authors\n"

// newRepo creates a repository in a temporary directory, makes it the
// working directory, and commits good.go and bad.go, which has no header. It
// tags that commit as base. The git commands have constant arguments
// (G204).
func newRepo(t *testing.T) {
	t.Helper()
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("GIT_AUTHOR_NAME", "Author")
	t.Setenv("GIT_AUTHOR_EMAIL", "author@example.com")
	t.Setenv("GIT_COMMITTER_NAME", "Committer")
	t.Setenv("GIT_COMMITTER_EMAIL", "committer@example.com")
	t.Chdir(t.TempDir())
	git(t, exec.CommandContext(t.Context(), "git", "init", "--quiet", "--initial-branch=main"))
	write(t, "good.go", header+"\npackage p\n")
	write(t, "bad.go", "package p\n")
	commit(t)
	git(t, exec.CommandContext(t.Context(), "git", "tag", "base"))
}

func git(t *testing.T, cmd *exec.Cmd) {
	t.Helper()
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%s: %v\n%s", cmd, err, out)
	}
}

func write(t *testing.T, name, src string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(name), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(name, []byte(src), 0o600); err != nil {
		t.Fatal(err)
	}
}

func commit(t *testing.T) {
	t.Helper()
	git(t, exec.CommandContext(t.Context(), "git", "add", "--all"))
	git(t, exec.CommandContext(t.Context(), "git", "commit", "--quiet", "--message=change"))
}

func TestRunChecksTheWholeTree(t *testing.T) {
	newRepo(t)
	write(t, "dir/untracked.yml", "name: x\n")
	var stdout, stderr bytes.Buffer
	if code := run(nil, &stdout, &stderr); code != 1 {
		t.Errorf("run() = %d, want 1", code)
	}
	want := "bad.go: does not start with the two SPDX lines as // comments (decision 0068)\n" +
		"dir/untracked.yml: does not start with the two SPDX lines as # comments (decision 0068)\n"
	if got := stdout.String(); got != want {
		t.Errorf("run() wrote stdout %q, want %q", got, want)
	}
	if got, want := stderr.String(), "spdx: files that fail the SPDX check (decision 0068): 2 of 3\n"; got != want {
		t.Errorf("run() wrote stderr %q, want %q", got, want)
	}
}

func TestRunPassesAGoodTree(t *testing.T) {
	newRepo(t)
	write(t, "bad.go", header+"\npackage p\n")
	write(t, "LICENSE", "license text\n")
	var stdout, stderr bytes.Buffer
	if code := run(nil, &stdout, &stderr); code != 0 {
		t.Errorf("run() = %d, want 0; stderr: %s", code, stderr.String())
	}
	if got, want := stdout.String(), "spdx: files checked: 3, none fails\n"; got != want {
		t.Errorf("run() wrote stdout %q, want %q", got, want)
	}
	if stderr.Len() != 0 {
		t.Errorf("run() wrote stderr %q, want nothing", stderr.String())
	}
}

func TestRunChecksOnlyTheChangedFiles(t *testing.T) {
	newRepo(t)
	write(t, "new.go", "package p\n")
	write(t, "good.go", header+"\npackage p\n\nfunc f() {}\n")
	commit(t)
	var stdout, stderr bytes.Buffer
	if code := run([]string{"base"}, &stdout, &stderr); code != 1 {
		t.Errorf("run() = %d, want 1", code)
	}
	// bad.go fails too, but it is not changed.
	want := "new.go: does not start with the two SPDX lines as // comments (decision 0068)\n"
	if got := stdout.String(); got != want {
		t.Errorf("run() wrote stdout %q, want %q", got, want)
	}
	if got, want := stderr.String(), "spdx: files that fail the SPDX check (decision 0068): 1 of 2\n"; got != want {
		t.Errorf("run() wrote stderr %q, want %q", got, want)
	}
}

func TestRunPassesWhenNoFileChanged(t *testing.T) {
	newRepo(t)
	var stdout, stderr bytes.Buffer
	if code := run([]string{"base"}, &stdout, &stderr); code != 0 {
		t.Errorf("run() = %d, want 0; stderr: %s", code, stderr.String())
	}
	if got, want := stdout.String(), "spdx: files checked: 0, none fails\n"; got != want {
		t.Errorf("run() wrote stdout %q, want %q", got, want)
	}
}

func TestRunRejectsTooManyArguments(t *testing.T) {
	newRepo(t)
	var stdout, stderr bytes.Buffer
	if code := run([]string{"a", "b"}, &stdout, &stderr); code != 2 {
		t.Errorf("run() = %d, want 2", code)
	}
	if got, want := stderr.String(), "usage: spdx [base-commit]\n"; got != want {
		t.Errorf("run() wrote stderr %q, want %q", got, want)
	}
}

func TestRunFailsOnABadBase(t *testing.T) {
	newRepo(t)
	var stdout, stderr bytes.Buffer
	if code := run([]string{"no-such-commit"}, &stdout, &stderr); code != 2 {
		t.Errorf("run() = %d, want 2", code)
	}
	if !strings.HasPrefix(stderr.String(), "spdx: ") {
		t.Errorf("run() wrote stderr %q, want an spdx: error", stderr.String())
	}
}

func TestRunFailsOutsideARepository(t *testing.T) {
	t.Setenv("GIT_CEILING_DIRECTORIES", filepath.Dir(t.TempDir()))
	t.Chdir(t.TempDir())
	var stdout, stderr bytes.Buffer
	if code := run(nil, &stdout, &stderr); code != 2 {
		t.Errorf("run() = %d, want 2", code)
	}
}
