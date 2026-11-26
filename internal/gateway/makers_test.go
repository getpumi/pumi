// SPDX-License-Identifier: Apache-2.0
// SPDX-FileCopyrightText: The Pumi Authors

package gateway

import (
	"strings"
	"testing"
)

// TestEachModelOfThePriceTableNamesItsMaker: every model of the list-price
// table names the company that trained it, Anthropic for the Claude
// models and OpenAI for the GPT models, and MakerOf returns it, or nothing
// for a model the table does not list (decision 0261).
func TestEachModelOfThePriceTableNamesItsMaker(t *testing.T) {
	for name, m := range prices {
		var want string
		switch {
		case strings.HasPrefix(name, "claude-"):
			want = Anthropic
		case strings.HasPrefix(name, "gpt-"):
			want = OpenAI
		default:
			t.Errorf("the model %s is neither a Claude nor a GPT model; give the test its maker", name)
			continue
		}
		if m.Maker != want {
			t.Errorf("the maker of %s is %q, want %q", name, m.Maker, want)
		}
		if got := MakerOf(name); got != want {
			t.Errorf("MakerOf(%q) = %q, want %q", name, got, want)
		}
	}
	if got := MakerOf("claude-unknown"); got != "" {
		t.Errorf("MakerOf of a model the table does not list = %q, want nothing", got)
	}
}
