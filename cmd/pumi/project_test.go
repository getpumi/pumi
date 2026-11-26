// SPDX-License-Identifier: Apache-2.0
// SPDX-FileCopyrightText: The Pumi Authors

package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"getpumi.dev/pumi/api/events"
	eventsv1 "getpumi.dev/pumi/api/gen/pumi/events/v1"
)

// projectText is a project section, in text format, with a convention file
// that the test clones hold and one that they do not.
const projectText = `project {
  name: "case"
  protected_paths: "docs/constitution.md"
  check_commands: "make test"
  convention_files: "AGENTS.md"
  convention_files: "docs/missing.md"
}`

// withProject replaces the project of the clone's configuration file with
// project, in text format.
func (c *commandClone) withProject(project string) {
	c.t.Helper()
	root, err := os.OpenRoot(filepath.Dir(c.config))
	if err != nil {
		c.t.Fatal(err)
	}
	defer func() { _ = root.Close() }()
	src, err := root.ReadFile(filepath.Base(c.config))
	if err != nil {
		c.t.Fatal(err)
	}
	text := strings.Replace(string(src), `project { name: "case" }`, project, 1)
	if text == string(src) {
		c.t.Fatal("the configuration file of the test clone has no project to replace")
	}
	writeFile(c.t, filepath.Dir(c.config), filepath.Base(c.config), text)
}

// TestRunAddsTheProjectSectionToTheTask: pumi run gives the kernel the
// project section of the configuration file, and the task of the agent
// run names its commands, its convention files, with the one that the
// trunk's tree does not hold as missing, and its protected paths
// (decision 0250).
func TestRunAddsTheProjectSectionToTheTask(t *testing.T) {
	c := newCommandClone(t, "tracks", adapterSucceeds)
	c.withProject(projectText)
	writeFile(t, c.dir, "AGENTS.md", "Write plain English.\n")
	git(t, c.dir, exec.CommandContext(t.Context(), "git", "add", "AGENTS.md"))
	git(t, c.dir, exec.CommandContext(t.Context(), "git", "commit", "--quiet", "--message=conventions"))
	r := c.start()
	c.must("add", "--track", "a", "a1")
	c.waitState(r, "a1", "merged")
	r.stop(t)
	requested := ofType(c.log(), events.TypeInvocationRequested)
	if len(requested) != 1 {
		t.Fatalf("the log has %d invocation.requested events, want 1", len(requested))
	}
	doc, err := events.Document(dataOf(t, requested[0], &eventsv1.InvocationRequested{}))
	if err != nil {
		t.Fatal(err)
	}
	task := doc.GetInput().GetTask()
	for _, want := range []string{
		"## The project\n",
		"- `make test`\n",
		"- `AGENTS.md`\n",
		"- `docs/missing.md`: missing, the input tree has no such file\n",
		"- `docs/constitution.md`\n",
	} {
		if !strings.Contains(task, want) {
			t.Errorf("the task of a1/1 has no %q; it is\n%s", want, task)
		}
	}
}

// TestHasFileReadsTheTree: hasFile reports a file of the tree, and not a
// path that the tree does not hold or that is a directory; a tree that the
// clone does not have is an error.
func TestHasFileReadsTheTree(t *testing.T) {
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	dir := newClone(t)
	writeFile(t, dir, "docs/style.md", "Short lines.\n")
	git(t, dir, exec.CommandContext(t.Context(), "git", "add", "docs/style.md"))
	git(t, dir, exec.CommandContext(t.Context(), "git", "commit", "--quiet", "--message=style"))
	tree := gitOutput(t, dir, exec.CommandContext(t.Context(), "git", "rev-parse", "HEAD^{tree}"))
	for path, want := range map[string]bool{"docs/style.md": true, "docs": false, "AGENTS.md": false, "docs/other.md": false} {
		got, err := hasFile(t.Context(), dir, tree, path)
		if err != nil || got != want {
			t.Errorf("hasFile(%s) = %v, %v; want %v", path, got, err, want)
		}
	}
	if _, err := hasFile(t.Context(), dir, strings.Repeat("0", len(tree)), "docs/style.md"); err == nil {
		t.Error("hasFile of a tree that the clone does not have succeeded, want an error")
	}
}

// TestReadConfigRefusesABadProjectSection: an empty command and a
// convention file that is not a path inside the repository are refused.
func TestReadConfigRefusesABadProjectSection(t *testing.T) {
	for name, tc := range map[string]struct{ project, want string }{
		"an empty command":         {`project { name: "case" check_commands: "" }`, "check_commands"},
		"an empty convention file": {`project { name: "case" convention_files: "" }`, "convention_files"},
		"an absolute file":         {`project { name: "case" convention_files: "/etc/AGENTS.md" }`, "/etc/AGENTS.md"},
		"a file outside the clone": {`project { name: "case" convention_files: "../AGENTS.md" }`, "../AGENTS.md"},
		"a path that is not clean": {`project { name: "case" convention_files: "docs//style.md" }`, "docs//style.md"},
		"a path with a backslash":  {`project { name: "case" convention_files: "docs\\style.md" }`, "convention_files"},
	} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			writeFile(t, dir, "pumi.txtpb", "workflow: \"workflow.txtpb\"\ntrunk: \"main\"\n"+tc.project+"\n"+models(signedIn, implementer, checks, keyed)+"\n")
			_, err := readConfig(dir + "/pumi.txtpb")
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("readConfig = %v, want an error that names %s", err, tc.want)
			}
		})
	}
	dir := t.TempDir()
	writeFile(t, dir, "pumi.txtpb", "workflow: \"workflow.txtpb\"\ntrunk: \"main\"\n"+projectText+"\n"+models(signedIn, implementer, checks, keyed)+"\n")
	cfg, err := readConfig(dir + "/pumi.txtpb")
	if err != nil {
		t.Fatalf("readConfig of a good project section = %v", err)
	}
	p := cfg.project()
	if strings.Join(p.Commands, ",") != "make test" || strings.Join(p.Conventions, ",") != "AGENTS.md,docs/missing.md" ||
		strings.Join(p.ProtectedPaths, ",") != "docs/constitution.md" {
		t.Errorf("the kernel's project is %+v, want the file's project section", p)
	}
}
