// SPDX-License-Identifier: Apache-2.0
// SPDX-FileCopyrightText: The Pumi Authors

// Package gateway is the model gateway component. It sits between an agent
// harness and the model providers (decision 0036).
//
// In embedded mode pumi run serves it on 127.0.0.1 (decision 0231). It
// passes calls of Anthropic's Messages API through unchanged, streaming
// included, with the adopter's API key or the harness's own sign-in
// (decision 0225). It counts every token of every call (decision 0227),
// prices each call from the list-price table built into the binary
// (decisions 0229 and 0239), keeps each open invocation's totals (decision
// 0230) and refuses an invocation's next call once its tokens or cost reach
// its budget (decision 0228).
//
// It sends a call that names a model role to the role's model. Before any
// of the answer reaches the harness, it sends a call answered with a usage
// or rate limit once to the role's fallback model, and a call answered with
// a server error once more to the same model after a wait. It writes each
// fallback and retry to the invocation's log (decision 0233).
//
// Import rule: it may import the standard library, internal/shared, the api
// module (getpumi.dev/pumi/api), the third-party modules on its allowlist and
// its own subpackages. It never imports another component or anything under
// cmd/. Only the main packages under cmd/ import more than one component.
package gateway
