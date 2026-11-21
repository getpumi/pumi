// SPDX-License-Identifier: Apache-2.0
// SPDX-FileCopyrightText: The Pumi Authors

package bufconfig

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// The tests run buf with constant arguments, because gosec reports a command
// that is built from variables (G204), and set only cmd.Dir. go test runs the
// tests in the directory of this package.
const (
	// root is the repository root, relative to the directory of this package.
	root = "../../.."
	// fixtureModfile, fixtureConfig, nestedModfile and nestedConfig are the
	// tool module file of buf and the repository's buf.yaml, relative to a
	// fixture directory testdata/<name> and testdata/<name>/<subdirectory>. buf reads
	// the module paths of buf.yaml relative to its working directory, so it
	// runs in the fixture.
	fixtureModfile = "-modfile=../../../../../tools/buf.mod"
	fixtureConfig  = "../../../../../buf.yaml"
	nestedModfile  = "-modfile=../../../../../../tools/buf.mod"
	nestedConfig   = "../../../../../../buf.yaml"
)

// result is the outcome of one run of buf.
type result struct {
	// exit is the exit status.
	exit int
	// stdout and stderr are the output.
	stdout, stderr string
}

// run runs cmd in dir and returns its exit status and output. A failure to
// start the command fails the test.
func run(t *testing.T, cmd *exec.Cmd, dir string) result {
	t.Helper()
	var stdout, stderr bytes.Buffer
	cmd.Dir = dir
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	var exit *exec.ExitError
	switch {
	case err == nil:
		return result{0, stdout.String(), stderr.String()}
	case errors.As(err, &exit):
		return result{exit.ExitCode(), stdout.String(), stderr.String()}
	default:
		t.Fatalf("%s: %v", cmd, err)
		return result{}
	}
}

// rules returns the rule of each finding in buf's JSON error format, one
// finding per line.
func rules(t *testing.T, r result) []string {
	t.Helper()
	got := []string{}
	scanner := bufio.NewScanner(bytes.NewBufferString(r.stdout))
	for scanner.Scan() {
		var finding struct {
			Path    string
			Type    string
			Message string
		}
		if err := json.Unmarshal(scanner.Bytes(), &finding); err != nil {
			t.Fatalf("reading a buf finding: %v\nstdout:\n%s\nstderr:\n%s", err, r.stdout, r.stderr)
		}
		t.Logf("%s: %s (%s)", finding.Path, finding.Message, finding.Type)
		got = append(got, finding.Type)
	}
	slices.Sort(got)
	return slices.Compact(got)
}

// TestLintReportsTheStandardRules checks that buf lint uses the STANDARD rules
// outside the vendored directory and reports nothing in it.
func TestLintReportsTheStandardRules(t *testing.T) {
	cmd := exec.CommandContext(t.Context(), "go", "tool", fixtureModfile, "buf", "lint",
		"--config", fixtureConfig, "--error-format", "json")
	r := run(t, cmd, "testdata/lint")
	if r.exit == 0 {
		t.Fatalf("buf lint passed the fixture; want findings\nstderr:\n%s", r.stderr)
	}
	want := []string{
		"ENUM_VALUE_PREFIX", "ENUM_ZERO_VALUE_SUFFIX", "FIELD_LOWER_SNAKE_CASE", "MESSAGE_PASCAL_CASE",
	}
	if got := rules(t, r); !slices.Equal(got, want) {
		t.Errorf("buf lint rules = %q, want %q", got, want)
	}
}

// TestBreakingUsesTheFileCategory checks that buf breaking reports a deleted
// field even when its number and name are reserved, which the WIRE and
// WIRE_JSON categories allow.
func TestBreakingUsesTheFileCategory(t *testing.T) {
	cmd := exec.CommandContext(t.Context(), "go", "tool", nestedModfile, "buf", "breaking",
		"--against", "../base", "--config", nestedConfig, "--against-config", nestedConfig,
		"--error-format", "json")
	r := run(t, cmd, "testdata/breaking/head")
	if r.exit == 0 {
		t.Fatalf("buf breaking passed the fixture; want findings\nstderr:\n%s", r.stderr)
	}
	if got := rules(t, r); !slices.Contains(got, "FIELD_NO_DELETE") {
		t.Errorf("buf breaking rules = %q, want FIELD_NO_DELETE among them", got)
	}
}

