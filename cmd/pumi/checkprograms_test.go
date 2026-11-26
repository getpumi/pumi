// SPDX-License-Identifier: Apache-2.0
// SPDX-FileCopyrightText: The Pumi Authors

package main

import (
	"maps"
	"path/filepath"
	"strings"
	"testing"

	configv1 "getpumi.dev/pumi/api/gen/pumi/config/v1"
	"getpumi.dev/pumi/internal/kernel/firstworkflow"
	"getpumi.dev/pumi/internal/runner"
)

// The tests of the check programs that the configuration names by the
// checks' component names (decision 0272) and of the project's archive
// setting (decision 0278).

// programText returns a check program entry for the component name, in
// text format, with the fields fields.
func programText(name, fields string) string {
	return `checks { key: "` + name + `" value { ` + fields + ` } }`
}

// builtInPrograms are entries for the checks of the built-in workflow.
var builtInPrograms = programText("spec", `executable: "bin/adapter-openspec" sha256: "`+validSHA+`"`) + " " +
	programText("checks", `executable: "/opt/pumi-checks" sha256: "`+validSHA+`"`)

// TestReadConfigRefusesABadCheckProgram: a check program needs an
// executable and a SHA-256 in lowercase hexadecimal, as an adapter does
// (decisions 0245 and 0272).
func TestReadConfigRefusesABadCheckProgram(t *testing.T) {
	base := models(signedIn, keyed, implementer, checks)
	for name, tc := range map[string]struct{ file, want string }{
		"no executable":           {programText("spec", `sha256: "`+validSHA+`"`), `check program "spec": it has no executable`},
		"no SHA-256":              {programText("spec", `executable: "a"`), `check program "spec": its sha256`},
		"an uppercase SHA-256":    {programText("spec", `executable: "a" sha256: "`+strings.ToUpper(validSHA)+`"`), `check program "spec": its sha256`},
		"an empty component name": {programText("", `executable: "a" sha256: "`+validSHA+`"`), "a check program with no component name"},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := readConfig(writeTestConfig(t, base+" "+tc.file))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("readConfig = %v, want an error that names %s", err, tc.want)
			}
		})
	}
}

// TestEachCheckOfTheWorkflowNeedsAProgram: a workflow check with no
// program is refused with an error that names the check, and a program
// for a check that the workflow does not use is accepted (decision 0272).
func TestEachCheckOfTheWorkflowNeedsAProgram(t *testing.T) {
	wf, err := firstworkflow.Load()
	if err != nil {
		t.Fatal(err)
	}
	extra := programText("protected-paths", `executable: "/opt/protected" sha256: "`+validSHA+`"`)
	cfg, err := readConfig(writeTestConfig(t, models(signedIn, keyed, implementer, checks)+" "+builtInPrograms+" "+extra))
	if err != nil {
		t.Fatalf("readConfig = %v, want the file read", err)
	}
	if err := checkPrograms(cfg, wf); err != nil {
		t.Errorf("checkPrograms with a program for each check and one more = %v, want none", err)
	}
	cfg, err = readConfig(writeTestConfig(t, models(signedIn, keyed, implementer, checks)+" "+programText("checks", `executable: "/opt/pumi-checks" sha256: "`+validSHA+`"`)))
	if err != nil {
		t.Fatalf("readConfig = %v, want the file read", err)
	}
	err = checkPrograms(cfg, wf)
	if err == nil || !strings.Contains(err.Error(), `"spec"`) || strings.Contains(err.Error(), `"checks"`) || !strings.Contains(err.Error(), "0272") {
		t.Errorf("checkPrograms with no program for spec = %v, want an error that names spec alone", err)
	}
}

// TestTheArchiveStepIsAfterImplementingByDefault: a project that does not
// set the archive step archives after implementing, and one that sets it
// keeps its setting (decision 0278).
func TestTheArchiveStepIsAfterImplementingByDefault(t *testing.T) {
	for setting, want := range map[string]configv1.Project_ArchiveStep{
		"": configv1.Project_ARCHIVE_STEP_AFTER_IMPLEMENTING,
		"archive_step: ARCHIVE_STEP_AFTER_IMPLEMENTING": configv1.Project_ARCHIVE_STEP_AFTER_IMPLEMENTING,
		"archive_step: ARCHIVE_STEP_AFTER_CONFORMANCE":  configv1.Project_ARCHIVE_STEP_AFTER_CONFORMANCE,
		"archive_step: ARCHIVE_STEP_OFF":                configv1.Project_ARCHIVE_STEP_OFF,
	} {
		dir := t.TempDir()
		writeFile(t, dir, "pumi.txtpb", "workflow: \"workflow.txtpb\"\ntrunk: \"main\"\nproject { name: \"case\" "+setting+" }\n"+models(signedIn, keyed, implementer, checks)+"\n")
		cfg, err := readConfig(dir + "/pumi.txtpb")
		if err != nil {
			t.Fatalf("readConfig with %q = %v", setting, err)
		}
		if got := cfg.project().ArchiveStep; got != want {
			t.Errorf("the archive step of a project with %q is %v, want %v", setting, got, want)
		}
	}
	dir := t.TempDir()
	writeFile(t, dir, "pumi.txtpb", "workflow: \"workflow.txtpb\"\ntrunk: \"main\"\nproject { name: \"case\" archive_step: 9 }\n"+models(signedIn, keyed, implementer, checks)+"\n")
	if _, err := readConfig(dir + "/pumi.txtpb"); err == nil || !strings.Contains(err.Error(), "archive_step") {
		t.Errorf("readConfig of an unknown archive step = %v, want an error that names archive_step", err)
	}
}

// TestTheRunnerGetsTheCheckPrograms: pumi run gives the runner each check
// program by its component name, with a relative executable made absolute
// from the directory of the configuration file (decision 0272).
func TestTheRunnerGetsTheCheckPrograms(t *testing.T) {
	path := writeTestConfig(t, models(signedIn, keyed, implementer, checks)+" "+builtInPrograms)
	cfg, err := readConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	got := cfg.checks()
	want := map[string]runner.Check{
		"spec":   {Path: filepath.Join(filepath.Dir(path), "bin", "adapter-openspec"), SHA256: validSHA},
		"checks": {Path: "/opt/pumi-checks", SHA256: validSHA},
	}
	if !maps.Equal(got, want) {
		t.Errorf("the runner's check programs are %v, want %v", got, want)
	}
}
