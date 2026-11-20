// SPDX-License-Identifier: Apache-2.0
// SPDX-FileCopyrightText: The Pumi Authors

package pins

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io/fs"
	"os"
	"path"
	"strconv"
	"strings"
	"time"
)

// The files that hold the pins, relative to the root of the repository.
const (
	InstallScript = ".devcontainer/install-tools.sh"
	Dockerfile    = ".devcontainer/Dockerfile"
	CoreModFile   = "go.mod"
	APIModFile    = "api/go.mod"
	// ToolsDir holds one module file for each pinned Go tool (decision
	// 0086).
	ToolsDir = "tools"
)

// Pin is one pinned version and what the proxy reports about it.
type Pin struct {
	// Name says what is pinned, such as golangci-lint or toolchain.
	Name string
	// File is the file that holds the pin.
	File string
	// Current is the pinned version, as the file writes it.
	Current string
	// Newest is the version that the pin should move to, written as the
	// file writes versions. It is empty when the pin is current.
	Newest string
	// OutOfDate is true when the pin must move now.
	OutOfDate bool
	// Due is when a pin that is not yet out of date becomes out of date, or
	// zero when that date is not known yet.
	Due time.Time
}

// String formats the pin as one line of the report.
func (p Pin) String() string {
	s := fmt.Sprintf("%s %s in %s: ", p.Name, p.Current, p.File)
	switch {
	case p.OutOfDate:
		return s + "out of date, newest " + p.Newest
	case p.Newest == "":
		return s + "current"
	case p.Due.IsZero():
		return s + "newest " + p.Newest + ", not yet out of date"
	}
	return s + "newest " + p.Newest + ", out of date from " + p.Due.UTC().Format(time.DateOnly)
}

// releaseTools are the tools pinned as release binaries in InstallScript, by
// the shell variable that holds the version and the module path without a
// major version suffix.
var releaseTools = []struct {
	name, variable, module string
}{
	{"golangci-lint", "GOLANGCI_LINT_VERSION", "github.com/golangci/golangci-lint"},
	{"Trivy", "TRIVY_VERSION", "github.com/aquasecurity/trivy"},
}

// Run reads the pins of the repository at root and asks proxy about newer
// releases, as of now. It returns the pins in a fixed order: the release
// tools, the Cedar CLI, the Go pins and the tool modules by file.
func Run(ctx context.Context, root string, src Sources, now time.Time) ([]Pin, error) {
	proxy := src.Proxy
	dir := os.DirFS(root)
	read := func(name string) ([]byte, error) {
		return fs.ReadFile(dir, name)
	}
	script, err := read(InstallScript)
	if err != nil {
		return nil, err
	}
	var pins []Pin
	for _, t := range releaseTools {
		p, err := releaseToolPin(ctx, proxy, script, t.name, t.variable, t.module)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", InstallScript, err)
		}
		pins = append(pins, p)
	}
	dockerfile, err := read(Dockerfile)
	if err != nil {
		return nil, err
	}
	cedar, err := cratePin(ctx, src.Crates, dockerfile, cedarCrate, cedarVariable)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", Dockerfile, err)
	}
	pins = append(pins, cedar)
	specs, err := readGoPins(read)
	if err != nil {
		return nil, err
	}
	goPins, err := evaluateGoPins(ctx, proxy, now, specs)
	if err != nil {
		return nil, err
	}
	pins = append(pins, goPins...)
	toolPins, err := readToolPins(ctx, dir, proxy)
	if err != nil {
		return nil, err
	}
	return append(pins, toolPins...), nil
}

// The Rust Cedar CLI is built from its crate in a stage of Dockerfile, at
// the version of the build argument cedarVariable (decision 0146).
const (
	cedarCrate    = "cedar-policy-cli"
	cedarVariable = "ARG CEDAR_VERSION"
)

// cratePin reads the version of a crate from the line `variable=version` of
// the Dockerfile and compares it with the newest stable release on
// crates.io.
func cratePin(ctx context.Context, crates *Crates, dockerfile []byte, crate, variable string) (Pin, error) {
	v, err := shellVariable(dockerfile, variable)
	if err != nil {
		return Pin{}, err
	}
	newest, err := crates.MaxStableVersion(ctx, crate)
	if err != nil {
		return Pin{}, fmt.Errorf("%s: %w", crate, err)
	}
	p := Pin{Name: crate, File: Dockerfile, Current: v}
	if compareSemver("v"+newest, "v"+v) > 0 {
		p.Newest, p.OutOfDate = newest, true
	}
	return p, nil
}

