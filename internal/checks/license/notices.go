// SPDX-License-Identifier: Apache-2.0
// SPDX-FileCopyrightText: The Pumi Authors

package license

import (
	"fmt"
	"io/fs"
	"os"
	"path"
	"slices"
	"strings"
	"unicode"
)

// noticePrefixes are the starts of the names, in upper case, of the files at
// the root of a module that hold its license text or its copyright and other
// notices, such as LICENSE, LICENSE.md, LICENSE-MIT, COPYING, NOTICE and the
// PATENTS file of the Go project.
var noticePrefixes = []string{"LICENSE", "LICENCE", "COPYING", "COPYRIGHT", "NOTICE", "PATENTS"}

// noticeExtensions are the extensions, in upper case, of the files that
// noticePrefixes name: none, or a text format. Source files such as
// license.go are left out.
var noticeExtensions = []string{"", ".MD", ".TXT", ".RST"}

// mplLicense is the license whose modules' notices say where the source is
// (decision 0079).
const mplLicense = "MPL-2.0"

// Notices returns THIRD_PARTY_NOTICES for the checked modules (decision
// 0079): for each module, its path and version, its licenses, for an MPL-2.0
// module where its source is, and the text of each license and notice file at
// the root of its directory, which holds the license text and the copyright
// notices. It fails for a module with no such file.
func Notices(checked []Checked) ([]byte, error) {
	var b strings.Builder
	b.WriteString("THIRD_PARTY_NOTICES\n\n" +
		"The Pumi binaries include the following software, each under its own license.\n" +
		"make notices writes this file from the modules that the license check checked (decision 0079).\n")
	for _, c := range checked {
		if err := writeNotice(&b, c); err != nil {
			return nil, err
		}
	}
	return []byte(b.String()), nil
}

// writeNotice writes the notice of one module to b.
func writeNotice(b *strings.Builder, c Checked) error {
	dir := os.DirFS(c.Module.Dir)
	files, err := noticeFiles(dir)
	if err != nil {
		return fmt.Errorf("%s %s: %w", c.Module.Path, c.Module.Version, err)
	}
	if len(files) == 0 {
		return fmt.Errorf("%s %s: no license or notice file in %s", c.Module.Path, c.Module.Version, c.Module.Dir)
	}
	b.WriteString("\n" + strings.Repeat("=", 80) + "\n")
	if c.Module.Path == StdPath {
		b.WriteString("The Go standard library " + c.Module.Version + "\n")
	} else {
		b.WriteString(c.Module.Path + " " + c.Module.Version + "\n")
	}
	b.WriteString("License: " + strings.Join(c.Licenses, ", ") + "\n")
	if slices.Contains(c.Licenses, mplLicense) {
		b.WriteString("Source: https://proxy.golang.org/" + escape(c.Module.Path) + "/@v/" + escape(c.Module.Version) + ".zip\n")
	}
	for _, name := range files {
		data, err := fs.ReadFile(dir, name)
		if err != nil {
			return err
		}
		text := string(data)
		if !strings.HasSuffix(text, "\n") {
			text += "\n"
		}
		b.WriteString("\n----- " + name + " -----\n\n" + text)
	}
	return nil
}

// noticeFiles returns the sorted names of the regular files at the root of
// dir whose names start with one of noticePrefixes, in any case.
func noticeFiles(dir fs.FS) ([]string, error) {
	entries, err := fs.ReadDir(dir, ".")
	if err != nil {
		return nil, err
	}
	var names []string
	for _, e := range entries {
		upper := strings.ToUpper(e.Name())
		if e.Type().IsRegular() && slices.Contains(noticeExtensions, path.Ext(upper)) &&
			slices.ContainsFunc(noticePrefixes, func(p string) bool { return strings.HasPrefix(upper, p) }) {
			names = append(names, e.Name())
		}
	}
	slices.Sort(names)
	return names, nil
}

// escape escapes a module path or version as the module proxy protocol
// does: each upper-case letter becomes an exclamation mark and the letter in
// lower case.
func escape(s string) string {
	var b strings.Builder
	for _, r := range s {
		if unicode.IsUpper(r) {
			b.WriteByte('!')
			r = unicode.ToLower(r)
		}
		b.WriteRune(r)
	}
	return b.String()
}
