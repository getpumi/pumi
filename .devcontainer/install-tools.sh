#!/bin/sh
# SPDX-License-Identifier: Apache-2.0
# SPDX-FileCopyrightText: The Pumi Authors

# Downloads the release binaries that are pinned by version and SHA-256
# checksum (decision 0086) and installs them in /usr/local/bin. A download
# whose checksum does not match stops the script, and so the image build.
#
# The checksums are copied from the checksum file that each project publishes
# with its release.

set -eu

GOLANGCI_LINT_VERSION=2.15.0
GOLANGCI_LINT_SHA256_AMD64=9b7fe6a6bf6334b98decf8d86b11415ea7889afdabdd1959fdce23af2a9ebcea
GOLANGCI_LINT_SHA256_ARM64=b4d8f17068b35ee6060cc2f69a16fb48740d706fbba81661c318649c114a6998

TRIVY_VERSION=0.76.2
TRIVY_SHA256_AMD64=bc0fc726e017b8927a62b2d3531d7a84ee01769daf5c99ed866dced54089fdbf
TRIVY_SHA256_ARM64=ab9d51f86e6a5fb936f605faff6b593ac51bc378fff4d3e8b9d58358b9c367c3

# fetch downloads the archive at $1, checks it against the SHA-256 in $2 and
# copies the file at path $3 inside the archive to /usr/local/bin.
fetch() {
	dir=$(mktemp -d)
	archive=$dir/$(basename "$1")
	curl --fail --silent --show-error --location --output "$archive" "$1"
	echo "$2  $archive" | sha256sum --check --strict
	tar --extract --gzip --file "$archive" --directory "$dir" "$3"
	install -m 0755 "$dir/$3" /usr/local/bin/
	rm -rf "$dir"
}

case "$(dpkg --print-architecture)" in
amd64)
	golangci_lint_arch=amd64
	golangci_lint_sha256=$GOLANGCI_LINT_SHA256_AMD64
	trivy_arch=64bit
	trivy_sha256=$TRIVY_SHA256_AMD64
	;;
arm64)
	golangci_lint_arch=arm64
	golangci_lint_sha256=$GOLANGCI_LINT_SHA256_ARM64
	trivy_arch=ARM64
	trivy_sha256=$TRIVY_SHA256_ARM64
	;;
*)
	echo "install-tools.sh: unsupported architecture $(dpkg --print-architecture)" >&2
	exit 1
	;;
esac

golangci_lint=golangci-lint-$GOLANGCI_LINT_VERSION-linux-$golangci_lint_arch
fetch \
	"https://github.com/golangci/golangci-lint/releases/download/v$GOLANGCI_LINT_VERSION/$golangci_lint.tar.gz" \
	"$golangci_lint_sha256" \
	"$golangci_lint/golangci-lint"

fetch \
	"https://github.com/aquasecurity/trivy/releases/download/v$TRIVY_VERSION/trivy_${TRIVY_VERSION}_Linux-$trivy_arch.tar.gz" \
	"$trivy_sha256" \
	trivy
