<!--
SPDX-License-Identifier: Apache-2.0
SPDX-FileCopyrightText: The Pumi Authors
-->
# action-did-not-happen

The external action of merging is in doubt when its lease expires, and the fake adapter answers that the action did not happen. The kernel decides retry on in_doubt and runs the merge again as plan-a/4 (decisions 0200 to 0202).

Regime sources, in `WalkingDisaster/scale-wholesale` at `9842f55`:

- PEX-032 in `openspec/specs/plan-execution/spec.md`.
- `TestAResumeEndsAPlanStoppedBeforeMergeWhosePullRequestAPersonMerged` in `tools/regime/internal/runner/recover_test.go`, the case in which the forge shows no merge.

The restart at every position, which every case runs with, includes a restart just after the lease expired, so the kernel also finds the invocation in doubt when `pumi` starts (decision 0199).
