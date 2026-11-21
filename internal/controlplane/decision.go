// SPDX-License-Identifier: Apache-2.0
// SPDX-FileCopyrightText: The Pumi Authors

package controlplane

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"slices"

	"github.com/cedar-policy/cedar-go"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/known/anypb"

	"getpumi.dev/pumi/api/events"
	"getpumi.dev/pumi/api/facts"
	cloudeventsv1 "getpumi.dev/pumi/api/gen/io/cloudevents/v1"
	eventsv1 "getpumi.dev/pumi/api/gen/pumi/events/v1"
	"getpumi.dev/pumi/api/policy"
	"getpumi.dev/pumi/internal/controlplane/embeddedlog"
	"getpumi.dev/pumi/internal/controlplane/projections"
)

// source is the source attribute of the events that the control plane
// appends itself (decision 0133).
const source = "/controlplane"

// principal is who a decision is for: the Cedar principal (decision
// 0141), from the caller's identity, never from a request (decision 0156).
type principal struct {
	caller Caller
	// id is the id of the principal's Cedar entity.
	id string
	// roles are a person's roles.
	roles []string
	// role is a runner's role, gate or agent, which the invocation it holds
	// gives (decision 0156).
	role string
}

// entity returns the principal's Cedar entity.
func (p principal) entity() cedar.Entity {
	switch p.caller {
	case CallerKernel:
	case CallerPerson:
		return cedar.Entity{
			UID:        cedar.NewEntityUID("Pumi::Person", cedar.String(p.id)),
			Attributes: cedar.NewRecord(cedar.RecordMap{"roles": stringSet(p.roles)}),
		}
	case CallerRunner:
		return cedar.Entity{
			UID:        cedar.NewEntityUID("Pumi::Runner", cedar.String(p.id)),
			Attributes: cedar.NewRecord(cedar.RecordMap{"role": cedar.String(p.role)}),
		}
	}
	return cedar.Entity{UID: cedar.NewEntityUID("Pumi::Kernel", cedar.String(p.id))}
}

// question is what one decision is about: the action, by its name in the
// schema, the plan, the keys of its facts, and the id of the event that
// led to it, if any, which the decision.made event names as its cause.
type question struct {
	action string
	plan   string
	keys   projections.Keys
	cause  string
}

// decided is a decision and the decision.made event that records it.
type decided struct {
	decision Decision
	event    *cloudeventsv1.CloudEvent
}

// policySetDigest returns sha256:<hex> of the names and texts of files, in
// order, which the decision.made events record as the policy set digest.
func policySetDigest(files []PolicyFile) string {
	h := sha256.New()
	for _, f := range files {
		// Each name and text ends with a zero byte, so that no two lists
		// of files give the same bytes.
		h.Write([]byte(f.Name))
		h.Write([]byte{0})
		h.Write([]byte(f.Text))
		h.Write([]byte{0})
	}
	return "sha256:" + hex.EncodeToString(h.Sum(nil))
}

// policyFiles returns the core invariants and the wiring's policies.
func policyFiles(cfg Config) []PolicyFile {
	return append([]PolicyFile{{Name: "invariants.cedar", Text: policy.Invariants}}, cfg.Policies...)
}

// knows reports whether the schema has the action.
func (s *Service) knows(action string) bool {
	_, ok := s.decider.contextAttributes(actionUID(action))
	return ok
}

// actionUID returns the Cedar UID of the action named action.
func actionUID(action string) cedar.EntityUID {
	return cedar.NewEntityUID("Pumi::Action", cedar.String(action))
}

// decide decides q for p with the log as of the position at, whose line
// has the hash hash, and returns the decision and the decision.made event
// that records it, which the caller appends. It projects each fact that
// the action's context declares; a fact with no projection, or that its
// projection does not give, is missing, and the decision is a deny
// (decisions 0143 and 0155). The profile settings and the hard caps come
// from the wiring.
func (s *Service) decide(at uint64, hash string, p principal, q question) (decided, error) {
	attrs, ok := s.decider.contextAttributes(actionUID(q.action))
	if !ok {
		return decided{}, fmt.Errorf("the schema has no action %q", q.action)
	}
	keys := q.keys
	keys.Plan, keys.Gate, keys.Project = q.plan, q.action, s.cfg.Project.Name
	context, basis, err := s.context(at, attrs, keys)
	if err != nil {
		return decided{}, err
	}
	d := s.decider.Decide(s.request(p, q, keys, cedar.NewRecord(context)))
	outcome := eventsv1.DecisionMade_OUTCOME_DENY
	if d.Allow {
		outcome = eventsv1.DecisionMade_OUTCOME_ALLOW
	}
	e, err := events.New(&eventsv1.DecisionMade{
		PolicySetDigest:     s.digest,
		Outcome:             outcome,
		DeterminingPolicies: d.DeterminingPolicies,
		Basis:               basis,
		LogHead:             hash,
		Mode:                eventsv1.DecisionMade_MODE_ENFORCED,
	}, events.Attributes{ID: newID(), Source: source, Tenant: embeddedlog.Tenant, Cause: q.cause})
	if err != nil {
		return decided{}, fmt.Errorf("building the decision.made event: %w", err)
	}
	return decided{decision: d, event: e}, nil
}

