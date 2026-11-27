// SPDX-License-Identifier: Apache-2.0
// SPDX-FileCopyrightText: The Pumi Authors

package main

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"

	"google.golang.org/protobuf/encoding/prototext"

	"getpumi.dev/pumi/api/events"
	cloudeventsv1 "getpumi.dev/pumi/api/gen/io/cloudevents/v1"
	configv1 "getpumi.dev/pumi/api/gen/pumi/config/v1"
	eventsv1 "getpumi.dev/pumi/api/gen/pumi/events/v1"
	harnessv1 "getpumi.dev/pumi/api/gen/pumi/harness/v1"
	runnerv1 "getpumi.dev/pumi/api/gen/pumi/runner/v1"
)

// The shown run of the OpenSpec adapter (getpumi/pumi#146, decision 0281).
// It is skipped unless the environment names a ScaleWholesale clone, the
// commit of it before the change specChange was archived, such as the
// parent of 9842f55, and the adapter's executable. The openspec on the PATH is the
// one the adapter runs, which its own tests check is the pinned version.
//
// The test builds pumi, makes a repository that reads the clone's objects,
// with the trunk main at the commit, and runs the commands as a person
// would: pumi add adds the plan named after the change (decision 0283), and
// pumi run runs it on the spec-archive workflow of internal/kernel/testdata
// with the adapter as the spec check program. The spec gate passes from the
// adapter's spec.valid (decision 0299), and the archive step archives the
// change and returns the tree (decision 0278). The test shows the commands,
// the log of each invocation and the files that the archive changed.
const (
	envSpecClone   = "PUMI_SHOWN_SPEC_CLONE"
	envSpecCommit  = "PUMI_SHOWN_SPEC_COMMIT"
	envSpecAdapter = "PUMI_SHOWN_SPEC_ADAPTER"
)

// specChange is the ScaleWholesale change of the shown run, and the name
// of its plan (decision 0283). Its proposal has a narrative, and its delta
// gives the contracts capability the narrative its living spec lacks. The
// commands' arguments are constants, because gosec reports a command built
// from variables (G204).
const specChange = "contracts-state-their-rules"

// prepareSpecRun makes the shown run's repository with the trunk main at
// commit, the person's git user.email and the configuration file
// ../pumi.txtpb, which names adapter as the spec check program. It builds
// pumi and puts it first on the PATH.
func prepareSpecRun(t *testing.T, clone, commit, adapter string) *shownCommands {
	t.Helper()
	ctx := t.Context()
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	objects := gitOutput(t, clone, exec.CommandContext(ctx, "git", "rev-parse", "--path-format=absolute", "--git-common-dir"))
	top := t.TempDir()
	dir := filepath.Join(top, "repository")
	if err := os.Mkdir(dir, 0o750); err != nil {
		t.Fatal(err)
	}
	git(t, dir, exec.CommandContext(ctx, "git", "init", "--quiet", "--initial-branch=main"))
	gitDir := filepath.Join(dir, ".git")
	writeFile(t, gitDir, "objects/info/alternates", filepath.Join(objects, "objects")+"\n")
	writeFile(t, gitDir, "refs/heads/main", commit+"\n")
	git(t, dir, exec.CommandContext(ctx, "git", "reset", "--quiet", "--hard"))
	git(t, dir, exec.CommandContext(ctx, "git", "config", "user.email", shownPerson))
	tree := gitOutput(t, dir, exec.CommandContext(ctx, "git", "rev-parse", "main^{tree}"))
	work := filepath.Join(top, "work")
	if err := os.Mkdir(work, 0o750); err != nil {
		t.Fatal(err)
	}
	run := shownRun{Repository: dir, Tree: tree, WorkDir: work}
	writeFile(t, top, "pumi.txtpb", specConfig(t, adapter))
	bin := filepath.Join(top, "bin")
	install := exec.CommandContext(ctx, "go", "install", ".")
	install.Env = append(os.Environ(), "GOBIN="+bin)
	if out, err := install.CombinedOutput(); err != nil {
		t.Fatalf("go install: %v\n%s", err, out)
	}
	t.Setenv("TMPDIR", work)
	t.Setenv("PATH", bin+string(filepath.ListSeparator)+os.Getenv("PATH"))
	t.Logf("trunk main at %s, tree %s", commit, tree)
	t.Logf("the configuration file ../pumi.txtpb:\n%s", readFile(t, top, "pumi.txtpb"))
	return &shownCommands{t: t, run: run}
}

// specConfig returns the configuration file of the run: the spec-archive
// workflow, the trunk main, write paths under openspec/ only, the spec
// check program adapter with its
// SHA-256, one attempt of the spec gate, and the project, whose archive
// step is the default, after implementing. The workflow has no agent run
// and no external action, so the one adapter that models must list as the
// check adapter (decision 0253) is never started; it names the OpenSpec
// adapter's executable.
func specConfig(t *testing.T, adapter string) string {
	t.Helper()
	wf, err := filepath.Abs(filepath.Join(workflowsDir, "spec-archive", "workflow.txtpb"))
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(readFile(t, filepath.Dir(adapter), filepath.Base(adapter)))
	hash := hex.EncodeToString(sum[:])
	cfg := &configv1.Config{
		Workflow: wf, Trunk: "main",
		Base: &runnerv1.Invocation{
			Input: &harnessv1.Input{Repo: "scale-wholesale"},
			// The archive step writes the change's archive and the living
			// specs, and the runner refuses a write outside these paths
			// (decision 0159).
			Capabilities: &harnessv1.Capabilities{WritePaths: []string{"openspec/**"}},
			Budget:       &harnessv1.Usage{Tokens: 1, CostMicrodollars: 1, Seconds: 600},
		},
		Project:  &configv1.Project{Name: "scale-wholesale"},
		Settings: &configv1.Settings{Parallelism: 1, MaxAttempts: map[string]int64{"spec": 1}},
		Checks:   map[string]*configv1.Check{"spec": {Executable: adapter, Sha256: hash}},
		Models: &configv1.Models{
			Adapters: map[string]*configv1.Adapter{"unused": {
				Executable: adapter, Sha256: hash, CredentialMode: configv1.Adapter_CREDENTIAL_MODE_SIGN_IN,
			}},
			CheckAdapter: "unused",
		},
	}
	out, err := prototext.MarshalOptions{Multiline: true}.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return string(out)
}

