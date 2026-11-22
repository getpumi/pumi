// SPDX-License-Identifier: Apache-2.0
// SPDX-FileCopyrightText: The Pumi Authors

// Package events names Pumi's v1 event types and builds and checks their
// CloudEvents envelopes (decisions 0132 to 0135). The data of each type is a
// message in getpumi.dev/pumi/api/gen/pumi/events/v1, and the envelope is the
// CloudEvent message of the CloudEvents protobuf format.
package events

import (
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"slices"

	cloudeventsv1 "getpumi.dev/pumi/api/gen/io/cloudevents/v1"
	eventsv1 "getpumi.dev/pumi/api/gen/pumi/events/v1"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/known/anypb"
)

// SchemaVersion is the version of the event schema that this package writes
// in the schemaversion extension. The event schema is versioned apart from
// the fact catalog and the runner protocol (decision 0041).
const SchemaVersion = "1.0.0"

// SpecVersion is the version of the CloudEvents specification of the
// envelope.
const SpecVersion = "1.0"

// TypePrefix starts every Pumi event type (decision 0133).
const TypePrefix = "dev.getpumi."

// The v1 event types (decision 0135). Each is written as TypePrefix and a
// name, which a test reads.
const (
	TypePlanAdded           = TypePrefix + "plan.added"
	TypeTransitionFired     = TypePrefix + "transition.fired"
	TypeDecisionMade        = TypePrefix + "decision.made"
	TypeLeaseGranted        = TypePrefix + "lease.granted"
	TypeLeaseReleased       = TypePrefix + "lease.released"
	TypeLeaseExpired        = TypePrefix + "lease.expired"
	TypeInvocationRequested = TypePrefix + "invocation.requested"
	TypeInvocationStarted   = TypePrefix + "invocation.started"
	TypeInvocationResult    = TypePrefix + "invocation.result"
	TypeInvocationFinished  = TypePrefix + "invocation.finished"
	TypeCancelRequested     = TypePrefix + "cancel.requested"
	TypeVerdictRecorded     = TypePrefix + "verdict.recorded"
	TypeWarningRaised       = TypePrefix + "warning.raised"
	TypeWarningReviewed     = TypePrefix + "warning.reviewed"
	TypeApprovalRecorded    = TypePrefix + "approval.recorded"
	TypeEscalationRaised    = TypePrefix + "escalation.raised"
	TypeEscalationResolved  = TypePrefix + "escalation.resolved"
	TypeOverrideRecorded    = TypePrefix + "override.recorded"
	TypeMergePerformed      = TypePrefix + "merge.performed"
	TypeTrunkUnattested     = TypePrefix + "trunk.unattested"
	TypeCheckIngested       = TypePrefix + "check.ingested"
)

// The names of Pumi's extension attributes (decision 0133).
const (
	// ExtensionTenant names the tenant of the record (decision 0039).
	ExtensionTenant = "tenant"
	// ExtensionSchemaVersion names the version of the event schema that the
	// writer used.
	ExtensionSchemaVersion = "schemaversion"
	// ExtensionCause names the id of the event that led to this one.
	ExtensionCause = "cause"
	// ExtensionPrevHash names the hash of the previous event's stored bytes in
	// the same log, as sha256:<hex>.
	ExtensionPrevHash = "prevhash"
)

// subject is the CloudEvents attribute that names what the event is about.
const subject = "subject"

// dataOf gives the data message of each type.
var dataOf = map[string]proto.Message{
	TypePlanAdded:           &eventsv1.PlanAdded{},
	TypeTransitionFired:     &eventsv1.TransitionFired{},
	TypeDecisionMade:        &eventsv1.DecisionMade{},
	TypeLeaseGranted:        &eventsv1.LeaseGranted{},
	TypeLeaseReleased:       &eventsv1.LeaseReleased{},
	TypeLeaseExpired:        &eventsv1.LeaseExpired{},
	TypeInvocationRequested: &eventsv1.InvocationRequested{},
	TypeInvocationStarted:   &eventsv1.InvocationStarted{},
	TypeInvocationResult:    &eventsv1.InvocationResult{},
	TypeInvocationFinished:  &eventsv1.InvocationFinished{},
	TypeCancelRequested:     &eventsv1.CancelRequested{},
	TypeVerdictRecorded:     &eventsv1.VerdictRecorded{},
	TypeWarningRaised:       &eventsv1.WarningRaised{},
	TypeWarningReviewed:     &eventsv1.WarningReviewed{},
	TypeApprovalRecorded:    &eventsv1.ApprovalRecorded{},
	TypeEscalationRaised:    &eventsv1.EscalationRaised{},
	TypeEscalationResolved:  &eventsv1.EscalationResolved{},
	TypeOverrideRecorded:    &eventsv1.OverrideRecorded{},
	TypeMergePerformed:      &eventsv1.MergePerformed{},
	TypeTrunkUnattested:     &eventsv1.TrunkUnattested{},
	TypeCheckIngested:       &eventsv1.CheckIngested{},
}

