// SPDX-License-Identifier: Apache-2.0
// SPDX-FileCopyrightText: The Pumi Authors

package license

import (
	"slices"
	"strings"
	"testing"
)

const policyJSON = `{
  "allowed": ["Apache-2.0", "MIT", "BSD-2-Clause", "BSD-3-Clause", "ISC", "0BSD", "Zlib"],
  "by_exception": ["MPL-2.0"]
}`

func TestParsePolicy(t *testing.T) {
	p, err := ParsePolicy([]byte(policyJSON))
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(p.Allowed, []string{"Apache-2.0", "MIT", "BSD-2-Clause", "BSD-3-Clause", "ISC", "0BSD", "Zlib"}) {
		t.Errorf("Allowed = %v", p.Allowed)
	}
	if !slices.Equal(p.ByException, []string{"MPL-2.0"}) {
		t.Errorf("ByException = %v", p.ByException)
	}
}

func TestParsePolicyRejects(t *testing.T) {
	for name, tc := range map[string]struct{ data, want string }{
		"unknown field":   {`{"allowed": ["MIT"], "by_exception": [], "extra": 1}`, "unknown field"},
		"trailing data":   {`{"allowed": ["MIT"], "by_exception": []} {}`, "data after"},
		"empty allowed":   {`{"allowed": [], "by_exception": []}`, `"allowed" is empty`},
		"missing by":      {`{"allowed": ["MIT"]}`, `"by_exception" is missing`},
		"blank license":   {`{"allowed": [" "], "by_exception": []}`, "is empty"},
		"listed twice":    {`{"allowed": ["MIT", "MIT"], "by_exception": []}`, "MIT is listed twice"},
		"in both lists":   {`{"allowed": ["MIT"], "by_exception": ["MIT"]}`, "MIT is listed twice"},
		"not json":        {`allowed`, "invalid character"},
		"comment in json": {"{\"allowed\": [\"MIT\"], // no\n \"by_exception\": []}", "invalid character"},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := ParsePolicy([]byte(tc.data))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("ParsePolicy() error = %v, want one containing %q", err, tc.want)
			}
		})
	}
}

func testPolicy(t *testing.T) *Policy {
	t.Helper()
	p, err := ParsePolicy([]byte(policyJSON))
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestParseExceptions(t *testing.T) {
	p := testPolicy(t)
	got, err := ParseExceptions([]byte(`{"exceptions": []}`), p)
	if err != nil || len(got) != 0 {
		t.Errorf("ParseExceptions(empty) = %v, %v; want no exceptions", got, err)
	}
	got, err = ParseExceptions([]byte(`{"exceptions": [
	  {"module": "github.com/hashicorp/go-version", "license": "MPL-2.0", "reason": "Version parsing."}
	]}`), p)
	if err != nil {
		t.Fatal(err)
	}
	want := []Exception{{Module: "github.com/hashicorp/go-version", License: "MPL-2.0", Reason: "Version parsing."}}
	if !slices.Equal(got, want) {
		t.Errorf("ParseExceptions() = %v, want %v", got, want)
	}
}

func TestParseExceptionsRejects(t *testing.T) {
	p := testPolicy(t)
	for name, tc := range map[string]struct{ data, want string }{
		"missing list":   {`{}`, `"exceptions" is missing`},
		"unknown field":  {`{"exceptions": [], "x": 1}`, "unknown field"},
		"trailing data":  {`{"exceptions": []} []`, "data after"},
		"empty module":   {`{"exceptions": [{"module": "", "license": "MPL-2.0", "reason": "r"}]}`, `exception 1: "module" is empty`},
		"empty license":  {`{"exceptions": [{"module": "m", "license": "", "reason": "r"}]}`, `exception 1: "license" is empty`},
		"empty reason":   {`{"exceptions": [{"module": "m", "license": "MPL-2.0", "reason": " "}]}`, `exception 1: "reason" is empty`},
		"listed twice":   {`{"exceptions": [{"module": "m", "license": "MPL-2.0", "reason": "r"}, {"module": "m", "license": "MPL-2.0", "reason": "r"}]}`, "the module m is listed twice"},
		"allowed anyway": {`{"exceptions": [{"module": "m", "license": "MIT", "reason": "r"}]}`, "MIT is not a license that passes by exception"},
		"never allowed":  {`{"exceptions": [{"module": "m", "license": "GPL-3.0-only", "reason": "r"}]}`, "GPL-3.0-only is not a license that passes by exception"},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := ParseExceptions([]byte(tc.data), p)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("ParseExceptions() error = %v, want one containing %q", err, tc.want)
			}
		})
	}
}

func TestPinnedTrivyVersion(t *testing.T) {
	script := "#!/bin/sh\nset -eu\n\nGOLANGCI_LINT_VERSION=2.15.0\nTRIVY_VERSION=0.76.2\nTRIVY_SHA256_AMD64=abc\n"
	got, err := PinnedTrivyVersion([]byte(script))
	if err != nil || got != "0.76.2" {
		t.Errorf("PinnedTrivyVersion() = %q, %v; want 0.76.2", got, err)
	}
	for name, script := range map[string]string{
		"missing": "GOLANGCI_LINT_VERSION=2.15.0\n",
		"twice":   "TRIVY_VERSION=0.76.2\nTRIVY_VERSION=0.76.3\n",
		"empty":   "TRIVY_VERSION=\n",
	} {
		t.Run(name, func(t *testing.T) {
			if got, err := PinnedTrivyVersion([]byte(script)); err == nil {
				t.Errorf("PinnedTrivyVersion() = %q, want an error", got)
			}
		})
	}
}
