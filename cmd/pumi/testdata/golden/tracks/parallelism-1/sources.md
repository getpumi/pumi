<!--
SPDX-License-Identifier: Apache-2.0
SPDX-FileCopyrightText: The Pumi Authors
-->
# parallelism-1

The plans of parallelism-2, with the parallelism setting at 1. a1 runs alone. When it has merged, both a2 and b1 are ready, and a2 starts first because it was added first. b1 starts when a2 has merged (decision 0189). The rows PAR-003.1 and PAR-009.2 of the triage table in getpumi/design `regime/scenarios.md`.

Regime sources, in `WalkingDisaster/scale-wholesale` at `9842f55`:

- [PAR-003] in `openspec/specs/parallel-execution/spec.md`: at most the configured number of plans run at once, and the next ready plan starts when one finishes.
- `TestMaxParallelPlans` in `tools/regime/internal/config/config_test.go`.
