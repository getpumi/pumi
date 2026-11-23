<!--
SPDX-License-Identifier: Apache-2.0
SPDX-FileCopyrightText: The Pumi Authors
-->
# result-after-cancel

As in cancel-while-running, but the runner finishes the agent run before its next heartbeat and reports it succeeded under its current lease. The plan is final, so the result fires nothing and the plan stays cancelled (decision 0192).

Regime sources, in `WalkingDisaster/scale-wholesale` at `9842f55`:

- PEX-032.1, "A person interrupts a stage", in `openspec/specs/plan-execution/spec.md`, and `TestTheStopNoteNamesEachPausedPlan` in `tools/regime/cmd/regime/run_test.go`. The regime has no cancel of a plan by a person: a person who interrupts `regime run` pauses the plan, and `regime resume` runs the stage again. Pumi has no pause by a person in the MVP (decision 0170), and a cancel is final (decisions 0035 and 0192). A search of `openspec/specs` and `tools/regime` for "cancel" found no other requirement or test of a person ending a plan.
