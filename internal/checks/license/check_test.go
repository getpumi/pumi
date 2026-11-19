// SPDX-License-Identifier: Apache-2.0
// SPDX-FileCopyrightText: The Pumi Authors

package license

import (
	"slices"
	"testing"
)

var (
	uuid      = Module{Path: "github.com/google/uuid", Version: "v1.6.0"}
	goVersion = Module{Path: "github.com/hashicorp/go-version", Version: "v1.9.0"}
	ratelimit = Module{Path: "github.com/juju/ratelimit", Version: "v1.0.2"}
	yaml      = Module{Path: "go.yaml.in/yaml/v3", Version: "v3.0.6"}
	gofrs     = Module{Path: "github.com/gofrs/uuid", Version: "v4.4.0+incompatible"}
	std       = Module{Path: StdPath, Version: "go1.27.4"}
)

var mplException = Exception{Module: goVersion.Path, License: "MPL-2.0", Reason: "Version parsing."}

func input(t *testing.T, report, stdReport string, mods []Module, exc ...Exception) Input {
	t.Helper()
	return Input{
		Policy:     testPolicy(t),
		Exceptions: exc,
		Modules:    mods,
		Report:     readReport(t, report),
		Std:        std,
		StdReport:  readReport(t, stdReport),
	}
}

func findings(fs []Finding) []string {
	out := make([]string, len(fs))
	for i, f := range fs {
		out[i] = f.String()
	}
	return out
}

func TestEvaluate(t *testing.T) {
	for name, tc := range map[string]struct {
		in   func(t *testing.T) Input
		want []string
	}{
		"allowed licenses pass": {
			in:   func(t *testing.T) Input { return input(t, "gomod.json", "goroot-license.json", []Module{uuid, yaml}) },
			want: nil,
		},
		"no third-party module passes": {
			in:   func(t *testing.T) Input { return input(t, "gomod.json", "goroot-license.json", nil) },
			want: nil,
		},
		"a replaced module is checked by its replacement": {
			in: func(t *testing.T) Input {
				return input(t, "gomod-replace.json", "goroot-license.json", []Module{gofrs})
			},
			want: nil,
		},
		"a license that is not allowed fails": {
			in: func(t *testing.T) Input {
				return input(t, "gomod.json", "goroot-license.json", []Module{uuid, ratelimit})
			},
			want: []string{"github.com/juju/ratelimit v1.0.2: is under LGPL-3.0-only, which is not an allowed license (decision 0075)"},
		},
		"MPL-2.0 without an exception fails": {
			in: func(t *testing.T) Input { return input(t, "gomod.json", "goroot-license.json", []Module{goVersion}) },
			want: []string{
				"github.com/hashicorp/go-version v1.9.0: is under MPL-2.0, which passes only with an exception in .pumi/license-exceptions.json (decisions 0075 and 0078)",
			},
		},
		"MPL-2.0 with an exception passes": {
			in: func(t *testing.T) Input {
				return input(t, "gomod.json", "goroot-license.json", []Module{goVersion}, mplException)
			},
			want: nil,
		},
		"an exception whose license no longer matches fails": {
			in: func(t *testing.T) Input {
				exc := Exception{Module: uuid.Path, License: "MPL-2.0", Reason: "Was MPL-2.0."}
				return input(t, "gomod.json", "goroot-license.json", []Module{uuid}, exc)
			},
			want: []string{
				"github.com/google/uuid v1.6.0: has an exception for MPL-2.0, but Trivy detects BSD-3-Clause; review the module again (decision 0078)",
			},
		},
		"an exception for a module not in the binaries fails": {
			in: func(t *testing.T) Input {
				return input(t, "gomod.json", "goroot-license.json", []Module{uuid}, mplException)
			},
			want: []string{
				"github.com/hashicorp/go-version: has an exception, but no binary includes the module; remove the exception",
			},
		},
		"a module without a detected license fails": {
			in: func(t *testing.T) Input {
				return input(t, "gomod-no-license.json", "goroot-license.json", []Module{uuid, yaml})
			},
			want: []string{"github.com/google/uuid v1.6.0: Trivy detects no license"},
		},
		"a module that Trivy does not list fails": {
			in: func(t *testing.T) Input {
				return input(t, "gomod-replace.json", "goroot-license.json", []Module{uuid})
			},
			want: []string{"github.com/google/uuid v1.6.0: Trivy does not list the module; run go mod tidy"},
		},
		"every license of a module must pass": {
			in: func(t *testing.T) Input {
				in := input(t, "gomod.json", "goroot-license.json", []Module{yaml})
				in.Report.Modules["go.yaml.in/yaml/v3@v3.0.6"] = []string{"MIT", "GPL-3.0-only"}
				return in
			},
			want: []string{"go.yaml.in/yaml/v3 v3.0.6: is under GPL-3.0-only, which is not an allowed license (decision 0075)"},
		},
		"MPL-2.0 next to an allowed license passes with an exception": {
			in: func(t *testing.T) Input {
				in := input(t, "gomod.json", "goroot-license.json", []Module{goVersion}, mplException)
				in.Report.Modules["github.com/hashicorp/go-version@v1.9.0"] = []string{"MIT", "MPL-2.0"}
				return in
			},
			want: nil,
		},
		"the standard library passes with an allowed license": {
			in: func(t *testing.T) Input {
				return input(t, "gomod.json", "license-file-mit.json", nil)
			},
			want: nil,
		},
		"the standard library fails without a detected license": {
			in: func(t *testing.T) Input {
				in := input(t, "gomod.json", "goroot-license.json", nil)
				in.StdReport.Files = nil
				return in
			},
			want: []string{"std go1.27.4: Trivy detects no license"},
		},
		"the standard library fails with a license that is not allowed": {
			in: func(t *testing.T) Input {
				in := input(t, "gomod.json", "goroot-license.json", nil)
				in.StdReport.Files = []string{"GPL-2.0-only"}
				return in
			},
			want: []string{"std go1.27.4: is under GPL-2.0-only, which is not an allowed license (decision 0075)"},
		},
	} {
		t.Run(name, func(t *testing.T) {
			_, got := Evaluate(tc.in(t))
			if !slices.Equal(findings(got), tc.want) {
				t.Errorf("Evaluate() findings = %q, want %q", findings(got), tc.want)
			}
		})
	}
}

func TestEvaluateChecked(t *testing.T) {
	checked, fs := Evaluate(input(t, "gomod.json", "goroot-license.json", []Module{uuid, goVersion, yaml}, mplException))
	if len(fs) != 0 {
		t.Fatalf("Evaluate() findings = %q, want none", findings(fs))
	}
	var got []string
	for _, c := range checked {
		s := c.Module.Path + " " + c.Module.Version
		for _, l := range c.Licenses {
			s += " " + l
		}
		if c.Exception != nil {
			s += " (exception)"
		}
		got = append(got, s)
	}
	want := []string{
		"std go1.27.4 BSD-3-Clause",
		"github.com/google/uuid v1.6.0 BSD-3-Clause",
		"github.com/hashicorp/go-version v1.9.0 MPL-2.0 (exception)",
		"go.yaml.in/yaml/v3 v3.0.6 Apache-2.0 MIT",
	}
	if !slices.Equal(got, want) {
		t.Errorf("Evaluate() checked = %q, want %q", got, want)
	}
}
