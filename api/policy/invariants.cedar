// SPDX-License-Identifier: Apache-2.0
// SPDX-FileCopyrightText: The Pumi Authors
// The core invariants of decision 0142. Each is a forbid policy, and a
// forbid overrides any permit (decision 0013), so no profile or lower policy
// set can loosen them. They read only facts from machines or people, never
// from model judgment (decision 0137), and never overrides (decision 0139).
// VERIFY passed on the tree before a merge.
@id("invariant.verify")
forbid (
  principal,
  action == Pumi::Action::"merge",
  resource
)
unless { context.verifyPassed };

// The change touches no protected path.
@id("invariant.protected-paths")
forbid (
  principal,
  action == Pumi::Action::"merge",
  resource
)
unless { context.pathsProtected.isEmpty() };

// The head is up to date, and its tree is the tree that the facts of the
// request are keyed to, which is the plan's tree.
@id("invariant.approved-head")
forbid (
  principal,
  action == Pumi::Action::"merge",
  resource
)
unless
{ context.headState.upToDate && context.headState.tree == resource.tree };

// No action once usage is over a hard cap. The caps come from the project
// configuration, set during bootstrapping, not from the profile.
@id("invariant.caps")
forbid (principal, action, resource)
when
{
  context.usage.tokens > context.hardCaps.tokens ||
  context.usage.costMicrodollars > context.hardCaps.costMicrodollars ||
  context.usage.seconds > context.hardCaps.seconds
};

// Only a runner in the gate role writes a verdict (decision 0024).
@id("invariant.verdicts-by-gate-role")
forbid (
  principal,
  action == Pumi::Action::"writeVerdict",
  resource
)
unless { principal is Pumi::Runner && principal.role == "gate" };

// Only a person approves or overrides.
@id("invariant.people-decide")
forbid (
  principal,
  action in [Pumi::Action::"approve", Pumi::Action::"override"],
  resource
)
unless { principal is Pumi::Person };
