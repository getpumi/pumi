// SPDX-License-Identifier: Apache-2.0
// SPDX-FileCopyrightText: The Pumi Authors

package lintconfig

import (
	"bytes"
	"encoding/json"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"testing"
)

// The tests run golangci-lint with the repository's configuration. The paths
// are constants, because gosec reports a command that is built from variables
// (G204). go test runs the tests in the directory of this package.
const (
	// config is the configuration, relative to the directory of this package.
	config = "../../../.golangci.yml"
	// fixtureConfig is the same file, relative to a fixture in testdata.
	fixtureConfig = "../../" + config
)

type issue struct {
	FromLinter string
	Text       string
	Pos        struct {
		Filename string
		Line     int
	}
}

// output runs cmd and returns its standard output. Any exit status other than
// 0 fails the test.
func output(t *testing.T, cmd *exec.Cmd) []byte {
	t.Helper()
	if _, err := exec.LookPath("golangci-lint"); err != nil {
		t.Fatalf("golangci-lint is not on the PATH; run the tests in the devcontainer: %v", err)
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("%s: %v\nstdout:\n%s\nstderr:\n%s", cmd, err, stdout.String(), stderr.String())
	}
	return stdout.Bytes()
}

// lint runs golangci-lint on one fixture package in testdata and returns the
// linters that reported each issue.
func lint(t *testing.T, fixture string) []string {
	t.Helper()
	cmd := exec.CommandContext(t.Context(), "golangci-lint", "run",
		"--config", fixtureConfig,
		"--issues-exit-code", "0",
		"--output.text.path", "",
		"--output.json.path", "stdout",
		"--show-stats=false",
		".")
	cmd.Dir = filepath.Join("testdata", fixture)
	out := output(t, cmd)
	var report struct{ Issues []issue }
	if err := json.Unmarshal(out, &report); err != nil {
		t.Fatalf("reading the golangci-lint report: %v\n%s", err, out)
	}
	linters := []string{}
	for _, i := range report.Issues {
		t.Logf("%s:%d: %s (%s)", i.Pos.Filename, i.Pos.Line, i.Text, i.FromLinter)
		linters = append(linters, i.FromLinter)
	}
	return linters
}

// names returns the sorted names in the output of "golangci-lint linters
// --json" or "golangci-lint formatters --json".
func names(t *testing.T, out []byte) []string {
	t.Helper()
	var list struct{ Enabled []struct{ Name string } }
	if err := json.Unmarshal(out, &list); err != nil {
		t.Fatalf("reading the golangci-lint list: %v\n%s", err, out)
	}
	names := []string{}
	for _, l := range list.Enabled {
		names = append(names, l.Name)
	}
	slices.Sort(names)
	return names
}

// TestEnabledLinters checks that the configuration enables exactly the 20
// linters of decision 0109, which shows that it does not inherit golangci-lint's
// default set.
func TestEnabledLinters(t *testing.T) {
	want := []string{
		"bodyclose", "contextcheck", "dupl", "errcheck", "errorlint",
		"exhaustive", "forcetypeassert", "gocognit", "godox", "gosec",
		"govet", "ineffassign", "misspell", "nilerr", "noctx",
		"nolintlint", "revive", "staticcheck", "unparam", "unused",
	}
	cmd := exec.CommandContext(t.Context(), "golangci-lint", "linters", "--config", config, "--json")
	if got := names(t, output(t, cmd)); !reflect.DeepEqual(got, want) {
		t.Errorf("enabled linters = %q, want %q", got, want)
	}
}

func TestEnabledFormatters(t *testing.T) {
	want := []string{"gofmt", "goimports"}
	cmd := exec.CommandContext(t.Context(), "golangci-lint", "formatters", "--config", config, "--json")
	if got := names(t, output(t, cmd)); !reflect.DeepEqual(got, want) {
		t.Errorf("enabled formatters = %q, want %q", got, want)
	}
}

func TestThresholds(t *testing.T) {
	tests := []struct {
		fixture string
		want    []string
	}{
		{"complexity15", []string{}},
		{"complexity16", []string{"gocognit"}},
		{"todo", []string{"godox"}},
	}
	for _, tt := range tests {
		t.Run(tt.fixture, func(t *testing.T) {
			if got := lint(t, tt.fixture); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("golangci-lint reported issues from %q, want %q", got, tt.want)
			}
		})
	}
}
