// SPDX-License-Identifier: Apache-2.0
// SPDX-FileCopyrightText: The Pumi Authors

// Package kernel is the kernel component. It runs the workflow and reaches
// the event log and policy decisions only through the control plane.
//
// Run is the run loop for one plan (decision 0180): it reads the log with
// Subscribe, rebuilds the plan's state from the log when it starts, fires
// the first allowed transition of each trigger and escalates an invocation
// that is in doubt when it starts (decision 0182). The golden cases in
// cmd/pumi play it against the embedded control plane (decision 0183).
//
// Import rule: it may import the standard library, internal/shared, the api
// module (getpumi.dev/pumi/api), the third-party modules on its allowlist and
// its own subpackages. It never imports another component or anything under
// cmd/. Only the main packages under cmd/ import more than one component.
package kernel
