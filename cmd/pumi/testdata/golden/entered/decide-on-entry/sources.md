<!--
SPDX-License-Identifier: Apache-2.0
SPDX-FileCopyrightText: The Pumi Authors
-->
# decide-on-entry

On the workflow in `internal/kernel/testdata/entered`: implementing goes on `advance` to reviewing, a state that starts nothing and decides `advance` again as it is entered, with the `transition.fired` that entered it as the cause, and goes to done.

Regime sources: none. This case is not in the triage table of getpumi/design `regime/README.md`; it covers the kernel's entered trigger (decision 0170), which the first workflow's states that only decide use (decision 0171).
