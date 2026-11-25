// SPDX-License-Identifier: Apache-2.0
// SPDX-FileCopyrightText: The Pumi Authors

package kernel

import (
	"context"
	"fmt"
	"strings"

	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/reflect/protoreflect"

	eventsv1 "getpumi.dev/pumi/api/gen/pumi/events/v1"
	workflowv1 "getpumi.dev/pumi/api/gen/pumi/workflow/v1"
)

// Project is the project section of the configuration (decision 0250).
type Project struct {
	// Commands are the commands that run the project's checks.
	Commands []string
	// Conventions are the files that hold the project's conventions, such
	// as AGENTS.md, relative to the root of the repository.
	Conventions []string
	// ProtectedPaths are the project's protected path patterns.
	ProtectedPaths []string
}

// planningOutput is the output message of a planning invocation, whose
// declared paths the project section names.
var planningOutput = (&workflowv1.PlanningOutput{}).ProtoReflect().Descriptor().FullName()

// followDeclared keeps the declared paths of the planning output of res,
// when res is the result of a plan's invocation whose document names the
// planning output (decision 0171). A later planning output replaces them.
func (k *kernel) followDeclared(res *eventsv1.InvocationResult) error {
	r, ok := k.invocations[res.GetInvocation()]
	doc := k.documents[res.GetInvocation()]
	if !ok || doc == nil || protoreflect.FullName(doc.GetOutputMessage()) != planningOutput || res.GetOutput() == "" {
		return nil
	}
	var out workflowv1.PlanningOutput
	if err := protojson.Unmarshal([]byte(res.GetOutput()), &out); err != nil {
		return fmt.Errorf("the planning output of %s does not parse: %w", res.GetInvocation(), err)
	}
	r.declared, r.planned = out.GetDeclaredPaths(), true
	return nil
}

// projectSection returns the project section of the task of an agent run
// of the plan with the input tree tree (decision 0250): the project's
// commands, its convention files, with each that tree does not hold named
// as missing, its protected paths and the plan's declared paths.
func (r *planRun) projectSection(ctx context.Context, tree string) (string, error) {
	p := r.k.cfg.Project
	conventions := make([]string, 0, len(p.Conventions))
	for _, path := range p.Conventions {
		has, err := r.k.cfg.HasFile(ctx, tree, path)
		if err != nil {
			return "", fmt.Errorf("cannot read whether the input tree %s holds the convention file %s: %w", tree, path, err)
		}
		item := "`" + path + "`"
		if !has {
			item += ": missing, the input tree has no such file"
		}
		conventions = append(conventions, item)
	}
	declared := []string{"none yet: the plan has not declared its paths"}
	if r.planned {
		declared = quoted(r.declared)
	}
	var b strings.Builder
	b.WriteString("## The project\n")
	list(&b, "The commands that run the project's checks:", quoted(p.Commands))
	list(&b, "The files that hold the project's conventions:", conventions)
	list(&b, "The protected paths, which no change may touch:", quoted(p.ProtectedPaths))
	list(&b, "The plan's declared paths, the only paths its change may touch:", declared)
	return b.String(), nil
}

// quoted returns each of values as code.
func quoted(values []string) []string {
	out := make([]string, len(values))
	for i, v := range values {
		out[i] = "`" + v + "`"
	}
	return out
}

// list writes a list with its lead-in to b, with the item none when items
// is empty.
func list(b *strings.Builder, lead string, items []string) {
	if len(items) == 0 {
		items = []string{"none"}
	}
	b.WriteString("\n" + lead + "\n\n")
	for _, item := range items {
		b.WriteString("- " + item + "\n")
	}
}
