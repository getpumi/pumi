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

.PHONY: build test image

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

# image prints the reference by digest of the devcontainer image for the
# committed content of .devcontainer/, which it tags with the git tree hash of
# that directory (decision 0113). It builds and pushes the image first when the
# registry does not have that tag. Everything except the reference goes to
# stderr. It needs a docker login to ghcr.io.
image:
	@test -z "$$(git status --porcelain -- .devcontainer)" || { echo "make image: .devcontainer/ has uncommitted changes" >&2; exit 1; }
	@ref=$(IMAGE):$$(git rev-parse HEAD:.devcontainer) && \
	if docker buildx imagetools inspect "$$ref" >/dev/null 2>&1; then \
		echo "make image: found $$ref" >&2; \
	else \
		echo "make image: building $$ref" >&2 && \
		docker build --tag "$$ref" .devcontainer >&2 && \
		docker push "$$ref" >&2; \
	fi && \
	digest=$$(docker buildx imagetools inspect "$$ref" | awk '$$1 == "Digest:" { print $$2 }') && \
	test -n "$$digest" && \
	echo "$(IMAGE)@$$digest"
