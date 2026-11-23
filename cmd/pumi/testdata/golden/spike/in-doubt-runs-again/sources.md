<!--
SPDX-License-Identifier: Apache-2.0
SPDX-FileCopyrightText: The Pumi Authors
-->
# in-doubt-runs-again

A runner claims the invocation, and its lease expires with no result. The invocation is in doubt, so the kernel decides retry on in_doubt, which reads the in-doubt attempts and maxAttempts 1, and runs the stage again as plan-a/2, with the lost run charged its whole budget (decisions 0191, 0200 and 0201). Until E6.2 this case was `in-doubt-escalates`, in which the plan escalated (decision 0182). An invocation that no runner claimed, or whose lease is current, is not in doubt, so a restart before the expiry changes nothing. The row of this case in the triage table of getpumi/design `regime/README.md` at `4a29c9b`.

Regime sources, in `WalkingDisaster/scale-wholesale` at `9842f55`:

- PEX-032.1, "A person interrupts a stage", and PEX-032.2, "Resuming a paused plan", in `openspec/specs/plan-execution/spec.md`. The regime pauses the plan and runs the stage again on resume; Pumi runs it again without a person.
- `TestAStageThatEndsAfterTheStopIsRunAgain` and `TestResumingAPausedPlanRunsTheStageAgain` in `tools/regime/internal/runner/recover_test.go`.

The restart at every position, which every case runs with, includes a restart just after the lease expired, so the kernel also finds the invocation in doubt when `pumi` starts (decision 0199).
