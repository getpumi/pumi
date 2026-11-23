<!--
SPDX-License-Identifier: Apache-2.0
SPDX-FileCopyrightText: The Pumi Authors
-->
# attempts-deny-the-retry

With maxAttempts 1 for in-doubt, the agent run in doubt runs again once as plan-a/2. When plan-a/2 is in doubt too, core.attempts-left denies the retry, and the plan escalates on in_doubt with escalation.raised (decisions 0191 and 0201).

Regime sources, in `WalkingDisaster/scale-wholesale` at `9842f55`:

- PEX-032.1, "A person interrupts a stage", and PEX-032.2, "Resuming a paused plan", in `openspec/specs/plan-execution/spec.md`. The regime pauses the plan and runs the stage again on resume; Pumi runs it again without a person.
- `TestAStageThatEndsAfterTheStopIsRunAgain` and `TestResumingAPausedPlanRunsTheStageAgain` in `tools/regime/internal/runner/recover_test.go`.
- `TestAnInterruptedAuditPausesThePlan` in `tools/regime/internal/runner/recover_test.go`.

The restart at every position, which every case runs with, includes a restart just after the lease expired, so the kernel also finds the invocation in doubt when `pumi` starts (decision 0199).
