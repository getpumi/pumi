# SPDX-License-Identifier: Apache-2.0
# SPDX-FileCopyrightText: The Pumi Authors

# The commands that developers, agents and CI run (decision 0088).

.DEFAULT_GOAL := build

# go test picks a new random seed when SHUFFLE is "on" and prints it as
# "-test.shuffle <seed>" when a test fails. Run "make test SHUFFLE=<seed>" to
# repeat that order.
SHUFFLE ?= on
TEST_FLAGS := -race -shuffle=$(SHUFFLE) -count=1

.PHONY: build test

# build compiles every package in both modules and writes the five binaries
# to bin/.
build:
	go build ./...
	go build -o bin/ ./cmd/...
	cd api && go build ./...

# test runs the tests of both modules (decision 0117) and writes a coverage
# profile for each module to coverage/.
test:
	mkdir -p coverage
	go test $(TEST_FLAGS) -coverprofile=coverage/core.out ./...
	cd api && go test $(TEST_FLAGS) -coverprofile=../coverage/api.out ./...
