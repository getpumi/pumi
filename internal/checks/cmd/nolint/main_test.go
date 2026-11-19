// SPDX-License-Identifier: Apache-2.0
// SPDX-FileCopyrightText: The Pumi Authors

package main

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeFile(t *testing.T, name, src string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(name), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(name, []byte(src), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestRunPassesWithoutDirectives(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "p.go"), "package p\n\n// f does nothing.\nfunc f() {}\n")
	var stdout, stderr bytes.Buffer
	if code := run([]string{dir}, &stdout, &stderr); code != 0 {
		t.Errorf("run() = %d, want 0; stderr: %s", code, stderr.String())
	}
	if stdout.Len() != 0 || stderr.Len() != 0 {
		t.Errorf("run() wrote stdout %q, stderr %q; want nothing", stdout.String(), stderr.String())
	}
}

func TestRunFailsOnDirectives(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "a", "p.go"), "package p\n\nvar x = 1 //nolint:gosec\n")
	writeFile(t, filepath.Join(dir, "b.go"), "package p\n\n//nolint\nfunc f() {}\n")
	var stdout, stderr bytes.Buffer
	if code := run([]string{dir}, &stdout, &stderr); code != 1 {
		t.Errorf("run() = %d, want 1", code)
	}
	want := "a/p.go:3:11: //nolint:gosec: suppress a finding with an exclusion rule in .golangci.yml, not a nolint comment (decision 0110)\n" +
		"b.go:3:1: //nolint: suppress a finding with an exclusion rule in .golangci.yml, not a nolint comment (decision 0110)\n"
	if got := stdout.String(); got != want {
		t.Errorf("run() wrote stdout %q, want %q", got, want)
	}
	if got := stderr.String(); got != "nolint: nolint comments found: 2\n" {
		t.Errorf("run() wrote stderr %q, want the count", got)
	}
}

func TestRunDefaultsToTheWorkingDirectory(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "p.go"), "package p\n\n//nolint\nfunc f() {}\n")
	t.Chdir(dir)
	var stdout, stderr bytes.Buffer
	if code := run(nil, &stdout, &stderr); code != 1 {
		t.Errorf("run() = %d, want 1", code)
	}
	if !strings.HasPrefix(stdout.String(), "p.go:3:1: //nolint: ") {
		t.Errorf("run() wrote stdout %q, want the finding in p.go", stdout.String())
	}
}

func TestRunReportsUsageErrors(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want string
	}{
		{"two directories", []string{"a", "b"}, "usage: nolint [directory]\n"},
		{"missing directory", []string{filepath.Join(t.TempDir(), "missing")}, "nolint: "},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			if code := run(tt.args, &stdout, &stderr); code != 2 {
				t.Errorf("run() = %d, want 2", code)
			}
			if !strings.HasPrefix(stderr.String(), tt.want) {
				t.Errorf("run() wrote stderr %q, want it to start with %q", stderr.String(), tt.want)
			}
		})
	}
}

type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) {
	return 0, errors.New("disk full")
}

func TestRunFailsWhenItCannotWriteTheFindings(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "p.go"), "package p\n\n//nolint\nfunc f() {}\n")
	var stderr bytes.Buffer
	if code := run([]string{dir}, failingWriter{}, &stderr); code != 2 {
		t.Errorf("run() = %d, want 2", code)
	}
	if got, want := stderr.String(), "nolint: disk full\n"; got != want {
		t.Errorf("run() wrote stderr %q, want %q", got, want)
	}
}
