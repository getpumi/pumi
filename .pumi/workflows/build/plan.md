<!--
SPDX-License-Identifier: Apache-2.0
SPDX-FileCopyrightText: The Pumi Authors
-->
Write the plan for the OpenSpec change of this plan. Read the proposal, the spec deltas, the design and tasks.md of the change, and the project's own specs and conventions given in the input.

Report:

- the plan, as Markdown: what you will change and in what order;
- the declared paths: the patterns of every path the change will touch, relative to the root of the repository. A change to a path outside them fails the scope gate;
- the open questions: anything the change leaves ambiguous that changes what you would build. Give each your own answer. Do not ask about what you can find out from the repository;
- the requirements with no test: each requirement that the change adds or modifies and that no test of the change will verify, by its ID, with the reason and how it is verified instead. Every other such requirement needs a test with a `Verifies:` line that names its ID.

If the input holds answers from a second opinion or from a person, use them and do not ask those questions again.
