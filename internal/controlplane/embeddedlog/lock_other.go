// SPDX-License-Identifier: Apache-2.0
// SPDX-FileCopyrightText: The Pumi Authors

//go:build !(darwin || dragonfly || freebsd || linux || netbsd || openbsd || windows)

package embeddedlog

import (
	"errors"
	"os"
	"runtime"
)

// errLocked is the error of lockFile when another open file holds the
// lock.
var errLocked = errors.New("the file is locked")

// lockFile fails: the embedded log has no file lock on this operating
// system, so it does not open, rather than open without one.
func lockFile(*os.File) error {
	return errors.New("the embedded event log has no file lock on " + runtime.GOOS)
}

// unlockFile does nothing, because lockFile never takes a lock.
func unlockFile(*os.File) error {
	return nil
}

// syncDir does nothing, because no log opens here.
func syncDir(*os.Root) error {
	return nil
}
