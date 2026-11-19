// SPDX-License-Identifier: Apache-2.0
// SPDX-FileCopyrightText: The Pumi Authors

package pins

import (
	"cmp"
	"fmt"
	"strconv"
	"strings"
)

// GoVersion is a Go release. A release written without a patch number, such
// as go1.20, has the patch number 0.
type GoVersion struct {
	Major int
	Minor int
	Patch int
}

// ParseGoVersion parses a Go release, with or without the "go" prefix, such
// as go1.27.4, 1.27.0 or 1.20. It rejects release candidates and betas.
func ParseGoVersion(s string) (GoVersion, error) {
	parts := strings.Split(strings.TrimPrefix(s, "go"), ".")
	if len(parts) < 2 || len(parts) > 3 {
		return GoVersion{}, fmt.Errorf("%q is not a Go release", s)
	}
	var n [3]int
	for i, p := range parts {
		v, err := strconv.Atoi(p)
		if err != nil || v < 0 || strconv.Itoa(v) != p {
			return GoVersion{}, fmt.Errorf("%q is not a Go release", s)
		}
		n[i] = v
	}
	return GoVersion{Major: n[0], Minor: n[1], Patch: n[2]}, nil
}

// Compare returns -1, 0 or 1 as v is older than, the same as or newer than w.
func (v GoVersion) Compare(w GoVersion) int {
	if c := cmp.Compare(v.Major, w.Major); c != 0 {
		return c
	}
	if c := cmp.Compare(v.Minor, w.Minor); c != 0 {
		return c
	}
	return cmp.Compare(v.Patch, w.Patch)
}

// minor returns the release x.y.0 of v's minor release x.y.
func (v GoVersion) minor() GoVersion {
	return GoVersion{Major: v.Major, Minor: v.Minor}
}

// String formats v as 1.27.4.
func (v GoVersion) String() string {
	return fmt.Sprintf("%d.%d.%d", v.Major, v.Minor, v.Patch)
}

// minorString formats v's minor release as 1.27.
func (v GoVersion) minorString() string {
	return fmt.Sprintf("%d.%d", v.Major, v.Minor)
}

// compareSemver compares two module versions, such as v1.2.3 or v1.0.0-rc.1,
// in semantic version order. Build metadata, such as +incompatible, is
// ignored, and a prerelease is older than its release. Prerelease identifiers
// are compared as strings, which is enough to order a release after its
// prereleases. An invalid version is older than any valid one.
func compareSemver(a, b string) int {
	va, okA := parseSemver(a)
	vb, okB := parseSemver(b)
	switch {
	case !okA || !okB:
		return cmp.Compare(btoi(okA), btoi(okB))
	}
	for i := range 3 {
		if c := cmp.Compare(va.n[i], vb.n[i]); c != 0 {
			return c
		}
	}
	switch {
	case va.pre == vb.pre:
		return 0
	case va.pre == "":
		return 1
	case vb.pre == "":
		return -1
	}
	return cmp.Compare(va.pre, vb.pre)
}

type semver struct {
	n   [3]int
	pre string
}

func parseSemver(s string) (semver, bool) {
	s, ok := strings.CutPrefix(s, "v")
	if !ok {
		return semver{}, false
	}
	if i := strings.Index(s, "+"); i >= 0 {
		s = s[:i]
	}
	var v semver
	if i := strings.Index(s, "-"); i >= 0 {
		s, v.pre = s[:i], s[i+1:]
	}
	parts := strings.Split(s, ".")
	if len(parts) != 3 {
		return semver{}, false
	}
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 {
			return semver{}, false
		}
		v.n[i] = n
	}
	return v, true
}

func btoi(b bool) int {
	if b {
		return 1
	}
	return 0
}

// semverMajor returns the major version of a module version, such as 2 for
// v2.15.0.
func semverMajor(s string) (int, bool) {
	v, ok := parseSemver(s)
	return v.n[0], ok
}

// isPrerelease reports whether a module version is a prerelease, such as
// v3.0.0-beta.1. The proxy answers @latest with a prerelease when a module
// has no release yet.
func isPrerelease(s string) bool {
	v, ok := parseSemver(s)
	return ok && v.pre != ""
}
