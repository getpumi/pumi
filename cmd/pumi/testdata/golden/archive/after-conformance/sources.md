<!--
SPDX-License-Identifier: Apache-2.0
SPDX-FileCopyrightText: The Pumi Authors
-->
# after-conformance

The project archives after the conformance audit (decision 0278). The plan implements and runs the checks, then enters archiving, and then the checks run again on the archived tree, in archived-checking, before the plan reaches done (decision 0296). In the first workflow the archive step sits after the conformance audit and the checks run again in the archived-* states before approval.

Regime sources, in `WalkingDisaster/scale-wholesale` at `9842f55`: CHD-014 in `openspec/specs/change-delivery/spec.md`, where the system archives the change on the plan's branch after the plan's conformance audit has passed. Running the checks again on the archived tree is Pumi's own rule from decision 0278.
