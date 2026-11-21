// SPDX-License-Identifier: Apache-2.0
// SPDX-FileCopyrightText: The Pumi Authors

package api

import (
	"slices"
	"strings"
	"testing"

	controlplanev1 "getpumi.dev/pumi/api/gen/pumi/controlplane/v1"
	"getpumi.dev/pumi/api/gen/pumi/controlplane/v1/controlplanev1connect"
	"google.golang.org/protobuf/reflect/protoreflect"
)

// controlPlaneService is the descriptor of ControlPlaneService.
func controlPlaneService(t *testing.T) protoreflect.ServiceDescriptor {
	t.Helper()
	svc := controlplanev1.File_pumi_controlplane_v1_controlplane_proto.Services().ByName("ControlPlaneService")
	if svc == nil {
		t.Fatal("pumi.controlplane.v1 has no ControlPlaneService")
	}
	if got, want := string(svc.FullName()), controlplanev1connect.ControlPlaneServiceName; got != want {
		t.Fatalf("the service is %s, but the Connect code serves %s", got, want)
	}
	return svc
}

func TestControlPlaneServiceHasTheFourVerbs(t *testing.T) {
	methods := controlPlaneService(t).Methods()
	got := []string{}
	for i := range methods.Len() {
		m := methods.Get(i)
		got = append(got, string(m.Name()))
		if m.IsStreamingClient() {
			t.Errorf("%s takes a stream from the client", m.Name())
		}
		if want := m.Name() == "Subscribe"; m.IsStreamingServer() != want {
			t.Errorf("%s: streams from the server = %v, want %v", m.Name(), m.IsStreamingServer(), want)
		}
	}
	slices.Sort(got)
	if want := []string{"Append", "Decide", "Query", "Subscribe"}; !slices.Equal(got, want) {
		t.Errorf("ControlPlaneService has %q, want the four verbs %q (decision 0006)", got, want)
	}
}

// fieldsOf calls visit with the path and descriptor of every field of msg
// and of the messages it holds, each message once.
func fieldsOf(msg protoreflect.MessageDescriptor, path string, seen map[protoreflect.FullName]bool, visit func(string, protoreflect.FieldDescriptor)) {
	if seen[msg.FullName()] {
		return
	}
	seen[msg.FullName()] = true
	fields := msg.Fields()
	for i := range fields.Len() {
		f := fields.Get(i)
		p := path + "." + string(f.Name())
		visit(p, f)
		if f.Message() != nil {
			fieldsOf(f.Message(), p, seen, visit)
		}
	}
}

func TestNoRequestNamesItsPrincipalOrTenant(t *testing.T) {
	methods := controlPlaneService(t).Methods()
	for i := range methods.Len() {
		in := methods.Get(i).Input()
		fieldsOf(in, string(in.Name()), map[protoreflect.FullName]bool{}, func(path string, f protoreflect.FieldDescriptor) {
			name := strings.ToLower(string(f.Name()))
			if strings.Contains(name, "principal") || strings.Contains(name, "tenant") {
				t.Errorf("%s names the caller's principal or tenant, which come from its identity (decision 0156)", path)
			}
		})
	}
}

func TestDecideTakesNoFactValues(t *testing.T) {
	in := controlPlaneService(t).Methods().ByName("Decide").Input()
	keys := []string{}
	fieldsOf(in, string(in.Name()), map[protoreflect.FullName]bool{}, func(path string, f protoreflect.FieldDescriptor) {
		if m := f.Message(); m != nil {
			if pkg := m.ParentFile().Package(); pkg == "pumi.facts.v1" || pkg == "pumi.events.v1" || pkg == "google.protobuf" {
				t.Errorf("%s is a %s, which could carry fact values (decision 0155)", path, m.FullName())
			}
			return
		}
		keys = append(keys, path)
	})
	slices.Sort(keys)
	want := []string{
		"DecideRequest.action",
		"DecideRequest.keys.base_tree",
		"DecideRequest.keys.head_commit",
		"DecideRequest.keys.head_tree",
		"DecideRequest.keys.invocation",
		"DecideRequest.plan",
	}
	if !slices.Equal(keys, want) {
		t.Errorf("DecideRequest holds %q, want the action, the plan and the fact keys %q (decision 0155)", keys, want)
	}
}

// field returns the field name of the message name in pumi.controlplane.v1.
func field(t *testing.T, message, name string) protoreflect.FieldDescriptor {
	t.Helper()
	msgs := controlplanev1.File_pumi_controlplane_v1_controlplane_proto.Messages()
	var msg protoreflect.MessageDescriptor
	for _, part := range strings.Split(message, ".") {
		if msg == nil {
			msg = msgs.ByName(protoreflect.Name(part))
		} else {
			msg = msg.Messages().ByName(protoreflect.Name(part))
		}
		if msg == nil {
			t.Fatalf("pumi.controlplane.v1 has no message %s", message)
		}
	}
	f := msg.Fields().ByName(protoreflect.Name(name))
	if f == nil {
		t.Fatalf("%s has no field %s", message, name)
	}
	return f
}

func TestPositionsAndVersions(t *testing.T) {
	for _, c := range []struct {
		message, field string
		list           bool
	}{
		{"AppendResponse", "positions", true},
		{"Entry", "position", false},
		{"QueryRequest", "after", false},
		{"QueryResponse", "head", false},
		{"SubscribeRequest", "after", false},
		{"DecideResponse", "position", false},
		{"Condition", "version", false},
		{"AppendConflict.Subject", "current_version", false},
	} {
		f := field(t, c.message, c.field)
		if f.Kind() != protoreflect.Uint64Kind || f.IsList() != c.list {
			t.Errorf("%s.%s is %v (list %v), want uint64 (list %v)", c.message, c.field, f.Kind(), f.IsList(), c.list)
		}
	}
}

func TestQueryAndSubscribeTakeTheSameFilter(t *testing.T) {
	q, s := field(t, "QueryRequest", "filter"), field(t, "SubscribeRequest", "filter")
	if q.Message() == nil || q.Message() != s.Message() {
		t.Fatalf("QueryRequest.filter and SubscribeRequest.filter are not the same message")
	}
	for _, name := range []string{"subject", "types"} {
		if q.Message().Fields().ByName(protoreflect.Name(name)) == nil {
			t.Errorf("Filter has no field %s (decision 0154)", name)
		}
	}
	field(t, "QueryRequest", "limit")
	if field(t, "SubscribeResponse", "entry").Message() != field(t, "QueryResponse", "events").Message() {
		t.Error("Subscribe and Query do not return the same Entry")
	}
}

func TestAConditionIsAVersionOrNoEvents(t *testing.T) {
	version, none := field(t, "Condition", "version"), field(t, "Condition", "no_events")
	field(t, "Condition", "subject")
	if version.ContainingOneof() == nil || version.ContainingOneof() != none.ContainingOneof() {
		t.Error("Condition.version and Condition.no_events are not one choice")
	}
}
