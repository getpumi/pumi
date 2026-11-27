// SPDX-License-Identifier: Apache-2.0
// SPDX-FileCopyrightText: The Pumi Authors

package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/known/anypb"

	"getpumi.dev/pumi/api/events"
	cloudeventsv1 "getpumi.dev/pumi/api/gen/io/cloudevents/v1"
	eventsv1 "getpumi.dev/pumi/api/gen/pumi/events/v1"
	"getpumi.dev/pumi/api/policy"
	"getpumi.dev/pumi/internal/controlplane/embeddedlog"
	"getpumi.dev/pumi/internal/kernel/firstworkflow"
	"getpumi.dev/pumi/internal/kernel/workflow"
)

// zeroHash stands for every hash that differs on every run: prevhash and the
// log_head of a decision.
var zeroHash = "sha256:" + strings.Repeat("0", 64)

// hashForm is the shape of such a hash.
var hashForm = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)

// refFields are the data fields, by event type, that hold the id of an
// event of the log.
var refFields = map[string][]protoreflect.Name{
	events.TypeTransitionFired:    {"decision"},
	events.TypeEscalationResolved: {"escalation"},
	events.TypeOverrideRecorded:   {"decision"},
	events.TypeWarningReviewed:    {"warning"},
}

// hashFields are the data fields, by event type, that hold a hash of a line
// of the log.
var hashFields = map[string][]protoreflect.Name{
	events.TypeDecisionMade: {"log_head"},
}

// lineID is the id of the event at line n of a case.
func lineID(n int) string { return fmt.Sprintf("line-%d", n) }

