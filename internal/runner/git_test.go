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
	if info, err := root.Stat(".git"); err != nil || !info.IsDir() {
		t.Errorf("the workspace's .git is %v, %v; want a directory (decision 0247)", info, err)
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

// inWorkspace runs the git command cmd in the directory dir of a
// workspace as an agent would, with the workspace's own .git and no
// configuration of the user's, and returns its output.
func inWorkspace(t *testing.T, dir string, cmd *exec.Cmd) string {
	t.Helper()
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL="+os.DevNull, "GIT_CONFIG_NOSYSTEM=1",
		"GIT_AUTHOR_NAME=Agent", "GIT_AUTHOR_EMAIL=agent@example.com",
		"GIT_COMMITTER_NAME=Agent", "GIT_COMMITTER_EMAIL=agent@example.com")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%q in the workspace: %v\n%s", cmd.Args, err, out)
	}
	return string(out)
}

func TestTheWorkspaceGitHasOneCommitOfTheInputTree(t *testing.T) {
	r := newTestRepo(t)
	_, work, _ := checkedOut(t, r)
	if got := inWorkspace(t, work, exec.CommandContext(t.Context(), "git", "status", "--porcelain", "--untracked-files=all")); got != "" {
		t.Errorf("git status in the workspace before the agent runs shows\n%s\nwant no changes", got)
	}
	if got := strings.TrimSpace(inWorkspace(t, work, exec.CommandContext(t.Context(), "git", "rev-list", "--count", "HEAD"))); got != "1" {
		t.Errorf("the workspace's HEAD has %s commits, want 1", got)
	}
	if got := strings.TrimSpace(inWorkspace(t, work, exec.CommandContext(t.Context(), "git", "rev-parse", "HEAD^{tree}"))); got != r.tree {
		t.Errorf("the workspace's commit holds the tree %s, want the input tree %s", got, r.tree)
	}
}

func TestTheWorkspaceGitShowsTheAgentsChanges(t *testing.T) {
	r := newTestRepo(t)
	_, work, _ := checkedOut(t, r)
	writeFile(t, work, "README.md", "changed\n")
	writeFile(t, work, "internal/runner/b.go", "package runner\n")
	if got := inWorkspace(t, work, exec.CommandContext(t.Context(), "git", "diff", "--name-only")); got != "README.md\n" {
		t.Errorf("git diff --name-only in the workspace shows %q, want README.md", got)
	}
	if got := inWorkspace(t, work, exec.CommandContext(t.Context(), "git", "status", "--porcelain", "--untracked-files=all")); got != " M README.md\n?? internal/runner/b.go\n" {
		t.Errorf("git status in the workspace shows %q", got)
	}
}

func TestTheWorkspaceGitStartsWithNoHooks(t *testing.T) {
	r := newTestRepo(t)
	_, work, _ := checkedOut(t, r)
	if entries, err := os.ReadDir(filepath.Join(work, ".git", "hooks")); err == nil && len(entries) > 0 {
		t.Errorf("the workspace's .git has the hooks %v, want none", entries)
	}
}

