// SPDX-License-Identifier: Apache-2.0
// SPDX-FileCopyrightText: The Pumi Authors

package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"testing"
	"time"

	"google.golang.org/protobuf/encoding/protojson"

	"getpumi.dev/pumi/api/events"
	cloudeventsv1 "getpumi.dev/pumi/api/gen/io/cloudevents/v1"
	controlplanev1 "getpumi.dev/pumi/api/gen/pumi/controlplane/v1"
	"getpumi.dev/pumi/api/gen/pumi/controlplane/v1/controlplanev1connect"
	eventsv1 "getpumi.dev/pumi/api/gen/pumi/events/v1"
	harnessv1 "getpumi.dev/pumi/api/gen/pumi/harness/v1"
	runnerv1 "getpumi.dev/pumi/api/gen/pumi/runner/v1"
	"getpumi.dev/pumi/api/gen/pumi/runner/v1/runnerv1connect"
	"getpumi.dev/pumi/api/policy"
	"getpumi.dev/pumi/internal/controlplane"
	"getpumi.dev/pumi/internal/controlplane/embeddedlog"
	"getpumi.dev/pumi/internal/kernel"
	"getpumi.dev/pumi/internal/kernel/workflow"
	"getpumi.dev/pumi/internal/runner"
)

// The shown run of an archived plan (decisions 0181, 0182 and 0186). It is
// skipped unless PUMI_SHOWN_CLONE names a local clone of ScaleWholesale and
// PUMI_SHOWN_PLAN the path, in that clone, of a plan of a change in
// openspec/changes/archive. The Claude Code adapter is PUMI_SHOWN_ADAPTER,
// or adapter-claude-code on the PATH, and claude runs on its own sign-in
// (decision 0185).
//
// Each run makes a new git repository in a temporary directory that reads
// the objects of the clone, checks it out at the plan's base commit and adds
// the plan as .pumi/plan.md, the file that the spike workflow's task gives
// as the input. That working tree is the input tree. The repository has no
// remote, so nothing is pushed. pumi runs in embedded mode: the embedded
// log in that repository, the control plane, the kernel on the spike
// workflow, and the in-process runner with the adapter.
const (
	envClone   = "PUMI_SHOWN_CLONE"
	envPlan    = "PUMI_SHOWN_PLAN"
	envAdapter = "PUMI_SHOWN_ADAPTER"
	// envChild holds the run, as JSON, for the child process that the
	// killed run starts and kills.
	envChild = "PUMI_SHOWN_CHILD"
)

// planFile is where the shown run puts the plan in the input tree.
const planFile = ".pumi/plan.md"

// The limits of the shown run.
const (
	// runTimeout is how long a run to the final state may take.
	runTimeout = 30 * time.Minute
	// killAfter is how long the invocation runs before the test kills
	// pumi. The plans of the regime that suit the shown run take claude
	// about 20 seconds, so it is shorter.
	killAfter = 5 * time.Second
	// stopWait is how long the processes of the killed run have to leave
	// its workspace after the kill: the adapter stops claude when its
	// standard input closes (decision 0158).
	stopWait = 2 * time.Minute
	// afterExpiry is how long the restarted pumi runs after the killed
	// run's lease expired, in which a runner would claim it again.
	afterExpiry = 10 * time.Second
)

// shownPlan is an archived plan of ScaleWholesale: its id, the commit it
// started from, the paths it declares and the whole file.
type shownPlan struct {
	ID    string
	Base  string
	Paths []string
	Text  []byte
}

// commitForm is the shape of a full commit id.
var commitForm = regexp.MustCompile(`^[0-9a-f]{40}$`)

