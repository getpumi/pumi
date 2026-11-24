// SPDX-License-Identifier: Apache-2.0
// SPDX-FileCopyrightText: The Pumi Authors

package runner

import (
	"bytes"
	"cmp"
	"fmt"
	"io"
	"slices"
	"sync"
)

// maskedCredential takes the place of a credential value in the log
// (decisions 0163 and 0232).
const maskedCredential = "[masked]"

// logBuffer holds an invocation's log: the adapter's standard error and
// the runner's own lines. It masks the values of the credentials that the
// runner holds for the invocation before any of the log leaves it, in a
// batch, in the full log or in the output, also a value that arrives in
// two writes: output that could be the start of a value stays back until
// the next write shows whether it is one, or until end.
type logBuffer struct {
	mu sync.Mutex
	// secrets are the values to mask, longest first, so that a value that
	// holds another is masked whole.
	secrets [][]byte
	// held is output not yet masked, because it ends with what could be
	// the start of a value.
	held []byte
	// buf is masked output that has not been sent.
	buf bytes.Buffer
	// all is the whole masked log so far, which the runner stores in the
	// blob store at the end (decision 0034).
	all bytes.Buffer
	// out, when set, also receives the masked log as it is released.
	out *lineWriter
}

// mask adds the credential values to mask in what is written from now on.
// Empty values are ignored.
func (l *logBuffer) mask(values ...string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, v := range values {
		if v != "" && !slices.ContainsFunc(l.secrets, func(s []byte) bool { return string(s) == v }) {
			l.secrets = append(l.secrets, []byte(v))
		}
	}
	slices.SortStableFunc(l.secrets, func(a, b []byte) int { return cmp.Compare(len(b), len(a)) })
}

// Write adds p to the log.
func (l *logBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.held = append(l.held, p...)
	l.release(false)
	return len(p), nil
}

// note adds a line of the runner to the log.
func (l *logBuffer) note(format string, args ...any) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.held = append(l.held, fmt.Sprintf(runnerPrefix+format+"\n", args...)...)
	l.release(false)
}

// end releases the output that stays back, because no more output comes.
func (l *logBuffer) end() {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.release(true)
}

// release masks the held output and moves it to the batch, the full log
// and the output. Unless final, it keeps back the end of the held output
// when that is the start of a value. The caller holds mu.
func (l *logBuffer) release(final bool) {
	var masked []byte
	i := 0
	for i < len(l.held) {
		rest := l.held[i:]
		if n := l.secretAt(rest); n > 0 {
			masked = append(masked, maskedCredential...)
			i += n
			continue
		}
		if !final && l.startsSecret(rest) {
			break
		}
		masked = append(masked, l.held[i])
		i++
	}
	l.held = append(l.held[:0], l.held[i:]...)
	if len(masked) == 0 {
		return
	}
	l.buf.Write(masked)
	l.all.Write(masked)
	if l.out != nil {
		l.out.write(masked)
	}
}

// secretAt returns the length of the longest value that p starts with, or
// 0 when it starts with none.
func (l *logBuffer) secretAt(p []byte) int {
	for _, s := range l.secrets {
		if bytes.HasPrefix(p, s) {
			return len(s)
		}
	}
	return 0
}

// startsSecret reports whether p, all of it, is the start of a value.
func (l *logBuffer) startsSecret(p []byte) bool {
	for _, s := range l.secrets {
		if len(p) < len(s) && bytes.HasPrefix(s, p) {
			return true
		}
	}
	return false
}

// full returns the whole masked log released so far.
func (l *logBuffer) full() []byte {
	l.mu.Lock()
	defer l.mu.Unlock()
	return bytes.Clone(l.all.Bytes())
}

// flush releases the output that stays back and writes a line of the
// output that has not ended.
func (l *logBuffer) flush() {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.release(true)
	if l.out != nil {
		l.out.flush()
	}
}

// restore puts out, which take returned and which could not be sent,
// back before the output released since.
func (l *logBuffer) restore(out []byte) {
	l.mu.Lock()
	defer l.mu.Unlock()
	rest := bytes.Clone(l.buf.Bytes())
	l.buf.Reset()
	l.buf.Write(out)
	l.buf.Write(rest)
}

// take returns the masked log output that has not been sent and forgets
// it.
func (l *logBuffer) take() []byte {
	l.mu.Lock()
	defer l.mu.Unlock()
	out := bytes.Clone(l.buf.Bytes())
	l.buf.Reset()
	return out
}

// lineWriter writes each line of what it is given to out after prefix. It
// keeps a line that has not ended until it ends or flush is called. Its
// callers serialize the calls, and an error of out is dropped: the output
// is for the person who watches, and the log goes to the control plane
// either way.
type lineWriter struct {
	out     io.Writer
	prefix  string
	partial []byte
}

// write writes the lines of p that end, and keeps the rest.
func (w *lineWriter) write(p []byte) {
	w.partial = append(w.partial, p...)
	for {
		i := bytes.IndexByte(w.partial, '\n')
		if i < 0 {
			return
		}
		_, _ = io.WriteString(w.out, w.prefix+string(w.partial[:i+1]))
		w.partial = w.partial[i+1:]
	}
}

// flush writes a line that has not ended, with a newline.
func (w *lineWriter) flush() {
	if len(w.partial) > 0 {
		_, _ = io.WriteString(w.out, w.prefix+string(w.partial)+"\n")
		w.partial = nil
	}
}
