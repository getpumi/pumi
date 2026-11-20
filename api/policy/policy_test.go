// SPDX-License-Identifier: Apache-2.0
// SPDX-FileCopyrightText: The Pumi Authors

package policy

import (
	"slices"
	"strings"
	"testing"
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
		switch s.CedarType {
		case "Bool", "Long", "Set<String>", "Record":
		default:
			t.Errorf("%s: the Cedar type %q is not Bool, Long, Set<String> or Record", s.Name, s.CedarType)
		}
		if s.About == "" {
			t.Errorf("%s: no About", s.Name)
		}
	}
	// The settings of decisions 0044, 0138 and 0140 that a gate reads.
	want := []string{"maxAttempts", "requiredOutsideChecks", "reviewers", "usageBudget", "warningsCap"}
	if !slices.Equal(names, want) {
		t.Errorf("Settings() names = %q, want %q", names, want)
	}
}
