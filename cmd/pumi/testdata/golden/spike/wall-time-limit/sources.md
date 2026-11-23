<!--
SPDX-License-Identifier: Apache-2.0
SPDX-FileCopyrightText: The Pumi Authors
-->
# wall-time-limit

The runner claims the agent run, whose budget allows 1,200 seconds. It stops the harness at that limit and reports the outcome cap reached, with the measured seconds and, because the harness was killed before it wrote a result, the tokens and cost of the budget (decisions 0193 and 0204). The usage is under the hard caps, so advance would be allowed, but the outcome fires the state's cap_reached transition instead of its result transitions, and the plan escalates to implementing-escalated (decision 0203).

Regime sources, in `WalkingDisaster/scale-wholesale` at `9842f55`:

- The regime has no wall-time limit on an agent session. A search of `openspec/specs` and of `tools/regime/internal/claude` and `tools/regime/internal/runner` for a timeout or deadline on a session found none.
- CIP-033.2, "A step hangs", in `openspec/specs/ci-pipeline/spec.md`: a CI job ends at its time limit, and the gate fails and names it. It is the regime's nearest rule: a run that reaches its time limit ends, and the failure is reported rather than passed on.
- `TestAnOpusStageAtALimitEscalates` in `tools/regime/internal/runner/fallback_test.go`: a stage that reports a usage limit of the model escalates the plan. The limit there is the model provider's, not a wall-time limit.
