// SPDX-License-Identifier: Apache-2.0
// SPDX-FileCopyrightText: The Pumi Authors

package license

import (
	"io/fs"
	"os"
	"slices"
	"strings"
	"testing"
)

// The reports in testdata were recorded with the pinned Trivy, 0.76.2:
//
//   - gomod.json: trivy fs --scanners license on the go.mod of a module that
//     requires github.com/google/uuid, github.com/hashicorp/go-version,
//     github.com/juju/ratelimit and go.yaml.in/yaml/v3 in its binary and
//     github.com/stretchr/testify in a test.
//   - gomod-no-license.json: the same, with a module cache that holds only
//     github.com/google/uuid, without its LICENSE file, and go.yaml.in/yaml/v3.
//   - gomod-replace.json: a go.mod that replaces github.com/google/uuid with
//     github.com/gofrs/uuid v4.4.0+incompatible.
//   - goroot-license.json: trivy fs --scanners license --license-full on the
//     LICENSE file of a Go installation.
//   - license-file-mit.json: the same on the MIT LICENSE of testify.
func readReport(t *testing.T, name string) *Report {
	t.Helper()
	data, err := fs.ReadFile(os.DirFS("testdata"), name)
	if err != nil {
		t.Fatal(err)
	}
	r, err := ParseReport(data)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestParseReportGoMod(t *testing.T) {
	r := readReport(t, "gomod.json")
	if r.Version != "0.76.2" {
		t.Errorf("Version = %q, want 0.76.2", r.Version)
	}
	want := map[string][]string{
		"github.com/google/uuid@v1.6.0":                        {"BSD-3-Clause"},
		"github.com/hashicorp/go-version@v1.9.0":               {"MPL-2.0"},
		"github.com/juju/ratelimit@v1.0.2":                     {"LGPL-3.0-only"},
		"github.com/stretchr/testify@v1.12.1":                  {"MIT"},
		"go.yaml.in/yaml/v3@v3.0.6":                            {"Apache-2.0", "MIT"},
		"gopkg.in/check.v1@v1.0.0-20201130134442-10cb98267c6c": {"BSD-2-Clause"},
	}
	if len(r.Modules) != len(want) {
		t.Errorf("Modules = %v, want %v", r.Modules, want)
	}
	for k, w := range want {
		if got, ok := r.Modules[k]; !ok || !slices.Equal(got, w) {
			t.Errorf("Modules[%s] = %v, %v; want %v", k, got, ok, w)
		}
	}
	if len(r.Files) != 0 {
		t.Errorf("Files = %v, want none", r.Files)
	}
}

func TestParseReportNoLicense(t *testing.T) {
	r := readReport(t, "gomod-no-license.json")
	got, ok := r.Modules["github.com/google/uuid@v1.6.0"]
	if !ok || len(got) != 0 {
		t.Errorf("Modules[uuid] = %v, %v; want listed with no license", got, ok)
	}
}

func TestParseReportReplace(t *testing.T) {
	r := readReport(t, "gomod-replace.json")
	if got := r.Modules["github.com/gofrs/uuid@v4.4.0+incompatible"]; !slices.Equal(got, []string{"MIT"}) {
		t.Errorf("Modules[gofrs/uuid] = %v, want [MIT]; all: %v", got, r.Modules)
	}
}

func TestParseReportLicenseFile(t *testing.T) {
	if got := readReport(t, "goroot-license.json").Files; !slices.Equal(got, []string{"BSD-3-Clause"}) {
		t.Errorf("Files = %v, want [BSD-3-Clause]", got)
	}
	if got := readReport(t, "license-file-mit.json").Files; !slices.Equal(got, []string{"MIT"}) {
		t.Errorf("Files = %v, want [MIT]", got)
	}
}

func TestParseReportRejects(t *testing.T) {
	for name, tc := range map[string]struct{ data, want string }{
		"not json":       {"Trivy", "invalid character"},
		"schema":         {`{"SchemaVersion": 3, "Trivy": {"Version": "0.76.2"}}`, "schema version 3"},
		"no version":     {`{"SchemaVersion": 2, "Trivy": {"Version": ""}}`, "no Trivy version"},
		"two go.mod":     {`{"SchemaVersion": 2, "Trivy": {"Version": "1"}, "Results": [{"Target": "go.mod", "Class": "lang-pkgs", "Type": "gomod"}, {"Target": "go.mod", "Class": "lang-pkgs", "Type": "gomod"}]}`, "more than one"},
		"other go.mod":   {`{"SchemaVersion": 2, "Trivy": {"Version": "1"}, "Results": [{"Target": "api/go.mod", "Class": "lang-pkgs", "Type": "gomod"}]}`, "api/go.mod"},
		"other language": {`{"SchemaVersion": 2, "Trivy": {"Version": "1"}, "Results": [{"Target": "package-lock.json", "Class": "lang-pkgs", "Type": "npm"}]}`, "npm"},
		"listed twice":   {`{"SchemaVersion": 2, "Trivy": {"Version": "1"}, "Results": [{"Target": "go.mod", "Class": "lang-pkgs", "Type": "gomod", "Packages": [{"Name": "m", "Version": "v1"}, {"Name": "m", "Version": "v1"}]}]}`, "m@v1 is listed twice"},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := ParseReport([]byte(tc.data))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("ParseReport() error = %v, want one containing %q", err, tc.want)
			}
		})
	}
}