// releaseToolPin reads the version of one release tool from the install
// script and compares it with the newest release of its module.
func releaseToolPin(ctx context.Context, proxy *Proxy, script []byte, name, variable, module string) (Pin, error) {
	v, err := shellVariable(script, variable)
	if err != nil {
		return Pin{}, err
	}
	major, _ := semverMajor("v" + v)
	newest, err := newestRelease(ctx, proxy, module, major)
	if err != nil {
		return Pin{}, fmt.Errorf("%s: %w", name, err)
	}
	p := Pin{Name: name, File: InstallScript, Current: v}
	if compareSemver(newest, "v"+v) > 0 {
		p.Newest, p.OutOfDate = strings.TrimPrefix(newest, "v"), true
	}
	return p, nil
}

// shellVariable returns the value of the one line NAME=value in script.
func shellVariable(script []byte, name string) (string, error) {
	var values []string
	sc := bufio.NewScanner(bytes.NewReader(script))
	for sc.Scan() {
		if v, ok := strings.CutPrefix(sc.Text(), name+"="); ok {
			values = append(values, v)
		}
	}
	if err := sc.Err(); err != nil {
		return "", err
	}
	if len(values) != 1 {
		return "", fmt.Errorf("want one %s= line, got %d", name, len(values))
	}
	if _, ok := parseSemver("v" + values[0]); !ok {
		return "", fmt.Errorf("%s=%s is not a version such as 1.2.3", name, values[0])
	}
	return values[0], nil
}

// newestRelease returns the newest version of the module whose path,
// without its major version suffix, is base, from the major version major
// up. A new major version of a Go module has a new module path, such as
// golangci-lint/v3, so it asks for each next major version until the proxy
// knows none or has only a prerelease of it. A gopkg.in path names its major
// version in another form, so only that path is asked.
func newestRelease(ctx context.Context, proxy *Proxy, base string, major int) (string, error) {
	newest, found, err := proxy.Latest(ctx, majorPath(base, major))
	if err != nil {
		return "", err
	}
	if !found {
		return "", fmt.Errorf("the proxy knows no version of %s", majorPath(base, major))
	}
	if strings.HasPrefix(base, "gopkg.in/") {
		return newest, nil
	}
	for next := max(major+1, 2); ; next++ {
		v, found, err := proxy.Latest(ctx, majorPath(base, next))
		if err != nil || !found || isPrerelease(v) {
			return newest, err
		}
		newest = v
	}
}

// majorPath returns the module path of the major version of the module
// whose path without a major version suffix is base.
func majorPath(base string, major int) string {
	if major < 2 {
		return base
	}
	return base + "/v" + strconv.Itoa(major)
}

// splitMajorPath returns a module path without its major version suffix,
// and the major version that the suffix names, or 1 when there is none.
func splitMajorPath(module string) (string, int) {
	dir, last := path.Split(module)
	if n, err := strconv.Atoi(strings.TrimPrefix(last, "v")); err == nil && strings.HasPrefix(last, "v") && n >= 2 && dir != "" {
		return strings.TrimSuffix(dir, "/"), n
	}
	return module, 1
}

// readToolPins returns one pin for each module that provides a tool line of
// a module file in ToolsDir.
func readToolPins(ctx context.Context, dir fs.FS, proxy *Proxy) ([]Pin, error) {
	files, err := fs.Glob(dir, ToolsDir+"/*.mod")
	if err != nil {
		return nil, err
	}
	var pins []Pin
	for _, name := range files {
		data, err := fs.ReadFile(dir, name)
		if err != nil {
			return nil, err
		}
		mods, err := toolModules(data)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", name, err)
		}
		for _, m := range mods {
			base, major := splitMajorPath(m.Path)
			newest, err := newestRelease(ctx, proxy, base, major)
			if err != nil {
				return nil, fmt.Errorf("%s: %s: %w", name, m.Path, err)
			}
			p := Pin{Name: m.Path, File: name, Current: m.Version}
			if compareSemver(newest, m.Version) > 0 {
				p.Newest, p.OutOfDate = newest, true
			}
			pins = append(pins, p)
		}
	}
	return pins, nil
}

// toolModules returns the required modules that provide the tool lines of a
// tool module file, each once, in the order of the tool lines. The module
// that provides a tool is the required module with the longest path that is
// the tool's package path or a prefix of it.
func toolModules(data []byte) ([]Module, error) {
	m, err := ParseModFile(data)
	if err != nil {
		return nil, err
	}
	var out []Module
	seen := map[string]bool{}
	for _, tool := range m.Tools {
		var best Module
		for _, r := range m.Requires {
			if (tool == r.Path || strings.HasPrefix(tool, r.Path+"/")) && len(r.Path) > len(best.Path) {
				best = r
			}
		}
		if best.Path == "" {
			return nil, fmt.Errorf("no require line provides the tool %s", tool)
		}
		if !seen[best.Path] {
			seen[best.Path] = true
			out = append(out, best)
		}
	}
	return out, nil
}
