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

`internal/checks/`, `tools/`, `.pumi/`, `.golangci.yml` and the check targets of the Makefile are added by later issues in epic E1 ([#1](https://github.com/getpumi/pumi/issues/1)).

The core's go.mod requires `getpumi.dev/pumi/api` and replaces it with `./api`, so the core always builds against the `api` code in the same commit, and a change to both is one pull request ([0106](https://github.com/getpumi/design/blob/main/decisions/0106-api-replace-directive.md)).

The core module's `go` line is the newest Go release. The `api` module's `go` line is the older of the two supported Go releases, so adapters are not forced to upgrade Go whenever Pumi does. Both modules pin the same exact compiler with the `toolchain` line ([0085](https://github.com/getpumi/design/blob/main/decisions/0085-go-versions.md)).

## Import rules

These rules hold for every change ([0063](https://github.com/getpumi/design/blob/main/decisions/0063-components-never-import-each-other.md), [0064](https://github.com/getpumi/design/blob/main/decisions/0064-third-party-allowlist.md), [0108](https://github.com/getpumi/design/blob/main/decisions/0108-own-import-graph-check.md)):

- A component never imports another component. It may import the standard library, `internal/shared`, the `api` module, the third-party modules on its allowlist and its own subpackages.
- Only the `main` packages under `cmd/` import more than one component, and only to wire them together.
- `internal/shared` imports only the standard library and the `api` module.
- The `api` module imports nothing from the core module.
- Adding a third-party module to a component's allowlist is reviewed like any other loosening of a gate.

## Commands

Run the commands through make ([0088](https://github.com/getpumi/design/blob/main/decisions/0088-make.md)). The logic of the project's own checks is written in Go, with tests; bash in the Makefile is only short glue.

- `make build` compiles both modules and writes the five binaries to `bin/`.
- `make test` runs `go test -race -shuffle=on -count=1 ./...` in both modules and writes a coverage profile for each module to `coverage/` ([0117](https://github.com/getpumi/design/blob/main/decisions/0117-make-test.md)).

The supported development environment is the repository's devcontainer, on Linux. Windows is not supported for development ([0087](https://github.com/getpumi/design/blob/main/decisions/0087-devcontainer-is-the-environment.md)).

## Tests

- A test failure is a real defect. Reproduce it and fix it. Never run the tests again in the hope that a failure goes away. `make test` runs the tests in a random order and, when a test fails, prints the order's seed as `-test.shuffle <seed>`. Where the order matters, repeat it with `make test SHUFFLE=<seed>`.

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