// normalize returns evs with the values that differ on every run
// normalized (decision 0183): each id is line-<n>, each reference to an
// event is the id of that event's line, and each hash is zeroHash. It
// fails on a duplicate id, a reference to an event that is not in evs and
// a hash of the wrong shape.
func normalize(evs []*cloudeventsv1.CloudEvent) ([]*cloudeventsv1.CloudEvent, error) {
	lines := map[string]int{}
	for i, e := range evs {
		if _, dup := lines[e.GetId()]; dup {
			return nil, fmt.Errorf("line %d has the id %q of an earlier line", i+1, e.GetId())
		}
		lines[e.GetId()] = i + 1
	}
	ref := func(at int, id string) (string, error) {
		n, ok := lines[id]
		if !ok {
			return "", fmt.Errorf("line %d names the event %q, which is not in the log", at, id)
		}
		return lineID(n), nil
	}
	out := make([]*cloudeventsv1.CloudEvent, 0, len(evs))
	for i, original := range evs {
		e := proto.CloneOf(original)
		e.Id = lineID(i + 1)
		if err := normalizeAttributes(e, i+1, ref); err != nil {
			return nil, err
		}
		if err := normalizeData(e, i+1, ref); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, nil
}

// normalizeAttributes normalizes the cause and the prevhash of e, the event
// at line at.
func normalizeAttributes(e *cloudeventsv1.CloudEvent, at int, ref func(int, string) (string, error)) error {
	attrs := e.GetAttributes()
	if c, ok := attrs[events.ExtensionCause]; ok {
		id, err := ref(at, c.GetCeString())
		if err != nil {
			return err
		}
		attrs[events.ExtensionCause] = stringAttr(id)
	}
	if h, ok := attrs[events.ExtensionPrevHash]; ok {
		if !hashForm.MatchString(h.GetCeString()) {
			return fmt.Errorf("line %d has the prevhash %q", at, h.GetCeString())
		}
		attrs[events.ExtensionPrevHash] = stringAttr(zeroHash)
	}
	return nil
}

// normalizeData normalizes the references and hashes in the data of e, the
// event at line at.
func normalizeData(e *cloudeventsv1.CloudEvent, at int, ref func(int, string) (string, error)) error {
	refs, hashes := refFields[e.GetType()], hashFields[e.GetType()]
	if len(refs) == 0 && len(hashes) == 0 {
		return nil
	}
	data, err := events.Data(e.GetType())
	if err != nil {
		return err
	}
	if err := e.GetProtoData().UnmarshalTo(data); err != nil {
		return fmt.Errorf("line %d: %w", at, err)
	}
	m := data.ProtoReflect()
	for _, name := range refs {
		if err := setString(m, name, func(id string) (string, error) { return ref(at, id) }); err != nil {
			return err
		}
	}
	for _, name := range hashes {
		err := setString(m, name, func(h string) (string, error) {
			if !hashForm.MatchString(h) {
				return "", fmt.Errorf("line %d has the %s %q", at, name, h)
			}
			return zeroHash, nil
		})
		if err != nil {
			return err
		}
	}
	packed, err := anypb.New(data)
	if err != nil {
		return err
	}
	e.Data = &cloudeventsv1.CloudEvent_ProtoData{ProtoData: packed}
	return nil
}

// setString sets the string field name of m, when it is not empty, to what
// to returns for its value.
func setString(m protoreflect.Message, name protoreflect.Name, to func(string) (string, error)) error {
	f := m.Descriptor().Fields().ByName(name)
	v := m.Get(f).String()
	if v == "" {
		return nil
	}
	n, err := to(v)
	if err != nil {
		return err
	}
	m.Set(f, protoreflect.ValueOfString(n))
	return nil
}

// stringAttr returns s as a string attribute.
func stringAttr(s string) *cloudeventsv1.CloudEvent_CloudEventAttributeValue {
	return &cloudeventsv1.CloudEvent_CloudEventAttributeValue{Attr: &cloudeventsv1.CloudEvent_CloudEventAttributeValue_CeString{CeString: s}}
}

// readFile returns the content of the file name in dir, read through an
// os.Root, which gosec does not report (G304).
func readFile(t *testing.T, dir, name string) []byte {
	t.Helper()
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = root.Close() }()
	b, err := root.ReadFile(name)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// readLines returns the lines of the JSON Lines file name in dir.
func readLines(t *testing.T, dir, name string) [][]byte {
	t.Helper()
	b := readFile(t, dir, name)
	var lines [][]byte
	s := bufio.NewScanner(bytes.NewReader(b))
	s.Buffer(nil, 1<<20)
	for s.Scan() {
		lines = append(lines, slices.Clone(s.Bytes()))
	}
	if err := s.Err(); err != nil {
		t.Fatal(err)
	}
	return lines
}

// readCase returns the events of the case in dir.
func readCase(t *testing.T, dir string) []*cloudeventsv1.CloudEvent {
	t.Helper()
	var out []*cloudeventsv1.CloudEvent
	for i, line := range readLines(t, dir, "log.jsonl") {
		e := &cloudeventsv1.CloudEvent{}
		if err := protojson.Unmarshal(line, e); err != nil {
			t.Fatalf("%s line %d: %v", dir, i+1, err)
		}
		out = append(out, e)
	}
	return out
}

// writeCase writes evs, normalized, as the log.jsonl of the case in dir.
func writeCase(t *testing.T, dir string, evs []*cloudeventsv1.CloudEvent) {
	t.Helper()
	norm, err := normalize(evs)
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	for _, e := range norm {
		b, err := protojson.Marshal(e)
		if err != nil {
			t.Fatal(err)
		}
		// protojson's spacing is not stable, so each line is compacted.
		if err := json.Compact(&buf, b); err != nil {
			t.Fatal(err)
		}
		buf.WriteByte('\n')
	}
	if err := os.WriteFile(filepath.Join(dir, "log.jsonl"), buf.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
}

// compare fails the test unless got, normalized, is want.
func compare(t *testing.T, got, want []*cloudeventsv1.CloudEvent) {
	t.Helper()
	norm, err := normalize(got)
	if err != nil {
		t.Fatalf("the log: %v", err)
	}
	for i := range max(len(norm), len(want)) {
		switch {
		case i >= len(want):
			t.Errorf("line %d is %s, after the last line of the case:\n%s", i+1, norm[i].GetType(), text(norm[i]))
		case i >= len(norm):
			t.Errorf("line %d, %s, is missing from the log:\n%s", i+1, want[i].GetType(), text(want[i]))
		case !proto.Equal(norm[i], want[i]):
			t.Errorf("line %d is\n%s\nwant\n%s", i+1, text(norm[i]), text(want[i]))
		}
	}
}

// text returns e as protojson.
func text(e *cloudeventsv1.CloudEvent) string {
	b, err := protojson.Marshal(e)
	if err != nil {
		return err.Error()
	}
	return string(b)
}

// dataOf returns the data of e as a T.
func dataOf[T proto.Message](t *testing.T, e *cloudeventsv1.CloudEvent, data T) T {
	t.Helper()
	if err := e.GetProtoData().UnmarshalTo(data); err != nil {
		t.Fatalf("the data of %s: %v", e.GetType(), err)
	}
	return data
}

// play plays the case want from the log as it is, with the kernel running
// or not. Each time the kernel waits, or before it starts, it does what the
// next line asks of someone other than the kernel; when the next line is
// the kernel's, it starts the kernel, or restarts pumi when the kernel
// already runs. It stops when the log has as many events as the case.
func (p *pumi) play(want []*cloudeventsv1.CloudEvent, running bool) {
	p.t.Helper()
	restartedAt := -1
	for {
		if running {
			p.waitIdle()
		}
		got := p.events()
		m := len(got)
		if m >= len(want) {
			return
		}
		next := want[m]
		switch {
		case next.GetType() == events.TypeLeaseGranted:
			p.claim()
		case next.GetType() == events.TypeLeaseExpired:
			p.expire()
		case next.GetType() == events.TypeInvocationResult:
			result := dataOf(p.t, next, &eventsv1.InvocationResult{})
			p.report(result, outcomeAfter(p.t, want, m, result.GetInvocation()), leaseNumber(p.t, want, m, result.GetInvocation()),
				reportedAfter(p.t, want, m, got)...)
		case strings.HasPrefix(next.GetSource(), personSource):
			p.appendAsPerson(personEvent(p.t, next, got))
		case permissionOfPerson(want, m) != nil:
			p.appendAsPerson(personEvent(p.t, permissionOfPerson(want, m), got))
		case !running:
			p.startKernel()
			running = true
		case restartedAt == m:
			p.t.Fatalf("after a restart the kernel waits at %d events, but the case expects its %s at line %d", m, next.GetType(), m+1)
		default:
			p.restart()
			restartedAt = m
		}
	}
}

// permissionOfPerson returns the person's event of want whose permission
// the decision.made at line at+1 decides, when it is one: the control
// plane appends that decision just before the event, in the person's
// append (decision 0156). It returns nil for any other line.
func permissionOfPerson(want []*cloudeventsv1.CloudEvent, at int) *cloudeventsv1.CloudEvent {
	if want[at].GetType() != events.TypeDecisionMade || at+1 >= len(want) {
		return nil
	}
	next := want[at+1]
	cause := want[at].GetAttributes()[events.ExtensionCause].GetCeString()
	if cause != next.GetId() || !strings.HasPrefix(next.GetSource(), personSource) {
		return nil
	}
	return next
}

// outcomeAfter returns the outcome of the invocation.finished of
// invocation that follows line at of want.
func outcomeAfter(t *testing.T, want []*cloudeventsv1.CloudEvent, at int, invocation string) eventsv1.InvocationFinished_Outcome {
	t.Helper()
	for _, e := range want[at:] {
		if e.GetType() == events.TypeInvocationFinished {
			if f := dataOf(t, e, &eventsv1.InvocationFinished{}); f.GetInvocation() == invocation {
				return f.GetOutcome()
			}
		}
	}
	t.Fatalf("the case has no invocation.finished of %s after line %d", invocation, at+1)
	return eventsv1.InvocationFinished_OUTCOME_UNSPECIFIED
}

// reportedAfter returns the events that the runner reports with the
// invocation.result at line at of want: the lines after it, up to its
// invocation.finished, each with a new id and its references replaced as
// personEvent replaces them, so that the scripted runner reports them again
// (decision 0246).
func reportedAfter(t *testing.T, want []*cloudeventsv1.CloudEvent, at int, got []*cloudeventsv1.CloudEvent) []*cloudeventsv1.CloudEvent {
	t.Helper()
	var out []*cloudeventsv1.CloudEvent
	for i := at + 1; i < len(want) && want[i].GetType() != events.TypeInvocationFinished; i++ {
		e := personEvent(t, want[i], got)
		e.Id = fmt.Sprintf("reported-event-%d", i+1)
		out = append(out, e)
	}
	return out
}

// leaseNumber returns the number of the last lease of invocation granted
// before line at of want.
func leaseNumber(t *testing.T, want []*cloudeventsv1.CloudEvent, at int, invocation string) int64 {
	t.Helper()
	for i := at - 1; i >= 0; i-- {
		if want[i].GetType() == events.TypeLeaseGranted {
			if g := dataOf(t, want[i], &eventsv1.LeaseGranted{}); g.GetLease() == invocation {
				return g.GetNumber()
			}
		}
	}
	t.Fatalf("the case grants no lease of %s before line %d", invocation, at+1)
	return 0
}

// personEvent returns the event of the case line next for the person to
// append, with a new id and each reference to a line of the case replaced
// by the id of the event at that line of got.
func personEvent(t *testing.T, next *cloudeventsv1.CloudEvent, got []*cloudeventsv1.CloudEvent) *cloudeventsv1.CloudEvent {
	t.Helper()
	e := proto.CloneOf(next)
	e.Id = fmt.Sprintf("person-event-%d", len(got)+1)
	delete(e.Attributes, events.ExtensionPrevHash)
	actual := func(_ int, line string) (string, error) {
		var n int
		if _, err := fmt.Sscanf(line, "line-%d", &n); err != nil || n < 1 || n > len(got) {
			return "", fmt.Errorf("the case line names %q, which is not an earlier line", line)
		}
		return got[n-1].GetId(), nil
	}
	if c, ok := e.GetAttributes()[events.ExtensionCause]; ok {
		id, err := actual(0, c.GetCeString())
		if err != nil {
			t.Fatal(err)
		}
		e.Attributes[events.ExtensionCause] = stringAttr(id)
	}
	if err := normalizeData(e, len(got)+1, actual); err != nil {
		t.Fatal(err)
	}
	return e
}

// A seed step is what the test does when the kernel waits, while it writes
// a case's log.jsonl with -update.
type step func(p *pumi)

// The usage that the scripted runner reports: under the hard caps, and over
// the cost cap.
var (
	underCaps = &eventsv1.InvocationResult_Usage{Tokens: 120_000, CostMicrodollars: 1_500_000, Seconds: 600}
	overCaps  = &eventsv1.InvocationResult_Usage{
		Tokens: 4_000_000, CostMicrodollars: policy.DefaultHardCaps().CostMicrodollars + 1, Seconds: 7_200,
	}
)

// claimStep claims the next invocation.
func claimStep(p *pumi) { p.claim() }

// expireStep lets every current lease expire.
func expireStep(p *pumi) { p.expire() }

// reportStep reports the last claimed invocation as succeeded, with usage.
func reportStep(usage *eventsv1.InvocationResult_Usage) step {
	return func(p *pumi) {
		last := ""
		for inv := range p.claims {
			last = max(last, inv)
		}
		if last == "" {
			p.t.Fatal("the seed reports before any claim")
		}
		reportOf(last, usage)(p)
	}
}

// reportOf reports the claimed invocation as succeeded, with usage.
func reportOf(invocation string, usage *eventsv1.InvocationResult_Usage) step {
	return func(p *pumi) {
		c, ok := p.claims[invocation]
		if !ok {
			p.t.Fatalf("the seed reports %s, which the runner did not claim", invocation)
		}
		result := &eventsv1.InvocationResult{
			Invocation: invocation, Usage: usage, Tree: "tree-1", Output: `{"summary":"Implemented the plan."}`,
		}
		p.report(result, eventsv1.InvocationFinished_OUTCOME_SUCCEEDED, c.GetLeaseNumber())
	}
}

// reportTreeOf reports the claimed invocation as succeeded with the tree
// tree-2 and no output, as the archive step reports (decisions 0278 and
// 0282).
func reportTreeOf(invocation string) step {
	return func(p *pumi) {
		c, ok := p.claims[invocation]
		if !ok {
			p.t.Fatalf("the seed reports %s, which the runner did not claim", invocation)
		}
		p.report(&eventsv1.InvocationResult{Invocation: invocation, Usage: underCaps, Tree: "tree-2"}, eventsv1.InvocationFinished_OUTCOME_SUCCEEDED, c.GetLeaseNumber())
	}
}

// The outputs of the spec check, as the OpenSpec adapter writes them
// (pumi.workflow.v1.SpecCheckOutput): spec.valid for the plan's tree, true
// and false, none, and for another tree.
const (
	specValidOutput     = `{"specValid":{"tree":"tree-0","specAdapterVersion":"adapter-openspec v0.1.0, openspec 2.2.0","value":true},"changeNarrative":"A narrative."}`
	specNotValidOutput  = `{"specValid":{"tree":"tree-0","specAdapterVersion":"adapter-openspec v0.1.0, openspec 2.2.0"},"changeNarrative":"A narrative."}`
	specNoFactOutput    = `{"changeNarrative":"A narrative."}`
	specOtherTreeOutput = `{"specValid":{"tree":"tree-9","specAdapterVersion":"adapter-openspec v0.1.0, openspec 2.2.0","value":true},"changeNarrative":"A narrative."}`
)

// firstWorkflowCases is the directory of goldenDir whose cases run on the
// first workflow.
const firstWorkflowCases = "first"

// reportSpec reports the claimed spec check of the plan, its first
// invocation, as succeeded with output.
func reportSpec(output string) step {
	return func(p *pumi) {
		const invocation = casePlan + "/1"
		c, ok := p.claims[invocation]
		if !ok {
			p.t.Fatalf("the seed reports %s, which the runner did not claim", invocation)
		}
		p.report(&eventsv1.InvocationResult{Invocation: invocation, Usage: underCaps, Output: output}, eventsv1.InvocationFinished_OUTCOME_SUCCEEDED, c.GetLeaseNumber())
	}
}

// addStep adds plan to track as the person, depending on dependsOn
// (decisions 0196 and 0198).
func addStep(plan, track string, dependsOn ...string) step {
	return func(p *pumi) {
		e, err := events.New(&eventsv1.PlanAdded{Plan: plan, Track: track, DependsOn: dependsOn},
			events.Attributes{ID: "add-" + plan, Source: personSource, Subject: plan, Tenant: embeddedlog.Tenant})
		if err != nil {
			p.t.Fatal(err)
		}
		p.appendAsPerson(e)
	}
}

// addPlanA adds the plan of the cases that run one plan.
var addPlanA = addStep(casePlan, caseTrack)

// addTwoTracks adds the plans of two tracks: a1 and a2 in the track a,
// where a2 depends on a1, and b1 in the track b.
var addTwoTracks = []step{addStep("a1", "a"), addStep("a2", "a", "a1"), addStep("b1", "b")}

// answerStep answers the last escalation of the plan as the person.
func answerStep(answer string) step {
	return func(p *pumi) {
		escalation := ""
		for _, e := range p.events() {
			if e.GetType() == events.TypeEscalationRaised {
				escalation = e.GetId()
			}
		}
		if escalation == "" {
			p.t.Fatal("the seed answers before any escalation")
		}
		e, err := events.New(&eventsv1.EscalationResolved{Escalation: escalation, Person: personName, Answer: answer},
			events.Attributes{ID: "answer-1", Source: personSource, Subject: casePlan, Tenant: embeddedlog.Tenant})
		if err != nil {
			p.t.Fatal(err)
		}
		p.appendAsPerson(e)
	}
}

// seeds are the steps that write each case's log.jsonl with -update, by
// the case's directory under goldenDir.
var seeds = map[string][]step{
	"spike/advance-to-final":    {addPlanA, claimStep, reportStep(underCaps)},
	"spike/over-caps-escalates": {addPlanA, claimStep, reportStep(overCaps)},
	// The lease of the agent run expires, and it runs again as a new
	// invocation (decisions 0191 and 0200).
	"spike/in-doubt-runs-again":        {addPlanA, claimStep, expireStep, claimStep, reportStep(underCaps)},
	"spike/answer-over-caps-escalates": {addPlanA, claimStep, reportStep(overCaps), answerStep("Go on.")},
	"entered/decide-on-entry":          {addPlanA, claimStep, reportStep(underCaps)},
	// With parallelism 2, a1 and b1 start at once, and a2 starts when a1
	// has merged.
	"tracks/parallelism-2": slices.Concat(addTwoTracks, []step{
		claimStep, claimStep, reportOf("a1/1", underCaps), claimStep, reportOf("b1/1", underCaps), reportOf("a2/1", underCaps),
	}),
	// With parallelism 1, a1 runs first; when it has merged, a2 and b1 are
	// ready, and a2 starts first because it was added first.
	"tracks/parallelism-1": slices.Concat(addTwoTracks, []step{
		claimStep, reportOf("a1/1", underCaps), claimStep, reportOf("a2/1", underCaps), claimStep, reportOf("b1/1", underCaps),
	}),
	// a1 escalates while b1 goes on and merges; a2 does not start, because
	// a1 has not merged, although a slot is free.
	"tracks/escalation-while-another-goes-on": slices.Concat(addTwoTracks, []step{
		claimStep, claimStep, reportOf("a1/1", overCaps), reportOf("b1/1", underCaps),
	}),
	// The agent run's lease expires; it runs again as plan-a/2, and the
	// plan goes on through the check and the merge.
	"recovery/agent-run-runs-again": {
		addPlanA, claimStep, expireStep, claimStep, reportStep(underCaps), claimStep, reportStep(underCaps),
		claimStep, reportStep(underCaps),
	},
	// The check's lease expires; the runner claims the same invocation
	// again, under a new lease number.
	"recovery/check-runs-again": {
		addPlanA, claimStep, reportStep(underCaps), claimStep, expireStep, claimStep, reportStep(underCaps),
		claimStep, reportStep(underCaps),
	},
	// The merge's lease expires and its adapter answers that it happened:
	// the kernel finishes it, resolved on recovery, and the plan merges.
	"recovery/action-happened": {
		addPlanA, claimStep, reportStep(underCaps), claimStep, reportStep(underCaps), claimStep, expireStep,
	},
	// The merge's lease expires and its adapter answers that it did not
	// happen: it runs again as plan-a/4.
	"recovery/action-did-not-happen": {
		addPlanA, claimStep, reportStep(underCaps), claimStep, reportStep(underCaps), claimStep, expireStep,
		claimStep, reportStep(underCaps),
	},
	// With maxAttempts 1 for in-doubt, the agent run runs again once; the
	// second time it is in doubt, the retry is denied and the plan escalates.
	"recovery/attempts-deny-the-retry": {addPlanA, claimStep, expireStep, claimStep, expireStep},
	// The budget is over the cost cap, so the lost run counts as having
	// spent more than the cap, the retry is denied and the plan escalates.
	"recovery/caps-deny-the-retry": {addPlanA, claimStep, expireStep},
	// The person cancels the plan while its agent run runs: the plan goes
	// to cancelled, the kernel asks the invocation to stop, and the runner
	// reports it cancelled (decision 0192).
	"spike/cancel-while-running": {addPlanA, claimStep, cancelStep(casePlan), reportCancelledOf("plan-a/1")},
	// The runner finishes the agent run before it learns of the cancel and
	// reports it succeeded; the plan stays cancelled.
	"spike/result-after-cancel": {addPlanA, claimStep, cancelStep(casePlan), reportOf("plan-a/1", underCaps)},
	// The person cancels the plan while it waits in its escalated state,
	// with no running invocation.
	"spike/cancel-while-escalated": {addPlanA, claimStep, reportStep(overCaps), cancelStep(casePlan)},
	// The runner stops the agent run at its wall-time limit and reports
	// the outcome cap reached; the plan escalates (decisions 0193 and
	// 0203).
	"spike/wall-time-limit": {addPlanA, claimStep, reportCapReachedOf("plan-a/1")},
	// The model gateway refuses a call of the agent run at its token cap,
	// and the runner reports cap reached with the gateway's totals
	// (decisions 0228 and 0230).
	"spike/token-cap": {addPlanA, claimStep, reportTokenCapOf("plan-a/1")},
	// The runner reports the agent run as failed; with maxAttempts 1 for
	// in-doubt, it runs again as plan-a/2, which succeeds, and the plan goes
	// to done (decision 0235).
	"spike/failed-runs-again": {addPlanA, claimStep, reportFailedOf("plan-a/1"), claimStep, reportOf("plan-a/2", underCaps)},
	// plan-a/2 fails too; the retry has no attempts left, so it is denied,
	// and the plan escalates on failed (decisions 0235 and 0237).
	"spike/failed-attempts-escalate": {addPlanA, claimStep, reportFailedOf("plan-a/1"), claimStep, reportFailedOf("plan-a/2")},
	// The provider's limit stops the agent run on its model role's own
	// adapter; the runner falls back to the role's fallback on another
	// adapter, reports that run's success with the switch, and the plan
	// goes to done. The person then switches the role back (decision 0246).
	"spike/role-falls-back": {addPlanA, claimStep, reportSwitchedOf("plan-a/1", eventsv1.InvocationFinished_OUTCOME_SUCCEEDED), resetStep("implementer")},
	// The limit stops the fallback's run too, so plan-a/1 fails with the
	// switch; it runs again as plan-a/2, on the fallback at once with no
	// second switch, and fails again, and the plan escalates on failed
	// (decisions 0235 and 0246).
	"spike/fallback-limited": {
		addPlanA, claimStep, reportSwitchedOf("plan-a/1", eventsv1.InvocationFinished_OUTCOME_FAILED), claimStep, reportFailedOf("plan-a/2"),
	},
	// With parallelism 1, b1 waits while a1 runs; the person cancels b1,
	// and b1 does not start when a1 has merged.
	// The project archives after implementing: the plan implements, the
	// archive step returns the tree with no output, and the checks run
	// (decisions 0278 and 0296).
	"archive/after-implementing": {
		addPlanA, claimStep, reportOf("plan-a/1", underCaps), claimStep, reportTreeOf("plan-a/2"), claimStep, reportOf("plan-a/3", underCaps),
	},
	// The project archives after the conformance audit: the checks run,
	// the archive step returns the tree, and the checks run again on it.
	"archive/after-conformance": {
		addPlanA, claimStep, reportOf("plan-a/1", underCaps), claimStep, reportOf("plan-a/2", underCaps),
		claimStep, reportTreeOf("plan-a/3"), claimStep, reportOf("plan-a/4", underCaps),
	},
	// The archive step is off: no state archives.
	"archive/off": {addPlanA, claimStep, reportOf("plan-a/1", underCaps), claimStep, reportOf("plan-a/2", underCaps)},
	// The spec adapter reports the change valid for the plan's tree: the
	// spec gate allows, and the plan goes on to planning (decisions 0272,
	// 0277 and 0299).
	"first/spec-valid": {addPlanA, claimStep, reportSpec(specValidOutput)},
	// The spec adapter reports the change not valid: the spec gate denies,
	// and the plan escalates, for a person to fix the spec.
	"first/spec-not-valid": {addPlanA, claimStep, reportSpec(specNotValidOutput)},
	// The output holds the narratives but no spec.valid: the fact cannot be
	// read, so the gate denies (decision 0143, openspec PEX-059).
	"first/spec-fact-missing": {addPlanA, claimStep, reportSpec(specNoFactOutput)},
	// spec.valid is for another tree than the one the check ran on: it is
	// not the fact of the decision's tree, so the gate denies (decision
	// 0300).
	"first/spec-other-tree": {addPlanA, claimStep, reportSpec(specOtherTreeOutput)},
	"tracks/cancel-while-waiting": {
		addStep("a1", "a"), addStep("b1", "b"), cancelStep("b1"), claimStep, reportOf("a1/1", underCaps),
	},
}

// seed runs the steps of a case with the settings s on a new log and
// writes its log.jsonl.
func seed(t *testing.T, wf *workflow.Workflow, s caseSettings, dir string, steps []step) {
	t.Helper()
	p := newPumi(t, wf, s, nil, 0)
	p.startPlane()
	p.startKernel()
	for _, s := range steps {
		p.waitIdle()
		s(p)
	}
	p.waitIdle()
	writeCase(t, dir, p.events())
	p.stop()
}

// TestGoldenCases plays each golden case, and again with a restart at
// every position (decisions 0182 and 0183).
func TestGoldenCases(t *testing.T) {
	dirs, err := filepath.Glob(filepath.Join(goldenDir, "*", "*"))
	if err != nil {
		t.Fatal(err)
	}
	if len(dirs) == 0 {
		t.Fatalf("no golden cases in %s", goldenDir)
	}
	for _, dir := range dirs {
		name := filepath.ToSlash(strings.TrimPrefix(dir, goldenDir+string(filepath.Separator)))
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			runCase(t, dir, name)
		})
	}
}

