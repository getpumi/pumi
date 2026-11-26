// SPDX-License-Identifier: Apache-2.0
// SPDX-FileCopyrightText: The Pumi Authors

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"getpumi.dev/pumi/api/events"
	cloudeventsv1 "getpumi.dev/pumi/api/gen/io/cloudevents/v1"
	configv1 "getpumi.dev/pumi/api/gen/pumi/config/v1"
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
)

// The golden event-sequence cases (decision 0183). Each case is a directory
// testdata/golden/<workflow>/<case> that holds log.jsonl, the log the case
// gives, and sources.md, the regime requirements and tests it comes from,
// and may hold settings.json, the settings of the control plane that the
// case runs with. <workflow> names the directory of internal/kernel/testdata
// that holds the workflow the case runs on.
//
// The test plays a case: it starts the embedded control plane and the
// kernel on a new log and, each time the kernel waits, does what the next
// line of the case asks of someone other than the kernel. A lease.granted
// line is a scripted runner's Claim, an invocation.result line its Report,
// with the outcome of the invocation.finished line after it, a
// lease.expired line a move of the control plane's time past the lease
// timeout, and a line
// whose source starts with /person a person's Append, such as the
// plan.added of each plan (decision 0198); a decision.made line whose cause
// is the person's line after it is the control plane's decision of that
// line's permission, which the person's Append writes with it (decision
// 0156). When the kernel
// waits and the next line is the kernel's own, or a decision that the
// kernel asks for, only a restart lets the kernel go on, so the test stops
// pumi and starts it again (decision 0182). At the end the log must be the
// case: event ids, prevhash and the log_head of a decision are compared by
// shape, and a cause, the decision of a transition and the escalation of
// an answer by the line of the event they name.
//
// Each case also runs with a restart at every position: at each position
// where a write to the log ended, the test starts pumi on the first lines
// of the log, sends the events of the lines after it that the kernel waits
// for, starts the kernel and plays the rest. Each run must give the case.

// update rewrites each case's log.jsonl from a run of its seed.
var update = flag.Bool("update", false, "rewrite each golden case's log.jsonl from a run of its seed")

// goldenDir holds the cases, and workflowsDir the workflows they run on.
const (
	goldenDir    = "testdata/golden"
	workflowsDir = "../../internal/kernel/testdata"
)

// The identities of the case's callers, and the plan and track of the
// cases that run one plan.
const (
	casePlan      = "plan-a"
	caseTrack     = "main"
	runnerName    = "runner-1"
	personName    = "person-1"
	personSource  = "/person/" + personName
	idleTimeout   = 10 * time.Second
	claimWaitTime = 5 * time.Second
)

// base is the part of each invocation document that the wiring of the
// cases sets (decision 0165).
func base(s caseSettings) *runnerv1.Invocation {
	return &runnerv1.Invocation{
		Component: &harnessv1.Component{Name: "scripted", Version: "1"},
		Input:     &harnessv1.Input{Repo: "case", Tree: "tree-0"},
		Budget:    s.Budget,
	}
}

// caseSettings are the settings of the control plane that a case runs
// with, read from its settings.json.
type caseSettings struct {
	// Parallelism is the parallelism setting (decision 0189); 0 counts as
	// 1.
	Parallelism int `json:"parallelism"`
	// MaxAttempts is the profile's maxAttempts, by gate or by "in-doubt"
	// (decision 0201).
	MaxAttempts map[string]int64 `json:"maxAttempts"`
	// Budget is the budget of each invocation document, which is what a
	// lost run counts as having spent (decision 0191).
	Budget *harnessv1.Usage `json:"budget"`
	// ActionHappened is the fake answer of the adapter of an external
	// action in doubt to whether the action happened (decision 0202).
	ActionHappened bool `json:"actionHappened"`
	// ArchiveStep is the project's archive step, by its name in
	// pumi.config.v1.Project.ArchiveStep, or empty for after implementing
	// (decisions 0278 and 0296).
	ArchiveStep string `json:"archiveStep"`
}

