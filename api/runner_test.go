// SPDX-License-Identifier: Apache-2.0
// SPDX-FileCopyrightText: The Pumi Authors

package api

import (
	"slices"
	"strings"
	"testing"

	eventsv1 "getpumi.dev/pumi/api/gen/pumi/events/v1"
	harnessv1 "getpumi.dev/pumi/api/gen/pumi/harness/v1"
	runnerv1 "getpumi.dev/pumi/api/gen/pumi/runner/v1"
	"getpumi.dev/pumi/api/gen/pumi/runner/v1/runnerv1connect"
	"google.golang.org/protobuf/reflect/protoreflect"
)

// runnerService is the descriptor of RunnerService.
func runnerService(t *testing.T) protoreflect.ServiceDescriptor {
	t.Helper()
	svc := runnerv1.File_pumi_runner_v1_runner_proto.Services().ByName("RunnerService")
	if svc == nil {
		t.Fatal("pumi.runner.v1 has no RunnerService")
	}
	if got, want := string(svc.FullName()), runnerv1connect.RunnerServiceName; got != want {
		t.Fatalf("the service is %s, but the Connect code serves %s", got, want)
	}
	return svc
}

// fieldOf returns the field name of the message md, failing the test when
// there is none.
func fieldOf(t *testing.T, md protoreflect.MessageDescriptor, name string) protoreflect.FieldDescriptor {
	t.Helper()
	f := md.Fields().ByName(protoreflect.Name(name))
	if f == nil {
		t.Fatalf("%s has no field %s", md.FullName(), name)
	}
	return f
}

// fieldNames returns the sorted names of the fields of md.
func fieldNames(md protoreflect.MessageDescriptor) []string {
	names := []string{}
	for i := range md.Fields().Len() {
		names = append(names, string(md.Fields().Get(i).Name()))
	}
	slices.Sort(names)
	return names
}

func TestRunnerServiceHasClaimHeartbeatReportAndSendLog(t *testing.T) {
	methods := runnerService(t).Methods()
	got := []string{}
	for i := range methods.Len() {
		m := methods.Get(i)
		got = append(got, string(m.Name()))
		if m.IsStreamingClient() || m.IsStreamingServer() {
			t.Errorf("%s streams; the runner loop uses only unary calls (decision 0031)", m.Name())
		}
	}
	slices.Sort(got)
	if want := []string{"Claim", "Heartbeat", "Report", "SendLog"}; !slices.Equal(got, want) {
		t.Errorf("RunnerService has %q, want %q (decisions 0152 and 0163)", got, want)
	}
}

func TestNoRunnerRequestNamesItsPrincipalOrTenant(t *testing.T) {
	methods := runnerService(t).Methods()
	for i := range methods.Len() {
		in := methods.Get(i).Input()
		fieldsOf(in, string(in.Name()), map[protoreflect.FullName]bool{}, func(path string, f protoreflect.FieldDescriptor) {
			name := strings.ToLower(string(f.Name()))
			if strings.Contains(name, "principal") || strings.Contains(name, "tenant") {
				t.Errorf("%s names the runner's principal or tenant, which come from its identity (decision 0156)", path)
			}
		})
	}
}

func TestClaimStatesTheProtocolVersions(t *testing.T) {
	m := runnerService(t).Methods().ByName("Claim")
	versions := fieldOf(t, m.Input(), "protocol_versions")
	if !versions.IsList() || versions.Message() == nil {
		t.Fatal("ClaimRequest.protocol_versions is not a list of messages (decision 0041)")
	}
	if got := fieldNames(versions.Message()); !slices.Equal(got, []string{"major", "minor"}) {
		t.Errorf("ProtocolVersion has %q, want major and minor", got)
	}
	out := m.Output()
	if fieldOf(t, out, "protocol_version").Message() != versions.Message() {
		t.Error("ClaimResponse.protocol_version is not a ProtocolVersion")
	}
	if fieldOf(t, out, "invocation").Message().FullName() != "pumi.runner.v1.Invocation" {
		t.Error("ClaimResponse.invocation is not the invocation document")
	}
	if fieldOf(t, out, "lease_number").Kind() != protoreflect.Int64Kind {
		t.Error("ClaimResponse.lease_number is not an int64, as in invocation.started")
	}
	fieldOf(t, out, "lease_timeout")
}

func TestHeartbeatCanCarryACancelRequest(t *testing.T) {
	m := runnerService(t).Methods().ByName("Heartbeat")
	if fieldOf(t, m.Input(), "lease_number").Kind() != protoreflect.Int64Kind {
		t.Error("HeartbeatRequest.lease_number is not an int64")
	}
	fieldOf(t, m.Input(), "invocation")
	cancel := fieldOf(t, m.Output(), "cancel")
	if cancel.Message() == nil || !cancel.HasPresence() {
		t.Error("HeartbeatResponse.cancel is not a message that can be unset (decision 0035)")
	}
	fieldOf(t, m.Output(), "lease_timeout")
}

