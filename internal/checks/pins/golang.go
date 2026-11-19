// SPDX-License-Identifier: Apache-2.0
// SPDX-FileCopyrightText: The Pumi Authors

package pins

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"time"
)

// goPin is one pin of a Go release.
type goPin struct {
	name, file, value string
	// exact is true for a pin of one release, such as a toolchain line, and
	// false for a go line, which names a minor release.
	exact bool
	// older is true for a pin of the older supported release (decision
	// 0085).
	older bool
	// prefix is "go" when the file writes the release as go1.27.4.
	prefix string
}

var (
	baseImageLine = regexp.MustCompile(`(?m)^FROM\s+golang:([0-9][^-@\s]*)`)
	olderLine     = regexp.MustCompile(`(?m)^ENV\s+PUMI_GO_OLDER=(\S*)\s*$`)
)

// readGoPins reads the Go pins from the go.mod files and the Dockerfile.
func readGoPins(read func(string) ([]byte, error)) ([]goPin, error) {
	var pins []goPin
	for _, f := range []struct {
		file  string
		older bool
	}{{CoreModFile, false}, {APIModFile, true}} {
		data, err := read(f.file)
		if err != nil {
			return nil, err
		}
		m, err := ParseModFile(data)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", f.file, err)
		}
		if m.Go == "" || m.Toolchain == "" {
			return nil, fmt.Errorf("%s: want a go line and a toolchain line", f.file)
		}
		pins = append(pins,
			goPin{name: "go line", file: f.file, value: m.Go, older: f.older},
			goPin{name: "toolchain", file: f.file, value: m.Toolchain, exact: true, prefix: "go"})
	}
	data, err := read(Dockerfile)
	if err != nil {
		return nil, err
	}
	image, err := onlyMatch(baseImageLine, data, "FROM golang:<version> line")
	if err != nil {
		return nil, err
	}
	older, err := onlyMatch(olderLine, data, "ENV PUMI_GO_OLDER= line")
	if err != nil {
		return nil, err
	}
	return append(pins,
		goPin{name: "golang base image", file: Dockerfile, value: image, exact: true},
		goPin{name: "PUMI_GO_OLDER", file: Dockerfile, value: older, exact: true, older: true, prefix: "go"}), nil
}

// onlyMatch returns the first group of the one match of re in data.
func onlyMatch(re *regexp.Regexp, data []byte, what string) (string, error) {
	m := re.FindAllSubmatch(data, -1)
	if len(m) != 1 {
		return "", fmt.Errorf("%s: want one %s, got %d", Dockerfile, what, len(m))
	}
	return string(m[0][1]), nil
}

// goReleases is what the proxy reports about the Go releases.
type goReleases struct {
	// minors are the minor releases, as x.y.0, from the oldest to the
	// newest.
	minors []GoVersion
	// newestPatch is the newest patch release of each minor release.
	newestPatch map[GoVersion]GoVersion
	// required is the oldest minor release that the newest pins may use:
	// the newest one whose x.y.1 is at least one month old.
	required GoVersion
	// due is when each minor release newer than required becomes required,
	// for those with an x.y.1.
	due map[GoVersion]time.Time
}

// readGoReleases lists the Go releases and finds the required minor
// release as of now. It asks for the publication time of x.y.1 for each
// minor release from the newest down, until it finds one that is at least
// one month old.
func readGoReleases(ctx context.Context, proxy *Proxy, now time.Time) (*goReleases, error) {
	list, err := proxy.GoReleases(ctx)
	if err != nil {
		return nil, err
	}
	r := &goReleases{newestPatch: map[GoVersion]GoVersion{}, due: map[GoVersion]time.Time{}}
	for _, v := range list {
		m := v.minor()
		if _, ok := r.newestPatch[m]; !ok {
			r.minors = append(r.minors, m)
		}
		r.newestPatch[m] = v
	}
	for i := len(r.minors) - 1; i >= 0; i-- {
		m := r.minors[i]
		if r.newestPatch[m].Patch < 1 {
			continue
		}
		t, err := proxy.GoReleaseTime(ctx, GoVersion{Major: m.Major, Minor: m.Minor, Patch: 1})
		if err != nil {
			return nil, err
		}
		due := t.AddDate(0, 1, 0)
		if !now.Before(due) {
			r.required = m
			return r, nil
		}
		r.due[m] = due
	}
	return nil, fmt.Errorf("no Go release has an x.y.1 that is one month old")
}

