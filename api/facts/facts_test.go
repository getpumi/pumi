// SPDX-License-Identifier: Apache-2.0
// SPDX-FileCopyrightText: The Pumi Authors

package facts

import (
	"fmt"
	"maps"
	"regexp"
	"slices"
	"strings"
	"testing"

	factsv1 "getpumi.dev/pumi/api/gen/pumi/facts/v1"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protodesc"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"
	"google.golang.org/protobuf/types/descriptorpb"
)

// want is the catalog of decision 0136: Cedar type, keys and source of each
// fact, by name.
var want = map[string]struct {
	cedarType string
	keys      []string
	source    factsv1.Source
}{
	"verify.passed":      {"Bool", []string{"tree", "gate_version"}, factsv1.Source_SOURCE_MACHINE},
	"paths.protected":    {"Set<String>", []string{"base_tree", "head_tree"}, factsv1.Source_SOURCE_MACHINE},
	"paths.outside":      {"Set<String>", []string{"plan", "base_tree", "head_tree"}, factsv1.Source_SOURCE_MACHINE},
	"spec.valid":         {"Bool", []string{"tree", "spec_adapter_version"}, factsv1.Source_SOURCE_MACHINE},
	"plan.review":        {"Record", []string{"plan", "tree"}, factsv1.Source_SOURCE_MODEL},
	"ambiguity.opinions": {"Record", []string{"plan", "question"}, factsv1.Source_SOURCE_MODEL},
	"budgets.breached":   {"Set<String>", []string{"tree", "metrics_adapter_version"}, factsv1.Source_SOURCE_MACHINE},
	"conformance.audit":  {"Record", []string{"plan", "tree"}, factsv1.Source_SOURCE_MODEL},
	"approved.by":        {"Set<String>", []string{"tree"}, factsv1.Source_SOURCE_PERSON},
	"dco.signed":         {"Bool", []string{"head_commit"}, factsv1.Source_SOURCE_MACHINE},
	"attempts":           {"Long", []string{"plan", "gate"}, factsv1.Source_SOURCE_MACHINE},
	"warnings.open":      {"Long", []string{"project"}, factsv1.Source_SOURCE_MACHINE},
	"usage":              {"Record", []string{"invocation", "plan"}, factsv1.Source_SOURCE_MACHINE},
	"head.state":         {"Record", []string{"change"}, factsv1.Source_SOURCE_MACHINE},
	"outside.checks":     {"Set<String>", []string{"head_commit"}, factsv1.Source_SOURCE_MACHINE},
	"overrides":          {"Set<String>", []string{"plan", "tree"}, factsv1.Source_SOURCE_PERSON},
}

func TestCatalogIsTheV1Catalog(t *testing.T) {
	catalog, err := Catalog()
	if err != nil {
		t.Fatal(err)
	}
	names := []string{}
	for _, f := range catalog {
		names = append(names, f.Name)
		w, ok := want[f.Name]
		if !ok {
			t.Errorf("%s is not a fact of decision 0136", f.Name)
			continue
		}
		if f.CedarType != w.cedarType || f.Source != w.source || !slices.Equal(f.Keys, w.keys) {
			t.Errorf("%s: Cedar type %s, source %s, keys %q; want %s, %s, %q",
				f.Name, f.CedarType, f.Source, f.Keys, w.cedarType, w.source, w.keys)
		}
		if f.Producer == "" || f.Value != "value" {
			t.Errorf("%s: producer %q, value field %q", f.Name, f.Producer, f.Value)
		}
	}
	if len(names) != len(want) {
		t.Errorf("the catalog has %d facts %q, want the %d of decision 0136", len(names), names, len(want))
	}
	if !slices.IsSorted(names) {
		t.Errorf("Catalog() is not sorted by name: %q", names)
	}
}

