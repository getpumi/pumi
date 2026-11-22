// SPDX-License-Identifier: Apache-2.0
// SPDX-FileCopyrightText: The Pumi Authors

// Package policy holds Pumi's Cedar schema (decision 0141): one Pumi
// namespace whose actions declare in their context the facts of the catalog
// in getpumi.dev/pumi/api/facts and the profile settings their gates read,
// the core invariants (decision 0142), the forbid policies that no lower
// policy set can loosen, and the core policies of the workflow actions
// (decisions 0169 to 0171).
package policy
