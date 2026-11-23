<!--
SPDX-License-Identifier: Apache-2.0
SPDX-FileCopyrightText: The Pumi Authors
-->
# answer-over-caps-escalates

After the plan escalated over the hard caps, a person answers with `escalation.resolved`. The answer is a trigger about the plan (decision 0170); `resume` reads the usage of the plan's last invocation, which is still over the caps, so `invariant.caps` forbids it, and the last transition, `escalate`, keeps the plan in implementing-escalated with a new `escalation.raised`.

Regime sources: none. This case is not in the triage table of getpumi/design `regime/README.md`; it covers the kernel's event triggers and the always allowed last transition of a wait state (decisions 0169 and 0170). The regime's nearest behavior is `regime resume` (PEX-032.2), which runs a paused stage again without a budget check.
