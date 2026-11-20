// SPDX-License-Identifier: Apache-2.0
// SPDX-FileCopyrightText: The Pumi Authors

package cedar

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"slices"
	"strings"
)

// The files that the check reads, relative to the root of the repository.
const (
	// Schema is the Cedar schema (decision 0141).
	Schema = "api/policy/pumi.cedarschema"
	// Invariants holds the core invariants (decision 0142), which every
	// other policy set runs with.
	Invariants = "api/policy/invariants.cedar"
	// Dockerfile pins the version of the Cedar CLI in its CEDAR_VERSION
	// build argument.
	Dockerfile = ".devcontainer/Dockerfile"
)

// versionArg is the line of Dockerfile that pins the Cedar CLI, without the
// version.
const versionArg = "ARG CEDAR_VERSION="

// The names of the files in the directory where the CLI runs. The check
// copies each input there, so that every argument it passes to the CLI is
// a constant.
const (
	schemaFile   = "schema.cedarschema"
	policiesFile = "policies.cedar"
	testsFile    = "tests.json"
)

// testsSuffix ends the name of a file of cedar run-tests cases. The cases in
// x.tests.json run on the policy file x.cedar beside it.
const testsSuffix = ".tests.json"

// Check runs the pinned Cedar CLI on the repository at root and writes one
// line for each step to out. Every policy file is checked with cedar format
// --check. The core invariants are validated in strict mode against the
// schema, and so is every other policy file together with the invariants,
// since a lower set always runs with them. Each file of run-tests cases runs
// on its policy file, together with the invariants unless the file is the
// invariants. Check runs every step and returns Findings that name each
// one that fails, or another error when it cannot run.
func Check(ctx context.Context, root string, out io.Writer) error {
	repo := os.DirFS(root)
	if err := checkVersion(ctx, repo); err != nil {
		return err
	}
	schema, err := fs.ReadFile(repo, Schema)
	if err != nil {
		return err
	}
	invariants, err := fs.ReadFile(repo, Invariants)
	if err != nil {
		return err
	}
	policies, tests, err := find(repo)
	if err != nil {
		return err
	}
	c := &checker{ctx: ctx, repo: repo, out: out, schema: schema, invariants: invariants}
	c.policy(Invariants)
	for _, p := range policies {
		c.policy(p)
	}
	for _, t := range tests {
		c.tests(t)
	}
	if len(c.findings) > 0 {
		return c.findings
	}
	_, err = fmt.Fprintf(out, "cedar: policy files checked: %d, test files run: %d\n", len(policies)+1, len(tests))
	return err
}

// checker runs the steps of a check and collects their findings.
type checker struct {
	ctx        context.Context
	repo       fs.FS
	out        io.Writer
	schema     []byte
	invariants []byte
	findings   Findings
}

// Findings are the steps of a check that failed, each with the CLI's
// output. Check returns them as its error.
type Findings []string

func (f Findings) Error() string {
	return strings.Join(f, "\n")
}

// policy checks the format of the policy file name and validates it, with
// the invariants unless it is the invariants.
func (c *checker) policy(name string) {
	src, err := c.read(name)
	if err != nil {
		c.report("format "+name, err)
		return
	}
	c.report("format "+name, c.run(map[string][]byte{policiesFile: src}, formatCheck))
	step, set := "validate "+Invariants, c.invariants
	if name != Invariants {
		step, set = step+" with "+name, join(c.invariants, src)
	}
	c.report(step, c.run(map[string][]byte{schemaFile: c.schema, policiesFile: set}, validate))
}

// tests runs the cases in the file name on the policy file beside it.
func (c *checker) tests(name string) {
	step := "run-tests " + name
	cases, err := c.read(name)
	if err != nil {
		c.report(step, err)
		return
	}
	policyName := strings.TrimSuffix(name, testsSuffix) + ".cedar"
	set := c.invariants
	if policyName != Invariants {
		src, err := c.read(policyName)
		if errors.Is(err, fs.ErrNotExist) {
			c.report(step, fmt.Errorf("%s: no policy file %s", name, policyName))
			return
		}
		if err != nil {
			c.report(step, err)
			return
		}
		set = join(c.invariants, src)
	}
	c.report(step, c.run(map[string][]byte{schemaFile: c.schema, policiesFile: set, testsFile: cases}, runTests))
}

func (c *checker) read(name string) ([]byte, error) {
	return fs.ReadFile(c.repo, name)
}

