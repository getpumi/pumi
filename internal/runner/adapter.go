// SPDX-License-Identifier: Apache-2.0
// SPDX-FileCopyrightText: The Pumi Authors

package runner

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strings"
	"time"

	"connectrpc.com/connect/v2"
	eventsv1 "getpumi.dev/pumi/api/gen/pumi/events/v1"
	harnessv1 "getpumi.dev/pumi/api/gen/pumi/harness/v1"
	runnerv1 "getpumi.dev/pumi/api/gen/pumi/runner/v1"
	"getpumi.dev/pumi/internal/shared"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/known/durationpb"
)

// execution is how one run of the adapter ended.
type execution struct {
	// result is the result that the adapter wrote as the last line of its
	// standard output, and readErr the error that ended the reading of its
	// standard output, or that says it wrote no result (decision 0252).
	result  *harnessv1.Result
	readErr error
	// waitErr is the error of waiting for the adapter: nil when it exited
	// with status 0.
	waitErr error
	// cancelled reports whether a heartbeat carried a cancel request.
	cancelled bool
	// capped reports whether the runner stopped the adapter at the
	// invocation's wall-time limit (decision 0193).
	capped bool
	// elapsed is the wall time from the start of the adapter until it
	// exited.
	elapsed time.Duration
	// ended is the error that ends the invocation without a report: ctx's
	// error when the runner stopped, or the error of a call of
	// RunnerService while the adapter ran.
	ended error
}

// execute starts the adapter at path in the workspace with the harness invocation
// inv as one line on its standard input and supervises it until it exits.
// Its standard error is the log.
func (r *run) execute(ctx context.Context, path string, inv *harnessv1.Invocation) (*execution, error) {
	line, err := protojson.Marshal(inv)
	if err != nil {
		return nil, fmt.Errorf("the harness invocation could not be written: %w", err)
	}
	stdout := &stdoutReader{usage: r.passUsage}
	cmd := &exec.Cmd{
		Path: path, Args: []string{path}, Dir: inv.GetWorkspace(), Env: os.Environ(),
		Stdout: stdout, Stderr: r.log,
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
	started := time.Now()
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
	ex.elapsed = time.Since(started)
	ex.result, ex.readErr = stdout.end()
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
	// limit fires when the wall time reaches the budget's seconds; nil
	// when the budget sets none.
	limit <-chan time.Time
	// metered is closed when the runner's count of the usage figures on
	// the sign-in path says that the run must stop; nil on any other path
	// (decision 0241).
	metered <-chan struct{}
	// leaseEnd is when the lease ends unless a heartbeat renews it,
	// counted from the claim or the last heartbeat that renewed it.
	leaseEnd time.Time
}

// supervise runs until the adapter exits and returns how it ended.
func (s *supervisor) supervise(ctx context.Context) *execution {
	s.beat = time.NewTicker(heartbeatInterval(s.run.claim.GetLeaseTimeout()))
	defer s.beat.Stop()
	s.logs = time.NewTicker(s.run.cfg.LogInterval)
	defer s.logs.Stop()
	s.beats, s.batches = s.beat.C, s.logs.C
	s.leaseEnd = s.run.claimed.Add(s.run.claim.GetLeaseTimeout().AsDuration())
	if seconds := s.run.budget.GetSeconds(); seconds > 0 {
		limit := time.NewTimer(time.Duration(seconds) * time.Second)
		defer limit.Stop()
		s.limit = limit.C
	}
	if m := s.run.meter; m != nil {
		s.metered = m.stop
	}
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
				s.failed(err)
			}
		case <-s.limit:
			s.limit = nil
			s.capReached()
		case <-s.metered:
			s.metered = nil
			s.meterStop()
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
	sent := time.Now()
	res, err := s.run.cfg.Service.Heartbeat(ctx, &runnerv1.HeartbeatRequest{
		Invocation: s.run.doc.GetInvocation(), LeaseNumber: s.run.claim.GetLeaseNumber(),
	})
	if err != nil {
		s.failed(fmt.Errorf("runner: a heartbeat of %s: %w", s.run.doc.GetInvocation(), err))
		return
	}
	// The lease is renewed from before the heartbeat was sent.
	timeout := s.run.claim.GetLeaseTimeout()
	if res.GetLeaseTimeout() != nil {
		timeout = res.GetLeaseTimeout()
		s.beat.Reset(heartbeatInterval(timeout))
	}
	s.leaseEnd = sent.Add(timeout.AsDuration())
	if res.GetCancel() != nil && !s.ex.cancelled && !s.ex.capped {
		s.ex.cancelled = true
		s.run.log.note("%s asked to cancel the invocation %s", res.GetCancel().GetRequestedBy(), s.run.doc.GetInvocation())
		s.stop()
	}
}

