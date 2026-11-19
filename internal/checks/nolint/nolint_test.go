// SPDX-License-Identifier: Apache-2.0
// SPDX-FileCopyrightText: The Pumi Authors

package nolint

import (
	"reflect"
	"testing"
	"testing/fstest"
)

func file(src string) *fstest.MapFile {
	return &fstest.MapFile{Data: []byte(src)}
}

func TestCheckFindsDirectives(t *testing.T) {
	tests := []struct {
		name string
		src  string
		want []Finding
	}{
		{
			name: "bare directive",
			src:  "package p\n\nvar x = 1 //nolint\n",
			want: []Finding{{Path: "p.go", Line: 3, Column: 11, Text: "//nolint"}},
		},
		{
			name: "directive with linters and a reason",
			src:  "package p\n\n//nolint:errcheck,gosec // the reason\nfunc f() {}\n",
			want: []Finding{{Path: "p.go", Line: 3, Column: 1, Text: "//nolint:errcheck,gosec // the reason"}},
		},
		{
			name: "space after the slashes",
			src:  "package p\n\n// nolint:errcheck\nfunc f() {}\n",
			want: []Finding{{Path: "p.go", Line: 3, Column: 1, Text: "// nolint:errcheck"}},
		},
		{
			name: "prose that golangci-lint reads as a directive",
			src:  "package p\n\n// nolint comments are rejected\nfunc f() {}\n",
			want: []Finding{{Path: "p.go", Line: 3, Column: 1, Text: "// nolint comments are rejected"}},
		},
		{
			name: "second line of a comment group",
			src:  "package p\n\n// f does nothing.\n//nolint:unused\nfunc f() {}\n",
			want: []Finding{{Path: "p.go", Line: 4, Column: 1, Text: "//nolint:unused"}},
		},
		{
			name: "after a line directive, which does not move the position",
			src:  "package p\n\n//line other.y:100\nvar x = 1 //nolint\n",
			want: []Finding{{Path: "p.go", Line: 4, Column: 11, Text: "//nolint"}},
		},
		{
			name: "two directives",
			src:  "package p\n\nvar a = 1 //nolint\nvar b = 2 //nolint:gosec\n",
			want: []Finding{
				{Path: "p.go", Line: 3, Column: 11, Text: "//nolint"},
				{Path: "p.go", Line: 4, Column: 11, Text: "//nolint:gosec"},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Check(fstest.MapFS{"p.go": file(tt.src)})
			if err != nil {
				t.Fatalf("Check() error = %v", err)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("Check() = %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestCheckIgnoresNonDirectives(t *testing.T) {
	tests := []struct {
		name string
		src  string
	}{
		{"no comments", "package p\n\nfunc f() {}\n"},
		{"ordinary comment", "package p\n\n// f does nothing.\nfunc f() {}\n"},
		{"word that only starts with nolint", "package p\n\n//nolintlint is a linter\nfunc f() {}\n"},
		{"nolint later in the comment", "package p\n\n// see //nolint\nfunc f() {}\n"},
		{"block comment, which golangci-lint does not read", "package p\n\n/* nolint */\nfunc f() {}\n"},
		{"string literal", "package p\n\nvar s = \"//nolint\"\n"},
		{"raw string literal", "package p\n\nvar s = `\n//nolint\n`\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Check(fstest.MapFS{"p.go": file(tt.src)})
			if err != nil {
				t.Fatalf("Check() error = %v", err)
			}
			if len(got) != 0 {
				t.Errorf("Check() = %+v, want no findings", got)
			}
		})
	}
}

func TestCheckWalksTheTree(t *testing.T) {
	const directive = "package p\n\nvar x = 1 //nolint\n"
	fsys := fstest.MapFS{
		"b.go":                     file(directive),
		"a/deep/c.go":              file(directive),
		"a/c_test.go":              file(directive),
		"api/d.go":                 file(directive),
		"testdata/fixture/e.go":    file(directive),
		"notes.md":                 file("//nolint\n"),
		"script.sh":                file("//nolint\n"),
		".git/hooks/x.go":          file(directive),
		".devcontainer/ignored.go": file(directive),
	}
	got, err := Check(fsys)
	if err != nil {
		t.Fatalf("Check() error = %v", err)
	}
	var paths []string
	for _, f := range got {
		paths = append(paths, f.Path)
	}
	want := []string{"a/c_test.go", "a/deep/c.go", "api/d.go", "b.go", "testdata/fixture/e.go"}
	if !reflect.DeepEqual(paths, want) {
		t.Errorf("Check() found directives in %q, want %q", paths, want)
	}
}

func TestCheckReadsFilesThatDoNotParse(t *testing.T) {
	got, err := Check(fstest.MapFS{"p.go": file("package p\n\nfunc f( { //nolint\n")})
	if err != nil {
		t.Fatalf("Check() error = %v", err)
	}
	want := []Finding{{Path: "p.go", Line: 3, Column: 11, Text: "//nolint"}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Check() = %+v, want %+v", got, want)
	}
}

func TestFindingString(t *testing.T) {
	f := Finding{Path: "a/b.go", Line: 3, Column: 11, Text: "//nolint:gosec"}
	want := "a/b.go:3:11: //nolint:gosec: suppress a finding with an exclusion rule in .golangci.yml, not a nolint comment (decision 0110)"
	if got := f.String(); got != want {
		t.Errorf("String() = %q, want %q", got, want)
	}
}
