// SPDX-License-Identifier: Apache-2.0
// SPDX-FileCopyrightText: The Pumi Authors

package pins

import (
	"slices"
	"testing"
)

func TestParseModFile(t *testing.T) {
	src := `// SPDX-License-Identifier: Apache-2.0

module getpumi.dev/pumi/tools/buf

go 1.27.0

toolchain go1.27.4

tool github.com/bufbuild/buf/cmd/buf

tool (
	github.com/bufbuild/buf/cmd/protoc-gen-buf-lint // a comment
)

require github.com/bufbuild/buf v1.74.0

require (
	// A comment line.
	"github.com/spf13/cobra" v1.10.1 // indirect
	golang.org/x/mod v0.30.0 // indirect
)
`
	m, err := ParseModFile([]byte(src))
	if err != nil {
		t.Fatal(err)
	}
	if m.Go != "1.27.0" || m.Toolchain != "go1.27.4" {
		t.Errorf("Go, Toolchain = %q, %q; want 1.27.0, go1.27.4", m.Go, m.Toolchain)
	}
	wantTools := []string{"github.com/bufbuild/buf/cmd/buf", "github.com/bufbuild/buf/cmd/protoc-gen-buf-lint"}
	if !slices.Equal(m.Tools, wantTools) {
		t.Errorf("Tools = %q, want %q", m.Tools, wantTools)
	}
	wantReq := []Module{
		{"github.com/bufbuild/buf", "v1.74.0"},
		{"github.com/spf13/cobra", "v1.10.1"},
		{"golang.org/x/mod", "v0.30.0"},
	}
	if !slices.Equal(m.Requires, wantReq) {
		t.Errorf("Requires = %v, want %v", m.Requires, wantReq)
	}
}

func TestParseModFileUnclosedBlock(t *testing.T) {
	if _, err := ParseModFile([]byte("module x\n\nrequire (\n\ta.b/c v1.0.0\n")); err == nil {
		t.Error("ParseModFile accepted a block without a closing parenthesis")
	}
}

func TestParseModFileBadRequire(t *testing.T) {
	if _, err := ParseModFile([]byte("module x\n\nrequire a.b/c\n")); err == nil {
		t.Error("ParseModFile accepted a require line without a version")
	}
}

func TestCompareSemver(t *testing.T) {
	ordered := []string{"v0.9.0", "v1.0.0-rc.1", "v1.0.0", "v1.2.0", "v1.10.0", "v2.0.0+incompatible", "v2.0.1"}
	for i, a := range ordered {
		for j, b := range ordered {
			got := compareSemver(a, b)
			want := 0
			if i < j {
				want = -1
			} else if i > j {
				want = 1
			}
			if got != want {
				t.Errorf("compareSemver(%s, %s) = %d, want %d", a, b, got, want)
			}
		}
	}
}

func TestParseGoVersion(t *testing.T) {
	for in, want := range map[string]GoVersion{
		"go1.27.4": {1, 27, 4},
		"1.27.0":   {1, 27, 0},
		"1.20":     {1, 20, 0},
		"go1.26":   {1, 26, 0},
	} {
		if got, err := ParseGoVersion(in); err != nil || got != want {
			t.Errorf("ParseGoVersion(%q) = %v, %v; want %v", in, got, err, want)
		}
	}
	for _, in := range []string{"", "1.27rc1", "go1", "1.x.2", "1.27.4.1"} {
		if _, err := ParseGoVersion(in); err == nil {
			t.Errorf("ParseGoVersion(%q) returned no error", in)
		}
	}
}