// previous returns the minor release before m.
func (r *goReleases) previous(m GoVersion) (GoVersion, error) {
	for i, v := range r.minors {
		if v == m && i > 0 {
			return r.minors[i-1], nil
		}
	}
	return GoVersion{}, fmt.Errorf("no Go minor release before %s", m.minorString())
}

// evaluateGoPins compares each Go pin with the releases.
func evaluateGoPins(ctx context.Context, proxy *Proxy, now time.Time, pins []goPin) ([]Pin, error) {
	r, err := readGoReleases(ctx, proxy, now)
	if err != nil {
		return nil, err
	}
	newest := r.minors[len(r.minors)-1]
	var out []Pin
	for _, p := range pins {
		target, required := newest, r.required
		if p.older {
			if target, err = r.previous(newest); err != nil {
				return nil, err
			}
			if required, err = r.previous(r.required); err != nil {
				return nil, err
			}
		}
		pin, err := r.evaluate(p, target, required)
		if err != nil {
			return nil, fmt.Errorf("%s: %s %s: %w", p.file, p.name, p.value, err)
		}
		out = append(out, pin)
	}
	return out, nil
}

// evaluate compares one Go pin with target, the minor release it should
// use, and required, the oldest minor release it may use now.
func (r *goReleases) evaluate(p goPin, target, required GoVersion) (Pin, error) {
	v, err := parsePinnedGo(p)
	if err != nil {
		return Pin{}, err
	}
	own, ok := r.newestPatch[v.minor()]
	if p.exact && (!ok || v.Compare(own) > 0) {
		return Pin{}, fmt.Errorf("the proxy lists no such Go release")
	}
	pin := Pin{Name: p.name, File: p.file, Current: p.value}
	behind := v.minor().Compare(target) < 0
	switch {
	case v.minor().Compare(required) < 0:
		pin.OutOfDate = true
	case p.exact && v.Patch < own.Patch:
		// The newest patch release of the pin's own minor release is due
		// now; a move to target is not.
		pin.Newest, pin.OutOfDate = p.prefix+own.String(), true
		return pin, nil
	case !behind:
		return pin, nil
	}
	if p.exact {
		pin.Newest = p.prefix + r.newestPatch[target].String()
	} else {
		pin.Newest = target.String()
	}
	if !pin.OutOfDate {
		pin.Due = r.dueFor(v.minor(), p.older)
	}
	return pin, nil
}

// dueFor returns when a pin on the minor release m becomes out of date: when
// the minor release after m becomes required, or, for a pin of the older
// release, the one after that. It is zero when that date is not known yet.
func (r *goReleases) dueFor(m GoVersion, older bool) time.Time {
	steps := 1
	if older {
		steps = 2
	}
	for i, v := range r.minors {
		if v == m && i+steps < len(r.minors) {
			return r.due[r.minors[i+steps]]
		}
	}
	return time.Time{}
}

// parsePinnedGo parses the release of a pin. A pin of one release must
// name its patch release.
func parsePinnedGo(p goPin) (GoVersion, error) {
	s, ok := strings.CutPrefix(p.value, p.prefix)
	if !ok {
		return GoVersion{}, fmt.Errorf("want a release written as %s1.2.3", p.prefix)
	}
	v, err := ParseGoVersion(s)
	if err != nil {
		return GoVersion{}, err
	}
	if p.exact && strings.Count(s, ".") != 2 {
		return GoVersion{}, fmt.Errorf("want a patch release such as %s1.2.3", p.prefix)
	}
	return v, nil
}