// parsePlan reads the front matter of a regime plan: plan, change,
// base_commit and the list paths. It refuses a plan without them.
func parsePlan(text []byte) (shownPlan, error) {
	s := string(text)
	if !strings.HasPrefix(s, "---\n") {
		return shownPlan{}, errors.New("the plan has no front matter")
	}
	front, _, ok := strings.Cut(s[len("---\n"):], "\n---\n")
	if !ok {
		return shownPlan{}, errors.New("the plan's front matter does not end")
	}
	p := shownPlan{Text: text}
	var plan, change string
	inPaths := false
	for line := range strings.SplitSeq(front, "\n") {
		if item, ok := strings.CutPrefix(line, "  - "); ok {
			if inPaths {
				p.Paths = append(p.Paths, strings.TrimSpace(item))
			}
			continue
		}
		key, value, _ := strings.Cut(line, ":")
		value = strings.TrimSpace(value)
		inPaths = key == "paths"
		switch key {
		case "plan":
			plan = value
		case "change":
			change = value
		case "base_commit":
			p.Base = value
		}
	}
	switch {
	case plan == "" || change == "":
		return shownPlan{}, errors.New("the plan's front matter has no plan or no change")
	case !commitForm.MatchString(p.Base):
		return shownPlan{}, fmt.Errorf("the plan's base_commit %q is not a full commit id", p.Base)
	case len(p.Paths) == 0:
		return shownPlan{}, errors.New("the plan declares no paths")
	}
	p.ID = change + "-" + strings.ToLower(plan)
	return p, nil
}

// shownRun is what one run of pumi needs. The killed run gives it to its
// child process as JSON.
type shownRun struct {
	// Repository is the run's git repository, which holds the log.
	Repository string
	// Tree is the input tree: the base commit's tree with the plan.
	Tree string
	// Plan is the plan's id and Paths the paths it declares.
	Plan  string
	Paths []string
	// Adapter is the absolute path of the Claude Code adapter.
	Adapter string
	// WorkDir is where the runner makes the workspaces.
	WorkDir string
}

// shownSetup returns the clone, the plan and the adapter that the
// environment names, and skips the test when it names no clone or plan.
func shownSetup(t *testing.T) (string, shownPlan, string) {
	t.Helper()
	clone, planPath := os.Getenv(envClone), os.Getenv(envPlan)
	if clone == "" || planPath == "" {
		t.Skipf("%s and %s do not name a ScaleWholesale clone and an archived plan", envClone, envPlan)
	}
	if !strings.HasPrefix(filepath.ToSlash(planPath), "openspec/changes/archive/") {
		t.Fatalf("%s is %q, which is not a plan in openspec/changes/archive", envPlan, planPath)
	}
	plan, err := parsePlan(readFile(t, clone, planPath))
	if err != nil {
		t.Fatalf("%s: %v", planPath, err)
	}
	adapter := os.Getenv(envAdapter)
	if adapter == "" {
		if adapter, err = exec.LookPath("adapter-claude-code"); err != nil {
			t.Fatalf("%s is not set and adapter-claude-code is not on the PATH: %v", envAdapter, err)
		}
	}
	if adapter, err = filepath.Abs(adapter); err != nil {
		t.Fatal(err)
	}
	return clone, plan, adapter
}

// gitOutput runs cmd in dir, as git does, and returns its standard output
// without the last newline.
func gitOutput(t *testing.T, dir string, cmd *exec.Cmd) string {
	t.Helper()
	var stdout bytes.Buffer
	cmd.Stdout = &stdout
	git(t, dir, cmd)
	return strings.TrimSuffix(stdout.String(), "\n")
}