// typeOf gives the type of each data message, by its full name.
var typeOf = func() map[protoreflect.FullName]string {
	m := map[protoreflect.FullName]string{}
	for typ, msg := range dataOf {
		m[msg.ProtoReflect().Descriptor().FullName()] = typ
	}
	return m
}()

// prevHash is the form of the prevhash extension.
var prevHash = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)

// Types returns every event type, sorted.
func Types() []string {
	types := make([]string, 0, len(dataOf))
	for typ := range dataOf {
		types = append(types, typ)
	}
	slices.Sort(types)
	return types
}

// Data returns a new, empty data message of typ.
func Data(typ string) (proto.Message, error) {
	msg, ok := dataOf[typ]
	if !ok {
		return nil, fmt.Errorf("%q is not a Pumi event type", typ)
	}
	return msg.ProtoReflect().New().Interface(), nil
}

// TypeOf returns the event type whose data is msg.
func TypeOf(msg proto.Message) (string, error) {
	if msg == nil {
		return "", errors.New("the event data is nil")
	}
	name := msg.ProtoReflect().Descriptor().FullName()
	typ, ok := typeOf[name]
	if !ok {
		return "", fmt.Errorf("%s is not the data of a Pumi event type", name)
	}
	return typ, nil
}

// Attributes are the envelope attributes that the writer of an event gives.
// ID, Source and Tenant are required; the others are left out of the
// envelope when they are empty (decision 0133).
type Attributes struct {
	// ID is unique within Source.
	ID string
	// Source names the producer, such as /kernel or /runner/<id>.
	Source string
	// Subject names what the event is about, such as the plan.
	Subject string
	// Tenant is the tenant of the record.
	Tenant string
	// Cause is the id of the event that led to this one.
	Cause string
	// PrevHash is sha256:<hex> of the previous event's stored bytes in the
	// same log.
	PrevHash string
}

// New builds the envelope of an event with data and attrs. The type follows
// from the data message, and the schemaversion is SchemaVersion. It refuses
// data that is not the data of a Pumi event type and any envelope that
// Validate refuses.
func New(data proto.Message, attrs Attributes) (*cloudeventsv1.CloudEvent, error) {
	typ, err := TypeOf(data)
	if err != nil {
		return nil, err
	}
	packed, err := anypb.New(data)
	if err != nil {
		return nil, fmt.Errorf("packing the data of %s: %w", typ, err)
	}
	e := &cloudeventsv1.CloudEvent{
		Id:          attrs.ID,
		Source:      attrs.Source,
		SpecVersion: SpecVersion,
		Type:        typ,
		Attributes:  map[string]*cloudeventsv1.CloudEvent_CloudEventAttributeValue{},
		Data:        &cloudeventsv1.CloudEvent_ProtoData{ProtoData: packed},
	}
	for name, value := range map[string]string{
		subject:                attrs.Subject,
		ExtensionTenant:        attrs.Tenant,
		ExtensionSchemaVersion: SchemaVersion,
		ExtensionCause:         attrs.Cause,
		ExtensionPrevHash:      attrs.PrevHash,
	} {
		if value != "" {
			e.Attributes[name] = &cloudeventsv1.CloudEvent_CloudEventAttributeValue{
				Attr: &cloudeventsv1.CloudEvent_CloudEventAttributeValue_CeString{CeString: value},
			}
		}
	}
	if err := Validate(e); err != nil {
		return nil, err
	}
	return e, nil
}

// Validate checks an envelope: the CloudEvents spec version, an id and a
// source that is a URI reference, a Pumi event type, the attributes (see
// validateAttributes), and protobuf data that holds a valid message of its
// type.
func Validate(e *cloudeventsv1.CloudEvent) error {
	if e == nil {
		return errors.New("the event is nil")
	}
	if e.GetSpecVersion() != SpecVersion {
		return fmt.Errorf("the CloudEvents spec version is %q, want %q", e.GetSpecVersion(), SpecVersion)
	}
	if e.GetId() == "" {
		return errors.New("the event has no id")
	}
	if e.GetSource() == "" {
		return errors.New("the event has no source")
	}
	if _, err := url.Parse(e.GetSource()); err != nil {
		return fmt.Errorf("the source %q is not a URI reference: %w", e.GetSource(), err)
	}
	data, err := Data(e.GetType())
	if err != nil {
		return err
	}
	if err := validateAttributes(e.GetAttributes()); err != nil {
		return err
	}
	return validateData(e, data)
}

