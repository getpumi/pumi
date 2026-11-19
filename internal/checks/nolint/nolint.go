// SPDX-License-Identifier: Apache-2.0
// SPDX-FileCopyrightText: The Pumi Authors

// Package nolint is the check that fails every //nolint comment in Go files.
// In Pumi's own repository a lint finding is suppressed only by an exclusion
// rule in .golangci.yml, so that every suppression is in one reviewed file
// (decision 0110).
package nolint

import (
	"fmt"
	"go/scanner"
	"go/token"
	"io/fs"
	"path"
	"regexp"
	"strings"
)

// directive is the pattern with which golangci-lint 2.15.0 recognizes a nolint
// directive, after it trims every leading slash and space from the comment
// (pkg/result/processors/nolint_filter.go). The check matches the same
// comments, so it fails every comment that golangci-lint would obey.
var directive = regexp.MustCompile(`^nolint( |:|$)`)

// Finding is one comment that golangci-lint would read as a nolint directive.
type Finding struct {
	Path   string
	Line   int
	Column int
	Text   string
}

// String formats the finding as path:line:column: text: message.
func (f Finding) String() string {
	return fmt.Sprintf("%s:%d:%d: %s: suppress a finding with an exclusion rule in .golangci.yml, not a nolint comment (decision 0110)",
		f.Path, f.Line, f.Column, f.Text)
}

// Check returns the nolint directives in the .go files of fsys, in the order
// of fs.WalkDir. It skips directories whose names start with a dot, such as
// .git. It reads testdata and files that do not parse, because golangci-lint
// can be run on them too.
func Check(fsys fs.FS) ([]Finding, error) {
	var findings []Finding
	err := fs.WalkDir(fsys, ".", func(name string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if name != "." && strings.HasPrefix(d.Name(), ".") {
				return fs.SkipDir
			}
			return nil
		}
		if path.Ext(name) != ".go" {
			return nil
		}
		src, err := fs.ReadFile(fsys, name)
		if err != nil {
			return err
		}
		findings = append(findings, checkFile(name, src)...)
		return nil
	})
	return findings, err
}

// checkFile returns the nolint directives in one Go source file, at their
// positions in that file, not those that a //line directive sets. The scanner
// reads comments without parsing the file, so a syntax error does not hide a
// directive, and a "//nolint" inside a string literal is not a comment.
func checkFile(name string, src []byte) []Finding {
	fset := token.NewFileSet()
	file := fset.AddFile(name, -1, len(src))
	var s scanner.Scanner
	s.Init(file, src, nil, scanner.ScanComments)
	var findings []Finding
	for {
		pos, tok, lit := s.Scan()
		if tok == token.EOF {
			return findings
		}
		if tok != token.COMMENT || !directive.MatchString(strings.TrimLeft(lit, "/ ")) {
			continue
		}
		p := fset.PositionFor(pos, false)
		findings = append(findings, Finding{Path: name, Line: p.Line, Column: p.Column, Text: lit})
	}
}
