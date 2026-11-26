// SPDX-License-Identifier: Apache-2.0
// SPDX-FileCopyrightText: The Pumi Authors

package runner

import (
	"errors"
	"fmt"
	"path/filepath"
	"slices"

	harnessv1 "getpumi.dev/pumi/api/gen/pumi/harness/v1"
	runnerv1 "getpumi.dev/pumi/api/gen/pumi/runner/v1"
	"google.golang.org/protobuf/proto"
)

// Resolved is what the runner resolves for an invocation before it starts
// the harness adapter (decision 0158).
type Resolved struct {
	// Workspace is the absolute path of the invocation's workspace, a
	// checkout of the input tree.
	Workspace string
	// Gateway is the model gateway's base URL, the invocation's short-lived
	// token and whether the harness keeps its own sign-in (decisions 0036,
	// 0225 and 0231). An agent run must have it (decision 0226); a check or
	// an external action has none.
	Gateway *harnessv1.Gateway
	// JSONSchema is the JSON Schema generated from the document's output
	// message (decision 0160); empty when the document names none.
	JSONSchema string
	// SignIn is set when the adapter of an agent run is in the sign-in
	// mode: its harness talks to the provider directly, so the run has no
	// gateway (decision 0240).
	SignIn bool
	// Model and FallbackModel are, on the sign-in path, the models that the
	// configuration maps the model role to: the role's model, and its
	// fallback's model when the fallback is on the same adapter (decision
	// 0254).
	Model, FallbackModel string
	// ReadPaths are the read paths of the adapter's configuration (decision
	// 0269).
	ReadPaths []string
}

// HarnessInvocation derives the harness invocation from the invocation
// document doc and what the runner resolved for it (decision 0158). It
// leaves out the credential handles, the image digest and the isolation
// level, which only the runner needs, and it copies what it keeps, so that
// the result shares nothing with doc. It refuses an agent run without
// network access to its models (decision 0226), and one that has the wrong
// path: an agent run with the gateway has no models, and one on the
// sign-in path has the role's model and no gateway (decisions 0240 and
// 0254).
func HarnessInvocation(doc *runnerv1.Invocation, r Resolved) (*harnessv1.Invocation, error) {
	switch {
	case doc == nil:
		return nil, errors.New("runner: no invocation document")
	case !filepath.IsAbs(r.Workspace):
		return nil, errors.New("runner: the workspace is not an absolute path")
	case (doc.GetOutputMessage() == "") != (r.JSONSchema == ""):
		return nil, errors.New("runner: the output message and its JSON Schema do not come together")
	}
	if err := checkAgentRun(doc, r.SignIn, r.Gateway != nil); err != nil {
		return nil, err
	}
	if err := checkModels(doc, r); err != nil {
		return nil, err
	}
	inv := &harnessv1.Invocation{
		Invocation:     doc.GetInvocation(),
		IdempotencyKey: doc.GetIdempotencyKey(),
		Component:      proto.CloneOf(doc.GetComponent()),
		Input:          proto.CloneOf(doc.GetInput()),
		Role:           doc.GetRole(),
		Capabilities:   proto.CloneOf(doc.GetCapabilities()),
		Budget:         proto.CloneOf(doc.GetBudget()),
		Workspace:      r.Workspace,
		Gateway:        proto.CloneOf(r.Gateway),
		ModelRole:      doc.GetModelRole(),
		Model:          r.Model,
		FallbackModel:  r.FallbackModel,
		ReadPaths:      slices.Clone(r.ReadPaths),
	}
	if doc.GetOutputMessage() != "" {
		inv.OutputSchema = &harnessv1.OutputSchema{Message: doc.GetOutputMessage(), JsonSchema: r.JSONSchema}
	}
	return inv, nil
}

// checkAgentRun refuses an agent run whose adapter is in the API key mode
// when the runner has no model gateway, or one whose adapter is in the
// sign-in mode when it has a gateway, which that path does not use
// (decisions 0226 and 0240). It also refuses an agent run whose network
// access does not reach its models: the model gateway, or on the sign-in
// path the provider. It allows any other kind of invocation.
func checkAgentRun(doc *runnerv1.Invocation, signIn, hasGateway bool) error {
	if doc.GetComponent().GetKind() != harnessv1.Component_KIND_AGENT_RUN {
		return nil
	}
	switch {
	case signIn && hasGateway:
		return errors.New("runner: an agent run on the sign-in path talks to the provider directly and has no model gateway (decision 0240)")
	case !signIn && !hasGateway:
		return errors.New("runner: an agent run goes through the model gateway, and the runner has no model gateway (decision 0226)")
	}
	access := doc.GetCapabilities().GetNetwork().GetAccess()
	if access != harnessv1.Capabilities_Network_ACCESS_GATEWAY && access != harnessv1.Capabilities_Network_ACCESS_HOSTS {
		return fmt.Errorf("runner: an agent run reaches at least its models, through the model gateway or on the sign-in path at the provider, but its network access is %v (decision 0226)", access)
	}
	return nil
}

// checkModels refuses models where they do not belong: an agent run on the
// sign-in path must have its role's model, and any other invocation has
// none, because the gateway maps the role (decisions 0233 and 0254).
func checkModels(doc *runnerv1.Invocation, r Resolved) error {
	onSignIn := r.SignIn && doc.GetComponent().GetKind() == harnessv1.Component_KIND_AGENT_RUN
	switch {
	case onSignIn && r.Model == "":
		return fmt.Errorf("runner: the model role %q of an agent run on the sign-in path has no model (decision 0254)", doc.GetModelRole())
	case !onSignIn && (r.Model != "" || r.FallbackModel != ""):
		return errors.New("runner: only an agent run on the sign-in path gets the models of its role (decision 0254)")
	}
	return nil
}
