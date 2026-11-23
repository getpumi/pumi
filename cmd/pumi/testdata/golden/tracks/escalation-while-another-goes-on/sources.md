<!--
SPDX-License-Identifier: Apache-2.0
SPDX-FileCopyrightText: The Pumi Authors
-->
# escalation-while-another-goes-on

The plans of parallelism-2, with the parallelism setting at 2. a1's result is over the cost cap, so a1 escalates and waits for a person. b1 goes on and merges. a2 does not start, although a slot is free, because a1 has not merged (decisions 0189 and 0197). The row PAR-004.1 of the triage table in getpumi/design `regime/scenarios.md`.

Regime sources, in `WalkingDisaster/scale-wholesale` at `9842f55`:

- [PAR-004] in `openspec/specs/parallel-execution/spec.md`: an escalated plan does not stop the plans that do not come after it, and holds those that do.
- `TestAuditLoopEscalatesAndIndependentChangesContinue` in `tools/regime/internal/runner/runner_test.go`.
