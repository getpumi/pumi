// SPDX-License-Identifier: Apache-2.0
// SPDX-FileCopyrightText: The Pumi Authors

package runner

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sync"
	"time"

	eventsv1 "getpumi.dev/pumi/api/gen/pumi/events/v1"
	harnessv1 "getpumi.dev/pumi/api/gen/pumi/harness/v1"
	runnerv1 "getpumi.dev/pumi/api/gen/pumi/runner/v1"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/known/durationpb"
)

// execution is how one run of the adapter ended.
type execution struct {
	// stdout is the adapter's standard output.
	stdout []byte
	// waitErr is the error of waiting for the adapter: nil when it exited
	// with status 0.
	waitErr error
	// cancelled reports whether a heartbeat carried a cancel request.
	cancelled bool
	// ended is the error that ends the invocation without a report: ctx's
	// error when the runner stopped, or the error of a call of
	// RunnerService while the adapter ran.
	ended error
}

// execute starts the adapter in the workspace with the harness invocation
// inv as one line on its standard input and supervises it until it exits.
// Its standard error is the log.
func (r *run) execute(ctx context.Context, inv *harnessv1.Invocation) (*execution, error) {
	line, err := protojson.Marshal(inv)
	if err != nil {
		return nil, fmt.Errorf("the harness invocation could not be written: %w", err)
	}
	var stdout bytes.Buffer
	cmd := &exec.Cmd{
		Path: r.cfg.Adapter, Args: []string{r.cfg.Adapter}, Dir: inv.GetWorkspace(), Env: os.Environ(),
		Stdout: &stdout, Stderr: r.log,
		// After the adapter exits, Wait waits this long for the copies of
		// its output to end, which a process it started can hold open.
		WaitDelay: r.cfg.Grace,
	}
	ownGroup(cmd)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("the adapter could not start: %w", err)
	}
	// Standard input stays open after the line, until the runner asks the
	// adapter to stop. The write ends when the adapter reads the line or
	// standard input closes.
	written := make(chan struct{})
	go func() {
		defer close(written)
		_, _ = stdin.Write(append(line, '\n'))
	}()
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	s := &supervisor{run: r, process: cmd.Process, stdin: stdin, written: written, done: done}
	ex := s.supervise(ctx)
	ex.stdout = stdout.Bytes()
	// The processes that the adapter started and left behind end with it.
	if err := killGroup(cmd.Process); err != nil {
		r.log.note("the processes that the adapter started could not be killed: %v", err)
	}
	return ex, nil
}

// supervisor sends heartbeats and log batches while the adapter runs, and
// stops it when it must.
type supervisor struct {
	run     *run
	process *os.Process
	stdin   io.Closer
	// written is closed when the write of the invocation line ends.
	written <-chan struct{}
	done    <-chan error
	ex      execution
	// beat and logs tick for heartbeats and log batches until the
	// invocation ends without a report.
	beat, logs *time.Ticker
	beats      <-chan time.Time
	batches    <-chan time.Time
	// kill fires when the grace period after the stop request ends.
	kill <-chan time.Time
}

// supervise runs until the adapter exits and returns how it ended.
func (s *supervisor) supervise(ctx context.Context) *execution {
	s.beat = time.NewTicker(heartbeatInterval(s.run.claim.GetLeaseTimeout()))
	defer s.beat.Stop()
	s.logs = time.NewTicker(s.run.cfg.LogInterval)
	defer s.logs.Stop()
	s.beats, s.batches = s.beat.C, s.logs.C
	stopped := ctx.Done()
	for {
		select {
		case err := <-s.done:
			s.ex.waitErr = err
			return &s.ex
		case <-stopped:
			stopped = nil
			s.end(ctx.Err())
		case <-s.beats:
			s.heartbeat(ctx)
		case <-s.batches:
			if err := s.run.sendLog(ctx); err != nil {
				s.end(err)
			}
		case <-s.kill:
			s.kill = nil
			if err := killGroup(s.process); err != nil {
				s.run.log.note("the adapter could not be killed: %v", err)
			}
		}
	}
}

