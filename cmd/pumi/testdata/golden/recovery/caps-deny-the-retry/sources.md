<!--
SPDX-License-Identifier: Apache-2.0
SPDX-FileCopyrightText: The Pumi Authors
-->
# caps-deny-the-retry

The budget of each invocation is over the hard cost cap. When the agent run is in doubt, the lost run counts as having spent its whole budget, so invariant.caps denies the retry and the plan escalates on in_doubt with escalation.raised (decision 0191).

Regime sources, in `WalkingDisaster/scale-wholesale` at `9842f55`:

- PEX-032.1, "A person interrupts a stage", and PEX-032.2, "Resuming a paused plan", in `openspec/specs/plan-execution/spec.md`. The regime pauses the plan and runs the stage again on resume; Pumi runs it again without a person.
- `TestAStageThatEndsAfterTheStopIsRunAgain` and `TestResumingAPausedPlanRunsTheStageAgain` in `tools/regime/internal/runner/recover_test.go`.

The restart at every position, which every case runs with, includes a restart just after the lease expired, so the kernel also finds the invocation in doubt when `pumi` starts (decision 0199).