// typeURLPrefix starts the type URL of every packed message, as anypb.New
// writes it.
const typeURLPrefix = "type.googleapis.com/"

// validateData checks that the data of e is protobuf data of the message of
// its type, data, and that the message is valid.
func validateData(e *cloudeventsv1.CloudEvent, data proto.Message) error {
	packed := e.GetProtoData()
	if packed == nil {
		return fmt.Errorf("the data of %s is not protobuf data", e.GetType())
	}
	want := string(data.ProtoReflect().Descriptor().FullName())
	if got := packed.GetTypeUrl(); got != typeURLPrefix+want {
		return fmt.Errorf("the data of %s has the type URL %q, want %q", e.GetType(), got, typeURLPrefix+want)
	}
	if err := packed.UnmarshalTo(data); err != nil {
		return fmt.Errorf("the data of %s cannot be read: %w", e.GetType(), err)
	}
	if rule, ok := dataRules[e.GetType()]; ok {
		return rule(data)
	}
	return nil
}

// dataRules are the rules for the data of some types that the schema cannot
// state.
var dataRules = map[string]func(proto.Message) error{
	// An invocation request carries its document, which matches its hash
	// (decision 0165), and names its plan (decision 0175).
	TypeInvocationRequested: func(m proto.Message) error {
		if r, ok := m.(*eventsv1.InvocationRequested); ok {
			if r.GetPlan() == "" {
				return errNoPlan
			}
			_, err := Document(r)
			return err
		}
		return nil
	},
	// An override needs a reason (decision 0139).
	TypeOverrideRecorded: func(m proto.Message) error {
		if o, ok := m.(*eventsv1.OverrideRecorded); ok && o.GetReason() == "" {
			return errors.New("the override has no reason (decision 0139)")
		}
		return nil
	},
}

// stringAttributes are the attributes that Pumi writes, each a non-empty
// string when present.
var stringAttributes = []string{subject, ExtensionTenant, ExtensionSchemaVersion, ExtensionCause, ExtensionPrevHash}

// otherAttributes are the optional attributes of the CloudEvents
// specification that Pumi does not write and accepts.
var otherAttributes = []string{"time", "datacontenttype", "dataschema"}

// schemaVersion is the form of the schemaversion extension: a semantic
// version with its major version first.
var schemaVersion = regexp.MustCompile(`^([0-9]+)\.[0-9]+\.[0-9]+$`)

// validateAttributes checks the attributes: no attribute other than those of
// stringAttributes and otherAttributes, tenant and schemaversion present,
// each attribute of stringAttributes that is present a non-empty string,
// schemaversion of the major version of SchemaVersion, and prevhash
// sha256:<hex>.
func validateAttributes(attrs map[string]*cloudeventsv1.CloudEvent_CloudEventAttributeValue) error {
	for name := range attrs {
		if !slices.Contains(stringAttributes, name) && !slices.Contains(otherAttributes, name) {
			return fmt.Errorf("the attribute %q is not one that Pumi events have", name)
		}
	}
	for _, name := range []string{ExtensionTenant, ExtensionSchemaVersion} {
		if _, ok := attrs[name]; !ok {
			return fmt.Errorf("the event has no %s attribute", name)
		}
	}
	for _, name := range stringAttributes {
		v, ok := attrs[name]
		if !ok {
			continue
		}
		s, ok := v.GetAttr().(*cloudeventsv1.CloudEvent_CloudEventAttributeValue_CeString)
		if !ok || s.CeString == "" {
			return fmt.Errorf("the %s attribute is not a non-empty string", name)
		}
		if err := validateString(name, s.CeString); err != nil {
			return err
		}
	}
	return nil
}

// validateString checks the form of the string attribute name.
func validateString(name, value string) error {
	switch name {
	case ExtensionPrevHash:
		if !prevHash.MatchString(value) {
			return fmt.Errorf("the prevhash attribute %q is not sha256:<64 lower-case hex digits>", value)
		}
	case ExtensionSchemaVersion:
		got := schemaVersion.FindStringSubmatch(value)
		want := schemaVersion.FindStringSubmatch(SchemaVersion)
		if got == nil || got[1] != want[1] {
			return fmt.Errorf("the schemaversion attribute %q is not a version %s.x.y of the event schema", value, want[1])
		}
	}
	return nil
}
