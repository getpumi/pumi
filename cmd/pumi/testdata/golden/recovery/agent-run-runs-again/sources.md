<!--
SPDX-License-Identifier: Apache-2.0
SPDX-FileCopyrightText: The Pumi Authors
-->
# agent-run-runs-again

The agent run of implementing is in doubt when its lease expires. The kernel decides retry on in_doubt, with the lost run counted as having spent its whole budget, and starts plan-a/2 (decisions 0191, 0200 and 0201). The plan then goes through the check and the merge to merged, with no person.

Regime sources, in `WalkingDisaster/scale-wholesale` at `9842f55`:

- PEX-032.1, "A person interrupts a stage", and PEX-032.2, "Resuming a paused plan", in `openspec/specs/plan-execution/spec.md`. The regime pauses the plan and runs the stage again on resume; Pumi runs it again without a person.
- `TestAStageThatEndsAfterTheStopIsRunAgain` and `TestResumingAPausedPlanRunsTheStageAgain` in `tools/regime/internal/runner/recover_test.go`.

The restart at every position, which every case runs with, includes a restart just after the lease expired, so the kernel also finds the invocation in doubt when `pumi` starts (decision 0199).
