// SPDX-License-Identifier: Apache-2.0
// SPDX-FileCopyrightText: The Pumi Authors

package license

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// mitText is the MIT License, which Trivy detects in a module's LICENSE file.
const mitText = `MIT License

Copyright (c) 2026 Example Authors

Permission is hereby granted, free of charge, to any person obtaining a copy
of this software and associated documentation files (the "Software"), to deal
in the Software without restriction, including without limitation the rights
to use, copy, modify, merge, publish, distribute, sublicense, and/or sell
copies of the Software, and to permit persons to whom the Software is
furnished to do so, subject to the following conditions:

The above copyright notice and this permission notice shall be included in all
copies or substantial portions of the Software.

THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR
IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY,
FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE
AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER
LIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM,
OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN THE
SOFTWARE.
`

// installScript returns the repository's own install-tools.sh, so that the
// tests ask for the pinned Trivy, and fail when the Trivy on the PATH is not
// that version.
func installScript(t *testing.T) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "..", "..", ".devcontainer", "install-tools.sh"))
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// newCheckedRepo writes a repository whose binary uses one module under the
// MIT License, with the configuration files that Run reads.
func newCheckedRepo(t *testing.T, script, exceptions string) string {
	t.Helper()
	proxy, _ := testGoEnv(t)
	addModule(t, proxy, "example.com/mit", map[string]string{"mit.go": "package mit\n", "LICENSE": mitText})
	root := t.TempDir()
	writeFiles(t, root, map[string]string{
		"go.mod":                         "module example.com/core\n\ngo 1.24\n\nrequire example.com/mit v1.0.0\n",
		"cmd/core/main.go":               "package main\n\nimport _ \"example.com/mit\"\n\nfunc main() {}\n",
		".pumi/licenses.json":            policyJSON,
		".pumi/license-exceptions.json":  exceptions,
		".devcontainer/install-tools.sh": script,
	})
	return root
}

func TestRunWithTrivy(t *testing.T) {
	root := newCheckedRepo(t, installScript(t), `{"exceptions": []}`)
	res, err := Run(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Findings) != 0 {
		t.Errorf("Run() findings = %q, want none", findings(res.Findings))
	}
	var got []string
	for _, c := range res.Checked {
		got = append(got, c.Module.Path+" "+strings.Join(c.Licenses, " "))
	}
	if want := []string{"std BSD-3-Clause", "example.com/mit MIT"}; !slices.Equal(got, want) {
		t.Errorf("Run() checked = %q, want %q", got, want)
	}
}

func TestRunIgnoresTrivyConfiguration(t *testing.T) {
	root := newCheckedRepo(t, installScript(t), `{"exceptions": []}`)
	writeFiles(t, root, map[string]string{"trivy.yaml": "license:\n  ignored:\n    - MIT\n"})
	t.Setenv("TRIVY_IGNORED_LICENSES", "MIT,BSD-3-Clause")
	res, err := Run(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Findings) != 0 || len(res.Checked) != 2 || !slices.Equal(res.Checked[1].Licenses, []string{"MIT"}) {
		t.Errorf("Run() = %+v, findings %q; want MIT detected for example.com/mit", res.Checked, findings(res.Findings))
	}
}

func TestRunNeedsTrivy(t *testing.T) {
	root := newCheckedRepo(t, installScript(t), `{"exceptions": []}`)
	gobin, err := exec.LookPath("go")
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", filepath.Dir(gobin))
	if _, err := Run(context.Background(), root); err == nil || !strings.Contains(err.Error(), "trivy") {
		t.Errorf("Run() error = %v, want one about Trivy", err)
	}
}

func TestRunFailsAStaleExceptionWithTrivy(t *testing.T) {
	root := newCheckedRepo(t, installScript(t), `{"exceptions": [{"module": "example.com/gone", "license": "MPL-2.0", "reason": "r"}]}`)
	res, err := Run(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"example.com/gone: has an exception, but no binary includes the module; remove the exception"}
	if !slices.Equal(findings(res.Findings), want) {
		t.Errorf("Run() findings = %q, want %q", findings(res.Findings), want)
	}
}

func TestRunRejectsAnotherTrivyVersion(t *testing.T) {
	root := newCheckedRepo(t, "TRIVY_VERSION=0.0.1\n", `{"exceptions": []}`)
	_, err := Run(context.Background(), root)
	if err == nil || !strings.Contains(err.Error(), "is not the pinned version 0.0.1") {
		t.Errorf("Run() error = %v, want one about the Trivy version", err)
	}
}

func TestRunRejectsAModuleCacheOutsideGOPATH(t *testing.T) {
	root := newCheckedRepo(t, installScript(t), `{"exceptions": []}`)
	t.Setenv("GOMODCACHE", t.TempDir())
	_, err := Run(context.Background(), root)
	if err == nil || !strings.Contains(err.Error(), "Trivy reads licenses only from") {
		t.Errorf("Run() error = %v, want one about GOMODCACHE", err)
	}
}

func TestRunReportsABadConfiguration(t *testing.T) {
	for _, name := range []string{PolicyPath, ExceptionsPath, InstallScript} {
		t.Run(name, func(t *testing.T) {
			root := newCheckedRepo(t, installScript(t), `{"exceptions": []}`)
			writeFiles(t, root, map[string]string{name: `{"bad": 1}`})
			_, err := Run(context.Background(), root)
			if err == nil || !strings.Contains(err.Error(), name) {
				t.Errorf("Run() error = %v, want one that names %s", err, name)
			}
		})
	}
}
