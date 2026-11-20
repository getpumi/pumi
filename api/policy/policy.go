// SPDX-License-Identifier: Apache-2.0
// SPDX-FileCopyrightText: The Pumi Authors

package policy

import (
	_ "embed" // for the schema
)

// Schema is the Cedar schema, pumi.cedarschema.
//
//go:embed pumi.cedarschema
var Schema string

// Setting is a profile setting that an action's context declares.
type Setting struct {
	// Name is the setting's name in the context, such as reviewers.
	Name string
	// CedarType is the Cedar type of the setting: Bool, Long, Set<String> or
	// Record.
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
		{"maxAttempts", "Long", "The attempts a gate gets before it escalates to a person (decision 0044)."},
		{"requiredOutsideChecks", "Set<String>", "The checks Pumi does not run that must pass on the head commit, for the response being decided (decision 0140)."},
		{"reviewers", "Long", "The independent passing verdicts a model-judgment gate needs (decision 0138)."},
		{"usageBudget", "Record", "The gate's token, cost and time budget, under the hard caps (decision 0044)."},
		{"warningsCap", "Long", "The open warnings before warned gates enforce again (decision 0044)."},
	}
}
