// SPDX-License-Identifier: Apache-2.0
// SPDX-FileCopyrightText: The Pumi Authors

//go:build windows

package embeddedlog

import (
	"errors"
	"os"

	"golang.org/x/sys/windows"
)

// errLocked is the error of lockFile when another open file holds the
// lock.
var errLocked = errors.New("the file is locked")

// lockFile takes an exclusive lock on the first byte of f without waiting,
// with LockFileEx. The lock belongs to the open file handle, so a second
// open of the same file fails to take it, in this process or another, and
// the operating system drops it when the process ends.
func lockFile(f *os.File) error {
	err := withHandle(f, func(h windows.Handle) error {
		return windows.LockFileEx(h, windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY,
			0, 1, 0, new(windows.Overlapped))
	})
	if errors.Is(err, windows.ERROR_LOCK_VIOLATION) {
		return errLocked
	}
	return err
}

// unlockFile releases the lock of lockFile on f.
func unlockFile(f *os.File) error {
	return withHandle(f, func(h windows.Handle) error {
		return windows.UnlockFileEx(h, 0, 1, 0, new(windows.Overlapped))
	})
}

// withHandle calls fn with the handle of f.
func withHandle(f *os.File, fn func(windows.Handle) error) error {
	conn, err := f.SyscallConn()
	if err != nil {
		return err
	}
	var fnErr error
	if err := conn.Control(func(fd uintptr) { fnErr = fn(windows.Handle(fd)) }); err != nil {
		return err
	}
	return fnErr
}

// syncDir does nothing on Windows, where a directory cannot be opened for
// a sync through the os package; MoveFileEx, which os.Rename calls, replaces
// head in one step.
func syncDir(*os.Root) error {
	return nil
}
