// SPDX-License-Identifier: Apache-2.0
// SPDX-FileCopyrightText: The Pumi Authors

// Package controlplane is the control-plane component. It holds the event
// log and decides policies, through the verbs append, query, decide and
// subscribe.
//
// Import rule: it may import the standard library, internal/shared, the api
// module (getpumi.dev/pumi/api), the third-party modules on its allowlist and
// its own subpackages. It never imports another component or anything under
// cmd/. Only the main packages under cmd/ import more than one component.
package controlplane
