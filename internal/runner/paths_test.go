// SPDX-License-Identifier: Apache-2.0
// SPDX-FileCopyrightText: The Pumi Authors

package runner

import (
	"slices"
	"testing"
)

func TestAllowed(t *testing.T) {
	for _, c := range []struct {
		glob string
		yes  []string
		no   []string
	}{
		{"internal/runner/**",
			[]string{"internal/runner/run.go", "internal/runner/testdata/a/b.txt"},
			[]string{"internal/runner", "internal/runnerx/a.go", "internal/kernel/run.go", "README.md"}},
		{"**",
			[]string{"README.md", "a/b/c"}, nil},
		{"**/*.go",
			[]string{"run.go", "internal/runner/run.go"},
			[]string{"go.mod", "internal/runner/doc.txt"}},
		{"docs/*.md",
			[]string{"docs/a.md"},
			[]string{"docs/a/b.md", "docs/a.txt", "a.md"}},
		{"api/**/gen/*",
			[]string{"api/gen/x.go", "api/a/b/gen/x.go"},
			[]string{"api/gen/a/x.go"}},
		{"README.md",
			[]string{"README.md"},
			[]string{"docs/README.md", "README.mdx"}},
	} {
		for _, p := range c.yes {
			if ok, err := allowed(p, []string{c.glob}); err != nil || !ok {
				t.Errorf("allowed(%q, %q) = %v, %v; want true", p, c.glob, ok, err)
			}
		}
		for _, p := range c.no {
			if ok, err := allowed(p, []string{c.glob}); err != nil || ok {
				t.Errorf("allowed(%q, %q) = %v, %v; want false", p, c.glob, ok, err)
			}
		}
	}
}

func TestAllowedByAnyGlob(t *testing.T) {
	globs := []string{"a/**", "b/*.go"}
	for p, want := range map[string]bool{"a/x": true, "b/x.go": true, "c/x.go": false} {
		if ok, err := allowed(p, globs); err != nil || ok != want {
			t.Errorf("allowed(%q, %q) = %v, %v; want %v", p, globs, ok, err, want)
		}
	}
}

func TestNoGlobAllowsNothing(t *testing.T) {
	if ok, err := allowed("README.md", nil); err != nil || ok {
		t.Errorf("allowed with no globs = %v, %v; want false", ok, err)
	}
}

func TestABadGlobIsAnError(t *testing.T) {
	for _, glob := range []string{"a/[", "/abs/**", "a/../b", ""} {
		if _, err := allowed("a/b", []string{glob}); err == nil {
			t.Errorf("allowed with the glob %q gives no error", glob)
		}
	}
}

func TestOutside(t *testing.T) {
	got, err := outside([]string{"a/x", "README.md", "b/y"}, []string{"a/**"})
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"README.md", "b/y"}; !slices.Equal(got, want) {
		t.Errorf("outside = %q, want %q", got, want)
	}
}