// TestBreakingReportsAMovedMessage checks that buf breaking reports a message
// that moves to another file of the same package, which the PACKAGE category
// allows and the FILE category does not.
func TestBreakingReportsAMovedMessage(t *testing.T) {
	cmd := exec.CommandContext(t.Context(), "go", "tool", nestedModfile, "buf", "breaking",
		"--against", "../base", "--config", nestedConfig, "--against-config", nestedConfig,
		"--error-format", "json")
	r := run(t, cmd, "testdata/breaking/moved")
	if r.exit == 0 {
		t.Fatalf("buf breaking passed the fixture; want findings\nstderr:\n%s", r.stderr)
	}
	if got := rules(t, r); !slices.Contains(got, "FILE_NO_DELETE") {
		t.Errorf("buf breaking rules = %q, want FILE_NO_DELETE among them", got)
	}
}

// generationInputs are the files, relative to root, that buf generate reads:
// the go.mod that go tool needs, the tool module files, the configuration and
// the schema.
var generationInputs = []string{
	"go.mod",
	"tools",
	"buf.yaml",
	"buf.gen.yaml",
	"api/proto",
}

// copyInputs copies the generation inputs from root into dst.
func copyInputs(t *testing.T, dst string) {
	t.Helper()
	repo := os.DirFS(root)
	for _, input := range generationInputs {
		info, err := fs.Stat(repo, input)
		if err != nil {
			t.Fatal(err)
		}
		if info.IsDir() {
			sub, err := fs.Sub(repo, input)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.CopyFS(filepath.Join(dst, input), sub); err != nil {
				t.Fatal(err)
			}
			continue
		}
		data, err := fs.ReadFile(repo, input)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dst, input), data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

// tree returns the content of every file under dir, by its slash-separated
// path relative to dir. A missing dir has no files.
func tree(t *testing.T, dir string) map[string]string {
	t.Helper()
	files := map[string]string{}
	fsys := os.DirFS(dir)
	err := fs.WalkDir(fsys, ".", func(name string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		data, err := fs.ReadFile(fsys, name)
		files[name] = string(data)
		return err
	})
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		t.Fatal(err)
	}
	return files
}

// TestGeneratedCodeIsCurrent runs buf generate on a copy of the schema and
// checks that the result is exactly the committed api/gen, the JSON Schemas
// in api/gen/jsonschema included: no file differs, is missing or is left over
// (decisions 0090 and 0160).
func TestGeneratedCodeIsCurrent(t *testing.T) {
	dir := t.TempDir()
	copyInputs(t, dir)
	cmd := exec.CommandContext(t.Context(), "go", "tool", "-modfile=tools/buf.mod", "buf", "generate")
	if r := run(t, cmd, dir); r.exit != 0 {
		t.Fatalf("buf generate exited with status %d\nstdout:\n%s\nstderr:\n%s", r.exit, r.stdout, r.stderr)
	}
	want := tree(t, filepath.Join(dir, "api", "gen"))
	if len(want) == 0 {
		t.Fatal("buf generate wrote no files to api/gen")
	}
	schemas := 0
	for name := range want {
		if strings.HasPrefix(name, "jsonschema/") && strings.HasSuffix(name, ".jsonschema.bundle.json") {
			schemas++
		}
	}
	if schemas == 0 {
		t.Fatal("buf generate wrote no JSON Schemas to api/gen/jsonschema (decision 0160)")
	}
	got := tree(t, filepath.Join(root, "api", "gen"))
	for name, content := range want {
		committed, ok := got[name]
		switch {
		case !ok:
			t.Errorf("api/gen/%s is not committed; run make generate", name)
		case committed != content:
			t.Errorf("api/gen/%s differs from what buf generate writes; run make generate", name)
		}
	}
	for name := range got {
		if _, ok := want[name]; !ok {
			t.Errorf("api/gen/%s is not written by buf generate; run make generate", name)
		}
	}
}
