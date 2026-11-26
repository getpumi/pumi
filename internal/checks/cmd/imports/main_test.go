// SPDX-License-Identifier: Apache-2.0
// SPDX-FileCopyrightText: The Pumi Authors

package main

import (
	"bytes"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"getpumi.dev/pumi/internal/checks/imports"
)

const config = `{
  "api": {"path": "api", "modules": []},
  "conformance": {"path": "conformance", "modules": []},
  "components": [
    {"name": "kernel", "path": "internal/kernel", "modules": []},
    {"name": "runner", "path": "internal/runner", "modules": []}
  ],
  "shared": {"path": "internal/shared"},
  "checks": {"path": "internal/checks", "modules": []},
  "commands": {"path": "cmd", "modules": []}
}`

// newModule writes a small core module, with its api and conformance
// modules and .pumi/imports.json, to a temporary directory and makes it the working
// directory. The kernel imports the runner when broken is true.
func newModule(t *testing.T, broken bool) {
	t.Helper()
	t.Setenv("GOPROXY", "off")
	t.Setenv("GOFLAGS", "")
	t.Chdir(t.TempDir())
	kernel := "package kernel\n"
	if broken {
		kernel += "\nimport _ \"example.com/core/internal/runner\"\n"
	}
	for name, src := range map[string]string{
		"go.mod":                     "module example.com/core\n\ngo 1.26.0\n\nrequire example.com/core/api v0.0.0\n\nreplace example.com/core/api => ./api\n",
		"api/go.mod":                 "module example.com/core/api\n\ngo 1.26.0\n",
		"api/api.go":                 "package api\n",
		"conformance/go.mod":         "module example.com/core/conformance\n\ngo 1.26.0\n",
		"conformance/conformance.go": "package conformance\n",
		"internal/kernel/kernel.go":  kernel,
		"internal/runner/runner.go":  "package runner\n",
		"internal/shared/shared.go":  "package shared\n\nimport _ \"example.com/core/api\"\n",
		"internal/checks/c/c.go":     "package c\n",
		"cmd/core/main.go":           "package main\n\nimport (\n\t_ \"example.com/core/internal/kernel\"\n\t_ \"example.com/core/internal/runner\"\n)\n\nfunc main() {}\n",
		".pumi/imports.json":         config,
	} {
		if err := os.MkdirAll(filepath.Dir(name), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(name, []byte(src), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

func TestRunPassesAGoodModule(t *testing.T) {
	newModule(t, false)
	var stdout, stderr bytes.Buffer
	if code := run(nil, &stdout, &stderr); code != 0 {
		t.Errorf("run() = %d, want 0; stderr: %s", code, stderr.String())
	}
	want := "imports: cmd/core: third-party modules: 0 direct, 0 indirect\n" +
		"imports: packages checked: 5, none fails\n"
	if got := stdout.String(); got != want {
		t.Errorf("run() wrote stdout %q, want %q", got, want)
	}
	if stderr.Len() != 0 {
		t.Errorf("run() wrote stderr %q, want nothing", stderr.String())
	}
}

func TestRunFailsABrokenRule(t *testing.T) {
	newModule(t, true)
	var stdout, stderr bytes.Buffer
	if code := run(nil, &stdout, &stderr); code != 1 {
		t.Errorf("run() = %d, want 1", code)
	}
	want := "example.com/core/internal/kernel imports example.com/core/internal/runner: a component never imports another component; only the main packages under cmd/ import more than one component (decision 0063)\n" +
		"imports: cmd/core: third-party modules: 0 direct, 0 indirect\n"
	if got := stdout.String(); got != want {
		t.Errorf("run() wrote stdout %q, want %q", got, want)
	}
	if got, want := stderr.String(), "imports: findings against the rules of .pumi/imports.json: 1\n"; got != want {
		t.Errorf("run() wrote stderr %q, want %q", got, want)
	}
}

func TestRunFailsWithoutAConfiguration(t *testing.T) {
	newModule(t, false)
	if err := os.Remove(".pumi/imports.json"); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	if code := run(nil, &stdout, &stderr); code != 2 {
		t.Errorf("run() = %d, want 2", code)
	}
	if !strings.HasPrefix(stderr.String(), "imports: open .pumi/imports.json: ") {
		t.Errorf("run() wrote stderr %q, want an error about .pumi/imports.json", stderr.String())
	}
}

func TestRunFailsOnABadConfiguration(t *testing.T) {
	newModule(t, false)
	if err := os.WriteFile(".pumi/imports.json", []byte(strings.Replace(config, `"path": "api"`, `"path": ""`, 1)), 0o600); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	if code := run(nil, &stdout, &stderr); code != 2 {
		t.Errorf("run() = %d, want 2", code)
	}
	if got, want := stderr.String(), "imports: .pumi/imports.json: \"api.path\" is empty\n"; got != want {
		t.Errorf("run() wrote stderr %q, want %q", got, want)
	}
}

func TestRunRejectsArguments(t *testing.T) {
	newModule(t, false)
	var stdout, stderr bytes.Buffer
	if code := run([]string{"x"}, &stdout, &stderr); code != 2 {
		t.Errorf("run() = %d, want 2", code)
	}
	if got, want := stderr.String(), "usage: imports\n"; got != want {
		t.Errorf("run() wrote stderr %q, want %q", got, want)
	}
}

// TestRunPassesTheRepository runs the check on this repository with its
// .pumi/imports.json, as make imports does.
func TestRunPassesTheRepository(t *testing.T) {
	t.Chdir("../../../..")
	var stdout, stderr bytes.Buffer
	if code := run(nil, &stdout, &stderr); code != 0 {
		t.Errorf("run() = %d, want 0; stdout: %s; stderr: %s", code, stdout.String(), stderr.String())
	}
	want := regexp.MustCompile(`\Aimports: cmd/pumi: third-party modules: \d+ direct.*\n` +
		`imports: cmd/pumi-gateway: .*\n` +
		`imports: cmd/pumi-kernel: .*\n` +
		`imports: cmd/pumi-runner: .*\n` +
		`imports: cmd/pumi-server: .*\n` +
		`imports: packages checked: \d+, none fails\n\z`)
	if !want.MatchString(stdout.String()) {
		t.Errorf("run() wrote stdout %q, want it to match %s", stdout.String(), want)
	}
}

func TestFormatBinary(t *testing.T) {
	tests := []struct {
		bin  imports.Binary
		want string
	}{
		{imports.Binary{Path: "cmd/a"}, "imports: cmd/a: third-party modules: 0 direct, 0 indirect"},
		{imports.Binary{Path: "cmd/a", Direct: []string{"x.org/a", "x.org/b"}, Indirect: []string{"x.org/c"}},
			"imports: cmd/a: third-party modules: 2 direct (x.org/a, x.org/b), 1 indirect (x.org/c)"},
	}
	for _, tt := range tests {
		if got := formatBinary(tt.bin); got != tt.want {
			t.Errorf("formatBinary(%+v) = %q, want %q", tt.bin, got, tt.want)
		}
	}
}
