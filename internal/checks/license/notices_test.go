// SPDX-License-Identifier: Apache-2.0
// SPDX-FileCopyrightText: The Pumi Authors

package license

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestNotices(t *testing.T) {
	stdDir, uuidDir, mplDir := t.TempDir(), t.TempDir(), t.TempDir()
	writeFiles(t, stdDir, map[string]string{"LICENSE": "Copyright 2009 The Go Authors.\n", "PATENTS": "Additional IP Rights Grant (Patents)\n", "README.md": "not a notice\n"})
	writeFiles(t, uuidDir, map[string]string{"LICENSE": "Copyright (c) 2009,2014 Google Inc.\n", "uuid.go": "package uuid\n", "license.go": "package uuid\n", "NOTICE.json": "{}"})
	writeFiles(t, mplDir, map[string]string{"LICENSE.md": "Mozilla Public License Version 2.0\n", "NOTICE.txt": "Copyright HashiCorp\n"})
	if err := os.Mkdir(filepath.Join(mplDir, "LICENSES"), 0o750); err != nil {
		t.Fatal(err)
	}
	checked := []Checked{
		{Module: Module{Path: StdPath, Version: "go1.27.4", Dir: stdDir}, Licenses: []string{"BSD-3-Clause"}},
		{Module: Module{Path: "github.com/google/uuid", Version: "v1.6.0", Dir: uuidDir}, Licenses: []string{"BSD-3-Clause"}},
		{Module: Module{Path: "github.com/HashiCorp/go-version", Version: "v1.9.0", Dir: mplDir}, Licenses: []string{"MPL-2.0"}, Exception: &mplException},
	}
	got, err := Notices(checked)
	if err != nil {
		t.Fatal(err)
	}
	rule := strings.Repeat("=", 80)
	want := "THIRD_PARTY_NOTICES\n\n" +
		"The Pumi binaries include the following software, each under its own license.\n" +
		"make notices writes this file from the modules that the license check checked (decision 0079).\n" +
		"\n" + rule + "\n" +
		"The Go standard library go1.27.4\n" +
		"License: BSD-3-Clause\n" +
		"\n----- LICENSE -----\n\nCopyright 2009 The Go Authors.\n" +
		"\n----- PATENTS -----\n\nAdditional IP Rights Grant (Patents)\n" +
		"\n" + rule + "\n" +
		"github.com/google/uuid v1.6.0\n" +
		"License: BSD-3-Clause\n" +
		"\n----- LICENSE -----\n\nCopyright (c) 2009,2014 Google Inc.\n" +
		"\n" + rule + "\n" +
		"github.com/HashiCorp/go-version v1.9.0\n" +
		"License: MPL-2.0\n" +
		"Source: https://proxy.golang.org/github.com/!hashi!corp/go-version/@v/v1.9.0.zip\n" +
		"\n----- LICENSE.md -----\n\nMozilla Public License Version 2.0\n" +
		"\n----- NOTICE.txt -----\n\nCopyright HashiCorp\n"
	if string(got) != want {
		t.Errorf("Notices() =\n%s\nwant\n%s", got, want)
	}
}

func TestNoticesAddsAMissingFinalNewline(t *testing.T) {
	dir := t.TempDir()
	writeFiles(t, dir, map[string]string{"COPYING": "text without a newline"})
	got, err := Notices([]Checked{{Module: Module{Path: "example.com/m", Version: "v1.0.0", Dir: dir}, Licenses: []string{"MIT"}}})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(string(got), "\n----- COPYING -----\n\ntext without a newline\n") {
		t.Errorf("Notices() = %q, want it to end with the text and a newline", got)
	}
}

func TestNoticesFailsWithoutALicenseFile(t *testing.T) {
	dir := t.TempDir()
	writeFiles(t, dir, map[string]string{"m.go": "package m\n"})
	_, err := Notices([]Checked{{Module: Module{Path: "example.com/m", Version: "v1.0.0", Dir: dir}, Licenses: []string{"MIT"}}})
	if err == nil || !strings.Contains(err.Error(), "example.com/m v1.0.0: no license or notice file in") {
		t.Errorf("Notices() error = %v, want one about the missing file", err)
	}
}