// heartbeat renews the lease, stops the adapter when the response carries
// a cancel request, and ends the invocation when the call fails.
func (s *supervisor) heartbeat(ctx context.Context) {
	res, err := s.run.cfg.Service.Heartbeat(ctx, &runnerv1.HeartbeatRequest{
		Invocation: s.run.doc.GetInvocation(), LeaseNumber: s.run.claim.GetLeaseNumber(),
	})
	if err != nil {
		s.end(fmt.Errorf("runner: a heartbeat of %s: %w", s.run.doc.GetInvocation(), err))
		return
	}
	if res.GetLeaseTimeout() != nil {
		s.beat.Reset(heartbeatInterval(res.GetLeaseTimeout()))
	}
	if res.GetCancel() != nil && !s.ex.cancelled {
		s.ex.cancelled = true
		s.run.log.note("%s asked to cancel the invocation %s", res.GetCancel().GetRequestedBy(), s.run.doc.GetInvocation())
		s.stop()
	}
}

// end ends the invocation without a report, with err: it stops the
// heartbeats and log batches and the adapter.
func (s *supervisor) end(err error) {
	if s.ex.ended == nil {
		s.ex.ended = err
	}
	s.beats, s.batches = nil, nil
	s.stop()
}

// stop asks the adapter to stop by closing its standard input, and starts
// the grace period, after which the runner kills it and the processes it
// started (decisions 0035 and 0158). Standard input closes after the invocation line is written, so
// that the adapter always gets the invocation; an adapter that does not
// read it is killed after the grace period, which ends the write.
func (s *supervisor) stop() {
	if s.kill != nil {
		return
	}
	go func() {
		<-s.written
		_ = s.stdin.Close()
	}()
	s.kill = time.After(s.run.cfg.Grace)
}

// heartbeatInterval is the time between heartbeats for the lease timeout:
// a third of it, so that two heartbeats can fail before the lease ends.
func heartbeatInterval(timeout *durationpb.Duration) time.Duration {
	if d := timeout.AsDuration() / 3; d > 0 {
		return d
	}
	return time.Second
}

// judge checks how the adapter's run ended and fills result: the usage,
// the resulting tree and the structured output. It returns the outcome,
// with the reason when the invocation failed. A failed invocation's result
// holds at most the usage.
func (r *run) judge(ctx context.Context, ex *execution, mt protoreflect.MessageType, work, index string, result *eventsv1.InvocationResult) (eventsv1.InvocationFinished_Outcome, error) {
	failed := eventsv1.InvocationFinished_OUTCOME_FAILED
	if ex.cancelled {
		failed = eventsv1.InvocationFinished_OUTCOME_CANCELLED
	}
	res, err := parseResult(ex)
	if err != nil {
		return failed, err
	}
	result.Usage = usageOf(res.GetUsage())
	if len(res.GetOutputFiles()) > 0 {
		return failed, fmt.Errorf("the adapter reported the output files %q, which the runner cannot store before the blob store of epic E7", res.GetOutputFiles())
	}
	tree, err := r.resultingTree(ctx, work, index)
	if err != nil {
		return failed, err
	}
	output, err := parseOutput(mt, res)
	if err != nil {
		return failed, err
	}
	outcome, err := outcomeOf(res.GetStatus(), ex.cancelled)
	if err != nil {
		return failed, err
	}
	result.Tree, result.Output = tree, output
	return outcome, nil
}

// parseResult parses the adapter's result from its standard output, with
// unknown fields refused. A non-zero exit status, a missing result and a
// result that does not parse are errors (decision 0158).
func parseResult(ex *execution) (*harnessv1.Result, error) {
	if ex.waitErr != nil {
		return nil, fmt.Errorf("the adapter ended with %w", ex.waitErr)
	}
	if len(bytes.TrimSpace(ex.stdout)) == 0 {
		return nil, errors.New("the adapter wrote no result")
	}
	res := &harnessv1.Result{}
	if err := protojson.Unmarshal(ex.stdout, res); err != nil {
		return nil, fmt.Errorf("the adapter's result does not parse: %w", err)
	}
	return res, nil
}

