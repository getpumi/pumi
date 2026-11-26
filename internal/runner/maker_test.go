// SPDX-License-Identifier: Apache-2.0
// SPDX-FileCopyrightText: The Pumi Authors

package runner

import (
	"testing"

	harnessv1 "getpumi.dev/pumi/api/gen/pumi/harness/v1"
)

// The makers of the models of the fixture's role and its fallback, as the
// tests give them to the runner in place of the price table.
const (
	roleMaker     = "Anthropic"
	fallbackMaker = "OpenAI"
)

// testMaker is the maker of the models of the tests, by model.
func testMaker(model string) string {
	return map[string]string{roleModel: roleMaker, fallbackModel: fallbackMaker}[model]
}

// TestTheResultRecordsTheMakerOfTheRolesModel: the result of an agent run
// records the maker of the model of its model role, which the runner takes
// from the configuration's Maker (decision 0261).
func TestTheResultRecordsTheMakerOfTheRolesModel(t *testing.T) {
	f := newFixture(t, "success")
	f.withFallback(t, false)
	f.cfg.Maker = testMaker
	if got := f.runOne(t).GetResult().GetMaker(); got != roleMaker {
		t.Errorf("the result's maker is %q, want %q, the maker of the role's model", got, roleMaker)
	}
}

// TestTheResultAfterAFallbackRecordsTheFallbacksMaker: after the limit
// switched the role to its fallback on another adapter, the result is the
// fallback run's, and records the maker of the fallback's model, which ran
// (decisions 0246 and 0260).
func TestTheResultAfterAFallbackRecordsTheFallbacksMaker(t *testing.T) {
	f := newFixture(t, "limited but the fallback")
	f.withFallback(t, false)
	f.cfg.Maker = testMaker
	if got := f.runOne(t).GetResult().GetMaker(); got != fallbackMaker {
		t.Errorf("the result's maker is %q, want %q, the maker of the fallback's model", got, fallbackMaker)
	}
}

// TestACheckRecordsNoMaker: a check runs no model, so its result names no
// maker.
func TestACheckRecordsNoMaker(t *testing.T) {
	f := newFixture(t, "success")
	f.withFallback(t, false)
	f.cfg.Maker = testMaker
	f.service.claim.Invocation.Component.Kind = harnessv1.Component_KIND_CHECK
	f.service.claim.Invocation.ModelRole = ""
	f.service.claim.Invocation.Capabilities.Network.Access = harnessv1.Capabilities_Network_ACCESS_NONE
	if got := f.runOne(t).GetResult().GetMaker(); got != "" {
		t.Errorf("the check's result names the maker %q, want none", got)
	}
}
