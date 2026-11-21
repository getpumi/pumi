// SPDX-License-Identifier: Apache-2.0
// SPDX-FileCopyrightText: The Pumi Authors

package embeddedlog

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
)

// helperDir names the environment variable that makes
// TestOpenInAnotherProcess open the log of the clone it names.
const helperDir = "PUMI_EMBEDDEDLOG_HELPER_DIR"

// testBinary is the running test binary on Linux, the one supported
// development platform (decision 0087). It is a constant, because gosec
// reports a command built from a variable such as os.Args[0] (G204).
const testBinary = "/proc/self/exe"

// TestOpenInAnotherProcess is the second process of
// TestASecondProcessCannotOpenTheLog. It does nothing unless helperDir is
// set. It prints what Open returned and exits with status 3 when Open fails.
func TestOpenInAnotherProcess(t *testing.T) {
	dir := os.Getenv(helperDir)
	if dir == "" {
		t.Skip("run only by TestASecondProcessCannotOpenTheLog")
	}
	l, err := Open(t.Context(), dir)
	if err != nil {
		fmt.Println(err)
		os.Exit(3)
	}
	_ = l.Close()
	fmt.Println("opened")
}

func TestASecondProcessCannotOpenTheLog(t *testing.T) {
	dir := newClone(t)
	l := open(t, dir)

	cmd := exec.CommandContext(t.Context(), testBinary, "-test.run=^TestOpenInAnotherProcess$", "-test.count=1")
	cmd.Env = append(os.Environ(), helperDir+"="+dir)
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	err := cmd.Run()
	if code := cmd.ProcessState.ExitCode(); code != 3 {
		t.Fatalf("the second process exited with status %d (%v), want 3:\n%s", code, err, out.String())
	}
	if !strings.Contains(out.String(), "in use") {
		t.Errorf("the second process said %q, want a message that the log is in use", out.String())
	}

	closeLog(t, l)
	out.Reset()
	cmd = exec.CommandContext(t.Context(), testBinary, "-test.run=^TestOpenInAnotherProcess$", "-test.count=1")
	cmd.Env = append(os.Environ(), helperDir+"="+dir)
	cmd.Stdout, cmd.Stderr = &out, &out
	if err := cmd.Run(); err != nil || !strings.Contains(out.String(), "opened") {
		t.Errorf("after Close the second process got %v:\n%s", err, out.String())
	}
}

func TestASecondOpenInTheSameProcessFails(t *testing.T) {
	dir := newClone(t)
	open(t, dir)
	l, err := Open(t.Context(), dir)
	if err == nil {
		_ = l.Close()
		t.Fatal("a second Open of the same log succeeded")
	}
	if !strings.Contains(err.Error(), "in use") {
		t.Errorf("the second Open returned %q, want a message that the log is in use", err)
	}
}
