<!--
SPDX-License-Identifier: Apache-2.0
SPDX-FileCopyrightText: The Pumi Authors
-->
# parallelism-2

A person adds three plans: a1 and a2 to the track a, where a2 depends on a1, and b1 to the track b. The parallelism setting is 2. a1 and b1 start at once, and a2 does not start until a1 has merged. Then a2 starts while b1 still runs, and every plan reaches the final state merged (decisions 0189, 0196 and 0197). The rows PAR-002.1, PAR-003.1 and PAR-009.1 of the triage table in getpumi/design `regime/scenarios.md`.

Regime sources, in `WalkingDisaster/scale-wholesale` at `9842f55`:

- [PAR-003] in `openspec/specs/parallel-execution/spec.md`: at most the configured number of plans run at once, and the next ready plan starts when one finishes.
- `TestTheDecompositionCarriesAfter` in `tools/regime/internal/runner/parallel_test.go`: a plan names the plans it comes after.
- `TestMaxParallelPlans` in `tools/regime/internal/config/config_test.go`.
