<!--
SPDX-License-Identifier: Apache-2.0
SPDX-FileCopyrightText: The Pumi Authors
-->
# after-implementing

The project archives after implementing, the default (decision 0278). The plan implements, then enters archiving, whose `invocation.requested` names the spec check's program with the task `archive-change`, its task id (decision 0297), and `returns_tree`; its result carries the tree and no output. The checks run on the archived tree, and the plan reaches done. The transitions of the other archive steps do not hold (decision 0296).

Regime sources, in `WalkingDisaster/scale-wholesale` at `9842f55`: CHD-014 in `openspec/specs/change-delivery/spec.md`, where the system, not the executor, archives the change on the plan's branch after the plan's conformance audit; Pumi's default runs the step earlier, right after implementing (decision 0278). PEX-080 in `openspec/specs/plan-execution/spec.md`, the archive that carries a delta's narrative into the living spec, is the adapter's part (getpumi/adapter-openspec).