func TestReportCarriesTheLeaseNumberAndTheResult(t *testing.T) {
	in := runnerService(t).Methods().ByName("Report").Input()
	if fieldOf(t, in, "lease_number").Kind() != protoreflect.Int64Kind {
		t.Error("ReportRequest.lease_number is not an int64 (decision 0035)")
	}
	fieldOf(t, in, "invocation")
	if got := fieldOf(t, in, "result").Message(); got != (&eventsv1.InvocationResult{}).ProtoReflect().Descriptor() {
		t.Errorf("ReportRequest.result is %v, want the data of invocation.result", got.FullName())
	}
	result := fieldOf(t, in, "result").Message()
	for _, name := range []string{"tree", "output"} {
		if f := fieldOf(t, result, name); f.Kind() != protoreflect.StringKind || f.IsList() {
			t.Errorf("InvocationResult.%s is not a string (decision 0162)", name)
		}
	}
	if got := fieldOf(t, in, "outcome").Enum(); got != eventsv1.InvocationFinished_OUTCOME_UNSPECIFIED.Descriptor() {
		t.Errorf("ReportRequest.outcome is %v, want the outcome of invocation.finished", got.FullName())
	}
	if f := fieldOf(t, in, "events"); !f.IsList() || f.Message().FullName() != "io.cloudevents.v1.CloudEvent" {
		t.Error("ReportRequest.events is not a list of CloudEvents (decision 0156)")
	}
}

func TestTheInvocationResultCarriesTheRefusals(t *testing.T) {
	result := (&eventsv1.InvocationResult{}).ProtoReflect().Descriptor()
	f := fieldOf(t, result, "refusals")
	if !f.IsList() || f.Message() != (&harnessv1.Refusal{}).ProtoReflect().Descriptor() {
		t.Error("InvocationResult.refusals is not a list of pumi.harness.v1.Refusal (decision 0249)")
	}
}

func TestSendLogCarriesTheLeaseNumberAndTheOutput(t *testing.T) {
	m := runnerService(t).Methods().ByName("SendLog")
	if m == nil {
		t.Fatal("RunnerService has no SendLog (decision 0163)")
	}
	in := m.Input()
	if fieldOf(t, in, "lease_number").Kind() != protoreflect.Int64Kind {
		t.Error("SendLogRequest.lease_number is not an int64 (decision 0163)")
	}
	if f := fieldOf(t, in, "invocation"); f.Kind() != protoreflect.StringKind {
		t.Error("SendLogRequest.invocation is not a string")
	}
	if f := fieldOf(t, in, "output"); f.Kind() != protoreflect.BytesKind || f.IsList() {
		t.Error("SendLogRequest.output is not bytes (decision 0034)")
	}
}

func TestTheInvocationDocumentHasEveryFieldOf0024(t *testing.T) {
	doc := (&runnerv1.Invocation{}).ProtoReflect().Descriptor()
	want := []string{
		"budget", "capabilities", "component", "credential_handles", "idempotency_key",
		"image_digest", "input", "invocation", "isolation", "model_role", "output_message", "returns_tree", "role",
	}
	if got := fieldNames(doc); !slices.Equal(got, want) {
		t.Errorf("the invocation document has %q, want %q (decisions 0024, 0233 and 0282)", got, want)
	}
	component := fieldOf(t, doc, "component").Message()
	if got := fieldNames(component); !slices.Equal(got, []string{"kind", "name", "version"}) {
		t.Errorf("Component has %q, want kind, name and version", got)
	}
	kinds := []string{}
	for i := range fieldOf(t, component, "kind").Enum().Values().Len() {
		kinds = append(kinds, string(fieldOf(t, component, "kind").Enum().Values().Get(i).Name()))
	}
	if want := []string{"KIND_UNSPECIFIED", "KIND_CHECK", "KIND_EXTERNAL_ACTION", "KIND_AGENT_RUN"}; !slices.Equal(kinds, want) {
		t.Errorf("Component.Kind is %q, want %q", kinds, want)
	}
	if got := fieldNames(fieldOf(t, doc, "input").Message()); !slices.Equal(got, []string{"repo", "task", "tree"}) {
		t.Errorf("Input has %q, want repo, task and tree", got)
	}
	if got := fieldNames(fieldOf(t, doc, "budget").Message()); !slices.Equal(got, []string{"cost_microdollars", "seconds", "tokens"}) {
		t.Errorf("the budget has %q, want tokens, cost_microdollars and seconds", got)
	}
	if f := fieldOf(t, doc, "credential_handles"); !f.IsList() || f.Kind() != protoreflect.StringKind {
		t.Error("credential_handles is not a list of strings")
	}
	if fieldOf(t, doc, "capabilities").Message() != (&harnessv1.Capabilities{}).ProtoReflect().Descriptor() {
		t.Error("the document's capabilities are not the capability list of pumi.harness.v1 (decision 0159)")
	}
}
