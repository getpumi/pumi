// SPDX-License-Identifier: Apache-2.0
// SPDX-FileCopyrightText: The Pumi Authors

package runner

import (
	"bytes"
	"errors"
	"fmt"
	"sync"

	harnessv1 "getpumi.dev/pumi/api/gen/pumi/harness/v1"
	"google.golang.org/protobuf/encoding/protojson"
)

// stdoutReader reads the adapter's standard output as it is written: lines,
// each the protojson of a pumi.harness.v1.Line, which hold usage figures and
// then the result as the last line (decisions 0241 and 0252). It passes
// each usage figure to usage as soon as its line is complete, and keeps the
// result. The first line that breaks the protocol ends the reading, and
// err returns it.
type stdoutReader struct {
	mu sync.Mutex
	// usage receives each usage figure, in order.
	usage func(*harnessv1.UsageFigure)
	// partial is the start of a line whose end has not been written.
	partial []byte
	// number is the number of the last line read, counted from 1.
	number int
	result *harnessv1.Result
	err    error
}

// Write reads the complete lines of p, and keeps the rest for the next
// write.
func (s *stdoutReader) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.partial = append(s.partial, p...)
	for {
		i := bytes.IndexByte(s.partial, '\n')
		if i < 0 {
			break
		}
		s.read(s.partial[:i])
		s.partial = s.partial[i+1:]
	}
	return len(p), nil
}

// end reads the last line when it has no newline, because no more output
// comes, and returns the result, or the error that ended the reading.
func (s *stdoutReader) end() (*harnessv1.Result, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.partial) > 0 {
		s.read(s.partial)
		s.partial = nil
	}
	if s.err != nil {
		return nil, s.err
	}
	if s.result == nil {
		return nil, errors.New("the adapter wrote no result")
	}
	return s.result, nil
}

// read reads one line. A line of white space only is skipped.
func (s *stdoutReader) read(text []byte) {
	s.number++
	if s.err != nil || len(bytes.TrimSpace(text)) == 0 {
		return
	}
	line := &harnessv1.Line{}
	if err := protojson.Unmarshal(text, line); err != nil {
		s.err = fmt.Errorf("the line %d of the adapter's standard output does not parse as a pumi.harness.v1.Line: %w", s.number, err)
		return
	}
	switch {
	case s.result != nil:
		s.err = fmt.Errorf("the adapter wrote the line %d of its standard output after its result, which must be the last line", s.number)
	case line.GetResult() != nil:
		s.result = line.GetResult()
	case line.GetUsage() != nil:
		s.usage(line.GetUsage())
	default:
		s.err = fmt.Errorf("the line %d of the adapter's standard output holds neither a usage figure nor the result", s.number)
	}
}
