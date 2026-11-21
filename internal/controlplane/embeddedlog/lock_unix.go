// SPDX-License-Identifier: Apache-2.0
// SPDX-FileCopyrightText: The Pumi Authors

//go:build darwin || dragonfly || freebsd || linux || netbsd || openbsd

package embeddedlog

import (
	"errors"
	"os"
	"syscall"
)

// errLocked is the error of lockFile when another open file holds the
// lock.
var errLocked = errors.New("the file is locked")

// lockFile takes an exclusive lock on f without waiting, with flock(2). The
// lock belongs to the open file, so a second open of the same file fails
// to take it, in this process or another, and the operating system drops
// it when the process ends.
func lockFile(f *os.File) error {
	err := withFd(f, func(fd int) error { return syscall.Flock(fd, syscall.LOCK_EX|syscall.LOCK_NB) })
	if errors.Is(err, syscall.EWOULDBLOCK) {
		return errLocked
	}
	return err
}

// unlockFile releases the lock of lockFile on f.
func unlockFile(f *os.File) error {
	return withFd(f, func(fd int) error { return syscall.Flock(fd, syscall.LOCK_UN) })
}

// withFd calls fn with the file descriptor of f.
func withFd(f *os.File, fn func(fd int) error) error {
	conn, err := f.SyscallConn()
	if err != nil {
		return err
	}
	var fnErr error
	if err := conn.Control(func(fd uintptr) { fnErr = fn(int(fd)) }); err != nil {
		return err
	}
	return fnErr
}

// syncDir syncs the directory of root, so that a rename in it survives a
// crash.
func syncDir(root *os.Root) error {
	d, err := root.Open(".")
	if err != nil {
		return err
	}
	if err := d.Sync(); err != nil {
		_ = d.Close()
		return err
	}
	return d.Close()
}
