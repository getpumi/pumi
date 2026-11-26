// SPDX-License-Identifier: Apache-2.0
// SPDX-FileCopyrightText: The Pumi Authors

package api

import (
	"slices"
	"strings"
	"testing"

	workflowv1 "getpumi.dev/pumi/api/gen/pumi/workflow/v1"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/encoding/prototext"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
)

// oneofOf returns the oneof name of md, failing the test when there is none.
func oneofOf(t *testing.T, md protoreflect.MessageDescriptor, name string) protoreflect.OneofDescriptor {
	t.Helper()
	o := md.Oneofs().ByName(protoreflect.Name(name))
	if o == nil {
		t.Fatalf("%s has no oneof %s", md.FullName(), name)
	}
	return o
}

// oneofFieldNames returns the sorted names of the fields of o.
func oneofFieldNames(o protoreflect.OneofDescriptor) []string {
	names := []string{}
	for i := range o.Fields().Len() {
		names = append(names, string(o.Fields().Get(i).Name()))
	}
	slices.Sort(names)
	return names
}

func TestWorkflowHasANameAVersionAnInitialStateAndStates(t *testing.T) {
	wf := (&workflowv1.Workflow{}).ProtoReflect().Descriptor()
	if got, want := string(wf.FullName()), "pumi.workflow.v1.Workflow"; got != want {
		t.Errorf("the workflow message is %s, want %s (decision 0167)", got, want)
	}
	if got, want := fieldNames(wf), []string{"initial", "name", "states", "version"}; !slices.Equal(got, want) {
		t.Errorf("Workflow has %q, want %q (decision 0168)", got, want)
	}
	if states := fieldOf(t, wf, "states"); !states.IsList() || states.Message().FullName() != "pumi.workflow.v1.State" {
		t.Errorf("Workflow.states is not a list of State")
	}
	for _, name := range []string{"name", "version", "initial"} {
		if f := fieldOf(t, wf, name); f.Kind() != protoreflect.StringKind || f.IsList() {
			t.Errorf("Workflow.%s is not a string", name)
		}
	}
}

func TestAStateHasANameAParentAGateAnEntryAndOrderedTransitions(t *testing.T) {
	st := (&workflowv1.State{}).ProtoReflect().Descriptor()
	want := []string{"final", "gate", "invoke", "name", "parent", "transitions", "wait"}
	if got := fieldNames(st); !slices.Equal(got, want) {
		t.Errorf("State has %q, want %q (decision 0168)", got, want)
	}
	for _, name := range []string{"name", "parent", "gate"} {
		if f := fieldOf(t, st, name); f.Kind() != protoreflect.StringKind || f.IsList() {
			t.Errorf("State.%s is not a string", name)
		}
	}
	entry := oneofOf(t, st, "entry")
	if got, want := oneofFieldNames(entry), []string{"final", "invoke", "wait"}; !slices.Equal(got, want) || entry.IsSynthetic() {
		t.Errorf("State's entry is one of %q, want exactly one of %q (decision 0168)", got, want)
	}
	if tr := fieldOf(t, st, "transitions"); !tr.IsList() || tr.Message().FullName() != "pumi.workflow.v1.Transition" {
		t.Errorf("State.transitions is not a list of Transition, which keeps the file's order (decision 0169)")
	}
}

func TestAnInvokeEntryNamesTheRoleTheKindTheTaskTheOutputMessageTheModelRoleTheComponentReturnsTreeAndItsInputs(t *testing.T) {
	inv := (&workflowv1.Invoke{}).ProtoReflect().Descriptor()
	if got, want := fieldNames(inv), []string{"component", "coverage", "kind", "model_role", "narratives", "output_message", "returns_tree", "role", "task"}; !slices.Equal(got, want) {
		t.Fatalf("Invoke has %q, want %q (decisions 0168, 0233, 0272, 0282 and 0298)", got, want)
	}
	for _, name := range []string{"narratives", "coverage"} {
		if f := fieldOf(t, inv, name); f.Kind() != protoreflect.BoolKind || f.IsList() {
			t.Errorf("Invoke.%s is not a bool", name)
		}
	}
	if got, want := valueNames(fieldOf(t, inv, "role").Enum()), []string{"ROLE_UNSPECIFIED", "ROLE_AGENT", "ROLE_GATE"}; !slices.Equal(got, want) {
		t.Errorf("Invoke.Role is %q, want %q", got, want)
	}
	if got, want := valueNames(fieldOf(t, inv, "kind").Enum()), []string{"KIND_UNSPECIFIED", "KIND_HARNESS", "KIND_CHECK", "KIND_EXTERNAL_ACTION"}; !slices.Equal(got, want) {
		t.Errorf("Invoke.Kind is %q, want %q", got, want)
	}
	for _, name := range []string{"task", "output_message"} {
		if f := fieldOf(t, inv, name); f.Kind() != protoreflect.StringKind || f.IsList() {
			t.Errorf("Invoke.%s is not a string", name)
		}
	}
}

