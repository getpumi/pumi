// SPDX-License-Identifier: Apache-2.0
// SPDX-FileCopyrightText: The Pumi Authors

package api

import (
	"slices"
	"strings"
	"testing"

	harnessv1 "getpumi.dev/pumi/api/gen/pumi/harness/v1"
	"google.golang.org/protobuf/reflect/protoreflect"
)

// valueNames returns the names of the values of e, in order.
func valueNames(e protoreflect.EnumDescriptor) []string {
	names := []string{}
	for i := range e.Values().Len() {
		names = append(names, string(e.Values().Get(i).Name()))
	}
	return names
}

func TestHarnessHasNoService(t *testing.T) {
	if n := harnessv1.File_pumi_harness_v1_harness_proto.Services().Len(); n != 0 {
		t.Errorf("pumi.harness.v1 has %d services, want none (decision 0152)", n)
	}
}

func TestTheHarnessInvocationHasTheFieldsOf0158(t *testing.T) {
	inv := (&harnessv1.Invocation{}).ProtoReflect().Descriptor()
	want := []string{
		"budget", "capabilities", "component", "gateway", "idempotency_key", "input",
		"invocation", "model_role", "output_schema", "role", "workspace",
	}
	if got := fieldNames(inv); !slices.Equal(got, want) {
		t.Errorf("Invocation has %q, want %q (decisions 0158 and 0233)", got, want)
	}
	gateway := fieldOf(t, inv, "gateway").Message()
	if got := fieldNames(gateway); !slices.Equal(got, []string{"base_url", "keeps_sign_in", "token"}) {
		t.Errorf("Gateway has %q, want base_url, keeps_sign_in and token (decisions 0036 and 0225)", got)
	}
	if f := gateway.Fields().ByName("keeps_sign_in"); f != nil && f.Kind() != protoreflect.BoolKind {
		t.Error("Gateway.keeps_sign_in is not a bool")
	}
	if got := fieldNames(fieldOf(t, inv, "output_schema").Message()); !slices.Equal(got, []string{"json_schema", "message"}) {
		t.Errorf("OutputSchema has %q, want the message's full name and its JSON Schema (decision 0160)", got)
	}
	if got, want := valueNames(fieldOf(t, inv, "role").Enum()), []string{"ROLE_UNSPECIFIED", "ROLE_GATE", "ROLE_AGENT"}; !slices.Equal(got, want) {
		t.Errorf("Role is %q, want %q", got, want)
	}
}

func TestTheHarnessInvocationHoldsNoRunnerOnlyField(t *testing.T) {
	inv := (&harnessv1.Invocation{}).ProtoReflect().Descriptor()
	fieldsOf(inv, "Invocation", map[protoreflect.FullName]bool{}, func(path string, f protoreflect.FieldDescriptor) {
		name := strings.ToLower(string(f.Name()))
		for _, part := range []string{"credential", "handle", "image", "digest", "isolation", "secret", "key"} {
			if strings.Contains(name, part) && name != "idempotency_key" {
				t.Errorf("%s looks like a field only the runner needs (decision 0158)", path)
			}
		}
	})
}

func TestTheCapabilityListIsThatOf0159(t *testing.T) {
	caps := (&harnessv1.Capabilities{}).ProtoReflect().Descriptor()
	if got, want := fieldNames(caps), []string{"commands", "network", "tools", "write_paths"}; !slices.Equal(got, want) {
		t.Fatalf("Capabilities has %q, want %q", got, want)
	}
	for _, name := range []string{"write_paths", "commands"} {
		if f := fieldOf(t, caps, name); !f.IsList() || f.Kind() != protoreflect.StringKind {
			t.Errorf("Capabilities.%s is not a list of strings", name)
		}
	}
	network := fieldOf(t, caps, "network").Message()
	if got := fieldNames(network); !slices.Equal(got, []string{"access", "hosts"}) {
		t.Errorf("Network has %q, want access and hosts", got)
	}
	if got, want := valueNames(fieldOf(t, network, "access").Enum()), []string{"ACCESS_UNSPECIFIED", "ACCESS_NONE", "ACCESS_GATEWAY", "ACCESS_HOSTS"}; !slices.Equal(got, want) {
		t.Errorf("Network.Access is %q, want %q", got, want)
	}
	tools := fieldOf(t, caps, "tools")
	if got, want := valueNames(tools.Enum()), []string{"TOOL_KIND_UNSPECIFIED", "TOOL_KIND_READ", "TOOL_KIND_EDIT", "TOOL_KIND_RUN_COMMANDS", "TOOL_KIND_WEB"}; !tools.IsList() || !slices.Equal(got, want) {
		t.Errorf("Capabilities.tools is %q (list %v), want a list of %q", got, tools.IsList(), want)
	}
}

func TestTheResultIsThatOf0160(t *testing.T) {
	result := (&harnessv1.Result{}).ProtoReflect().Descriptor()
	if got, want := fieldNames(result), []string{"output", "output_files", "status", "usage"}; !slices.Equal(got, want) {
		t.Fatalf("Result has %q, want %q", got, want)
	}
	if got, want := valueNames(fieldOf(t, result, "status").Enum()), []string{"STATUS_UNSPECIFIED", "STATUS_COMPLETED", "STATUS_FAILED", "STATUS_STOPPED"}; !slices.Equal(got, want) {
		t.Errorf("Result.Status is %q, want %q", got, want)
	}
	if f := fieldOf(t, result, "output"); f.Kind() != protoreflect.StringKind {
		t.Error("Result.output is not a string of JSON")
	}
	if f := fieldOf(t, result, "output_files"); !f.IsList() || f.Kind() != protoreflect.StringKind {
		t.Error("Result.output_files is not a list of paths")
	}
	if fieldOf(t, result, "usage").Message() != (&harnessv1.Usage{}).ProtoReflect().Descriptor() {
		t.Error("Result.usage is not a Usage")
	}
}
