// SPDX-License-Identifier: Apache-2.0
// SPDX-FileCopyrightText: The Pumi Authors

package policy

import (
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
