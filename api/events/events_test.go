// SPDX-License-Identifier: Apache-2.0
// SPDX-FileCopyrightText: The Pumi Authors

package events

import (
	"go/ast"
	"go/parser"
	"go/token"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"

	cloudeventsv1 "getpumi.dev/pumi/api/gen/io/cloudevents/v1"
	eventsv1 "getpumi.dev/pumi/api/gen/pumi/events/v1"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"
	"google.golang.org/protobuf/types/known/anypb"
	"google.golang.org/protobuf/types/known/durationpb"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// v1Types are the 21 event types of decision 0135.
var v1Types = []string{
	"plan.added", "transition.fired", "decision.made",
	"lease.granted", "lease.released", "lease.expired",
	"invocation.requested", "invocation.started", "invocation.result", "invocation.finished", "cancel.requested",
	"verdict.recorded", "warning.raised", "warning.reviewed",
	"approval.recorded", "escalation.raised", "escalation.resolved", "override.recorded",
	"merge.performed", "trunk.unattested", "check.ingested",
}

func TestTypesAreTheV1Types(t *testing.T) {
	want := []string{}
	for _, name := range v1Types {
		want = append(want, "dev.getpumi."+name)
	}
	slices.Sort(want)
	if got := Types(); !slices.Equal(got, want) {
		t.Errorf("Types() = %q, want %q", got, want)
	}
	// The budget of decision 0017.
	if n := len(Types()); n > 25 {
		t.Errorf("%d event types, more than the budget of 25 (decision 0017)", n)
	}
}

// typeConstants returns the values of the constants in events.go whose names
// start with Type, other than TypePrefix.
func typeConstants(t *testing.T) []string {
	t.Helper()
	file, err := parser.ParseFile(token.NewFileSet(), "events.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	values := []string{}
	for _, decl := range file.Decls {
		if gen, ok := decl.(*ast.GenDecl); ok && gen.Tok == token.CONST {
			values = append(values, typeValues(t, gen)...)
		}
	}
	slices.Sort(values)
	return values
}

// typeValues returns the values of the Type constants in one const
// declaration.
func typeValues(t *testing.T, gen *ast.GenDecl) []string {
	t.Helper()
	values := []string{}
	for _, spec := range gen.Specs {
		vs, ok := spec.(*ast.ValueSpec)
		if !ok {
			continue
		}
		for i, name := range vs.Names {
			if strings.HasPrefix(name.Name, "Type") && name.Name != "TypePrefix" {
				values = append(values, constantValue(t, name.Name, vs.Values[i]))
			}
		}
	}
	return values
}

// constantValue returns the value of a constant written as TypePrefix + "name".
func constantValue(t *testing.T, name string, expr ast.Expr) string {
	t.Helper()
	bin, ok := expr.(*ast.BinaryExpr)
	if !ok {
		t.Fatalf("%s is not written as TypePrefix + \"name\"", name)
	}
	prefix, okPrefix := bin.X.(*ast.Ident)
	lit, okLit := bin.Y.(*ast.BasicLit)
	if !okPrefix || prefix.Name != "TypePrefix" || !okLit || lit.Kind != token.STRING {
		t.Fatalf("%s is not written as TypePrefix + \"name\"", name)
	}
	s, err := strconv.Unquote(lit.Value)
	if err != nil {
		t.Fatal(err)
	}
	return TypePrefix + s
}

// dataMessages returns the full names of the top-level messages of the
// pumi.events.v1 package other than Basis, sorted.
func dataMessages(t *testing.T) []string {
	t.Helper()
	names := []string{}
	protoregistry.GlobalFiles.RangeFilesByPackage("pumi.events.v1", func(fd protoreflect.FileDescriptor) bool {
		for i := range fd.Messages().Len() {
			if m := fd.Messages().Get(i); m.Name() != "Basis" {
				names = append(names, string(m.FullName()))
			}
		}
		return true
	})
	if len(names) == 0 {
		t.Fatal("no messages registered for package pumi.events.v1")
	}
	slices.Sort(names)
	return names
}

// TestEveryTypeHasDataAndEveryDataHasAType checks that the Type constants,
// Types and the data messages agree, so a type cannot be added without a data
// message, or a data message without a type.
func TestEveryTypeHasDataAndEveryDataHasAType(t *testing.T) {
	if got, want := typeConstants(t), Types(); !slices.Equal(got, want) {
		t.Errorf("Type constants in events.go = %q, want Types() = %q", got, want)
	}
	mapped := []string{}
	for _, typ := range Types() {
		msg, err := Data(typ)
		if err != nil {
			t.Errorf("Data(%q): %v", typ, err)
			continue
		}
		got, err := TypeOf(msg)
		if err != nil || got != typ {
			t.Errorf("TypeOf(Data(%q)) = %q, %v; want %q", typ, got, err, typ)
		}
		mapped = append(mapped, string(msg.ProtoReflect().Descriptor().FullName()))
	}
	slices.Sort(mapped)
	if want := dataMessages(t); !slices.Equal(mapped, want) {
		t.Errorf("messages of the types = %q, want the data messages of pumi.events.v1 = %q", mapped, want)
	}
}

func TestExtensionNames(t *testing.T) {
	got := []string{ExtensionTenant, ExtensionSchemaVersion, ExtensionCause, ExtensionPrevHash}
	want := []string{"tenant", "schemaversion", "cause", "prevhash"}
	if !slices.Equal(got, want) {
		t.Errorf("extension names = %q, want %q", got, want)
	}
	// The CloudEvents specification's rule for attribute names (decision 0133).
	valid := regexp.MustCompile(`^[a-z0-9]{1,20}$`)
	for _, name := range got {
		if !valid.MatchString(name) {
			t.Errorf("extension name %q is not 1 to 20 lower-case letters and digits", name)
		}
	}
	if SchemaVersion == "" {
		t.Error("SchemaVersion is empty")
	}
}

// attrs are valid attributes with every optional attribute set.
func attrs() Attributes {
	return Attributes{
		ID:       "event-1",
		Source:   "/kernel",
		Subject:  "plan-1",
		Tenant:   "tenant-1",
		Cause:    "event-0",
		PrevHash: "sha256:" + strings.Repeat("ab", 32),
	}
}

func stringAttribute(t *testing.T, e *cloudeventsv1.CloudEvent, name string) string {
	t.Helper()
	v, ok := e.GetAttributes()[name]
	if !ok {
		t.Fatalf("attribute %q is missing", name)
	}
	s, ok := v.GetAttr().(*cloudeventsv1.CloudEvent_CloudEventAttributeValue_CeString)
	if !ok {
		t.Fatalf("attribute %q is %T, want a string", name, v.GetAttr())
	}
	return s.CeString
}

func TestNewBuildsTheEnvelope(t *testing.T) {
	data := &eventsv1.PlanAdded{Plan: "plan-1", Track: "main"}
	e, err := New(data, attrs())
	if err != nil {
		t.Fatal(err)
	}
	if e.GetSpecVersion() != "1.0" || e.GetId() != "event-1" || e.GetSource() != "/kernel" || e.GetType() != TypePlanAdded {
		t.Errorf("spec version, id, source, type = %q, %q, %q, %q", e.GetSpecVersion(), e.GetId(), e.GetSource(), e.GetType())
	}
	for name, want := range map[string]string{
		"subject":              "plan-1",
		ExtensionTenant:        "tenant-1",
		ExtensionSchemaVersion: SchemaVersion,
		ExtensionCause:         "event-0",
		ExtensionPrevHash:      attrs().PrevHash,
	} {
		if got := stringAttribute(t, e, name); got != want {
			t.Errorf("attribute %q = %q, want %q", name, got, want)
		}
	}
	got := &eventsv1.PlanAdded{}
	if err := e.GetProtoData().UnmarshalTo(got); err != nil {
		t.Fatal(err)
	}
	if !proto.Equal(got, data) {
		t.Errorf("data = %v, want %v", got, data)
	}
}

func TestNewLeavesOutEmptyOptionalAttributes(t *testing.T) {
	a := attrs()
	a.Subject, a.Cause, a.PrevHash = "", "", ""
	e, err := New(&eventsv1.TrunkUnattested{Commit: "c"}, a)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"subject", ExtensionCause, ExtensionPrevHash} {
		if _, ok := e.GetAttributes()[name]; ok {
			t.Errorf("attribute %q is set, want it left out", name)
		}
	}
}

func TestNewRefuses(t *testing.T) {
	tests := []struct {
		name  string
		data  proto.Message
		attrs func(*Attributes)
		want  string
	}{
		{"no tenant", &eventsv1.PlanAdded{}, func(a *Attributes) { a.Tenant = "" }, "tenant"},
		{"no id", &eventsv1.PlanAdded{}, func(a *Attributes) { a.ID = "" }, "id"},
		{"no source", &eventsv1.PlanAdded{}, func(a *Attributes) { a.Source = "" }, "source"},
		{"bad prevhash", &eventsv1.PlanAdded{}, func(a *Attributes) { a.PrevHash = "sha256:AB" }, "prevhash"},
		{"no data", nil, func(*Attributes) {}, "data"},
		{"not event data", &eventsv1.Basis{}, func(*Attributes) {}, "pumi.events.v1.Basis"},
		{"other message", durationpb.New(0), func(*Attributes) {}, "google.protobuf.Duration"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			a := attrs()
			tt.attrs(&a)
			e, err := New(tt.data, a)
			if err == nil {
				t.Fatalf("New() = %v, want an error", e)
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Errorf("New() error = %q, want it to name %q", err, tt.want)
			}
		})
	}
}

