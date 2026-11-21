// SPDX-License-Identifier: Apache-2.0
// SPDX-FileCopyrightText: The Pumi Authors

package controlplane

import (
	"github.com/cedar-policy/cedar-go"

	"getpumi.dev/pumi/api/policy"
)

// Config is how the wiring sets up the embedded control plane: the
// policies beside the core invariants, the profile settings, the hard caps
// and the project. Policy sets replace it in epic E10.
type Config struct {
	// Policies are the policy files beside the core invariants, which the
	// control plane always adds (decision 0142).
	Policies []PolicyFile
	// Settings are the profile settings that the actions read.
	Settings Settings
	// HardCaps are the project's hard caps (decision 0147).
	HardCaps policy.Caps
	// Project is the project that the plans are in.
	Project Project
}

// Project is the project of the Cedar resource Plan (decision 0141).
type Project struct {
	// Name is the project's name, the id of its Cedar entity, and the key
	// of the facts kept per project, such as warnings.open.
	Name string
	// ProtectedPaths are the project's protected path patterns.
	ProtectedPaths []string
}

// Settings are the profile settings that the actions of the Cedar schema
// read, one field for each setting of policy.Settings.
type Settings struct {
	// MaxAttempts is maxAttempts.
	MaxAttempts int64
	// OutsideChecksEnforced is outsideChecksEnforced.
	OutsideChecksEnforced []string
	// OutsideChecksWarned is outsideChecksWarned.
	OutsideChecksWarned []string
	// Reviewers is reviewers.
	Reviewers int64
	// UsageBudget is usageBudget.
	UsageBudget policy.Caps
	// WarningsCap is warningsCap.
	WarningsCap int64
}

// hardCaps is the name of the context attribute that holds the hard caps.
const hardCaps = "hardCaps"

// values returns the settings as Cedar values, by their context names.
func (s Settings) values() map[string]cedar.Value {
	return map[string]cedar.Value{
		"maxAttempts":           cedar.Long(s.MaxAttempts),
		"outsideChecksEnforced": stringSet(s.OutsideChecksEnforced),
		"outsideChecksWarned":   stringSet(s.OutsideChecksWarned),
		"reviewers":             cedar.Long(s.Reviewers),
		"usageBudget":           capsRecord(s.UsageBudget),
		"warningsCap":           cedar.Long(s.WarningsCap),
	}
}

// stringSet returns the strings as a Cedar set.
func stringSet(in []string) cedar.Set {
	values := make([]cedar.Value, 0, len(in))
	for _, s := range in {
		values = append(values, cedar.String(s))
	}
	return cedar.NewSet(values...)
}

// capsRecord returns caps as the Cedar record of the Usage type.
func capsRecord(c policy.Caps) cedar.Record {
	m := cedar.RecordMap{}
	for name, v := range c.Context() {
		m[cedar.String(name)] = cedar.Long(v)
	}
	return cedar.NewRecord(m)
}
