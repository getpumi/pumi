// SPDX-License-Identifier: Apache-2.0
// SPDX-FileCopyrightText: The Pumi Authors

// Package facts reads Pumi's v1 fact catalog from the schema in
// getpumi.dev/pumi/api/gen/pumi/facts/v1 (decisions 0136 and 0137). Each fact
// is a message whose fact option gives its name, Cedar type, producer and
// source, and whose fields are marked as the fact's key or its value.
package facts

import (
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"

	factsv1 "getpumi.dev/pumi/api/gen/pumi/facts/v1"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"
)

// CatalogVersion is the version of the fact catalog. It is versioned apart
// from the event schema and the runner protocol (decision 0041).
const CatalogVersion = "1.0.0"

// Budget is the most facts the v1 catalog may hold (decision 0017).
const Budget = 20

// pkg is the protobuf package of the catalog, and options the message in it
// that is not a fact.
const (
	pkg     = "pumi.facts.v1"
	options = "FactOptions"
)

// name is the form of a fact's name: parts joined by dots, each a Cedar
// identifier, so that Attribute is a Cedar identifier too.
var name = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*(\.[A-Za-z_][A-Za-z0-9_]*)*$`)

// Fact is one fact of the catalog.
type Fact struct {
	// Name is the fact's name, such as verify.passed.
	Name string
	// CedarType is the Cedar type of the value: Bool, Long, Set<String> or
	// Record.
	CedarType string
	// Producer is what produces the fact.
	Producer string
	// Source is where the fact comes from.
	Source factsv1.Source
	// Keys are the names of the key fields, in field order.
	Keys []string
	// Value is the name of the value field.
	Value string
	// Message is the fact's message.
	Message protoreflect.MessageDescriptor
}

// Catalog returns the facts of the pumi.facts.v1 package, sorted by name. It
// fails when a message of the package other than FactOptions is not a valid
// fact, when two facts share a name or a Cedar context name, or when there
// are more than Budget.
func Catalog() ([]Fact, error) {
	mds := []protoreflect.MessageDescriptor{}
	protoregistry.GlobalFiles.RangeFilesByPackage(pkg, func(fd protoreflect.FileDescriptor) bool {
		for i := range fd.Messages().Len() {
			if md := fd.Messages().Get(i); md.Name() != options {
				mds = append(mds, md)
			}
		}
		return true
	})
	if len(mds) == 0 {
		return nil, fmt.Errorf("no messages are registered for the package %s", pkg)
	}
	return FromMessages(mds)
}

// FromMessages returns the facts that mds describe, sorted by name, with the
// checks of Catalog. Two facts may not have the same Attribute either.
func FromMessages(mds []protoreflect.MessageDescriptor) ([]Fact, error) {
	if len(mds) > Budget {
		return nil, fmt.Errorf("%d facts, more than the budget of %d (decision 0017)", len(mds), Budget)
	}
	facts := make([]Fact, 0, len(mds))
	seen := map[string]protoreflect.FullName{}
	attributes := map[string]protoreflect.FullName{}
	for _, md := range mds {
		f, err := Describe(md)
		if err != nil {
			return nil, err
		}
		if other, ok := seen[f.Name]; ok {
			return nil, fmt.Errorf("%s and %s are both the fact %s", other, md.FullName(), f.Name)
		}
		seen[f.Name] = md.FullName()
		if other, ok := attributes[f.Attribute()]; ok {
			return nil, fmt.Errorf("%s and %s both have the Cedar context name %s", other, md.FullName(), f.Attribute())
		}
		attributes[f.Attribute()] = md.FullName()
		facts = append(facts, f)
	}
	slices.SortFunc(facts, func(a, b Fact) int {
		switch {
		case a.Name < b.Name:
			return -1
		case a.Name > b.Name:
			return 1
		}
		return 0
	})
	return facts, nil
}

// Describe reads the fact that md is. It fails when md has no fact option,
// when the option has no name, producer or source, when the name's parts are
// not Cedar identifiers, when a field has no role
// or one that is not key or value, when a key field is not a single string
// without explicit presence, when there is no key field or not exactly one
// value field, or when the value field does not have the option's Cedar type.
func Describe(md protoreflect.MessageDescriptor) (Fact, error) {
	opts, ok := proto.GetExtension(md.Options(), factsv1.E_Fact).(*factsv1.FactOptions)
	if !ok || opts == nil || !proto.HasExtension(md.Options(), factsv1.E_Fact) {
		return Fact{}, fmt.Errorf("%s has no fact option", md.FullName())
	}
	switch {
	case opts.GetName() == "":
		return Fact{}, fmt.Errorf("%s: the fact has no name", md.FullName())
	case !name.MatchString(opts.GetName()):
		return Fact{}, fmt.Errorf("%s: the fact name %q is not dot-separated parts that make a Cedar identifier", md.FullName(), opts.GetName())
	case opts.GetProducer() == "":
		return Fact{}, fmt.Errorf("%s: the fact has no producer", md.FullName())
	case opts.GetSource() == factsv1.Source_SOURCE_UNSPECIFIED:
		return Fact{}, fmt.Errorf("%s: the fact has no source", md.FullName())
	}
	f := Fact{
		Name:      opts.GetName(),
		CedarType: opts.GetCedarType(),
		Producer:  opts.GetProducer(),
		Source:    opts.GetSource(),
		Message:   md,
	}
	value, err := fields(md, &f)
	if err != nil {
		return Fact{}, err
	}
	if err := checkCedarType(f.CedarType, value); err != nil {
		return Fact{}, fmt.Errorf("%s: %w", md.FullName(), err)
	}
	return f, nil
}

// fields sets the Keys and Value of f from the roles of the fields of md and
// returns the value field.
func fields(md protoreflect.MessageDescriptor, f *Fact) (protoreflect.FieldDescriptor, error) {
	var values []protoreflect.FieldDescriptor
	for i := range md.Fields().Len() {
		fd := md.Fields().Get(i)
		role, _ := proto.GetExtension(fd.Options(), factsv1.E_Role).(factsv1.Role)
		switch role {
		case factsv1.Role_ROLE_KEY:
			if fd.Kind() != protoreflect.StringKind || fd.IsList() || fd.HasPresence() {
				return nil, fmt.Errorf("%s: the key field %s is not a single string without explicit presence", md.FullName(), fd.Name())
			}
			f.Keys = append(f.Keys, string(fd.Name()))
		case factsv1.Role_ROLE_VALUE:
			values = append(values, fd)
		case factsv1.Role_ROLE_UNSPECIFIED:
			return nil, fmt.Errorf("%s: the field %s has no role", md.FullName(), fd.Name())
		default:
			return nil, fmt.Errorf("%s: the field %s has the role %d, which is not key or value", md.FullName(), fd.Name(), role)
		}
	}
	if len(f.Keys) == 0 {
		return nil, fmt.Errorf("%s: the fact has no key field", md.FullName())
	}
	if len(values) != 1 {
		return nil, fmt.Errorf("%s: the fact has %d value fields, want one value field", md.FullName(), len(values))
	}
	f.Value = string(values[0].Name())
	return values[0], nil
}

// checkCedarType checks that the value field fits the Cedar type: Bool a
// single bool, Long a single int64, Set<String> a repeated string and Record
// a single message that is not a map and that checkRecord accepts.
func checkCedarType(cedarType string, value protoreflect.FieldDescriptor) error {
	var ok bool
	switch cedarType {
	case "Bool":
		ok = value.Kind() == protoreflect.BoolKind && !value.IsList()
	case "Long":
		ok = value.Kind() == protoreflect.Int64Kind && !value.IsList()
	case "Set<String>":
		ok = value.Kind() == protoreflect.StringKind && value.IsList()
	case "Record":
		ok = value.Kind() == protoreflect.MessageKind && !value.IsList() && !value.IsMap()
		if ok {
			return checkRecord(value.Message(), map[protoreflect.FullName]bool{})
		}
	default:
		return fmt.Errorf("the Cedar type %q is not Bool, Long, Set<String> or Record", cedarType)
	}
	if !ok {
		return errors.New("the value field " + string(value.Name()) + " does not fit the Cedar type " + cedarType)
	}
	return nil
}

// checkRecord checks that every field of the record md can be an attribute
// of a Cedar record: a single bool, int64 or string, a repeated string, or a
// single message that checkRecord accepts. Cedar has no floating-point,
// unsigned or byte type, which is why decision 0136 gives cost in
// microdollars as a Long. A record that holds itself, directly or through
// another record, fails, because its Cedar type would have no end. open holds
// the records being checked.
func checkRecord(md protoreflect.MessageDescriptor, open map[protoreflect.FullName]bool) error {
	if open[md.FullName()] {
		return fmt.Errorf("the record %s holds itself", md.FullName())
	}
	open[md.FullName()] = true
	defer delete(open, md.FullName())
	for i := range md.Fields().Len() {
		fd := md.Fields().Get(i)
		if err := checkAttribute(fd, open); err != nil {
			return err
		}
	}
	return nil
}

// checkAttribute checks one field of a record for checkRecord.
func checkAttribute(fd protoreflect.FieldDescriptor, open map[protoreflect.FullName]bool) error {
	kind := fd.Kind()
	if fd.IsMap() || (fd.IsList() && kind != protoreflect.StringKind) {
		return fmt.Errorf("the record field %s is a map or a repeated field that is not string", fd.FullName())
	}
	if kind == protoreflect.MessageKind {
		if err := checkRecord(fd.Message(), open); err != nil {
			return fmt.Errorf("the record field %s: %w", fd.FullName(), err)
		}
		return nil
	}
	if kind != protoreflect.BoolKind && kind != protoreflect.Int64Kind && kind != protoreflect.StringKind {
		return fmt.Errorf("the record field %s is %s, which is not bool, int64, string or a message", fd.FullName(), kind)
	}
	return nil
}

// Attribute returns the name of the fact in the context of a Cedar request:
// its name with the dots taken out and each part after the first starting
// with a capital letter, such as verifyPassed for verify.passed (decision
// 0141).
func (f Fact) Attribute() string {
	parts := strings.Split(f.Name, ".")
	for i := 1; i < len(parts); i++ {
		if parts[i] != "" {
			parts[i] = strings.ToUpper(parts[i][:1]) + parts[i][1:]
		}
	}
	return strings.Join(parts, "")
}

// ValueType returns the Cedar type of the fact's value as the Cedar schema
// writes it, with the attributes of a record sorted by name and named by
// the JSON names of the message's fields, such as {fail: Long, pass: Long}.
// Every attribute is required, a message field too, so the producer sets
// every field of a record. ValueType is for a Fact that Describe returned.
func (f Fact) ValueType() string {
	return fieldType(f.Message.Fields().ByName(protoreflect.Name(f.Value)))
}

// fieldType returns the Cedar type of a field that Describe accepts, in the
// form of ValueType.
func fieldType(fd protoreflect.FieldDescriptor) string {
	single := map[protoreflect.Kind]string{
		protoreflect.BoolKind:   "Bool",
		protoreflect.Int64Kind:  "Long",
		protoreflect.StringKind: "String",
	}[fd.Kind()]
	if fd.Kind() == protoreflect.MessageKind {
		single = recordType(fd.Message())
	}
	if fd.IsList() {
		return "Set<" + single + ">"
	}
	return single
}

// recordType returns the Cedar record type of md for fieldType.
func recordType(md protoreflect.MessageDescriptor) string {
	attributes := make([]string, 0, md.Fields().Len())
	for i := range md.Fields().Len() {
		fd := md.Fields().Get(i)
		attributes = append(attributes, fd.JSONName()+": "+fieldType(fd))
	}
	slices.Sort(attributes)
	return "{" + strings.Join(attributes, ", ") + "}"
}
