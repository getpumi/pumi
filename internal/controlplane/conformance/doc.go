// SPDX-License-Identifier: Apache-2.0
// SPDX-FileCopyrightText: The Pumi Authors

// Package conformance is the conformance suite of the control plane
// (decision 0161). It reaches the control plane only through the generated
// Connect clients, so the same tests run against the embedded control plane
// in-process and against a control plane over the network.
//
// A test of a control plane calls Run with a function that starts a new
// control plane with an empty log and returns it as a Plane: its clients of
// both services and a way to move its clock.
package conformance
