// SPDX-License-Identifier: Apache-2.0
// SPDX-FileCopyrightText: The Pumi Authors

package license

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// emptyConfig is the name of the empty Trivy configuration file in Trivy's
// cache directory.
const emptyConfig = "trivy.yaml"

// Report is what the check reads from one JSON report of Trivy: the Trivy
// version; the licenses of each module in go.mod, by path@version, with an
// empty list for a module whose license Trivy did not find; and the licenses
// of the license files of a scan with --license-full.
type Report struct {
	Version string
	Modules map[string][]string
	Files   []string
}

// report is the part of Trivy's JSON report, schema version 2, that the
// check reads.
type report struct {
	SchemaVersion int
	Trivy         struct {
		Version string
	}
	Results []result
}

// result is one result of Trivy's JSON report.
type result struct {
	Target   string
	Class    string
	Type     string
	Packages []pkgResult
	Licenses []struct {
		Name string
	}
}

// pkgResult is one package of a result of Trivy's JSON report.
type pkgResult struct {
	Name     string
	Version  string
	Licenses []string
}

// ParseReport reads a JSON report of Trivy. It fails on a report that holds
// packages of anything but the go.mod at the root of the scan, such as a
// nested go.mod or another language, because the check covers only the Go
// modules of the core module.
func ParseReport(data []byte) (*Report, error) {
	var raw report
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, err
	}
	if raw.SchemaVersion != 2 {
		return nil, fmt.Errorf("trivy report: schema version %d, want 2", raw.SchemaVersion)
	}
	if raw.Trivy.Version == "" {
		return nil, fmt.Errorf("trivy report: no Trivy version")
	}
	r := &Report{Version: raw.Trivy.Version, Modules: map[string][]string{}}
	goMods := 0
	for _, res := range raw.Results {
		switch res.Class {
		case "lang-pkgs":
			goMods++
			if err := r.addModules(res, goMods); err != nil {
				return nil, err
			}
		case "license-file":
			for _, l := range res.Licenses {
				r.Files = append(r.Files, l.Name)
			}
		}
	}
	return r, nil
}

// addModules adds the licenses of the packages of res, the n-th result of
// packages in the report, to r.Modules.
func (r *Report) addModules(res result, n int) error {
	if res.Type != "gomod" || res.Target != "go.mod" {
		return fmt.Errorf("trivy report: packages of type %s in %s; the check reads only go.mod", res.Type, res.Target)
	}
	if n > 1 {
		return fmt.Errorf("trivy report: more than one result for go.mod")
	}
	for _, p := range res.Packages {
		if p.Version == "" {
			// The main module, which go.mod declares.
			continue
		}
		key := p.Name + "@" + p.Version
		if _, ok := r.Modules[key]; ok {
			return fmt.Errorf("trivy report: %s is listed twice", key)
		}
		r.Modules[key] = append([]string{}, p.Licenses...)
	}
	return nil
}

// trivy runs the pinned Trivy's license scanner on target in dir and parses
// its report. It turns off what needs a network: the version check, the
// telemetry that Trivy sends by default and the API requests for
// dependencies. gopath is the GOPATH whose module cache Trivy reads the
// licenses from, and cacheDir is Trivy's own cache, which holds the empty
// file emptyConfig. Trivy reads its options from TRIVY_ variables and from a
// trivy.yaml in dir, which could change the licenses it detects, so no
// TRIVY_ variable is passed on and the configuration file is emptyConfig.
// Each caller passes constant arguments (G204).
func trivy(dir, gopath, cacheDir string, cmd *exec.Cmd) (*Report, error) {
	cmd.Dir = dir
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(kv, "TRIVY_") {
			cmd.Env = append(cmd.Env, kv)
		}
	}
	cmd.Env = append(cmd.Env, "PWD="+dir, "GOPATH="+gopath, "TRIVY_CACHE_DIR="+cacheDir, "TRIVY_CONFIG="+filepath.Join(cacheDir, emptyConfig))
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("%s in %s: %w: %s", strings.Join(cmd.Args, " "), dir, err, strings.TrimSpace(stderr.String()))
	}
	return ParseReport(stdout.Bytes())
}

// scanGoMod reports the licenses of the modules in the go.mod in dir.
func scanGoMod(ctx context.Context, dir, gopath, cacheDir string) (*Report, error) {
	return trivy(dir, gopath, cacheDir, exec.CommandContext(ctx, "trivy", "filesystem",
		"--scanners", "license", "--format", "json", "--quiet",
		"--disable-telemetry", "--skip-version-check", "--offline-scan", "go.mod"))
}

// scanLicenseFile reports the license of the file named LICENSE in dir.
func scanLicenseFile(ctx context.Context, dir, gopath, cacheDir string) (*Report, error) {
	return trivy(dir, gopath, cacheDir, exec.CommandContext(ctx, "trivy", "filesystem",
		"--scanners", "license", "--license-full", "--format", "json", "--quiet",
		"--disable-telemetry", "--skip-version-check", "--offline-scan", "LICENSE"))
}
