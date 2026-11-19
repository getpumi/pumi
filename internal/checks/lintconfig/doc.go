// SPDX-License-Identifier: Apache-2.0
// SPDX-FileCopyrightText: The Pumi Authors

// Package lintconfig has no code of its own. Its tests run golangci-lint with
// the repository's .golangci.yml on the fixtures in testdata, to show that the
// configuration enforces the thresholds of decisions 0092 and 0109. The tests
// need golangci-lint on the PATH, as in the devcontainer.
package lintconfig
