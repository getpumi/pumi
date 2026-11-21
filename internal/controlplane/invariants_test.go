// SPDX-License-Identifier: Apache-2.0
// SPDX-FileCopyrightText: The Pumi Authors

package controlplane

import (
	"encoding/json"
	"io/fs"
	"maps"
	"os"
	"slices"
	"sort"
	"testing"

	"github.com/cedar-policy/cedar-go"
	cedartypes "github.com/cedar-policy/cedar-go/types"
	xast "github.com/cedar-policy/cedar-go/x/exp/ast"
	"github.com/cedar-policy/cedar-go/x/exp/schema/validate"

	"getpumi.dev/pumi/api/facts"
	factsv1 "getpumi.dev/pumi/api/gen/pumi/facts/v1"
	"getpumi.dev/pumi/api/policy"
)

// policySet parses the Cedar policies in text into a set whose policy IDs
// are their @id annotations.
func policySet(t *testing.T, name, text string) *cedar.PolicySet {
	t.Helper()
	list, err := cedar.NewPolicyListFromBytes(name, []byte(text))
	if err != nil {
		t.Fatalf("cedar-go cannot parse %s: %v", name, err)
	}
	set := cedar.NewPolicySet()
	for _, p := range list {
		id, ok := p.Annotations()["id"]
		if !ok {
			t.Fatalf("%s: a policy at %v has no @id annotation", name, p.Position())
		}
		if !set.Add(cedar.PolicyID(id), p) {
			t.Fatalf("%s: two policies have the @id %s", name, id)
		}
	}
	return set
}

func TestInvariantsAreTheSixForbidPoliciesOf0142(t *testing.T) {
	set := policySet(t, "invariants.cedar", policy.Invariants)
	ids := []string{}
	for id, p := range set.All() {
		ids = append(ids, string(id))
		if p.Effect() != cedar.Forbid {
			t.Errorf("%s is not a forbid policy", id)
		}
	}
	sort.Strings(ids)
	want := []string{
		"invariant.approved-head", "invariant.caps", "invariant.people-decide",
		"invariant.protected-paths", "invariant.verdicts-by-gate-role", "invariant.verify",
	}
	if !slices.Equal(ids, want) {
		t.Errorf("invariant ids = %q, want %q", ids, want)
	}
}

func TestInvariantsValidateStrictly(t *testing.T) {
	v := validate.New(resolvedSchema(t), validate.WithStrict())
	for id, p := range policySet(t, "invariants.cedar", policy.Invariants).All() {
		if err := v.Policy(string(id), (*xast.Policy)(p.AST())); err != nil {
			t.Errorf("%s does not validate against the schema: %v", id, err)
		}
	}
}

// contextReads returns the context attributes that p reads, and whether it
// uses the context in any other way, such as passing it whole.
func contextReads(t *testing.T, p *cedar.Policy) (map[string]bool, bool) {
	t.Helper()
	b, err := p.MarshalJSON()
	if err != nil {
		t.Fatal(err)
	}
	var tree any
	if err := json.Unmarshal(b, &tree); err != nil {
		t.Fatal(err)
	}
	w := &walker{reads: map[string]bool{}}
	w.walk(tree)
	return w.reads, w.other
}

// walker collects the context reads of a policy in Cedar's JSON format.
type walker struct {
	reads map[string]bool
	other bool
}

// walk visits the JSON node n.
func (w *walker) walk(n any) {
	switch n := n.(type) {
	case map[string]any:
		for op, v := range n {
			w.node(op, v)
		}
	case []any:
		for _, v := range n {
			w.walk(v)
		}
	}
}

// node visits the operator op of a JSON object, with its operand v.
func (w *walker) node(op string, v any) {
	if attr, ok := contextAttribute(op, v); ok {
		w.reads[attr] = true
		return
	}
	if op == "Var" && v == "context" {
		w.other = true
	}
	w.walk(v)
}

// contextAttribute returns the attribute that the operator op reads from
// the context, when op is . or has on the variable context.
func contextAttribute(op string, v any) (string, bool) {
	access, ok := v.(map[string]any)
	if !ok || (op != "." && op != "has") || !isContext(access["left"]) {
		return "", false
	}
	attr, ok := access["attr"].(string)
	return attr, ok
}

// isContext reports whether the JSON policy node n is the variable context.
func isContext(n any) bool {
	m, ok := n.(map[string]any)
	return ok && len(m) == 1 && m["Var"] == "context"
}

// forbiddenReads returns the context attributes that no invariant may read:
// the facts from model judgment (decision 0137) and overrides (decision
// 0139).
func forbiddenReads(t *testing.T) map[string]string {
	t.Helper()
	catalog, err := facts.Catalog()
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]string{"overrides": "the overrides fact"}
	for _, f := range catalog {
		if f.Source == factsv1.Source_SOURCE_MODEL {
			out[f.Attribute()] = "the model-judgment fact " + f.Name
		}
	}
	return out
}

func TestNoInvariantReadsAModelFactOrOverrides(t *testing.T) {
	forbidden := forbiddenReads(t)
	if len(forbidden) != 4 {
		t.Errorf("forbidden reads = %v, want overrides and the three model-judgment facts", slices.Sorted(maps.Keys(forbidden)))
	}
	for id, p := range policySet(t, "invariants.cedar", policy.Invariants).All() {
		reads, other := contextReads(t, p)
		if other {
			t.Errorf("%s uses the context other than by reading an attribute", id)
		}
		for attr := range reads {
			if what, ok := forbidden[attr]; ok {
				t.Errorf("%s reads %s (%s)", id, attr, what)
			}
		}
	}
}

