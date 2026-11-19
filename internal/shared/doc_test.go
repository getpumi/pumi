// SPDX-License-Identifier: Apache-2.0
// SPDX-FileCopyrightText: The Pumi Authors

package shared

// The blank import keeps the core module's requirement of the api module.
// Nothing in the core uses the api module yet, and go mod tidy removes a
// requirement that no package or test imports. The replace directive in
// go.mod points it at ./api, so the core always builds against the api code in
// the same commit (decision 0106). It is in a test file so that the binaries
// do not link the api module before they use it.
import _ "getpumi.dev/pumi/api"