// archiveStep returns the project's archive step of the settings s.
func (s caseSettings) archiveStep(t *testing.T) configv1.Project_ArchiveStep {
	t.Helper()
	if s.ArchiveStep == "" {
		return configv1.Project_ARCHIVE_STEP_UNSPECIFIED
	}
	v, ok := configv1.Project_ArchiveStep_value[s.ArchiveStep]
	if !ok {
		t.Fatalf("settings.json: the archive step %q is unknown", s.ArchiveStep)
	}
	return configv1.Project_ArchiveStep(v)
}

// readSettings returns the settings of the case in dir, from its
// settings.json, or none when it has no such file.
func readSettings(t *testing.T, dir string) caseSettings {
	t.Helper()
	var s caseSettings
	if _, err := os.Stat(filepath.Join(dir, "settings.json")); errors.Is(err, os.ErrNotExist) {
		return s
	}
	dec := json.NewDecoder(bytes.NewReader(readFile(t, dir, "settings.json")))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&s); err != nil {
		t.Fatalf("%s/settings.json: %v", dir, err)
	}
	return s
}

// planeConfig is the wiring of the control plane: the core invariants and
// policies only, the case's settings, the default hard caps and long
// leases, so that a lease does not expire while a case runs.
func planeConfig(s caseSettings) controlplane.Config {
	return controlplane.Config{
		Settings:     controlplane.Settings{Parallelism: s.Parallelism, MaxAttempts: s.MaxAttempts},
		HardCaps:     policy.DefaultHardCaps(),
		Project:      controlplane.Project{Name: "case"},
		LeaseTimeout: time.Hour,
		ClaimWait:    claimWaitTime,
	}
}

// pumi is one run of the embedded control plane and the kernel on a clone.
type pumi struct {
	t        *testing.T
	clone    string
	wf       *workflow.Workflow
	settings caseSettings
	// clock is the control plane's time, which moves only when a case
	// lets a lease expire.
	clock *fakeClock

	log          *embeddedlog.Log
	reader       controlplanev1connect.ControlPlaneServiceClient
	kernelClient controlplanev1connect.ControlPlaneServiceClient
	runner       runnerv1connect.RunnerServiceClient
	person       controlplanev1connect.ControlPlaneServiceClient

	stopKernel context.CancelFunc
	done       chan error
	idle       chan uint64
	// mu guards writes, which the kernel's Wrote adds to.
	mu sync.Mutex
	// writes are the positions at which a write to the log ended.
	writes []uint64
	// claims are the Claim responses by invocation.
	claims map[string]*runnerv1.ClaimResponse
	// trunk, when set, is the kernel's Trunk (decision 0209).
	trunk func(context.Context) (string, error)
}

// newClone creates a git repository with one commit and returns its
// working tree. The commands have constant arguments, because gosec
// reports a command built from variables (G204).
func newClone(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "clone")
	if err := os.Mkdir(dir, 0o750); err != nil {
		t.Fatal(err)
	}
	git(t, dir, exec.CommandContext(t.Context(), "git", "init", "--quiet", "--initial-branch=main"))
	git(t, dir, exec.CommandContext(t.Context(), "git", "commit", "--quiet", "--allow-empty", "--message=first"))
	return dir
}

// git runs cmd in dir, without the user's and the system's configuration.
func git(t *testing.T, dir string, cmd *exec.Cmd) {
	t.Helper()
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_CONFIG_GLOBAL="+os.DevNull, "GIT_CONFIG_NOSYSTEM=1",
		"GIT_AUTHOR_NAME=Author", "GIT_AUTHOR_EMAIL=author@example.com",
		"GIT_COMMITTER_NAME=Committer", "GIT_COMMITTER_EMAIL=committer@example.com")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("%s: %v\n%s", cmd, err, stderr.String())
	}
}

// logFiles returns the paths of log.jsonl and head of the clone's log
// (decision 0157).
func logFiles(clone string) (string, string) {
	dir := filepath.Join(clone, ".git", "pumi")
	return filepath.Join(dir, "log.jsonl"), filepath.Join(dir, "head")
}

