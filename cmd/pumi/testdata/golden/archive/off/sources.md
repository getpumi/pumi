<!--
SPDX-License-Identifier: Apache-2.0
SPDX-FileCopyrightText: The Pumi Authors
-->
# off

The project turns the archive step off (decision 0278): the plan implements and runs the checks and reaches done, and no state starts the archive step, because no transition to it holds (decision 0296). A person archives the change by hand in the change's own commits.

Regime sources: none. The regime always archives a change after its last plan's audit (CHD-014 in `WalkingDisaster/scale-wholesale` at `9842f55`); turning the step off is Pumi's own setting from decision 0278.
