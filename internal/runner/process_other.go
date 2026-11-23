// SPDX-License-Identifier: Apache-2.0
// SPDX-FileCopyrightText: The Pumi Authors

//go:build !unix

package runner

import (
	"errors"
	"os"
	"os/exec"
)

// ownGroup does nothing where there are no process groups, such as on
// Windows.
func ownGroup(*exec.Cmd) {}

// killGroup kills the adapter. Where there are no process groups, such as
// on Windows, the processes it started are not killed.
func killGroup(p *os.Process) error {
	if err := p.Kill(); err != nil && !errors.Is(err, os.ErrProcessDone) {
		return err
	}
	return nil
}
