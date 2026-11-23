// SPDX-License-Identifier: Apache-2.0
// SPDX-FileCopyrightText: The Pumi Authors

package runner

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// testRepo is a repository made for a test, with one commit.
type testRepo struct {
	t    *testing.T
	dir  string
	tree string
}

// newTestRepo makes a repository whose one commit holds a regular file,
// an executable, a symbolic link, a file in a directory and a .gitignore.
func newTestRepo(t *testing.T) *testRepo {
	t.Helper()
	r := &testRepo{t: t, dir: t.TempDir()}
	r.run(exec.CommandContext(t.Context(), "git", "init", "--quiet", "--initial-branch=main"))
	files := map[string]string{
		"README.md":            "read me\n",
		"internal/runner/a.go": "package runner\n",
		".gitignore":           "/bin/\n",
	}
	for name, content := range files {
		r.write(name, content)
	}
	r.write("run.sh", "#!/bin/sh\n")
	root := openRoot(t, r.dir)
	if err := root.Chmod("run.sh", 0o700); err != nil {
		t.Fatal(err)
	}
	if err := root.Symlink("README.md", "link"); err != nil {
		t.Fatal(err)
	}
	r.run(exec.CommandContext(t.Context(), "git", "add", "--all"))
	r.run(exec.CommandContext(t.Context(), "git", "-c", "user.name=Test", "-c", "user.email=test@example.com",
		"commit", "--quiet", "--message=first"))
	r.tree = strings.TrimSpace(r.run(exec.CommandContext(t.Context(), "git", "rev-parse", "HEAD^{tree}")))
	return r
}

// write writes content to the file name of the repository's working tree.
func (r *testRepo) write(name, content string) {
	r.t.Helper()
	writeFile(r.t, r.dir, name, content)
}

// run runs cmd in the repository and returns its output.
func (r *testRepo) run(cmd *exec.Cmd) string {
	r.t.Helper()
	cmd.Dir = r.dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		r.t.Fatalf("%v: %v\n%s", cmd.Args, err, out)
	}
	return string(out)
}

// openRoot opens the directory dir as a root, through which the tests
// reach the files below it, and closes it when the test ends.
func openRoot(t *testing.T, dir string) *os.Root {
	t.Helper()
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = root.Close() })
	return root
}

// writeFile writes content to the file name below the directory dir,
// making its directories.
func writeFile(t *testing.T, dir, name, content string) {
	t.Helper()
	root := openRoot(t, dir)
	if err := root.MkdirAll(filepath.Dir(name), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := root.WriteFile(name, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

// checkedOut returns the repository's git and a workspace and index in a
// new directory, with the repository's tree checked out in the workspace.
func checkedOut(t *testing.T, r *testRepo) (repository, string, string) {
	t.Helper()
	g, err := openRepository(t.Context(), r.dir)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	work, index := filepath.Join(dir, "workspace"), filepath.Join(dir, "index")
	if err := g.checkout(t.Context(), r.tree, work, index); err != nil {
		t.Fatal(err)
	}
	return g, work, index
}

func TestACheckoutHoldsTheTree(t *testing.T) {
	r := newTestRepo(t)
	_, work, _ := checkedOut(t, r)
	root := openRoot(t, work)
	for name, want := range map[string]string{"README.md": "read me\n", "internal/runner/a.go": "package runner\n"} {
		got, err := root.ReadFile(name)
		if err != nil || string(got) != want {
			t.Errorf("%s holds %q, %v; want %q", name, got, err, want)
		}
	}
	if info, err := root.Stat("run.sh"); err != nil || info.Mode().Perm()&0o100 == 0 {
		t.Errorf("run.sh is %v, %v; want an executable", info, err)
	}
	if target, err := root.Readlink("link"); err != nil || target != "README.md" {
		t.Errorf("link points to %q, %v; want README.md", target, err)
	}
	if _, err := root.Stat(".git"); !os.IsNotExist(err) {
		t.Errorf("the workspace has a .git: %v", err)
	}
}

func TestACheckoutLeavesTheRepositoryAlone(t *testing.T) {
	r := newTestRepo(t)
	r.write("README.md", "changed but not committed\n")
	before := r.run(exec.CommandContext(t.Context(), "git", "status", "--porcelain"))
	checkedOut(t, r)
	if after := r.run(exec.CommandContext(t.Context(), "git", "status", "--porcelain")); after != before {
		t.Errorf("git status was\n%s\nand is now\n%s", before, after)
	}
}

func TestAnUnchangedWorkspaceHasTheInputTree(t *testing.T) {
	r := newTestRepo(t)
	g, work, index := checkedOut(t, r)
	got, err := g.writeTree(t.Context(), work, index)
	if err != nil {
		t.Fatal(err)
	}
	if got != r.tree {
		t.Errorf("the tree of the unchanged workspace is %s, want the input tree %s", got, r.tree)
	}
}

func TestTheChangedPaths(t *testing.T) {
	r := newTestRepo(t)
	g, work, index := checkedOut(t, r)
	writeFile(t, work, "internal/runner/b.go", "package runner\n")
	writeFile(t, work, "README.md", "changed\n")
	if err := openRoot(t, work).Remove("run.sh"); err != nil {
		t.Fatal(err)
	}
	// An ignored file is not part of the tree.
	writeFile(t, work, "bin/pumi", "binary\n")
	tree, err := g.writeTree(t.Context(), work, index)
	if err != nil {
		t.Fatal(err)
	}
	got, err := g.changed(t.Context(), r.tree, tree)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"README.md", "internal/runner/b.go", "run.sh"}; !slices.Equal(got, want) {
		t.Errorf("the changed paths are %q, want %q", got, want)
	}
}

func TestACheckoutOfAMissingTreeFails(t *testing.T) {
	r := newTestRepo(t)
	g, err := openRepository(t.Context(), r.dir)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	for _, tree := range []string{"0123456789abcdef0123456789abcdef01234567", "not a tree", "HEAD", ""} {
		if err := g.checkout(t.Context(), tree, filepath.Join(dir, "w"), filepath.Join(dir, "i")); err == nil {
			t.Errorf("the checkout of %q gives no error", tree)
		}
	}
}

func TestOpeningWhatIsNotARepositoryFails(t *testing.T) {
	if _, err := openRepository(t.Context(), t.TempDir()); err == nil {
		t.Error("openRepository of an empty directory gives no error")
	}
}
