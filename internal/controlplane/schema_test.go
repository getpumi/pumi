// SPDX-License-Identifier: Apache-2.0
// SPDX-FileCopyrightText: The Pumi Authors

package controlplane

import (
	"maps"
	"slices"
	"sort"
	"strings"
	"testing"

	"github.com/cedar-policy/cedar-go"
	cedartypes "github.com/cedar-policy/cedar-go/types"
	xast "github.com/cedar-policy/cedar-go/x/exp/ast"
	"github.com/cedar-policy/cedar-go/x/exp/schema"
	"github.com/cedar-policy/cedar-go/x/exp/schema/resolved"
	"github.com/cedar-policy/cedar-go/x/exp/schema/validate"

	"getpumi.dev/pumi/api/facts"
	"getpumi.dev/pumi/api/policy"
)

// resolvedSchema parses and resolves the Cedar schema with cedar-go.
func resolvedSchema(t *testing.T) *resolved.Schema {
	t.Helper()
	var s schema.Schema
	s.SetFilename("pumi.cedarschema")
	if err := s.UnmarshalCedar([]byte(policy.Schema)); err != nil {
		t.Fatalf("cedar-go cannot parse the schema: %v", err)
	}
	r, err := s.Resolve()
	if err != nil {
		t.Fatalf("cedar-go cannot resolve the schema: %v", err)
	}
	return r
}

func TestSchemaHasTheEntitiesOf0141(t *testing.T) {
	r := resolvedSchema(t)
	got := []string{}
	for et := range r.Entities {
		got = append(got, string(et))
	}
	sort.Strings(got)
	want := []string{"Pumi::Agent", "Pumi::Kernel", "Pumi::Person", "Pumi::Plan", "Pumi::Project", "Pumi::Runner"}
	if !slices.Equal(got, want) {
		t.Errorf("entity types = %q, want %q", got, want)
	}
	if parents := r.Entities["Pumi::Plan"].ParentTypes; !slices.Equal(parents, []cedartypes.EntityType{"Pumi::Project"}) {
		t.Errorf("Plan is in %q, want Pumi::Project", parents)
	}
	attributes := map[cedartypes.EntityType]string{
		"Pumi::Person":  "roles",
		"Pumi::Runner":  "role",
		"Pumi::Project": "protectedPaths",
		"Pumi::Plan":    "tree",
	}
	for et, name := range attributes {
		if _, ok := r.Entities[et].Shape[cedartypes.String(name)]; !ok {
			t.Errorf("%s has no attribute %s", et, name)
		}
	}
}

// gates are the actions of 0141 for the gates.
var gates = []string{"ambiguity", "budgets", "conformance", "dco", "mergeApproval", "outsideChecks", "planReview", "scope", "spec"}

func TestSchemaHasTheActionsOf0141(t *testing.T) {
	r := resolvedSchema(t)
	got := []string{}
	for uid, a := range r.Actions {
		if uid.Type != "Pumi::Action" {
			t.Errorf("the action %s is not in the Pumi namespace", uid)
		}
		if a.AppliesTo == nil {
			t.Errorf("the action %s applies to nothing", uid)
		}
		got = append(got, string(uid.ID))
	}
	sort.Strings(got)
	want := slices.Sorted(slices.Values(append(slices.Clone(gates), "merge", "writeVerdict", "approve", "override", "cancel")))
	if !slices.Equal(got, want) {
		t.Errorf("actions = %q, want %q", got, want)
	}
}

// typeOf writes a resolved Cedar type as text, with the attributes of a
// record sorted and marked when optional.
func typeOf(t resolved.IsType) string {
	switch t := t.(type) {
	case resolved.BoolType:
		return "Bool"
	case resolved.LongType:
		return "Long"
	case resolved.StringType:
		return "String"
	case resolved.SetType:
		return "Set<" + typeOf(t.Element) + ">"
	case resolved.RecordType:
		return recordOf(t)
	case resolved.EntityType:
		return "Entity<" + string(t) + ">"
	}
	return "unknown"
}

