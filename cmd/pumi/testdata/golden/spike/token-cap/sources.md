<!--
SPDX-License-Identifier: Apache-2.0
SPDX-FileCopyrightText: The Pumi Authors
-->
# token-cap

The runner claims the agent run, whose budget allows 200,000 tokens. The model gateway counts every token of its calls; once they reach the budget's tokens, it refuses the next call, and the call in progress at the cap finishes, so the run goes over the budget by that one call (decision 0228). The runner reports the outcome cap reached, whatever the adapter reports, with the gateway's totals and the measured seconds as the usage (decision 0230). The usage is under the hard caps, so advance would be allowed, but the outcome fires the state's cap_reached transition instead of its result transitions, and the plan escalates to implementing-escalated (decision 0203).

Regime sources, in `WalkingDisaster/scale-wholesale` at `9842f55`:

- The regime caps each Claude Code session by cost only, with `--max-budget-usd` set to the plan budget (`MaxBudgetUSD` in `tools/regime/internal/claude/claude.go`, set in `tools/regime/internal/runner/runner.go`). It has no token cap.
- `TestBudgetEscalates` in `tools/regime/internal/runner/runner_test.go`: a stage whose cost is over the plan budget escalates the change, with a reason that names the budget.
- `TestTheRetrysCostCountsAgainstThePlanBudget` in the same file (RPT-003): the cost of every session counts against the plan budget.
