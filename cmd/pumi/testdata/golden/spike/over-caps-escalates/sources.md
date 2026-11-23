<!--
SPDX-License-Identifier: Apache-2.0
SPDX-FileCopyrightText: The Pumi Authors
-->
# over-caps-escalates

The result's usage is over the hard cost cap, so `invariant.caps` forbids `advance` and the last transition, `escalate`, fires, with `escalation.raised` for a person. With no gateway, the usage is the adapter's (decision 0185, getpumi/pumi#73). The row of this case in the triage table of getpumi/design `regime/README.md` at `4a29c9b`.

Regime sources, in `WalkingDisaster/scale-wholesale` at `9842f55`:

- `TestEscalateFromAnyState` in `tools/regime/internal/machine/machine_test.go`: escalation with the reason budget exceeded.
- `TestTheRetrysCostCountsAgainstThePlanBudget` and `TestAStageThatChangedNothingAndSpentTheBudgetIsNotRetried` in `tools/regime/internal/runner/runner_test.go` (RPT-003). The regime's plan budget has no requirement; it is `planOverBudget` in `internal/runner/runner.go`.
