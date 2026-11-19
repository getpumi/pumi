// SPDX-License-Identifier: Apache-2.0
// SPDX-FileCopyrightText: The Pumi Authors

package pins

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// recorded is when testdata/proxy.json was recorded. Its newest releases
// are golangci-lint 2.15.0, Trivy 0.76.2, buf 1.75.0 and Go 1.27.4, 1.26.11
// and 1.25.14. Go 1.27.1 was published on 2026-08-28 at 16:20:06 UTC, so a
// pin on Go 1.26 is out of date from 2026-09-28 at 16:20:06 UTC.
var recorded = time.Date(2026, 11, 19, 22, 0, 0, 0, time.UTC)

var due127 = time.Date(2026, 9, 28, 16, 20, 6, 0, time.UTC)

// currentFiles are the pinning files of a repository whose pins were all
// current when the data was recorded.
func currentFiles() map[string]string {
	return map[string]string{
		InstallScript: "#!/bin/sh\nset -eu\n\nGOLANGCI_LINT_VERSION=2.15.0\nGOLANGCI_LINT_SHA256_AMD64=abc\n\nTRIVY_VERSION=0.76.2\n",
		Dockerfile:    "FROM golang:1.27.4-trixie@sha256:f18c\n\nENV PUMI_GO_OLDER=go1.26.11\nRUN true\n",
		CoreModFile:   "module getpumi.dev/pumi\n\ngo 1.27.0\n\ntoolchain go1.27.4\n",
		APIModFile:    "module getpumi.dev/pumi/api\n\ngo 1.26.0\n\ntoolchain go1.27.4\n",
	}
}

