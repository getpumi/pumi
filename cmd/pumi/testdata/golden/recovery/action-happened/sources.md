<!--
SPDX-License-Identifier: Apache-2.0
SPDX-FileCopyrightText: The Pumi Authors
-->
# action-happened

The external action of merging is in doubt when its lease expires. The control plane grants it only once, and the kernel asks its adapter whether the action happened. The fake adapter answers that it did, so the kernel appends invocation.finished, resolved on recovery, which is the result of merging, and the plan merges (decision 0202).

Regime sources, in `WalkingDisaster/scale-wholesale` at `9842f55`:

- PEX-032 in `openspec/specs/plan-execution/spec.md`.
- `TestAResumeEndsAPlanStoppedBeforeMergeWhosePullRequestAPersonMerged` and `TestAMergeThatFailsWhileAPersonMergesIsNotAnEscalation` in `tools/regime/internal/runner/recover_test.go`: the regime asks the forge whether the pull request merged and does not merge it again.

The restart at every position, which every case runs with, includes a restart just after the lease expired, so the kernel also finds the invocation in doubt when `pumi` starts (decision 0199).
