// SPDX-License-Identifier: Apache-2.0
// SPDX-FileCopyrightText: The Pumi Authors

package controlplane_test

import (
	"slices"
	"sync"
	"time"

	"getpumi.dev/pumi/internal/controlplane"
)

// fakeClock is a controlplane.Clock whose time moves only when Advance
// moves it, so tests do not sleep.
type fakeClock struct {
	mu     sync.Mutex
	now    time.Time
	timers []*fakeTimer
	// added receives a value, without blocking, each time a timer is set.
	added chan struct{}
}

// newFakeClock returns a fake clock at a fixed time.
func newFakeClock() *fakeClock {
	return &fakeClock{now: time.Date(2026, 11, 21, 0, 0, 0, 0, time.UTC), added: make(chan struct{}, 64)}
}

// fakeTimer is a timer of a fakeClock.
type fakeTimer struct {
	clock *fakeClock
	at    time.Time
	f     func()
}

// Now returns the clock's time.
func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

// AfterFunc sets a timer that calls f in Advance once d has passed.
func (c *fakeClock) AfterFunc(d time.Duration, f func()) controlplane.Timer {
	c.mu.Lock()
	t := &fakeTimer{clock: c, at: c.now.Add(d), f: f}
	c.timers = append(c.timers, t)
	c.mu.Unlock()
	select {
	case c.added <- struct{}{}:
	default:
	}
	return t
}

// Stop stops the timer and reports whether it was set.
func (t *fakeTimer) Stop() bool {
	c := t.clock
	c.mu.Lock()
	defer c.mu.Unlock()
	i := slices.Index(c.timers, t)
	if i < 0 {
		return false
	}
	c.timers = slices.Delete(c.timers, i, i+1)
	return true
}

// Advance moves the clock forward by d and calls, in the order of their
// times, the functions of the timers that are due, including those that
// they set, and returns when they have returned.
func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	end := c.now.Add(d)
	c.mu.Unlock()
	for {
		c.mu.Lock()
		next := -1
		for i, t := range c.timers {
			if !t.at.After(end) && (next < 0 || t.at.Before(c.timers[next].at)) {
				next = i
			}
		}
		if next < 0 {
			c.now = end
			c.mu.Unlock()
			return
		}
		t := c.timers[next]
		c.timers = slices.Delete(c.timers, next, next+1)
		c.now = t.at
		c.mu.Unlock()
		t.f()
	}
}
