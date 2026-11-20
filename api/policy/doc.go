// SPDX-License-Identifier: Apache-2.0
// SPDX-FileCopyrightText: The Pumi Authors

// Package policy holds Pumi's Cedar schema (decision 0141): one Pumi
// namespace whose actions declare in their context the facts of the catalog
// in getpumi.dev/pumi/api/facts and the profile settings their gates read,
// and the core invariants (decision 0142), the forbid policies that no lower
// policy set can loosen.
package policy
