<!--
SPDX-License-Identifier: Apache-2.0
SPDX-FileCopyrightText: The Pumi Authors
-->
# check-runs-again

The check of checking is in doubt when its lease expires. A check runs again: the control plane grants the same invocation again under lease number 2, and the kernel does nothing (decision 0191). checking has no transition on in_doubt (decision 0200).

Regime sources, in `WalkingDisaster/scale-wholesale` at `9842f55`:

- PEX-032.2, "Resuming a paused plan", in `openspec/specs/plan-execution/spec.md`, and `TestResumingAPausedPlanRunsTheStageAgain` in `tools/regime/internal/runner/recover_test.go`: the regime runs a stopped stage again. I found no regime test of a check in doubt on its own.

The restart at every position, which every case runs with, includes a restart just after the lease expired, so the kernel also finds the invocation in doubt when `pumi` starts (decision 0199).
