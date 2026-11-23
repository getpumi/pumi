<!--
SPDX-License-Identifier: Apache-2.0
SPDX-FileCopyrightText: The Pumi Authors
-->
# in-doubt-escalates

The log ends with `invocation.requested` and no result, and pumi is started again. On start the kernel escalates the plan instead of running the stage again, because the invocation may have run (decisions 0019 and 0182). The row of this case in the triage table of getpumi/design `regime/README.md` at `4a29c9b`.

Regime sources, in `WalkingDisaster/scale-wholesale` at `9842f55`:

- PEX-032.1, "A person interrupts a stage", in `openspec/specs/plan-execution/spec.md`. The regime pauses the plan and runs the stage again on resume; Pumi escalates instead.
- `TestATerminationSignalStopsARun` in `tools/regime/cmd/regime/main_test.go` and `TestAStopBeforeTheRestartIsHonoured` in `tools/regime/cmd/regime/move_test.go`.
- `TestAnInterruptedAuditPausesThePlan`, `TestAStageThatEndsAfterTheStopIsRunAgain` and `TestAnInterruptedDecomposeOpensNoIssue` in `tools/regime/internal/runner/recover_test.go`.

The restart at every position, which every case runs with, stands for PEX-032.2, "Resuming a paused plan", and `TestResumingAPausedPlanRunsTheStageAgain` in `tools/regime/internal/runner`.