// newRepo writes files, with the changes in changes, to a temporary
// directory and returns it. An empty value in changes removes the file.
func newRepo(t *testing.T, files, changes map[string]string) string {
	t.Helper()
	root := t.TempDir()
	for k, v := range changes {
		files[k] = v
	}
	for name, src := range files {
		if src == "" {
			continue
		}
		p := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(src), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func runAt(t *testing.T, changes map[string]string, overrides map[string]response, now time.Time) []Pin {
	t.Helper()
	pins, err := Run(t.Context(), newRepo(t, currentFiles(), changes), newProxy(t, overrides), now)
	if err != nil {
		t.Fatal(err)
	}
	return pins
}

// find returns the pin with the name in the file.
func find(t *testing.T, pins []Pin, name, file string) Pin {
	t.Helper()
	for _, p := range pins {
		if p.Name == name && p.File == file {
			return p
		}
	}
	t.Fatalf("no pin %s in %s among %v", name, file, pins)
	return Pin{}
}

func TestRunAllCurrent(t *testing.T) {
	pins := runAt(t, nil, nil, recorded)
	want := []string{
		"golangci-lint 2.15.0 in .devcontainer/install-tools.sh: current",
		"Trivy 0.76.2 in .devcontainer/install-tools.sh: current",
		"go line 1.27.0 in go.mod: current",
		"toolchain go1.27.4 in go.mod: current",
		"go line 1.26.0 in api/go.mod: current",
		"toolchain go1.27.4 in api/go.mod: current",
		"golang base image 1.27.4 in .devcontainer/Dockerfile: current",
		"PUMI_GO_OLDER go1.26.11 in .devcontainer/Dockerfile: current",
	}
	if len(pins) != len(want) {
		t.Fatalf("Run() = %v, want %d pins", pins, len(want))
	}
	for i, p := range pins {
		if p.String() != want[i] || p.OutOfDate {
			t.Errorf("pin %d = %q (out of date %v), want %q", i, p.String(), p.OutOfDate, want[i])
		}
	}
}

func TestRunNewerRelease(t *testing.T) {
	pins := runAt(t, map[string]string{
		InstallScript: "GOLANGCI_LINT_VERSION=2.14.0\nTRIVY_VERSION=0.76.1\n",
	}, nil, recorded)
	for name, newest := range map[string]string{"golangci-lint": "2.15.0", "Trivy": "0.76.2"} {
		p := find(t, pins, name, InstallScript)
		if !p.OutOfDate || p.Newest != newest {
			t.Errorf("%s = %+v, want out of date with newest %s", name, p, newest)
		}
	}
	want := "golangci-lint 2.14.0 in .devcontainer/install-tools.sh: out of date, newest 2.15.0"
	if got := find(t, pins, "golangci-lint", InstallScript).String(); got != want {
		t.Errorf("String() = %q, want %q", got, want)
	}
}

func TestRunNewMajorVersion(t *testing.T) {
	pins := runAt(t, nil, map[string]response{
		"/github.com/golangci/golangci-lint/v3/@latest": {200, `{"Version":"v3.0.1","Time":"2026-11-18T10:00:00Z"}`},
		"/github.com/golangci/golangci-lint/v4/@latest": {404, "not found"},
		"/github.com/aquasecurity/trivy/v2/@latest":     {200, `{"Version":"v2.0.0","Time":"2026-11-18T10:00:00Z"}`},
		"/github.com/aquasecurity/trivy/v3/@latest":     {410, "gone"},
	}, recorded)
	for name, newest := range map[string]string{"golangci-lint": "3.0.1", "Trivy": "2.0.0"} {
		p := find(t, pins, name, InstallScript)
		if !p.OutOfDate || p.Newest != newest {
			t.Errorf("%s = %+v, want out of date with newest %s", name, p, newest)
		}
	}
}

func TestRunNewerGoPatch(t *testing.T) {
	pins := runAt(t, map[string]string{
		CoreModFile: "module getpumi.dev/pumi\n\ngo 1.27.0\n\ntoolchain go1.27.3\n",
		Dockerfile:  "FROM golang:1.27.3-trixie@sha256:f18c\n\nENV PUMI_GO_OLDER=go1.26.10\n",
	}, nil, recorded)
	for _, c := range []struct{ name, file, newest string }{
		{"toolchain", CoreModFile, "go1.27.4"},
		{"golang base image", Dockerfile, "1.27.4"},
		{"PUMI_GO_OLDER", Dockerfile, "go1.26.11"},
	} {
		p := find(t, pins, c.name, c.file)
		if !p.OutOfDate || p.Newest != c.newest {
			t.Errorf("%s in %s = %+v, want out of date with newest %s", c.name, c.file, p, c.newest)
		}
	}
	if p := find(t, pins, "toolchain", APIModFile); p.OutOfDate {
		t.Errorf("toolchain in api/go.mod = %+v, want current", p)
	}
}

// on126 are the changes that put the repository on Go 1.26, with 1.25 as the
// older release, on the newest patch releases of both.
var on126 = map[string]string{
	Dockerfile:  "FROM golang:1.26.11-trixie@sha256:f18c\n\nENV PUMI_GO_OLDER=go1.25.14\n",
	CoreModFile: "module getpumi.dev/pumi\n\ngo 1.26.0\n\ntoolchain go1.26.11\n",
	APIModFile:  "module getpumi.dev/pumi/api\n\ngo 1.25.0\n\ntoolchain go1.26.11\n",
}

// The pins of Go 1.26 that a move to 1.27 changes, with their newest values.
var moveTo127 = []struct{ name, file, newest string }{
	{"go line", CoreModFile, "1.27.0"},
	{"toolchain", CoreModFile, "go1.27.4"},
	{"go line", APIModFile, "1.26.0"},
	{"toolchain", APIModFile, "go1.27.4"},
	{"golang base image", Dockerfile, "1.27.4"},
	{"PUMI_GO_OLDER", Dockerfile, "go1.26.11"},
}

func TestRunNewGoMinorBeforeDue(t *testing.T) {
	pins := runAt(t, on126, nil, due127.Add(-time.Second))
	for _, c := range moveTo127 {
		p := find(t, pins, c.name, c.file)
		if p.OutOfDate || p.Newest != c.newest || !p.Due.Equal(due127) {
			t.Errorf("%s in %s = %+v, want newest %s, not out of date, due %v", c.name, c.file, p, c.newest, due127)
		}
	}
	want := "go line 1.26.0 in go.mod: newest 1.27.0, out of date from 2026-09-28"
	if got := find(t, pins, "go line", CoreModFile).String(); got != want {
		t.Errorf("String() = %q, want %q", got, want)
	}
}

func TestRunNewGoMinorDue(t *testing.T) {
	pins := runAt(t, on126, nil, due127)
	for _, c := range moveTo127 {
		p := find(t, pins, c.name, c.file)
		if !p.OutOfDate || p.Newest != c.newest {
			t.Errorf("%s in %s = %+v, want out of date with newest %s", c.name, c.file, p, c.newest)
		}
	}
}

func TestRunGoMinorWithoutPatchRelease(t *testing.T) {
	// Go 1.28.0 is out, but not 1.28.1, so nothing is due for 1.28 yet and
	// the check asks for no release time of 1.28.
	list := strings.Join([]string{"v0.0.1-go1.26.11.linux-amd64", "v0.0.1-go1.27.0.linux-amd64", "v0.0.1-go1.27.1.linux-amd64", "v0.0.1-go1.27.4.linux-amd64", "v0.0.1-go1.28.0.linux-amd64"}, "\n") + "\n"
	pins := runAt(t, nil, map[string]response{"/golang.org/toolchain/@v/list": {200, list}}, recorded)
	p := find(t, pins, "toolchain", CoreModFile)
	if p.OutOfDate || p.Newest != "go1.28.0" || !p.Due.IsZero() {
		t.Errorf("toolchain = %+v, want newest go1.28.0, not out of date, no due date", p)
	}
	if got, want := p.String(), "toolchain go1.27.4 in go.mod: newest go1.28.0, not yet out of date"; got != want {
		t.Errorf("String() = %q, want %q", got, want)
	}
}

func TestRunOlderGoTooOld(t *testing.T) {
	pins := runAt(t, map[string]string{
		APIModFile: "module getpumi.dev/pumi/api\n\ngo 1.25.0\n\ntoolchain go1.27.4\n",
		Dockerfile: "FROM golang:1.27.4-trixie@sha256:f18c\n\nENV PUMI_GO_OLDER=go1.25.14\n",
	}, nil, recorded)
	for _, c := range []struct{ name, file, newest string }{
		{"go line", APIModFile, "1.26.0"},
		{"PUMI_GO_OLDER", Dockerfile, "go1.26.11"},
	} {
		p := find(t, pins, c.name, c.file)
		if !p.OutOfDate || p.Newest != c.newest {
			t.Errorf("%s in %s = %+v, want out of date with newest %s", c.name, c.file, p, c.newest)
		}
	}
}

func TestRunGoAheadOfOlder(t *testing.T) {
	// The api module may require the newest release; that is not out of date.
	pins := runAt(t, map[string]string{
		APIModFile: "module getpumi.dev/pumi/api\n\ngo 1.27.0\n\ntoolchain go1.27.4\n",
	}, nil, recorded)
	if p := find(t, pins, "go line", APIModFile); p.OutOfDate || p.Newest != "" {
		t.Errorf("go line in api/go.mod = %+v, want current", p)
	}
}

func TestRunTools(t *testing.T) {
	buf := `// SPDX-License-Identifier: Apache-2.0

module getpumi.dev/pumi/tools/buf

go 1.27.0

tool github.com/bufbuild/buf/cmd/buf

require github.com/bufbuild/buf v1.74.0

require github.com/spf13/cobra v1.10.1 // indirect
`
	pins := runAt(t, map[string]string{"tools/buf.mod": buf, "tools/README.md": "not a module file\n"}, nil, recorded)
	p := find(t, pins, "github.com/bufbuild/buf", "tools/buf.mod")
	if !p.OutOfDate || p.Newest != "v1.75.0" {
		t.Errorf("buf = %+v, want out of date with newest v1.75.0", p)
	}
	if len(pins) != 9 {
		t.Errorf("Run() = %v, want the 8 pins and buf", pins)
	}
}

func TestRunToolWithoutModule(t *testing.T) {
	_, err := Run(t.Context(), newRepo(t, currentFiles(), map[string]string{
		"tools/x.mod": "module getpumi.dev/pumi/tools/x\n\ntool example.com/x/cmd/x\n",
	}), newProxy(t, nil), recorded)
	if err == nil || !strings.Contains(err.Error(), "example.com/x/cmd/x") {
		t.Errorf("Run() error = %v, want one that names the tool", err)
	}
}

func TestRunMissingPins(t *testing.T) {
	for name, changes := range map[string]map[string]string{
		"no Trivy":         {InstallScript: "GOLANGCI_LINT_VERSION=2.15.0\n"},
		"two Trivy":        {InstallScript: "GOLANGCI_LINT_VERSION=2.15.0\nTRIVY_VERSION=0.76.2\nTRIVY_VERSION=0.76.1\n"},
		"no base image":    {Dockerfile: "FROM debian\n\nENV PUMI_GO_OLDER=go1.26.11\n"},
		"no older Go":      {Dockerfile: "FROM golang:1.27.4-trixie@sha256:f18c\n"},
		"no toolchain":     {CoreModFile: "module getpumi.dev/pumi\n\ngo 1.27.0\n"},
		"no api module":    {APIModFile: ""},
		"unknown release":  {CoreModFile: "module getpumi.dev/pumi\n\ngo 1.27.0\n\ntoolchain go1.27.9\n"},
		"bad Go version":   {Dockerfile: "FROM golang:1.27-trixie\n\nENV PUMI_GO_OLDER=go1.26.11\n"},
		"bad tool version": {InstallScript: "GOLANGCI_LINT_VERSION=latest\nTRIVY_VERSION=0.76.2\n"},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := Run(t.Context(), newRepo(t, currentFiles(), changes), newProxy(t, nil), recorded); err == nil {
				t.Error("Run() returned no error")
			}
		})
	}
}