func TestWaitCarriesNothing(t *testing.T) {
	if md := (&workflowv1.Wait{}).ProtoReflect().Descriptor(); md.Fields().Len() != 0 {
		t.Errorf("%s has %d fields, want none", md.FullName(), md.Fields().Len())
	}
}

// TestFinalCarriesMerged: a final state says only whether the plan has
// merged in it (decision 0197).
func TestFinalCarriesMerged(t *testing.T) {
	md := (&workflowv1.Final{}).ProtoReflect().Descriptor()
	if got, want := fieldNames(md), []string{"merged"}; !slices.Equal(got, want) {
		t.Fatalf("Final has %q, want %q", got, want)
	}
	if f := fieldOf(t, md, "merged"); f.Kind() != protoreflect.BoolKind || f.IsList() {
		t.Errorf("Final.merged is not a bool")
	}
}

func TestATransitionHasATriggerATargetAnActionAndTheArchiveStepsItHoldsUnder(t *testing.T) {
	tr := (&workflowv1.Transition{}).ProtoReflect().Descriptor()
	if got, want := fieldNames(tr), []string{"action", "archive_steps", "target", "trigger"}; !slices.Equal(got, want) {
		t.Fatalf("Transition has %q, want %q (decisions 0168 and 0296)", got, want)
	}
	if f := fieldOf(t, tr, "archive_steps"); !f.IsList() || f.Enum() == nil || f.Enum().FullName() != "pumi.config.v1.Project.ArchiveStep" {
		t.Errorf("Transition.archive_steps is not a list of the project's archive steps (decision 0296)")
	}
	if f := fieldOf(t, tr, "trigger"); f.Message() == nil || f.Message().FullName() != "pumi.workflow.v1.Trigger" {
		t.Errorf("Transition.trigger is not a Trigger")
	}
	for _, name := range []string{"target", "action"} {
		if f := fieldOf(t, tr, name); f.Kind() != protoreflect.StringKind || f.IsList() {
			t.Errorf("Transition.%s is not a string", name)
		}
	}
}

func TestATriggerIsExactlyOneOfResultEnteredInDoubtCapReachedFailedOrAnEventType(t *testing.T) {
	trig := (&workflowv1.Trigger{}).ProtoReflect().Descriptor()
	kind := oneofOf(t, trig, "kind")
	if got, want := oneofFieldNames(kind), []string{"cap_reached", "entered", "event", "failed", "in_doubt", "result"}; !slices.Equal(got, want) || kind.IsSynthetic() {
		t.Fatalf("Trigger is one of %q, want exactly one of %q (decisions 0170, 0200, 0203 and 0235)", got, want)
	}
	if got, want := fieldNames(trig), oneofFieldNames(kind); !slices.Equal(got, want) {
		t.Errorf("Trigger has %q outside its oneof", got)
	}
	if f := fieldOf(t, trig, "event"); f.Kind() != protoreflect.StringKind {
		t.Errorf("Trigger.event is not a string")
	}
	for _, name := range []string{"result", "entered", "in_doubt", "cap_reached", "failed"} {
		if f := fieldOf(t, trig, name); f.Message() == nil || f.Message().Fields().Len() != 0 {
			t.Errorf("Trigger.%s is not a message with no fields", name)
		}
	}
}

// sampleWorkflow is a small workflow in protobuf text format, with comments,
// as the workflow file is written (decision 0167).
const sampleWorkflow = `
# A sample.
name: "sample"
version: "1"
initial: "planning"
states {
  name: "active"
}
states {
  name: "planning"
  parent: "active"  # Inside the superstate.
  invoke {
    role: ROLE_AGENT
    kind: KIND_HARNESS
    task: "plan"
    output_message: "pumi.workflow.v1.PlanningOutput"
  }
  transitions {
    trigger { result {} }
    target: "plan review"
    action: "advance"
  }
}
states {
  name: "plan review"
  parent: "active"
  gate: "planReview"
  wait {}
  transitions {
    trigger { entered {} }
    target: "merged"
    action: "planReview"
  }
  transitions {
    trigger { event: "dev.getpumi.approval.recorded" }
    target: "merged"
    action: "mergeApproval"
  }
}
states {
  name: "merged"
  final {}
}
`

func TestTheSampleWorkflowParsesFromTextFormat(t *testing.T) {
	var wf workflowv1.Workflow
	if err := prototext.Unmarshal([]byte(sampleWorkflow), &wf); err != nil {
		t.Fatal(err)
	}
	if got := len(wf.GetStates()); got != 4 {
		t.Fatalf("the sample has %d states, want 4", got)
	}
	planning := wf.GetStates()[1]
	if planning.GetInvoke().GetOutputMessage() != "pumi.workflow.v1.PlanningOutput" || planning.GetParent() != "active" {
		t.Errorf("planning parsed as %v", planning)
	}
	review := wf.GetStates()[2]
	if review.GetWait() == nil || review.GetGate() != "planReview" {
		t.Errorf("plan review parsed as %v", review)
	}
	triggers := []string{}
	for _, tr := range review.GetTransitions() {
		switch k := tr.GetTrigger().GetKind().(type) {
		case *workflowv1.Trigger_Entered:
			triggers = append(triggers, "entered")
		case *workflowv1.Trigger_Event:
			triggers = append(triggers, k.Event)
		default:
			triggers = append(triggers, "other")
		}
	}
	if want := []string{"entered", "dev.getpumi.approval.recorded"}; !slices.Equal(triggers, want) {
		t.Errorf("plan review's triggers are %q, want %q in the file's order", triggers, want)
	}
	if wf.GetStates()[3].GetFinal() == nil {
		t.Errorf("merged is not final")
	}
}

