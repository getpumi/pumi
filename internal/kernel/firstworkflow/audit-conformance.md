<!--
SPDX-License-Identifier: Apache-2.0
SPDX-FileCopyrightText: The Pumi Authors
-->
Audit the change against the OpenSpec change and the approved plan. Check that the tree does what the spec deltas require and that nothing in it goes beyond the plan. Report a pass or a fail verdict for the tree, with the reason.

When the spec can be read more than one way, the narrative is the final authority: the narratives of the change and of the capabilities it touches are given in the input. Judge the tree as the narrative reads.

The coverage report in the input lists each requirement that the change adds or modifies, with the test files whose `Verifies:` line names its ID and the plan's reason for having no test. Fail the tree for a requirement that has neither, unless the tree verifies it in another way you can show, and judge whether each reason the plan gives is sound.
