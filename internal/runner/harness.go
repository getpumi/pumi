// SPDX-License-Identifier: Apache-2.0
// SPDX-FileCopyrightText: The Pumi Authors

package runner

import (
	"errors"
	"fmt"
	"path/filepath"

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
}

// HarnessInvocation derives the harness invocation from the invocation
// document doc and what the runner resolved for it (decision 0158). It
// leaves out the credential handles, the image digest and the isolation
// level, which only the runner needs, and it copies what it keeps, so that
// the result shares nothing with doc. It refuses an agent run without the
// gateway or without network access to it (decision 0226).
func HarnessInvocation(doc *runnerv1.Invocation, r Resolved) (*harnessv1.Invocation, error) {
	switch {
	case doc == nil:
		return nil, errors.New("runner: no invocation document")
	case !filepath.IsAbs(r.Workspace):
		return nil, errors.New("runner: the workspace is not an absolute path")
	case (doc.GetOutputMessage() == "") != (r.JSONSchema == ""):
		return nil, errors.New("runner: the output message and its JSON Schema do not come together")
	}
	if err := checkAgentRun(doc, r.Gateway != nil); err != nil {
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
	}
	if doc.GetOutputMessage() != "" {
		inv.OutputSchema = &harnessv1.OutputSchema{Message: doc.GetOutputMessage(), JsonSchema: r.JSONSchema}
	}
	return inv, nil
}

// checkAgentRun refuses an agent run when the runner has no model gateway,
// or when its network access does not reach the gateway: every agent run
// goes through the gateway (decision 0226). It allows any other kind of
// invocation.
func checkAgentRun(doc *runnerv1.Invocation, hasGateway bool) error {
	if doc.GetComponent().GetKind() != harnessv1.Component_KIND_AGENT_RUN {
		return nil
	}
	if !hasGateway {
		return errors.New("runner: an agent run goes through the model gateway, and the runner has no model gateway (decision 0226)")
	}
	access := doc.GetCapabilities().GetNetwork().GetAccess()
	if access != harnessv1.Capabilities_Network_ACCESS_GATEWAY && access != harnessv1.Capabilities_Network_ACCESS_HOSTS {
		return fmt.Errorf("runner: an agent run reaches at least the model gateway, but its network access is %v (decision 0226)", access)
	}
	return nil
}