// failed handles the error of a heartbeat or a log batch. While the
// control plane cannot be reached, the adapter runs on until the lease
// would end, counted from the claim or the last heartbeat that renewed it;
// then, or at once for any other error, such as a lease that is not
// current, the invocation ends without a report (decision 0205).
func (s *supervisor) failed(err error) {
	if connect.CodeOf(err) != connect.CodeUnavailable {
		s.end(err)
		return
	}
	if !time.Now().Before(s.leaseEnd) {
		s.end(fmt.Errorf("runner: the lease of %s ended at %s while the control plane could not be reached: %w",
			s.run.doc.GetInvocation(), s.leaseEnd.Format(time.RFC3339Nano), err))
		return
	}
	s.run.log.note("the control plane could not be reached; the lease of %s ends at %s: %v",
		s.run.doc.GetInvocation(), s.leaseEnd.Format(time.RFC3339Nano), err)
}

// capReached stops the adapter when the wall time reaches the budget's
// seconds, unless a cancel already stopped it (decision 0193).
func (s *supervisor) capReached() {
	if s.ex.cancelled || s.ex.capped {
		return
	}
	s.ex.capped = true
	s.run.log.note("the invocation %s reached its wall-time limit of %d s", s.run.doc.GetInvocation(), s.run.budget.GetSeconds())
	s.stop()
}

