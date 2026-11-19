# SPDX-License-Identifier: Apache-2.0
# SPDX-FileCopyrightText: The Pumi Authors

# The one image for development and CI (decisions 0089 and 0113). CI tags it
# with the git tree hash of this directory, so any change here builds a new
# image.

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
