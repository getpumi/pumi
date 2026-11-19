// SPDX-License-Identifier: Apache-2.0
// SPDX-FileCopyrightText: The Pumi Authors

// Package shared is the one shared leaf package of the core module, for small
// code that more than one component needs.
//
// Import rule: it may import only the standard library and the api module
// (getpumi.dev/pumi/api). Any package in the core module may import it.
package shared

// The blank import keeps the core module's requirement of the api module.
// Nothing in the core uses the api module yet, and go mod tidy removes a
// requirement that no package imports. The replace directive in go.mod points
// it at ./api, so the core always builds against the api code in the same
// commit (decision 0106).
import _ "getpumi.dev/pumi/api"