// newPumi returns a run with the settings s on a new clone whose log holds
// the first n lines of lines, the lines of a log.jsonl, committed by its
// head file.
func newPumi(t *testing.T, wf *workflow.Workflow, s caseSettings, lines [][]byte, n uint64) *pumi {
	t.Helper()
	p := &pumi{t: t, clone: newClone(t), wf: wf, settings: s, clock: newFakeClock(), claims: map[string]*runnerv1.ClaimResponse{}}
	if n > 0 {
		logPath, headPath := logFiles(p.clone)
		if err := os.MkdirAll(filepath.Dir(logPath), 0o750); err != nil {
			t.Fatal(err)
		}
		var buf bytes.Buffer
		for _, line := range lines[:n] {
			buf.Write(line)
			buf.WriteByte('\n')
		}
		if err := os.WriteFile(logPath, buf.Bytes(), 0o600); err != nil {
			t.Fatal(err)
		}
		head, err := json.Marshal(map[string]any{"position": n, "hash": events.HashOf(lines[n-1])})
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(headPath, head, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(p.stop)
	return p
}

// startPlane opens the log and starts the control plane with the clients
// of the scripted runner and the person.
func (p *pumi) startPlane() {
	p.t.Helper()
	log, err := embeddedlog.Open(p.t.Context(), p.clone)
	if err != nil {
		p.t.Fatal(err)
	}
	p.log = log
	cfg := planeConfig(p.settings)
	cfg.Clock = p.clock
	svc, err := controlplane.New(log, cfg)
	if err != nil {
		p.t.Fatal(err)
	}
	dial := func(id controlplane.Identity) controlplanev1connect.ControlPlaneServiceClient {
		c, err := svc.Client(id)
		if err != nil {
			p.t.Fatal(err)
		}
		return controlplanev1connect.NewControlPlaneServiceClient(c)
	}
	runner, err := svc.Client(controlplane.Identity{
		Caller: controlplane.CallerRunner, Principal: runnerName, Roles: []string{controlplane.RoleAgent, controlplane.RoleGate},
		Tenant: embeddedlog.Tenant,
	})
	if err != nil {
		p.t.Fatal(err)
	}
	p.runner = runnerv1connect.NewRunnerServiceClient(runner)
	p.person = dial(controlplane.Identity{
		Caller: controlplane.CallerPerson, Principal: personName, Roles: []string{"maintainer"}, Tenant: embeddedlog.Tenant,
	})
	p.reader = p.person
	p.kernelClient = dial(controlplane.Identity{Caller: controlplane.CallerKernel, Principal: "kernel", Tenant: embeddedlog.Tenant})
}

// startKernel starts the kernel, with the parallelism setting of the
// control plane, as the wiring of pumi gives it.
func (p *pumi) startKernel() {
	ctx, cancel := context.WithCancel(context.Background())
	p.stopKernel = cancel
	p.done = make(chan error, 1)
	p.idle = make(chan uint64, 64)
	cfg := kernel.Config{
		Client: p.kernelClient, Workflow: p.wf, Base: base(p.settings),
		Parallelism: planeConfig(p.settings).Settings.Parallelism,
		Resolve: func(context.Context, *runnerv1.Invocation) (bool, error) {
			return p.settings.ActionHappened, nil
		},
		Idle: func(head uint64) {
			select {
			case p.idle <- head:
			default:
			}
		},
		Wrote:   func(position uint64) { p.wrote(position) },
		Trunk:   p.trunk,
		Project: kernel.Project{ArchiveStep: p.settings.archiveStep(p.t)},
	}
	go func() { p.done <- kernel.Run(ctx, cfg) }()
}

// wrote records that a write to the log ended at position.
func (p *pumi) wrote(position uint64) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.writes = append(p.writes, position)
}

// stop stops the kernel and closes the log, if they run.
func (p *pumi) stop() {
	if p.stopKernel != nil {
		p.stopKernel()
		if err := <-p.done; err != nil && !errors.Is(err, context.Canceled) {
			p.t.Errorf("the kernel stopped with %v", err)
		}
		p.stopKernel = nil
	}
	if p.log != nil {
		if err := p.log.Close(); err != nil {
			p.t.Errorf("closing the log: %v", err)
		}
		p.log = nil
	}
}

// restart stops pumi and starts it again (decision 0182).
func (p *pumi) restart() {
	p.t.Helper()
	p.stop()
	p.startPlane()
	p.startKernel()
}

// head returns the head of the log.
func (p *pumi) head() uint64 {
	p.t.Helper()
	res, err := p.reader.Query(p.t.Context(), &controlplanev1.QueryRequest{After: 0, Limit: 1})
	if err != nil {
		p.t.Fatal(err)
	}
	return res.GetHead()
}

// events returns every event of the log, in order.
func (p *pumi) events() []*cloudeventsv1.CloudEvent {
	p.t.Helper()
	res, err := p.reader.Query(p.t.Context(), &controlplanev1.QueryRequest{})
	if err != nil {
		p.t.Fatal(err)
	}
	out := make([]*cloudeventsv1.CloudEvent, 0, len(res.GetEvents()))
	for _, e := range res.GetEvents() {
		out = append(out, e.GetEvent())
	}
	return out
}

// waitIdle waits until the kernel has handled every event of the log and
// waits for more, or has stopped because the plan finished.
func (p *pumi) waitIdle() {
	p.t.Helper()
	timeout := time.After(idleTimeout)
	for {
		select {
		case h := <-p.idle:
			if h == p.head() {
				return
			}
		case err := <-p.done:
			p.done <- err
			if err != nil {
				p.t.Fatalf("the kernel stopped with %v", err)
			}
			return
		case <-timeout:
			p.t.Fatalf("the kernel did not wait for an event within %s; the log has %d events", idleTimeout, p.head())
		}
	}
}

// claim claims the next invocation as the scripted runner. The kernel
// waits, so an invocation that is ready is claimed at once; Claim would
// wait for one on the control plane's clock, which does not move, so it
// fails after idleTimeout instead.
func (p *pumi) claim() {
	p.t.Helper()
	ctx, cancel := context.WithTimeout(p.t.Context(), idleTimeout)
	defer cancel()
	res, err := p.runner.Claim(ctx, &runnerv1.ClaimRequest{ProtocolVersions: []*runnerv1.ProtocolVersion{{Major: 1}}})
	if err != nil || res.GetInvocation() == nil {
		p.t.Fatalf("the scripted runner's Claim = %v, %v; want an invocation", res, err)
	}
	p.claims[res.GetInvocation().GetInvocation()] = res
	p.wrote(p.head())
}

// report reports the result of its invocation with the outcome and the
// events evs, under the lease number of the lease the log granted last
// for it.
func (p *pumi) report(result *eventsv1.InvocationResult, outcome eventsv1.InvocationFinished_Outcome, number int64, evs ...*cloudeventsv1.CloudEvent) {
	p.t.Helper()
	res, err := p.runner.Report(p.t.Context(), &runnerv1.ReportRequest{
		Invocation: result.GetInvocation(), LeaseNumber: number, Result: result, Outcome: outcome, Events: evs,
	})
	if err != nil {
		p.t.Fatalf("the scripted runner's Report of %s: %v", result.GetInvocation(), err)
	}
	p.wrote(res.GetPositions()[len(res.GetPositions())-1])
}

// expire moves the control plane's time past the lease timeout, so that
// every current lease expires.
func (p *pumi) expire() {
	p.t.Helper()
	p.clock.Advance(planeConfig(p.settings).LeaseTimeout + time.Second)
	p.wrote(p.head())
}

// appendAsPerson appends e as the person.
func (p *pumi) appendAsPerson(e *cloudeventsv1.CloudEvent) {
	p.t.Helper()
	res, err := p.person.Append(p.t.Context(), &controlplanev1.AppendRequest{Events: []*cloudeventsv1.CloudEvent{e}})
	if err != nil {
		p.t.Fatalf("the person's append of %s: %v", e.GetType(), err)
	}
	p.wrote(res.GetPositions()[len(res.GetPositions())-1])
}
