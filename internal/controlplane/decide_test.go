// SPDX-License-Identifier: Apache-2.0
// SPDX-FileCopyrightText: The Pumi Authors

package controlplane

import (
	"slices"
	"strings"
	"testing"

	"github.com/cedar-policy/cedar-go"
	cedartypes "github.com/cedar-policy/cedar-go/types"

	"getpumi.dev/pumi/api/policy"
)

const permitAllText = "@id(\"permit-all\")\npermit (principal, action, resource);\n"

// newDecider returns a decider for the invariants and the lower set text.
func newDecider(t *testing.T, lower string) *Decider {
	t.Helper()
	d, err := NewDecider(policy.Schema,
		PolicyFile{Name: "invariants.cedar", Text: policy.Invariants},
		PolicyFile{Name: "lower.cedar", Text: lower})
	if err != nil {
		t.Fatal(err)
	}
	return d
}

// mergeRequest is a merge that meets every invariant, from the first case
// of api/policy/testdata/permit-all.tests.json.
func mergeRequest(t *testing.T) Request {
	t.Helper()
	tt := runTests(t)[0]
	if tt.Name != "a merge that meets every invariant is allowed" {
		t.Fatalf("the first case of permit-all.tests.json is %q", tt.Name)
	}
	return Request{
		Principal: uid(t, tt.Request.Principal),
		Action:    uid(t, tt.Request.Action),
		Resource:  uid(t, tt.Request.Resource),
		Context:   tt.Request.Context,
		Entities:  tt.Entities,
	}
}

// withoutAttribute returns the request without the context attribute name.
func withoutAttribute(req Request, name string) Request {
	m := req.Context.Map()
	delete(m, cedartypes.String(name))
	req.Context = cedartypes.NewRecord(m)
	return req
}

func TestDecideAllowsAndNamesThePoliciesAndFacts(t *testing.T) {
	d := newDecider(t, permitAllText)
	got := d.Decide(mergeRequest(t))
	if !got.Allow {
		t.Fatalf("Decide() = %+v, want allow", got)
	}
	if !slices.Equal(got.DeterminingPolicies, []string{"permit-all"}) {
		t.Errorf("DeterminingPolicies = %q, want permit-all", got.DeterminingPolicies)
	}
	wantFacts := []string{"head.state", "paths.protected", "usage", "verify.passed"}
	if !slices.Equal(got.Facts, wantFacts) {
		t.Errorf("Facts = %q, want %q", got.Facts, wantFacts)
	}
	if len(got.Reasons) != 0 {
		t.Errorf("Reasons = %q, want none", got.Reasons)
	}
}

func TestDecideDeniesWithTheForbiddingPolicy(t *testing.T) {
	req := mergeRequest(t)
	m := req.Context.Map()
	m["verifyPassed"] = cedartypes.False
	req.Context = cedartypes.NewRecord(m)
	got := newDecider(t, permitAllText).Decide(req)
	if got.Allow || !slices.Equal(got.DeterminingPolicies, []string{"invariant.verify"}) {
		t.Errorf("Decide() = %+v, want deny by invariant.verify", got)
	}
}

func TestDecideDeniesAMissingFactThatPlainAuthorizationAllows(t *testing.T) {
	req := withoutAttribute(mergeRequest(t), "verifyPassed")
	set := policySet(t, "invariants.cedar and permit-all", policy.Invariants+"\n"+permitAllText)
	plain, diag := cedar.Authorize(set, req.Entities, cedar.Request{
		Principal: req.Principal, Action: req.Action, Resource: req.Resource, Context: req.Context,
	})
	if plain != cedar.Allow || len(diag.Errors) == 0 {
		t.Fatalf("plain authorization = %v with errors %v; the test needs an allow with an error", plain, diag.Errors)
	}
	got := newDecider(t, permitAllText).Decide(req)
	if got.Allow {
		t.Fatalf("Decide() = %+v, want deny", got)
	}
	if len(got.Reasons) == 0 || !strings.Contains(strings.Join(got.Reasons, "\n"), "verifyPassed") {
		t.Errorf("Reasons = %q, want one that names verifyPassed", got.Reasons)
	}
}

func TestDecideDeniesAnEvaluationError(t *testing.T) {
	overflow := permitAllText + "\n@id(\"overflow\")\nforbid (principal, action == Pumi::Action::\"merge\", resource)\nwhen { context.usage.tokens * 9223372036854775807 > 0 };\n"
	got := newDecider(t, overflow).Decide(mergeRequest(t))
	if got.Allow {
		t.Fatalf("Decide() = %+v, want deny", got)
	}
	if !slices.Equal(got.DeterminingPolicies, []string{"overflow"}) {
		t.Errorf("DeterminingPolicies = %q, want the policy that failed", got.DeterminingPolicies)
	}
	if len(got.Reasons) == 0 || !strings.Contains(got.Reasons[0], "overflow") {
		t.Errorf("Reasons = %q, want the evaluation error", got.Reasons)
	}
}

