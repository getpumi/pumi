// SPDX-License-Identifier: Apache-2.0
// SPDX-FileCopyrightText: The Pumi Authors

package main

import (
	"os"
	"os/exec"
	"strings"
	"syscall"
	"testing"

	"getpumi.dev/pumi/api/events"
)

// The plans of the shown runs on a sign-in. Both implement the archived
// plan in .pumi/plan.md of the trunk.
const (
	// signInCountedPlan is the plan that runs to done, whose usage the
	// runner counts from the figures that the adapter passes.
	signInCountedPlan = "counted-by-the-runner"
	// signInCappedPlan is the plan whose agent run has the small token cap
	// shownTokenCap.
	signInCappedPlan = "capped-by-the-runner"
)

// TestShownRunsOnASignIn runs the real harness that PUMI_SHOWN_HARNESS
// names on the person's own sign-in, in the direct mode: no gateway is in
// the path, the harness talks to the provider itself, and the runner
// counts, prices and caps the usage figures that the adapter passes
// (decisions 0240 and 0241). The first plan runs to done, and its
// invocation.result holds the runner's count. The second plan's agent run
// has a small token cap: the runner stops the run once the figures reach
// it, which for Claude Code is during the run and for codex, which reports
// its usage once per run, when the run ends (decision 0243); the run
// reports cap_reached and the plan escalates. pumi log prints each
// invocation's log, with the harness's version.
//
// It is skipped when PUMI_SHOWN_API_KEY_ENV names a key, because then the
// adapter is in the API key mode and TestShownRunsThroughTheGateway runs.
func TestShownRunsOnASignIn(t *testing.T) {
	if key := os.Getenv(envProviderVariable); key != "" {
		t.Skipf("%s names the key %s, so the adapter is in the API key mode, which TestShownRunsThroughTheGateway shows", envProviderVariable, key)
	}
	h, err := shownHarnessOf()
	if err != nil {
		t.Fatal(err)
	}
	clone, plan, adapter := shownSetup(t)
	s := prepareCommands(t, clone, plan, adapter)
	s.writeCappedConfig()
	ctx := t.Context()

	s.pumi(exec.CommandContext(ctx, "pumi", "add", "--config", "../pumi.txtpb", "--track", "counted", signInCountedPlan))
	first := s.start(exec.CommandContext(ctx, "pumi", "run", "--config", "../pumi.txtpb"))
	s.waitCountedDone(first, signInCountedPlan)
	s.pumi(exec.CommandContext(ctx, "pumi", "status"))
	if err := s.stop(first, syscall.SIGINT); err != nil {
		t.Errorf("pumi run exited with %v after SIGINT, want status 0", err)
	}
	t.Logf("the output of the first pumi run:\n%s", first.out.String())
	countedLog := s.pumi(exec.CommandContext(ctx, "pumi", "log", "--config", "../pumi.txtpb", signInCountedPlan+"/1"))
	if !strings.Contains(countedLog, h.process+" --version: ") {
		t.Errorf("pumi log of %s/1 does not name the version of %s", signInCountedPlan, h.process)
	}

	s.pumi(exec.CommandContext(ctx, "pumi", "add", "--config", cappedConfig, "--track", "capped", signInCappedPlan))
	second := s.start(exec.CommandContext(ctx, "pumi", "run", "--config", cappedConfig))
	s.waitLog(second, runTimeout, "escalation.raised of the second plan", ofTypeFor(events.TypeEscalationRaised, signInCappedPlan))
	s.pumi(exec.CommandContext(ctx, "pumi", "status"))
	s.pumi(exec.CommandContext(ctx, "pumi", "log", signInCappedPlan+"/1"))
	if err := s.stop(second, syscall.SIGINT); err != nil {
		t.Errorf("pumi run exited with %v after SIGINT, want status 0", err)
	}
	t.Logf("the output of the second pumi run:\n%s", second.out.String())

	evs := readLog(t, s.run)
	showLog(t, "shown runs on a sign-in", s.run)
	checkCounted(t, evs, signInCountedPlan)
	checkCapped(t, evs, signInCappedPlan)
	showChange(t, s.run, evs, signInCountedPlan+"/1")
}