// parseOutput parses the structured output of res strictly into a new
// message of the type mt, so that an unknown field fails, and returns its
// protojson (decision 0160). A completed run must have an output when the
// document names a message, and no run may have one when it names none.
func parseOutput(mt protoreflect.MessageType, res *harnessv1.Result) (string, error) {
	out := res.GetOutput()
	switch {
	case mt == nil && out != "":
		return "", errors.New("the adapter wrote a structured output, but the invocation names no output message")
	case mt == nil:
		return "", nil
	case out == "" && res.GetStatus() == harnessv1.Result_STATUS_COMPLETED:
		return "", fmt.Errorf("the adapter completed without the structured output %s", mt.Descriptor().FullName())
	case out == "":
		return "", nil
	}
	msg := mt.New().Interface()
	if err := protojson.Unmarshal([]byte(out), msg); err != nil {
		return "", fmt.Errorf("the structured output does not parse as %s: %w", mt.Descriptor().FullName(), err)
	}
	parsed, err := protojson.Marshal(msg)
	if err != nil {
		return "", err
	}
	return string(parsed), nil
}

// outcomeOf returns the outcome of a run that ended with status, when a
// cancel was requested or not. A run that the runner asked to stop is
// cancelled whatever its status (decision 0035).
func outcomeOf(status harnessv1.Result_Status, cancelled bool) (eventsv1.InvocationFinished_Outcome, error) {
	switch {
	case status == harnessv1.Result_STATUS_UNSPECIFIED:
		return 0, errors.New("the adapter's result has no status")
	case cancelled:
		return eventsv1.InvocationFinished_OUTCOME_CANCELLED, nil
	case status == harnessv1.Result_STATUS_COMPLETED:
		return eventsv1.InvocationFinished_OUTCOME_SUCCEEDED, nil
	case status == harnessv1.Result_STATUS_FAILED:
		return eventsv1.InvocationFinished_OUTCOME_FAILED, nil
	case status == harnessv1.Result_STATUS_STOPPED:
		return 0, errors.New("the adapter stopped, but the runner did not ask it to")
	}
	return 0, fmt.Errorf("the adapter's result has the status %v", status)
}

// resultingTree computes the tree of the workspace and fails when it
// changes a file outside the allowed paths (decision 0159). It returns ""
// when the tree is the input tree, because the invocation changed no code
// (decision 0162).
func (r *run) resultingTree(ctx context.Context, work, index string) (string, error) {
	input := r.doc.GetInput().GetTree()
	tree, err := r.repo.writeTree(ctx, work, index)
	if err != nil {
		return "", fmt.Errorf("the resulting tree could not be computed: %w", err)
	}
	paths, err := r.repo.changed(ctx, input, tree)
	if err != nil {
		return "", err
	}
	globs := r.doc.GetCapabilities().GetWritePaths()
	out, err := outside(paths, globs)
	if err != nil {
		return "", err
	}
	if len(out) > 0 {
		return "", fmt.Errorf("the adapter wrote %q, outside the allowed paths %q (decision 0159)", out, globs)
	}
	if tree == input {
		return "", nil
	}
	return tree, nil
}

// usageOf returns the usage of invocation.result for the adapter's usage
// figures, which count until the model gateway of E7 counts them (decision
// 0185).
func usageOf(u *harnessv1.Usage) *eventsv1.InvocationResult_Usage {
	if u == nil {
		return nil
	}
	return &eventsv1.InvocationResult_Usage{Tokens: u.GetTokens(), CostMicrodollars: u.GetCostMicrodollars(), Seconds: u.GetSeconds()}
}

// logBuffer holds the log output that has not been sent yet: the
// adapter's standard error and the runner's own lines.
type logBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

// Write adds p to the log.
func (l *logBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.buf.Write(p)
}

// note adds a line of the runner to the log.
func (l *logBuffer) note(format string, args ...any) {
	l.mu.Lock()
	defer l.mu.Unlock()
	fmt.Fprintf(&l.buf, runnerPrefix+format+"\n", args...)
}

// take returns the log output that has not been sent and forgets it.
func (l *logBuffer) take() []byte {
	l.mu.Lock()
	defer l.mu.Unlock()
	out := bytes.Clone(l.buf.Bytes())
	l.buf.Reset()
	return out
}
