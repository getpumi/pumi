// SPDX-License-Identifier: Apache-2.0
// SPDX-FileCopyrightText: The Pumi Authors

package main

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"google.golang.org/protobuf/encoding/prototext"

	"getpumi.dev/pumi/api/events"
	cloudeventsv1 "getpumi.dev/pumi/api/gen/io/cloudevents/v1"
	configv1 "getpumi.dev/pumi/api/gen/pumi/config/v1"
	eventsv1 "getpumi.dev/pumi/api/gen/pumi/events/v1"
	"getpumi.dev/pumi/internal/controlplane"
	"getpumi.dev/pumi/internal/kernel"
)

// The shown run of recovery and cancel through the pumi commands (decision
// 0195, getpumi/pumi#89). It is skipped unless the environment names a
// ScaleWholesale clone and an archived plan, as for the shown runs of the
// spike (shown_test.go).
//
// The test builds pumi from this package with go install, puts it first on
// the PATH and runs each command as a person would, in a new repository
// that reads the clone's objects. Its trunk branch, main, is the plan's base
// commit with the plan added as .pumi/plan.md, so every plan starts from
// that tree (decision 0209). The configuration file is beside the
// repository. The commands' arguments are constants, because gosec reports
// a command built from variables (G204).

// The plans of the shown run. Both implement the archived plan in
// .pumi/plan.md of the trunk.
const (
	// shownKilledPlan is the plan whose pumi run is killed during its agent
	// run and started again.
	shownKilledPlan = "killed-and-recovered"
	// shownCancelledPlan is the plan that the person cancels while it runs.
	shownCancelledPlan = "cancelled-by-person"
)

// shownPerson is the git user.email of the shown run's repository, the
// person (decision 0208).
const shownPerson = "person@example.com"

// The limits of the shown run through the commands.
const (
	// cancelAfter is how long the second plan's agent run runs before the
	// person cancels it.
	cancelAfter = 10 * time.Second
	// stopLimit is how long the runner has to report that the cancelled
	// invocation finished: its next heartbeat, then the harness's stop.
	stopLimit = 2 * time.Minute
)

// shownCommands is the repository of the shown run, where the commands
// run.
type shownCommands struct {
	t   *testing.T
	run shownRun
}

// prepareCommands makes the shown run's repository, with the trunk main,
// the person's git user.email and the configuration file ../pumi.txtpb.
// It builds pumi and puts it first on the PATH.
func prepareCommands(t *testing.T, clone string, plan shownPlan, adapter string) *shownCommands {
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
	writeFile(t, gitDir, "refs/heads/main", plan.Base+"\n")
	git(t, dir, exec.CommandContext(ctx, "git", "reset", "--quiet", "--hard"))
	git(t, dir, exec.CommandContext(ctx, "git", "config", "user.email", shownPerson))
	if _, err := os.Lstat(filepath.Join(dir, filepath.FromSlash(planFile))); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the base commit %s already has %s", plan.Base, planFile)
	}
	writeFile(t, dir, planFile, string(plan.Text))
	git(t, dir, exec.CommandContext(ctx, "git", "add", "--force", "--", planFile))
	git(t, dir, exec.CommandContext(ctx, "git", "commit", "--quiet", "--message", "Add the plan of the shown run"))
	tree := gitOutput(t, dir, exec.CommandContext(ctx, "git", "rev-parse", "main^{tree}"))
	work := filepath.Join(top, "work")
	if err := os.Mkdir(work, 0o750); err != nil {
		t.Fatal(err)
	}
	run := shownRun{Repository: dir, Tree: tree, Plan: plan.ID, Paths: plan.Paths, Adapter: adapter, WorkDir: work}
	writeFile(t, top, "pumi.txtpb", shownConfig(t, run))
	bin := filepath.Join(top, "bin")
	install := exec.CommandContext(ctx, "go", "install", ".")
	install.Env = append(os.Environ(), "GOBIN="+bin)
	if out, err := install.CombinedOutput(); err != nil {
		t.Fatalf("go install: %v\n%s", err, out)
	}
	// pumi run makes the workspaces in TMPDIR, where the test looks for
	// the processes of an invocation.
	t.Setenv("TMPDIR", work)
	t.Setenv("PATH", bin+string(filepath.ListSeparator)+os.Getenv("PATH"))
	t.Logf("plan %s, base commit %s, trunk main at %s, tree %s",
		plan.ID, plan.Base, gitOutput(t, dir, exec.CommandContext(ctx, "git", "rev-parse", "main")), tree)
	t.Logf("the configuration file ../pumi.txtpb:\n%s", readFile(t, top, "pumi.txtpb"))
	return &shownCommands{t: t, run: run}
}

