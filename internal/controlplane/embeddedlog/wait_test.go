// SPDX-License-Identifier: Apache-2.0
// SPDX-FileCopyrightText: The Pumi Authors

package embeddedlog

import (
	"context"
	"errors"
	"testing"
	"time"
)

// waitResult starts l.Wait(ctx, after) and returns a channel that gets its
// error.
func waitResult(ctx context.Context, l *Log, after uint64) <-chan error {
	done := make(chan error, 1)
	go func() { done <- l.Wait(ctx, after) }()
	return done
}

// notYet fails the test if done has a result within a short time.
func notYet(t *testing.T, done <-chan error) {
	t.Helper()
	select {
	case err := <-done:
		t.Fatalf("Wait returned %v before the head moved past its position", err)
	case <-time.After(50 * time.Millisecond):
	}
}

// result returns the result in done, and fails the test if there is none
// within ten seconds.
func result(t *testing.T, done <-chan error) error {
	t.Helper()
	select {
	case err := <-done:
		return err
	case <-time.After(10 * time.Second):
		t.Fatal("Wait did not return")
		return nil
	}
}

func TestWaitReturnsAtOnceWhenTheHeadIsPastThePosition(t *testing.T) {
	l := open(t, newClone(t))
	mustAppend(t, l, planAdded(t, "1", "plan-a"))
	if err := result(t, waitResult(t.Context(), l, 0)); err != nil {
		t.Fatalf("Wait(0) with head 1: %v", err)
	}
}

func TestWaitReturnsWhenAnAppendMovesTheHeadPastThePosition(t *testing.T) {
	l := open(t, newClone(t))
	mustAppend(t, l, planAdded(t, "1", "plan-a"))
	done := waitResult(t.Context(), l, 2)
	notYet(t, done)
	mustAppend(t, l, planAdded(t, "2", "plan-a"))
	notYet(t, done)
	mustAppend(t, l, planAdded(t, "3", "plan-b"))
	if err := result(t, done); err != nil {
		t.Fatalf("Wait(2) after the head moved to 3: %v", err)
	}
}

func TestWaitReturnsTheContextsErrorWhenItIsDone(t *testing.T) {
	l := open(t, newClone(t))
	ctx, cancel := context.WithCancel(t.Context())
	done := waitResult(ctx, l, 0)
	notYet(t, done)
	cancel()
	if err := result(t, done); !errors.Is(err, context.Canceled) {
		t.Fatalf("Wait after cancel = %v, want context.Canceled", err)
	}
}

func TestWaitReturnsErrClosedWhenTheLogCloses(t *testing.T) {
	l := open(t, newClone(t))
	done := waitResult(t.Context(), l, 0)
	notYet(t, done)
	closeLog(t, l)
	if err := result(t, done); !errors.Is(err, ErrClosed) {
		t.Fatalf("Wait after Close = %v, want ErrClosed", err)
	}
	if err := l.Wait(t.Context(), 0); !errors.Is(err, ErrClosed) {
		t.Fatalf("Wait on a closed Log = %v, want ErrClosed", err)
	}
	if _, _, err := l.Read(Filter{}); !errors.Is(err, ErrClosed) {
		t.Fatalf("Read on a closed Log = %v, want ErrClosed", err)
	}
}
