// SPDX-License-Identifier: Apache-2.0
// SPDX-FileCopyrightText: The Pumi Authors

package kernel

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"strings"

	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"

	configv1 "getpumi.dev/pumi/api/gen/pumi/config/v1"
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
	// ArchiveStep is where the spec adapter archives the plan's change
	// (decision 0278). The plan moves only on the workflow's transitions
	// that hold under it, and ARCHIVE_STEP_UNSPECIFIED counts as after
	// implementing (decision 0296).
	ArchiveStep configv1.Project_ArchiveStep
}

// The output messages whose content the kernel keeps for the tasks: the
// planning output's declared paths and reasons for having no test, the
// spec check's narratives and the coverage report (decisions 0250, 0279,
// 0280 and 0295).
var (
	planningOutput  = (&workflowv1.PlanningOutput{}).ProtoReflect().Descriptor().FullName()
	specCheckOutput = (&workflowv1.SpecCheckOutput{}).ProtoReflect().Descriptor().FullName()
	coverageReport  = (&workflowv1.CoverageReport{}).ProtoReflect().Descriptor().FullName()
)

// followOutput keeps what the tasks need of the output of res, when res is
// the result of a plan's invocation whose document names one of the output
// messages above. The latest output of each kind replaces the one before:
// the declared paths and the reasons for having no test of the planning
// output (decisions 0171 and 0295), the narratives of the spec check,
// which a person's change to a narrative runs again (decision 0279), and
// the coverage report (decision 0294).
func (k *kernel) followOutput(res *eventsv1.InvocationResult) error {
	r, ok := k.invocations[res.GetInvocation()]
	doc := k.documents[res.GetInvocation()]
	if !ok || doc == nil || res.GetOutput() == "" {
		return nil
	}
	parse := func(m proto.Message, what string) error {
		if err := protojson.Unmarshal([]byte(res.GetOutput()), m); err != nil {
			return fmt.Errorf("the %s of %s does not parse: %w", what, res.GetInvocation(), err)
		}
		return nil
	}
	switch protoreflect.FullName(doc.GetOutputMessage()) {
	case planningOutput:
		var out workflowv1.PlanningOutput
		if err := parse(&out, "planning output"); err != nil {
			return err
		}
		r.declared, r.noTest, r.planned = out.GetDeclaredPaths(), out.GetNoTest(), true
	case specCheckOutput:
		var out workflowv1.SpecCheckOutput
		if err := parse(&out, "spec check's output"); err != nil {
			return err
		}
		r.narratives = &out
	case coverageReport:
		var out workflowv1.CoverageReport
		if err := parse(&out, "coverage report"); err != nil {
			return err
		}
		r.coverage = &out
	}
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

// narrativesSection returns the section of the task of an agent run that
// asks for the narratives (decisions 0271, 0279 and 0298): the narrative of
// the plan's change and those of the capabilities it touches, by name,
// from the spec check's last result, each as a quotation.
func (r *planRun) narrativesSection() string {
	var b strings.Builder
	b.WriteString("## The narratives\n\nThe narrative is the final authority when the spec can be read more than one way.\n")
	if r.narratives == nil {
		b.WriteString("\n- none yet: the spec check has not given them\n")
		return b.String()
	}
	quote(&b, "The narrative of the plan's change:", r.narratives.GetChangeNarrative())
	caps := r.narratives.GetCapabilityNarratives()
	for _, name := range slices.Sorted(maps.Keys(caps)) {
		quote(&b, "The narrative of the capability `"+name+"`:", caps[name])
	}
	return b.String()
}

// quote writes text as a quotation with its lead-in to b.
func quote(b *strings.Builder, lead, text string) {
	b.WriteString("\n" + lead + "\n\n")
	text = strings.TrimRight(text, "\n")
	if text == "" {
		text = "none"
	}
	for line := range strings.SplitSeq(text, "\n") {
		b.WriteString(strings.TrimRight("> "+line, " ") + "\n")
	}
}

// coverageSection returns the section of the task of an agent run that
// asks for the coverage report (decisions 0280, 0294, 0295 and 0298): each
// requirement of the last report with its test files and the reason that
// the last planning output gives for having no test, and the reasons for
// IDs that the report does not list.
func (r *planRun) coverageSection() string {
	var b strings.Builder
	b.WriteString("## The coverage report\n")
	if r.coverage == nil {
		b.WriteString("\n- none yet: the coverage report has not run\n")
		return b.String()
	}
	reasons := map[string]string{}
	var order []string
	for _, n := range r.noTest {
		if _, ok := reasons[n.GetRequirementId()]; !ok {
			order = append(order, n.GetRequirementId())
		}
		reasons[n.GetRequirementId()] = strings.Join(strings.Fields(n.GetReason()), " ")
	}
	var items []string
	listed := map[string]bool{}
	for _, req := range r.coverage.GetRequirements() {
		listed[req.GetId()] = true
		reason, has := reasons[req.GetId()]
		item := "`" + req.GetId() + "`: "
		switch {
		case len(req.GetTestFiles()) > 0 && has:
			item += "tested by " + strings.Join(quoted(req.GetTestFiles()), ", ") + "; the plan's reason for having no test: " + reason
		case len(req.GetTestFiles()) > 0:
			item += "tested by " + strings.Join(quoted(req.GetTestFiles()), ", ")
		case has:
			item += "no test; the plan's reason: " + reason
		default:
			item += "no test, and the plan gives no reason"
		}
		items = append(items, item)
	}
	if len(items) == 0 {
		items = []string{"none: the change adds or modifies no requirement"}
	}
	list(&b, "Each requirement that the change adds or modifies, with the test files whose `Verifies:` line names its ID and the plan's reason for having no test. Judge each requirement with neither, and each reason:", items)
	var others []string
	for _, id := range order {
		if !listed[id] {
			others = append(others, "`"+id+"`: "+reasons[id])
		}
	}
	if len(others) > 0 {
		list(&b, "The plan gives a reason for having no test for these IDs, which the change does not add or modify:", others)
	}
	return b.String()
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
