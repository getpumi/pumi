// SPDX-License-Identifier: Apache-2.0
// SPDX-FileCopyrightText: The Pumi Authors
// The policies of the settable gates that are built before epic E10, which
// moves them into the public set with the policy sets and the profiles
// (decision 0299). The control plane loads them after the core invariants
// and the core policies, so the invariants still forbid what they forbid.
// The spec gate: the kernel may pass it when the spec adapter reports the
// plan's spec valid for the tree, or when a person overrode the spec gate
// for the tree (decisions 0139, 0277 and 0300).
@id("gate.spec")
permit (
  principal is Pumi::Kernel,
  action == Pumi::Action::"spec",
  resource
)
when { context.specValid || context.overrides.contains("spec") };