// report writes the result of a step and keeps it as a finding when it
// failed.
func (c *checker) report(step string, err error) {
	if err == nil {
		_, _ = fmt.Fprintf(c.out, "cedar: %s: ok\n", step)
		return
	}
	_, _ = fmt.Fprintf(c.out, "cedar: %s: fails\n", step)
	c.findings = append(c.findings, step+": "+err.Error())
}

// join returns the policy set made of the invariants followed by a lower
// set. cedar run-tests and cedar validate take one policies file.
func join(invariants, lower []byte) []byte {
	set := bytes.Clone(invariants)
	if !bytes.HasSuffix(set, []byte("\n")) {
		set = append(set, '\n')
	}
	return append(append(set, '\n'), lower...)
}

// A command runs the Cedar CLI in dir, which holds the input files under
// their fixed names. Every argument is a constant.
type command func(ctx context.Context, dir string) *exec.Cmd

func formatCheck(ctx context.Context, dir string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, "cedar", "format", "--check", "--policies", policiesFile)
	cmd.Dir = dir
	return cmd
}

func validate(ctx context.Context, dir string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, "cedar", "validate", "--schema", schemaFile, "--policies", policiesFile, "--validation-mode", "strict")
	cmd.Dir = dir
	return cmd
}

func runTests(ctx context.Context, dir string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, "cedar", "run-tests", "--schema", schemaFile, "--policies", policiesFile, "--tests", testsFile)
	cmd.Dir = dir
	return cmd
}

// run writes files to a new temporary directory and runs the command there.
// When the command fails, the error holds its output.
func (c *checker) run(files map[string][]byte, cmd command) error {
	dir, err := os.MkdirTemp("", "pumi-cedar-")
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(dir) }()
	for name, data := range files {
		if err := os.WriteFile(filepath.Join(dir, name), data, 0o600); err != nil {
			return err
		}
	}
	output, err := cmd(c.ctx, dir).CombinedOutput()
	if err != nil {
		return fmt.Errorf("%w\n%s", err, strings.TrimRight(string(output), "\n"))
	}
	return nil
}

// find returns the policy files other than the invariants and the files of
// run-tests cases in the repository, each sorted by path. It skips the .git
// directory.
func find(repo fs.FS) (policies, tests []string, err error) {
	err = fs.WalkDir(repo, ".", func(p string, d fs.DirEntry, err error) error {
		switch {
		case err != nil:
			return err
		case d.IsDir() && d.Name() == ".git":
			return fs.SkipDir
		case d.IsDir():
			return nil
		case strings.HasSuffix(p, testsSuffix):
			tests = append(tests, p)
		case path.Ext(p) == ".cedar" && p != Invariants:
			policies = append(policies, p)
		}
		return nil
	})
	slices.Sort(policies)
	slices.Sort(tests)
	return policies, tests, err
}

// checkVersion fails when the cedar on the PATH is not the version that
// Dockerfile pins.
func checkVersion(ctx context.Context, repo fs.FS) error {
	data, err := fs.ReadFile(repo, Dockerfile)
	if err != nil {
		return err
	}
	pinned, err := PinnedVersion(data)
	if err != nil {
		return err
	}
	output, err := exec.CommandContext(ctx, "cedar", "--version").Output()
	if err != nil {
		return fmt.Errorf("cedar --version: %w", err)
	}
	got := strings.TrimPrefix(strings.TrimSpace(string(output)), "cedar-policy-cli ")
	if got != pinned {
		return fmt.Errorf("cedar %s on the PATH is not the pinned version %s in %s", got, pinned, Dockerfile)
	}
	return nil
}

// PinnedVersion returns the version of the Cedar CLI in the one line
// ARG CEDAR_VERSION=<version> of a Dockerfile.
func PinnedVersion(dockerfile []byte) (string, error) {
	var versions []string
	sc := bufio.NewScanner(bytes.NewReader(dockerfile))
	for sc.Scan() {
		if v, ok := strings.CutPrefix(sc.Text(), versionArg); ok {
			versions = append(versions, v)
		}
	}
	if err := sc.Err(); err != nil {
		return "", err
	}
	if len(versions) != 1 || versions[0] == "" {
		return "", fmt.Errorf("%s: want one %s line with a version, got %d", Dockerfile, versionArg, len(versions))
	}
	return versions[0], nil
}
