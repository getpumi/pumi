<!--
SPDX-License-Identifier: Apache-2.0
SPDX-FileCopyrightText: The Pumi Authors
-->

# Instructions for agents

Read this file before you change anything in this repository. It is harness-neutral: CLAUDE.md only imports it, so every agent reads the same rules ([0091](https://github.com/getpumi/design/blob/main/decisions/0091-agents-md.md)). People who work here follow the same rules.

The design lives in [getpumi/design](https://github.com/getpumi/design). Read its [decisions index](https://github.com/getpumi/design/blob/main/decisions/README.md) and the component files for the part of the code you change. A change to the code that changes the design needs a decision there first. Do not rely on anything you seem to remember.

## Starting work

- Start only an issue whose item in the [Pumi GitHub project](https://github.com/orgs/getpumi/projects/5) has the Status Ready. Ready is the maintainer's approval. Do not start an item in any other Status ([0118](https://github.com/getpumi/design/blob/main/decisions/0118-ready-is-approval.md)).
- Moving an item to Ready does not start work by itself. Start a Ready item only when the maintainer says to start it.
- Read and change the Status through GitHub's MCP server, as described in the design's [development environment](https://github.com/getpumi/design/blob/main/components/development-environment.md#tracking-work). api.github.com does not work for projects from a cloud thread ([0093](https://github.com/getpumi/design/blob/main/decisions/0093-github-projects-through-mcp-server.md)).
- Set the item to In Review when you open the pull request.
- Close the issue through the pull request description ("Closes #n"). Never put a closing keyword in a commit pushed directly to `main`, because it closes the issue at once.
- The issue's acceptance criteria are the specification. Do not commit plans, specs, design notes or other files that record how a change was worked out; the pull request description holds the plan and the evidence. Some agent tools commit such files by default, such as the Superpowers brainstorming and plan-writing skills under `docs/superpowers/`. This holds until [design #39](https://github.com/getpumi/design/issues/39) decides who owns Pumi's own specs ([0120](https://github.com/getpumi/design/blob/main/decisions/0120-no-process-files.md)).

## Layout

There are two Go modules ([0057](https://github.com/getpumi/design/blob/main/decisions/0057-go-modules.md), [0105](https://github.com/getpumi/design/blob/main/decisions/0105-core-module-layout.md)):

- `getpumi.dev/pumi`, the core, at the repository root.
- `getpumi.dev/pumi/api`, in `api/`: the protobuf schema, the generated Connect code and the schemas for event types and facts. Adapters in other repositories import it without the rest of the core.

In the core module:

- `cmd/pumi`, `cmd/pumi-server`, `cmd/pumi-kernel`, `cmd/pumi-runner` and `cmd/pumi-gateway` are the `main` packages of the five binaries ([0058](https://github.com/getpumi/design/blob/main/decisions/0058-core-deployables.md)).
- `internal/kernel`, `internal/controlplane`, `internal/runner`, `internal/gateway` and `internal/cli` are the components.
- `internal/shared` is the one shared leaf package.
- `internal/checks/<name>/` holds the logic of the project's own checks, and `internal/checks/cmd/<name>/` holds the `main` package that make runs with `go run`. The checks are not part of the binaries ([0107](https://github.com/getpumi/design/blob/main/decisions/0107-checks-in-internal-checks.md)).
- `tools/` holds one module file per pinned Go tool, run with `go tool -modfile` ([0086](https://github.com/getpumi/design/blob/main/decisions/0086-tool-pinning.md)).
- `.pumi/` holds the plain JSON files that the checks read ([0112](https://github.com/getpumi/design/blob/main/decisions/0112-check-config-json.md)).

`tools/`, `.pumi/` and the other check targets of the Makefile are added by later issues in epic E1 ([#1](https://github.com/getpumi/pumi/issues/1)).

The core's go.mod requires `getpumi.dev/pumi/api` and replaces it with `./api`, so the core always builds against the `api` code in the same commit, and a change to both is one pull request ([0106](https://github.com/getpumi/design/blob/main/decisions/0106-api-replace-directive.md)).

The core module's `go` line is the newest Go release. The `api` module's `go` line is the older of the two supported Go releases, so adapters are not forced to upgrade Go whenever Pumi does. Both modules pin the same exact compiler with the `toolchain` line ([0085](https://github.com/getpumi/design/blob/main/decisions/0085-go-versions.md)).

## Import rules

These rules hold for every change ([0063](https://github.com/getpumi/design/blob/main/decisions/0063-components-never-import-each-other.md), [0064](https://github.com/getpumi/design/blob/main/decisions/0064-third-party-allowlist.md), [0108](https://github.com/getpumi/design/blob/main/decisions/0108-own-import-graph-check.md)):

- A component never imports another component. It may import the standard library, `internal/shared`, the `api` module, the third-party modules on its allowlist and its own subpackages.
- Only the `main` packages under `cmd/` import more than one component, and only to wire them together.
- `internal/shared` imports only the standard library and the `api` module.
- The `api` module imports nothing from the core module, and only the third-party modules on its own allowlist ([0125](https://github.com/getpumi/design/blob/main/decisions/0125-api-module-allowlist.md)).
- The packages under `internal/checks` import no component and nothing under `cmd/`, and no package outside `internal/checks` imports them ([0123](https://github.com/getpumi/design/blob/main/decisions/0123-checks-import-no-component.md)).
- Adding a third-party module to a component's allowlist is reviewed like any other loosening of a gate.

## Commands

Run the commands through make ([0088](https://github.com/getpumi/design/blob/main/decisions/0088-make.md)). The logic of the project's own checks is written in Go, with tests; bash in the Makefile is only short glue.

- `make build` compiles both modules and writes the five binaries to `bin/`.
- `make test` runs `go test -race -shuffle=on -count=1 ./...` in both modules, runs the `api` module's tests again on the older supported Go release named by `PUMI_GO_OLDER` in the devcontainer image, and writes a coverage profile for each run to `coverage/` ([0117](https://github.com/getpumi/design/blob/main/decisions/0117-make-test.md)).
- `make lint` runs the nolint check on the whole repository and golangci-lint with `.golangci.yml` on both modules. The tests in `internal/checks/lintconfig` check that the golangci-lint on the PATH is the pinned version and run it on fixtures to show that the configuration enables the 20 linters and enforces the thresholds ([0109](https://github.com/getpumi/design/blob/main/decisions/0109-linter-set.md), [0110](https://github.com/getpumi/design/blob/main/decisions/0110-suppressions-in-lint-config.md)).
- `make dco RANGE=<base>..<head>` runs the DCO check on the commits in the range, merge commits included; `RANGE` defaults to `origin/main..HEAD`. A commit needs a `Signed-off-by` line that matches its author, or, when the author is listed in `.pumi/agent-identities.json`, one from a person. CI's `dco` job runs it on each pull request ([0070](https://github.com/getpumi/design/blob/main/decisions/0070-dco-check.md), [0081](https://github.com/getpumi/design/blob/main/decisions/0081-agent-is-commit-author.md)).
- `make spdx` runs the SPDX header check on every file of the tree; `make spdx BASE=<commit>` checks only the files added or changed between that commit and HEAD, which CI's `spdx` job does for each pull request. The check lists in `internal/checks/spdx` which file types are source, in which comment syntax, and which files are exempt; a file of a type it does not list fails ([0068](https://github.com/getpumi/design/blob/main/decisions/0068-copyright-and-spdx.md)).
- `make imports` runs the import graph check: it reads the import rules and the third-party allowlists of each component, the `api` module, `internal/checks` and `cmd/` from `.pumi/imports.json` and the imports of every Go file of both modules, test files and files for other platforms or build tags included, and fails on an import that breaks a rule above or on a package of the core module that the file does not place. It also prints the number of direct and indirect third-party modules each binary links, without a limit. CI's `imports` job runs it ([0108](https://github.com/getpumi/design/blob/main/decisions/0108-own-import-graph-check.md)).
- `make license` runs the dependency license check: it lists the modules that the five binaries include on any of Go's first-class ports, with cgo on and off, with `go list -deps ./cmd/...`, and the Go standard library, lets the pinned Trivy detect their licenses, and fails on a license that `.pumi/licenses.json` does not allow, on an MPL-2.0 module without an entry in `.pumi/license-exceptions.json` (for a module that go.mod replaces with another, the entry names the replacement), on an exception whose license Trivy no longer detects and on an exception for a module that no binary includes. A module passes only if every license Trivy detects for it passes. It also fails when the Trivy on the PATH is not the version pinned in `.devcontainer/install-tools.sh`. CI's `license` job runs it ([0075](https://github.com/getpumi/design/blob/main/decisions/0075-accepted-dependency-licenses.md) to [0078](https://github.com/getpumi/design/blob/main/decisions/0078-license-exceptions.md), [0126](https://github.com/getpumi/design/blob/main/decisions/0126-license-check-first-class-ports.md) to [0129](https://github.com/getpumi/design/blob/main/decisions/0129-every-detected-license-must-pass.md)).
- `make notices` runs the license check and, when it passes, writes `bin/THIRD_PARTY_NOTICES` with each checked module's licenses and the text of its license and notice files, and for an MPL-2.0 module where its source is ([0079](https://github.com/getpumi/design/blob/main/decisions/0079-third-party-notices.md), [0130](https://github.com/getpumi/design/blob/main/decisions/0130-third-party-notices-in-bin.md)).
- `make pins` runs the pin report: it asks the Go module proxy (`proxy.golang.org`, or the URL given to the program with `-proxy`) for newer releases of golangci-lint and Trivy, pinned in `.devcontainer/install-tools.sh`, of Go, pinned in the go and toolchain lines of both go.mod files and in the base image and `PUMI_GO_OLDER` of `.devcontainer/Dockerfile`, and of each module that provides a tool line of a `tools/*.mod` file. It fails when a pin is out of date. A pin is out of date when a newer release exists, except that a move to a newer Go minor release is due one month after that release's x.y.1, as the proxy dates it. It needs network access, so CI does not run it on pull requests; the weekly `Pins` workflow runs it in the devcontainer image. Dependabot (`.github/dependabot.yml`) updates the action SHAs, the go.mod files and the base image ([0085](https://github.com/getpumi/design/blob/main/decisions/0085-go-versions.md), [0086](https://github.com/getpumi/design/blob/main/decisions/0086-tool-pinning.md), [0115](https://github.com/getpumi/design/blob/main/decisions/0115-pin-updates.md)).
- `make image` prints the reference by digest of the devcontainer image for the committed content of `.devcontainer/`, tagged with that directory's git tree hash, and builds and pushes the image to `ghcr.io/getpumi/pumi-devcontainer` first when the tag is missing. CI's first job runs it ([0113](https://github.com/getpumi/design/blob/main/decisions/0113-devcontainer-image-by-content.md)).

The supported development environment is the repository's devcontainer, on Linux. Windows is not supported for development ([0087](https://github.com/getpumi/design/blob/main/decisions/0087-devcontainer-is-the-environment.md)).

## Tests

- For a change to code, write the test first and run it to see it fail because the behavior is missing, then write the code that makes it pass. Show the failing run in the pull request, because the final diff cannot show the order. This does not cover files with no behavior to test, such as go.mod, the Makefile or this file ([0119](https://github.com/getpumi/design/blob/main/decisions/0119-test-first.md)).
- A test failure is a real defect. Reproduce it and fix it. Never run the tests again in the hope that a failure goes away. `make test` runs the tests in a random order and, when a test fails, prints the order's seed as `-test.shuffle <seed>`. Where the order matters, repeat it with `make test SHUFFLE=<seed>`.
- A pull request shows the output of every make target it affects that CI does not run ([0121](https://github.com/getpumi/design/blob/main/decisions/0121-show-output-ci-does-not-run.md)).

## Files

- Every source file starts with the two SPDX lines in the comment syntax of its file type ([0068](https://github.com/getpumi/design/blob/main/decisions/0068-copyright-and-spdx.md)):

  ```
  SPDX-License-Identifier: Apache-2.0
  SPDX-FileCopyrightText: The Pumi Authors
  ```

- Code has no TODO or FIXME comments ([0092](https://github.com/getpumi/design/blob/main/decisions/0092-follow-up-work.md)).
- A lint finding is suppressed only by an exclusion rule in `.golangci.yml`, never by a `//nolint` comment ([0110](https://github.com/getpumi/design/blob/main/decisions/0110-suppressions-in-lint-config.md)).

## Pull requests

- All changes are made through pull requests. Keep each pull request to one topic.
- **Agents never merge.** The maintainer merges, and the merge is the maintainer's approval ([0114](https://github.com/getpumi/design/blob/main/decisions/0114-main-ruleset.md)). Until agents have their own GitHub identity this is a written rule, not one GitHub enforces.
- **Follow-up work.** Anything needed for a change to be correct, tested and passing its checks is part of that change and is never deferred, however difficult it is. Only work outside the change's scope, such as a different topic or an unrelated defect found along the way, may become follow-up. Do not open follow-up issues yourself. List them in the pull request description under the heading "Proposed follow-up", each with the reason it is outside the change's scope. The reviewer accepts or rejects each item; a rejected item is done in the same pull request, and an accepted item becomes an issue labeled `deferred` in the repository it belongs to ([0092](https://github.com/getpumi/design/blob/main/decisions/0092-follow-up-work.md)).
- **Dependency updates stand alone.** An update made because of a Dependabot alert, a Dependabot pull request or the pin report (`make pins`) is a pull request of its own that contains nothing else. A functional pull request changes a dependency only when the change itself needs a new dependency or a newer version; a new module also goes through the allowlist review ([0116](https://github.com/getpumi/design/blob/main/decisions/0116-dependency-updates-alone.md)).

## Commits

- The agent is the git author of the commits it writes ([0081](https://github.com/getpumi/design/blob/main/decisions/0081-agent-is-commit-author.md)).
- The agent adds the `Signed-off-by` line of the person who asked for the work, on that person's instruction. That person's approval of the pull request confirms the sign-off ([0082](https://github.com/getpumi/design/blob/main/decisions/0082-agent-adds-requester-sign-off.md)).
- A commit written mostly by an agent carries an `Assisted-by` trailer that names the harness and the model, next to the person's sign-off ([0069](https://github.com/getpumi/design/blob/main/decisions/0069-agent-trailer.md)):

  ```
  Assisted-by: <harness> (<model>)
  Signed-off-by: Jane Doe <jane@example.com>
  ```

## Keeping this file current

When you learn a fact that the next agent needs in order to work here, such as how a tool behaves or which network host works, add it under "Facts" in the pull request where you learned it. Say when a fact holds only in one environment, such as cloud threads or one harness. Do not add a rule, meaning something that must or must not be done: a new rule needs the maintainer's decision and a decision record in getpumi/design first ([0122](https://github.com/getpumi/design/blob/main/decisions/0122-agents-md-facts.md)).

## Facts

- Cloud threads only: go.dev is blocked, so its release list cannot be read. `https://proxy.golang.org/golang.org/toolchain/@v/list` lists every Go release, and `GOTOOLCHAIN=auto` downloads the toolchain in a go.mod `toolchain` line from the module proxy.
- Cloud threads only: Docker Hub answers image pulls with 429 Too Many Requests. Start `dockerd` with `{"registry-mirrors":["https://mirror.gcr.io"]}` in `/etc/docker/daemon.json`; the mirror serves the same digests. A container that downloads during a build does not trust the cloud proxy's certificate authority, so a local test build needs `/root/.ccr/ca-bundle.crt` copied into the image and named by `SSL_CERT_FILE`. That change is for local test builds only.
- revive's default `blank-imports` rule rejects a blank import in a non-`main` package even with a comment above a single-line import, and accepts one in a test file. The blank import of the `api` module that keeps its requirement in go.mod is in `internal/shared/doc_test.go` for that reason.
- Cloud threads only: the golangci-lint on the PATH is an older release (2.5.0, built with Go 1.25), not the version pinned in `.devcontainer/install-tools.sh`. `TestGolangciLintVersion` in `internal/checks/lintconfig` fails with it. Download the pinned release from github.com and check its SHA-256 against the script before running `make lint` or `make test`.
- gosec's G204 reports `exec.Command` and `exec.CommandContext` when any argument is not a constant, including the arguments of a helper that passes `args...`. Constant arguments, with `cmd.Dir` set for anything that varies, avoid the finding, as in `internal/checks/lintconfig`.
- `golangci-lint run ./...` skips `testdata` directories, as the go command does, so fixtures there that break the lint rules do not fail `make lint`.
- `git diff-tree --stdin` accepts only object IDs, not names such as `HEAD` or `origin/main`, and prints each input line back, ended by a newline even with `-z`. `git cat-file --batch-check` resolves names given on standard input, such as `origin/main^{tree}`, so `internal/checks/spdx` resolves the trees with it and keeps every git argument constant.
- `go list ./...` leaves out a directory whose Go files build constraints all exclude on the current platform, and `go list` reads only the imports of a file, so it does not report a syntax error after them. `internal/checks/imports` walks the directories and parses the imports of every Go file itself for that reason.
- `go list` on a `main` package reads its git status to stamp the build, which fails with "error obtaining VCS status: exit status 128" where git cannot read the repository, as in CI's container, whose workspace belongs to another user. `internal/checks/imports` passes `-buildvcs=false`, because it does not need the stamp.
- Trivy 0.76.2 sends anonymous usage data unless it is given `--disable-telemetry`. `internal/checks/license` passes it with `--skip-version-check` and `--offline-scan`.
- `trivy filesystem --scanners license` on a go.mod reports every module in it, test libraries included, and reads their licenses from `$GOPATH/pkg/mod`; it ignores `GOMODCACHE`, and a module that is not in that cache gets no license. It reports a module replaced by another module under the replacement's path and version, leaves out a module replaced by a local directory, and does not report the Go standard library. `trivy rootfs` on a Go binary lists its modules but no licenses. `internal/checks/license` takes the modules from `go list` for those reasons.
- Cloud threads only: Trivy is not on the PATH. Download the pinned release from github.com and check its SHA-256 against `.devcontainer/install-tools.sh` before running `make license` or `make test`.
- The Go module proxy lists the release tags of golangci-lint (`github.com/golangci/golangci-lint/v2`) and Trivy (`github.com/aquasecurity/trivy`) without authentication, and the Go releases as versions of `golang.org/toolchain`, such as `v0.0.1-go1.27.4.linux-amd64`; a release before Go 1.21 with patch number 0 has none in its name, such as `go1.20`. The `.info` file of a version gives the time the proxy published it. The proxy answers 404 for a module path it does not know, such as the next major version `github.com/golangci/golangci-lint/v3`.
- Dependabot's commits have no `Signed-off-by` line unless web-commit sign-off is required, which it is not on getpumi/pumi. In public repositories where it is required, Dependabot signs off as `dependabot[bot] <support@github.com>`, while its author email is `49699333+dependabot[bot]@users.noreply.github.com`, so `make dco` fails on its pull requests either way.
