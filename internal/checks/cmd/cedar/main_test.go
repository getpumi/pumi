// SPDX-License-Identifier: Apache-2.0
// SPDX-FileCopyrightText: The Pumi Authors

package main

import (
	"bytes"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// The tests run the cedar on the PATH, which in the devcontainer is the
// pinned Rust Cedar CLI.

// root is the repository's root, relative to this package.
const root = "../../../.."

// copyRepo copies the schema, the invariants and the Dockerfile of the
// repository, and the extra files, to a temporary directory and makes it the
// working directory, as the files of a new git repository.
func copyRepo(t *testing.T, extra map[string]string) {
	t.Helper()
	files := map[string]string{}
	for _, name := range []string{"api/policy/pumi.cedarschema", "api/policy/invariants.cedar", ".devcontainer/Dockerfile"} {
		data, err := fs.ReadFile(os.DirFS(root), name)
		if err != nil {
			t.Fatal(err)
		}
		files[name] = string(data)
	}
	for k, v := range extra {
		files[k] = v
	}
	t.Chdir(t.TempDir())
	for name, src := range files {
		if err := os.MkdirAll(filepath.Dir(name), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(name, []byte(src), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	for _, cmd := range []*exec.Cmd{
		exec.CommandContext(t.Context(), "git", "init", "--quiet"),
		exec.CommandContext(t.Context(), "git", "add", "--all"),
	} {
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%v: %v\n%s", cmd.Args, err, out)
		}
	}
}

func TestRunPasses(t *testing.T) {
	copyRepo(t, nil)
	var stdout, stderr bytes.Buffer
	if code := run(nil, &stdout, &stderr); code != 0 {
		t.Fatalf("run() = %d, want 0; stderr: %s", code, stderr.String())
	}
	if !strings.HasSuffix(stdout.String(), "cedar: policy files checked: 1, test files run: 0\n") {
		t.Errorf("run() wrote stdout %q, want the steps and a summary", stdout.String())
	}
	if stderr.Len() != 0 {
		t.Errorf("run() wrote stderr %q, want nothing", stderr.String())
	}
}

func TestRunFindings(t *testing.T) {
	copyRepo(t, map[string]string{"x/a.cedar": "@id(\"a\") permit(principal,action,resource);\n"})
	var stdout, stderr bytes.Buffer
	if code := run(nil, &stdout, &stderr); code != 1 {
		t.Fatalf("run() = %d, want 1; stderr: %s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "cedar: format x/a.cedar: fails\n") {
		t.Errorf("run() wrote stdout %q, want the failing step", stdout.String())
	}
	if !strings.HasPrefix(stderr.String(), "cedar: format x/a.cedar: exit status 1\n") {
		t.Errorf("run() wrote stderr %q, want the finding", stderr.String())
	}
}

func TestRunCannotRun(t *testing.T) {
	t.Chdir(t.TempDir())
	var stdout, stderr bytes.Buffer
	if code := run(nil, &stdout, &stderr); code != 2 {
		t.Fatalf("run() = %d, want 2", code)
	}
	if !strings.HasPrefix(stderr.String(), "cedar: ") {
		t.Errorf("run() wrote stderr %q, want the error", stderr.String())
	}
}

func TestRunUsage(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := run([]string{"extra"}, &stdout, &stderr); code != 2 || stderr.String() != "usage: cedar\n" {
		t.Errorf("run(extra) = %d, stderr %q; want 2 and the usage", code, stderr.String())
	}
}
