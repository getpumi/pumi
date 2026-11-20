# SPDX-License-Identifier: Apache-2.0
# SPDX-FileCopyrightText: The Pumi Authors

# The one image for development and CI (decisions 0089 and 0113). CI tags it
# with the git tree hash of this directory, so any change here builds a new
# image.

# The Rust Cedar CLI, which checks the policies in CI (decision 0146), built
# from its crate with the crate's own lock file in a stage of its own, so the
# final image holds only the cedar binary. The official rust image is on the
# same Debian release as the golang image, so the binary links the same C
# library. make pins reports a newer release of the crate.
FROM rust:1.100.0-trixie@sha256:223cc32ddca8f3ee90c436dbc94b6b5f052ea7f9653c5201e9612f08ad7d21b8 AS cedar
ARG CEDAR_VERSION=4.15.0
RUN cargo install --locked --root /cedar cedar-policy-cli@$CEDAR_VERSION

# The official golang image for the newest Go release, pinned by the digest of
# its multi-platform index. It includes gcc, which -race needs.
FROM golang:1.27.4-trixie@sha256:f18c3d73a184fb0582828510722e723b63a93f7188d11dd223d9be6f6880e65d

LABEL org.opencontainers.image.source=https://github.com/getpumi/pumi

# The older supported Go release, on which make test also tests the api module
# (decisions 0085 and 0117). The go command downloads it from the module proxy
# into the module cache, so make test runs it without a network.
ENV PUMI_GO_OLDER=go1.26.11
RUN GOTOOLCHAIN=$PUMI_GO_OLDER go version

COPY install-tools.sh /tmp/install-tools.sh
RUN /tmp/install-tools.sh && rm /tmp/install-tools.sh

COPY --from=cedar /cedar/bin/cedar /usr/local/bin/cedar
