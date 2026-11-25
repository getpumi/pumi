// SPDX-License-Identifier: Apache-2.0
// SPDX-FileCopyrightText: The Pumi Authors

package conformance

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"sync"
	"time"

	harnessv1 "getpumi.dev/pumi/api/gen/pumi/harness/v1"
	"google.golang.org/protobuf/encoding/protojson"
)

// process is one run of the adapter, which the suite starts and
// supervises as the runner does (decision 0158).
type process struct {
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	stdout *lines
	stderr *tail
	// written is closed when the write of the invocation line ends.
	written chan struct{}
	// done is closed when the adapter has exited and its output has
	// ended; waitErr is then the error of Wait.
	done      chan struct{}
	waitErr   error
	closeOnce sync.Once
}

// start starts the adapter at path in dir with env and writes inv as one
// line of protojson on its standard input, which stays open.
func start(path, dir string, env []string, inv *harnessv1.Invocation, grace time.Duration) (*process, error) {
	line, err := protojson.Marshal(inv)
	if err != nil {
		return nil, fmt.Errorf("the harness invocation could not be written: %w", err)
	}
	p := &process{stdout: &lines{}, stderr: &tail{}, written: make(chan struct{}), done: make(chan struct{})}
	p.cmd = &exec.Cmd{
		Path: path, Args: []string{path}, Dir: dir, Env: env,
		Stdout: p.stdout, Stderr: p.stderr,
		// After the adapter exits, Wait waits this long for the copies of
		// its output to end, which a process it started can hold open.
		WaitDelay: grace,
	}
	ownGroup(p.cmd)
	if p.stdin, err = p.cmd.StdinPipe(); err != nil {
		return nil, err
	}
	if err := p.cmd.Start(); err != nil {
		return nil, fmt.Errorf("the adapter %s could not start: %w", path, err)
	}
	go func() {
		defer close(p.written)
		_, _ = p.stdin.Write(append(line, '\n'))
	}()
	go func() {
		p.waitErr = p.cmd.Wait()
		close(p.done)
	}()
	return p, nil
}

// closeStdin asks the adapter to stop, once the invocation line is
// written.
func (p *process) closeStdin() {
	p.closeOnce.Do(func() {
		go func() {
			<-p.written
			_ = p.stdin.Close()
		}()
	})
}

// waitFor waits up to d for the adapter to exit and reports whether it
// did.
func (p *process) waitFor(d time.Duration) bool {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-p.done:
		return true
	case <-timer.C:
		return false
	}
}

// kill kills the adapter and the processes it started, and waits for it.
func (p *process) kill() {
	_ = killGroup(p.cmd.Process)
	<-p.done
}

// end kills what the adapter left behind after it exited.
func (p *process) end() {
	_ = killGroup(p.cmd.Process)
}

// outcome is how a run of the adapter ended.
type outcome struct {
	// exitErr is the error of waiting for the adapter: nil when it exited
	// with status 0.
	exitErr error
	// figures are the usage figures, in order, and result the result.
	figures []*harnessv1.UsageFigure
	result  *harnessv1.Result
	// protocolErr is the first line that broke the protocol of decision
	// 0252, or the missing result.
	protocolErr error
	// stderr is the end of the adapter's standard error, its log.
	stderr string
}

// outcome returns how the run ended. The adapter must have exited.
func (p *process) outcome() *outcome {
	<-p.done
	o := &outcome{exitErr: p.waitErr, stderr: p.stderr.String()}
	o.figures, o.result, o.protocolErr = p.stdout.end()
	return o
}

// lines reads the adapter's standard output as the runner does: lines,
// each the protojson of a pumi.harness.v1.Line, usage figures and then the
// result as the last line (decision 0252). The first line that breaks the
// protocol ends the reading.
type lines struct {
	mu      sync.Mutex
	partial []byte
	number  int
	figures []*harnessv1.UsageFigure
	result  *harnessv1.Result
	err     error
}

func (l *lines) Write(b []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.partial = append(l.partial, b...)
	for {
		i := bytes.IndexByte(l.partial, '\n')
		if i < 0 {
			return len(b), nil
		}
		l.read(l.partial[:i])
		l.partial = l.partial[i+1:]
	}
}

// end reads a last line that has no newline and returns what was read.
func (l *lines) end() ([]*harnessv1.UsageFigure, *harnessv1.Result, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if len(l.partial) > 0 {
		l.read(l.partial)
		l.partial = nil
	}
	if l.err == nil && l.result == nil {
		l.err = errors.New("the adapter wrote no result")
	}
	return l.figures, l.result, l.err
}

// read reads one line; a line of white space only is skipped.
func (l *lines) read(text []byte) {
	l.number++
	if l.err != nil || len(bytes.TrimSpace(text)) == 0 {
		return
	}
	line := &harnessv1.Line{}
	if err := protojson.Unmarshal(text, line); err != nil {
		l.err = fmt.Errorf("the line %d of the adapter's standard output does not parse as a pumi.harness.v1.Line: %w", l.number, err)
		return
	}
	switch {
	case l.result != nil:
		l.err = fmt.Errorf("the adapter wrote the line %d of its standard output after its result, which must be the last line", l.number)
	case line.GetResult() != nil:
		l.result = line.GetResult()
	case line.GetUsage() != nil:
		l.figures = append(l.figures, line.GetUsage())
	default:
		l.err = fmt.Errorf("the line %d of the adapter's standard output holds neither a usage figure nor the result", l.number)
	}
}

// tailSize is how much of the adapter's log the suite keeps.
const tailSize = 64 << 10

// tail keeps the end of what is written to it.
type tail struct {
	mu  sync.Mutex
	buf []byte
}

func (t *tail) Write(b []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.buf = append(t.buf, b...)
	if len(t.buf) > tailSize {
		t.buf = t.buf[len(t.buf)-tailSize:]
	}
	return len(b), nil
}

func (t *tail) String() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return string(t.buf)
}