// valid returns a valid envelope of plan.added.
func valid(t *testing.T) *cloudeventsv1.CloudEvent {
	t.Helper()
	e, err := New(&eventsv1.PlanAdded{Plan: "plan-1"}, attrs())
	if err != nil {
		t.Fatal(err)
	}
	return e
}

func TestValidateRefuses(t *testing.T) {
	other, err := anypb.New(&eventsv1.TrunkUnattested{})
	if err != nil {
		t.Fatal(err)
	}
	integer := &cloudeventsv1.CloudEvent_CloudEventAttributeValue{
		Attr: &cloudeventsv1.CloudEvent_CloudEventAttributeValue_CeInteger{CeInteger: 1},
	}
	tests := []struct {
		name   string
		change func(*cloudeventsv1.CloudEvent)
		want   string
	}{
		{"nil", nil, "nil"},
		{"spec version", func(e *cloudeventsv1.CloudEvent) { e.SpecVersion = "0.3" }, "spec version"},
		{"no id", func(e *cloudeventsv1.CloudEvent) { e.Id = "" }, "id"},
		{"no source", func(e *cloudeventsv1.CloudEvent) { e.Source = "" }, "source"},
		{"unknown type", func(e *cloudeventsv1.CloudEvent) { e.Type = "dev.getpumi.plan.removed" }, "dev.getpumi.plan.removed"},
		{"no tenant", func(e *cloudeventsv1.CloudEvent) { delete(e.Attributes, ExtensionTenant) }, "tenant"},
		{"empty tenant", func(e *cloudeventsv1.CloudEvent) { e.Attributes[ExtensionTenant] = str("") }, "tenant"},
		{"tenant not a string", func(e *cloudeventsv1.CloudEvent) { e.Attributes[ExtensionTenant] = integer }, "tenant"},
		{"no schemaversion", func(e *cloudeventsv1.CloudEvent) { delete(e.Attributes, ExtensionSchemaVersion) }, "schemaversion"},
		{"empty cause", func(e *cloudeventsv1.CloudEvent) { e.Attributes[ExtensionCause] = str("") }, "cause"},
		{"bad prevhash", func(e *cloudeventsv1.CloudEvent) { e.Attributes[ExtensionPrevHash] = str("md5:00") }, "prevhash"},
		{"source not a URI reference", func(e *cloudeventsv1.CloudEvent) { e.Source = "kernel\nrunner" }, "source"},
		{"basis extension", func(e *cloudeventsv1.CloudEvent) { e.Attributes["basis"] = str("x") }, "basis"},
		{"misspelled extension", func(e *cloudeventsv1.CloudEvent) { e.Attributes["PrevHash"] = str("x") }, "PrevHash"},
		{"schemaversion not a version", func(e *cloudeventsv1.CloudEvent) { e.Attributes[ExtensionSchemaVersion] = str("banana") }, "schemaversion"},
		{"schemaversion of another major version", func(e *cloudeventsv1.CloudEvent) {
			e.Attributes[ExtensionSchemaVersion] = str("2.0.0")
		}, "schemaversion"},
		{"data type URL of another host", func(e *cloudeventsv1.CloudEvent) {
			e.GetProtoData().TypeUrl = "example.com/pumi.events.v1.PlanAdded"
		}, "type URL"},
		{"corrupt data", func(e *cloudeventsv1.CloudEvent) { e.GetProtoData().Value = []byte{0xff, 0xff} }, "data"},
		{"override without a reason", func(e *cloudeventsv1.CloudEvent) {
			e.Type = TypeOverrideRecorded
			data, err := anypb.New(&eventsv1.OverrideRecorded{Person: "p", Plan: "plan-1", Gate: "g", Tree: "t"})
			if err != nil {
				t.Fatal(err)
			}
			e.Data = &cloudeventsv1.CloudEvent_ProtoData{ProtoData: data}
		}, "reason"},
		{"text data", func(e *cloudeventsv1.CloudEvent) {
			e.Data = &cloudeventsv1.CloudEvent_TextData{TextData: "{}"}
		}, "protobuf data"},
		{"data of another type", func(e *cloudeventsv1.CloudEvent) {
			e.Data = &cloudeventsv1.CloudEvent_ProtoData{ProtoData: other}
		}, "pumi.events.v1.TrunkUnattested"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var e *cloudeventsv1.CloudEvent
			if tt.change != nil {
				e = valid(t)
				tt.change(e)
			}
			err := Validate(e)
			if err == nil {
				t.Fatal("Validate() = nil, want an error")
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Errorf("Validate() error = %q, want it to name %q", err, tt.want)
			}
		})
	}
}