// meterStop stops the adapter when the runner's count of its usage figures
// reaches the token or cost cap, which is cap reached unless a cancel or
// the wall-time limit already stopped it, or when a figure cannot be
// priced, which fails the invocation (decision 0241).
func (s *supervisor) meterStop() {
	if err := s.run.meter.failure(); err != nil {
		s.run.log.note("the runner stops the adapter of %s: %v", s.run.doc.GetInvocation(), err)
		s.stop()
		return
	}
	if s.ex.cancelled || s.ex.capped {
		return
	}
	s.ex.capped = true
	tokens, microdollars := s.run.meter.current()
	s.run.log.note("the invocation %s reached its token or cost cap with %d tokens and %d microdollars that the runner counted from the adapter's usage figures (decision 0241)",
		s.run.doc.GetInvocation(), tokens, microdollars)
	s.stop()
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
// the resulting tree and the structured output. The usage is the gateway's
// totals with the measured wall time (decision 0230), or on the sign-in
// path the runner's count of the adapter's usage figures, or of its
// result's usage when it passed none (decision 0241). It returns the
// outcome, with the reason when the invocation failed. A failed
// invocation's result holds at most the usage and the refusals that the
// harness reported (decision 0249).
func (r *run) judge(ctx context.Context, ex *execution, mt protoreflect.MessageType, work, index string, totals GatewayTotals, result *eventsv1.InvocationResult) (eventsv1.InvocationFinished_Outcome, error) {
	res, resErr := resultOf(ex)
	capReached, failed := r.usage(ex, res, totals, result)
	if resErr != nil {
		return failed, resErr
	}
	if r.meter != nil {
		if err := r.meter.failure(); err != nil {
			return eventsv1.InvocationFinished_OUTCOME_FAILED, err
		}
	}
	// The refusals stay in the result whatever the outcome, because they
	// can say why the run failed (decision 0249).
	if refusals := res.GetRefusals(); len(refusals) > 0 {
		result.Refusals = cloneRefusals(refusals)
		r.log.note("the harness refused: %s (decision 0249)", shared.Refusals(refusals))
	}
	if res.GetStatus() == harnessv1.Result_STATUS_LIMITED {
		r.log.note("the provider's usage or rate limit stopped the run of %s (decision 0249)", r.doc.GetInvocation())
	}
	r.noteAdapterUsage(res.GetUsage())
	tree, err := r.resultingTree(ctx, work, index)
	if err != nil {
		return failed, err
	}
	output, err := parseOutput(mt, res)
	if err != nil {
		return failed, err
	}
	outcome, err := outcomeOf(res.GetStatus(), ex.cancelled || ex.capped)
	if err != nil {
		return failed, err
	}
	if capReached {
		outcome = eventsv1.InvocationFinished_OUTCOME_CAP_REACHED
	}
	// The output files are uploaded last, so that the blob store gets none
	// of an invocation that fails.
	files, err := r.upload(ctx, work, res.GetOutputFiles())
	if err != nil {
		return failed, err
	}
	result.Tree, result.Output, result.OutputFiles = tree, output, files
	return outcome, nil
}

// upload stores each of the output files paths, relative to the workspace
// work and separated by slashes, in the blob store, and returns them with
// their hashes, each path cleaned (decision 0160). A path that is not
// local to the workspace, a link that leads out of it and a path that is
// not a regular file are refused.
func (r *run) upload(ctx context.Context, work string, paths []string) ([]*eventsv1.InvocationResult_OutputFile, error) {
	if len(paths) == 0 {
		return nil, nil
	}
	// The root refuses a path, a link included, that leads out of the
	// workspace.
	root, err := os.OpenRoot(work)
	if err != nil {
		return nil, fmt.Errorf("the workspace could not be opened for the output files: %w", err)
	}
	defer func() { _ = root.Close() }()
	files := make([]*eventsv1.InvocationResult_OutputFile, 0, len(paths))
	for _, p := range paths {
		name := filepath.FromSlash(p)
		if !filepath.IsLocal(name) {
			return nil, fmt.Errorf("the output file %q is not a path in the workspace", p)
		}
		hash, err := r.put(ctx, root, name)
		if err != nil {
			return nil, fmt.Errorf("the output file %q: %w", p, err)
		}
		files = append(files, &eventsv1.InvocationResult_OutputFile{Path: path.Clean(p), BlobHash: hash})
	}
	return files, nil
}

// put stores the regular file name of root in the blob store and returns
// its hash.
func (r *run) put(ctx context.Context, root *os.Root, name string) (string, error) {
	f, err := root.Open(name)
	if err != nil {
		return "", err
	}
	defer func() { _ = f.Close() }()
	info, err := f.Stat()
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("it is not a regular file but %v", info.Mode().Type())
	}
	hash, err := r.cfg.Blobs.Put(ctx, f)
	if err != nil {
		return "", fmt.Errorf("the blob store: %w", err)
	}
	return hash, nil
}

// resultOf returns the adapter's result, which the runner read from its
// standard output with unknown fields refused. A non-zero exit status, a
// missing result, a line that does not parse and a line after the result
// are errors (decisions 0158 and 0252).
func resultOf(ex *execution) (*harnessv1.Result, error) {
	if ex.waitErr != nil {
		return nil, fmt.Errorf("the adapter ended with %w", ex.waitErr)
	}
	if ex.readErr != nil {
		return nil, ex.readErr
	}
	return ex.result, nil
}

// cloneRefusals returns a copy of refusals, so that the result shares
// nothing with the adapter's.
func cloneRefusals(refusals []*harnessv1.Refusal) []*harnessv1.Refusal {
	out := make([]*harnessv1.Refusal, len(refusals))
	for i, r := range refusals {
		out[i] = proto.CloneOf(r)
	}
	return out
}

// usage sets the usage of result: the gateway's totals, or on the sign-in
// path the runner's count, with the measured wall time. It returns whether
// the outcome is cap reached, and the outcome of the invocation if it
// fails: cancelled, cap reached or failed.
func (r *run) usage(ex *execution, res *harnessv1.Result, totals GatewayTotals, result *eventsv1.InvocationResult) (bool, eventsv1.InvocationFinished_Outcome) {
	if r.meter != nil {
		totals = r.signInTotals(res)
	} else if totals.CapReached {
		r.log.note("the model gateway refused a call of %s at its token or cost cap (decision 0228)", r.doc.GetInvocation())
	}
	result.Usage = &eventsv1.InvocationResult_Usage{
		Tokens: totals.Tokens, CostMicrodollars: totals.CostMicrodollars,
		Seconds: int64((ex.elapsed + time.Second - 1) / time.Second),
	}
	// A cancel outranks a cap: the person asked to stop the run.
	capReached := !ex.cancelled && (ex.capped || totals.CapReached)
	switch {
	case ex.cancelled:
		return capReached, eventsv1.InvocationFinished_OUTCOME_CANCELLED
	case capReached:
		return capReached, eventsv1.InvocationFinished_OUTCOME_CAP_REACHED
	}
	return capReached, eventsv1.InvocationFinished_OUTCOME_FAILED
}