// list128 is a list of Go releases in which 1.28.1 is out but not yet one
// month old as of recorded.
var list128 = strings.Join([]string{
	"v0.0.1-go1.25.14.linux-amd64",
	"v0.0.1-go1.26.0.linux-amd64", "v0.0.1-go1.26.1.linux-amd64", "v0.0.1-go1.26.11.linux-amd64",
	"v0.0.1-go1.27.0.linux-amd64", "v0.0.1-go1.27.1.linux-amd64", "v0.0.1-go1.27.4.linux-amd64",
	"v0.0.1-go1.28.0.linux-amd64", "v0.0.1-go1.28.1.linux-amd64",
}, "\n") + "\n"

var due128 = time.Date(2026, 12, 12, 9, 0, 0, 0, time.UTC)

func TestRunGoPatchBehindBeforeNewMinorDue(t *testing.T) {
	pins := runAt(t, map[string]string{
		CoreModFile: "module getpumi.dev/pumi\n\ngo 1.27.0\n\ntoolchain go1.27.3\n",
		Dockerfile:  "FROM golang:1.27.4-trixie@sha256:f18c\n\nENV PUMI_GO_OLDER=go1.26.10\n",
	}, map[string]response{
		"/golang.org/toolchain/@v/list":                             {200, list128},
		"/golang.org/toolchain/@v/v0.0.1-go1.28.1.linux-amd64.info": {200, `{"Version":"v0.0.1-go1.28.1.linux-amd64","Time":"2026-11-12T09:00:00Z"}`},
	}, recorded)
	// The patch release of the pin's own minor release is due now; the move
	// to 1.28 is not.
	for _, c := range []struct{ name, file, newest string }{
		{"toolchain", CoreModFile, "go1.27.4"},
		{"PUMI_GO_OLDER", Dockerfile, "go1.26.11"},
	} {
		p := find(t, pins, c.name, c.file)
		if !p.OutOfDate || p.Newest != c.newest {
			t.Errorf("%s in %s = %+v, want out of date with newest %s", c.name, c.file, p, c.newest)
		}
	}
	// A pin on the newest patch release of 1.27 shows 1.28 and its due date.
	p := find(t, pins, "golang base image", Dockerfile)
	if p.OutOfDate || p.Newest != "1.28.1" || !p.Due.Equal(due128) {
		t.Errorf("golang base image = %+v, want newest 1.28.1, not out of date, due %v", p, due128)
	}
}

