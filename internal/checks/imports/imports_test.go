// SPDX-License-Identifier: Apache-2.0
// SPDX-FileCopyrightText: The Pumi Authors

package imports

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
)

// The fixture is a small copy of the core's layout in the module
// example.com/core: a nested api module, two components (kernel and runner),
// internal/shared, a check under internal/checks and one binary under cmd/.
// Two third-party modules are replaced by local directories, so go list needs
// no network: the kernel imports example.com/lib, which is on its list, and
// example.com/lib imports example.com/deep.
const (
	core   = "example.com/core"
	kernel = core + "/internal/kernel"
	runner = core + "/internal/runner"
	shared = core + "/internal/shared"
	check  = core + "/internal/checks/c"
	lib    = "example.com/lib"
	deep   = "example.com/deep"
	apiMod = core + "/api"
	conf   = core + "/conformance"
)

const fixtureGoMod = `module example.com/core

go 1.26.0

require (
	example.com/core/api v0.0.0
	example.com/deep v0.0.0 // indirect
	example.com/lib v0.0.0
)

replace (
	example.com/core/api => ./api
	example.com/deep => ./third/deep
	example.com/lib => ./third/lib
)
`

// goFile returns the source of a Go file of package name with a blank import
// of each path.
func goFile(name string, paths ...string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "package %s\n", name)
	for _, p := range paths {
		fmt.Fprintf(&b, "\nimport _ %q\n", p)
	}
	return b.String()
}

// baseFixture returns the files of a fixture that passes every rule.
func baseFixture() map[string]string {
	return map[string]string{
		"go.mod":                          fixtureGoMod,
		"api/go.mod":                      "module example.com/core/api\n\ngo 1.26.0\n",
		"api/api.go":                      goFile("api", "fmt"),
		"conformance/go.mod":              conformanceGoMod,
		"conformance/conformance.go":      goFile("conformance", "testing", apiMod),
		"third/lib/go.mod":                "module example.com/lib\n\ngo 1.26.0\n\nrequire example.com/deep v0.0.0\n",
		"third/lib/lib.go":                goFile("lib", deep),
		"third/deep/go.mod":               "module example.com/deep\n\ngo 1.26.0\n",
		"third/deep/deep.go":              goFile("deep"),
		"internal/kernel/kernel.go":       goFile("kernel", "fmt", lib, kernel+"/sub", shared, apiMod),
		"internal/kernel/sub/sub.go":      goFile("sub"),
		"internal/kernel/kernel_test.go":  goFile("kernel", "testing", shared),
		"internal/runner/runner.go":       goFile("runner", shared),
		"internal/runner/runner_test.go":  goFile("runner_test", runner, "testing"),
		"internal/shared/shared.go":       goFile("shared", "strings", apiMod),
		"internal/checks/c/c.go":          goFile("c", "os", shared, apiMod),
		"internal/checks/cmd/c/main.go":   goFile("main", check),
		"cmd/core/main.go":                goFile("main", kernel, runner, shared),
		"cmd/core/main_test.go":           goFile("main", "testing", kernel),
		"internal/kernel/testdata/bad.go": goFile("bad", runner),
	}
}

// fixtureConfig is the configuration for the fixture.
func fixtureConfig() *Config {
	return &Config{
		API:         Group{Path: "api", Modules: []Module{}},
		Conformance: Group{Path: "conformance", Modules: []Module{}},
		Components: []Component{
			{Name: "kernel", Path: "internal/kernel", Modules: []Module{{Module: lib, Reason: "The kernel needs it."}}},
			{Name: "runner", Path: "internal/runner", Modules: []Module{}},
		},
		Shared:   Shared{Path: "internal/shared"},
		Checks:   Group{Path: "internal/checks", Modules: []Module{}},
		Commands: Group{Path: "cmd", Modules: []Module{}},
	}
}

