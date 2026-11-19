// SPDX-License-Identifier: Apache-2.0
// SPDX-FileCopyrightText: The Pumi Authors

// Package imports is the import graph check (decision 0108). It reads the
// layout and the third-party allowlists from .pumi/imports.json (decisions
// 0064 and 0112), the imports of every Go file from the files themselves,
// and the packages and modules from go list, and fails when:
//
//   - a component imports another component (decision 0063);
//   - internal/shared imports anything but the standard library, the api
//     module and its own packages (decision 0063);
//   - internal/checks imports a component or a package under cmd, or a
//     package outside internal/checks imports internal/checks, which is not
//     part of the binaries (decision 0107);
//   - a package outside cmd imports a package under cmd (decision 0063);
//   - a package imports a third-party module that is not on its list
//     (decision 0064);
//   - the api module imports anything from the core module (decision 0105);
//     such an import can make an import cycle that stops go list from
//     loading the core, so it is reported alone;
//   - a package of the core module is in none of the places that
//     .pumi/imports.json lists (decision 0105).
//
// So a package outside cmd never imports more than one component: a
// component may import none but its own, and internal/shared and
// internal/checks may import none. Every Go file counts: test files, and
// files that build constraints leave out on the platform the check runs on.
//
// It also reports the third-party modules that each binary under cmd links,
// direct and indirect, which decision 0064 tracks without a limit.
package imports

import (
	"context"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
)

// Finding is an import that breaks a rule, or, with Import empty, a package
// that is in no place of the configuration.
type Finding struct {
	Package string
	Import  string
	Problem string
}

// String formats the finding as one line.
func (f Finding) String() string {
	if f.Import == "" {
		return f.Package + " " + f.Problem
	}
	return f.Package + " imports " + f.Import + ": " + f.Problem
}

// Binary is the main package of one binary under cmd, by its path in the
// core module, and the third-party modules it links. Direct holds the
// modules that a package of the core or the api module imports; Indirect
// holds the rest. Both are sorted.
type Binary struct {
	Path     string
	Direct   []string
	Indirect []string
}

// Result is what Check found: the findings, sorted; the binaries, sorted by
// path; and the number of packages of the core module, not counting test
// variants.
type Result struct {
	Findings []Finding
	Binaries []Binary
	Packages int
}

// kind is the kind of place a package of the core module is in.
type kind int

const (
	inNone kind = iota
	inComponent
	inShared
	inChecks
	inCommands
)

// place is where a package of the core module is. index is the component's
// index in the configuration.
type place struct {
	kind  kind
	index int
}

type checker struct {
	cfg  *Config
	core string
	api  string
	// mods holds the build list of the core module, without the core and
	// the api module.
	mods []string
	// pkgs holds each package that go list reported in the core module, by
	// its import path.
	pkgs map[string]*pkg
	// findings is a set, so each finding is reported once.
	findings map[Finding]bool
}

// Check runs the check on the core module in dir with the configuration cfg.
// It checks the api module first; when the api module imports the core, it
// returns only those findings, because such an import can make an import
// cycle that stops go list from loading the core. It returns an error when go
// list fails or reports a package that it cannot load, other than a package
// of the core module that the api module imports, when the api path does not
// hold the api module, and when a place in the configuration holds no
// package.
func Check(ctx context.Context, dir string, cfg *Config) (*Result, error) {
	c := &checker{cfg: cfg, pkgs: map[string]*pkg{}, findings: map[Finding]bool{}}
	apiDir := filepath.Join(dir, filepath.FromSlash(cfg.API))
	var err error
	if c.core, err = mainModule(ctx, dir); err != nil {
		return nil, err
	}
	if c.api, err = mainModule(ctx, apiDir); err != nil {
		return nil, err
	}
	// Without this, a wrong path would make the core its own api module,
	// and every import of the core would pass.
	if want := c.core + "/" + cfg.API; c.api != want {
		return nil, fmt.Errorf(`"api": %s is the module %s, not %s`, cfg.API, c.api, want)
	}
	if err := c.checkAPI(apiDir); err != nil {
		return nil, err
	}
	if len(c.findings) > 0 {
		return &Result{Findings: c.sorted()}, nil
	}
	return c.checkCoreModule(ctx, dir)
}

func (c *checker) checkCoreModule(ctx context.Context, dir string) (*Result, error) {
	corePkgs, err := list(ctx, dir)
	if err != nil {
		return nil, err
	}
	mods, err := buildList(ctx, dir)
	if err != nil {
		return nil, err
	}
	for _, m := range mods {
		if m != c.core && m != c.api {
			c.mods = append(c.mods, m)
		}
	}
	if err := c.index(corePkgs); err != nil {
		return nil, err
	}
	res := &Result{}
	if res.Packages, err = c.checkCore(dir); err != nil {
		return nil, err
	}
	res.Findings = c.sorted()
	if res.Binaries, err = c.binaries(corePkgs); err != nil {
		return nil, err
	}
	return res, nil
}

