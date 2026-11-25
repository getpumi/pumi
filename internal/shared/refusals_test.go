// SPDX-License-Identifier: Apache-2.0
// SPDX-FileCopyrightText: The Pumi Authors

package shared

import (
	"testing"

	harnessv1 "getpumi.dev/pumi/api/gen/pumi/harness/v1"
)

func TestRefusalsNamesEachRefusal(t *testing.T) {
	tests := []struct {
		name     string
		refusals []*harnessv1.Refusal
		want     string
	}{
		{"none", nil, ""},
		{"a path edited", []*harnessv1.Refusal{
			{Tool: harnessv1.Capabilities_TOOL_KIND_EDIT, Asked: &harnessv1.Refusal_Path{Path: "README.md"}},
		}, "editing the path README.md"},
		{"one of each", []*harnessv1.Refusal{
			{Tool: harnessv1.Capabilities_TOOL_KIND_READ, Asked: &harnessv1.Refusal_Path{Path: "/etc/passwd"}},
			{Tool: harnessv1.Capabilities_TOOL_KIND_RUN_COMMANDS, Asked: &harnessv1.Refusal_Command{Command: "GOFLAGS=-x go test ./..."}},
			{Tool: harnessv1.Capabilities_TOOL_KIND_WEB, Asked: &harnessv1.Refusal_Host{Host: "example.com"}},
		}, "reading the path /etc/passwd; running the command GOFLAGS=-x go test ./...; using the web at the host example.com"},
		{"nothing named", []*harnessv1.Refusal{{}}, "using a tool of no kind on nothing named"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Refusals(tt.refusals); got != tt.want {
				t.Errorf("Refusals() = %q, want %q", got, tt.want)
			}
		})
	}
}