// loadCaseWorkflow returns the workflow that the cases in the directory
// dir of goldenDir run on: the first workflow, built into the release, for
// first, and otherwise the workflow of the directory of workflowsDir of
// that name.
func loadCaseWorkflow(dir string) (*workflow.Workflow, error) {
	if dir == firstWorkflowCases {
		return firstworkflow.Load()
	}
	return workflow.Load(os.DirFS(filepath.Join(workflowsDir, dir)), "workflow.txtpb")
}

// runCase runs the case name in dir: with -update it writes the case's
// log.jsonl from its seed first, then it plays the case.
func runCase(t *testing.T, dir, name string) {
	t.Helper()
	wf, err := loadCaseWorkflow(filepath.Dir(name))
	if err != nil {
		t.Fatal(err)
	}
	settings := readSettings(t, dir)
	if *update {
		steps, ok := seeds[name]
		if !ok {
			t.Fatalf("the case %s has no seed", name)
		}
		seed(t, wf, settings, dir, steps)
	}
	if len(bytes.TrimSpace(readFile(t, dir, "sources.md"))) == 0 {
		t.Errorf("the case %s has an empty sources.md; it names the case's regime sources (decision 0183)", name)
	}
	playCase(t, wf, settings, readCase(t, dir))
}

// playCase plays want with the settings s, then plays it again with a
// restart at each position where a write of the first run ended.
func playCase(t *testing.T, wf *workflow.Workflow, s caseSettings, want []*cloudeventsv1.CloudEvent) {
	t.Helper()
	first := newPumi(t, wf, s, nil, 0)
	first.startPlane()
	first.startKernel()
	first.play(want, true)
	compare(t, first.events(), want)
	first.stop()
	if t.Failed() {
		return
	}
	logPath, _ := logFiles(first.clone)
	lines := readLines(t, filepath.Dir(logPath), filepath.Base(logPath))
	positions := slices.Compact(slices.Sorted(slices.Values(first.writes)))
	if len(positions) == 0 {
		t.Fatal("the run recorded no writes")
	}
	for _, k := range positions {
		t.Run(fmt.Sprintf("restart at %d", k), func(t *testing.T) {
			t.Parallel()
			p := newPumi(t, wf, s, lines, k)
			p.startPlane()
			p.play(want, false)
			if p.stopKernel == nil {
				p.startKernel()
				p.waitIdle()
			}
			compare(t, p.events(), want)
		})
	}
}