func TestTextFormatRefusesTwoEntriesOrTwoTriggerKinds(t *testing.T) {
	for name, text := range map[string]string{
		"entry":   `states { name: "s" wait {} final {} }`,
		"trigger": `states { name: "s" wait {} transitions { trigger { result {} entered {} } target: "s" action: "advance" } }`,
	} {
		var wf workflowv1.Workflow
		err := prototext.Unmarshal([]byte(text), &wf)
		if err == nil || !strings.Contains(err.Error(), "oneof") {
			t.Errorf("two kinds of %s: got error %v, want one about the oneof", name, err)
		}
	}
}

func TestThePlanningOutputHasDeclaredPathsAndOpenQuestionsWithAnswers(t *testing.T) {
	out := (&workflowv1.PlanningOutput{}).ProtoReflect().Descriptor()
	if got, want := fieldNames(out), []string{"declared_paths", "no_test", "plan", "questions"}; !slices.Equal(got, want) {
		t.Fatalf("PlanningOutput has %q, want %q (decisions 0171 and 0295)", got, want)
	}
	n := fieldOf(t, out, "no_test")
	if !n.IsList() || n.Message() == nil {
		t.Fatalf("PlanningOutput.no_test is not a list of messages")
	}
	if got, want := fieldNames(n.Message()), []string{"reason", "requirement_id"}; !slices.Equal(got, want) {
		t.Errorf("a requirement with no test has %q, want %q: the requirement's ID and the reason (decision 0295)", got, want)
	}
	if f := fieldOf(t, out, "declared_paths"); !f.IsList() || f.Kind() != protoreflect.StringKind {
		t.Errorf("PlanningOutput.declared_paths is not a list of strings")
	}
	q := fieldOf(t, out, "questions")
	if !q.IsList() || q.Message() == nil {
		t.Fatalf("PlanningOutput.questions is not a list of messages")
	}
	if got, want := fieldNames(q.Message()), []string{"agent_answer", "id", "text"}; !slices.Equal(got, want) {
		t.Errorf("a question has %q, want %q: each with the agent's own answer (decision 0171)", got, want)
	}
}

func TestTheSecondOpinionAnswersEachQuestion(t *testing.T) {
	out := (&workflowv1.SecondOpinion{}).ProtoReflect().Descriptor()
	if got, want := fieldNames(out), []string{"answers"}; !slices.Equal(got, want) {
		t.Fatalf("SecondOpinion has %q, want %q (decision 0171)", got, want)
	}
	a := fieldOf(t, out, "answers")
	if !a.IsList() || a.Message() == nil {
		t.Fatalf("SecondOpinion.answers is not a list of messages")
	}
	if got, want := fieldNames(a.Message()), []string{"answer", "question_id"}; !slices.Equal(got, want) {
		t.Errorf("an answer has %q, want %q", got, want)
	}
}

func TestTheOutputMessagesRoundTripThroughProtojson(t *testing.T) {
	for _, m := range []proto.Message{
		&workflowv1.PlanningOutput{
			Plan:          "Add the loader.",
			DeclaredPaths: []string{"internal/kernel/workflow/**"},
			Questions:     []*workflowv1.PlanningOutput_Question{{Id: "q1", Text: "Which order?", AgentAnswer: "File order."}},
			NoTest:        []*workflowv1.PlanningOutput_NoTest{{RequirementId: "WFL-001", Reason: "The golden cases show it."}},
		},
		&workflowv1.SecondOpinion{Answers: []*workflowv1.SecondOpinion_Answer{{QuestionId: "q1", Answer: "File order."}}},
	} {
		roundTrip(t, m)
	}
}

// roundTrip writes m as protojson, reads it back into a new message of its
// type, and fails the test when the two differ, as the runner parses an
// invocation's output (decision 0160).
func roundTrip(t *testing.T, m proto.Message) {
	t.Helper()
	data, err := protojson.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	back := m.ProtoReflect().New().Interface()
	if err := (protojson.UnmarshalOptions{}).Unmarshal(data, back); err != nil {
		t.Fatalf("%s: %v", m.ProtoReflect().Descriptor().FullName(), err)
	}
	if !proto.Equal(m, back) {
		t.Errorf("%s did not round-trip: %s", m.ProtoReflect().Descriptor().FullName(), data)
	}
}