// TestShownSpecAndArchive runs the shown run of the OpenSpec adapter: the
// plan goes from the spec state through the archive step to done.
func TestShownSpecAndArchive(t *testing.T) {
	clone, commit, adapter := os.Getenv(envSpecClone), os.Getenv(envSpecCommit), os.Getenv(envSpecAdapter)
	if clone == "" || commit == "" || adapter == "" {
		t.Skipf("%s, %s and %s do not name a clone, the commit before %s was archived and the OpenSpec adapter",
			envSpecClone, envSpecCommit, envSpecAdapter, specChange)
	}
	s := prepareSpecRun(t, clone, commit, adapter)
	ctx := t.Context()
	s.pumi(exec.CommandContext(ctx, "openspec", "--version"))
	s.pumi(exec.CommandContext(ctx, "pumi", "add", "--config", "../pumi.txtpb", "--track", "spec", specChange))
	b := s.start(exec.CommandContext(ctx, "pumi", "run", "--config", "../pumi.txtpb"))
	s.waitLog(b, runTimeout, "the end of the plan", func(e *cloudeventsv1.CloudEvent) bool {
		if e.GetType() != events.TypeTransitionFired {
			return false
		}
		f := dataOf(t, e, &eventsv1.TransitionFired{})
		return f.GetToState() == "done" || strings.HasSuffix(f.GetToState(), "-escalated")
	})
	s.pumi(exec.CommandContext(ctx, "pumi", "status"))
	s.pumi(exec.CommandContext(ctx, "pumi", "log", specChange+"/1"))
	s.pumi(exec.CommandContext(ctx, "pumi", "log", specChange+"/2"))
	if err := s.stop(b, syscall.SIGINT); err != nil {
		t.Errorf("pumi run exited with %v after SIGINT, want status 0", err)
	}
	evs := readLog(t, s.run)
	checkSpecTransitions(t, evs)
	checkArchive(t, s, evs)
}

// checkSpecTransitions shows the plan's transitions and the spec gate's
// decision, and fails unless the plan went from spec through archiving to
// done.
func checkSpecTransitions(t *testing.T, evs []*cloudeventsv1.CloudEvent) {
	t.Helper()
	var states []string
	for _, f := range firedIn(t, evs) {
		states = append(states, f.GetFromState()+" -> "+f.GetToState())
	}
	t.Logf("the plan's transitions:\n%s", strings.Join(states, "\n"))
	if want := []string{"spec -> archiving", "archiving -> done"}; !slices.Equal(states, want) {
		t.Fatalf("the transitions are %q, want %q", states, want)
	}
	for _, e := range evs {
		if e.GetType() != events.TypeDecisionMade {
			continue
		}
		if d := dataOf(t, e, &eventsv1.DecisionMade{}); slices.Contains(d.GetDeterminingPolicies(), "gate.spec") {
			t.Logf("the spec gate's decision: %s by %q, basis %v", d.GetOutcome(), d.GetDeterminingPolicies(), d.GetBasis().GetFacts())
		}
	}
}

// checkArchive shows the files that the archive step changed, from the
// input tree to the tree it returned, and fails unless it archived the
// change and changed the living specs. It names both trees by refs, so the
// git arguments stay constant.
func checkArchive(t *testing.T, s *shownCommands, evs []*cloudeventsv1.CloudEvent) {
	t.Helper()
	archived := ""
	for _, e := range evs {
		if e.GetType() == events.TypeInvocationResult {
			if r := dataOf(t, e, &eventsv1.InvocationResult{}); r.GetInvocation() == specChange+"/2" {
				archived = r.GetTree()
			}
		}
	}
	if archived == "" || archived == s.run.Tree {
		t.Fatalf("the archive step returned the tree %q; want a tree other than the input tree %s", archived, s.run.Tree)
	}
	gitDir := filepath.Join(s.run.Repository, ".git")
	writeFile(t, gitDir, "refs/shown/input", s.run.Tree+"\n")
	writeFile(t, gitDir, "refs/shown/archived", archived+"\n")
	diff := gitOutput(t, s.run.Repository, exec.CommandContext(t.Context(), "git", "diff", "--name-status", "--find-renames", "refs/shown/input", "refs/shown/archived"))
	t.Logf("$ git diff --name-status --find-renames <input tree> <archived tree>\n%s", diff)
	if !strings.Contains(diff, "\topenspec/changes/archive/") || !strings.Contains(diff, "\topenspec/specs/") {
		t.Errorf("the archived tree has no archive of the change %s or no change to the living specs", specChange)
	}
}
