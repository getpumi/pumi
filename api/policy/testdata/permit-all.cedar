// SPDX-License-Identifier: Apache-2.0
// SPDX-FileCopyrightText: The Pumi Authors
// A lower policy layer that permits everything, to show that it cannot
// loosen the invariants (decision 0142).
@id("permit-all")
permit (principal, action, resource);