func TestValidatePassesAValidEnvelope(t *testing.T) {
	if err := Validate(valid(t)); err != nil {
		t.Errorf("Validate() = %v", err)
	}
}

// TestValidatePassesOptionalCloudEventsAttributes checks that the optional
// attributes of the CloudEvents specification are accepted, and a later minor
// version of the schema.
func TestValidatePassesOptionalCloudEventsAttributes(t *testing.T) {
	e := valid(t)
	e.Attributes["time"] = &cloudeventsv1.CloudEvent_CloudEventAttributeValue{
		Attr: &cloudeventsv1.CloudEvent_CloudEventAttributeValue_CeTimestamp{CeTimestamp: timestamppb.Now()},
	}
	e.Attributes["datacontenttype"] = str("application/protobuf")
	e.Attributes["dataschema"] = str("https://getpumi.dev/schema")
	e.Attributes[ExtensionSchemaVersion] = str("1.4.0")
	if err := Validate(e); err != nil {
		t.Errorf("Validate() = %v", err)
	}
}

// fill sets every field of m, recursively, so a round trip covers every
// field of every data message.
func fill(t *testing.T, m protoreflect.Message) {
	t.Helper()
	fields := m.Descriptor().Fields()
	for i := range fields.Len() {
		fd := fields.Get(i)
		switch {
		case fd.IsList():
			list := m.Mutable(fd).List()
			list.Append(value(t, m, fd, list.NewElement()))
		default:
			m.Set(fd, value(t, m, fd, protoreflect.Value{}))
		}
	}
}

