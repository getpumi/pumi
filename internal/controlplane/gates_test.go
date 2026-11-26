// SPDX-License-Identifier: Apache-2.0
// SPDX-FileCopyrightText: The Pumi Authors

package controlplane

import (
	"slices"
	"testing"

	eventsv1 "getpumi.dev/pumi/api/gen/pumi/events/v1"
	factsv1 "getpumi.dev/pumi/api/gen/pumi/facts/v1"
	"getpumi.dev/pumi/api/policy"
	"getpumi.dev/pumi/internal/controlplane/projections"
)

// TestTheSpecGateIsDecidedByItsPolicy: with no policy of the wiring, the
// spec gate allows when spec.valid is true or the spec gate is overridden
// for the tree, and denies otherwise, and over a hard cap (decisions 0142
// and 0299).
func TestTheSpecGateIsDecidedByItsPolicy(t *testing.T) {
	tests := []struct {
		name      string
		valid     bool
		overrides []string
		usage     int64
		allow     bool
		policies  []string
	}{
		{"a valid spec", true, nil, 10, true, []string{"gate.spec"}},
		{"a spec that is not valid", false, nil, 10, false, nil},
		{"a spec that is not valid, overridden", false, []string{"spec"}, 10, true, []string{"gate.spec"}},
		{"a spec that is not valid, another gate overridden", false, []string{"scope"}, 10, false, nil},
		{"a valid spec over the token cap", true, nil, policy.DefaultHardCaps().Tokens + 1, false, []string{"invariant.caps"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := testService(t)
			s.cfg.Policies = nil
			d, err := NewDecider(policy.Schema, policyFiles(s.cfg)...)
			if err != nil {
				t.Fatal(err)
			}
			s.decider = d
			for _, p := range []projections.Projection{
				fixed("spec.valid", &factsv1.SpecValid{Tree: "tree-1", SpecAdapterVersion: "v", Value: tt.valid}),
				fixed("overrides", &factsv1.Overrides{Plan: "plan-a", Tree: "tree-1", Value: tt.overrides}),
				fixed("usage", &factsv1.Usage{Invocation: "i", Plan: "plan-a", Value: &factsv1.Usage_Totals{Tokens: tt.usage}}),
			} {
				s.projections[p.Fact] = p
			}
			write(t, s, "plan-a", &eventsv1.PlanAdded{Plan: "plan-a", Track: "main"})
			got, data := decideAsKernel(t, s, question{action: "spec", plan: "plan-a", keys: projections.Keys{Invocation: "i", HeadTree: "tree-1"}})
			if got.Allow != tt.allow || !slices.Equal(data.GetDeterminingPolicies(), tt.policies) {
				t.Errorf("allow %v by %q (reasons %q), want %v by %q", got.Allow, data.GetDeterminingPolicies(), got.Reasons, tt.allow, tt.policies)
			}
		})
	}
}
