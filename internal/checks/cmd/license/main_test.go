// SPDX-License-Identifier: Apache-2.0
// SPDX-FileCopyrightText: The Pumi Authors

package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const policy = `{"allowed": ["Apache-2.0", "MIT", "BSD-2-Clause", "BSD-3-Clause", "ISC", "0BSD", "Zlib"], "by_exception": ["MPL-2.0"]}`

// newRepo writes a core module with one binary and no third-party module,
// with the license check's configuration and the repository's own
// install-tools.sh, to a temporary directory and makes it the working
// directory.
func newRepo(t *testing.T, exceptions string) {
	t.Helper()
	script, err := os.ReadFile(filepath.Join("..", "..", "..", "..", ".devcontainer", "install-tools.sh"))
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("GOPROXY", "off")
	t.Setenv("GOFLAGS", "")
	t.Chdir(t.TempDir())
	for name, src := range map[string]string{
		"go.mod":                         "module example.com/core\n\ngo 1.24\n",
		"cmd/core/main.go":               "package main\n\nfunc main() {}\n",
		".pumi/licenses.json":            policy,
		".pumi/license-exceptions.json":  exceptions,
		".devcontainer/install-tools.sh": string(script),
	} {
		if err := os.MkdirAll(filepath.Dir(name), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(name, []byte(src), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

func TestRunPasses(t *testing.T) {
	newRepo(t, `{"exceptions": []}`)
	var stdout, stderr bytes.Buffer
	if code := run(nil, &stdout, &stderr); code != 0 {
		t.Fatalf("run() = %d, want 0; stderr: %s", code, stderr.String())
	}
	lines := strings.Split(strings.TrimSuffix(stdout.String(), "\n"), "\n")
	if len(lines) != 2 || !strings.HasPrefix(lines[0], "license: std go1.") || !strings.HasSuffix(lines[0], ": BSD-3-Clause") ||
		lines[1] != "license: modules checked: 1, none fails" {
		t.Errorf("run() wrote stdout %q, want the standard library and a summary", stdout.String())
	}
	if stderr.Len() != 0 {
		t.Errorf("run() wrote stderr %q, want nothing", stderr.String())
	}
}

func TestRunWritesNotices(t *testing.T) {
	newRepo(t, `{"exceptions": []}`)
	var stdout, stderr bytes.Buffer
	if code := run([]string{"-notices", "bin/THIRD_PARTY_NOTICES"}, &stdout, &stderr); code != 0 {
		t.Fatalf("run() = %d, want 0; stderr: %s", code, stderr.String())
	}
	data, err := os.ReadFile(filepath.Join("bin", "THIRD_PARTY_NOTICES"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(data), "THIRD_PARTY_NOTICES\n") || !strings.Contains(string(data), "The Go standard library go1.") ||
		!strings.Contains(string(data), "----- LICENSE -----") {
		t.Errorf("THIRD_PARTY_NOTICES = %q, want the Go license", data)
	}
	if !strings.HasSuffix(stdout.String(), "license: wrote bin/THIRD_PARTY_NOTICES\n") {
		t.Errorf("run() wrote stdout %q, want it to name the file", stdout.String())
	}
}

func TestRunFailsAndWritesNoNotices(t *testing.T) {
	newRepo(t, `{"exceptions": [{"module": "example.com/gone", "license": "MPL-2.0", "reason": "r"}]}`)
	var stdout, stderr bytes.Buffer
	if code := run([]string{"-notices", "THIRD_PARTY_NOTICES"}, &stdout, &stderr); code != 1 {
		t.Errorf("run() = %d, want 1", code)
	}
	if want := "license: example.com/gone: has an exception, but no binary includes the module; remove the exception\n"; !strings.HasSuffix(stdout.String(), want) {
		t.Errorf("run() wrote stdout %q, want it to end with %q", stdout.String(), want)
	}
	if want := "license: findings against .pumi/licenses.json and .pumi/license-exceptions.json: 1\n"; stderr.String() != want {
		t.Errorf("run() wrote stderr %q, want %q", stderr.String(), want)
	}
	if _, err := os.Stat("THIRD_PARTY_NOTICES"); !os.IsNotExist(err) {
		t.Errorf("THIRD_PARTY_NOTICES exists (%v), want it not written when the check fails", err)
	}
}

func TestRunRejectsArguments(t *testing.T) {
	for _, args := range [][]string{{"extra"}, {"-notices"}, {"-other"}} {
		var stdout, stderr bytes.Buffer
		if code := run(args, &stdout, &stderr); code != 2 {
			t.Errorf("run(%q) = %d, want 2", args, code)
		}
		if !strings.Contains(stderr.String(), "usage: license [-notices file]") {
			t.Errorf("run(%q) wrote stderr %q, want the usage", args, stderr.String())
		}
	}
}

func TestRunReportsAnError(t *testing.T) {
	newRepo(t, `{"exceptions": []}`)
	if err := os.Remove(".pumi/licenses.json"); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	if code := run(nil, &stdout, &stderr); code != 2 {
		t.Errorf("run() = %d, want 2", code)
	}
	if !strings.HasPrefix(stderr.String(), "license: ") || !strings.Contains(stderr.String(), "licenses.json") {
		t.Errorf("run() wrote stderr %q, want the error", stderr.String())
	}
}
