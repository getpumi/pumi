// SPDX-License-Identifier: Apache-2.0
// SPDX-FileCopyrightText: The Pumi Authors

package runner

import (
	"errors"
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
	// Gateway is the model gateway's base URL and the invocation's
	// short-lived token (decision 0036).
	Gateway *harnessv1.Gateway
	// JSONSchema is the JSON Schema generated from the document's output
	// message (decision 0160); empty when the document names none.
	JSONSchema string
}

// HarnessInvocation derives the harness invocation from the invocation
// document doc and what the runner resolved for it (decision 0158). It
// leaves out the credential handles, the image digest and the isolation
// level, which only the runner needs, and it copies what it keeps, so that
// the result shares nothing with doc.
func HarnessInvocation(doc *runnerv1.Invocation, r Resolved) (*harnessv1.Invocation, error) {
	switch {
	case doc == nil:
		return nil, errors.New("runner: no invocation document")
	case !filepath.IsAbs(r.Workspace):
		return nil, errors.New("runner: the workspace is not an absolute path")
	case r.Gateway == nil:
		return nil, errors.New("runner: no model gateway")
	case (doc.GetOutputMessage() == "") != (r.JSONSchema == ""):
		return nil, errors.New("runner: the output message and its JSON Schema do not come together")
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