func TestDecideDeniesInvalidEntities(t *testing.T) {
	req := mergeRequest(t)
	entities := cedartypes.EntityMap{}
	for k, e := range req.Entities {
		if k.Type == "Pumi::Plan" {
			e.Attributes = cedartypes.NewRecord(cedartypes.RecordMap{"tree": cedartypes.Long(1)})
		}
		entities[k] = e
	}
	req.Entities = entities
	got := newDecider(t, permitAllText).Decide(req)
	if got.Allow || !strings.Contains(strings.Join(got.Reasons, "\n"), `Pumi::Plan::"p1"`) {
		t.Errorf("Decide() = %+v, want deny with a reason that names the Plan entity", got)
	}
}

func TestDecideDeniesByDefault(t *testing.T) {
	got := newDecider(t, "").Decide(mergeRequest(t))
	if got.Allow || len(got.DeterminingPolicies) != 0 || len(got.Reasons) != 1 || got.Reasons[0] != "no policy permits the request" {
		t.Errorf("Decide() = %+v, want deny because nothing permits", got)
	}
}

func TestNewDeciderRefuses(t *testing.T) {
	for name, lower := range map[string]string{
		"a policy that does not validate": "@id(\"x\")\nforbid (principal, action, resource)\nwhen { context.nothing };\n",
		"a policy without an id":          "permit (principal, action, resource);\n",
		"two policies with one id":        permitAllText + permitAllText,
		"an invariant's id":               "@id(\"invariant.verify\")\npermit (principal, action, resource);\n",
		"text that does not parse":        "permit (",
	} {
		t.Run(name, func(t *testing.T) {
			_, err := NewDecider(policy.Schema,
				PolicyFile{Name: "invariants.cedar", Text: policy.Invariants},
				PolicyFile{Name: "lower.cedar", Text: lower})
			if err == nil || !strings.Contains(err.Error(), "lower.cedar") {
				t.Errorf("NewDecider() = %v, want an error that names lower.cedar", err)
			}
		})
	}
	if _, err := NewDecider("namespace {", PolicyFile{Name: "x.cedar", Text: permitAllText}); err == nil {
		t.Error("NewDecider() accepted a schema that does not parse")
	}
}

func TestDecideNamesAnUndeclaredContextAttribute(t *testing.T) {
	req := mergeRequest(t)
	m := req.Context.Map()
	m["extra"] = cedartypes.True
	req.Context = cedartypes.NewRecord(m)
	got := newDecider(t, permitAllText).Decide(req)
	if got.Allow || !strings.Contains(strings.Join(got.Reasons, "\n"), "extra, which the schema does not declare") {
		t.Errorf("Decide() = %+v, want deny with a reason that names extra", got)
	}
}

func TestDecideNamesTheForbidAsWellAsTheFailedPolicy(t *testing.T) {
	overflow := permitAllText + "\n@id(\"overflow\")\nforbid (principal, action == Pumi::Action::\"merge\", resource)\nwhen { context.usage.tokens * 9223372036854775807 > 0 };\n"
	req := mergeRequest(t)
	m := req.Context.Map()
	m["verifyPassed"] = cedartypes.False
	req.Context = cedartypes.NewRecord(m)
	got := newDecider(t, overflow).Decide(req)
	if got.Allow || !slices.Equal(got.DeterminingPolicies, []string{"invariant.verify", "overflow"}) {
		t.Errorf("Decide() = %+v, want deny determined by invariant.verify and overflow", got)
	}
}

// groupSchema has an action in an action group, which only the schema
// declares.
const groupSchema = `entity User;
entity Doc;
action all;
action read in [all] appliesTo { principal: User, resource: Doc };
`

func TestDecideKnowsTheSchemasActionGroups(t *testing.T) {
	d, err := NewDecider(groupSchema, PolicyFile{Name: "group.cedar", Text: "@id(\"in-all\")\npermit (principal, action in Action::\"all\", resource);\n"})
	if err != nil {
		t.Fatal(err)
	}
	got := d.Decide(Request{
		Principal: cedartypes.NewEntityUID("User", "u"),
		Action:    cedartypes.NewEntityUID("Action", "read"),
		Resource:  cedartypes.NewEntityUID("Doc", "d"),
		Context:   cedartypes.NewRecord(nil),
		Entities:  cedartypes.EntityMap{},
	})
	if !got.Allow || !slices.Equal(got.DeterminingPolicies, []string{"in-all"}) {
		t.Errorf("Decide() = %+v, want allow by in-all, because read is in the group all", got)
	}
}
