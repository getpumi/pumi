// SPDX-License-Identifier: Apache-2.0
// SPDX-FileCopyrightText: The Pumi Authors

package embeddedlog

import (
	"errors"
	"slices"
	"testing"

	cloudeventsv1 "getpumi.dev/pumi/api/gen/io/cloudevents/v1"
)

func TestSnapshotGivesTheHeadAndTheHashOfItsLine(t *testing.T) {
	l := open(t, newClone(t))
	if p, h := l.Snapshot(); p != 0 || h != "" {
		t.Fatalf("Snapshot of an empty log = %d, %q; want 0 and no hash", p, h)
	}
	mustAppend(t, l, planAdded(t, "e1", "plan-1"), planAdded(t, "e2", "plan-2"))
	lines := readLines(t, l.Dir())
	if p, h := l.Snapshot(); p != 2 || h != hashOf(lines[1]) {
		t.Fatalf("Snapshot = %d, %q; want 2 and the hash of line 2, %q", p, h, hashOf(lines[1]))
	}
}

func TestAppendAtWritesOnlyWhileTheHeadIsWhereTheCallerSawIt(t *testing.T) {
	l := open(t, newClone(t))
	mustAppend(t, l, planAdded(t, "e1", "plan-1"))
	positions, err := l.AppendAt(1, []*cloudeventsv1.CloudEvent{planAdded(t, "e2", "plan-1")}, nil)
	if err != nil || !slices.Equal(positions, []uint64{2}) {
		t.Fatalf("AppendAt(1) = %v, %v; want [2]", positions, err)
	}
	if _, err := l.AppendAt(1, []*cloudeventsv1.CloudEvent{planAdded(t, "e3", "plan-1")}, nil); !errors.Is(err, ErrHeadMoved) {
		t.Fatalf("AppendAt(1) with the head at 2 = %v, want ErrHeadMoved", err)
	}
	if got := len(readLines(t, l.Dir())); got != 2 || l.Head() != 2 {
		t.Errorf("after a refused AppendAt the log has %d lines and head %d, want 2 and 2", got, l.Head())
	}
}

func TestAppendAtRunsItsCheckWithTheVersions(t *testing.T) {
	l := open(t, newClone(t))
	mustAppend(t, l, planAdded(t, "e1", "plan-1"))
	refused := errors.New("refused")
	_, err := l.AppendAt(1, []*cloudeventsv1.CloudEvent{planAdded(t, "e2", "plan-2")}, func(version func(string) uint64) error {
		if version("plan-1") == 1 {
			return refused
		}
		return nil
	})
	if !errors.Is(err, refused) || l.Head() != 1 {
		t.Fatalf("AppendAt with a failing check = %v, head %d; want the check's error and head 1", err, l.Head())
	}
}