func TestAnAgentCommitLeavesTheResultingTreeTheSameAsTheFiles(t *testing.T) {
	r := newTestRepo(t)
	g, work, index := checkedOut(t, r)
	writeFile(t, work, "internal/runner/b.go", "package runner\n")
	writeFile(t, work, "README.md", "changed\n")
	inWorkspace(t, work, exec.CommandContext(t.Context(), "git", "add", "--all"))
	inWorkspace(t, work, exec.CommandContext(t.Context(), "git", "commit", "--quiet", "--message=agent"))
	inWorkspace(t, work, exec.CommandContext(t.Context(), "git", "branch", "agent-branch"))
	// A file changed after the commit is part of the resulting tree, which
	// comes from the files and not from the agent's commit.
	writeFile(t, work, "README.md", "changed again\n")
	agentCommit := strings.TrimSpace(inWorkspace(t, work, exec.CommandContext(t.Context(), "git", "rev-parse", "HEAD")))
	tree, err := g.writeTree(t.Context(), work, index)
	if err != nil {
		t.Fatal(err)
	}
	got, err := g.changed(t.Context(), r.tree, tree)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"README.md", "internal/runner/b.go"}; !slices.Equal(got, want) {
		t.Errorf("the changed paths are %q, want %q", got, want)
	}
	cat := exec.CommandContext(t.Context(), "git", "cat-file", "--batch")
	cat.Stdin = strings.NewReader(tree + ":README.md\n")
	readme := r.run(cat)
	if _, content, _ := strings.Cut(readme, "\n"); content != "changed again\n\n" {
		t.Errorf("README.md in the resulting tree holds %q, want the file's content", readme)
	}
	checkNoGit(t, g, tree)
	// The agent's commit and branch never reach the project's repository.
	check := exec.CommandContext(t.Context(), "git", "cat-file", "--batch-check")
	check.Stdin = strings.NewReader(agentCommit + "\n")
	if got := r.run(check); got != agentCommit+" missing\n" {
		t.Errorf("the project's repository has the agent's commit %s", agentCommit)
	}
	if refs := r.run(exec.CommandContext(t.Context(), "git", "for-each-ref", "--format=%(refname)")); refs != "refs/heads/main\n" {
		t.Errorf("the project's repository has the refs %q, want only refs/heads/main", refs)
	}
}

// checkNoGit fails the test when the tree of the repository g has a path
// with a .git in it or a gitlink.
func checkNoGit(t *testing.T, g repository, tree string) {
	t.Helper()
	raw, err := g.run(exec.CommandContext(t.Context(), "git", "diff-tree", "--stdin", "-r", "-z", "--no-renames"),
		"", "", []byte(g.emptyTree+" "+tree+"\n"))
	if err != nil {
		t.Fatal(err)
	}
	info, err := indexInfo(raw)
	if err != nil {
		t.Fatal(err)
	}
	for line := range strings.SplitSeq(strings.TrimSuffix(string(info), "\x00"), "\x00") {
		mode, path, _ := strings.Cut(line, "\t")
		if strings.HasPrefix(mode, "160000") || slices.Contains(strings.Split(path, "/"), ".git") {
			t.Errorf("the resulting tree has %q", line)
		}
	}
}

func TestANestedGitIsLeftOutOfTheResultingTree(t *testing.T) {
	r := newTestRepo(t)
	g, work, index := checkedOut(t, r)
	// A nested repository with a commit, which git add would record as a
	// gitlink, and a directory of the input tree that becomes one.
	writeFile(t, work, "vendor/lib/x.go", "package lib\n")
	inWorkspace(t, filepath.Join(work, "vendor", "lib"), exec.CommandContext(t.Context(), "git", "init", "--quiet"))
	inWorkspace(t, filepath.Join(work, "vendor", "lib"), exec.CommandContext(t.Context(), "git", "add", "--all"))
	inWorkspace(t, filepath.Join(work, "vendor", "lib"), exec.CommandContext(t.Context(), "git", "commit", "--quiet", "--message=nested"))
	inWorkspace(t, filepath.Join(work, "internal"), exec.CommandContext(t.Context(), "git", "init", "--quiet"))
	writeFile(t, work, "internal/runner/b.go", "package runner\n")
	inWorkspace(t, filepath.Join(work, "internal"), exec.CommandContext(t.Context(), "git", "add", "--all"))
	inWorkspace(t, filepath.Join(work, "internal"), exec.CommandContext(t.Context(), "git", "commit", "--quiet", "--message=nested"))
	// An ignored file in a nested repository stays out.
	writeFile(t, work, "vendor/lib/bin/x", "binary\n")
	writeFile(t, work, ".gitignore", "/bin/\nbin/\n")
	// A .git file, as a linked work tree has.
	writeFile(t, work, "docs/.git", "gitdir: /nowhere\n")
	writeFile(t, work, "docs/a.md", "a\n")
	tree, err := g.writeTree(t.Context(), work, index)
	if err != nil {
		t.Fatal(err)
	}
	got, err := g.changed(t.Context(), r.tree, tree)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{".gitignore", "docs/a.md", "internal/runner/b.go", "vendor/lib/x.go"}; !slices.Equal(got, want) {
		t.Errorf("the changed paths are %q, want %q", got, want)
	}
	checkNoGit(t, g, tree)
}