// TestEveryMessageIsAFact checks that every top-level message of the
// pumi.facts.v1 package other than FactOptions is in the catalog, so a
// message cannot be added without a fact option.
func TestEveryMessageIsAFact(t *testing.T) {
	catalog, err := Catalog()
	if err != nil {
		t.Fatal(err)
	}
	inCatalog := map[protoreflect.FullName]bool{}
	for _, f := range catalog {
		inCatalog[f.Message.FullName()] = true
	}
	protoregistry.GlobalFiles.RangeFilesByPackage("pumi.facts.v1", func(fd protoreflect.FileDescriptor) bool {
		for i := range fd.Messages().Len() {
			m := fd.Messages().Get(i)
			if m.Name() != "FactOptions" && !inCatalog[m.FullName()] {
				t.Errorf("%s is not in the catalog", m.FullName())
			}
		}
		return true
	})
}

// TestModelFactsRecordTheirModels checks that the value of every fact from
// model judgment names the model of each verdict or opinion (decision 0137).
func TestModelFactsRecordTheirModels(t *testing.T) {
	catalog, err := Catalog()
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range catalog {
		if f.Source != factsv1.Source_SOURCE_MODEL {
			continue
		}
		value := f.Message.Fields().ByName(protoreflect.Name(f.Value))
		if !namesModels(value.Message()) {
			t.Errorf("%s: the value does not record a model", f.Name)
		}
	}
}

// namesModels reports whether md or a message in it has a field named model
// or models.
func namesModels(md protoreflect.MessageDescriptor) bool {
	return namesModelsIn(md, map[protoreflect.FullName]bool{})
}

// namesModelsIn is namesModels that skips the messages in seen, so that a
// recursive message ends the walk.
func namesModelsIn(md protoreflect.MessageDescriptor, seen map[protoreflect.FullName]bool) bool {
	if md == nil || seen[md.FullName()] {
		return false
	}
	seen[md.FullName()] = true
	for i := range md.Fields().Len() {
		fd := md.Fields().Get(i)
		if fd.Name() == "model" || fd.Name() == "models" || namesModelsIn(fd.Message(), seen) {
			return true
		}
	}
	return false
}

func TestCatalogVersionAndBudget(t *testing.T) {
	if !regexp.MustCompile(`^[0-9]+\.[0-9]+\.[0-9]+$`).MatchString(CatalogVersion) {
		t.Errorf("CatalogVersion = %q, want a semantic version", CatalogVersion)
	}
	// The budget of decision 0017.
	if Budget != 20 {
		t.Errorf("Budget = %d, want 20", Budget)
	}
}

// field is a field of a synthetic fact message. A message field has the type
// typeName, or pumi.facts.v1.FactOptions when typeName is empty. An optional
// field has explicit presence.
type field struct {
	name     string
	typ      descriptorpb.FieldDescriptorProto_Type
	repeated bool
	role     factsv1.Role
	typeName string
	optional bool
}

// synthetic builds a message named name in its own file of package
// pumi.facts.test, with the fact option opts, or none when opts is nil.
func synthetic(t *testing.T, name string, opts *factsv1.FactOptions, fields ...field) protoreflect.MessageDescriptor {
	t.Helper()
	return syntheticWith(t, name, opts, nil, fields...)
}

// syntheticWith is synthetic with the nested messages nested.
func syntheticWith(t *testing.T, name string, opts *factsv1.FactOptions, nested []*descriptorpb.DescriptorProto, fields ...field) protoreflect.MessageDescriptor {
	t.Helper()
	msg := &descriptorpb.DescriptorProto{Name: proto.String(name), NestedType: nested}
	if opts != nil {
		msg.Options = &descriptorpb.MessageOptions{}
		proto.SetExtension(msg.Options, factsv1.E_Fact, opts)
	}
	var number int32
	for _, f := range fields {
		number++
		msg.Field = append(msg.Field, fieldProto(msg, number, f))
	}
	file := &descriptorpb.FileDescriptorProto{
		Name:        proto.String("pumi/facts/test/" + strings.ToLower(name) + ".proto"),
		Package:     proto.String("pumi.facts.test"),
		Dependency:  []string{"pumi/facts/v1/options.proto"},
		MessageType: []*descriptorpb.DescriptorProto{msg},
		Syntax:      proto.String("proto3"),
	}
	fd, err := protodesc.NewFile(file, protoregistry.GlobalFiles)
	if err != nil {
		t.Fatal(err)
	}
	return fd.Messages().Get(0)
}

