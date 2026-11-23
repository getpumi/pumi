// SPDX-License-Identifier: Apache-2.0
// SPDX-FileCopyrightText: The Pumi Authors

package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"google.golang.org/protobuf/encoding/protojson"

	"getpumi.dev/pumi/api/events"
	cloudeventsv1 "getpumi.dev/pumi/api/gen/io/cloudevents/v1"
	eventsv1 "getpumi.dev/pumi/api/gen/pumi/events/v1"
	harnessv1 "getpumi.dev/pumi/api/gen/pumi/harness/v1"
	"getpumi.dev/pumi/api/policy"
)

// The tests of the pumi commands run them in this process, on a temporary
// clone with its own embedded log, the clone's git user.email as the person
// and the test binary as the harness adapter (getpumi/pumi#88).

// testAdapterMode, when set, makes the test binary act as a harness adapter
// in that mode, which the runner of pumi run starts for each invocation.
const testAdapterMode = "PUMI_TEST_ADAPTER"

// testAdapterPrefix starts each line the test adapter logs.
const testAdapterPrefix = "test adapter: "

// The test adapter's modes.
const (
	// adapterSucceeds completes with usage under the hard caps.
	adapterSucceeds = "succeeds"
	// adapterOverCaps completes with a cost over the hard caps.
	adapterOverCaps = "over caps"
	// adapterWaits runs until the runner closes its standard input.
	adapterWaits = "waits"
	// adapterReportsAFile writes outputFileContent to outputFilePath and
	// reports it as an output file.
	adapterReportsAFile = "reports a file"
)

// The output file of the mode adapterReportsAFile, in a directory that the
// test clone's .gitignore ignores, so that the resulting tree does not
// change a file outside the allowed paths.
const (
	outputFilePath    = "out/report.txt"
	outputFileContent = "the report\n"
)

// testPerson is the git user.email of the test clones.
const testPerson = "person@example.com"

// commandWait is how long a test waits for pumi run to reach a state.
const commandWait = 30 * time.Second

func TestMain(m *testing.M) {
	if mode, ok := os.LookupEnv(testAdapterMode); ok {
		os.Exit(testAdapter(mode))
	}
	os.Exit(m.Run())
}

// testAdapter acts as a harness adapter in mode and returns its exit
// status: it reads the harness invocation from standard input, logs a line
// to standard error and does what mode says (decision 0158).
func testAdapter(mode string) int {
	in := bufio.NewReader(os.Stdin)
	line, err := in.ReadString('\n')
	if err != nil {
		fmt.Fprintln(os.Stderr, testAdapterPrefix+"no invocation:", err)
		return 9
	}
	inv := &harnessv1.Invocation{}
	if err := protojson.Unmarshal([]byte(line), inv); err != nil {
		fmt.Fprintln(os.Stderr, testAdapterPrefix+"the invocation does not parse:", err)
		return 9
	}
	fmt.Fprintf(os.Stderr, "%sruns %s on the tree %s\n", testAdapterPrefix, inv.GetInvocation(), inv.GetInput().GetTree())
	usage := &harnessv1.Usage{Tokens: 10, CostMicrodollars: 20, Seconds: 1}
	var files []string
	switch mode {
	case adapterSucceeds:
	case adapterReportsAFile:
		if err := os.MkdirAll(filepath.Dir(outputFilePath), 0o750); err != nil {
			panic(err)
		}
		if err := os.WriteFile(outputFilePath, []byte(outputFileContent), 0o600); err != nil {
			panic(err)
		}
		files = []string{outputFilePath}
	case adapterOverCaps:
		usage.CostMicrodollars = policy.DefaultHardCaps().CostMicrodollars + 1
	case adapterWaits:
		_, _ = in.ReadString('\n')
		fmt.Fprintln(os.Stderr, testAdapterPrefix+"standard input closed")
		return 0
	default:
		fmt.Fprintln(os.Stderr, testAdapterPrefix+"no mode", mode)
		return 9
	}
	out, err := protojson.Marshal(&harnessv1.Result{
		Status: harnessv1.Result_STATUS_COMPLETED, Output: `{"summary":"Implemented the plan."}`, Usage: usage, OutputFiles: files,
	})
	if err != nil {
		panic(err)
	}
	fmt.Println(string(out))
	return 0
}

