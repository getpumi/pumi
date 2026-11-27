<!--
SPDX-License-Identifier: Apache-2.0
SPDX-FileCopyrightText: The Pumi Authors
-->
# spec-fact-missing

The spec check's output holds the narratives but no `spec.valid`. The fact cannot be read, so it is missing from the decision's context and the spec gate denies (decision 0143); the plan escalates.

Regime sources, in `WalkingDisaster/scale-wholesale` at `9842f55`: openspec PEX-059: a fact that cannot be read stops the work instead of being read as a pass.
