// SPDX-License-Identifier: Apache-2.0
// SPDX-FileCopyrightText: The Pumi Authors

package shared

import (
	"fmt"
	"io"
	"runtime/debug"
)

// develVersion is the version reported when the build carries no module
// version, as in `go run` or a build outside a git checkout.
const develVersion = "(devel)"

// Version returns the version of the main module that the Go toolchain
// recorded in the binary. `go build` in a git checkout records the tag or a
// pseudo-version for the commit, with "+dirty" when the tree has changes.
func Version() string {
	info, ok := debug.ReadBuildInfo()
	return versionFrom(info, ok)
}

func versionFrom(info *debug.BuildInfo, ok bool) string {
	if !ok || info == nil || info.Main.Version == "" {
		return develVersion
	}
	return info.Main.Version
}

// WriteVersion writes one line with the binary's name and its version.
func WriteVersion(w io.Writer, binary string) error {
	_, err := fmt.Fprintf(w, "%s %s\n", binary, Version())
	return err
}
