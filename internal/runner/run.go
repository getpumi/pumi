// SPDX-License-Identifier: Apache-2.0
// SPDX-FileCopyrightText: The Pumi Authors

package runner

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"getpumi.dev/pumi/api"
	eventsv1 "getpumi.dev/pumi/api/gen/pumi/events/v1"
	runnerv1 "getpumi.dev/pumi/api/gen/pumi/runner/v1"
	"getpumi.dev/pumi/api/gen/pumi/runner/v1/runnerv1connect"
	"google.golang.org/protobuf/reflect/protoreflect"
)

// The defaults of Config.
const (
	// DefaultGrace is how long an adapter has to stop after the runner
	// closes its standard input, before the runner kills it (decision
	// 0035).
	DefaultGrace = 30 * time.Second
	// DefaultLogInterval is how often the runner sends a batch of the
	// adapter's log output (decision 0163).
	DefaultLogInterval = time.Second
)

// runnerPrefix starts each line that the runner adds to an invocation's
// log, such as the reason it failed the invocation.
const runnerPrefix = "pumi runner: "

// protocolVersions are the versions of the runner protocol that the runner
// supports.
var protocolVersions = []*runnerv1.ProtocolVersion{{Major: 1, Minor: 0}}

// Config is what the wiring gives the in-process runner, which runs one
// harness adapter (decision 0180).
type Config struct {
	// Service is the client of RunnerService, through which the runner
	// claims invocations and reports their results (decision 0152).
	Service runnerv1connect.RunnerServiceClient
	// Adapter is the absolute path of the harness adapter's executable,
	// which the runner starts once for each invocation (decision 0158).
	Adapter string
	// Repository is a directory in the git repository that holds the
	// invocations' input trees and receives their resulting trees.
	Repository string
	// WorkDir is the directory in which the runner makes a new directory
	// for each invocation, with its workspace, and removes it after the
	// invocation.
	WorkDir string
	// Grace is how long an adapter has to stop after the runner closes its
	// standard input; DefaultGrace when zero.
	Grace time.Duration
	// LogInterval is how often the runner sends a batch of the adapter's
	// standard error; DefaultLogInterval when zero.
	LogInterval time.Duration
	// Blobs is the blob store of the runner's tenant, to which the runner
	// uploads the output files that the adapter reports (decisions 0033
	// and 0160).
	Blobs Blobs
	// Output, when set, also receives each invocation's log as it is
	// written, each line after the invocation's id and ": ", so that pumi
	// run prints it (getpumi/pumi#88).
	Output io.Writer
}

// Blobs is the blob store as the runner sees it: bound to the runner's
// tenant, which the runner never names (decision 0039).
type Blobs interface {
	// Put stores the content read from content and returns its hash,
	// "sha256:" and the hexadecimal SHA-256.
	Put(ctx context.Context, content io.Reader) (string, error)
}

// check refuses a configuration that lacks a value the runner needs, and
// sets the defaults.
func (c *Config) check() error {
	switch {
	case c.Service == nil:
		return errors.New("runner: no RunnerService client")
	case !filepath.IsAbs(c.Adapter):
		return fmt.Errorf("runner: the adapter %q is not an absolute path", c.Adapter)
	case c.Repository == "":
		return errors.New("runner: no repository")
	case c.WorkDir == "":
		return errors.New("runner: no work directory")
	case c.Blobs == nil:
		return errors.New("runner: no blob store")
	}
	if c.Grace <= 0 {
		c.Grace = DefaultGrace
	}
	if c.LogInterval <= 0 {
		c.LogInterval = DefaultLogInterval
	}
	return nil
}

// Run claims and runs invocations, one at a time, until ctx is done or a
// call of RunnerService fails, and returns the error that ended it.
func Run(ctx context.Context, cfg Config) error {
	for ctx.Err() == nil {
		if _, err := RunOne(ctx, cfg); err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return err
		}
	}
	return ctx.Err()
}