// fixture writes the base fixture, changed by change, to a temporary
// directory and returns it. A file whose new content is empty is removed.
// go list runs with GOPROXY=off, so the tests show that the check needs no
// network.
func fixture(t *testing.T, change map[string]string) string {
	t.Helper()
	t.Setenv("GOPROXY", "off")
	t.Setenv("GOFLAGS", "")
	files := baseFixture()
	for name, src := range change {
		if src == "" {
			delete(files, name)
			continue
		}
		files[name] = src
	}
	dir := t.TempDir()
	for name, src := range files {
		path := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(src), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// findings runs the check on the fixture changed by change and returns its
// findings.
func findings(t *testing.T, change map[string]string) []Finding {
	t.Helper()
	res, err := Check(t.Context(), fixture(t, change), fixtureConfig())
	if err != nil {
		t.Fatal(err)
	}
	return res.Findings
}

func assertFindings(t *testing.T, got, want []Finding) {
	t.Helper()
	if !reflect.DeepEqual(got, want) {
		t.Errorf("findings:\n%s\nwant:\n%s", lines(got), lines(want))
	}
}

func lines(fs []Finding) string {
	var b strings.Builder
	for _, f := range fs {
		b.WriteString("\t" + f.String() + "\n")
	}
	return b.String()
}

const (
	componentProblem = "a component never imports another component; only the main packages under cmd/ import more than one component (decision 0063)"
	sharedProblem    = "internal/shared imports only the standard library, the api module and its own packages (decision 0063)"
	checksProblem    = "internal/checks imports only the standard library, the api module, internal/shared, its own packages and the modules on its list (decisions 0063, 0107 and 0123)"
	toChecksProblem  = "only packages under internal/checks import internal/checks, because the checks are not part of the binaries (decisions 0107 and 0123)"
	toCmdProblem     = "only packages under cmd import packages under cmd, which wire the components together (decision 0063)"
	noGroupProblem   = "is not in a component, internal/shared, internal/checks or cmd as .pumi/imports.json lists them (decision 0105)"
)

func TestCheckPassesAGoodLayout(t *testing.T) {
	// The go command skips these files and directories, and so does the
	// check.
	res, err := Check(t.Context(), fixture(t, map[string]string{
		"internal/_old/old.go":          goFile("old", kernel, runner),
		"internal/.hidden/hidden.go":    goFile("hidden", kernel, runner),
		"internal/kernel/_skip.go":      goFile("kernel", runner),
		"internal/kernel/vendor/v/v.go": goFile("v", runner),
		"internal/nested/go.mod":        "module example.com/nested\n\ngo 1.26.0\n",
		"internal/nested/nested.go":     goFile("nested", runner),
	}), fixtureConfig())
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Findings) != 0 {
		t.Errorf("findings:\n%s\nwant none", lines(res.Findings))
	}
	// kernel, kernel/sub, runner, shared, c, cmd/c and cmd/core.
	if res.Packages != 7 {
		t.Errorf("Packages = %d, want 7", res.Packages)
	}
}

func TestCheckCountsModulesPerBinary(t *testing.T) {
	res, err := Check(t.Context(), fixture(t, map[string]string{
		"cmd/other/main.go": goFile("main", runner),
	}), fixtureConfig())
	if err != nil {
		t.Fatal(err)
	}
	// internal/checks/cmd/c is a main package, but not a binary.
	want := []Binary{
		{Path: "cmd/core", Direct: []string{lib}, Indirect: []string{deep}},
		{Path: "cmd/other"},
	}
	if !reflect.DeepEqual(res.Binaries, want) {
		t.Errorf("Binaries = %+v, want %+v", res.Binaries, want)
	}
}

func TestCheckCountsAModuleThatABinaryImportsDirectlyAsDirect(t *testing.T) {
	res, err := Check(t.Context(), fixture(t, map[string]string{
		"internal/runner/runner.go": goFile("runner", shared, deep),
	}), &Config{
		API:         Group{Path: "api", Modules: []Module{}},
		Conformance: Group{Path: "conformance", Modules: []Module{}},
		Components: []Component{
			{Name: "kernel", Path: "internal/kernel", Modules: []Module{{Module: lib, Reason: "r"}}},
			{Name: "runner", Path: "internal/runner", Modules: []Module{{Module: deep, Reason: "r"}}},
		},
		Shared:   Shared{Path: "internal/shared"},
		Checks:   Group{Path: "internal/checks"},
		Commands: Group{Path: "cmd"},
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []Binary{{Path: "cmd/core", Direct: []string{deep, lib}}}
	if !reflect.DeepEqual(res.Binaries, want) {
		t.Errorf("Binaries = %+v, want %+v", res.Binaries, want)
	}
}

// TestCheckDoesNotNeedGit shows that the check runs where git cannot read
// the repository, as in CI's container, where the workspace belongs to
// another user: the .git directory here is empty, so git fails. go list
// would read the git status of the main packages to stamp the build.
func TestCheckDoesNotNeedGit(t *testing.T) {
	dir := fixture(t, nil)
	if err := os.Mkdir(filepath.Join(dir, ".git"), 0o750); err != nil {
		t.Fatal(err)
	}
	if _, err := Check(t.Context(), dir, fixtureConfig()); err != nil {
		t.Fatal(err)
	}
}

func TestCheckFailsAComponentThatImportsAnotherComponent(t *testing.T) {
	got := findings(t, map[string]string{
		"internal/runner/runner.go": goFile("runner", shared, kernel),
	})
	assertFindings(t, got, []Finding{{Package: runner, Import: kernel, Problem: componentProblem}})
}

func TestCheckFailsAComponentThatImportsAnotherComponentsSubpackage(t *testing.T) {
	got := findings(t, map[string]string{
		"internal/runner/runner.go": goFile("runner", kernel+"/sub"),
	})
	assertFindings(t, got, []Finding{{Package: runner, Import: kernel + "/sub", Problem: componentProblem}})
}

func TestCheckFailsAComponentTestThatImportsAnotherComponent(t *testing.T) {
	got := findings(t, map[string]string{
		"internal/runner/runner_test.go": goFile("runner_test", runner, kernel),
	})
	assertFindings(t, got, []Finding{{Package: runner, Import: kernel, Problem: componentProblem}})
}

func TestCheckFailsANonCmdPackageThatImportsMoreThanOneComponent(t *testing.T) {
	got := findings(t, map[string]string{
		// The changes to runner.go and kernel_test.go avoid import cycles.
		"internal/kernel/kernel.go":      goFile("kernel", lib, runner),
		"internal/kernel/kernel_test.go": goFile("kernel", "testing"),
		"internal/runner/runner.go":      goFile("runner"),
		"internal/checks/c/c.go":         goFile("c", kernel, runner),
		"internal/shared/shared.go":      goFile("shared", kernel, runner),
	})
	assertFindings(t, got, []Finding{
		{Package: check, Import: kernel, Problem: checksProblem},
		{Package: check, Import: runner, Problem: checksProblem},
		{Package: kernel, Import: runner, Problem: componentProblem},
		{Package: shared, Import: kernel, Problem: sharedProblem},
		{Package: shared, Import: runner, Problem: sharedProblem},
	})
}

func TestCheckFailsSharedThatImportsAThirdPartyModule(t *testing.T) {
	got := findings(t, map[string]string{
		"internal/shared/shared.go": goFile("shared", lib),
	})
	assertFindings(t, got, []Finding{{Package: shared, Import: lib, Problem: sharedProblem}})
}

func TestCheckFailsImportsOfCmdAndOfChecksFromOutside(t *testing.T) {
	got := findings(t, map[string]string{
		"internal/shared/shared.go":  goFile("shared", check),
		"internal/checks/c/c.go":     goFile("c", "os"),
		"internal/shared/sub/sub.go": goFile("sub", shared),
		"cmd/core/lib/lib.go":        goFile("lib"),
		"internal/kernel/kernel.go":  goFile("kernel", lib, core+"/cmd/core/lib"),
	})
	assertFindings(t, got, []Finding{
		{Package: kernel, Import: core + "/cmd/core/lib", Problem: toCmdProblem},
		{Package: shared, Import: check, Problem: sharedProblem},
	})
}

func TestCheckFailsAComponentThatImportsACheck(t *testing.T) {
	got := findings(t, map[string]string{
		"internal/runner/runner.go": goFile("runner", check),
	})
	assertFindings(t, got, []Finding{{Package: runner, Import: check, Problem: toChecksProblem}})
}

func TestCheckFailsACmdPackageThatImportsACheck(t *testing.T) {
	got := findings(t, map[string]string{
		"cmd/core/main.go": goFile("main", kernel, check),
	})
	assertFindings(t, got, []Finding{{Package: core + "/cmd/core", Import: check, Problem: toChecksProblem}})
}

func TestCheckFailsACheckThatImportsCmd(t *testing.T) {
	got := findings(t, map[string]string{
		"cmd/core/lib/lib.go":    goFile("lib"),
		"internal/checks/c/c.go": goFile("c", core+"/cmd/core/lib"),
	})
	assertFindings(t, got, []Finding{{Package: check, Import: core + "/cmd/core/lib", Problem: checksProblem}})
}

func TestCheckFailsAThirdPartyModuleThatIsNotOnTheList(t *testing.T) {
	got := findings(t, map[string]string{
		"internal/runner/runner.go":      goFile("runner", lib),
		"internal/checks/c/c.go":         goFile("c", deep),
		"cmd/core/main.go":               goFile("main", kernel, lib),
		"internal/kernel/kernel.go":      goFile("kernel", lib, deep),
		"internal/runner/runner_test.go": goFile("runner", "testing", deep),
	})
	assertFindings(t, got, []Finding{
		{Package: core + "/cmd/core", Import: lib, Problem: "the module example.com/lib is not on the list of cmd in .pumi/imports.json (decision 0064)"},
		{Package: check, Import: deep, Problem: "the module example.com/deep is not on the list of internal/checks in .pumi/imports.json (decision 0064)"},
		{Package: kernel, Import: deep, Problem: "the module example.com/deep is not on the list of the kernel component in .pumi/imports.json (decision 0064)"},
		{Package: runner, Import: deep, Problem: "the module example.com/deep is not on the list of the runner component in .pumi/imports.json (decision 0064)"},
		{Package: runner, Import: lib, Problem: "the module example.com/lib is not on the list of the runner component in .pumi/imports.json (decision 0064)"},
	})
}

func TestCheckFailsTheAPIModuleImportingTheCore(t *testing.T) {
	got := findings(t, map[string]string{
		// The core imports the api module, so these imports are of core
		// packages that do not import it, to avoid an import cycle.
		"api/api.go":          goFile("api", kernel+"/sub"),
		"api/sub/sub.go":      goFile("sub", core),
		"api/sub/sub_test.go": goFile("sub_test", runner),
		"api/ok/ok.go":        goFile("ok", apiMod),
	})
	assertFindings(t, got, []Finding{
		{Package: apiMod, Import: kernel + "/sub", Problem: apiProblem},
		{Package: apiMod + "/sub", Import: core, Problem: apiProblem},
		{Package: apiMod + "/sub", Import: runner, Problem: apiProblem},
	})
}

// TestCheckReportsTheAPIModuleImportingTheCoreInACycle shows that an import
// of the core from the api module is reported even when it makes an import
// cycle, which stops go list from loading the core: shared imports the api
// module, and the api module imports shared. The core is not checked then.
func TestCheckReportsTheAPIModuleImportingTheCoreInACycle(t *testing.T) {
	res, err := Check(t.Context(), fixture(t, map[string]string{
		"api/api.go":                goFile("api", shared),
		"internal/runner/runner.go": goFile("runner", kernel),
	}), fixtureConfig())
	if err != nil {
		t.Fatal(err)
	}
	assertFindings(t, res.Findings, []Finding{{Package: apiMod, Import: shared, Problem: apiProblem}})
	if res.Packages != 0 || res.Binaries != nil {
		t.Errorf("Packages = %d, Binaries = %+v, want the core unchecked", res.Packages, res.Binaries)
	}
}

// TestCheckReadsFilesThatTheBuildLeavesOut shows that files for another
// platform or behind a build tag are checked too, so CI on one platform sees
// every import.
func TestCheckReadsFilesThatTheBuildLeavesOut(t *testing.T) {
	other := "windows"
	if runtime.GOOS == "windows" {
		other = "linux"
	}
	got := findings(t, map[string]string{
		"internal/runner/runner_" + other + ".go":    goFile("runner", kernel),
		"internal/runner/integ_test.go":              "//go:build integration\n\n" + goFile("runner", runner+"/only"),
		"internal/runner/only/only_" + other + ".go": goFile("only", lib),
		"internal/shared/shared_" + other + ".go":    "//go:build cgo\n\n" + goFile("shared", deep),
	})
	assertFindings(t, got, []Finding{
		{Package: runner, Import: kernel, Problem: componentProblem},
		{Package: runner + "/only", Import: lib, Problem: "the module example.com/lib is not on the list of the runner component in .pumi/imports.json (decision 0064)"},
		{Package: shared, Import: deep, Problem: sharedProblem},
	})
}

// TestCheckDoesNotBlameATestDependency shows that a package compiled again
// for another package's test is checked as itself: cmd/core imports the
// kernel and the runner, which is allowed, and the kernel's external test
// imports cmd/core/lib, which breaks a rule of its own.
func TestCheckDoesNotBlameATestDependency(t *testing.T) {
	got := findings(t, map[string]string{
		"cmd/core/lib/lib.go":              goFile("lib", kernel, runner),
		"internal/kernel/kernel_x_test.go": goFile("kernel_test", core+"/cmd/core/lib"),
	})
	assertFindings(t, got, []Finding{{Package: kernel, Import: core + "/cmd/core/lib", Problem: toCmdProblem}})
}

func TestCheckFailsWhenTheAPIPathIsNotTheAPIModule(t *testing.T) {
	for _, api := range []string{"third", "third/deep"} {
		cfg := fixtureConfig()
		cfg.API.Path = api
		_, err := Check(t.Context(), fixture(t, nil), cfg)
		if err == nil || !strings.Contains(err.Error(), `"api": `+api+" is the module") {
			t.Errorf("Check() with api %q: error = %v, want one about the api module", api, err)
		}
	}
}

// apiGoMod is a go.mod for the fixture's api module that requires the two
// third-party modules.
const apiGoMod = `module example.com/core/api

go 1.26.0

require (
	example.com/deep v0.0.0 // indirect
	example.com/lib v0.0.0
)

replace (
	example.com/deep => ../third/deep
	example.com/lib => ../third/lib
)
`

func TestCheckFailsTheAPIModuleImportingAThirdPartyModuleNotOnItsList(t *testing.T) {
	got := findings(t, map[string]string{
		"api/go.mod":                apiGoMod,
		"api/api.go":                goFile("api", lib),
		"api/sub/sub_test.go":       goFile("sub_test", deep),
		"internal/runner/runner.go": goFile("runner", kernel),
	})
	// The core is still checked.
	assertFindings(t, got, []Finding{
		{Package: apiMod, Import: lib, Problem: "the module example.com/lib is not on the list of the api module in .pumi/imports.json (decisions 0064 and 0125)"},
		{Package: apiMod + "/sub", Import: deep, Problem: "the module example.com/deep is not on the list of the api module in .pumi/imports.json (decisions 0064 and 0125)"},
		{Package: runner, Import: kernel, Problem: componentProblem},
	})
}

func TestCheckPassesTheAPIModuleImportingAModuleOnItsList(t *testing.T) {
	cfg := fixtureConfig()
	cfg.API.Modules = []Module{{Module: lib, Reason: "r"}}
	res, err := Check(t.Context(), fixture(t, map[string]string{
		"api/go.mod": apiGoMod,
		"api/api.go": goFile("api", lib),
	}), cfg)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Findings) != 0 {
		t.Errorf("findings:\n%s\nwant none", lines(res.Findings))
	}
	// cmd/core imports the kernel, which imports the api module and lib.
	want := []Binary{{Path: "cmd/core", Direct: []string{lib}, Indirect: []string{deep}}}
	if !reflect.DeepEqual(res.Binaries, want) {
		t.Errorf("Binaries = %+v, want %+v", res.Binaries, want)
	}
}

// conformanceGoMod is the go.mod of the fixture's conformance module, which
// requires the api module from the same tree, as the core does.
const conformanceGoMod = `module example.com/core/conformance

go 1.26.0

require example.com/core/api v0.0.0

replace (
	example.com/core/api => ../api
	example.com/deep => ../third/deep
	example.com/lib => ../third/lib
)
`

// conformanceThirdGoMod is conformanceGoMod with the two third-party
// modules required as well.
const conformanceThirdGoMod = `module example.com/core/conformance

go 1.26.0

require (
	example.com/core/api v0.0.0
	example.com/deep v0.0.0 // indirect
	example.com/lib v0.0.0
)

replace (
	example.com/core/api => ../api
	example.com/deep => ../third/deep
	example.com/lib => ../third/lib
)
`

func TestCheckFailsTheConformanceModuleImportingTheCore(t *testing.T) {
	got := findings(t, map[string]string{
		"conformance/go.mod":          conformanceGoMod + "\nrequire example.com/core v0.0.0\n\nreplace example.com/core => ../\n",
		"conformance/conformance.go":  goFile("conformance", shared),
		"conformance/sub/sub_test.go": goFile("sub_test", runner),
		"conformance/ok/ok.go":        goFile("ok", apiMod, conf),
		"internal/runner/runner.go":   goFile("runner", kernel),
	})
	// The core is still checked.
	assertFindings(t, got, []Finding{
		{Package: conf, Import: shared, Problem: conformanceProblem},
		{Package: conf + "/sub", Import: runner, Problem: conformanceProblem},
		{Package: runner, Import: kernel, Problem: componentProblem},
	})
}

func TestCheckFailsTheConformanceModuleImportingAThirdPartyModuleNotOnItsList(t *testing.T) {
	got := findings(t, map[string]string{
		"conformance/go.mod":          conformanceThirdGoMod,
		"conformance/conformance.go":  goFile("conformance", lib),
		"conformance/sub/sub_test.go": goFile("sub_test", deep),
	})
	assertFindings(t, got, []Finding{
		{Package: conf, Import: lib, Problem: "the module example.com/lib is not on the list of the conformance module in .pumi/imports.json (decisions 0064 and 0244)"},
		{Package: conf + "/sub", Import: deep, Problem: "the module example.com/deep is not on the list of the conformance module in .pumi/imports.json (decisions 0064 and 0244)"},
	})
}

func TestCheckPassesTheConformanceModuleImportingAModuleOnItsList(t *testing.T) {
	cfg := fixtureConfig()
	cfg.Conformance.Modules = []Module{{Module: lib, Reason: "r"}}
	res, err := Check(t.Context(), fixture(t, map[string]string{
		"conformance/go.mod":         conformanceThirdGoMod,
		"conformance/conformance.go": goFile("conformance", lib, apiMod, "net/http"),
	}), cfg)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Findings) != 0 {
		t.Errorf("findings:\n%s\nwant none", lines(res.Findings))
	}
}

// TestCheckFailsTheAPIModuleImportingTheConformanceModule shows that the
// import is reported alone, because go list cannot load the core then.
func TestCheckFailsTheAPIModuleImportingTheConformanceModule(t *testing.T) {
	got := findings(t, map[string]string{
		"api/api.go":                goFile("api", conf),
		"internal/runner/runner.go": goFile("runner", kernel),
	})
	assertFindings(t, got, []Finding{
		{Package: apiMod, Import: conf, Problem: "the api module imports nothing from the conformance module, which imports the api module (decision 0244)"},
	})
}

func TestCheckFailsWhenTheConformancePathIsNotTheConformanceModule(t *testing.T) {
	for _, path := range []string{"third", "third/deep"} {
		cfg := fixtureConfig()
		cfg.Conformance.Path = path
		_, err := Check(t.Context(), fixture(t, nil), cfg)
		if err == nil || !strings.Contains(err.Error(), `"conformance": `+path+" is the module") {
			t.Errorf("Check() with conformance %q: error = %v, want one about the conformance module", path, err)
		}
	}
}

func TestCheckFailsAPackageInNoGroup(t *testing.T) {
	got := findings(t, map[string]string{
		"internal/other/other.go": goFile("other", kernel, runner),
		"core.go":                 goFile("core"),
	})
	assertFindings(t, got, []Finding{
		{Package: core, Problem: noGroupProblem},
		{Package: core + "/internal/other", Problem: noGroupProblem},
	})
}

func TestCheckFailsWhenAListedPathHasNoPackage(t *testing.T) {
	dir := fixture(t, nil)
	cfg := fixtureConfig()
	cfg.Components = append(cfg.Components, Component{Name: "gateway", Path: "internal/gateway"})
	_, err := Check(t.Context(), dir, cfg)
	if err == nil || !strings.Contains(err.Error(), "the gateway component: no package of the core module is in internal/gateway") {
		t.Errorf("Check() error = %v, want one about internal/gateway", err)
	}
}

func TestCheckFailsWhenThePackagesCannotBeLoaded(t *testing.T) {
	tests := []struct {
		name   string
		change map[string]string
		want   string
	}{
		{"bad imports in the core", map[string]string{"internal/runner/runner.go": "package runner\n\nimport (\n"}, "go list: example.com/core/internal/runner: expected ')'"},
		{"missing package", map[string]string{"internal/runner/runner.go": goFile("runner", core+"/internal/nothing")}, "go list: example.com/core/internal/nothing: no required module provides package"},
		{"bad imports in the api", map[string]string{"api/api.go": "package api\n\nimport (\n"}, "api/api.go:3:10: expected ')'"},
		{"missing api module", map[string]string{"api/go.mod": "", "api/api.go": ""}, "/api: no such file or directory"},
		{"missing third-party in api", map[string]string{"api/api.go": goFile("api", "example.com/missing")}, "example.com/core/api: no module in the build list of the api module provides example.com/missing"},
		{"bad imports in the conformance module", map[string]string{"conformance/conformance.go": "package conformance\n\nimport (\n"}, "conformance/conformance.go:3:10: expected ')'"},
		{"missing conformance module", map[string]string{"conformance/go.mod": "", "conformance/conformance.go": ""}, "/conformance: no such file or directory"},
		{"missing third-party in conformance", map[string]string{"conformance/conformance.go": goFile("conformance", "example.com/missing")}, "example.com/core/conformance: no module in the build list of the conformance module provides example.com/missing"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Check(t.Context(), fixture(t, tt.change), fixtureConfig())
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Errorf("Check() error = %v, want one that contains %q", err, tt.want)
			}
		})
	}
}

func TestFindingString(t *testing.T) {
	f := Finding{Package: "a/b", Import: "c/d", Problem: "a problem"}
	if got, want := f.String(), "a/b imports c/d: a problem"; got != want {
		t.Errorf("String() = %q, want %q", got, want)
	}
	f = Finding{Package: "a/b", Problem: "is somewhere"}
	if got, want := f.String(), "a/b is somewhere"; got != want {
		t.Errorf("String() = %q, want %q", got, want)
	}
}
