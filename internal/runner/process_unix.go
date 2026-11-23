// SPDX-License-Identifier: Apache-2.0
// SPDX-FileCopyrightText: The Pumi Authors

//go:build unix

package runner

import (
	"errors"
	"os"
	"os/exec"
	"syscall"
)

// ownGroup starts the adapter in a process group of its own, so that
// killGroup reaches the processes it starts, such as its harness.
func ownGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

// killGroup kills every process of the adapter's process group, the
// adapter included. A group that has no process left is not an error.
func killGroup(p *os.Process) error {
	if err := syscall.Kill(-p.Pid, syscall.SIGKILL); err != nil && !errors.Is(err, syscall.ESRCH) {
		return err
	}
	return nil
}
