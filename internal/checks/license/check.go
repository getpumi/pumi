// SPDX-License-Identifier: Apache-2.0
// SPDX-FileCopyrightText: The Pumi Authors

package license

import (
	"slices"
	"strings"
)

// Input is what Evaluate checks: the policy and the exceptions, the
// third-party modules that the binaries include, Trivy's report on go.mod,
// the Go standard library and Trivy's report on its LICENSE file.
type Input struct {
	Policy     *Policy
	Exceptions []Exception
	Modules    []Module
	Report     *Report
	Std        Module
	StdReport  *Report
}

// Checked is one module that the check checked, with the licenses Trivy
// detects and the exception it passes with, if any.
type Checked struct {
	Module    Module
	Licenses  []string
	Exception *Exception
}

// Finding is one module that fails the check, by its path and, unless the
// finding is about an exception for a module that no binary includes, its
// version.
type Finding struct {
	Module  string
	Version string
	Problem string
}

// String formats the finding as one line.
func (f Finding) String() string {
	if f.Version == "" {
		return f.Module + ": " + f.Problem
	}
	return f.Module + " " + f.Version + ": " + f.Problem
}

// Evaluate checks each module and the standard library against the policy
// and the exceptions, and each exception against the modules. It returns the
// checked modules, the standard library first and then in the order of
// in.Modules, and the findings in the same order, followed by the findings
// for exceptions of modules that no binary includes.
//
// A module fails when Trivy does not list it or detects no license for it,
// when any license that Trivy detects for it is not allowed, since the report
// does not say whether several licenses are a choice or all apply, when a
// license that passes by exception has no exception for it, and when its
// exception names a license that Trivy no longer detects (decisions 0075 and
// 0078). Every license must pass (decision 0129). An exception for a module
// that no binary includes fails too (decision 0128).
func Evaluate(in Input) ([]Checked, []Finding) {
	exceptions := map[string]*Exception{}
	for i := range in.Exceptions {
		exceptions[in.Exceptions[i].Module] = &in.Exceptions[i]
	}
	var checked []Checked
	var findings []Finding
	add := func(m Module, licenses []string, problems []string) {
		for _, p := range problems {
			findings = append(findings, Finding{Module: m.Path, Version: m.Version, Problem: p})
		}
		if len(problems) == 0 {
			checked = append(checked, Checked{Module: m, Licenses: licenses, Exception: exceptions[m.Path]})
		}
	}
	add(in.Std, in.StdReport.Files, in.Policy.problems(in.StdReport.Files, exceptions[in.Std.Path]))
	built := map[string]bool{in.Std.Path: true}
	for _, m := range in.Modules {
		built[m.Path] = true
		licenses, listed := in.Report.Modules[m.Path+"@"+m.Version]
		if !listed {
			add(m, nil, []string{"Trivy does not list the module; run go mod tidy"})
			continue
		}
		add(m, licenses, in.Policy.problems(licenses, exceptions[m.Path]))
	}
	for _, e := range in.Exceptions {
		if !built[e.Module] {
			findings = append(findings, Finding{Module: e.Module, Problem: "has an exception, but no binary includes the module; remove the exception"})
		}
	}
	return checked, findings
}

// problems returns why a module with the licenses that Trivy detects and the
// exception exc, which may be nil, fails, or nothing when it passes.
func (p *Policy) problems(licenses []string, exc *Exception) []string {
	if len(licenses) == 0 {
		return []string{"Trivy detects no license"}
	}
	var out []string
	for _, l := range licenses {
		switch {
		case slices.Contains(p.Allowed, l):
		case !slices.Contains(p.ByException, l):
			out = append(out, "is under "+l+", which is not an allowed license (decision 0075)")
		case exc == nil:
			out = append(out, "is under "+l+", which passes only with an exception in .pumi/license-exceptions.json (decisions 0075 and 0078)")
		}
	}
	// An exception for a license that Trivy does not detect is reported
	// here, whatever the other licenses are.
	if exc != nil && !slices.Contains(licenses, exc.License) {
		out = append(out, "has an exception for "+exc.License+", but Trivy detects "+strings.Join(licenses, ", ")+"; review the module again (decision 0078)")
	}
	return out
}
