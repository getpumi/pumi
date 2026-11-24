// SPDX-License-Identifier: Apache-2.0
// SPDX-FileCopyrightText: The Pumi Authors

//go:build !(darwin || dragonfly || freebsd || linux || netbsd || openbsd || windows)

package blobstore

import (
	"errors"
	"os"
	"runtime"
)

// syncDir fails: the local blob store does not know how to sync a
// directory on this operating system, so Put fails rather than store a
// blob that a crash could lose, as the embedded log does not open here.
func syncDir(*os.Root, string) error {
	return errors.New("blobstore: no directory sync on " + runtime.GOOS)
}
