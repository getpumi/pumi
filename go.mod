// SPDX-License-Identifier: Apache-2.0
// SPDX-FileCopyrightText: The Pumi Authors

module getpumi.dev/pumi

go 1.27.0

toolchain go1.27.4

require (
	connectrpc.com/connect/v2 v2.0.2
	getpumi.dev/pumi/api v0.0.0
	github.com/cedar-policy/cedar-go v1.9.4
	github.com/qmuntal/stateless v1.8.0
	golang.org/x/sys v0.50.0
	google.golang.org/protobuf v1.36.12
)

require (
	github.com/go-json-experiment/json v1.27.2 // indirect
	golang.org/x/exp v0.0.0-20220921023135-46d9e7742f1e // indirect
)

replace getpumi.dev/pumi/api => ./api
