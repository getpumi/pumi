// SPDX-License-Identifier: Apache-2.0
// SPDX-FileCopyrightText: The Pumi Authors

package cedar

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The tests run the cedar on the PATH, which in the devcontainer is the
// pinned Rust Cedar CLI.

// root is the repository's root, relative to this package.
const root = "../../.."

// repoFile returns the content of a file of the repository.
func repoFile(t *testing.T, name string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(name)))
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// newRepo writes a repository with the repository's schema, invariants and
// Dockerfile, and the files in changes, to a temporary directory and returns
// it. An empty value in changes leaves the file out.
func newRepo(t *testing.T, changes map[string]string) string {
	t.Helper()
	files := map[string]string{
		Schema:     repoFile(t, Schema),
		Invariants: repoFile(t, Invariants),
		Dockerfile: repoFile(t, Dockerfile),
	}
	for k, v := range changes {
		files[k] = v
	}
	dir := t.TempDir()
	for name, src := range files {
		if src == "" {
			continue
		}
		p := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(src), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// check runs Check on dir and returns its output and error.
func check(t *testing.T, dir string) (string, error) {
	t.Helper()
	var out bytes.Buffer
	err := Check(t.Context(), dir, &out)
	return out.String(), err
}

const permitAll = "@id(\"permit-all\")\npermit (principal, action, resource);\n"

// mergeTest is a run-tests file with one case: a merge that meets every
// invariant, which a permit-all layer allows.
const mergeTest = `[
  {
    "name": "a merge that meets every invariant is allowed",
    "request": {
      "principal": "Pumi::Kernel::\"kernel\"",
      "action": "Pumi::Action::\"merge\"",
      "resource": "Pumi::Plan::\"plan\"",
      "context": {
        "hardCaps": {"costMicrodollars": 50000000, "seconds": 14400, "tokens": 5000000},
        "headState": {"commit": "c", "tree": "t", "upToDate": true},
        "pathsProtected": [],
        "usage": {"costMicrodollars": 1, "seconds": 1, "tokens": 1},
        "verifyPassed": %s
      }
    },
    "entities": [
      {"uid": {"type": "Pumi::Kernel", "id": "kernel"}, "attrs": {}, "parents": []},
      {"uid": {"type": "Pumi::Project", "id": "p"}, "attrs": {"protectedPaths": []}, "parents": []},
      {"uid": {"type": "Pumi::Plan", "id": "plan"}, "attrs": {"tree": "t"}, "parents": [{"type": "Pumi::Project", "id": "p"}]}
    ],
    "decision": "allow",
    "reason": ["permit-all"],
    "num_errors": 0
  }
]
`

func mergeTests(verifyPassed string) string {
	return strings.Replace(mergeTest, "%s", verifyPassed, 1)
}

func TestCheckPassesOnTheRepository(t *testing.T) {
	out, err := check(t, root)
	if err != nil {
		t.Fatalf("Check(repository) = %v; output:\n%s", err, out)
	}
	for _, want := range []string{
		"cedar: format " + Invariants + ": ok",
		"cedar: validate " + Invariants + ": ok",
		"cedar: validate " + Invariants + " with api/policy/testdata/permit-all.cedar: ok",
		"cedar: run-tests api/policy/testdata/permit-all.tests.json: ok",
	} {
		if !strings.Contains(out, want+"\n") {
			t.Errorf("Check(repository) output does not have %q:\n%s", want, out)
		}
	}
}

func TestCheckFindsSetsAndTests(t *testing.T) {
	dir := newRepo(t, map[string]string{
		"a/b/lower.cedar":      permitAll,
		"a/b/lower.tests.json": mergeTests("true"),
	})
	out, err := check(t, dir)
	if err != nil {
		t.Fatalf("Check() = %v; output:\n%s", err, out)
	}
	want := "cedar: format api/policy/invariants.cedar: ok\n" +
		"cedar: validate api/policy/invariants.cedar: ok\n" +
		"cedar: format a/b/lower.cedar: ok\n" +
		"cedar: validate api/policy/invariants.cedar with a/b/lower.cedar: ok\n" +
		"cedar: run-tests a/b/lower.tests.json: ok\n" +
		"cedar: policy files checked: 2, test files run: 1\n"
	if out != want {
		t.Errorf("Check() output:\n%s\nwant:\n%s", out, want)
	}
}

func TestCheckFails(t *testing.T) {
	for name, c := range map[string]struct {
		changes map[string]string
		want    string
	}{
		"a set that does not validate strictly": {
			map[string]string{"x/lower.cedar": "@id(\"x\")\nforbid (principal, action, resource)\nwhen { context.nothing };\n"},
			"validate api/policy/invariants.cedar with x/lower.cedar",
		},
		"an invariant that does not validate strictly": {
			map[string]string{Invariants: "@id(\"x\")\nforbid (principal, action, resource)\nwhen { context.nothing };\n"},
			"validate api/policy/invariants.cedar",
		},
		"a set that is not formatted": {
			map[string]string{"x/lower.cedar": "@id(\"permit-all\") permit(principal,action,resource);\n"},
			"format x/lower.cedar",
		},
		"a case whose decision differs": {
			map[string]string{"x/lower.cedar": permitAll, "x/lower.tests.json": mergeTests("false")},
			"run-tests x/lower.tests.json",
		},
		"a tests file without its policies": {
			map[string]string{"x/other.tests.json": mergeTests("true")},
			"x/other.tests.json: no policy file x/other.cedar",
		},
		"a pin that is not the cedar on the PATH": {
			map[string]string{Dockerfile: strings.Replace(repoFile(t, Dockerfile), "ARG CEDAR_VERSION=", "ARG CEDAR_VERSION=1", 1)},
			"is not the pinned version",
		},
		"no schema": {
			map[string]string{Schema: ""},
			Schema,
		},
		"no invariants": {
			map[string]string{Invariants: ""},
			Invariants,
		},
	} {
		t.Run(name, func(t *testing.T) {
			out, err := check(t, newRepo(t, c.changes))
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Errorf("Check() = %v, want an error with %q; output:\n%s", err, c.want, out)
			}
		})
	}
}

func TestCheckReportsEveryFinding(t *testing.T) {
	out, err := check(t, newRepo(t, map[string]string{
		"x/a.cedar": "@id(\"a\") permit(principal,action,resource);\n",
		"y/b.cedar": "@id(\"b\")\nforbid (principal, action, resource)\nwhen { context.nothing };\n",
	}))
	if err == nil {
		t.Fatalf("Check() returned no error; output:\n%s", out)
	}
	for _, want := range []string{"format x/a.cedar", "validate api/policy/invariants.cedar with y/b.cedar"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("Check() = %v, want it to name %q", err, want)
		}
	}
}
