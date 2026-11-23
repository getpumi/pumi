<!--
SPDX-License-Identifier: Apache-2.0
SPDX-FileCopyrightText: The Pumi Authors
-->
# cancel-while-running

The runner claims the agent run, and the person cancels the plan with a cancel.requested that names the plan. The control plane decides the person's cancel, which core.person-cancel permits, before it appends the event. The kernel decides cancel and goes to cancelled, and in the same append asks the running invocation to stop with a cancel.requested that names plan-a/1. The runner learns of it in a heartbeat, stops the harness and reports the outcome cancelled; the result fires nothing, because the plan is final (decision 0192).

Regime sources, in `WalkingDisaster/scale-wholesale` at `9842f55`:

- PEX-032.1, "A person interrupts a stage", in `openspec/specs/plan-execution/spec.md`, and `TestTheStopNoteNamesEachPausedPlan` in `tools/regime/cmd/regime/run_test.go`. The regime has no cancel of a plan by a person: a person who interrupts `regime run` pauses the plan, and `regime resume` runs the stage again. Pumi has no pause by a person in the MVP (decision 0170), and a cancel is final (decisions 0035 and 0192). A search of `openspec/specs` and `tools/regime` for "cancel" found no other requirement or test of a person ending a plan.
