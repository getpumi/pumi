// SPDX-License-Identifier: Apache-2.0
// SPDX-FileCopyrightText: The Pumi Authors

package policy

import (
	_ "embed" // for the schema
	"regexp"
	"slices"
)

// Schema is the Cedar schema, pumi.cedarschema.
//
//go:embed pumi.cedarschema
var Schema string

// Invariants are the core invariants of decision 0142, invariants.cedar.
//
//go:embed invariants.cedar
var Invariants string

// Core are the core policies of the workflow actions, core.cedar: the
// permits that let the kernel escalate, retry, advance, resume and ask, and
// the forbid policies of the retry limit and of ask (decisions 0169 to
// 0171). The control plane adds them, with the invariants, to every policy
// set.
//
//go:embed core.cedar
var Core string

// Caps are hard caps on usage. They are the only floor under every profile
// (decision 0012).
type Caps struct {
	// Tokens is the most tokens.
	Tokens int64
	// CostMicrodollars is the most cost, in microdollars.
	CostMicrodollars int64
	// Seconds is the most time.
	Seconds int64
}

// DefaultHardCaps returns the default hard caps of this release. The
// adopter sets a project's hard caps during bootstrapping, and the
// bootstrapping interview offers these values (decision 0147);
// invariant.caps reads the project's caps as context.hardCaps, and the
// model gateway enforces them as well (decisions 0012 and 0142). It
// returns a copy, so no importer can change them. The values are
// placeholders until profiling replaces them, as the first profile
// drafts' budgets and caps are: five million tokens, 50 dollars and four
// hours.
func DefaultHardCaps() Caps {
	return Caps{
		Tokens:           5_000_000,
		CostMicrodollars: 50_000_000,
		Seconds:          4 * 60 * 60,
	}
}

// Context returns the caps as the record that the hardCaps context
// attribute holds, by attribute name.
func (c Caps) Context() map[string]int64 {
	return map[string]int64{
		"tokens":           c.Tokens,
		"costMicrodollars": c.CostMicrodollars,
		"seconds":          c.Seconds,
	}
}

// ProjectValues returns the values that the project configuration, not
// the profile, puts in the context of every action.
func ProjectValues() []Setting {
	return []Setting{
		{"hardCaps", "{costMicrodollars: Long, seconds: Long, tokens: Long}", "The project's hard caps on usage, set during bootstrapping from DefaultHardCaps (decisions 0142 and 0147)."},
	}
}

// Setting is a profile setting that an action's context declares.
type Setting struct {
	// Name is the setting's name in the context, such as reviewers.
	Name string
	// CedarType is the Cedar type of the setting in the form of
	// facts.Fact.ValueType: Bool, Long, Set<String> or a record such as
	// {seconds: Long, tokens: Long}.
	CedarType string
	// About says what the setting is and which decision adds it.
	About string
}

// Settings returns the profile settings that the schema's actions read,
// sorted by name. They are the settings of decision 0044, 0138 and 0140
// that a gate checks; the others, such as where a gate runs, are applied by
// the control plane and the kernel, not by a policy.
func Settings() []Setting {
	return []Setting{
		{"maxAttempts", "Long", "The attempts a gate gets before it escalates to a person, set for each gate; a retry reads the setting of the gate it retries (decisions 0044 and 0169)."},
		{"outsideChecksEnforced", "Set<String>", "The required outside checks whose response is enforce: checks Pumi does not run that must pass on the head commit (decision 0140)."},
		{"outsideChecksWarned", "Set<String>", "The required outside checks whose response is warn (decision 0140)."},
		{"reviewers", "Long", "The independent passing verdicts a model-judgment gate needs (decision 0138)."},
		{"usageBudget", "{costMicrodollars: Long, seconds: Long, tokens: Long}", "The gate's token, cost and time budget, under the hard caps (decision 0044)."},
		{"warningsCap", "Long", "The open warnings before warned gates enforce again (decision 0044)."},
	}
}

// actionDecl matches the declaration of an action in the schema, which
// pumi.cedarschema writes one to a line as "action <name> appliesTo {".
var actionDecl = regexp.MustCompile(`(?m)^\s*action\s+([A-Za-z_][A-Za-z0-9_]*)\s+appliesTo\b`)

// Actions returns the names of the actions that the schema declares,
// sorted. The workflow loader refuses a transition guarded by any other
// name (decision 0168). The control plane's tests check the list against
// the schema as cedar-go reads it.
func Actions() []string {
	names := []string{}
	for _, m := range actionDecl.FindAllStringSubmatch(Schema, -1) {
		names = append(names, m[1])
	}
	slices.Sort(names)
	return slices.Compact(names)
}

// AlwaysAllowed returns the actions that are always allowed for the kernel
// unless an invariant forbids it, sorted: each reads no facts, and a core
// policy permits it for the kernel. They are escalate (core.escalate,
// decision 0169) and cancel (core.cancel, decision 0177). The workflow
// loader requires the last transition of each trigger to be guarded by one
// of them, so that a plan never has no move (decision 0169). It returns a
// new list each time.
func AlwaysAllowed() []string {
	return []string{"cancel", "escalate"}
}