// value returns a value for fd that is not the zero value. element is a new
// element for a list of messages.
func value(t *testing.T, m protoreflect.Message, fd protoreflect.FieldDescriptor, element protoreflect.Value) protoreflect.Value {
	t.Helper()
	if fd.Kind() == protoreflect.MessageKind {
		return messageValue(t, m, fd, element)
	}
	if fd.Kind() == protoreflect.StringKind {
		return protoreflect.ValueOfString("value-of-" + string(fd.Name()))
	}
	if fd.Kind() == protoreflect.BytesKind {
		return protoreflect.ValueOfBytes([]byte("value-of-" + string(fd.Name())))
	}
	if fd.Kind() == protoreflect.Int64Kind {
		return protoreflect.ValueOfInt64(int64(fd.Number()) + 40)
	}
	if fd.Kind() == protoreflect.Int32Kind {
		return protoreflect.ValueOfInt32(int32(fd.Number()) + 40)
	}
	if fd.Kind() == protoreflect.EnumKind {
		return protoreflect.ValueOfEnum(fd.Enum().Values().Get(1).Number())
	}
	t.Fatalf("%s: the test does not fill a field of kind %s", fd.FullName(), fd.Kind())
	return protoreflect.Value{}
}

// messageValue returns a filled message for fd. A google.protobuf.Any, as in
// Basis, holds a Duration in place of a fact.
func messageValue(t *testing.T, m protoreflect.Message, fd protoreflect.FieldDescriptor, element protoreflect.Value) protoreflect.Value {
	t.Helper()
	if fd.Message().FullName() == "google.protobuf.Any" {
		fact, err := anypb.New(durationpb.New(3))
		if err != nil {
			t.Fatal(err)
		}
		return protoreflect.ValueOfMessage(fact.ProtoReflect())
	}
	msg := element
	if !msg.IsValid() {
		msg = m.NewField(fd)
	}
	fill(t, msg.Message())
	return msg
}

