// SPDX-License-Identifier: Apache-2.0
// SPDX-FileCopyrightText: The Pumi Authors

module getpumi.dev/pumi/conformance

go 1.26.0

toolchain go1.27.4

replace getpumi.dev/pumi/api => ../api

require (
	getpumi.dev/pumi/api v0.0.0-00010101000000-000000000000
	google.golang.org/protobuf v1.36.12
)
