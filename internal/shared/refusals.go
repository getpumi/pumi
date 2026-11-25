// SPDX-License-Identifier: Apache-2.0
// SPDX-FileCopyrightText: The Pumi Authors

package shared

import (
	"strings"

	harnessv1 "getpumi.dev/pumi/api/gen/pumi/harness/v1"
)

// toolPhrases name what a kind of tool did when it asked.
var toolPhrases = map[harnessv1.Capabilities_ToolKind]string{
	harnessv1.Capabilities_TOOL_KIND_READ:         "reading",
	harnessv1.Capabilities_TOOL_KIND_EDIT:         "editing",
	harnessv1.Capabilities_TOOL_KIND_RUN_COMMANDS: "running",
	harnessv1.Capabilities_TOOL_KIND_WEB:          "using the web at",
}

// Refusals names each of the refusals that a harness reported, in order and
// separated by "; ", such as "editing the path README.md; running the
// command rm -rf /", for the log and the question of an escalation
// (decision 0249). It returns "" for none.
func Refusals(refusals []*harnessv1.Refusal) string {
	names := make([]string, 0, len(refusals))
	for _, r := range refusals {
		tool, ok := toolPhrases[r.GetTool()]
		if !ok {
			tool = "using a tool of no kind on"
		}
		asked := "nothing named"
		switch a := r.GetAsked().(type) {
		case *harnessv1.Refusal_Path:
			asked = "the path " + a.Path
		case *harnessv1.Refusal_Command:
			asked = "the command " + a.Command
		case *harnessv1.Refusal_Host:
			asked = "the host " + a.Host
		}
		names = append(names, tool+" "+asked)
	}
	return strings.Join(names, "; ")
}
