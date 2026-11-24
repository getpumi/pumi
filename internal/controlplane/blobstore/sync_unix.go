// SPDX-License-Identifier: Apache-2.0
// SPDX-FileCopyrightText: The Pumi Authors

//go:build darwin || dragonfly || freebsd || linux || netbsd || openbsd

package blobstore

import "os"

// syncDir syncs the directory dir of root, so that a rename in it
// survives a crash.
func syncDir(root *os.Root, dir string) error {
	d, err := root.Open(dir)
	if err != nil {
		return err
	}
	if err := d.Sync(); err != nil {
		_ = d.Close()
		return err
	}
	return d.Close()
}
