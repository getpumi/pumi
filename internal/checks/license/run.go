// SPDX-License-Identifier: Apache-2.0
// SPDX-FileCopyrightText: The Pumi Authors

package license

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// The files that Run reads, relative to the root of the core module.
const (
	PolicyPath     = ".pumi/licenses.json"
	ExceptionsPath = ".pumi/license-exceptions.json"
	// InstallScript pins the Trivy version (decision 0086).
	InstallScript = ".devcontainer/install-tools.sh"
)

// Result is what Run found: the modules that pass and the findings.
type Result struct {
	Checked  []Checked
	Findings []Finding
}

// Run runs the license check on the core module at root. It reads the policy
// and the exceptions from .pumi/, lists the modules that the binaries
// include, lets the pinned Trivy detect their licenses and the license of
// the Go standard library, and evaluates them. It fails when the Trivy on the
// PATH is not the version that install-tools.sh pins.
func Run(ctx context.Context, root string) (*Result, error) {
	policy, exceptions, pinned, err := readConfig(root)
	if err != nil {
		return nil, err
	}
	gopath, err := moduleCacheGOPATH(ctx, root)
	if err != nil {
		return nil, err
	}
	mods, err := BuildModules(ctx, root)
	if err != nil {
		return nil, err
	}
	std, err := StandardLibrary(ctx, root)
	if err != nil {
		return nil, err
	}
	cacheDir, err := os.MkdirTemp("", "pumi-license-trivy-")
	if err != nil {
		return nil, err
	}
	defer func() { _ = os.RemoveAll(cacheDir) }()
	if err := os.WriteFile(filepath.Join(cacheDir, emptyConfig), nil, 0o600); err != nil {
		return nil, err
	}
	report, err := scanGoMod(ctx, root, gopath, cacheDir)
	if err != nil {
		return nil, err
	}
	stdReport, err := scanLicenseFile(ctx, std.Dir, gopath, cacheDir)
	if err != nil {
		return nil, err
	}
	for _, r := range []*Report{report, stdReport} {
		if r.Version != pinned {
			return nil, fmt.Errorf("trivy %s on the PATH is not the pinned version %s in %s", r.Version, pinned, InstallScript)
		}
	}
	checked, findings := Evaluate(Input{
		Policy:     policy,
		Exceptions: exceptions,
		Modules:    mods,
		Report:     report,
		Std:        std,
		StdReport:  stdReport,
	})
	return &Result{Checked: checked, Findings: findings}, nil
}

// readConfig reads the policy, the exceptions and the pinned Trivy version.
func readConfig(root string) (*Policy, []Exception, string, error) {
	dir := os.DirFS(root)
	read := func(name string) ([]byte, error) {
		return fs.ReadFile(dir, name)
	}
	data, err := read(PolicyPath)
	if err != nil {
		return nil, nil, "", err
	}
	policy, err := ParsePolicy(data)
	if err != nil {
		return nil, nil, "", fmt.Errorf("%s: %w", PolicyPath, err)
	}
	if data, err = read(ExceptionsPath); err != nil {
		return nil, nil, "", err
	}
	exceptions, err := ParseExceptions(data, policy)
	if err != nil {
		return nil, nil, "", fmt.Errorf("%s: %w", ExceptionsPath, err)
	}
	if data, err = read(InstallScript); err != nil {
		return nil, nil, "", err
	}
	pinned, err := PinnedTrivyVersion(data)
	if err != nil {
		return nil, nil, "", fmt.Errorf("%s: %w", InstallScript, err)
	}
	return policy, exceptions, pinned, nil
}

// moduleCacheGOPATH returns the GOPATH whose module cache the go command
// uses. Trivy reads the licenses from GOPATH/pkg/mod and ignores GOMODCACHE,
// so the check fails when the module cache is anywhere else, where Trivy
// would find no license.
func moduleCacheGOPATH(ctx context.Context, root string) (string, error) {
	env, err := goEnv(ctx, root, func(ctx context.Context) *exec.Cmd {
		return exec.CommandContext(ctx, "go", "env", "-json", "GOPATH", "GOMODCACHE")
	})
	if err != nil {
		return "", err
	}
	gopath := env["GOPATH"]
	if gopath == "" || strings.Contains(gopath, string(filepath.ListSeparator)) {
		return "", fmt.Errorf("GOPATH is %q; the check needs one directory", gopath)
	}
	if want := filepath.Join(gopath, "pkg", "mod"); env["GOMODCACHE"] != want {
		return "", fmt.Errorf("GOMODCACHE is %s, but Trivy reads licenses only from %s; unset GOMODCACHE", env["GOMODCACHE"], want)
	}
	return gopath, nil
}
