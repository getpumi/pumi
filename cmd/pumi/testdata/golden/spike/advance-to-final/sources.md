<!--
SPDX-License-Identifier: Apache-2.0
SPDX-FileCopyrightText: The Pumi Authors
-->
# advance-to-final

The plan enters implementing, `invocation.requested` names it, the runner's `invocation.result` arrives under the hard caps, `advance` is allowed, and the plan reaches the final state done. The row of this case in the triage table of getpumi/design `regime/README.md` at `4a29c9b`.

Regime sources, in `WalkingDisaster/scale-wholesale` at `9842f55`:

- `TestHappyPath` in `tools/regime/internal/machine/machine_test.go`: the state machine's path from start to merge.
- `TestHappyPathMergesEveryPlan` in `tools/regime/internal/runner/runner_test.go`.
