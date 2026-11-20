// SPDX-License-Identifier: Apache-2.0
// SPDX-FileCopyrightText: The Pumi Authors

package controlplane

import (
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/cedar-policy/cedar-go"
	"github.com/cedar-policy/cedar-go/x/exp/ast"
	"github.com/cedar-policy/cedar-go/x/exp/schema"
	"github.com/cedar-policy/cedar-go/x/exp/schema/resolved"
	"github.com/cedar-policy/cedar-go/x/exp/schema/validate"

	"getpumi.dev/pumi/api/facts"
)

// noPermit is the reason of a deny that no policy determines.
const noPermit = "no policy permits the request"

// PolicyFile is a file of Cedar policies, such as the core invariants or a
// lower set. Name is used in errors.
type PolicyFile struct {
	// Name is the file's name, such as invariants.cedar.
	Name string
	// Text is the file's Cedar policies.
	Text string
}

// Request is one request for a decision: the principal, the action and the
// resource, the context that holds the facts and settings, and the entities
// that the policies may read.
type Request struct {
	Principal cedar.EntityUID
	Action    cedar.EntityUID
	Resource  cedar.EntityUID
	Context   cedar.Record
	Entities  cedar.EntityMap
}

// Decision is the result of Decide, with what the decision.made event
// records about it.
type Decision struct {
	// Allow is true when the request is allowed.
	Allow bool
	// DeterminingPolicies are the @id annotations of the policies that
	// determine the decision, sorted: the forbid policies that deny, the
	// permit policies that allow, or the policies whose evaluation failed.
	DeterminingPolicies []string
	// Facts are the names of the facts of the catalog that the context
	// holds, sorted.
	Facts []string
	// Reasons say why a request is denied when no policy says it: the
	// validation error, the evaluation errors, or that no policy permits
	// the request. An allowed request has none.
	Reasons []string
}

// Decider decides requests with one set of policies against one schema
// (decision 0146).
type Decider struct {
	validator *validate.Validator
	policies  *cedar.PolicySet
	// contexts is the context type of each action of the schema.
	contexts map[cedar.EntityUID]resolved.RecordType
	// actions are the action entities of the schema, with the action
	// groups they are in, which the Rust CLI also takes from the schema.
	actions cedar.EntityMap
	// facts maps the Cedar context name of each fact of the catalog to the
	// fact's name.
	facts map[string]string
}

// NewDecider parses and resolves the Cedar schema text, and parses the
// policy files. Every policy needs an @id annotation, unique across the
// files, and must validate strictly against the schema. cedar-go reports
// no validation warnings, such as a policy that can never apply; make
// cedar refuses those in CI with the Rust CLI (decision 0146).
func NewDecider(schemaText string, files ...PolicyFile) (*Decider, error) {
	var s schema.Schema
	s.SetFilename("schema")
	if err := s.UnmarshalCedar([]byte(schemaText)); err != nil {
		return nil, fmt.Errorf("cannot parse the schema: %w", err)
	}
	r, err := s.Resolve()
	if err != nil {
		return nil, fmt.Errorf("cannot resolve the schema: %w", err)
	}
	catalog, err := facts.Catalog()
	if err != nil {
		return nil, fmt.Errorf("cannot read the fact catalog: %w", err)
	}
	d := &Decider{
		validator: validate.New(r, validate.WithStrict()),
		policies:  cedar.NewPolicySet(),
		contexts:  map[cedar.EntityUID]resolved.RecordType{},
		actions:   cedar.EntityMap{},
		facts:     map[string]string{},
	}
	for id, a := range r.Actions {
		d.actions[id] = a.Entity
		if a.AppliesTo != nil {
			d.contexts[id] = a.AppliesTo.Context
		}
	}
	for _, f := range catalog {
		d.facts[f.Attribute()] = f.Name
	}
	for _, file := range files {
		if err := d.add(file); err != nil {
			return nil, err
		}
	}
	return d, nil
}

// add parses the policies of file, checks them and adds them to the set.
func (d *Decider) add(file PolicyFile) error {
	list, err := cedar.NewPolicyListFromBytes(file.Name, []byte(file.Text))
	if err != nil {
		return fmt.Errorf("%s: %w", file.Name, err)
	}
	for _, p := range list {
		id, ok := p.Annotations()["id"]
		if !ok {
			return fmt.Errorf("%s: the policy at line %d has no @id annotation", file.Name, p.Position().Line)
		}
		if err := d.validator.Policy(string(id), (*ast.Policy)(p.AST())); err != nil {
			return fmt.Errorf("%s: policy %s does not validate against the schema: %w", file.Name, id, err)
		}
		if !d.policies.Add(cedar.PolicyID(id), p) {
			return fmt.Errorf("%s: the @id %s is already used", file.Name, id)
		}
	}
	return nil
}

