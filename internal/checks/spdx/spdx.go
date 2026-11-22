// SPDX-License-Identifier: Apache-2.0
// SPDX-FileCopyrightText: The Pumi Authors

// Package spdx is the check that every source file starts with the two SPDX
// lines of decision 0068, SPDX-License-Identifier: Apache-2.0 and
// SPDX-FileCopyrightText: The Pumi Authors, in the comment syntax of its file
// type. Classify lists which files are source and which are exempt. A file
// that Classify does not know fails the check, so that every file in the
// repository is either checked or exempt for a stated reason.
package spdx

import (
	"bytes"
	"errors"
	"io/fs"
	"path"
	"regexp"
)

// Style is how a file carries the SPDX lines, or that it is exempt or of a
// file type that the check does not know.
type Style int

// The styles. Unknown is the zero value.
const (
	// Unknown is a file type that the check does not list. It fails.
	Unknown Style = iota
	// Exempt is a file that does not need the SPDX lines.
	Exempt
	// Slash is a file whose first two lines are // comments.
	Slash
	// Hash is a file whose first two lines are # comments. In a shell
	// script, a #! line may come before them.
	Hash
	// HTML is a file that starts with an HTML comment that holds the two
	// lines, each on a line of its own.
	HTML
)

// The two SPDX lines of decision 0068, without comment syntax.
const (
	licenseLine   = "SPDX-License-Identifier: Apache-2.0"
	copyrightLine = "SPDX-FileCopyrightText: The Pumi Authors"
)

// headers holds what a file of each style starts with, and the problem that
// a file without it has.
var headers = map[Style]struct {
	text    []byte
	problem string
}{
	Slash: {
		[]byte("// " + licenseLine + "\n// " + copyrightLine + "\n"),
		"does not start with the two SPDX lines as // comments (decision 0068)",
	},
	Hash: {
		[]byte("# " + licenseLine + "\n# " + copyrightLine + "\n"),
		"does not start with the two SPDX lines as # comments (decision 0068)",
	},
	HTML: {
		[]byte("<!--\n" + licenseLine + "\n" + copyrightLine + "\n-->\n"),
		"does not start with the two SPDX lines in an HTML comment (decision 0068)",
	},
}

// unknownProblem is the problem of a file whose type the check does not list.
const unknownProblem = "has a file type that the SPDX check does not list; list it in internal/checks/spdx as a source type or as exempt (decision 0068)"

// exemptPaths are the files, by their path from the repository root, that do
// not carry the SPDX lines.
var exemptPaths = map[string]bool{
	// The text of the Apache License 2.0, which is not changed (decision 0067).
	"LICENSE": true,
	// Its content is set by decision 0067: the name and the copyright line.
	"NOTICE": true,
	// It holds only the import of AGENTS.md (decision 0091), which Check
	// checks.
	claudeFile: true,
	// The CloudEvents protobuf envelope (decision 0132), kept unchanged from
	// the CloudEvents specification, which has no header in it. It is
	// Apache-2.0; the README.md beside it gives its source.
	"api/proto/io/cloudevents/v1/cloudevents.proto": true,
}

// claudeFile is the file that holds only the import of AGENTS.md, and
// claudeContent is that content.
const (
	claudeFile    = "CLAUDE.md"
	claudeContent = "@AGENTS.md\n"
)

// claudeProblem is the problem of a CLAUDE.md that holds more than the import.
const claudeProblem = "is exempt only while it holds only the line @AGENTS.md (decisions 0068 and 0091)"

// pathStyles are the files, by their path from the repository root, whose
// style differs from the one their name or extension gives.
var pathStyles = map[string]Style{
	// The devcontainer configuration is JSON with comments, unlike the
	// other JSON files.
	".devcontainer/devcontainer.json": Slash,
}

// nameStyles are the files, by their base name, in any directory.
var nameStyles = map[string]Style{
	"Makefile":   Hash,
	"Dockerfile": Hash,
	".gitignore": Hash,
}

