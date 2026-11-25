<!--
SPDX-License-Identifier: Apache-2.0
SPDX-FileCopyrightText: The Pumi Authors
-->
# role-falls-back

The runner claims the agent run of the model role frontier. The provider's usage or rate limit stops its run on the role's own adapter, and the role has a fallback on another adapter, so the runner starts the fallback's adapter once, in a fresh checkout of the same input tree, and reports that run's result with the usage of both runs. The Report writes the switch of the role, `dev.getpumi.model_role.switched`, between the result and the end of the invocation; it names the role, the adapter and model it switched to, the invocation and the limit's adapter and model. The run succeeded, so the plan advances to done on result. The kernel does not handle the switch. The person then switches the role back, with an event that names the person (decision 0246).

Regime sources, in `WalkingDisaster/scale-wholesale` at `9842f55`:

- MFB-001, "A stage configured for Fable is retried on Opus when the Fable limit is reached", in `openspec/specs/model-fallback/spec.md`: the regime retries the stage once on another model when the account's limit is reached. Pumi falls back to another adapter, and the role stays there for every later run until a person switches it back, where the regime falls back for one stage.
- MFB-002, "A fallback is logged and recorded in the run state", in the same file: the regime records the stage, the plan, the model it was configured for and the model it ran on. Pumi records the role, both adapters and models and the invocation in the event log.
- `TestAnAuditFallsBackToOpus` and `TestAFallbackIsSavedBeforeTheRetry` in `tools/regime/internal/runner/fallback_test.go`.
