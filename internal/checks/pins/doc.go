// SPDX-License-Identifier: Apache-2.0
// SPDX-FileCopyrightText: The Pumi Authors

// Package pins is the pin report (decision 0115). It reads the pins that
// Dependabot does not update, or does not update together, and asks the Go
// module proxy for newer releases:
//
//   - golangci-lint and Trivy, pinned in .devcontainer/install-tools.sh
//     (decision 0086). Both are Go modules, so the proxy lists their release
//     tags, without authentication.
//   - Go: the go and toolchain lines of the core and api go.mod files, the
//     golang base image in .devcontainer/Dockerfile and the older release in
//     its PUMI_GO_OLDER (decision 0085). The proxy lists the Go releases as
//     versions of the module golang.org/toolchain.
//   - Each tool module file tools/*.mod: the modules that provide its tool
//     lines (decision 0086).
//
// A pin is out of date when a newer release exists, with one exception for
// Go: a move to a newer Go minor release is due one month after that
// release's first patch release, x.y.1, as published on the proxy (decision
// 0085). Until then the report shows the newer release and the date from
// which the pin is out of date.
package pins