// syncBuffer is a bytes.Buffer that is safe for concurrent use.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// commandClone is a temporary clone with a configuration file of pumi.
type commandClone struct {
	t      *testing.T
	dir    string
	config string
}

// newCommandClone returns a clone whose git user.email is testPerson, with
// a configuration file for the workflow of internal/kernel/testdata named
// workflow, and makes the test binary the adapter in mode. git reads no
// configuration of the user or the system, so the clone's is the only one.
func newCommandClone(t *testing.T, workflow, mode string) *commandClone {
	t.Helper()
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv(testAdapterMode, mode)
	dir := newClone(t)
	git(t, dir, exec.CommandContext(t.Context(), "git", "config", "user.email", testPerson))
	wf, err := filepath.Abs(filepath.Join(workflowsDir, workflow, "workflow.txtpb"))
	if err != nil {
		t.Fatal(err)
	}
	config := filepath.Join(t.TempDir(), "pumi.txtpb")
	writeFile(t, filepath.Dir(config), filepath.Base(config), fmt.Sprintf(`# The configuration of the test clone.
workflow: %q
adapter: "/proc/self/exe"
trunk: "main"
base {
  component { name: "test-adapter" version: "1" }
  input { repo: "case" }
  budget { seconds: 600 cost_microdollars: 5000000 }
}
project { name: "case" }
settings {
  parallelism: 1
  max_attempts { key: "in-doubt" value: 1 }
}
`, wf))
	return &commandClone{t: t, dir: dir, config: config}
}

// pumi runs the pumi command with args in the clone and returns its
// standard output.
func (c *commandClone) pumi(args ...string) (string, error) {
	c.t.Helper()
	var out bytes.Buffer
	err := execute(c.t.Context(), c.dir, args, &out, &out)
	return out.String(), err
}

