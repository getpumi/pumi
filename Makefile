# SPDX-License-Identifier: Apache-2.0
# SPDX-FileCopyrightText: The Pumi Authors

# The commands that developers, agents and CI run (decision 0088).

.DEFAULT_GOAL := build

# go test picks a new random seed when SHUFFLE is "on" and prints it as
# "-test.shuffle <seed>" when a test fails. Run "make test SHUFFLE=<seed>" to
# repeat that order.
SHUFFLE ?= on
TEST_FLAGS := -race -shuffle=$(SHUFFLE) -count=1

# GO_OLDER is the older supported Go release, on which the api module is also
# tested (decisions 0085 and 0117). The devcontainer image sets PUMI_GO_OLDER
# and has that toolchain in its module cache.
GO_OLDER ?= $(PUMI_GO_OLDER)

# IMAGE is the repository in the GitHub container registry that holds the
# devcontainer images (decision 0113).
IMAGE ?= ghcr.io/getpumi/pumi-devcontainer

# RANGE is the range of commits that make dco checks. CI sets it to the base
# and head commits of the pull request.
RANGE ?= origin/main..HEAD

# BASE is the commit against which make spdx checks only the files added or
# changed up to HEAD. When it is empty, make spdx checks every file of the
# tree. CI sets it to the base commit of the pull request.
BASE ?=

# AGAINST is the commit whose protobuf schema make buf compares with the
# working tree's in buf breaking (decision 0090). CI sets it to the first
# parent of the commit it checks.
AGAINST ?= origin/main

# BUF runs the pinned buf (decision 0086).
BUF := go tool -modfile=tools/buf.mod buf

.PHONY: build test lint dco spdx imports license notices pins image generate buf cedar

# build compiles every package in both modules and writes the five binaries
# to bin/.
build:
	go build ./...
	go build -o bin/ ./cmd/...
	cd api && go build ./...

# test runs the tests of both modules (decision 0117), runs those of the api
# module again on the older Go release, and writes a coverage profile for each
# run to coverage/.
test:
	@test -n "$(GO_OLDER)" || { echo "make test: GO_OLDER is not set; run make test in the devcontainer" >&2; exit 1; }
	mkdir -p coverage
	go test $(TEST_FLAGS) -coverprofile=coverage/core.out ./...
	cd api && go test $(TEST_FLAGS) -coverprofile=../coverage/api.out ./...
	cd api && GOTOOLCHAIN=$(GO_OLDER) go test $(TEST_FLAGS) -coverprofile=../coverage/api-$(GO_OLDER).out ./...

# lint runs the nolint check on the whole repository (decision 0110), first
# because it is fast, and then golangci-lint with .golangci.yml on both modules
# (decision 0109).
lint:
	go run ./internal/checks/cmd/nolint
	golangci-lint run ./...
	cd api && golangci-lint run --config ../.golangci.yml ./...

# dco runs the DCO check (decisions 0070 and 0081) on the commits in RANGE,
# merge commits included.
dco:
	go run ./internal/checks/cmd/dco "$(RANGE)"

# spdx runs the SPDX header check (decision 0068) on every file of the tree,
# or, when BASE is set, on the files added or changed between BASE and HEAD.
spdx:
	go run ./internal/checks/cmd/spdx $(if $(BASE),"$(BASE)")

# imports runs the import graph check (decision 0108) with the rules and the
# third-party allowlists in .pumi/imports.json, and reports the third-party
# modules that each binary links.
imports:
	go run ./internal/checks/cmd/imports

# license runs the dependency license check (decisions 0075 to 0078) on the
# modules that the five binaries include and on the Go standard library, with
# the pinned Trivy and the policy and exceptions in .pumi/.
license:
	go run ./internal/checks/cmd/license

# notices runs the license check and, when it passes, writes
# bin/THIRD_PARTY_NOTICES for the release archives and images (decisions 0079
# and 0130).
notices:
	go run ./internal/checks/cmd/license -notices bin/THIRD_PARTY_NOTICES

# pins runs the pin report (decision 0115): it asks the Go module proxy for
# newer releases of golangci-lint, Trivy, Go and each tool module in tools/,
# and fails when a pin is out of date. A newer Go minor release is due one
# month after its x.y.1 (decision 0085). It needs network access, so CI runs it
# only in the weekly pins workflow.
pins:
	go run ./internal/checks/cmd/pins

# generate writes the Go code of the protobuf schema in api/proto to api/gen,
# and a JSON Schema of each top-level message to api/gen/jsonschema (decision
# 0160), with the pinned buf and plugins, without the Buf Schema Registry
# (decision 0090). The generated files are committed, and a test in
# internal/checks/bufconfig fails when they are not what generate writes.
generate:
	$(BUF) generate

# buf runs buf lint on the protobuf schema and buf breaking against the schema
# at AGAINST (decisions 0041 and 0090). buf fails on a side with no .proto
# files, so when AGAINST has none in api/proto, buf breaking has nothing to
# compare and is skipped; a name that is not a commit fails.
buf:
	$(BUF) lint
	@git rev-parse --quiet --verify "$(AGAINST)^{commit}" >/dev/null || { echo "make buf: AGAINST=$(AGAINST) is not a commit" >&2; exit 1; }
	@if git ls-tree -r --name-only "$(AGAINST)" -- api/proto | grep -q '\.proto$$'; then \
		echo "$(BUF) breaking --against .git#ref=$(AGAINST)"; \
		$(BUF) breaking --against ".git#ref=$(AGAINST)"; \
	else \
		echo "make buf: $(AGAINST) has no .proto files in api/proto, so buf breaking has nothing to compare" >&2; \
	fi

# cedar runs the pinned Rust Cedar CLI on the Cedar policies that git tracks
# (decision 0146): cedar format --check on every policy file, cedar validate
# in strict mode, with warnings as failures, on the core invariants and on
# every other policy set together with them, and cedar run-tests on every
# x.tests.json file, with the policies of x.cedar beside it. It fails when
# the cedar on the PATH is not the version pinned in .devcontainer/Dockerfile.
cedar:
	go run ./internal/checks/cmd/cedar

# image prints the reference by digest of the devcontainer image for the
# committed content of .devcontainer/, which it tags with the git tree hash of
# that directory (decision 0113). It builds and pushes the image first when the
# registry reports that the tag is not found; any other registry error stops
# it, so an existing tag is never rebuilt. Everything except the reference goes
# to stderr. It needs a docker login to ghcr.io.
image:
	@test -z "$$(git status --porcelain -- .devcontainer)" || { echo "make image: .devcontainer/ has uncommitted changes" >&2; exit 1; }
	@ref=$(IMAGE):$$(git rev-parse HEAD:.devcontainer) && \
	if err=$$(docker buildx imagetools inspect "$$ref" 2>&1 >/dev/null); then \
		echo "make image: found $$ref" >&2; \
	elif echo "$$err" | grep -q ': not found$$'; then \
		echo "make image: building $$ref" >&2 && \
		docker build --tag "$$ref" .devcontainer >&2 && \
		docker push "$$ref" >&2; \
	else \
		echo "$$err" >&2; exit 1; \
	fi && \
	digest=$$(docker buildx imagetools inspect "$$ref" --format '{{.Manifest.Digest}}') && \
	echo "$$digest" | grep -Eqx 'sha256:[0-9a-f]{64}' && \
	echo "$(IMAGE)@$$digest"
