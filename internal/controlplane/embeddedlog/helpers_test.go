// SPDX-License-Identifier: Apache-2.0
// SPDX-FileCopyrightText: The Pumi Authors

package embeddedlog

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"getpumi.dev/pumi/api/events"
	cloudeventsv1 "getpumi.dev/pumi/api/gen/io/cloudevents/v1"
	eventsv1 "getpumi.dev/pumi/api/gen/pumi/events/v1"
)

// newClone creates a git repository with one commit in a temporary
// directory and returns its working tree. It turns off the user's and the
// system's git configuration for the test. The commands have constant
// arguments, because gosec reports a command built from variables (G204).
func newClone(t *testing.T) string {
	t.Helper()
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	dir := filepath.Join(t.TempDir(), "main")
	if err := os.Mkdir(dir, 0o750); err != nil {
		t.Fatal(err)
	}
	git(t, dir, exec.CommandContext(t.Context(), "git", "init", "--quiet", "--initial-branch=main"))
	git(t, dir, exec.CommandContext(t.Context(), "git", "commit", "--quiet", "--allow-empty", "--message=first"))
	return dir
}

// addWorktree adds the worktree ../worktree to the clone in dir, on a new
// branch, and returns its path.
func addWorktree(t *testing.T, dir string) string {
	t.Helper()
	git(t, dir, exec.CommandContext(t.Context(), "git", "worktree", "add", "--quiet", "-b", "other", "../worktree"))
	return filepath.Join(filepath.Dir(dir), "worktree")
}

// git runs cmd in dir.
func git(t *testing.T, dir string, cmd *exec.Cmd) {
	t.Helper()
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=Author", "GIT_AUTHOR_EMAIL=author@example.com",
		"GIT_COMMITTER_NAME=Committer", "GIT_COMMITTER_EMAIL=committer@example.com")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("%s: %v\n%s", cmd, err, stderr.String())
	}
}

// open opens the log of the clone in dir and closes it when the test ends.
func open(t *testing.T, dir string) *Log {
	t.Helper()
	l, err := Open(t.Context(), dir)
	if err != nil {
		t.Fatalf("Open(%s): %v", dir, err)
	}
	t.Cleanup(func() { _ = l.Close() })
	return l
}

// closeLog closes l.
func closeLog(t *testing.T, l *Log) {
	t.Helper()
	if err := l.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
}

// planAdded returns a dev.getpumi.plan.added event with id and subject.
func planAdded(t *testing.T, id, subject string) *cloudeventsv1.CloudEvent {
	t.Helper()
	e, err := events.New(&eventsv1.PlanAdded{Plan: subject, Track: "main"},
		events.Attributes{ID: id, Source: "/kernel", Subject: subject, Tenant: "set by the caller"})
	if err != nil {
		t.Fatal(err)
	}
	return e
}

// leaseGranted returns a dev.getpumi.lease.granted event with id and
// subject.
func leaseGranted(t *testing.T, id, subject string) *cloudeventsv1.CloudEvent {
	t.Helper()
	e, err := events.New(&eventsv1.LeaseGranted{Lease: subject, Holder: "runner", Number: 1},
		events.Attributes{ID: id, Source: "/controlplane", Subject: subject, Tenant: "set by the caller"})
	if err != nil {
		t.Fatal(err)
	}
	return e
}

// mustAppend appends evs with no check and returns their positions.
func mustAppend(t *testing.T, l *Log, evs ...*cloudeventsv1.CloudEvent) []uint64 {
	t.Helper()
	positions, err := l.Append(evs, nil)
	if err != nil {
		t.Fatalf("Append: %v", err)
	}
	return positions
}

// openRoot opens the directory pumi as a root and closes it when the test
// ends. Tests reach the files of a log through it, because gosec reports
// opening a file at a path built from a variable (G304).
func openRoot(t *testing.T, pumi string) *os.Root {
	t.Helper()
	root, err := os.OpenRoot(pumi)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = root.Close() })
	return root
}

// readLines returns the lines of log.jsonl in pumi without their newlines,
// and fails the test if the file does not end with a newline.
func readLines(t *testing.T, pumi string) [][]byte {
	t.Helper()
	b, err := openRoot(t, pumi).ReadFile("log.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	if len(b) == 0 {
		return nil
	}
	if b[len(b)-1] != '\n' {
		t.Fatalf("%s/log.jsonl does not end with a newline", pumi)
	}
	return bytes.Split(b[:len(b)-1], []byte("\n"))
}

// ids returns the ids of the events of entries, in order.
func ids(entries []Entry) []string {
	out := make([]string, 0, len(entries))
	for _, e := range entries {
		out = append(out, fmt.Sprintf("%d:%s", e.Position, e.Event.GetId()))
	}
	return out
}