func TestTheResultingTreeKeepsTheIgnoreRules(t *testing.T) {
	r := newTestRepo(t)
	// A file that the .gitignore ignores but the input tree tracks stays,
	// and its change is recorded.
	r.write("bin/kept", "kept\n")
	r.run(exec.CommandContext(t.Context(), "git", "add", "--force", "bin/kept"))
	r.tree = strings.TrimSpace(r.run(exec.CommandContext(t.Context(), "git", "write-tree")))
	g, work, index := checkedOut(t, r)
	if got := inWorkspace(t, work, exec.CommandContext(t.Context(), "git", "status", "--porcelain", "--untracked-files=all")); got != "" {
		t.Errorf("git status in the workspace shows\n%s\nwant no changes", got)
	}
	writeFile(t, work, "bin/kept", "changed\n")
	writeFile(t, work, "bin/pumi", "binary\n")
	writeFile(t, work, "internal/.gitignore", "*.tmp\n")
	writeFile(t, work, "internal/runner/x.tmp", "temporary\n")
	// A file replaced by a directory, and a directory replaced by a file.
	if err := openRoot(t, work).Remove("README.md"); err != nil {
		t.Fatal(err)
	}
	writeFile(t, work, "README.md/inside", "inside\n")
	if err := openRoot(t, work).RemoveAll("internal/runner"); err != nil {
		t.Fatal(err)
	}
	writeFile(t, work, "internal/runner", "now a file\n")
	tree, err := g.writeTree(t.Context(), work, index)
	if err != nil {
		t.Fatal(err)
	}
	got, err := g.changed(t.Context(), r.tree, tree)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"README.md", "README.md/inside", "bin/kept", "internal/.gitignore", "internal/runner", "internal/runner/a.go"}
	if !slices.Equal(got, want) {
		t.Errorf("the changed paths are %q, want %q", got, want)
	}
}