// extensionStyles are the files, by their extension, in any directory.
var extensionStyles = map[string]Style{
	".go": Slash,
	// go.mod and the module files of the pinned tools in tools/ (decision
	// 0086) take // comments.
	".mod":  Slash,
	".yml":  Hash,
	".yaml": Hash,
	".sh":   Hash,
	".md":   HTML,
	// The protobuf schemas in api/proto (decision 0090).
	".proto": Slash,
	// The Cedar schema in the api module (decision 0141).
	".cedarschema": Slash,
	// The Cedar policies in the api module (decision 0142).
	".cedar": Slash,
	// The workflow file and its test fixtures, in protobuf text format
	// (decision 0167).
	".txtpb": Hash,
	// The graph of the first workflow's state machine, in the DOT language
	// of Graphviz, which has // comments.
	".dot": Slash,
	// go.sum and the tools' .sum files are written by the go command.
	".sum": Exempt,
	// JSON has no comments, and the files in .pumi/ are plain JSON
	// (decision 0112).
	".json": Exempt,
}

// Classify returns the style of the file at name, a slash-separated path from
// the repository root. A generated Go file is classified as Slash here and is
// exempted by Check, which reads it.
func Classify(name string) Style {
	if exemptPaths[name] {
		return Exempt
	}
	if s, ok := pathStyles[name]; ok {
		return s
	}
	if s, ok := nameStyles[path.Base(name)]; ok {
		return s
	}
	if s, ok := extensionStyles[path.Ext(name)]; ok {
		return s
	}
	return Unknown
}

// Finding is one file that fails the check.
type Finding struct {
	Path    string
	Problem string
}

// String formats the finding as path: problem.
func (f Finding) String() string {
	return f.Path + ": " + f.Problem
}

// Check returns the files among paths in fsys that fail the check, in the
// order of paths. It skips paths that do not exist, directories and symbolic
// links. It does not read exempt files, except CLAUDE.md, which is exempt only
// while it holds only the import of AGENTS.md.
func Check(fsys fs.FS, paths []string) ([]Finding, error) {
	var findings []Finding
	for _, name := range paths {
		problem, err := checkFile(fsys, name)
		if err != nil {
			return nil, err
		}
		if problem != "" {
			findings = append(findings, Finding{Path: name, Problem: problem})
		}
	}
	return findings, nil
}

// checkFile returns the problem of the file at name, or "" when it passes or
// is skipped.
func checkFile(fsys fs.FS, name string) (string, error) {
	if name == claudeFile {
		return checkClaudeFile(fsys)
	}
	style := Classify(name)
	if style == Exempt {
		return "", nil
	}
	info, err := fs.Lstat(fsys, name)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return "", nil
	case err != nil:
		return "", err
	case !info.Mode().IsRegular():
		return "", nil
	case style == Unknown:
		return unknownProblem, nil
	}
	src, err := fs.ReadFile(fsys, name)
	if err != nil {
		return "", err
	}
	if hasHeader(name, style, src) || (path.Ext(name) == ".go" && generated(src)) {
		return "", nil
	}
	return headers[style].problem, nil
}

// checkClaudeFile returns the problem of CLAUDE.md, or "" when it holds only
// the import of AGENTS.md or does not exist.
func checkClaudeFile(fsys fs.FS) (string, error) {
	src, err := fs.ReadFile(fsys, claudeFile)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return "", nil
	case err != nil:
		return "", err
	case string(src) != claudeContent:
		return claudeProblem, nil
	}
	return "", nil
}

// hasHeader reports whether src, the content of the file at name, starts with
// the SPDX lines of style. A shell script may start with a #! line before
// them.
func hasHeader(name string, style Style, src []byte) bool {
	header := headers[style].text
	if bytes.HasPrefix(src, header) {
		return true
	}
	if path.Ext(name) != ".sh" || !bytes.HasPrefix(src, []byte("#!")) {
		return false
	}
	_, rest, ok := bytes.Cut(src, []byte("\n"))
	return ok && bytes.HasPrefix(rest, header)
}

// generatedLine is the line that marks a generated Go file
// (https://go.dev/s/generatedcode).
var generatedLine = regexp.MustCompile(`^// Code generated .* DO NOT EDIT\.$`)

// generated reports whether the Go source src is generated: it has the
// generated line before the first line that is not blank and not a //
// comment.
func generated(src []byte) bool {
	for line := range bytes.Lines(src) {
		line = bytes.TrimSuffix(line, []byte("\n"))
		switch {
		case generatedLine.Match(line):
			return true
		case len(bytes.TrimSpace(line)) == 0, bytes.HasPrefix(line, []byte("//")):
			continue
		default:
			return false
		}
	}
	return false
}
