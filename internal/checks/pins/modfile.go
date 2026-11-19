// SPDX-License-Identifier: Apache-2.0
// SPDX-FileCopyrightText: The Pumi Authors

package pins

import (
	"bufio"
	"bytes"
	"fmt"
	"strconv"
	"strings"
)

// Module is a module path and version from a require line.
type Module struct {
	Path    string
	Version string
}

// ModFile is what the pin report reads from a go.mod file or a tool module
// file: the go and toolchain lines, the tool lines and the require lines.
type ModFile struct {
	Go        string
	Toolchain string
	Tools     []string
	Requires  []Module
}

// ParseModFile reads the go, toolchain, tool and require lines of a module
// file, in their single-line and block forms, and ignores the other lines.
// The check uses only the standard library (decision 0064), so it does not
// use golang.org/x/mod.
func ParseModFile(data []byte) (*ModFile, error) {
	m := &ModFile{}
	block := ""
	sc := bufio.NewScanner(bytes.NewReader(data))
	for n := 1; sc.Scan(); n++ {
		fields, err := lineFields(sc.Text())
		if err != nil {
			return nil, fmt.Errorf("line %d: %w", n, err)
		}
		switch {
		case len(fields) == 0:
			continue
		case block != "" && len(fields) == 1 && fields[0] == ")":
			block = ""
			continue
		case block != "":
			err = m.add(block, fields)
		case len(fields) == 2 && fields[1] == "(":
			block = fields[0]
			continue
		default:
			err = m.add(fields[0], fields[1:])
		}
		if err != nil {
			return nil, fmt.Errorf("line %d: %w", n, err)
		}
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	if block != "" {
		return nil, fmt.Errorf("the %s block has no closing parenthesis", block)
	}
	return m, nil
}

// lineFields splits a line into its fields, without a // comment, and
// removes the quotes around a quoted field.
func lineFields(line string) ([]string, error) {
	if i := strings.Index(line, "//"); i >= 0 {
		line = line[:i]
	}
	fields := strings.Fields(line)
	for i, f := range fields {
		if strings.HasPrefix(f, `"`) {
			u, err := strconv.Unquote(f)
			if err != nil {
				return nil, fmt.Errorf("bad quoted string %s", f)
			}
			fields[i] = u
		}
	}
	return fields, nil
}

// add records the arguments of one directive.
func (m *ModFile) add(verb string, args []string) error {
	switch verb {
	case "go", "toolchain", "tool":
		if len(args) != 1 {
			return fmt.Errorf("%s takes one argument, got %q", verb, args)
		}
	case "require":
		if len(args) != 2 {
			return fmt.Errorf("require takes a module path and a version, got %q", args)
		}
	}
	switch verb {
	case "go":
		m.Go = args[0]
	case "toolchain":
		m.Toolchain = args[0]
	case "tool":
		m.Tools = append(m.Tools, args[0])
	case "require":
		m.Requires = append(m.Requires, Module{Path: args[0], Version: args[1]})
	}
	return nil
}
