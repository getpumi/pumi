<!--
SPDX-License-Identifier: Apache-2.0
SPDX-FileCopyrightText: The Pumi Authors
-->
# spec-other-tree

The spec check's output holds `spec.valid` true for another tree than the plan's input tree, which the kernel names as the decision's head tree (decision 0300). It is not the fact of that tree, so it is missing and the spec gate denies; the plan escalates.

Regime sources, in `WalkingDisaster/scale-wholesale` at `9842f55`: openspec PEX-059: a fact that cannot be read stops the work instead of being read as a pass.
