// SPDX-License-Identifier: Apache-2.0
// SPDX-FileCopyrightText: The Pumi Authors

package spdx

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"testing"
)

// repo is a git repository in a temporary directory. Its commands have
// constant arguments, because gosec reports a command that is built from
// variables (G204).
type repo struct {
	t   *testing.T
	dir string
}

// newRepo creates an empty repository whose branch is main. It turns off the
// user's and the system's git configuration for the test.
func newRepo(t *testing.T) *repo {
	t.Helper()
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	r := &repo{t: t, dir: t.TempDir()}
	r.git(exec.CommandContext(t.Context(), "git", "init", "--quiet", "--initial-branch=main"))
	return r
}

// git runs cmd in the repository.
func (r *repo) git(cmd *exec.Cmd) {
	r.t.Helper()
	cmd.Dir = r.dir
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=Author", "GIT_AUTHOR_EMAIL=author@example.com",
		"GIT_COMMITTER_NAME=Committer", "GIT_COMMITTER_EMAIL=committer@example.com")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		r.t.Fatalf("%s: %v\n%s", cmd, err, stderr.String())
	}
}

// write writes a file in the working tree.
func (r *repo) write(name, src string) {
	r.t.Helper()
	p := filepath.Join(r.dir, name)
	if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
		r.t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(src), 0o600); err != nil {
		r.t.Fatal(err)
	}
}

// remove deletes a file from the working tree.
func (r *repo) remove(name string) {
	r.t.Helper()
	if err := os.Remove(filepath.Join(r.dir, name)); err != nil {
		r.t.Fatal(err)
	}
}

// commit stages every change and commits it.
func (r *repo) commit() {
	r.t.Helper()
	r.git(exec.CommandContext(r.t.Context(), "git", "add", "--all"))
	r.git(exec.CommandContext(r.t.Context(), "git", "commit", "--quiet", "--message=change"))
}

// tag tags HEAD as base.
func (r *repo) tag() {
	r.t.Helper()
	r.git(exec.CommandContext(r.t.Context(), "git", "tag", "base"))
}

func TestFilesListsTrackedAndUntrackedFiles(t *testing.T) {
	r := newRepo(t)
	r.write(".gitignore", "/bin/\n")
	r.write("a.go", "")
	r.write("dir/with space.go", "")
	r.write("deleted.go", "")
	r.commit()
	r.remove("deleted.go")
	r.write("untracked.go", "")
	r.write("bin/ignored", "")
	got, err := Files(t.Context(), r.dir)
	if err != nil {
		t.Fatalf("Files() error = %v", err)
	}
	want := []string{".gitignore", "a.go", "deleted.go", "dir/with space.go", "untracked.go"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Files() = %q, want %q", got, want)
	}
}

func TestChangedFilesListsAddedAndModifiedFiles(t *testing.T) {
	r := newRepo(t)
	r.write("same.go", "")
	r.write("modified.go", "")
	r.write("deleted.go", "")
	r.write("renamed.go", "package p\n\nfunc f() {}\n")
	r.commit()
	r.tag()
	r.write("modified.go", "x")
	r.remove("deleted.go")
	r.remove("renamed.go")
	r.write("new name.go", "package p\n\nfunc f() {}\n")
	r.write("dir/added.go", "")
	r.commit()
	r.write("uncommitted.go", "")
	got, err := ChangedFiles(t.Context(), r.dir, "base")
	if err != nil {
		t.Fatalf("ChangedFiles() error = %v", err)
	}
	want := []string{"dir/added.go", "modified.go", "new name.go"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("ChangedFiles() = %q, want %q", got, want)
	}
}

func TestChangedFilesWithNoChanges(t *testing.T) {
	r := newRepo(t)
	r.write("a.go", "")
	r.commit()
	r.tag()
	got, err := ChangedFiles(t.Context(), r.dir, "base")
	if err != nil || len(got) != 0 {
		t.Errorf("ChangedFiles() = %q, %v; want none and no error", got, err)
	}
}

func TestChangedFilesRejectsBadBases(t *testing.T) {
	r := newRepo(t)
	r.write("a.go", "")
	r.commit()
	for _, base := range []string{"", "--output=x", "main\nHEAD", "main HEAD", "no-such-commit", "main..HEAD"} {
		if got, err := ChangedFiles(t.Context(), r.dir, base); err == nil {
			t.Errorf("ChangedFiles(%q) = %q, want an error", base, got)
		}
	}
}