// prepare makes the run's repository: a new repository that reads the
// clone's objects, checked out at the plan's base commit, with the plan
// added as planFile. It returns the run with its input tree.
func prepare(t *testing.T, clone string, plan shownPlan, adapter string) shownRun {
	t.Helper()
	ctx := t.Context()
	objects := gitOutput(t, clone, exec.CommandContext(ctx, "git", "rev-parse", "--path-format=absolute", "--git-common-dir"))
	dir := filepath.Join(t.TempDir(), "repository")
	if err := os.Mkdir(dir, 0o750); err != nil {
		t.Fatal(err)
	}
	git(t, dir, exec.CommandContext(ctx, "git", "init", "--quiet"))
	gitDir := filepath.Join(dir, ".git")
	writeFile(t, gitDir, "objects/info/alternates", filepath.Join(objects, "objects")+"\n")
	writeFile(t, gitDir, "HEAD", plan.Base+"\n")
	git(t, dir, exec.CommandContext(ctx, "git", "reset", "--quiet", "--hard"))
	if _, err := os.Lstat(filepath.Join(dir, filepath.FromSlash(planFile))); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the base commit %s already has %s", plan.Base, planFile)
	}
	writeFile(t, dir, planFile, string(plan.Text))
	git(t, dir, exec.CommandContext(ctx, "git", "add", "--force", "--", planFile))
	tree := gitOutput(t, dir, exec.CommandContext(ctx, "git", "write-tree"))
	work := filepath.Join(t.TempDir(), "work")
	if err := os.Mkdir(work, 0o750); err != nil {
		t.Fatal(err)
	}
	t.Logf("plan %s, base commit %s, input tree %s", plan.ID, plan.Base, tree)
	return shownRun{Repository: dir, Tree: tree, Plan: plan.ID, Paths: plan.Paths, Adapter: adapter, WorkDir: work}
}

