// SPDX-License-Identifier: Apache-2.0
// SPDX-FileCopyrightText: The Pumi Authors

package main

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"testing"

	configv1 "getpumi.dev/pumi/api/gen/pumi/config/v1"
	"getpumi.dev/pumi/internal/kernel/workflow"
)

// ownConfig is the configuration file of Pumi's own runs (decisions 0290,
// 0291 and 0307), from this package's directory.
const ownConfig = "../../.pumi/pumi.txtpb"

// The workflow files of Pumi's own runs, relative to the configuration
// file's directory: one with only the spec state (decision 0288) and one
// with the spec, planning, implementing and archive states (decision 0293).
const (
	ownSpecWorkflow  = "workflows/spec/workflow.txtpb"
	ownBuildWorkflow = "workflows/build/workflow.txtpb"
)

// TestTheOwnConfigurationIsRead: the configuration file of Pumi's own runs
// passes readConfig and names the Claude Code adapter on the sign-in path,
// the OpenSpec adapter as the spec check program, the trunk main, the
// project's commands and protected paths, and the hard caps (decision
// 0291, getpumi/pumi#150).
func TestTheOwnConfigurationIsRead(t *testing.T) {
	cfg, err := readConfig(ownConfig)
	if err != nil {
		t.Fatalf("readConfig = %v, want the file read", err)
	}
	f := cfg.file
	if f.GetTrunk() != "main" {
		t.Errorf("the trunk is %q, want main", f.GetTrunk())
	}
	if f.GetProject().GetName() != "pumi" {
		t.Errorf("the project is %q, want pumi", f.GetProject().GetName())
	}
	if len(f.GetProject().GetCheckCommands()) == 0 {
		t.Error("the project names no check commands")
	}
	if len(f.GetProject().GetProtectedPaths()) == 0 {
		t.Error("the project names no protected paths")
	}
	if h := f.GetHardCaps(); h.GetTokens() <= 0 || h.GetCostMicrodollars() <= 0 || h.GetSeconds() <= 0 {
		t.Errorf("the hard caps are %v, want a limit on tokens, cost and time", h)
	}
	a, ok := f.GetModels().GetAdapters()["claude-code"]
	if !ok || a.GetCredentialMode() != configv1.Adapter_CREDENTIAL_MODE_SIGN_IN {
		t.Errorf("the adapter claude-code is %v, want it on the sign-in path", a)
	}
	for _, role := range []string{"planner", "implementer"} {
		if r := f.GetModels().GetRoles()[role]; r.GetAdapter() != "claude-code" {
			t.Errorf("the model role %s is %v, want it on claude-code", role, r)
		}
	}
	if c, ok := f.GetChecks()["spec"]; !ok || filepath.Base(c.GetExecutable()) != "adapter-openspec" {
		t.Errorf("the spec check program is %v, want the OpenSpec adapter", c)
	}
}

// TestTheOwnWorkflowsLoad: each workflow file of Pumi's own runs loads
// with the workflow loader, from the directory of the configuration file,
// has the states its decision gives it, and starts only checks for which
// the configuration names a program (decisions 0288, 0293 and 0272).
func TestTheOwnWorkflowsLoad(t *testing.T) {
	cases := []struct {
		file   string
		stages []string
	}{
		{ownSpecWorkflow, []string{"spec", "spec-escalated", "done", "cancelled"}},
		{ownBuildWorkflow, []string{
			"spec", "spec-escalated", "planning", "planning-escalated", "implementing", "implementing-escalated",
			"archiving", "archiving-escalated", "done", "cancelled",
		}},
	}
	cfg, err := readConfig(ownConfig)
	if err != nil {
		t.Fatalf("readConfig = %v, want the file read", err)
	}
	for _, c := range cases {
		t.Run(c.file, func(t *testing.T) {
			p := filepath.Join(cfg.dir, c.file)
			wf, err := workflow.Load(os.DirFS(filepath.Dir(p)), filepath.Base(p))
			if err != nil {
				t.Fatalf("workflow.Load = %v, want the workflow loaded", err)
			}
			if got, want := stateNames(wf), slices.Sorted(slices.Values(c.stages)); !slices.Equal(got, want) {
				t.Errorf("the states are %q, want %q", got, want)
			}
			if err := checkPrograms(cfg, wf); err != nil {
				t.Errorf("checkPrograms = %v, want a program for each check", err)
			}
			checkAttempts(t, cfg, wf)
		})
	}
}

// stateNames returns the names of the states of wf, sorted, without the
// superstate active.
func stateNames(wf *workflow.Workflow) []string {
	var names []string
	for _, s := range wf.Definition().GetStates() {
		if s.GetName() != "active" {
			names = append(names, s.GetName())
		}
	}
	slices.Sort(names)
	return names
}

// checkAttempts fails the test unless the configuration sets max_attempts
// for each gate of wf and, when a state with no gate starts an invocation,
// for in-doubt. A decision about a gate with no maxAttempts denies
// (decision 0143), and a retry from a state with no gate reads the
// in-doubt attempts (decisions 0201 and 0237).
func checkAttempts(t *testing.T, cfg *config, wf *workflow.Workflow) {
	t.Helper()
	attempts := cfg.file.GetSettings().GetMaxAttempts()
	for _, s := range wf.Definition().GetStates() {
		if g := s.GetGate(); g != "" && attempts[g] <= 0 {
			t.Errorf("the state %s has the gate %s, for which the configuration sets no max_attempts", s.GetName(), g)
		}
		if s.GetGate() == "" && s.GetInvoke() != nil && attempts["in-doubt"] <= 0 {
			t.Errorf("the state %s retries with the in-doubt attempts, for which the configuration sets no max_attempts", s.GetName())
		}
	}
}

// TestTheOwnConfigurationNamesAnOwnWorkflow: the configuration file names
// one of the two workflow files of Pumi's own runs.
func TestTheOwnConfigurationNamesAnOwnWorkflow(t *testing.T) {
	cfg, err := readConfig(ownConfig)
	if err != nil {
		t.Fatalf("readConfig = %v, want the file read", err)
	}
	if w := cfg.file.GetWorkflow(); w != ownSpecWorkflow && w != ownBuildWorkflow {
		t.Errorf("the workflow is %q, want %q or %q", w, ownSpecWorkflow, ownBuildWorkflow)
	}
}

// TestTheOwnAdaptersMatchTheirHashes: each adapter and check program that
// the configuration names, when it has been built at its path, has the
// SHA-256 that the configuration gives it (decision 0308). It skips an
// executable that has not been built, as in CI, which does not build the
// adapters.
func TestTheOwnAdaptersMatchTheirHashes(t *testing.T) {
	cfg, err := readConfig(ownConfig)
	if err != nil {
		t.Fatalf("readConfig = %v, want the file read", err)
	}
	type program struct{ name, path, sha string }
	var programs []program
	for name, a := range cfg.adapters() {
		programs = append(programs, program{"the adapter " + name, a.Path, a.SHA256})
	}
	for name, c := range cfg.checks() {
		programs = append(programs, program{"the check program " + name, c.Path, c.SHA256})
	}
	for _, p := range programs {
		data, err := os.ReadFile(p.path)
		if errors.Is(err, fs.ErrNotExist) {
			t.Logf("%s is not built at %s", p.name, p.path)
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		sum := sha256.Sum256(data)
		if got := hex.EncodeToString(sum[:]); got != p.sha {
			t.Errorf("%s at %s has the SHA-256 %s, want %s", p.name, p.path, got, p.sha)
		} else {
			t.Logf("%s at %s has the SHA-256 of the configuration", p.name, p.path)
		}
	}
}
