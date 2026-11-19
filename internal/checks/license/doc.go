// SPDX-License-Identifier: Apache-2.0
// SPDX-FileCopyrightText: The Pumi Authors

// Package license is the dependency license check (decisions 0075 to 0078)
// and the generator of THIRD_PARTY_NOTICES (decision 0079). It reads the
// allowed licenses from .pumi/licenses.json and the exceptions from
// .pumi/license-exceptions.json (decision 0112).
//
// The modules it checks are those that the binaries under cmd include on any
// of Go's first-class ports, from go list, and the Go standard library
// (decisions 0126 and 0127). Test libraries and build tools are not in a
// binary and are not checked (decision 0076). The pinned Trivy (decisions
// 0077 and 0086) only detects the licenses: of the modules from go.mod and
// the module cache, and of the standard library from the LICENSE file in
// GOROOT.
package license