// signInTotals returns the usage of an agent run on the sign-in path: the
// runner's count of the usage figures, or the usage of the result res when
// the adapter passed none. It checks the caps on that usage, which for an
// adapter that passed no figures is when the run ends (decision 0241).
func (r *run) signInTotals(res *harnessv1.Result) GatewayTotals {
	c := r.meter.count(res)
	if c.figures == 0 && c.capReached {
		r.log.note("the usage of %s reached its token or cost cap when the run ended, with %d tokens and %d microdollars (decision 0241)",
			r.doc.GetInvocation(), c.tokens, c.microdollars)
	}
	return GatewayTotals{Tokens: c.tokens, CostMicrodollars: c.microdollars, CapReached: c.capReached}
}

// noteAdapterUsage writes the usage that the adapter reported in its
// result to the log, and says which usage the result holds instead.
func (r *run) noteAdapterUsage(u *harnessv1.Usage) {
	if u == nil {
		return
	}
	why := "the usage is the model gateway's totals (decision 0230)"
	if r.meter != nil {
		why = "the usage is the runner's count of its usage figures (decision 0241)"
		if c := r.meter.count(nil); c.figures == 0 {
			why = "it passed no usage figures, so the usage is the result's and the caps are checked when the run ends (decision 0241)"
		}
	}
	r.log.note("the adapter reported %d tokens, %d microdollars and %d s; %s", u.GetTokens(), u.GetCostMicrodollars(), u.GetSeconds(), why)
}

// passUsage writes a usage figure that the adapter passed while it ran to
// the log, and on the sign-in path counts it (decision 0241).
func (r *run) passUsage(u *harnessv1.UsageFigure) {
	r.logUsage(u)
	if r.meter != nil {
		r.meter.add(u)
	}
}

// logUsage writes a usage figure that the adapter passed while it ran to
// the log (decision 0241).
func (r *run) logUsage(u *harnessv1.UsageFigure) {
	var extra strings.Builder
	if u.GetSpeed() != "" {
		fmt.Fprintf(&extra, ", speed %s", u.GetSpeed())
	}
	if u.GetInferenceGeo() != "" {
		fmt.Fprintf(&extra, ", inference geography %s", u.GetInferenceGeo())
	}
	r.log.note("the adapter passed the usage of a call to %s: %d input, %d five-minute cache write, %d one-hour cache write, %d cache read and %d output tokens, %d web searches%s",
		u.GetModel(), u.GetInputTokens(), u.GetFiveMinuteCacheWriteTokens(), u.GetOneHourCacheWriteTokens(),
		u.GetCacheReadTokens(), u.GetOutputTokens(), u.GetWebSearches(), extra.String())
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

// outcomeOf returns the outcome of a run that ended with status, when the
// runner asked the adapter to stop or not. A run that the runner asked to
// stop on a cancel is cancelled whatever its status (decision 0035); the
// caller makes one stopped at its wall-time limit cap reached.
func outcomeOf(status harnessv1.Result_Status, stopped bool) (eventsv1.InvocationFinished_Outcome, error) {
	switch {
	case status == harnessv1.Result_STATUS_UNSPECIFIED:
		return 0, errors.New("the adapter's result has no status")
	case stopped:
		return eventsv1.InvocationFinished_OUTCOME_CANCELLED, nil
	case status == harnessv1.Result_STATUS_COMPLETED:
		return eventsv1.InvocationFinished_OUTCOME_SUCCEEDED, nil
	case status == harnessv1.Result_STATUS_FAILED:
		return eventsv1.InvocationFinished_OUTCOME_FAILED, nil
	case status == harnessv1.Result_STATUS_LIMITED:
		// The run fails with the limit as its reason (decision 0249).
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
