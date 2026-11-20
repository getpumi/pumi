// SPDX-License-Identifier: Apache-2.0
// SPDX-FileCopyrightText: The Pumi Authors

package controlplane

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"path"
	"strings"
	"testing"
	"testing/fstest"

	cedartypes "github.com/cedar-policy/cedar-go/types"

	"getpumi.dev/pumi/api/policy"
)

// repoRoot is the repository root, seen from this package's directory.
const repoRoot = "../.."

// invariantsPath is the path of the core invariants from the root.
const invariantsPath = "api/policy/invariants.cedar"

// agreementCase is one case of a cedar run-tests file, with the request
// and the entities kept as JSON so that both deciders read the same bytes.
type agreementCase struct {
	Name    string `json:"name"`
	Request struct {
		Principal string          `json:"principal"`
		Action    string          `json:"action"`
		Resource  string          `json:"resource"`
		Context   json.RawMessage `json:"context"`
	} `json:"request"`
	Entities  json.RawMessage `json:"entities"`
	NumErrors int             `json:"num_errors"`
}

// runTestsFiles returns the cedar run-tests files that git tracks, by path
// from the repository root, which are the files that make cedar runs.
// safe.directory is set because the workspace in CI's container belongs
// to another user.
func runTestsFiles(t *testing.T) []string {
	t.Helper()
	cmd := exec.CommandContext(t.Context(), "git", "-c", "safe.directory=*", "ls-files", "-z", "--", "*.tests.json")
	cmd.Dir = repoRoot
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("git ls-files: %v", err)
	}
	var files []string
	for _, p := range strings.Split(string(out), "\x00") {
		if p != "" {
			files = append(files, p)
		}
	}
	return files
}

// agreementCases reads the cases of the run-tests file p, with an entities
// value that names a file replaced by that file's content, as make cedar
// does.
func agreementCases(t *testing.T, root fs.FS, p string) []agreementCase {
	t.Helper()
	b, err := fs.ReadFile(root, p)
	if err != nil {
		t.Fatal(err)
	}
	var cases []agreementCase
	if err := json.Unmarshal(b, &cases); err != nil {
		t.Fatalf("cannot read %s: %v", p, err)
	}
	for i, c := range cases {
		if len(c.Entities) == 0 {
			t.Fatalf("%s: the case %q has no entities", p, c.Name)
		}
		var file string
		if json.Unmarshal(c.Entities, &file) != nil {
			continue
		}
		entities, err := fs.ReadFile(root, path.Join(path.Dir(p), file))
		if err != nil {
			t.Fatalf("%s: entities file %s: %v", p, file, err)
		}
		cases[i].Entities = entities
	}
	return cases
}

// cliDecision runs the Rust cedar CLI's authorize command on one request
// in dir, where the schema, policies, entities and request files are, and
// returns ALLOW for exit status 0 and DENY for exit status 2.
func cliDecision(t *testing.T, dir string) string {
	t.Helper()
	cmd := exec.CommandContext(t.Context(), "cedar", "authorize",
		"--schema", "schema.cedarschema", "--policies", "policies.cedar",
		"--entities", "entities.json", "--request-json", "request.json")
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	var exit *exec.ExitError
	switch {
	case err == nil:
		return "ALLOW"
	case errors.As(err, &exit) && exit.ExitCode() == 2:
		return "DENY"
	default:
		t.Fatalf("cedar authorize: %v\n%s", err, out)
		return ""
	}
}

// writeFile writes b to name in dir.
func writeFile(t *testing.T, dir, name string, b []byte) {
	t.Helper()
	if err := os.WriteFile(dir+"/"+name, b, 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestDecideAgreesWithTheCedarCLIOnEveryRunTestsCase(t *testing.T) {
	root := os.DirFS(repoRoot)
	seen := map[string]bool{}
	files := runTestsFiles(t)
	if len(files) == 0 {
		t.Fatal("the repository has no cedar run-tests files")
	}
	for _, p := range files {
		lowerName := strings.TrimSuffix(p, ".tests.json") + ".cedar"
		lower, err := fs.ReadFile(root, lowerName)
		if err != nil {
			t.Fatalf("%s has no policy file beside it: %v", p, err)
		}
		// As make cedar does, a lower set is joined to the invariants, and
		// the invariants' own cases run on the invariants alone.
		files, joined := []PolicyFile{{Name: lowerName, Text: string(lower)}}, string(lower)
		if lowerName != invariantsPath {
			files = append([]PolicyFile{{Name: invariantsPath, Text: policy.Invariants}}, files...)
			joined = policy.Invariants + "\n" + joined
		}
		d, err := NewDecider(policy.Schema, files...)
		if err != nil {
			t.Fatal(err)
		}
		for _, c := range agreementCases(t, root, p) {
			t.Run(p+"/"+c.Name, func(t *testing.T) {
				seen[checkAgreement(t, d, joined, c)] = true
			})
		}
	}
	// Both decisions occur, so the test shows that each exit status is
	// read, not only that the two deciders agree on one answer.
	if !seen["ALLOW"] || !seen["DENY"] {
		t.Errorf("the cedar CLI decided %v; the cases need both ALLOW and DENY", seen)
	}
}

// checkAgreement decides c with d and with the cedar CLI on the policies
// text, fails when the two decisions differ, and returns the CLI's
// decision. A case whose evaluation has errors is the one intended
// difference: the CLI skips a policy that fails, as cedar run-tests
// expects, and Decide denies (decision 0146).
func checkAgreement(t *testing.T, d *Decider, policies string, c agreementCase) string {
	t.Helper()
	dir := t.TempDir()
	writeFile(t, dir, "schema.cedarschema", []byte(policy.Schema))
	writeFile(t, dir, "policies.cedar", []byte(policies))
	writeFile(t, dir, "entities.json", c.Entities)
	request, err := json.Marshal(c.Request)
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, dir, "request.json", request)
	cli := cliDecision(t, dir)

	var context cedartypes.Record
	if err := json.Unmarshal(c.Request.Context, &context); err != nil {
		t.Fatalf("cannot read the context: %v", err)
	}
	var entities cedartypes.EntityMap
	if err := json.Unmarshal(c.Entities, &entities); err != nil {
		t.Fatalf("cannot read the entities: %v", err)
	}
	got := d.Decide(Request{
		Principal: uid(t, c.Request.Principal),
		Action:    uid(t, c.Request.Action),
		Resource:  uid(t, c.Request.Resource),
		Context:   context,
		Entities:  entities,
	})
	decide := "DENY"
	if got.Allow {
		decide = "ALLOW"
	}
	want := cli
	if c.NumErrors > 0 {
		want = "DENY"
	}
	if decide != want {
		t.Errorf("Decide() = %s (%+v), cedar authorize = %s, %d evaluation errors expected", decide, got, cli, c.NumErrors)
	}
	return cli
}

func TestAgreementCasesInlineAnEntitiesFile(t *testing.T) {
	root := fstest.MapFS{
		"p/x.tests.json": {Data: []byte(`[{"name":"a","entities":"e.json"},{"name":"b","entities":[]}]`)},
		"p/e.json":       {Data: []byte(`[{"uid":{"type":"T","id":"1"},"attrs":{},"parents":[]}]`)},
	}
	cases := agreementCases(t, root, "p/x.tests.json")
	if len(cases) != 2 || string(cases[0].Entities) != string(root["p/e.json"].Data) || string(cases[1].Entities) != "[]" {
		t.Errorf("agreementCases() = %+v, want the file's content in the first case only", cases)
	}
}
