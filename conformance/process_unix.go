// SPDX-License-Identifier: Apache-2.0
// SPDX-FileCopyrightText: The Pumi Authors

//go:build unix

package conformance

import (
	"errors"
	"os"
	"os/exec"
	"syscall"
)

// ownGroup starts the adapter in a process group of its own, so that the
// suite can kill the processes that it started with it, as the runner
// does.
func ownGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

// killGroup kills the adapter's process group.
func killGroup(p *os.Process) error {
	if err := syscall.Kill(-p.Pid, syscall.SIGKILL); err != nil && !errors.Is(err, syscall.ESRCH) {
		return err
	}
	return nil
}