func TestContextReadsFindsWhatAPolicyReads(t *testing.T) {
	set := policySet(t, "fixture", `
@id("reads")
forbid (principal, action == Pumi::Action::"planReview", resource)
unless { context has planReview && context.planReview.pass > 0 && context.overrides.isEmpty() };
@id("whole")
forbid (principal, action, resource) when { context == {} };
`)
	reads, other := contextReads(t, set.Get("reads"))
	if !maps.Equal(reads, map[string]bool{"planReview": true, "overrides": true}) || other {
		t.Errorf("contextReads(reads) = %v, %v; want planReview and overrides, false", reads, other)
	}
	if _, other := contextReads(t, set.Get("whole")); !other {
		t.Error("contextReads(whole) did not report the use of the whole context")
	}
}

// runTest is one case of testdata/permit-all.tests.json, in the format of
// cedar run-tests.
type runTest struct {
	Name    string `json:"name"`
	Request struct {
		Principal string            `json:"principal"`
		Action    string            `json:"action"`
		Resource  string            `json:"resource"`
		Context   cedartypes.Record `json:"context"`
	} `json:"request"`
	Entities  cedartypes.EntityMap `json:"entities"`
	Decision  string               `json:"decision"`
	Reason    []string             `json:"reason"`
	NumErrors int                  `json:"num_errors"`
}

// runTests reads the cases of testdata/permit-all.tests.json, which
// cedar run-tests runs on invariants.cedar followed by permit-all.cedar.
func runTests(t *testing.T) []runTest {
	t.Helper()
	b, err := fs.ReadFile(os.DirFS("../../api/policy/testdata"), "permit-all.tests.json")
	if err != nil {
		t.Fatal(err)
	}
	var tests []runTest
	if err := json.Unmarshal(b, &tests); err != nil {
		t.Fatalf("cannot read permit-all.tests.json: %v", err)
	}
	return tests
}

// uid parses an entity reference such as Pumi::Kernel::"kernel".
func uid(t *testing.T, s string) cedartypes.EntityUID {
	t.Helper()
	var u cedartypes.EntityUID
	if err := u.UnmarshalCedar([]byte(s)); err != nil {
		t.Fatalf("cannot parse the entity reference %s: %v", s, err)
	}
	return u
}

func TestInvariantsDecideTheRunTestsCasesWithAPermitAllLayer(t *testing.T) {
	permitAll, err := fs.ReadFile(os.DirFS("../../api/policy/testdata"), "permit-all.cedar")
	if err != nil {
		t.Fatal(err)
	}
	set := policySet(t, "invariants.cedar and permit-all.cedar", policy.Invariants+"\n"+string(permitAll))
	v := validate.New(resolvedSchema(t), validate.WithStrict())
	capsMap := cedartypes.RecordMap{}
	for name, value := range policy.DefaultHardCaps().Context() {
		capsMap[cedartypes.String(name)] = cedartypes.Long(value)
	}
	caps := cedartypes.NewRecord(capsMap)
	tests := runTests(t)
	if len(tests) == 0 {
		t.Fatal("permit-all.tests.json has no cases")
	}
	for _, tt := range tests {
		t.Run(tt.Name, func(t *testing.T) {
			checkCase(t, set, v, caps, tt)
		})
	}
}

// checkCase checks that the request of tt validates, carries the release's
// default caps and gets the decision, reasons and errors that tt expects.
func checkCase(t *testing.T, set *cedar.PolicySet, v *validate.Validator, caps cedartypes.Record, tt runTest) {
	t.Helper()
	req := cedar.Request{
		Principal: uid(t, tt.Request.Principal),
		Action:    uid(t, tt.Request.Action),
		Resource:  uid(t, tt.Request.Resource),
		Context:   tt.Request.Context,
	}
	// The permissions read no facts and no caps (decision 0164).
	got, ok := tt.Request.Context.Get("hardCaps")
	if slices.Contains(permissions, string(req.Action.ID)) {
		if ok {
			t.Errorf("the case of the permission %s has hardCaps %v, want none", req.Action.ID, got)
		}
	} else if !ok || !got.Equal(caps) {
		t.Errorf("the case's hardCaps are %v, want the release's defaults %v", got, caps)
	}
	if err := v.Request(req); err != nil {
		t.Errorf("the request does not validate against the schema: %v", err)
	}
	if err := v.Entities(tt.Entities); err != nil {
		t.Errorf("the entities do not validate against the schema: %v", err)
	}
	decision, diag := cedar.Authorize(set, tt.Entities, req)
	want := cedar.Deny
	if tt.Decision == "allow" {
		want = cedar.Allow
	}
	reasons := []string{}
	for _, r := range diag.Reasons {
		reasons = append(reasons, string(r.PolicyID))
	}
	sort.Strings(reasons)
	if decision != want || !slices.Equal(reasons, slices.Sorted(slices.Values(tt.Reason))) || len(diag.Errors) != tt.NumErrors {
		t.Errorf("decision %v, reasons %q, errors %v; want %s, %q, %d errors", decision, reasons, diag.Errors, tt.Decision, tt.Reason, tt.NumErrors)
	}
}
