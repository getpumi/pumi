// SPDX-License-Identifier: Apache-2.0
// SPDX-FileCopyrightText: The Pumi Authors

// Package kernel is the kernel component. It runs the workflow and reaches
// the event log and policy decisions only through the control plane.
//
// Run is the run loop of every plan that the log adds (decisions 0180 and
// 0189): it reads the log with Subscribe, rebuilds each plan's state from
// the log when it starts, starts the plans in their declared order up to
// the parallelism setting, fires the first allowed transition of each
// trigger and escalates an invocation that is in doubt (decision 0182).
// The golden cases in cmd/pumi play it against the embedded control plane
// (decision 0183).
//
// Import rule: it may import the standard library, internal/shared, the api
// module (getpumi.dev/pumi/api), the third-party modules on its allowlist and
// its own subpackages. It never imports another component or anything under
// cmd/. Only the main packages under cmd/ import more than one component.
package kernel
