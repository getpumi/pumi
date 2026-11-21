// SPDX-License-Identifier: Apache-2.0
// SPDX-FileCopyrightText: The Pumi Authors

package controlplane

import "time"

// Clock is the control plane's source of time: the lease timers and the
// wait of Claim use it. The wiring injects it, so tests move time without
// sleeping.
type Clock interface {
	// Now returns the current time.
	Now() time.Time
	// AfterFunc calls f in its own goroutine once d has passed, unless the
	// returned timer is stopped first.
	AfterFunc(d time.Duration, f func()) Timer
}

// Timer is a timer that a Clock set.
type Timer interface {
	// Stop stops the timer and reports whether it stopped it before it
	// fired.
	Stop() bool
}

// systemClock is the Clock of the operating system.
type systemClock struct{}

// Now returns time.Now.
func (systemClock) Now() time.Time { return time.Now() }

// AfterFunc is time.AfterFunc.
func (systemClock) AfterFunc(d time.Duration, f func()) Timer { return time.AfterFunc(d, f) }
