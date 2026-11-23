// SPDX-License-Identifier: Apache-2.0
// SPDX-FileCopyrightText: The Pumi Authors

package runner

import (
	"fmt"
	"path"
	"slices"
	"strings"
)

// allowed reports whether the path p, relative to the workspace and
// separated by slashes, matches one of the globs (decision 0159). A glob
// is relative to the workspace too. Each of its segments matches one
// segment of p as path.Match does, and a segment "**" matches any number
// of segments, none included, except that a last "**" matches one or more.
func allowed(p string, globs []string) (bool, error) {
	for _, glob := range globs {
		if err := checkGlob(glob); err != nil {
			return false, err
		}
		ok, err := match(strings.Split(glob, "/"), strings.Split(p, "/"))
		if err != nil {
			return false, fmt.Errorf("the write path %q: %w", glob, err)
		}
		if ok {
			return true, nil
		}
	}
	return false, nil
}

// checkGlob refuses a glob that is empty, absolute or that has an empty
// segment or a segment "." or "..", because it would not name paths in
// the workspace, and a glob that path.Match cannot read.
func checkGlob(glob string) error {
	if glob == "" || strings.HasPrefix(glob, "/") {
		return fmt.Errorf("the write path %q is not relative to the workspace", glob)
	}
	if slices.ContainsFunc(strings.Split(glob, "/"), func(s string) bool { return s == "." || s == ".." || s == "" }) {
		return fmt.Errorf("the write path %q has an empty, . or .. segment", glob)
	}
	for _, segment := range strings.Split(glob, "/") {
		if _, err := path.Match(segment, ""); err != nil {
			return fmt.Errorf("the write path %q: %w", glob, err)
		}
	}
	return nil
}

// match reports whether the segments of a glob match the segments of a
// path.
func match(glob, p []string) (bool, error) {
	if len(glob) == 0 {
		return len(p) == 0, nil
	}
	if glob[0] == "**" && len(glob) == 1 {
		// A last "**" matches the paths below a directory, not the
		// directory itself.
		return len(p) > 0, nil
	}
	if glob[0] == "**" {
		for i := 0; i <= len(p); i++ {
			ok, err := match(glob[1:], p[i:])
			if err != nil || ok {
				return ok, err
			}
		}
		return false, nil
	}
	if len(p) == 0 {
		return false, nil
	}
	ok, err := path.Match(glob[0], p[0])
	if err != nil || !ok {
		return false, err
	}
	return match(glob[1:], p[1:])
}

// outside returns the paths that none of the globs allows, in their order.
func outside(paths, globs []string) ([]string, error) {
	out := []string{}
	for _, p := range paths {
		ok, err := allowed(p, globs)
		if err != nil {
			return nil, err
		}
		if !ok {
			out = append(out, p)
		}
	}
	return out, nil
}