// sorted returns the findings, sorted by package, import and problem.
func (c *checker) sorted() []Finding {
	var fs []Finding
	for f := range c.findings {
		fs = append(fs, f)
	}
	slices.SortFunc(fs, func(a, b Finding) int {
		return strings.Compare(a.Package+"\x00"+a.Import+"\x00"+a.Problem, b.Package+"\x00"+b.Import+"\x00"+b.Problem)
	})
	return fs
}

// index fills c.pkgs and fails on a package that go list could not load.
func (c *checker) index(pkgs []pkg) error {
	for i := range pkgs {
		p := &pkgs[i]
		if p.Error != nil {
			return fmt.Errorf("go list: %s: %s", p.ImportPath, p.Error.Err)
		}
		c.pkgs[p.ImportPath] = p
	}
	return nil
}

// checkCore checks the imports of every Go file of each package of the core
// module in dir and returns the number of packages.
func (c *checker) checkCore(dir string) (int, error) {
	rels, err := packageDirs(dir)
	if err != nil {
		return 0, err
	}
	used := map[place]bool{}
	for _, rel := range rels {
		path := c.core + "/" + rel
		if rel == "." {
			path = c.core
		}
		from := c.placeOf(path)
		used[from] = true
		if from.kind == inNone {
			c.findings[Finding{Package: path, Problem: c.noPlaceProblem()}] = true
			continue
		}
		if err := c.checkPackage(filepath.Join(dir, filepath.FromSlash(rel)), path, from); err != nil {
			return 0, err
		}
	}
	return len(rels), c.checkUsed(used)
}

func (c *checker) checkPackage(dir, path string, from place) error {
	imps, err := dirImports(dir)
	if err != nil {
		return err
	}
	for _, imp := range imps {
		problem, err := c.problem(from, imp)
		if err != nil {
			return fmt.Errorf("%s: %w", path, err)
		}
		if problem != "" {
			c.findings[Finding{Package: path, Import: imp, Problem: problem}] = true
		}
	}
	return nil
}

// checkUsed fails when a place in the configuration holds no package, which
// is most likely a wrong path.
func (c *checker) checkUsed(used map[place]bool) error {
	for i, comp := range c.cfg.Components {
		if !used[place{inComponent, i}] {
			return fmt.Errorf("the %s component: no package of the core module is in %s", comp.Name, comp.Path)
		}
	}
	for _, g := range []struct {
		kind       kind
		name, path string
	}{
		{inShared, "shared", c.cfg.Shared.Path},
		{inChecks, "checks", c.cfg.Checks.Path},
		{inCommands, "commands", c.cfg.Commands.Path},
	} {
		if !used[place{kind: g.kind}] {
			return fmt.Errorf("%s: no package of the core module is in %s", g.name, g.path)
		}
	}
	return nil
}

// checkAPI checks that no Go file of the api module in dir imports a
// package of the core module.
func (c *checker) checkAPI(dir string) error {
	rels, err := packageDirs(dir)
	if err != nil {
		return err
	}
	for _, rel := range rels {
		path := c.api + "/" + rel
		if rel == "." {
			path = c.api
		}
		imps, err := dirImports(filepath.Join(dir, filepath.FromSlash(rel)))
		if err != nil {
			return err
		}
		for _, imp := range imps {
			if c.inCore(imp) {
				c.findings[Finding{Package: path, Import: imp, Problem: apiProblem}] = true
			}
		}
	}
	return nil
}

// inCore reports whether path is a package of the core module.
func (c *checker) inCore(path string) bool {
	return within(path, c.core) && !within(path, c.api)
}

// placeOf returns the place of a package of the core module.
func (c *checker) placeOf(path string) place {
	if path == c.core {
		return place{}
	}
	rel := strings.TrimPrefix(path, c.core+"/")
	for i, comp := range c.cfg.Components {
		if within(rel, comp.Path) {
			return place{inComponent, i}
		}
	}
	switch {
	case within(rel, c.cfg.Shared.Path):
		return place{kind: inShared}
	case within(rel, c.cfg.Checks.Path):
		return place{kind: inChecks}
	case within(rel, c.cfg.Commands.Path):
		return place{kind: inCommands}
	}
	return place{}
}

// problem returns the rule that an import from a package in from breaks, or
// "" when it breaks none.
func (c *checker) problem(from place, imp string) (string, error) {
	switch {
	case within(imp, c.api):
		return "", nil
	case within(imp, c.core):
		return c.coreProblem(from, c.placeOf(imp)), nil
	}
	if standard(imp) {
		return "", nil
	}
	mod := c.moduleOf(imp)
	if mod == "" {
		return "", fmt.Errorf("no module in the build list provides %s", imp)
	}
	return c.thirdPartyProblem(from, mod), nil
}

// moduleOf returns the third-party module in the build list whose path is
// the longest prefix of the package path imp, or "".
func (c *checker) moduleOf(imp string) string {
	best := ""
	for _, m := range c.mods {
		if within(imp, m) && len(m) > len(best) {
			best = m
		}
	}
	return best
}

