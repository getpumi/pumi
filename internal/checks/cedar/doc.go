// SPDX-License-Identifier: Apache-2.0
// SPDX-FileCopyrightText: The Pumi Authors

// Package cedar is the policy check (decision 0146). It runs the pinned Rust
// Cedar CLI on the Cedar policies that git tracks: cedar format --check on
// every policy file, cedar validate in strict mode, with warnings as
// failures, against the schema on the core invariants and on every other
// policy set together with them, and cedar run-tests on every file of test
// cases. The runtime decides with cedar-go;
// the Rust CLI is the reference implementation of Cedar, and an agreement
// test checks that the two decide alike.
package cedar