// TestRoundTrips writes an envelope of every type, with every field of its
// data set, in protojson, as the embedded log stores it, and in the
// CloudEvents protobuf format, reads it back and checks that nothing changed.
func TestRoundTrips(t *testing.T) {
	for _, typ := range Types() {
		t.Run(typ, func(t *testing.T) {
			data, err := Data(typ)
			if err != nil {
				t.Fatal(err)
			}
			fill(t, data.ProtoReflect())
			if r, ok := data.(*eventsv1.InvocationRequested); ok {
				// The document must match the other fields (decision 0165).
				filled, err := InvocationRequested("plan-1", document())
				if err != nil {
					t.Fatal(err)
				}
				proto.Reset(r)
				proto.Merge(r, filled)
			}
			e, err := New(data, attrs())
			if err != nil {
				t.Fatal(err)
			}
			checkRoundTrip(t, "protojson", e, data, fromJSON(t, e))
			checkRoundTrip(t, "protobuf", e, data, fromBinary(t, e))
		})
	}
}

// fromJSON writes e with protojson and reads it back.
func fromJSON(t *testing.T, e *cloudeventsv1.CloudEvent) *cloudeventsv1.CloudEvent {
	t.Helper()
	js, err := protojson.Marshal(e)
	if err != nil {
		t.Fatal(err)
	}
	got := &cloudeventsv1.CloudEvent{}
	if err := protojson.Unmarshal(js, got); err != nil {
		t.Fatal(err)
	}
	return got
}

// fromBinary writes e in the protobuf format and reads it back.
func fromBinary(t *testing.T, e *cloudeventsv1.CloudEvent) *cloudeventsv1.CloudEvent {
	t.Helper()
	bin, err := proto.Marshal(e)
	if err != nil {
		t.Fatal(err)
	}
	got := &cloudeventsv1.CloudEvent{}
	if err := proto.Unmarshal(bin, got); err != nil {
		t.Fatal(err)
	}
	return got
}

// checkRoundTrip checks that got, read back in format, equals want, passes
// Validate and holds data.
func checkRoundTrip(t *testing.T, format string, want *cloudeventsv1.CloudEvent, data proto.Message, got *cloudeventsv1.CloudEvent) {
	t.Helper()
	if !proto.Equal(got, want) {
		t.Errorf("%s round trip changed the envelope:\ngot  %v\nwant %v", format, got, want)
	}
	if err := Validate(got); err != nil {
		t.Errorf("%s round trip: Validate() = %v", format, err)
	}
	read, err := got.GetProtoData().UnmarshalNew()
	if err != nil {
		t.Fatal(err)
	}
	if !proto.Equal(read, data) {
		t.Errorf("%s round trip changed the data:\ngot  %v\nwant %v", format, read, data)
	}
}

func str(s string) *cloudeventsv1.CloudEvent_CloudEventAttributeValue {
	return &cloudeventsv1.CloudEvent_CloudEventAttributeValue{
		Attr: &cloudeventsv1.CloudEvent_CloudEventAttributeValue_CeString{CeString: s},
	}
}
