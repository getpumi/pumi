// SPDX-License-Identifier: Apache-2.0
// SPDX-FileCopyrightText: The Pumi Authors

//go:build windows

package blobstore

import "os"

// syncDir does nothing on Windows, where a directory cannot be opened for
// a sync through the os package; MoveFileEx, which os.Rename calls, moves
// the blob into place in one step.
func syncDir(*os.Root, string) error {
	return nil
}
