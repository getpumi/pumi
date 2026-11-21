// SPDX-License-Identifier: Apache-2.0
// SPDX-FileCopyrightText: The Pumi Authors

package runner

import (
	"strings"
	"testing"

	harnessv1 "getpumi.dev/pumi/api/gen/pumi/harness/v1"
	runnerv1 "getpumi.dev/pumi/api/gen/pumi/runner/v1"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

// The values of the fields that only the runner needs, chosen so that they
// cannot appear in a harness invocation by chance.
const (
	handleA     = "handle-a-7f3c1e"
	handleB     = "handle-b-92d40a"
	imageDigest = "sha256:5b1e0c0ffee5b1e0c0ffee5b1e0c0ffee5b1e0c0ffee5b1e0c0ffee5b1e0c0f"
)

// document returns an invocation document with every field set.
func document() *runnerv1.Invocation {
	return &runnerv1.Invocation{
		Invocation:     "inv-1",
		IdempotencyKey: "key-1",
		Component: &harnessv1.Component{
			Kind: harnessv1.Component_KIND_AGENT_RUN, Name: "implement", Version: "1.2.3",
		},
		Input:       &harnessv1.Input{Repo: "getpumi/pumi", Tree: "4b825dc6", Task: "Add the test."},
		ImageDigest: imageDigest,
		Role:        harnessv1.Role_ROLE_AGENT,
		Capabilities: &harnessv1.Capabilities{
			WritePaths: []string{"internal/runner/**"},
			Commands:   []string{"go test"},
			Network:    &harnessv1.Capabilities_Network{Access: harnessv1.Capabilities_Network_ACCESS_GATEWAY},
			Tools:      []harnessv1.Capabilities_ToolKind{harnessv1.Capabilities_TOOL_KIND_READ, harnessv1.Capabilities_TOOL_KIND_EDIT},
		},
		Budget:            &harnessv1.Usage{Tokens: 100000, CostMicrodollars: 2000000, Seconds: 600},
		OutputMessage:     "pumi.events.v1.VerdictRecorded",
		Isolation:         runnerv1.Invocation_ISOLATION_MICRO_VM,
		CredentialHandles: []string{handleA, handleB},
	}
}

// resolved returns what the runner resolved for document.
func resolved() Resolved {
	return Resolved{
		Workspace:  "/work/inv-1",
		Gateway:    &harnessv1.Gateway{BaseUrl: "http://127.0.0.1:7070", Token: "short-lived"},
		JSONSchema: `{"type":"object"}`,
	}
}

func TestAHarnessInvocationCarriesNoRunnerOnlyField(t *testing.T) {
	inv, err := HarnessInvocation(document(), resolved())
	if err != nil {
		t.Fatal(err)
	}
	binary, err := proto.Marshal(inv)
	if err != nil {
		t.Fatal(err)
	}
	for name, data := range map[string][]byte{
		"protojson": []byte(protojson.Format(inv)),
		"binary":    binary,
	} {
		for _, value := range []string{handleA, handleB, imageDigest, "MICRO_VM", "MicroVm"} {
			if strings.Contains(string(data), value) {
				t.Errorf("the %s harness invocation holds %q, which only the runner needs (decision 0158)", name, value)
			}
		}
	}
}

func TestAHarnessInvocationCarriesTheRest(t *testing.T) {
	doc, r := document(), resolved()
	got, err := HarnessInvocation(doc, r)
	if err != nil {
		t.Fatal(err)
	}
	want := &harnessv1.Invocation{
		Invocation:     doc.GetInvocation(),
		IdempotencyKey: doc.GetIdempotencyKey(),
		Component:      doc.GetComponent(),
		Input:          doc.GetInput(),
		Role:           doc.GetRole(),
		Capabilities:   doc.GetCapabilities(),
		Budget:         doc.GetBudget(),
		Workspace:      r.Workspace,
		Gateway:        r.Gateway,
		OutputSchema:   &harnessv1.OutputSchema{Message: doc.GetOutputMessage(), JsonSchema: r.JSONSchema},
	}
	if !proto.Equal(got, want) {
		t.Errorf("HarnessInvocation =\n%v\nwant\n%v", protojson.Format(got), protojson.Format(want))
	}
}

func TestAHarnessInvocationSharesNothingWithTheDocument(t *testing.T) {
	doc := document()
	inv, err := HarnessInvocation(doc, resolved())
	if err != nil {
		t.Fatal(err)
	}
	inv.GetCapabilities().WritePaths[0] = "**"
	inv.GetInput().Task = "something else"
	if !proto.Equal(doc, document()) {
		t.Error("changing the harness invocation changed the invocation document")
	}
}

func TestHarnessInvocationRefuses(t *testing.T) {
	for _, c := range []struct {
		name   string
		change func(*runnerv1.Invocation, *Resolved)
	}{
		{"no document", nil},
		{"no workspace", func(_ *runnerv1.Invocation, r *Resolved) { r.Workspace = "" }},
		{"a relative workspace", func(_ *runnerv1.Invocation, r *Resolved) { r.Workspace = "work/inv-1" }},
		{"no gateway", func(_ *runnerv1.Invocation, r *Resolved) { r.Gateway = nil }},
		{"an output message without a JSON Schema", func(_ *runnerv1.Invocation, r *Resolved) { r.JSONSchema = "" }},
		{"a JSON Schema without an output message", func(d *runnerv1.Invocation, _ *Resolved) { d.OutputMessage = "" }},
	} {
		t.Run(c.name, func(t *testing.T) {
			doc, r := document(), resolved()
			if c.change == nil {
				doc = nil
			} else {
				c.change(doc, &r)
			}
			if inv, err := HarnessInvocation(doc, r); err == nil {
				t.Errorf("HarnessInvocation = %v, want an error", inv)
			}
		})
	}
}

func TestHarnessInvocationWithoutAnOutput(t *testing.T) {
	doc, r := document(), resolved()
	doc.OutputMessage, r.JSONSchema = "", ""
	inv, err := HarnessInvocation(doc, r)
	if err != nil {
		t.Fatal(err)
	}
	if inv.GetOutputSchema() != nil {
		t.Errorf("the output schema is %v, want none for an invocation without structured output", inv.GetOutputSchema())
	}
}