// RunOne claims an invocation through RunnerService, runs it and reports
// its result. It reports whether it claimed one: Claim is a long poll,
// which ends with none when no invocation was ready.
//
// The runner checks the input tree out into a new workspace, starts the
// adapter there with the harness invocation as one line on its standard
// input, sends heartbeats while it runs and batches of its standard error
// as the log, and reads its result from its standard output (decision
// 0158). It parses the structured output strictly into the message that
// the document names, computes the resulting tree from the workspace and
// fails the invocation when a file was written outside the allowed paths,
// before the result is appended (decisions 0159, 0160 and 0162). It then
// uploads each output file that the adapter reports to the blob store and
// records its path and hash in the result; a path outside the workspace
// fails the invocation (decision 0160).
//
// When a heartbeat carries a cancel request, the runner closes the
// adapter's standard input, kills it after the grace period and reports
// the outcome cancelled (decision 0035). When the wall time reaches the
// budget's seconds, it stops the adapter the same way and reports the
// outcome cap reached, with the measured seconds and, when the adapter
// reported none, the tokens and cost of the budget (decisions 0193 and
// 0204). When ctx is done, or a call of RunnerService fails while the
// adapter runs, it stops the adapter the same way, reports nothing and
// returns the error: an invocation with no result is in doubt (decisions
// 0182 and 0199). A call that cannot reach the control plane ends the
// invocation only once the lease would have ended, so the wall-time limit
// holds meanwhile (decision 0205).
func RunOne(ctx context.Context, cfg Config) (bool, error) {
	if err := cfg.check(); err != nil {
		return false, err
	}
	repo, err := openRepository(ctx, cfg.Repository)
	if err != nil {
		return false, err
	}
	// The lease is counted from before the claim, so that the runner never
	// takes it to end later than the control plane does (decision 0205).
	claimed := time.Now()
	claim, err := cfg.Service.Claim(ctx, &runnerv1.ClaimRequest{ProtocolVersions: protocolVersions})
	if err != nil {
		return false, fmt.Errorf("runner: the claim: %w", err)
	}
	if claim.GetInvocation() == nil {
		return false, nil
	}
	log := &logBuffer{}
	if cfg.Output != nil {
		log.out = &lineWriter{out: cfg.Output, prefix: claim.GetInvocation().GetInvocation() + ": "}
		defer log.flush()
	}
	r := &run{cfg: cfg, repo: repo, claim: claim, claimed: claimed, doc: claim.GetInvocation(), log: log}
	return true, r.do(ctx)
}

// run is one claimed invocation.
type run struct {
	cfg   Config
	repo  repository
	claim *runnerv1.ClaimResponse
	// claimed is when the runner asked for the claim.
	claimed time.Time
	doc     *runnerv1.Invocation
	log     *logBuffer
}

// do runs the invocation in a new directory, which it removes after, and
// reports its result.
func (r *run) do(ctx context.Context) error {
	dir, err := os.MkdirTemp(r.cfg.WorkDir, "invocation-")
	if err != nil {
		return fmt.Errorf("runner: the directory of %s: %w", r.doc.GetInvocation(), err)
	}
	defer func() { _ = os.RemoveAll(dir) }()
	work, index := filepath.Join(dir, "workspace"), filepath.Join(dir, "index")
	result := &eventsv1.InvocationResult{Invocation: r.doc.GetInvocation()}
	ex, mt, err := r.start(ctx, work, index)
	if err != nil {
		return r.report(ctx, result, eventsv1.InvocationFinished_OUTCOME_FAILED, err)
	}
	if ex.ended != nil {
		return ex.ended
	}
	outcome, err := r.judge(ctx, ex, mt, work, index, result)
	return r.report(ctx, result, outcome, err)
}

// start checks the input tree out into work, finds the output message and
// its JSON Schema, and runs the adapter. An error fails the invocation
// before the adapter starts.
func (r *run) start(ctx context.Context, work, index string) (*execution, protoreflect.MessageType, error) {
	if err := r.repo.checkout(ctx, r.doc.GetInput().GetTree(), work, index); err != nil {
		return nil, nil, fmt.Errorf("the input tree could not be checked out: %w", err)
	}
	var mt protoreflect.MessageType
	resolved := Resolved{Workspace: work}
	if name := r.doc.GetOutputMessage(); name != "" {
		var err error
		if mt, resolved.JSONSchema, err = api.Output(name); err != nil {
			return nil, nil, err
		}
	}
	inv, err := HarnessInvocation(r.doc, resolved)
	if err != nil {
		return nil, nil, err
	}
	ex, err := r.execute(ctx, inv)
	return ex, mt, err
}

// report sends the remaining log, with the reason the invocation failed
// when problem is not nil, and reports the result with the outcome.
func (r *run) report(ctx context.Context, result *eventsv1.InvocationResult, outcome eventsv1.InvocationFinished_Outcome, problem error) error {
	if problem != nil {
		r.log.note("the invocation %s failed: %v", r.doc.GetInvocation(), problem)
	}
	if err := r.sendLog(ctx); err != nil {
		return err
	}
	_, err := r.cfg.Service.Report(ctx, &runnerv1.ReportRequest{
		Invocation: r.doc.GetInvocation(), LeaseNumber: r.claim.GetLeaseNumber(), Result: result, Outcome: outcome,
	})
	if err != nil {
		return fmt.Errorf("runner: the report of %s: %w", r.doc.GetInvocation(), err)
	}
	return nil
}

// sendLog sends the log output that has not been sent.
func (r *run) sendLog(ctx context.Context) error {
	out := r.log.take()
	if len(out) == 0 {
		return nil
	}
	_, err := r.cfg.Service.SendLog(ctx, &runnerv1.SendLogRequest{
		Invocation: r.doc.GetInvocation(), LeaseNumber: r.claim.GetLeaseNumber(), Output: out,
	})
	if err != nil {
		// The batch goes again with the next one.
		r.log.restore(out)
		return fmt.Errorf("runner: a log batch of %s: %w", r.doc.GetInvocation(), err)
	}
	return nil
}
