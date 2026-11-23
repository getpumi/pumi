<!--
SPDX-License-Identifier: Apache-2.0
SPDX-FileCopyrightText: The Pumi Authors
-->
# failed-attempts-escalate

The runner reports the agent run as failed, and with maxAttempts 1 for in-doubt it runs again as plan-a/2. When plan-a/2 fails too, the plan has one in-doubt attempt, so core.attempts-left denies the retry, and the plan escalates on failed to implementing-escalated with escalation.raised (decisions 0169, 0235 and 0237).

Regime sources, in `WalkingDisaster/scale-wholesale` at `9842f55`:

- MFB-005, "A retry after a passing failure is recorded, and a second failure stops the plan", and its scenario "The retry fails the same way", in `openspec/specs/model-fallback/spec.md`: a retry that fails again escalates and is not tried again.
- MFB-001, scenario "Any other failure", in the same file: a stage that fails for a reason other than a usage limit is not retried on another model, and the change escalates.
- `TestARetryThatFailsTheSameWayEscalatesNamingBothFailures` in `tools/regime/internal/runner/passing_test.go` and `TestAnotherFailureIsNotRetried` in `tools/regime/internal/runner/fallback_test.go`.
