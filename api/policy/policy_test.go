// SPDX-License-Identifier: Apache-2.0
// SPDX-FileCopyrightText: The Pumi Authors

package policy

import (
	"maps"
	"slices"
	"strings"
	"testing"

	"getpumi.dev/pumi/api/facts"
)

func TestSchemaIsEmbedded(t *testing.T) {
	if !strings.HasPrefix(Schema, "// SPDX-License-Identifier: Apache-2.0\n") {
		t.Errorf("Schema does not start with the SPDX lines: %.80q", Schema)
	}
	if !strings.Contains(Schema, "namespace Pumi {") {
		t.Error("Schema has no Pumi namespace")
	}
}

func TestSettings(t *testing.T) {
	settings := Settings()
	names := make([]string, 0, len(settings))
	for _, s := range settings {
		names = append(names, s.Name)
		if !strings.HasPrefix(s.CedarType, "{") {
			switch s.CedarType {
			case "Bool", "Long", "Set<String>":
			default:
				t.Errorf("%s: the Cedar type %q is not Bool, Long, Set<String> or a record", s.Name, s.CedarType)
			}
		}
		if s.About == "" {
			t.Errorf("%s: no About", s.Name)
		}
	}
	// The settings of decisions 0044, 0138 and 0140 that a gate reads.
	want := []string{"maxAttempts", "outsideChecksEnforced", "outsideChecksWarned", "reviewers", "usageBudget", "warningsCap"}
	if !slices.Equal(names, want) {
		t.Errorf("Settings() names = %q, want %q", names, want)
	}
}

func TestUsageBudgetHasTheShapeOfUsage(t *testing.T) {
	catalog, err := facts.Catalog()
	if err != nil {
		t.Fatal(err)
	}
	var usage string
	for _, f := range catalog {
		if f.Name == "usage" {
			usage = f.ValueType()
		}
	}
	for _, s := range Settings() {
		if s.Name == "usageBudget" && s.CedarType != usage {
			t.Errorf("usageBudget is %q, want %q, the type of usage", s.CedarType, usage)
		}
	}
}

func TestInvariantsAreEmbedded(t *testing.T) {
	if !strings.HasPrefix(Invariants, "// SPDX-License-Identifier: Apache-2.0\n") {
		t.Errorf("Invariants does not start with the SPDX lines: %.80q", Invariants)
	}
	if !strings.Contains(Invariants, `@id("invariant.verify")`) {
		t.Error("Invariants has no invariant.verify")
	}
}

func TestCoreIsEmbedded(t *testing.T) {
	if !strings.HasPrefix(Core, "// SPDX-License-Identifier: Apache-2.0\n") {
		t.Errorf("Core does not start with the SPDX lines: %.80q", Core)
	}
	if !strings.Contains(Core, `@id("core.escalate")`) {
		t.Error("Core has no core.escalate")
	}
}

func TestDefaultHardCaps(t *testing.T) {
	caps := DefaultHardCaps()
	if caps.Tokens <= 0 || caps.CostMicrodollars <= 0 || caps.Seconds <= 0 {
		t.Errorf("DefaultHardCaps() = %+v, want every cap above zero", caps)
	}
	values := ProjectValues()
	if len(values) != 1 || values[0].Name != "hardCaps" {
		t.Fatalf("ProjectValues() = %+v, want hardCaps only", values)
	}
	catalog, err := facts.Catalog()
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, f := range catalog {
		if f.Name == "usage" {
			found = true
			if values[0].CedarType != f.ValueType() {
				t.Errorf("hardCaps is %q, want %q, the type of usage", values[0].CedarType, f.ValueType())
			}
		}
	}
	if !found {
		t.Error("the catalog has no usage fact to compare hardCaps with")
	}
	want := map[string]int64{"tokens": caps.Tokens, "costMicrodollars": caps.CostMicrodollars, "seconds": caps.Seconds}
	if got := caps.Context(); !maps.Equal(got, want) {
		t.Errorf("DefaultHardCaps().Context() = %v, want %v", got, want)
	}
}

func TestActionsAreTheActionsTheSchemaDeclares(t *testing.T) {
	// The control plane's tests check the same list against the schema as
	// cedar-go reads it; this test shows the names and their order.
	want := []string{
		"advance", "ambiguity", "approve", "ask", "budgets", "cancel", "conformance", "dco", "escalate",
		"merge", "mergeApproval", "outsideChecks", "override", "planReview", "resume", "retry", "scope",
		"spec", "writeVerdict",
	}
	if got := Actions(); !slices.Equal(got, want) {
		t.Errorf("Actions() = %q, want %q", got, want)
	}
}

func TestAlwaysAllowedAreEscalateAndCancel(t *testing.T) {
	// The workflow loader requires the last transition of each trigger to
	// guard with one of these (decisions 0169 and 0177).
	got := AlwaysAllowed()
	if want := []string{"cancel", "escalate"}; !slices.Equal(got, want) {
		t.Errorf("AlwaysAllowed() = %q, want %q", got, want)
	}
	for _, a := range got {
		if !slices.Contains(Actions(), a) {
			t.Errorf("%s is not an action of the schema", a)
		}
	}
	if len(got) > 0 {
		got[0] = "changed"
		if AlwaysAllowed()[0] == "changed" {
			t.Error("AlwaysAllowed returns a list an importer can change")
		}
	}
}
