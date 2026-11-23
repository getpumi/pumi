<!--
SPDX-License-Identifier: Apache-2.0
SPDX-FileCopyrightText: The Pumi Authors
-->
# failed-runs-again

The runner claims the agent run and reports it with the outcome failed, with usage under the hard caps. The outcome fires the state's failed transitions, not its result transitions, so the plan does not advance to done. With maxAttempts 1 for in-doubt, retry is allowed, and the plan goes back to implementing, which requests the run again as plan-a/2. plan-a/2 succeeds, and the plan advances to done on result (decision 0235). A retry from a state with no gate counts with the in-doubt attempts (decision 0237).

Regime sources, in `WalkingDisaster/scale-wholesale` at `9842f55`:

- MFB-004, "A session that fails for a passing reason outside the plan is tried once more", in `openspec/specs/model-fallback/spec.md`, and its scenario "The retry succeeds" under MFB-005: the regime runs a failed session once more only for a server error or a Cloudflare page. Pumi runs any failed invocation again under the attempts limit and the hard caps.
- `TestAServerErrorOrAnOverloadIsTriedOnceMore` in `tools/regime/internal/runner/passing_test.go`.