// must runs the pumi command with args and fails the test when it fails.
func (c *commandClone) must(args ...string) string {
	c.t.Helper()
	out, err := c.pumi(args...)
	if err != nil {
		c.t.Fatalf("pumi %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return out
}

// running is a pumi run in the background.
type running struct {
	out    *syncBuffer
	cancel context.CancelFunc
	done   chan error
}

// start starts pumi run with the clone's configuration file, and waits
// until it serves the socket: until pumi status, which is given no
// configuration file, works.
func (c *commandClone) start() *running {
	c.t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	r := &running{out: &syncBuffer{}, cancel: cancel, done: make(chan error, 1)}
	go func() { r.done <- execute(ctx, c.dir, []string{"run", "--config", c.config}, r.out, r.out) }()
	c.t.Cleanup(func() { r.stop(c.t) })
	deadline := time.Now().Add(commandWait)
	for {
		if _, err := c.pumi("status"); err == nil {
			return r
		}
		select {
		case err := <-r.done:
			r.done <- err
			c.t.Fatalf("pumi run stopped with %v before it served the socket; its output:\n%s", err, r.out.String())
		default:
		}
		if time.Now().After(deadline) {
			c.t.Fatalf("pumi run did not serve the socket within %s; its output:\n%s", commandWait, r.out.String())
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// stop stops pumi run, as Ctrl-C does, and fails the test unless it
// stopped without an error.
func (r *running) stop(t *testing.T) {
	t.Helper()
	if r.cancel == nil {
		return
	}
	r.cancel()
	r.cancel = nil
	if err := <-r.done; err != nil {
		t.Errorf("pumi run stopped with %v; its output:\n%s", err, r.out.String())
	}
}

// statusRow is one plan's line of pumi status.
type statusRow struct {
	track, state, escalation string
}

// status runs pumi status with args and returns its rows by plan.
func (c *commandClone) status(args ...string) map[string]statusRow {
	c.t.Helper()
	out := c.must(append([]string{"status"}, args...)...)
	lines := strings.Split(strings.TrimSuffix(out, "\n"), "\n")
	if len(lines) == 0 || !strings.HasPrefix(lines[0], "PLAN") {
		c.t.Fatalf("pumi status wrote no header:\n%s", out)
	}
	rows := map[string]statusRow{}
	for _, line := range lines[1:] {
		f := strings.Fields(line)
		if len(f) < 3 {
			c.t.Fatalf("pumi status wrote the line %q, want a plan, a track and a state", line)
		}
		rows[f[0]] = statusRow{track: f[1], state: f[2], escalation: strings.Join(f[3:], " ")}
	}
	return rows
}

// waitState waits until pumi status shows plan in state.
func (c *commandClone) waitState(r *running, plan, state string) statusRow {
	c.t.Helper()
	deadline := time.Now().Add(commandWait)
	for {
		row := c.status("--config", c.config)[plan]
		if row.state == state {
			return row
		}
		if time.Now().After(deadline) {
			c.t.Fatalf("pumi status shows %s in %q after %s, want %q; the output of pumi run:\n%s",
				plan, row.state, commandWait, state, r.out.String())
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// log returns the events of the clone's log, read from its file.
func (c *commandClone) log() []*cloudeventsv1.CloudEvent {
	c.t.Helper()
	return readLog(c.t, shownRun{Repository: c.dir})
}

// ofType returns the events of evs of type typ.
func ofType(evs []*cloudeventsv1.CloudEvent, typ string) []*cloudeventsv1.CloudEvent {
	var out []*cloudeventsv1.CloudEvent
	for _, e := range evs {
		if e.GetType() == typ {
			out = append(out, e)
		}
	}
	return out
}

// waitEvent waits until the clone's log has an event of type typ, and
// returns the last one.
func (c *commandClone) waitEvent(r *running, typ string) *cloudeventsv1.CloudEvent {
	c.t.Helper()
	deadline := time.Now().Add(commandWait)
	for {
		if evs := ofType(c.log(), typ); len(evs) > 0 {
			return evs[len(evs)-1]
		}
		if time.Now().After(deadline) {
			c.t.Fatalf("the log has no %s after %s; the output of pumi run:\n%s", typ, commandWait, r.out.String())
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func TestVersionWritesNameAndVersion(t *testing.T) {
	var out bytes.Buffer
	if err := execute(t.Context(), t.TempDir(), []string{"version"}, &out, &out); err != nil {
		t.Fatalf("pumi version: %v", err)
	}
	if got := out.String(); !strings.HasPrefix(got, "pumi ") || !strings.HasSuffix(got, "\n") {
		t.Errorf("pumi version wrote %q, want a line starting with %q", got, "pumi ")
	}
}

func TestAnUnknownCommandFails(t *testing.T) {
	var out bytes.Buffer
	if err := execute(t.Context(), t.TempDir(), []string{"frobnicate"}, &out, &out); err == nil {
		t.Errorf("pumi frobnicate succeeded, want an error; it wrote\n%s", out.String())
	}
}

// TestRunRunsThePlansAndPrintsTheRunnersOutput: pumi run wires the kernel,
// the embedded control plane and the in-process runner on the clone's log;
// pumi add, through its socket, adds two plans of a track, and both run to
// merged from the trunk's tree, printing the adapter's lines. After the run
// stops, pumi status opens the log itself with the configuration file.
func TestRunRunsThePlansAndPrintsTheRunnersOutput(t *testing.T) {
	c := newCommandClone(t, "tracks", adapterSucceeds)
	trunk := gitOutput(t, c.dir, exec.CommandContext(t.Context(), "git", "rev-parse", "main^{tree}"))
	r := c.start()
	if out := c.must("add", "--track", "a", "a1"); !strings.Contains(out, "a1") {
		t.Errorf("pumi add wrote %q, want a line that names the plan", out)
	}
	c.must("add", "--track", "a", "--depends-on", "a1", "a2")
	c.waitState(r, "a1", "merged")
	c.waitState(r, "a2", "merged")
	for _, inv := range []string{"a1/1", "a2/1"} {
		if want := inv + ": " + testAdapterPrefix + "runs " + inv + " on the tree " + trunk; !strings.Contains(r.out.String(), want) {
			t.Errorf("the output of pumi run has no line %q; it is\n%s", want, r.out.String())
		}
	}
	r.stop(t)
	rows := c.status("--config", c.config)
	if rows["a1"] != (statusRow{track: "a", state: "merged"}) || rows["a2"] != (statusRow{track: "a", state: "merged"}) {
		t.Errorf("pumi status after the run shows %v, want a1 and a2 merged in the track a", rows)
	}
	for _, e := range ofType(c.log(), events.TypePlanAdded) {
		if e.GetSource() != "/person/"+testPerson {
			t.Errorf("the plan.added %s has the source %q, want the person's /person/%s", e.GetId(), e.GetSource(), testPerson)
		}
	}
}

// TestRunStoresTheOutputFilesInTheBlobStore: the runner of pumi run
// uploads the file that the adapter reports to the local blob store, at
// pumi/blobs/local/<sha256> in the clone's common git directory, and the
// invocation's result records its path and hash (decision 0232).
func TestRunStoresTheOutputFilesInTheBlobStore(t *testing.T) {
	c := newCommandClone(t, "tracks", adapterReportsAFile)
	writeFile(t, c.dir, ".gitignore", "/out/\n")
	git(t, c.dir, exec.CommandContext(t.Context(), "git", "add", ".gitignore"))
	git(t, c.dir, exec.CommandContext(t.Context(), "git", "commit", "--quiet", "--message=ignore out"))
	r := c.start()
	c.must("add", "--track", "a", "a1")
	c.waitState(r, "a1", "merged")
	r.stop(t)
	results := ofType(c.log(), events.TypeInvocationResult)
	if len(results) != 1 {
		t.Fatalf("the log has %d invocation.result events, want 1", len(results))
	}
	result := dataOf(t, results[0], &eventsv1.InvocationResult{})
	sum := sha256.Sum256([]byte(outputFileContent))
	name := hex.EncodeToString(sum[:])
	files := result.GetOutputFiles()
	if len(files) != 1 || files[0].GetPath() != outputFilePath || files[0].GetBlobHash() != "sha256:"+name {
		t.Fatalf("the result's output files are %v, want %s with sha256:%s", files, outputFilePath, name)
	}
	root, err := os.OpenRoot(filepath.Join(c.dir, ".git", "pumi"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = root.Close() }()
	content, err := root.ReadFile(filepath.Join("blobs", "local", name))
	if err != nil || string(content) != outputFileContent {
		t.Errorf("pumi/blobs/local/%s holds %q, %v; want the output file's content", name, content, err)
	}
}

// TestCommandsWithNoRunOpenTheLog: with no pumi run, a command opens the
// clone's log with the configuration file, and without one it fails; the
// next pumi run runs what the commands appended.
func TestCommandsWithNoRunOpenTheLog(t *testing.T) {
	c := newCommandClone(t, "tracks", adapterSucceeds)
	if out, err := c.pumi("status"); err == nil || !strings.Contains(out+err.Error(), "--config") {
		t.Errorf("pumi status with no run and no configuration file = %q, %v; want an error that names --config", out, err)
	}
	c.must("add", "--config", c.config, "--track", "a", "a1")
	c.must("add", "--config", c.config, "--track", "b", "b1")
	if rows := c.status("--config", c.config); rows["a1"] != (statusRow{track: "a", state: "waiting"}) {
		t.Errorf("pumi status shows a1 as %v, want waiting in the track a", rows["a1"])
	}
	if out, err := c.pumi("add", "--config", c.config, "--track", "a", "--depends-on", "b1", "a2"); err == nil {
		t.Errorf("pumi add of a plan that depends on a plan of another track succeeded: %q", out)
	}
	c.must("cancel", "--config", c.config, "b1")
	r := c.start()
	c.waitState(r, "b1", "cancelled")
	c.waitState(r, "a1", "merged")
}

// TestCancelARunningPlan: pumi cancel cancels a plan as the person while
// its agent run runs; the plan is cancelled at once, and the runner stops
// the adapter and reports the outcome cancelled (decision 0192).
func TestCancelARunningPlan(t *testing.T) {
	c := newCommandClone(t, "tracks", adapterWaits)
	r := c.start()
	c.must("add", "--track", "a", "a1")
	c.waitEvent(r, events.TypeInvocationStarted)
	if row := c.status("--config", c.config)["a1"]; row.state != "implementing" {
		t.Errorf("pumi status shows a1 in %q while it runs, want implementing", row.state)
	}
	c.must("cancel", "a1")
	c.waitState(r, "a1", "cancelled")
	finished := dataOf(t, c.waitEvent(r, events.TypeInvocationFinished), &eventsv1.InvocationFinished{})
	if finished.GetOutcome() != eventsv1.InvocationFinished_OUTCOME_CANCELLED {
		t.Errorf("the invocation finished with %v, want cancelled", finished.GetOutcome())
	}
	var asked *eventsv1.CancelRequested
	for _, e := range ofType(c.log(), events.TypeCancelRequested) {
		if e.GetSource() == "/person/"+testPerson {
			asked = dataOf(t, e, &eventsv1.CancelRequested{})
		}
	}
	if asked.GetPlan() != "a1" || asked.GetRequestedBy() != testPerson {
		t.Errorf("the person's cancel.requested is %v, want one of a1 requested by %s", asked, testPerson)
	}
}

// TestCancelOfAPlanThatWasNotAddedFails: pumi cancel names a plan of the
// log.
func TestCancelOfAPlanThatWasNotAddedFails(t *testing.T) {
	c := newCommandClone(t, "tracks", adapterSucceeds)
	if out, err := c.pumi("cancel", "--config", c.config, "nope"); err == nil {
		t.Errorf("pumi cancel of a plan that was not added succeeded: %q", out)
	}
	if evs := ofType(c.log(), events.TypeCancelRequested); len(evs) != 0 {
		t.Errorf("the log has %d cancel.requested events, want none", len(evs))
	}
}

// TestAnswerAnEscalation: pumi status shows the question of a plan's open
// escalation, and pumi answer resolves it as the person; the answer's
// resume is denied over the caps, so the plan escalates again.
func TestAnswerAnEscalation(t *testing.T) {
	c := newCommandClone(t, "spike", adapterOverCaps)
	r := c.start()
	if out, err := c.pumi("answer", "plan-a", "Go", "on."); err == nil {
		t.Errorf("pumi answer of a plan that was not added succeeded: %q", out)
	}
	c.must("add", "--track", "main", "plan-a")
	row := c.waitState(r, "plan-a", "implementing-escalated")
	raised := c.waitEvent(r, events.TypeEscalationRaised)
	question := dataOf(t, raised, &eventsv1.EscalationRaised{}).GetQuestion()
	if row.escalation != strings.Join(strings.Fields(question), " ") {
		t.Errorf("pumi status shows the escalation %q, want its question %q", row.escalation, question)
	}
	c.must("answer", "plan-a", "Go", "on.")
	resolved := dataOf(t, c.waitEvent(r, events.TypeEscalationResolved), &eventsv1.EscalationResolved{})
	want := &eventsv1.EscalationResolved{Escalation: raised.GetId(), Person: testPerson, Answer: "Go on."}
	if resolved.GetEscalation() != want.GetEscalation() || resolved.GetPerson() != want.GetPerson() || resolved.GetAnswer() != want.GetAnswer() {
		t.Errorf("the escalation.resolved is %v, want %v", resolved, want)
	}
	deadline := time.Now().Add(commandWait)
	for len(ofType(c.log(), events.TypeEscalationRaised)) < 2 {
		if time.Now().After(deadline) {
			t.Fatalf("the plan did not escalate again after the answer; the output of pumi run:\n%s", r.out.String())
		}
		time.Sleep(50 * time.Millisecond)
	}
	if row := c.status("--config", c.config)["plan-a"]; row.escalation == "" {
		t.Errorf("pumi status shows no open escalation of plan-a after it escalated again")
	}
}

// TestACloneWithNoUserEmailRefusesThePersonsCommands: the person is the
// clone's git user.email, and a clone without one refuses the commands
// that act as a person (decision 0208).
func TestACloneWithNoUserEmailRefusesThePersonsCommands(t *testing.T) {
	c := newCommandClone(t, "tracks", adapterSucceeds)
	git(t, c.dir, exec.CommandContext(t.Context(), "git", "config", "--unset", "user.email"))
	for _, args := range [][]string{
		{"add", "--config", c.config, "--track", "a", "a1"},
		{"run", "--config", c.config},
	} {
		out, err := c.pumi(args...)
		if err == nil || !strings.Contains(err.Error(), "user.email") {
			t.Errorf("pumi %s = %q, %v; want an error that names user.email", strings.Join(args, " "), out, err)
		}
	}
	if _, err := os.Stat(filepath.Join(c.dir, ".git", "pumi", "log.jsonl")); err == nil {
		if evs := c.log(); len(evs) != 0 {
			t.Errorf("the log has %d events, want none", len(evs))
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
	}
}
