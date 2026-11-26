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
		"budget", "capabilities", "component", "fallback_model", "gateway", "idempotency_key", "input",
		"invocation", "model", "model_role", "output_schema", "read_paths", "role", "workspace",
	}
	if got := fieldNames(inv); !slices.Equal(got, want) {
		t.Errorf("Invocation has %q, want %q (decisions 0158, 0233, 0254 and 0269)", got, want)
	}
	if f := fieldOf(t, inv, "read_paths"); f.Kind() != protoreflect.StringKind || !f.IsList() {
		t.Errorf("read_paths is a %v, list %v; want a list of strings (decision 0269)", f.Kind(), f.IsList())
	}
	for _, name := range []string{"model", "fallback_model"} {
		if f := fieldOf(t, inv, name); f.Kind() != protoreflect.StringKind || f.IsList() {
			t.Errorf("Invocation.%s is not a string (decision 0254)", name)
		}
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
	if got, want := fieldNames(result), []string{"output", "output_files", "refusals", "status", "usage"}; !slices.Equal(got, want) {
		t.Fatalf("Result has %q, want %q (decisions 0160 and 0249)", got, want)
	}
	want := []string{"STATUS_UNSPECIFIED", "STATUS_COMPLETED", "STATUS_FAILED", "STATUS_STOPPED", "STATUS_LIMITED"}
	if got := valueNames(fieldOf(t, result, "status").Enum()); !slices.Equal(got, want) {
		t.Errorf("Result.Status is %q, want %q (decisions 0160 and 0249)", got, want)
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

func TestARefusalHasTheKindOfToolAndWhatWasAsked(t *testing.T) {
	result := (&harnessv1.Result{}).ProtoReflect().Descriptor()
	refusals := fieldOf(t, result, "refusals")
	if !refusals.IsList() || refusals.Message() != (&harnessv1.Refusal{}).ProtoReflect().Descriptor() {
		t.Fatal("Result.refusals is not a list of Refusal (decision 0249)")
	}
	refusal := refusals.Message()
	if got, want := fieldNames(refusal), []string{"command", "host", "path", "tool"}; !slices.Equal(got, want) {
		t.Fatalf("Refusal has %q, want %q", got, want)
	}
	if fieldOf(t, refusal, "tool").Enum() != harnessv1.Capabilities_TOOL_KIND_UNSPECIFIED.Descriptor() {
		t.Error("Refusal.tool is not a Capabilities.ToolKind")
	}
	for _, name := range []string{"path", "command", "host"} {
		f := fieldOf(t, refusal, name)
		if f.Kind() != protoreflect.StringKind || f.ContainingOneof() == nil || f.ContainingOneof().Name() != "asked" {
			t.Errorf("Refusal.%s is not a string of the oneof asked", name)
		}
	}
}

func TestEachLineOfStandardOutputIsAUsageFigureOrTheResult(t *testing.T) {
	line := (&harnessv1.Line{}).ProtoReflect().Descriptor()
	if got, want := fieldNames(line), []string{"result", "usage"}; !slices.Equal(got, want) {
		t.Fatalf("Line has %q, want %q (decision 0241)", got, want)
	}
	for name, md := range map[string]protoreflect.MessageDescriptor{
		"result": (&harnessv1.Result{}).ProtoReflect().Descriptor(),
		"usage":  (&harnessv1.UsageFigure{}).ProtoReflect().Descriptor(),
	} {
		f := fieldOf(t, line, name)
		if f.Message() != md || f.ContainingOneof() == nil || f.ContainingOneof().Name() != "kind" {
			t.Errorf("Line.%s is not a %s of the oneof kind", name, md.Name())
		}
	}
}

func TestAUsageFigureHasTheModelAndEveryKindOfTokenThePriceTablePrices(t *testing.T) {
	figure := (&harnessv1.UsageFigure{}).ProtoReflect().Descriptor()
	want := []string{
		"cache_read_tokens", "five_minute_cache_write_tokens", "inference_geo", "input_tokens", "model",
		"one_hour_cache_write_tokens", "output_tokens", "speed", "web_searches",
	}
	if got := fieldNames(figure); !slices.Equal(got, want) {
		t.Fatalf("UsageFigure has %q, want %q (decisions 0239 and 0241)", got, want)
	}
	for _, name := range want {
		f := fieldOf(t, figure, name)
		switch name {
		case "model", "speed", "inference_geo":
			if f.Kind() != protoreflect.StringKind {
				t.Errorf("UsageFigure.%s is not a string", name)
			}
		default:
			if f.Kind() != protoreflect.Int64Kind {
				t.Errorf("UsageFigure.%s is not an int64", name)
			}
		}
	}
}