// recordOf writes a resolved record type as text for typeOf.
func recordOf(r resolved.RecordType) string {
	parts := []string{}
	for _, name := range slices.Sorted(maps.Keys(r)) {
		mark := ""
		if r[name].Optional {
			mark = "?"
		}
		parts = append(parts, string(name)+mark+": "+typeOf(r[name].Type))
	}
	return "{" + strings.Join(parts, ", ") + "}"
}

// contexts returns the context of each action, by the action's name.
func contexts(t *testing.T) map[string]resolved.RecordType {
	t.Helper()
	out := map[string]resolved.RecordType{}
	for uid, a := range resolvedSchema(t).Actions {
		if a.AppliesTo != nil {
			out[string(uid.ID)] = a.AppliesTo.Context
		}
	}
	return out
}

func TestEveryContextAttributeIsAFactOrASetting(t *testing.T) {
	catalog, err := facts.Catalog()
	if err != nil {
		t.Fatal(err)
	}
	byAttribute := map[string]facts.Fact{}
	for _, f := range catalog {
		byAttribute[f.Attribute()] = f
	}
	settings := map[string]policy.Setting{}
	for _, s := range slices.Concat(policy.Settings(), policy.ReleaseValues()) {
		settings[s.Name] = s
	}
	read := map[string]bool{}
	for action, ctx := range contexts(t) {
		for name, attr := range ctx {
			read[string(name)] = true
			if err := checkAttribute(string(name), attr, byAttribute, settings); err != "" {
				t.Errorf("%s: %s", action, err)
			}
		}
	}
	for attribute, f := range byAttribute {
		if !read[attribute] {
			t.Errorf("no action reads the fact %s (%s)", f.Name, attribute)
		}
	}
	for name := range settings {
		if !read[name] {
			t.Errorf("no action reads the setting %s", name)
		}
	}
}

// checkAttribute returns what is wrong with the context attribute name, or
// nothing when it is a required fact or setting of the right type.
func checkAttribute(name string, attr resolved.Attribute, byAttribute map[string]facts.Fact, settings map[string]policy.Setting) string {
	got := typeOf(attr.Type)
	if attr.Optional {
		return "the context attribute " + name + " is optional, so a request without it would still validate"
	}
	if f, ok := byAttribute[name]; ok {
		if want := f.ValueType(); got != want {
			return "the fact " + f.Name + " is " + got + " in the schema, want " + want + " from the catalog"
		}
		return ""
	}
	s, ok := settings[name]
	if !ok {
		return "the context attribute " + name + " is neither a fact of the catalog, a profile setting nor a release value"
	}
	if got != s.CedarType {
		return "the setting " + name + " is " + got + " in the schema, want " + s.CedarType
	}
	return ""
}

func TestEveryActionReadsUsageAndTheHardCaps(t *testing.T) {
	// invariant.caps forbids any action when usage is over the hard caps
	// (decision 0142).
	for action, ctx := range contexts(t) {
		for _, name := range []cedartypes.String{"usage", "hardCaps"} {
			if _, ok := ctx[name]; !ok {
				t.Errorf("%s: the context does not declare %s", action, name)
			}
		}
	}
}

func TestTheValidatorUsesTheSchema(t *testing.T) {
	v := validate.New(resolvedSchema(t), validate.WithStrict())
	check := func(text string) error {
		t.Helper()
		var p cedar.Policy
		if err := p.UnmarshalCedar([]byte(text)); err != nil {
			t.Fatalf("cannot parse %q: %v", text, err)
		}
		return v.Policy("p", (*xast.Policy)(p.AST()))
	}
	if err := check(`forbid (principal, action == Pumi::Action::"merge", resource) unless { context.verifyPassed };`); err != nil {
		t.Errorf("a policy that reads a declared fact: %v", err)
	}
	if err := check(`forbid (principal, action == Pumi::Action::"merge", resource) unless { context.specValid };`); err == nil {
		t.Error("a policy that reads a fact merge does not declare passed validation")
	}
}
