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
		case next.GetType() == events.TypeInvocationResult:
			result := dataOf(p.t, next, &eventsv1.InvocationResult{})
			p.report(result, outcomeAfter(p.t, want, m, result.GetInvocation()), leaseNumber(p.t, want, m, result.GetInvocation()))
		case strings.HasPrefix(next.GetSource(), personSource):
			p.appendAsPerson(personEvent(p.t, next, got))
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

// restartStep stops pumi and starts it again.
func restartStep(p *pumi) { p.restart() }

// reportStep reports the last claimed invocation as succeeded, with usage.
func reportStep(usage *eventsv1.InvocationResult_Usage) step {
	return func(p *pumi) {
		var last *eventsv1.InvocationResult
		var number int64
		for inv, c := range p.claims {
			if last == nil || inv > last.GetInvocation() {
				last, number = &eventsv1.InvocationResult{Invocation: inv}, c.GetLeaseNumber()
			}
		}
		if last == nil {
			p.t.Fatal("the seed reports before any claim")
		}
		last.Usage = usage
		last.Tree = "tree-1"
		last.Output = `{"summary":"Implemented the plan."}`
		p.report(last, eventsv1.InvocationFinished_OUTCOME_SUCCEEDED, number)
	}
}

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
	"spike/advance-to-final":           {claimStep, reportStep(underCaps)},
	"spike/over-caps-escalates":        {claimStep, reportStep(overCaps)},
	"spike/in-doubt-escalates":         {restartStep},
	"spike/answer-over-caps-escalates": {claimStep, reportStep(overCaps), answerStep("Go on.")},
	"entered/decide-on-entry":          {claimStep, reportStep(underCaps)},
}

// seed runs the steps of a case on a new log and writes its log.jsonl.
func seed(t *testing.T, wf *workflow.Workflow, dir string, steps []step) {
	t.Helper()
	p := newPumi(t, wf, nil, 0)
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

// runCase runs the case name in dir: with -update it writes the case's
// log.jsonl from its seed first, then it plays the case.
func runCase(t *testing.T, dir, name string) {
	t.Helper()
	wf, err := workflow.Load(os.DirFS(filepath.Join(workflowsDir, filepath.Dir(name))), "workflow.txtpb")
	if err != nil {
		t.Fatal(err)
	}
	if *update {
		steps, ok := seeds[name]
		if !ok {
			t.Fatalf("the case %s has no seed", name)
		}
		seed(t, wf, dir, steps)
	}
	if len(bytes.TrimSpace(readFile(t, dir, "sources.md"))) == 0 {
		t.Errorf("the case %s has an empty sources.md; it names the case's regime sources (decision 0183)", name)
	}
	playCase(t, wf, readCase(t, dir))
}

// playCase plays want, then plays it again with a restart at each position
// where a write of the first run ended.
func playCase(t *testing.T, wf *workflow.Workflow, want []*cloudeventsv1.CloudEvent) {
	t.Helper()
	first := newPumi(t, wf, nil, 0)
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
			p := newPumi(t, wf, lines, k)
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
