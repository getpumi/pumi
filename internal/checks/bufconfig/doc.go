// SPDX-License-Identifier: Apache-2.0
// SPDX-FileCopyrightText: The Pumi Authors

// Package bufconfig has no code of its own. Its tests run the pinned buf with
// the repository's buf.yaml and buf.gen.yaml (decision 0090): on the fixtures
// in testdata, to show which rules buf lint and buf breaking report with that
// configuration, and on api/proto, to show that the generated code in api/gen
// is current. The tests need the go command and the module cache or network
// access to build the tools in tools/.
package bufconfig
