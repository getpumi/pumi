// SPDX-License-Identifier: Apache-2.0
// SPDX-FileCopyrightText: The Pumi Authors

// Package kernel is the kernel component. It runs the workflow and reaches
// the event log and policy decisions only through the control plane.
//
// Import rule: it may import the standard library, internal/shared, the api
// module (getpumi.dev/pumi/api), the third-party modules on its allowlist and
// its own subpackages. It never imports another component or anything under
// cmd/. Only the main packages under cmd/ import more than one component.
package kernel
