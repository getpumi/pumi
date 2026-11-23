// SPDX-License-Identifier: Apache-2.0
// SPDX-FileCopyrightText: The Pumi Authors

package controlplane

import (
	"time"

	"github.com/cedar-policy/cedar-go"

	"getpumi.dev/pumi/api/policy"
)

// Config is how the wiring sets up the embedded control plane: the
// policies beside the core invariants, the profile settings, the hard caps
// and the project. Policy sets replace it in epic E10.
type Config struct {
	// Policies are the policy files beside the core invariants and the core
	// policies of the workflow actions, which the control plane always adds
	// (decisions 0142 and 0169).
	Policies []PolicyFile
	// Settings are the profile settings that the actions read.
	Settings Settings
	// HardCaps are the project's hard caps (decision 0147).
	HardCaps policy.Caps
	// Project is the project that the plans are in.
	Project Project
	// Clock is the source of time of the lease timers and of Claim's wait;
	// the operating system's clock when it is nil.
	Clock Clock
	// LeaseTimeout is how long a lease lasts unless a heartbeat renews it;
	// DefaultLeaseTimeout when it is 0.
	LeaseTimeout time.Duration
	// ClaimWait is how long Claim waits for an invocation before it returns
	// none; DefaultClaimWait when it is 0.
	ClaimWait time.Duration
}

// DefaultLeaseTimeout is the lease timeout when the wiring sets none.
const DefaultLeaseTimeout = 30 * time.Second

// DefaultClaimWait is how long Claim waits when the wiring sets no wait.
const DefaultClaimWait = 20 * time.Second

// Project is the project of the Cedar resource Plan (decision 0141).
type Project struct {
	// Name is the project's name, the id of its Cedar entity, and the key
	// of the facts kept per project, such as warnings.open.
	Name string
	// ProtectedPaths are the project's protected path patterns.
	ProtectedPaths []string
}

// Settings are the profile settings that the actions of the Cedar schema
// read, one field for each setting of policy.Settings, and the
// parallelism setting, which the kernel reads.
type Settings struct {
	// MaxAttempts is maxAttempts for each gate, by the gate's Cedar action,
	// such as planReview (decision 0044), and, by "in-doubt", for the retry
	// of an invocation in doubt from a state with no gate (decision 0201). A
	// gate with no entry has no maxAttempts, so a decision that reads it
	// denies (decision 0143).
	MaxAttempts map[string]int64
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
	// Parallelism is the parallelism setting: how many plans run at once
	// across all tracks (decisions 0172 and 0189). No action of the Cedar
	// schema reads it; the wiring gives it to the kernel. 0 counts as 1.
	// Profiles set it from epic E10.
	Parallelism int
}

// hardCaps is the name of the context attribute that holds the hard caps.
const hardCaps = "hardCaps"

// values returns the settings of a decision about gate as Cedar values, by
// their context names. maxAttempts is the gate's own, and is left out when
// the gate has none.
func (s Settings) values(gate string) map[string]cedar.Value {
	values := map[string]cedar.Value{
		"outsideChecksEnforced": stringSet(s.OutsideChecksEnforced),
		"outsideChecksWarned":   stringSet(s.OutsideChecksWarned),
		"reviewers":             cedar.Long(s.Reviewers),
		"usageBudget":           capsRecord(s.UsageBudget),
		"warningsCap":           cedar.Long(s.WarningsCap),
	}
	if n, ok := s.MaxAttempts[gate]; ok {
		values["maxAttempts"] = cedar.Long(n)
	}
	return values
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