// fieldProto returns the field f of msg with the field number number, and
// adds the oneof that an optional field needs.
func fieldProto(msg *descriptorpb.DescriptorProto, number int32, f field) *descriptorpb.FieldDescriptorProto {
	label := descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL
	if f.repeated {
		label = descriptorpb.FieldDescriptorProto_LABEL_REPEATED
	}
	fdp := &descriptorpb.FieldDescriptorProto{
		Name:     proto.String(f.name),
		Number:   proto.Int32(number),
		Label:    label.Enum(),
		Type:     f.typ.Enum(),
		JsonName: proto.String(f.name),
	}
	if f.typ == descriptorpb.FieldDescriptorProto_TYPE_MESSAGE {
		fdp.TypeName = proto.String(".pumi.facts.v1.FactOptions")
		if f.typeName != "" {
			fdp.TypeName = proto.String(f.typeName)
		}
	}
	if f.optional {
		fdp.Proto3Optional = proto.Bool(true)
		var index int32
		for range msg.OneofDecl {
			index++
		}
		fdp.OneofIndex = proto.Int32(index)
		msg.OneofDecl = append(msg.OneofDecl, &descriptorpb.OneofDescriptorProto{Name: proto.String("_" + f.name)})
	}
	if f.role != factsv1.Role_ROLE_UNSPECIFIED {
		fdp.Options = &descriptorpb.FieldOptions{}
		proto.SetExtension(fdp.Options, factsv1.E_Role, f.role)
	}
	return fdp
}

var (
	tString = descriptorpb.FieldDescriptorProto_TYPE_STRING
	tBool   = descriptorpb.FieldDescriptorProto_TYPE_BOOL
	tInt64  = descriptorpb.FieldDescriptorProto_TYPE_INT64
	tMsg    = descriptorpb.FieldDescriptorProto_TYPE_MESSAGE
	key     = factsv1.Role_ROLE_KEY
	val     = factsv1.Role_ROLE_VALUE
)

// opts returns fact options for a valid Bool fact from a machine.
func opts() *factsv1.FactOptions {
	return &factsv1.FactOptions{Name: "test.fact", CedarType: "Bool", Producer: "a test", Source: factsv1.Source_SOURCE_MACHINE}
}

func TestDescribeReadsAFact(t *testing.T) {
	md := synthetic(t, "Good", opts(), field{"tree", tString, false, key, "", false}, field{"value", tBool, false, val, "", false})
	f, err := Describe(md)
	if err != nil {
		t.Fatal(err)
	}
	if f.Name != "test.fact" || f.CedarType != "Bool" || f.Producer != "a test" ||
		f.Source != factsv1.Source_SOURCE_MACHINE || !slices.Equal(f.Keys, []string{"tree"}) || f.Value != "value" {
		t.Errorf("Describe() = %+v", f)
	}
}

