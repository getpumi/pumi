// SPDX-License-Identifier: Apache-2.0
// SPDX-FileCopyrightText: The Pumi Authors

// Package conformance is the adapter conformance suite (decision 0244). An
// adapter's tests call Run with the adapter's executable and a small piece
// of the adapter's own code, Adapter.Turn, that turns each step of a
// scenario into its harness's tool calls. The suite then plays the runner:
// it makes a workspace with one commit of the input, starts the adapter in
// it, writes the harness invocation as one line on its standard input and
// reads the lines of its standard output (decisions 0158 and 0252). It also
// serves a fake provider, for Anthropic's Messages API or OpenAI's
// Responses API, which scripts the model's side of every call, so no call
// reaches a real provider.
//
// The scenarios are written without any harness in mind. They check the
// process contract (the result, the exit status, the output schema and
// stopping when standard input closes); that each item of the capability
// list is refused when the list leaves it out (paths, commands, network
// and kinds of tool), with path rules anchored to the workspace (regime
// PEX-007) and commands with assignments (decision 0248); that the adapter
// runs where its harness's nested-session guard would see a session (regime
// PEX-042); the token and model of every call; the usage on the sign-in
// path (decision 0241); and the limit status and the refusals (decision
// 0249) where the adapter says its harness reports them.
//
// Each scenario is a subtest of Run, named after the scenario, so that an
// adapter's tests can run one with go test -run.
package conformance