// shownConfig returns the configuration file of the run (decision 0207):
// the spike workflow, the adapter, the trunk main, the base invocation of
// the shown runs, the project, one plan at a time and one more attempt for
// an invocation in doubt (decision 0201). The kernel sets the input tree.
func shownConfig(t *testing.T, run shownRun) string {
	t.Helper()
	wf, err := filepath.Abs(filepath.Join(workflowsDir, "spike", "workflow.txtpb"))
	if err != nil {
		t.Fatal(err)
	}
	base := shownBase(run)
	base.Input.Tree = ""
	cfg := &configv1.Config{
		Workflow: wf, Adapter: run.Adapter, Trunk: "main", Base: base,
		Project: &configv1.Project{Name: "scale-wholesale"},
		Settings: &configv1.Settings{
			Parallelism: 1,
			MaxAttempts: map[string]int64{"in-doubt": 1},
		},
	}
	b, err := prototext.MarshalOptions{Multiline: true}.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// pumi runs cmd, a pumi command, in the repository and shows it and its
// output. It fails the test when the command fails.
func (s *shownCommands) pumi(cmd *exec.Cmd) {
	s.t.Helper()
	cmd.Dir = s.run.Repository
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	err := cmd.Run()
	s.t.Logf("$ %s\n%s", shownLine(cmd), out.String())
	if err != nil {
		s.t.Fatalf("%s: %v", shownLine(cmd), err)
	}
}

// shownLine returns the command line of cmd as a person types it.
func shownLine(cmd *exec.Cmd) string {
	return strings.Join(append([]string{filepath.Base(cmd.Args[0])}, cmd.Args[1:]...), " ")
}

// background is a pumi run that runs in the background.
type background struct {
	cmd  *exec.Cmd
	out  *syncBuffer
	done chan error
}

// start starts cmd, pumi run, in the repository and waits until pumi
// status, given no configuration file, reaches it through its socket.
func (s *shownCommands) start(cmd *exec.Cmd) *background {
	s.t.Helper()
	cmd.Dir = s.run.Repository
	b := &background{cmd: cmd, out: &syncBuffer{}, done: make(chan error, 1)}
	cmd.Stdout, cmd.Stderr = b.out, b.out
	if err := cmd.Start(); err != nil {
		s.t.Fatal(err)
	}
	s.t.Logf("$ %s &", shownLine(cmd))
	go func() { b.done <- cmd.Wait() }()
	s.t.Cleanup(func() {
		if cmd.ProcessState == nil {
			_ = cmd.Process.Kill()
			s.t.Logf("pumi run was killed at the end of the test: %v", <-b.done)
		}
	})
	deadline := time.Now().Add(commandWait)
	for {
		status := exec.CommandContext(s.t.Context(), "pumi", "status")
		status.Dir = s.run.Repository
		if status.Run() == nil {
			return b
		}
		select {
		case err := <-b.done:
			b.done <- err
			s.t.Fatalf("pumi run stopped with %v before it served its socket; its output:\n%s", err, b.out.String())
		default:
		}
		if time.Now().After(deadline) {
			s.t.Fatalf("pumi run did not serve its socket within %s; its output:\n%s", commandWait, b.out.String())
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// stop sends sig to the pumi run and waits until it exits. It shows the
// run's output.
func (s *shownCommands) stop(b *background, sig syscall.Signal) error {
	s.t.Helper()
	if err := b.cmd.Process.Signal(sig); err != nil {
		s.t.Fatal(err)
	}
	err := <-b.done
	s.t.Logf("pumi run got %s and exited with %v; its output:\n%s", sig, b.cmd.ProcessState, b.out.String())
	return err
}

// waitLog waits until the repository's log has an event for which match
// is true.
func (s *shownCommands) waitLog(b *background, limit time.Duration, what string,
	match func(e *cloudeventsv1.CloudEvent) bool,
) {
	s.t.Helper()
	deadline := time.Now().Add(limit)
	for {
		evs := readLog(s.t, s.run)
		for _, e := range evs {
			if match(e) {
				return
			}
		}
		select {
		case err := <-b.done:
			b.done <- err
			s.t.Fatalf("pumi run stopped with %v before %s; its output:\n%s", err, what, b.out.String())
		default:
		}
		if time.Now().After(deadline) {
			s.t.Fatalf("the log shows no %s after %s; the output of pumi run:\n%s", what, limit, b.out.String())
		}
		time.Sleep(time.Second)
	}
}

// ofTypeFor returns a match for an event of type typ whose subject is
// subject.
func ofTypeFor(typ, subject string) func(e *cloudeventsv1.CloudEvent) bool {
	return func(e *cloudeventsv1.CloudEvent) bool {
		return e.GetType() == typ && e.GetAttributes()["subject"].GetCeString() == subject
	}
}

// entered returns a match for the transition.fired of plan into state.
func (s *shownCommands) entered(plan, state string) func(e *cloudeventsv1.CloudEvent) bool {
	return func(e *cloudeventsv1.CloudEvent) bool {
		if e.GetType() != events.TypeTransitionFired {
			return false
		}
		tf := dataOf(s.t, e, &eventsv1.TransitionFired{})
		return tf.GetPlan() == plan && tf.GetToState() == state
	}
}

// TestShownCommandsRecoverAndCancel adds a plan with pumi add, runs it with
// pumi run, kills pumi run with SIGKILL during the agent run and starts it
// again: the invocation in doubt runs again on retry and the plan reaches
// done with no person (decisions 0191, 0199 to 0201). It then adds a second
// plan, which the person cancels with pumi cancel while its agent run runs
// (decision 0192). It shows the commands, their output and the log.
func TestShownCommandsRecoverAndCancel(t *testing.T) {
	clone, plan, adapter := shownSetup(t)
	s := prepareCommands(t, clone, plan, adapter)
	ctx := t.Context()

	s.pumi(exec.CommandContext(ctx, "pumi", "add", "--config", "../pumi.txtpb", "--track", "recovery", shownKilledPlan))
	first := s.start(exec.CommandContext(ctx, "pumi", "run", "--config", "../pumi.txtpb"))
	s.waitLog(first, commandWait, "invocation.started of the first plan", ofTypeFor(events.TypeInvocationStarted, shownKilledPlan+"/1"))
	time.Sleep(killAfter)
	if has(readLog(t, s.run), events.TypeInvocationResult) {
		t.Fatalf("the invocation ended within %s, before the kill", killAfter)
	}
	s.pumi(exec.CommandContext(ctx, "pumi", "status"))
	running := inDir(s.run.WorkDir)
	if len(running) == 0 {
		t.Fatal("no process runs in the workspace before the kill")
	}
	t.Logf("before the kill, these processes run in the workspace: %s", strings.Join(running, ", "))
	if err := s.stop(first, syscall.SIGKILL); err == nil {
		t.Fatal("pumi run exited with status 0 after SIGKILL")
	}
	leftInWorkspace(t, s.run.WorkDir)
	killedAt := len(readLog(t, s.run))
	s.pumi(exec.CommandContext(ctx, "pumi", "status", "--config", "../pumi.txtpb"))

	second := s.start(exec.CommandContext(ctx, "pumi", "run", "--config", "../pumi.txtpb"))
	s.pumi(exec.CommandContext(ctx, "pumi", "status"))
	s.waitLog(second, controlplane.DefaultLeaseTimeout+commandWait, "lease.expired of the killed run",
		ofTypeFor(events.TypeLeaseExpired, shownKilledPlan+"/1"))
	s.waitLog(second, commandWait, "invocation.started of the run again", ofTypeFor(events.TypeInvocationStarted, shownKilledPlan+"/2"))
	s.pumi(exec.CommandContext(ctx, "pumi", "status"))
	s.waitLog(second, runTimeout, "transition of the first plan to done", s.entered(shownKilledPlan, "done"))
	s.pumi(exec.CommandContext(ctx, "pumi", "status"))

	s.pumi(exec.CommandContext(ctx, "pumi", "add", "--track", "cancel", shownCancelledPlan))
	s.waitLog(second, commandWait, "invocation.started of the second plan", ofTypeFor(events.TypeInvocationStarted, shownCancelledPlan+"/1"))
	time.Sleep(cancelAfter)
	if has(ofTypesFor(readLog(t, s.run), shownCancelledPlan+"/1"), events.TypeInvocationResult) {
		t.Fatalf("the second plan's invocation ended within %s, before the cancel", cancelAfter)
	}
	s.pumi(exec.CommandContext(ctx, "pumi", "status"))
	s.pumi(exec.CommandContext(ctx, "pumi", "cancel", shownCancelledPlan))
	s.waitLog(second, stopLimit, "invocation.finished of the cancelled run",
		ofTypeFor(events.TypeInvocationFinished, shownCancelledPlan+"/1"))
	leftInWorkspace(t, s.run.WorkDir)
	s.pumi(exec.CommandContext(ctx, "pumi", "status"))
	if err := s.stop(second, syscall.SIGINT); err != nil {
		t.Errorf("pumi run exited with %v after SIGINT, want status 0", err)
	}

	evs := readLog(t, s.run)
	showLog(t, "shown run through the commands", s.run)
	t.Logf("the kill left %d events; the runs after it appended %d", killedAt, len(evs)-killedAt)
	checkRecovered(t, evs)
	checkCancelledRun(t, evs)
	showChange(t, s.run, evs, shownKilledPlan+"/2")
}

// ofTypesFor returns the events of evs whose subject is subject.
func ofTypesFor(evs []*cloudeventsv1.CloudEvent, subject string) []*cloudeventsv1.CloudEvent {
	var out []*cloudeventsv1.CloudEvent
	for _, e := range evs {
		if e.GetAttributes()["subject"].GetCeString() == subject {
			out = append(out, e)
		}
	}
	return out
}

// planFired returns the transitions of plan in evs, as from, trigger and
// to.
func planFired(t *testing.T, evs []*cloudeventsv1.CloudEvent, plan string) []string {
	t.Helper()
	var out []string
	for _, tf := range firedIn(t, evs) {
		if tf.GetPlan() == plan {
			out = append(out, tf.GetFromState()+" -"+tf.GetTrigger()+"-> "+tf.GetToState())
		}
	}
	return out
}

// checkRecovered checks the first plan: the killed run's lease expired with
// no result, the plan went back to implementing on in-doubt, its second
// invocation ran once and succeeded, and the plan reached done.
func checkRecovered(t *testing.T, evs []*cloudeventsv1.CloudEvent) {
	t.Helper()
	got := strings.Join(planFired(t, evs, shownKilledPlan), ", ")
	want := strings.Join([]string{
		"implementing -" + kernel.InDoubt + "-> implementing",
		"implementing -result-> done",
	}, ", ")
	if got != want {
		t.Errorf("the transitions of %s are %s, want %s", shownKilledPlan, got, want)
	}
	lost := ofTypesFor(evs, shownKilledPlan+"/1")
	if has(lost, events.TypeInvocationResult) || !has(lost, events.TypeLeaseExpired) {
		t.Errorf("the killed run %s/1 has a result or no expired lease", shownKilledPlan)
	}
	again := ofTypesFor(evs, shownKilledPlan+"/2")
	if n := len(ofType(again, events.TypeLeaseGranted)); n != 1 {
		t.Errorf("the run again %s/2 has %d leases, want 1", shownKilledPlan, n)
	}
	finished := ofType(again, events.TypeInvocationFinished)
	if len(finished) != 1 || dataOf(t, finished[0], &eventsv1.InvocationFinished{}).GetOutcome() != eventsv1.InvocationFinished_OUTCOME_SUCCEEDED {
		t.Errorf("the run again %s/2 did not finish once as succeeded: %v", shownKilledPlan, finished)
	}
	if has(evs, events.TypeEscalationRaised) {
		t.Error("the log has an escalation.raised, but the plans need no person")
	}
}

// checkCancelledRun checks the second plan: it went to cancelled on the
// person's cancel.requested, the kernel asked its invocation to stop, and
// the runner reported the outcome cancelled.
func checkCancelledRun(t *testing.T, evs []*cloudeventsv1.CloudEvent) {
	t.Helper()
	got := strings.Join(planFired(t, evs, shownCancelledPlan), ", ")
	if want := "implementing -" + events.TypeCancelRequested + "-> cancelled"; got != want {
		t.Errorf("the transitions of %s are %s, want %s", shownCancelledPlan, got, want)
	}
	var byPerson, byKernel bool
	for _, e := range ofType(evs, events.TypeCancelRequested) {
		c := dataOf(t, e, &eventsv1.CancelRequested{})
		switch {
		case c.GetPlan() == shownCancelledPlan && c.GetInvocation() == "" && c.GetRequestedBy() == shownPerson:
			byPerson = true
		case c.GetInvocation() == shownCancelledPlan+"/1":
			byKernel = true
		}
	}
	if !byPerson || !byKernel {
		t.Errorf("the log has the person's cancel.requested %v and the kernel's for the invocation %v, want both", byPerson, byKernel)
	}
	finished := ofType(ofTypesFor(evs, shownCancelledPlan+"/1"), events.TypeInvocationFinished)
	if len(finished) != 1 || dataOf(t, finished[0], &eventsv1.InvocationFinished{}).GetOutcome() != eventsv1.InvocationFinished_OUTCOME_CANCELLED {
		t.Errorf("the cancelled run did not finish once as cancelled: %v", finished)
	}
}

// showChange shows the change of the invocation's result, from the input
// tree to the result's tree.
func showChange(t *testing.T, run shownRun, evs []*cloudeventsv1.CloudEvent, invocation string) {
	t.Helper()
	results := ofType(ofTypesFor(evs, invocation), events.TypeInvocationResult)
	if len(results) != 1 {
		t.Fatalf("the invocation %s has %d results, want 1", invocation, len(results))
	}
	result := dataOf(t, results[0], &eventsv1.InvocationResult{})
	if result.GetTree() == "" {
		t.Logf("the result of %s has no tree, so the agent changed nothing", invocation)
		return
	}
	diff := exec.CommandContext(t.Context(), "git", "diff-tree", "--patch", "--stdin")
	diff.Stdin = strings.NewReader(run.Tree + " " + result.GetTree() + "\n")
	t.Logf("the change of %s from the input tree to the result's tree:\n%s", invocation, gitOutput(t, run.Repository, diff))
}