func TestRunDueDateTwoMinorsBehind(t *testing.T) {
	// The pins are on 1.26, 1.27 is not due yet, and 1.28.0 has no patch
	// release. The pins become out of date when 1.27 is due.
	list := strings.Join([]string{
		"v0.0.1-go1.25.14.linux-amd64", "v0.0.1-go1.26.1.linux-amd64", "v0.0.1-go1.26.11.linux-amd64",
		"v0.0.1-go1.27.1.linux-amd64", "v0.0.1-go1.27.4.linux-amd64", "v0.0.1-go1.28.0.linux-amd64",
	}, "\n") + "\n"
	pins := runAt(t, on126, map[string]response{"/golang.org/toolchain/@v/list": {200, list}}, due127.Add(-time.Second))
	for _, c := range []struct{ name, file string }{{"go line", CoreModFile}, {"PUMI_GO_OLDER", Dockerfile}} {
		if p := find(t, pins, c.name, c.file); p.OutOfDate || !p.Due.Equal(due127) {
			t.Errorf("%s in %s = %+v, want not out of date, due %v", c.name, c.file, p, due127)
		}
	}
}

func TestRunNewMajorPrerelease(t *testing.T) {
	pins := runAt(t, nil, map[string]response{
		"/github.com/golangci/golangci-lint/v3/@latest": {200, `{"Version":"v3.0.0-beta.1","Time":"2026-11-18T10:00:00Z"}`},
	}, recorded)
	if p := find(t, pins, "golangci-lint", InstallScript); p.OutOfDate || p.Newest != "" {
		t.Errorf("golangci-lint = %+v, want current: a prerelease is not a release", p)
	}
}

func TestRunToolMajorFromPath(t *testing.T) {
	mod := `module getpumi.dev/pumi/tools/x

tool gopkg.in/yaml.v3/cmd/y
tool example.com/foo/cmd/foo

require gopkg.in/yaml.v3 v3.0.1
require example.com/foo v4.0.0+incompatible
`
	pins := runAt(t, map[string]string{"tools/x.mod": mod}, map[string]response{
		"/gopkg.in/yaml.v3/@latest":   {200, `{"Version":"v3.0.1"}`},
		"/example.com/foo/@latest":    {200, `{"Version":"v4.1.0+incompatible"}`},
		"/example.com/foo/v2/@latest": {404, "not found"},
	}, recorded)
	if p := find(t, pins, "gopkg.in/yaml.v3", "tools/x.mod"); p.OutOfDate {
		t.Errorf("yaml.v3 = %+v, want current", p)
	}
	if p := find(t, pins, "example.com/foo", "tools/x.mod"); !p.OutOfDate || p.Newest != "v4.1.0+incompatible" {
		t.Errorf("foo = %+v, want out of date with newest v4.1.0+incompatible", p)
	}
}