func TestDescribeRefuses(t *testing.T) {
	with := func(change func(*factsv1.FactOptions)) *factsv1.FactOptions {
		o := opts()
		change(o)
		return o
	}
	keyField := field{"tree", tString, false, key, "", false}
	boolValue := field{"value", tBool, false, val, "", false}
	tests := []struct {
		name   string
		opts   *factsv1.FactOptions
		fields []field
		want   string
	}{
		{"NoOption", nil, []field{keyField, boolValue}, "no fact option"},
		{"NoName", with(func(o *factsv1.FactOptions) { o.Name = "" }), []field{keyField, boolValue}, "no name"},
		{"NoProducer", with(func(o *factsv1.FactOptions) { o.Producer = "" }), []field{keyField, boolValue}, "no producer"},
		{"NoSource", with(func(o *factsv1.FactOptions) { o.Source = factsv1.Source_SOURCE_UNSPECIFIED }), []field{keyField, boolValue}, "no source"},
		{"NoKey", opts(), []field{boolValue}, "no key"},
		{"NoValue", opts(), []field{keyField}, "one value"},
		{"TwoValues", opts(), []field{keyField, boolValue, {"other", tBool, false, val, "", false}}, "one value"},
		{"FieldWithoutRole", opts(), []field{keyField, boolValue, {"extra", tString, false, factsv1.Role_ROLE_UNSPECIFIED, "", false}}, "extra"},
		{"RepeatedKey", opts(), []field{{"trees", tString, true, key, "", false}, boolValue}, "trees"},
		{"BoolNotBool", opts(), []field{keyField, {"value", tInt64, false, val, "", false}}, "Bool"},
		{"LongNotLong", with(func(o *factsv1.FactOptions) { o.CedarType = "Long" }), []field{keyField, boolValue}, "Long"},
		{"SetNotRepeated", with(func(o *factsv1.FactOptions) { o.CedarType = "Set<String>" }), []field{keyField, {"value", tString, false, val, "", false}}, "Set<String>"},
		{"RecordNotMessage", with(func(o *factsv1.FactOptions) { o.CedarType = "Record" }), []field{keyField, boolValue}, "Record"},
		{"RecordRepeated", with(func(o *factsv1.FactOptions) { o.CedarType = "Record" }), []field{keyField, {"value", tMsg, true, val, "", false}}, "Record"},
		{"UnknownCedarType", with(func(o *factsv1.FactOptions) { o.CedarType = "Decimal" }), []field{keyField, boolValue}, "Decimal"},
		{"UnknownRole", opts(), []field{keyField, boolValue, {name: "extra", typ: tString, role: factsv1.Role(3)}}, "extra"},
		{"OptionalKey", opts(), []field{{name: "tree", typ: tString, role: key, optional: true}, boolValue}, "tree"},
		// A record whose field is an enum: FactOptions has the enum source.
		{"RecordWithEnum", record(), []field{keyField, {"value", tMsg, false, val, "", false}}, "source"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			refuses(t, synthetic(t, tt.name, tt.opts, tt.fields...), tt.want)
		})
	}
}

// refuses checks that Describe fails on md with an error that names want.
func refuses(t *testing.T, md protoreflect.MessageDescriptor, want string) {
	t.Helper()
	f, err := Describe(md)
	if err == nil {
		t.Fatalf("Describe() = %+v, want an error", f)
	}
	if !strings.Contains(err.Error(), want) {
		t.Errorf("Describe() error = %q, want it to name %q", err, want)
	}
}

// record returns fact options for a valid Record fact from a machine.
func record() *factsv1.FactOptions {
	o := opts()
	o.CedarType = "Record"
	return o
}

// nestedMessage returns a message named name with the fields fields.
func nestedMessage(name string, fields ...field) *descriptorpb.DescriptorProto {
	msg := &descriptorpb.DescriptorProto{Name: proto.String(name)}
	var number int32
	for _, f := range fields {
		number++
		msg.Field = append(msg.Field, fieldProto(msg, number, f))
	}
	return msg
}

func TestDescribeReadsARecord(t *testing.T) {
	inner := nestedMessage("Inner", field{name: "ok", typ: tBool})
	totals := nestedMessage("Totals",
		field{name: "count", typ: tInt64},
		field{name: "label", typ: tString},
		field{name: "flag", typ: tBool},
		field{name: "limit", typ: tInt64, optional: true},
		field{name: "models", typ: tString, repeated: true},
		field{name: "inner", typ: tMsg, typeName: ".pumi.facts.test.GoodRecord.Inner"},
	)
	md := syntheticWith(t, "GoodRecord", record(), []*descriptorpb.DescriptorProto{totals, inner},
		field{name: "tree", typ: tString, role: key},
		field{name: "value", typ: tMsg, role: val, typeName: ".pumi.facts.test.GoodRecord.Totals"})
	if _, err := Describe(md); err != nil {
		t.Fatal(err)
	}
}

