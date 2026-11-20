// SPDX-License-Identifier: Apache-2.0
// SPDX-FileCopyrightText: The Pumi Authors

package main

import (
	"bytes"
	"encoding/json"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// serve serves the responses that internal/checks/pins recorded in the
// file testdata/<name>.json, and returns the server's URL.
func serve(t *testing.T, name string) string {
	t.Helper()
	data, err := fs.ReadFile(os.DirFS("../../pins/testdata"), name+".json")
	if err != nil {
		t.Fatal(err)
	}
	var rec struct {
		Responses map[string]struct {
			Status int    `json:"status"`
			Body   string `json:"body"`
		} `json:"responses"`
	}
	if err := json.Unmarshal(data, &rec); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp, ok := rec.Responses[r.URL.Path]
		if !ok {
			http.Error(w, "not recorded", http.StatusInternalServerError)
			return
		}
		w.WriteHeader(resp.Status)
		_, _ = w.Write([]byte(resp.Body))
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

// servers returns the arguments that point the report at the recorded
// module proxy, from proxy.golang.org on 2026-11-19, and the recorded
// crates.io API, from 2026-11-20.
func servers(t *testing.T) []string {
	t.Helper()
	return []string{"-proxy", serve(t, "proxy"), "-crates", serve(t, "crates")}
}

// newRepo writes the pinning files to a temporary directory, with the
// golangci-lint version given, and makes it the working directory. The other
// pins are the newest releases in the recorded data.
func newRepo(t *testing.T, golangciLint string) {
	t.Helper()
	t.Chdir(t.TempDir())
	for name, src := range map[string]string{
		".devcontainer/install-tools.sh": "GOLANGCI_LINT_VERSION=" + golangciLint + "\nTRIVY_VERSION=0.76.2\n",
		".devcontainer/Dockerfile":       "FROM rust:1.100.0-trixie@sha256:223c AS cedar\nARG CEDAR_VERSION=4.15.0\nFROM golang:1.27.4-trixie@sha256:f18c\nENV PUMI_GO_OLDER=go1.26.11\n",
		"go.mod":                         "module getpumi.dev/pumi\n\ngo 1.27.0\n\ntoolchain go1.27.4\n",
		"api/go.mod":                     "module getpumi.dev/pumi/api\n\ngo 1.26.0\n\ntoolchain go1.27.4\n",
	} {
		if err := os.MkdirAll(filepath.Dir(name), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(name, []byte(src), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

func TestRunCurrent(t *testing.T) {
	args := servers(t)
	newRepo(t, "2.15.0")
	var stdout, stderr bytes.Buffer
	if code := run(args, &stdout, &stderr); code != 0 {
		t.Fatalf("run() = %d, want 0; stderr: %s", code, stderr.String())
	}
	lines := strings.Split(strings.TrimSuffix(stdout.String(), "\n"), "\n")
	if len(lines) != 10 || lines[0] != "pins: golangci-lint 2.15.0 in .devcontainer/install-tools.sh: current" ||
		lines[2] != "pins: cedar-policy-cli 4.15.0 in .devcontainer/Dockerfile: current" ||
		lines[9] != "pins: pins checked: 9, none is out of date" {
		t.Errorf("run() wrote stdout %q, want 9 pins and a summary", stdout.String())
	}
	if stderr.Len() != 0 {
		t.Errorf("run() wrote stderr %q, want nothing", stderr.String())
	}
}

func TestRunOutOfDate(t *testing.T) {
	args := servers(t)
	newRepo(t, "2.14.0")
	var stdout, stderr bytes.Buffer
	if code := run(args, &stdout, &stderr); code != 1 {
		t.Fatalf("run() = %d, want 1; stderr: %s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "pins: golangci-lint 2.14.0 in .devcontainer/install-tools.sh: out of date, newest 2.15.0\n") {
		t.Errorf("run() wrote stdout %q, want the out-of-date pin", stdout.String())
	}
	if want := "pins: out of date: 1; update each in a pull request of its own (decision 0116)\n"; stderr.String() != want {
		t.Errorf("run() wrote stderr %q, want %q", stderr.String(), want)
	}
}

func TestRunCannotRun(t *testing.T) {
	args := servers(t)
	newRepo(t, "latest")
	var stdout, stderr bytes.Buffer
	if code := run(args, &stdout, &stderr); code != 2 {
		t.Fatalf("run() = %d, want 2", code)
	}
	if !strings.HasPrefix(stderr.String(), "pins: .devcontainer/install-tools.sh: ") {
		t.Errorf("run() wrote stderr %q, want the error", stderr.String())
	}
}

func TestRunUsage(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := run([]string{"extra"}, &stdout, &stderr); code != 2 || stderr.String() != "usage: pins [-proxy url] [-crates url]\n" {
		t.Errorf("run(extra) = %d, stderr %q; want 2 and the usage", code, stderr.String())
	}
}