// writeFile writes content to the file name, a slash-separated path, in
// dir, through an os.Root, which gosec does not report (G304).
func writeFile(t *testing.T, dir, name, content string) {
	t.Helper()
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = root.Close() }()
	if err := root.MkdirAll(filepath.Dir(filepath.FromSlash(name)), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := root.WriteFile(filepath.FromSlash(name), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

// shownBase is the part of each invocation document that the wiring of the
// shown run sets: the adapter, the input tree, the capabilities and the
// budget. The run may read, edit the plan's paths and run Go's tests;
// claude reaches the model on its own sign-in, so the network access names
// no host for the web tools.
func shownBase(run shownRun) *runnerv1.Invocation {
	return &runnerv1.Invocation{
		Component: &harnessv1.Component{Name: "adapter-claude-code", Version: "0"},
		Input:     &harnessv1.Input{Repo: "scale-wholesale", Tree: run.Tree},
		Capabilities: &harnessv1.Capabilities{
			WritePaths: run.Paths,
			Commands:   []string{"go -C tools/regime test", "go test"},
			Network:    &harnessv1.Capabilities_Network{Access: harnessv1.Capabilities_Network_ACCESS_NONE},
			Tools: []harnessv1.Capabilities_ToolKind{
				harnessv1.Capabilities_TOOL_KIND_READ,
				harnessv1.Capabilities_TOOL_KIND_EDIT,
				harnessv1.Capabilities_TOOL_KIND_RUN_COMMANDS,
			},
		},
		Budget: &harnessv1.Usage{CostMicrodollars: 5_000_000, Seconds: 20 * 60},
	}
}

// embedded runs pumi in embedded mode on the run until the plan is final
// or ctx is done, and returns the kernel's error: nil when the plan is
// final. It adds the plan as a person when the log does not have it
// (decision 0198). idle, when set, is also called when the kernel waits.
func embedded(ctx context.Context, run shownRun, idle func(head uint64)) error {
	wf, err := workflow.Load(os.DirFS(filepath.Join(workflowsDir, "spike")), "workflow.txtpb")
	if err != nil {
		return err
	}
	log, err := embeddedlog.Open(ctx, run.Repository)
	if err != nil {
		return err
	}
	defer func() { _ = log.Close() }()
	svc, err := controlplane.New(log, controlplane.Config{
		HardCaps: policy.DefaultHardCaps(),
		Project:  controlplane.Project{Name: "scale-wholesale"},
	})
	if err != nil {
		return err
	}
	kernelConn, err := svc.Client(controlplane.Identity{Caller: controlplane.CallerKernel, Principal: "kernel", Tenant: embeddedlog.Tenant})
	if err != nil {
		return err
	}
	personConn, err := svc.Client(controlplane.Identity{
		Caller: controlplane.CallerPerson, Principal: personName, Roles: []string{"maintainer"}, Tenant: embeddedlog.Tenant,
	})
	if err != nil {
		return err
	}
	person := controlplanev1connect.NewControlPlaneServiceClient(personConn)
	if err := addShownPlan(ctx, person, run.Plan); err != nil {
		return err
	}
	runnerConn, err := svc.Client(controlplane.Identity{
		Caller: controlplane.CallerRunner, Principal: runnerName, Roles: []string{controlplane.RoleAgent}, Tenant: embeddedlog.Tenant,
	})
	if err != nil {
		return err
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	ran := make(chan error, 1)
	go func() {
		ran <- runner.Run(ctx, runner.Config{
			Service:    runnerv1connect.NewRunnerServiceClient(runnerConn),
			Adapter:    run.Adapter,
			Repository: run.Repository,
			WorkDir:    run.WorkDir,
		})
	}()
	final := false
	err = kernel.Run(ctx, kernel.Config{
		Client:   controlplanev1connect.NewControlPlaneServiceClient(kernelConn),
		Workflow: wf, Base: shownBase(run),
		Idle: func(head uint64) {
			if idle != nil {
				idle(head)
			}
			if isFinal(ctx, person, wf, run.Plan) {
				final = true
				cancel()
			}
		},
	})
	if final {
		err = nil
	}
	cancel()
	if rerr := <-ran; rerr != nil && !errors.Is(rerr, context.Canceled) && err == nil {
		err = fmt.Errorf("the runner stopped with %w", rerr)
	}
	return err
}

// addShownPlan appends the plan.added of plan as the person, unless the log
// has it.
func addShownPlan(ctx context.Context, person controlplanev1connect.ControlPlaneServiceClient, plan string) error {
	res, err := person.Query(ctx, &controlplanev1.QueryRequest{Filter: &controlplanev1.Filter{Subject: plan, Types: []string{events.TypePlanAdded}}})
	if err != nil || len(res.GetEvents()) > 0 {
		return err
	}
	e, err := events.New(&eventsv1.PlanAdded{Plan: plan, Track: caseTrack},
		events.Attributes{ID: "add-" + plan, Source: personSource, Subject: plan, Tenant: embeddedlog.Tenant})
	if err != nil {
		return err
	}
	_, err = person.Append(ctx, &controlplanev1.AppendRequest{Events: []*cloudeventsv1.CloudEvent{e}})
	return err
}

// isFinal reports whether the last transition.fired of plan in the log
// enters a final state of wf.
func isFinal(ctx context.Context, reader controlplanev1connect.ControlPlaneServiceClient, wf *workflow.Workflow, plan string) bool {
	res, err := reader.Query(ctx, &controlplanev1.QueryRequest{
		Filter: &controlplanev1.Filter{Subject: plan, Types: []string{events.TypeTransitionFired}},
	})
	if err != nil || len(res.GetEvents()) == 0 {
		return false
	}
	var tf eventsv1.TransitionFired
	if err := res.GetEvents()[len(res.GetEvents())-1].GetEvent().GetProtoData().UnmarshalTo(&tf); err != nil {
		return false
	}
	for _, s := range wf.Definition().GetStates() {
		if s.GetName() == tf.GetToState() {
			return s.GetFinal() != nil
		}
	}
	return false
}

// readLog returns the events of the run's log, read from its file, which
// the test may read while another process writes it.
func readLog(t *testing.T, run shownRun) []*cloudeventsv1.CloudEvent {
	t.Helper()
	logPath, _ := logFiles(run.Repository)
	root, err := os.OpenRoot(filepath.Dir(logPath))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = root.Close() }()
	b, err := root.ReadFile(filepath.Base(logPath))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	var out []*cloudeventsv1.CloudEvent
	s := bufio.NewScanner(bytes.NewReader(b))
	s.Buffer(nil, 1<<24)
	for s.Scan() {
		e := &cloudeventsv1.CloudEvent{}
		if err := protojson.Unmarshal(s.Bytes(), e); err != nil {
			// The last line may still be written.
			break
		}
		out = append(out, e)
	}
	return out
}

// showLog writes the run's log to the test's output, one event per line.
func showLog(t *testing.T, name string, run shownRun) {
	t.Helper()
	logPath, _ := logFiles(run.Repository)
	t.Logf("the log of the %s:\n%s", name, readFile(t, filepath.Dir(logPath), filepath.Base(logPath)))
}

// firedIn returns the data of each transition.fired in evs.
func firedIn(t *testing.T, evs []*cloudeventsv1.CloudEvent) []*eventsv1.TransitionFired {
	t.Helper()
	var out []*eventsv1.TransitionFired
	for _, e := range evs {
		if e.GetType() == events.TypeTransitionFired {
			out = append(out, dataOf(t, e, &eventsv1.TransitionFired{}))
		}
	}
	return out
}

// TestShownRunReachesTheFinalState runs the plan with Claude Code to the
// spike workflow's final state done (decision 0186). It shows the log and
// the change, the difference between the input tree and the result's tree.
func TestShownRunReachesTheFinalState(t *testing.T) {
	clone, plan, adapter := shownSetup(t)
	run := prepare(t, clone, plan, adapter)
	ctx, cancel := context.WithTimeout(t.Context(), runTimeout)
	defer cancel()
	if err := embedded(ctx, run, nil); err != nil {
		t.Fatalf("pumi stopped with %v before the plan was final", err)
	}
	evs := readLog(t, run)
	showLog(t, "run to the final state", run)
	fired := firedIn(t, evs)
	if len(fired) == 0 || fired[len(fired)-1].GetToState() != "done" {
		t.Fatalf("the plan's last transition is %v, want one to done", fired)
	}
	var result *eventsv1.InvocationResult
	for _, e := range evs {
		if e.GetType() == events.TypeInvocationResult {
			result = dataOf(t, e, &eventsv1.InvocationResult{})
		}
	}
	if result == nil {
		t.Fatal("the log has no invocation.result")
	}
	if result.GetTree() == "" {
		t.Logf("the result has no tree, so the agent changed nothing")
		return
	}
	diff := exec.CommandContext(t.Context(), "git", "diff-tree", "--patch", "--stdin")
	diff.Stdin = strings.NewReader(run.Tree + " " + result.GetTree() + "\n")
	t.Logf("the change from the input tree to the result's tree:\n%s", gitOutput(t, run.Repository, diff))
}

// TestShownRunKilledAndStartedAgain starts pumi as a child process, kills
// it while the adapter runs, and starts pumi again: the invocation is in
// doubt, so the plan escalates and the invocation is not run again after
// its lease expires (decision 0182). It shows the log.
func TestShownRunKilledAndStartedAgain(t *testing.T) {
	clone, plan, adapter := shownSetup(t)
	run := prepare(t, clone, plan, adapter)
	config, err := json.Marshal(run)
	if err != nil {
		t.Fatal(err)
	}
	child := exec.CommandContext(t.Context(), "/proc/self/exe", "-test.run=^TestShownPumi$", "-test.v")
	child.Env = append(os.Environ(), envChild+"="+string(config))
	var out bytes.Buffer
	child.Stdout, child.Stderr = &out, &out
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	waitFor(t, run, runTimeout, events.TypeInvocationStarted)
	time.Sleep(killAfter)
	if has(readLog(t, run), events.TypeInvocationResult) {
		t.Fatalf("the invocation ended within %s, before the kill", killAfter)
	}
	running := inDir(run.WorkDir)
	if len(running) == 0 {
		t.Fatal("no process runs in the workspace before the kill")
	}
	t.Logf("before the kill, these processes run in the workspace: %s", strings.Join(running, ", "))
	if err := child.Process.Signal(syscall.SIGKILL); err != nil {
		t.Fatal(err)
	}
	_ = child.Wait()
	t.Logf("pumi was killed after %s of the invocation; its output:\n%s", killAfter, out.String())
	leftInWorkspace(t, run.WorkDir)
	before := len(readLog(t, run))

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- embedded(ctx, run, nil) }()
	waitFor(t, run, time.Minute, events.TypeEscalationRaised)
	// The killed run's lease expires after the restart. The runner must
	// not claim the invocation again (decision 0182).
	time.Sleep(controlplane.DefaultLeaseTimeout + afterExpiry)
	cancel()
	if err := <-done; err != nil && !errors.Is(err, context.Canceled) {
		t.Fatalf("pumi stopped with %v", err)
	}
	evs := readLog(t, run)
	showLog(t, "killed run", run)
	t.Logf("the kill left %d events; the restart appended %d", before, len(evs)-before)
	fired := firedIn(t, evs)
	last := fired[len(fired)-1]
	if last.GetTrigger() != kernel.InDoubt || last.GetToState() != "implementing-escalated" {
		t.Errorf("the last transition is %v, want the in-doubt one to implementing-escalated", last)
	}
	if has(evs, events.TypeInvocationResult) {
		t.Errorf("the log has an invocation.result, but the invocation was killed")
	}
	if !has(evs, events.TypeLeaseExpired) {
		t.Errorf("the killed run's lease did not expire after the restart")
	}
	granted := 0
	for _, e := range evs {
		if e.GetType() == events.TypeLeaseGranted {
			granted++
		}
	}
	if granted != 1 {
		t.Errorf("the log has %d lease.granted events, want 1: the invocation in doubt is not run again", granted)
	}
}

// has reports whether evs has an event of type typ.
func has(evs []*cloudeventsv1.CloudEvent, typ string) bool {
	for _, e := range evs {
		if e.GetType() == typ {
			return true
		}
	}
	return false
}

// waitFor waits until the run's log has an event of type typ.
func waitFor(t *testing.T, run shownRun, limit time.Duration, typ string) {
	t.Helper()
	deadline := time.Now().Add(limit)
	for time.Now().Before(deadline) {
		if has(readLog(t, run), typ) {
			return
		}
		time.Sleep(time.Second)
	}
	t.Fatalf("the log has no %s after %s", typ, limit)
}

// leftInWorkspace waits until no process has its working directory in
// workDir, where the killed runner started the adapter and claude, and
// fails the test if one is left after stopWait. A process that ended and
// was not reaped has no working directory to read.
func leftInWorkspace(t *testing.T, workDir string) {
	t.Helper()
	deadline := time.Now().Add(stopWait)
	for {
		left := inDir(workDir)
		if len(left) == 0 {
			t.Logf("no process is left in the workspace")
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("processes %v are still in the workspace %s after %s", left, workDir, stopWait)
		}
		time.Sleep(time.Second)
	}
}

// inDir returns the ids and command names of the processes whose working
// directory is in dir.
func inDir(dir string) []string {
	procs, err := filepath.Glob("/proc/[0-9]*/cwd")
	if err != nil {
		return nil
	}
	var out []string
	for _, p := range procs {
		cwd, err := os.Readlink(p)
		if err == nil && (cwd == dir || strings.HasPrefix(cwd, dir+string(filepath.Separator))) {
			pid := filepath.Base(filepath.Dir(p))
			out = append(out, pid+" ("+command(pid)+")")
		}
	}
	return out
}

// command returns the command name of the process pid, read through an
// os.Root, which gosec does not report (G304).
func command(pid string) string {
	root, err := os.OpenRoot("/proc")
	if err != nil {
		return "?"
	}
	defer func() { _ = root.Close() }()
	b, err := root.ReadFile(pid + "/comm")
	if err != nil {
		return "?"
	}
	return strings.TrimSpace(string(b))
}

// TestShownPumi is the child process of the killed run: it runs pumi in
// embedded mode on the run in PUMI_SHOWN_CHILD until it is killed. It is
// skipped unless that variable is set.
func TestShownPumi(t *testing.T) {
	config := os.Getenv(envChild)
	if config == "" {
		t.Skipf("%s is not set; the killed shown run starts this test as its child", envChild)
	}
	var run shownRun
	if err := json.Unmarshal([]byte(config), &run); err != nil {
		t.Fatal(err)
	}
	if err := embedded(t.Context(), run, nil); err != nil {
		t.Fatal(err)
	}
}
