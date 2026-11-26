<!--
SPDX-License-Identifier: Apache-2.0
SPDX-FileCopyrightText: The Pumi Authors
-->
# spec-not-valid

The OpenSpec adapter reports `spec.valid` false, as it does for each kind of invalid change in getpumi/adapter-openspec's `TestEachFindingMakesTheSpecInvalid`: the spec gate denies, and the plan escalates to spec-escalated, for a person to fix the spec. The findings are in the check's log (decision 0277).

Regime sources, in `WalkingDisaster/scale-wholesale` at `9842f55`: openspec PEX-037.1, PEX-037.3, PEX-037.4, PEX-078.2, PEX-078.3, PEX-078.5, PEX-079.2, PEX-079.4, PEX-079.7 and CIP-021.2; the regime's lint fails each such change.
