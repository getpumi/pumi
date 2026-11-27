<!--
SPDX-License-Identifier: Apache-2.0
SPDX-FileCopyrightText: The Pumi Authors
-->
# spec-valid

The first workflow's spec state runs the spec check program, and the OpenSpec adapter reports `spec.valid` true for the plan's tree (decisions 0272 and 0277). The control plane projects the fact from the check's output for the tree that the kernel names (decision 0300), the gate policy `gate.spec` permits the spec gate (decision 0299), and the plan goes on to planning.

Regime sources, in `WalkingDisaster/scale-wholesale` at `9842f55`: openspec PEX-037.2 (a valid change), PEX-078.1, PEX-079.1, PEX-079.3 and CIP-021.1; the regime's lint reports nothing for a valid change.