func TestAHookTheAgentWritesNeverRunsOutsideTheWorkspace(t *testing.T) {
	r := newTestRepo(t)
	g, work, index := checkedOut(t, r)
	marks := t.TempDir()
	// A hook that the runner ran would see the variable too.
	t.Setenv("PUMI_TEST_MARKS", marks)
	hooks := filepath.Join(work, ".git", "hooks")
	if err := os.MkdirAll(hooks, 0o750); err != nil {
		t.Fatal(err)
	}
	names := []string{"pre-commit", "post-commit", "post-checkout", "post-index-change", "pre-auto-gc", "reference-transaction"}
	for _, name := range names {
		script := "#!/bin/sh\ntouch \"$PUMI_TEST_MARKS/" + name + "\"\n"
		writeFile(t, hooks, name, script)
		if err := openRoot(t, hooks).Chmod(name, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	// The agent also points git at an fsmonitor command and its hooks.
	inWorkspace(t, work, exec.CommandContext(t.Context(), "git", "config", "core.fsmonitor", ".git/hooks/pre-commit"))
	inWorkspace(t, work, exec.CommandContext(t.Context(), "git", "config", "core.hooksPath", ".git/hooks"))
	writeFile(t, work, "internal/runner/b.go", "package runner\n")
	tree, err := g.writeTree(t.Context(), work, index)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := g.changed(t.Context(), r.tree, tree); err != nil {
		t.Fatal(err)
	}
	if entries, err := os.ReadDir(marks); err != nil || len(entries) > 0 {
		t.Errorf("hooks the agent wrote ran when the runner computed the tree: %v, %v", entries, err)
	}
	// The hooks work in the workspace itself, so the check above is not
	// empty.
	inWorkspace(t, work, exec.CommandContext(t.Context(), "git", "add", "--all"))
	if _, err := os.Stat(filepath.Join(marks, "post-index-change")); err != nil {
		t.Errorf("the agent's hook did not run in the workspace either: %v", err)
	}
	if _, err := os.Stat(filepath.Join(r.dir, ".git", "hooks", "pre-commit")); !os.IsNotExist(err) {
		t.Errorf("the project's repository has a pre-commit hook: %v", err)
	}
}

func TestAGitlinkOfTheInputTreeStays(t *testing.T) {
	r := newTestRepo(t)
	sub := newTestRepo(t)
	commit := strings.TrimSpace(sub.run(exec.CommandContext(t.Context(), "git", "rev-parse", "HEAD")))
	link := exec.CommandContext(t.Context(), "git", "update-index", "--index-info")
	link.Stdin = strings.NewReader("160000 " + commit + "\tthird_party/sub\n")
	r.run(link)
	r.tree = strings.TrimSpace(r.run(exec.CommandContext(t.Context(), "git", "write-tree")))
	g, work, index := checkedOut(t, r)
	if got := inWorkspace(t, work, exec.CommandContext(t.Context(), "git", "status", "--porcelain", "--untracked-files=all")); got != "" {
		t.Errorf("git status in the workspace shows\n%s\nwant no changes", got)
	}
	// The agent makes a repository with a commit of its own where the
	// gitlink is.
	writeFile(t, work, "third_party/sub/x.go", "package sub\n")
	inWorkspace(t, filepath.Join(work, "third_party", "sub"), exec.CommandContext(t.Context(), "git", "init", "--quiet"))
	inWorkspace(t, filepath.Join(work, "third_party", "sub"), exec.CommandContext(t.Context(), "git", "add", "--all"))
	inWorkspace(t, filepath.Join(work, "third_party", "sub"), exec.CommandContext(t.Context(), "git", "commit", "--quiet", "--message=agent"))
	tree, err := g.writeTree(t.Context(), work, index)
	if err != nil {
		t.Fatal(err)
	}
	if tree != r.tree {
		got, _ := g.changed(t.Context(), r.tree, tree)
		t.Errorf("the resulting tree changes %q, want the gitlink as it was", got)
	}
}

func TestAWorkspaceOfASHA256Repository(t *testing.T) {
	dir := t.TempDir()
	r := &testRepo{t: t, dir: dir}
	r.run(exec.CommandContext(t.Context(), "git", "init", "--quiet", "--object-format=sha256"))
	r.write("README.md", "read me\n")
	r.run(exec.CommandContext(t.Context(), "git", "add", "--all"))
	r.tree = strings.TrimSpace(r.run(exec.CommandContext(t.Context(), "git", "write-tree")))
	_, work, _ := checkedOut(t, r)
	if got := strings.TrimSpace(inWorkspace(t, work, exec.CommandContext(t.Context(), "git", "rev-parse", "HEAD^{tree}"))); got != r.tree {
		t.Errorf("the workspace's commit holds the tree %s, want %s", got, r.tree)
	}
	if got := inWorkspace(t, work, exec.CommandContext(t.Context(), "git", "status", "--porcelain", "--untracked-files=all")); got != "" {
		t.Errorf("git status in the workspace shows\n%s\nwant no changes", got)
	}
}

func TestAnEmptyInputTree(t *testing.T) {
	r := newTestRepo(t)
	g, err := openRepository(t.Context(), r.dir)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	work, index := filepath.Join(dir, "workspace"), filepath.Join(dir, "index")
	if err := g.checkout(t.Context(), g.emptyTree, work, index); err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(inWorkspace(t, work, exec.CommandContext(t.Context(), "git", "rev-parse", "HEAD^{tree}"))); got != g.emptyTree {
		t.Errorf("the workspace's commit holds the tree %s, want the empty tree", got)
	}
}