// Decide validates the request and its entities against the schema and
// then authorizes the request. It denies a request that does not validate
// and a request whose evaluation fails in any policy, because cedar-go
// skips a policy whose evaluation fails, so a forbid policy that fails for
// a missing or wrong fact would not deny (decision 0146).
func (d *Decider) Decide(req Request) Decision {
	creq := cedar.Request{
		Principal: req.Principal,
		Action:    req.Action,
		Resource:  req.Resource,
		Context:   req.Context,
	}
	decision := Decision{Facts: d.factsOf(req.Context)}
	if problems := d.validationProblems(creq, req.Entities); len(problems) > 0 {
		decision.Reasons = problems
		return decision
	}
	entities := maps.Clone(req.Entities)
	if entities == nil {
		entities = cedar.EntityMap{}
	}
	maps.Copy(entities, d.actions)
	result, diag := cedar.Authorize(d.policies, entities, creq)
	if result == cedar.Deny || len(diag.Errors) == 0 {
		// On a deny the reasons are the forbid policies that apply; on an
		// allow with errors they are permit policies, which do not
		// determine the deny that follows.
		for _, r := range diag.Reasons {
			decision.DeterminingPolicies = append(decision.DeterminingPolicies, string(r.PolicyID))
		}
	}
	for _, e := range diag.Errors {
		decision.DeterminingPolicies = append(decision.DeterminingPolicies, string(e.PolicyID))
		decision.Reasons = append(decision.Reasons, e.String())
	}
	slices.Sort(decision.DeterminingPolicies)
	decision.DeterminingPolicies = slices.Compact(decision.DeterminingPolicies)
	decision.Allow = result == cedar.Allow && len(diag.Errors) == 0
	if !decision.Allow && len(decision.DeterminingPolicies) == 0 {
		decision.Reasons = []string{noPermit}
	}
	return decision
}

// validationProblems validates the request and the entities against the
// schema, and returns a reason for each problem: cedar-go's errors, with
// the context attributes and the entities that do not conform named,
// because cedar-go's errors do not name them.
func (d *Decider) validationProblems(req cedar.Request, entities cedar.EntityMap) []string {
	problems := []string{}
	if err := d.validator.Request(req); err != nil {
		problems = append(problems, "the request does not validate against the schema: "+err.Error())
		problems = append(problems, d.contextProblems(req.Action, req.Context)...)
	}
	if err := d.validator.Entities(entities); err != nil {
		problems = append(problems, "the entities do not validate against the schema: "+err.Error())
		for _, id := range slices.SortedFunc(maps.Keys(entities), compareUIDs) {
			if err := d.validator.Entity(entities[id]); err != nil {
				problems = append(problems, fmt.Sprintf("the entity %s does not validate: %v", id, err))
			}
		}
	}
	return problems
}

// compareUIDs orders entity UIDs by their Cedar text.
func compareUIDs(a, b cedar.EntityUID) int {
	return strings.Compare(a.String(), b.String())
}

// factsOf returns the sorted names of the catalog's facts that context
// holds.
func (d *Decider) factsOf(context cedar.Record) []string {
	names := []string{}
	for name := range context.All() {
		if f, ok := d.facts[string(name)]; ok {
			names = append(names, f)
		}
	}
	slices.Sort(names)
	return names
}

// contextProblems names the attributes that the schema requires in the
// context of action and context lacks, and the attributes of context that
// the schema does not declare, because cedar-go's validation error does
// not name them.
func (d *Decider) contextProblems(action cedar.EntityUID, context cedar.Record) []string {
	want, ok := d.contexts[action]
	if !ok {
		return nil
	}
	problems := []string{}
	for name, attr := range want {
		if _, ok := context.Get(name); !ok && !attr.Optional {
			problems = append(problems, fmt.Sprintf("the context lacks the attribute %s", name))
		}
	}
	for name := range context.All() {
		if _, ok := want[name]; !ok {
			problems = append(problems, fmt.Sprintf("the context has the attribute %s, which the schema does not declare", name))
		}
	}
	slices.Sort(problems)
	return problems
}
