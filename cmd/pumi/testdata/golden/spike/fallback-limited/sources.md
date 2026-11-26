<!--
SPDX-License-Identifier: Apache-2.0
SPDX-FileCopyrightText: The Pumi Authors
-->
# fallback-limited

The runner claims the agent run of the model role implementer. The provider's limit stops its run on the role's own adapter, the runner runs the role's fallback on another adapter, and the limit stops that run too. The runner reports the invocation as failed with the limit as its reason and with the switch of the role, `dev.getpumi.model_role.switched`, because the role stays on its fallback. The outcome fires the state's failed transitions (decision 0235); with maxAttempts 1 for in-doubt, retry is allowed and the run is requested again as plan-a/2. The claim of plan-a/2 says that the role is on its fallback, so the runner starts only the fallback's adapter, and there is no second switch. The limit stops it again, plan-a/2 fails, the retry has no attempts left, and the plan escalates on failed (decision 0246).

Regime sources, in `WalkingDisaster/scale-wholesale` at `9842f55`:

- MFB-003, "A limit on the retry escalates", in `openspec/specs/model-fallback/spec.md`: when the retry on the other model also reports a limit, the regime escalates the change and does not retry again. Pumi fails the invocation and lets the workflow's failed trigger decide, which runs it again under the attempts limit before it escalates.
- `TestARetryThatFailsOtherwiseEscalatesOnce` in `tools/regime/internal/runner/fallback_test.go`.