func TestDescribeRefusesARecordCedarCannotHold(t *testing.T) {
	entry := nestedMessage("ValueEntry", field{name: "key", typ: tString}, field{name: "value", typ: tInt64})
	entry.Options = &descriptorpb.MessageOptions{MapEntry: proto.Bool(true)}
	tests := []struct {
		name  string
		inner *descriptorpb.DescriptorProto
		want  string
	}{
		{"RecordWithDouble", nestedMessage("Totals", field{name: "cost", typ: descriptorpb.FieldDescriptorProto_TYPE_DOUBLE}), "cost"},
		{"RecordWithUint64", nestedMessage("Totals", field{name: "tokens", typ: descriptorpb.FieldDescriptorProto_TYPE_UINT64}), "tokens"},
		{"RecordWithBytes", nestedMessage("Totals", field{name: "blob", typ: descriptorpb.FieldDescriptorProto_TYPE_BYTES}), "blob"},
		{"RecordWithRepeatedLong", nestedMessage("Totals", field{name: "counts", typ: tInt64, repeated: true}), "counts"},
		{"RecordWithSelf", nestedMessage("Totals", field{name: "parent", typ: tMsg, typeName: ".pumi.facts.test.RecordWithSelf.Totals"}), "parent"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			md := syntheticWith(t, tt.name, record(), []*descriptorpb.DescriptorProto{tt.inner},
				field{name: "tree", typ: tString, role: key},
				field{name: "value", typ: tMsg, role: val, typeName: ".pumi.facts.test." + tt.name + ".Totals"})
			refuses(t, md, tt.want)
		})
	}
	t.Run("RecordIsMap", func(t *testing.T) {
		md := syntheticWith(t, "RecordIsMap", record(), []*descriptorpb.DescriptorProto{entry},
			field{name: "tree", typ: tString, role: key},
			field{name: "value", typ: tMsg, repeated: true, role: val, typeName: ".pumi.facts.test.RecordIsMap.ValueEntry"})
		refuses(t, md, "Record")
	})
}

// facts returns n valid synthetic facts with distinct names.
func facts(t *testing.T, n int) []protoreflect.MessageDescriptor {
	t.Helper()
	mds := []protoreflect.MessageDescriptor{}
	for i := range n {
		o := opts()
		o.Name = fmt.Sprintf("test.fact%d", i)
		mds = append(mds, synthetic(t, fmt.Sprintf("Fact%d", i), o, field{"tree", tString, false, key, "", false}, field{"value", tBool, false, val, "", false}))
	}
	return mds
}

func TestFromMessagesKeepsTheBudget(t *testing.T) {
	if _, err := FromMessages(facts(t, 20)); err != nil {
		t.Errorf("FromMessages(20 facts) = %v, want no error", err)
	}
	_, err := FromMessages(facts(t, 21))
	if err == nil || !strings.Contains(err.Error(), "20") {
		t.Errorf("FromMessages(21 facts) = %v, want an error that names the budget of 20", err)
	}
}

func TestFromMessagesRefusesADuplicateName(t *testing.T) {
	mds := facts(t, 2)
	o := opts()
	o.Name = "test.fact0"
	mds = append(mds, synthetic(t, "Again", o, field{"tree", tString, false, key, "", false}, field{"value", tBool, false, val, "", false}))
	_, err := FromMessages(mds)
	if err == nil || !strings.Contains(err.Error(), "test.fact0") {
		t.Errorf("FromMessages() = %v, want an error that names test.fact0", err)
	}
}

func TestAttributeIsTheCedarContextName(t *testing.T) {
	for name, want := range map[string]string{
		"verify.passed":      "verifyPassed",
		"ambiguity.opinions": "ambiguityOpinions",
		"head.state":         "headState",
		"attempts":           "attempts",
		"a.b.c":              "aBC",
	} {
		if got := (Fact{Name: name}).Attribute(); got != want {
			t.Errorf("Fact{Name: %q}.Attribute() = %q, want %q", name, got, want)
		}
	}
}

func TestValueTypeIsTheCedarType(t *testing.T) {
	catalog, err := Catalog()
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"verify.passed":      "Bool",
		"attempts":           "Long",
		"approved.by":        "Set<String>",
		"plan.review":        "{fail: Long, models: Set<String>, pass: Long}",
		"ambiguity.opinions": "{first: {answer: String, model: String}, second: {answer: String, model: String}}",
		"usage":              "{costMicrodollars: Long, seconds: Long, tokens: Long}",
		"head.state":         "{commit: String, tree: String, upToDate: Bool}",
	}
	for _, f := range catalog {
		if w, ok := want[f.Name]; ok {
			if got := f.ValueType(); got != w {
				t.Errorf("%s: ValueType() = %q, want %q", f.Name, got, w)
			}
			delete(want, f.Name)
		}
	}
	if len(want) != 0 {
		t.Errorf("the catalog has none of %q", slices.Collect(maps.Keys(want)))
	}
}
