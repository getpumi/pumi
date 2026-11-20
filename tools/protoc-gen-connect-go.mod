// SPDX-License-Identifier: Apache-2.0
// SPDX-FileCopyrightText: The Pumi Authors

module getpumi.dev/pumi/tools/protoc-gen-connect-go

go 1.27.0

toolchain go1.27.4

tool connectrpc.com/connect/v2/cmd/protoc-gen-connect-go

require (
	connectrpc.com/connect/v2 v2.0.2 // indirect
	google.golang.org/protobuf v1.36.12 // indirect
)