// coreProblem returns the rule that an import from a package in from of a
// package in to breaks, or "".
func (c *checker) coreProblem(from, to place) string {
	switch {
	case to.kind == inNone, from == to:
		// A package in no place is reported once, by itself.
		return ""
	case from.kind == inShared:
		return c.sharedProblem()
	case to.kind == inShared:
		return ""
	case from.kind == inChecks:
		return c.checksProblem()
	case to.kind == inChecks:
		return fmt.Sprintf("only packages under %[1]s import %[1]s, because the checks are not part of the binaries (decision 0107)", c.cfg.Checks.Path)
	case to.kind == inCommands:
		return fmt.Sprintf("only packages under %[1]s import packages under %[1]s, which wire the components together (decision 0063)", c.cfg.Commands.Path)
	case from.kind == inCommands:
		return ""
	}
	return fmt.Sprintf("a component never imports another component; only the main packages under %s/ import more than one component (decision 0063)", c.cfg.Commands.Path)
}

// thirdPartyProblem returns the rule that an import of the third-party
// module mod from a package in from breaks, or "".
func (c *checker) thirdPartyProblem(from place, mod string) string {
	var mods []Module
	var name string
	switch from.kind {
	case inShared:
		return c.sharedProblem()
	case inComponent:
		comp := c.cfg.Components[from.index]
		mods, name = comp.Modules, "the "+comp.Name+" component"
	case inChecks:
		mods, name = c.cfg.Checks.Modules, c.cfg.Checks.Path
	case inCommands:
		mods, name = c.cfg.Commands.Modules, c.cfg.Commands.Path
	case inNone:
		return ""
	}
	if slices.ContainsFunc(mods, func(m Module) bool { return m.Module == mod }) {
		return ""
	}
	return fmt.Sprintf("the module %s is not on the list of %s in .pumi/imports.json (decision 0064)", mod, name)
}

func (c *checker) sharedProblem() string {
	return c.cfg.Shared.Path + " imports only the standard library, the api module and its own packages (decision 0063)"
}

func (c *checker) checksProblem() string {
	return c.cfg.Checks.Path + " imports only the standard library, the api module, " + c.cfg.Shared.Path + ", its own packages and the modules on its list (decisions 0063 and 0107)"
}

func (c *checker) noPlaceProblem() string {
	return fmt.Sprintf("is not in a component, %s, %s or %s as .pumi/imports.json lists them (decision 0105)", c.cfg.Shared.Path, c.cfg.Checks.Path, c.cfg.Commands.Path)
}

const apiProblem = "the api module imports nothing from the core module (decisions 0063 and 0105)"

// binaries returns each binary under cmd with the third-party modules it
// links.
func (c *checker) binaries(pkgs []pkg) ([]Binary, error) {
	var bins []Binary
	for i := range pkgs {
		p := &pkgs[i]
		if !p.inModule() || p.Name != "main" || c.placeOf(p.ImportPath).kind != inCommands {
			continue
		}
		bin, err := c.binary(p)
		if err != nil {
			return nil, err
		}
		bins = append(bins, bin)
	}
	slices.SortFunc(bins, func(a, b Binary) int { return strings.Compare(a.Path, b.Path) })
	return bins, nil
}

func (c *checker) binary(p *pkg) (Binary, error) {
	direct := map[string]bool{}
	all := map[string]bool{}
	for _, path := range append([]string{p.ImportPath}, p.Deps...) {
		q, ok := c.pkgs[path]
		if !ok {
			return Binary{}, fmt.Errorf("go list did not report %s, a dependency of %s", path, p.ImportPath)
		}
		if mod := c.thirdParty(q); mod != "" {
			all[mod] = true
		} else if !q.Standard {
			c.addImportedModules(q, direct)
		}
	}
	bin := Binary{Path: strings.TrimPrefix(p.ImportPath, c.core+"/")}
	for mod := range all {
		if direct[mod] {
			bin.Direct = append(bin.Direct, mod)
		} else {
			bin.Indirect = append(bin.Indirect, mod)
		}
	}
	slices.Sort(bin.Direct)
	slices.Sort(bin.Indirect)
	return bin, nil
}

// addImportedModules adds to mods the third-party modules that p, a package
// of the core or the api module, imports directly.
func (c *checker) addImportedModules(p *pkg, mods map[string]bool) {
	for _, imp := range p.Imports {
		if q, ok := c.pkgs[imp]; ok {
			if mod := c.thirdParty(q); mod != "" {
				mods[mod] = true
			}
		}
	}
}

// thirdParty returns the module of p when it is a third-party module, and ""
// otherwise.
func (c *checker) thirdParty(p *pkg) string {
	if p.Standard || p.Module == nil || p.Module.Path == c.core || p.Module.Path == c.api {
		return ""
	}
	return p.Module.Path
}