// context returns the context of a request whose action declares attrs,
// with the facts projected for keys from the log as of the position at,
// and the basis that lists those facts. The settings and the hard caps
// come from the wiring. An attribute that nothing gives is left out.
func (s *Service) context(at uint64, attrs []string, keys projections.Keys) (cedar.RecordMap, *eventsv1.Basis, error) {
	context := cedar.RecordMap{}
	basis := &eventsv1.Basis{}
	settings := s.cfg.Settings.values()
	for _, name := range attrs {
		f, isFact := s.catalog[name]
		switch {
		case isFact:
			msg, err := s.project(at, f, keys)
			if err != nil {
				return nil, nil, err
			}
			if msg == nil {
				continue
			}
			packed, err := anypb.New(msg)
			if err != nil {
				return nil, nil, fmt.Errorf("packing the fact %s: %w", f.Name, err)
			}
			basis.Facts = append(basis.Facts, packed)
			context[cedar.String(name)] = factValue(msg, f)
		case name == hardCaps:
			context[cedar.String(name)] = capsRecord(s.cfg.HardCaps)
		default:
			if v, ok := settings[name]; ok {
				context[cedar.String(name)] = v
			}
		}
	}
	return context, basis, nil
}

// request returns the Cedar request of q for p, with context, and the
// entities it reads: the principal, the plan with the tree its facts are
// keyed to (the head tree), and the plan's project.
func (s *Service) request(p principal, q question, keys projections.Keys, context cedar.Record) Request {
	pe := p.entity()
	project := cedar.Entity{
		UID:        cedar.NewEntityUID("Pumi::Project", cedar.String(s.cfg.Project.Name)),
		Attributes: cedar.NewRecord(cedar.RecordMap{"protectedPaths": stringSet(s.cfg.Project.ProtectedPaths)}),
	}
	plan := cedar.Entity{
		UID:        cedar.NewEntityUID("Pumi::Plan", cedar.String(q.plan)),
		Parents:    cedar.NewEntityUIDSet(project.UID),
		Attributes: cedar.NewRecord(cedar.RecordMap{"tree": cedar.String(keys.HeadTree)}),
	}
	return Request{
		Principal: pe.UID,
		Action:    actionUID(q.action),
		Resource:  plan.UID,
		Context:   context,
		Entities:  cedar.EntityMap{pe.UID: pe, plan.UID: plan, project.UID: project},
	}
}

// project returns the fact f for keys from the log as of the position at,
// or nil when no projection is registered for it or its projection does
// not give it. A projection that fails gives nothing, so the decision
// fails closed (decision 0143); an error of the log is returned.
func (s *Service) project(at uint64, f facts.Fact, keys projections.Keys) (proto.Message, error) {
	p, ok := s.projections[f.Name]
	if !ok {
		return nil, nil
	}
	found, _, err := s.log.Read(embeddedlog.Filter{Types: p.Types})
	if err != nil {
		return nil, err
	}
	evs := make([]*cloudeventsv1.CloudEvent, 0, len(found))
	for _, e := range found {
		if e.Position <= at {
			evs = append(evs, e.Event)
		}
	}
	return run(p, f, keys, evs), nil
}

// run returns the fact f that p projects for keys from evs, or nil when p
// gives none, fails, or gives a message other than the fact's.
func run(p projections.Projection, f facts.Fact, keys projections.Keys, evs []*cloudeventsv1.CloudEvent) proto.Message {
	msg, err := p.Project(keys, evs)
	if err != nil || msg == nil || msg.ProtoReflect().Descriptor().FullName() != f.Message.FullName() {
		return nil
	}
	return msg
}

// factValue returns the value of the fact msg, whose catalog entry is f,
// as a Cedar value of its type (decision 0141).
func factValue(msg proto.Message, f facts.Fact) cedar.Value {
	fd := f.Message.Fields().ByName(protoreflect.Name(f.Value))
	return fieldValue(fd, msg.ProtoReflect().Get(fd))
}

// fieldValue returns v, the value of the field fd of a fact, as a Cedar
// value: a bool as a Bool, an int64 as a Long, a string as a String, a
// repeated string as a Set, and a message as a record of its fields by
// their JSON names, which facts.Describe allows and the schema declares.
func fieldValue(fd protoreflect.FieldDescriptor, v protoreflect.Value) cedar.Value {
	if fd.IsList() {
		list := v.List()
		values := make([]cedar.Value, 0, list.Len())
		for i := range list.Len() {
			values = append(values, cedar.String(list.Get(i).String()))
		}
		return cedar.NewSet(values...)
	}
	kind := fd.Kind()
	if kind == protoreflect.BoolKind {
		return cedar.Boolean(v.Bool())
	}
	if kind == protoreflect.Int64Kind {
		return cedar.Long(v.Int())
	}
	if kind != protoreflect.MessageKind {
		return cedar.String(v.String())
	}
	m := v.Message()
	record := cedar.RecordMap{}
	fields := m.Descriptor().Fields()
	for i := range fields.Len() {
		field := fields.Get(i)
		record[cedar.String(field.JSONName())] = fieldValue(field, m.Get(field))
	}
	return cedar.NewRecord(record)
}

// newID returns a new event id for an event that the control plane
// appends, unique within its source.
func newID() string {
	return "decision-" + rand.Text()
}

// factsByAttribute returns the facts of the catalog by their context names.
func factsByAttribute() (map[string]facts.Fact, error) {
	catalog, err := facts.Catalog()
	if err != nil {
		return nil, fmt.Errorf("cannot read the fact catalog: %w", err)
	}
	out := map[string]facts.Fact{}
	for _, f := range catalog {
		out[f.Attribute()] = f
	}
	return out, nil
}

// contextAttributes returns the names of the attributes that the context of
// action declares, sorted, and whether the schema has the action.
func (d *Decider) contextAttributes(action cedar.EntityUID) ([]string, bool) {
	ctx, ok := d.contexts[action]
	if !ok {
		return nil, false
	}
	names := make([]string, 0, len(ctx))
	for name := range ctx {
		names = append(names, string(name))
	}
	slices.Sort(names)
	return names, true
}
