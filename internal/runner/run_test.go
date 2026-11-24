// SPDX-License-Identifier: Apache-2.0
// SPDX-FileCopyrightText: The Pumi Authors

package runner

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"connectrpc.com/connect/v2"
	eventsv1 "getpumi.dev/pumi/api/gen/pumi/events/v1"
	harnessv1 "getpumi.dev/pumi/api/gen/pumi/harness/v1"
	runnerv1 "getpumi.dev/pumi/api/gen/pumi/runner/v1"
	workflowv1 "getpumi.dev/pumi/api/gen/pumi/workflow/v1"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/durationpb"
)

// fakeAdapterMode is the environment variable that makes the test binary
// act as a fake harness adapter in one of the modes of fakeAdapter. The
// runner starts the test binary again as /proc/self/exe, a constant path,
// which works only on Linux.
const fakeAdapterMode = "PUMI_FAKE_ADAPTER"

// fakeAdapterPIDDir is the environment variable that names the directory
// in which the fake adapter writes its process id to the file pid.
const fakeAdapterPIDDir = "PUMI_FAKE_ADAPTER_PID_DIR"

// fakeAdapterPrefix starts each line that the fake adapter writes to its
// standard error.
const fakeAdapterPrefix = "fake adapter: "

func TestMain(m *testing.M) {
	if mode, ok := os.LookupEnv(fakeAdapterMode); ok {
		os.Exit(fakeAdapter(mode))
	}
	os.Exit(m.Run())
}

// fakeAdapter acts as a harness adapter in mode and returns its exit
// status. It reads the harness invocation from standard input, logs it and
// its process id to standard error, checks that it runs in the workspace,
// and then does what mode says.
func fakeAdapter(mode string) int {
	if mode == "sleep" {
		if err := writePID(); err != nil {
			return 9
		}
		time.Sleep(time.Hour)
		return 0
	}
	in := bufio.NewReader(os.Stdin)
	line, err := in.ReadString('\n')
	if err != nil {
		fmt.Fprintln(os.Stderr, fakeAdapterPrefix+"no invocation:", err)
		return 9
	}
	inv := &harnessv1.Invocation{}
	if err := protojson.Unmarshal([]byte(line), inv); err != nil {
		fmt.Fprintln(os.Stderr, fakeAdapterPrefix+"the invocation does not parse:", err)
		return 9
	}
	fmt.Fprintf(os.Stderr, "%spid %d\n", fakeAdapterPrefix, os.Getpid())
	if err := writePID(); err != nil {
		fmt.Fprintln(os.Stderr, fakeAdapterPrefix+"the process id could not be written:", err)
		return 9
	}
	fmt.Fprintf(os.Stderr, "%sinvocation %s\n", fakeAdapterPrefix, strings.TrimSpace(line))
	if wd, err := os.Getwd(); err != nil || wd != inv.GetWorkspace() {
		fmt.Fprintf(os.Stderr, "%sruns in %s, not the workspace %s\n", fakeAdapterPrefix, wd, inv.GetWorkspace())
		return 9
	}
	done := `{"summary":"done"}`
	usage := &harnessv1.Usage{Tokens: 10, CostMicrodollars: 20, Seconds: 1}
	switch mode {
	case "success":
		write("internal/runner/b.go", "package runner\n")
		// Long enough for heartbeats and log batches.
		time.Sleep(150 * time.Millisecond)
		return result(&harnessv1.Result{Status: harnessv1.Result_STATUS_COMPLETED, Output: done, Usage: usage})
	case "unchanged":
		return result(&harnessv1.Result{Status: harnessv1.Result_STATUS_COMPLETED, Output: done, Usage: usage})
	case "failed":
		return result(&harnessv1.Result{Status: harnessv1.Result_STATUS_FAILED, Usage: usage})
	case "no output":
		return result(&harnessv1.Result{Status: harnessv1.Result_STATUS_COMPLETED, Usage: usage})
	case "unknown field":
		return result(&harnessv1.Result{Status: harnessv1.Result_STATUS_COMPLETED, Output: `{"summary":"done","extra":1}`, Usage: usage})
	case "output files":
		write("internal/runner/report.txt", "report\n")
		write("internal/runner/notes/b.txt", "notes\n")
		return result(&harnessv1.Result{Status: harnessv1.Result_STATUS_COMPLETED, Output: done,
			OutputFiles: []string{"internal/runner/report.txt", "internal/runner/./notes/b.txt"}, Usage: usage})
	case "output files outside the paths":
		write("internal/runner/report.txt", "report\n")
		write("README.md", "changed outside the paths\n")
		return result(&harnessv1.Result{Status: harnessv1.Result_STATUS_COMPLETED, Output: done, OutputFiles: []string{"internal/runner/report.txt"}, Usage: usage})
	case "output file absolute":
		return result(&harnessv1.Result{Status: harnessv1.Result_STATUS_COMPLETED, Output: done,
			OutputFiles: []string{filepath.Join(os.Getenv(fakeAdapterPIDDir), "pid")}, Usage: usage})
	case "output file above":
		return result(&harnessv1.Result{Status: harnessv1.Result_STATUS_COMPLETED, Output: done, OutputFiles: []string{"../index"}, Usage: usage})
	case "output file link":
		if err := os.Symlink(filepath.Join(os.Getenv(fakeAdapterPIDDir), "pid"), "internal/runner/link"); err != nil {
			panic(err)
		}
		return result(&harnessv1.Result{Status: harnessv1.Result_STATUS_COMPLETED, Output: done, OutputFiles: []string{"internal/runner/link"}, Usage: usage})
	case "output file missing":
		return result(&harnessv1.Result{Status: harnessv1.Result_STATUS_COMPLETED, Output: done, OutputFiles: []string{"internal/runner/never.txt"}, Usage: usage})
	case "output file directory":
		return result(&harnessv1.Result{Status: harnessv1.Result_STATUS_COMPLETED, Output: done, OutputFiles: []string{"internal/runner"}, Usage: usage})
	case "output file empty":
		return result(&harnessv1.Result{Status: harnessv1.Result_STATUS_COMPLETED, Output: done, OutputFiles: []string{""}, Usage: usage})
	case "split credential":
		// The gateway token in two writes, with log batches between them.
		token := inv.GetGateway().GetToken()
		half := len(token) / 2
		fmt.Fprintf(os.Stderr, "%stoken %s", fakeAdapterPrefix, token[:half])
		time.Sleep(200 * time.Millisecond)
		fmt.Fprintf(os.Stderr, "%s end\n", token[half:])
		return result(&harnessv1.Result{Status: harnessv1.Result_STATUS_COMPLETED, Output: done, Usage: usage})
	case "not json":
		fmt.Println("this is not a result")
		return 0
	case "no result":
		return 0
	case "exit 3":
		result(&harnessv1.Result{Status: harnessv1.Result_STATUS_COMPLETED, Output: done, Usage: usage})
		return 3
	case "outside":
		write("internal/runner/b.go", "package runner\n")
		write("README.md", "changed outside the paths\n")
		return result(&harnessv1.Result{Status: harnessv1.Result_STATUS_COMPLETED, Output: done, Usage: usage})
	case "stopped unasked":
		return result(&harnessv1.Result{Status: harnessv1.Result_STATUS_STOPPED, Usage: usage})
	case "stop":
		// Wait until standard input closes, then write what it has.
		write("internal/runner/b.go", "package runner\n")
		_, _ = in.ReadString(0)
		fmt.Fprintln(os.Stderr, fakeAdapterPrefix+"standard input closed")
		return result(&harnessv1.Result{Status: harnessv1.Result_STATUS_STOPPED, Output: `{"summary":"partial"}`, Usage: usage})
	case "leave child":
		startChild()
		return result(&harnessv1.Result{Status: harnessv1.Result_STATUS_COMPLETED, Output: done, Usage: usage})
	case "ignore stop with child":
		startChild()
		_, _ = in.ReadString(0)
		time.Sleep(time.Hour)
		return 0
	case "ignore stop":
		_, _ = in.ReadString(0)
		fmt.Fprintln(os.Stderr, fakeAdapterPrefix+"standard input closed")
		time.Sleep(time.Hour)
		return 0
	}
	fmt.Fprintln(os.Stderr, fakeAdapterPrefix+"no mode", mode)
	return 9
}

// writePID writes the process id to the file pid in the directory that
// fakeAdapterPIDDir names, or to the file child for a process that the
// fake adapter started.
func writePID() error {
	root, err := os.OpenRoot(os.Getenv(fakeAdapterPIDDir))
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()
	name := "pid"
	if os.Getenv(fakeAdapterMode) == "sleep" {
		name = "child"
	}
	return root.WriteFile(name, []byte(strconv.Itoa(os.Getpid())), 0o600)
}

// startChild starts the test binary again in the mode sleep, a process
// that outlives the fake adapter unless it is killed, and waits until it
// wrote its process id.
func startChild() {
	cmd := exec.CommandContext(context.Background(), "/proc/self/exe")
	cmd.Env = append(os.Environ(), fakeAdapterMode+"=sleep")
	if err := cmd.Start(); err != nil {
		panic(err)
	}
	root, err := os.OpenRoot(os.Getenv(fakeAdapterPIDDir))
	if err != nil {
		panic(err)
	}
	defer func() { _ = root.Close() }()
	for {
		if _, err := root.Stat("child"); err == nil {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// write writes content to the file name of the fake adapter's workspace.
func write(name, content string) {
	if err := os.MkdirAll(filepath.Dir(name), 0o750); err != nil {
		panic(err)
	}
	if err := os.WriteFile(name, []byte(content), 0o600); err != nil {
		panic(err)
	}
}

// result writes r to standard output and returns exit status 0.
func result(r *harnessv1.Result) int {
	out, err := protojson.Marshal(r)
	if err != nil {
		panic(err)
	}
	fmt.Println(string(out))
	return 0
}

// fakeService is a RunnerService that hands out one invocation and records
// the calls.
type fakeService struct {
	mu sync.Mutex
	// claim is the response of the first Claim; later claims get none.
	claim *runnerv1.ClaimResponse
	// cancelAt is the heartbeat whose response carries a cancel request,
	// counted from 1; zero for none.
	cancelAt int
	// cancelWhen, when set, makes the response of every heartbeat for which
	// it reports true carry a cancel request.
	cancelWhen func() bool
	// heartbeatErr is the error of every Heartbeat when set.
	heartbeatErr error
	// logFailures is how many of the first SendLog calls fail because the
	// control plane cannot be reached.
	logFailures int
	// leaseTimeout is the lease timeout that each heartbeat gives;
	// testLeaseTimeout when zero.
	leaseTimeout time.Duration
	heartbeats   int
	logs         []byte
	// batches are the log batches sent, one entry for each.
	batches []string
	reports []*runnerv1.ReportRequest
}

func (f *fakeService) Claim(_ context.Context, req *runnerv1.ClaimRequest) (*runnerv1.ClaimResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(req.GetProtocolVersions()) == 0 {
		return nil, errors.New("the claim names no protocol version")
	}
	res := f.claim
	f.claim = nil
	if res == nil {
		return &runnerv1.ClaimResponse{ProtocolVersion: req.GetProtocolVersions()[0]}, nil
	}
	return res, nil
}

func (f *fakeService) Heartbeat(_ context.Context, req *runnerv1.HeartbeatRequest) (*runnerv1.HeartbeatResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if req.GetInvocation() != "plan-1/1" || req.GetLeaseNumber() != 4 {
		return nil, fmt.Errorf("a heartbeat of %s with the lease %d", req.GetInvocation(), req.GetLeaseNumber())
	}
	f.heartbeats++
	if f.heartbeatErr != nil {
		return nil, f.heartbeatErr
	}
	timeout := f.leaseTimeout
	if timeout == 0 {
		timeout = testLeaseTimeout
	}
	res := &runnerv1.HeartbeatResponse{LeaseTimeout: durationpb.New(timeout)}
	if f.heartbeats == f.cancelAt || (f.cancelWhen != nil && f.cancelWhen()) {
		res.Cancel = &runnerv1.HeartbeatResponse_Cancel{RequestedBy: "/person/michael"}
	}
	return res, nil
}

func (f *fakeService) Report(_ context.Context, req *runnerv1.ReportRequest) (*runnerv1.ReportResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reports = append(f.reports, req)
	return &runnerv1.ReportResponse{}, nil
}

func (f *fakeService) SendLog(_ context.Context, req *runnerv1.SendLogRequest) (*runnerv1.SendLogResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if req.GetInvocation() != "plan-1/1" || req.GetLeaseNumber() != 4 {
		return nil, fmt.Errorf("a log batch of %s with the lease %d", req.GetInvocation(), req.GetLeaseNumber())
	}
	if f.logFailures > 0 {
		f.logFailures--
		return nil, unreachable
	}
	f.logs = append(f.logs, req.GetOutput()...)
	f.batches = append(f.batches, string(req.GetOutput()))
	return &runnerv1.SendLogResponse{}, nil
}

// fakeBlobs is the blob store of the runner's tenant in the tests: it
// keeps each blob in memory, or fails every Put with err when it is set.
type fakeBlobs struct {
	mu    sync.Mutex
	blobs map[string]string
	err   error
}

func (b *fakeBlobs) Put(_ context.Context, content io.Reader) (string, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.err != nil {
		return "", b.err
	}
	data, err := io.ReadAll(content)
	if err != nil {
		return "", err
	}
	hash := blobHash(string(data))
	if b.blobs == nil {
		b.blobs = map[string]string{}
	}
	b.blobs[hash] = string(data)
	return hash, nil
}

// stored returns the blobs stored so far.
func (b *fakeBlobs) stored() map[string]string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return maps.Clone(b.blobs)
}

// outputBlobs returns the blobs stored so far, without the log of the
// report.
func (f *fixture) outputBlobs(report *runnerv1.ReportRequest) map[string]string {
	stored := f.blobs.stored()
	delete(stored, report.GetResult().GetLogBlobHash())
	return stored
}

// blobHash returns the blob hash of content: "sha256:" and its SHA-256.
func blobHash(content string) string {
	sum := sha256.Sum256([]byte(content))
	return "sha256:" + hex.EncodeToString(sum[:])
}

// report returns the one report, failing the test when there is not
// exactly one.
func (f *fakeService) report(t *testing.T) *runnerv1.ReportRequest {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.reports) != 1 {
		t.Fatalf("the runner made %d reports, want 1; the log is\n%s", len(f.reports), f.logs)
	}
	return f.reports[0]
}

// log returns the log output sent so far.
func (f *fakeService) log() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return string(f.logs)
}

// testLeaseTimeout is the lease timeout of the fake service, short so
// that the runner sends heartbeats during a test.
const testLeaseTimeout = 30 * time.Millisecond

// fixture is a repository, a fake service that hands out one invocation
// of its tree and the configuration of a runner for them.
type fixture struct {
	repo    *testRepo
	service *fakeService
	blobs   *fakeBlobs
	cfg     Config
	// pidDir is the directory in which the fake adapter writes its
	// process id.
	pidDir string
}

// newFixture returns a fixture whose adapter runs in mode.
func newFixture(t *testing.T, mode string) *fixture {
	t.Helper()
	t.Setenv(fakeAdapterMode, mode)
	pidDir := t.TempDir()
	t.Setenv(fakeAdapterPIDDir, pidDir)
	repo := newTestRepo(t)
	doc := &runnerv1.Invocation{
		Invocation:     "plan-1/1",
		IdempotencyKey: "plan-1/1",
		Component:      &harnessv1.Component{Kind: harnessv1.Component_KIND_AGENT_RUN, Name: "fake", Version: "1"},
		Input:          &harnessv1.Input{Repo: "example/repo", Tree: repo.tree, Task: "Add b.go."},
		Role:           harnessv1.Role_ROLE_AGENT,
		Capabilities: &harnessv1.Capabilities{
			WritePaths: []string{"internal/runner/**"},
			Tools:      []harnessv1.Capabilities_ToolKind{harnessv1.Capabilities_TOOL_KIND_EDIT},
		},
		OutputMessage:     "pumi.workflow.v1.ImplementationOutput",
		CredentialHandles: []string{"handle-never-sent"},
	}
	service := &fakeService{claim: &runnerv1.ClaimResponse{
		ProtocolVersion: &runnerv1.ProtocolVersion{Major: 1},
		Invocation:      doc,
		LeaseNumber:     4,
		LeaseTimeout:    durationpb.New(testLeaseTimeout),
	}}
	blobs := &fakeBlobs{}
	return &fixture{repo: repo, service: service, blobs: blobs, pidDir: pidDir, cfg: Config{
		Service:    service,
		Blobs:      blobs,
		Adapter:    "/proc/self/exe",
		Repository: repo.dir,
		WorkDir:    t.TempDir(),
		// Long enough for an adapter that stops on its own to start, which
		// takes a while with the race detector.
		Grace:       10 * time.Second,
		LogInterval: 20 * time.Millisecond,
	}}
}

// cancelOnceWritten makes the heartbeats carry a cancel request once the
// fake adapter wrote the file name to its process id directory. A test
// whose grace period is short waits for the adapter so: a test binary
// built with -race takes about a second to start, and a cancel at a fixed
// heartbeat would let the runner kill the adapter before it wrote its
// process id.
func (f *fixture) cancelOnceWritten(name string) {
	f.service.cancelWhen = func() bool {
		_, err := os.Stat(filepath.Join(f.pidDir, name))
		return err == nil
	}
}

// runOne runs one invocation and fails the test unless the runner claimed
// one and returned no error.
func (f *fixture) runOne(t *testing.T) *runnerv1.ReportRequest {
	t.Helper()
	ran, err := RunOne(t.Context(), f.cfg)
	if err != nil || !ran {
		t.Fatalf("RunOne = %v, %v; want true, nil; the log is\n%s", ran, err, f.service.log())
	}
	return f.service.report(t)
}

// changed returns the paths that tree changes from the fixture's tree.
func (f *fixture) changed(t *testing.T, tree string) []string {
	t.Helper()
	g, err := openRepository(t.Context(), f.repo.dir)
	if err != nil {
		t.Fatal(err)
	}
	paths, err := g.changed(t.Context(), f.repo.tree, tree)
	if err != nil {
		t.Fatal(err)
	}
	return paths
}

// sentInvocation returns the harness invocation that the fake adapter
// logged.
func sentInvocation(t *testing.T, log string) *harnessv1.Invocation {
	t.Helper()
	for line := range strings.Lines(log) {
		if rest, ok := strings.CutPrefix(line, fakeAdapterPrefix+"invocation "); ok {
			inv := &harnessv1.Invocation{}
			if err := protojson.Unmarshal([]byte(rest), inv); err != nil {
				t.Fatal(err)
			}
			return inv
		}
	}
	t.Fatalf("the adapter logged no invocation; the log is\n%s", log)
	return nil
}

// checkEmpty fails the test when the directory dir has any entries.
func checkEmpty(t *testing.T, dir string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Errorf("%s still holds %v", dir, entries)
	}
}

func TestASuccessfulInvocation(t *testing.T) {
	f := newFixture(t, "success")
	report := f.runOne(t)
	if report.GetInvocation() != "plan-1/1" || report.GetLeaseNumber() != 4 {
		t.Errorf("the report is of %s with the lease %d, want plan-1/1 with 4", report.GetInvocation(), report.GetLeaseNumber())
	}
	if report.GetOutcome() != eventsv1.InvocationFinished_OUTCOME_SUCCEEDED {
		t.Errorf("the outcome is %v, want succeeded; the log is\n%s", report.GetOutcome(), f.service.log())
	}
	result := report.GetResult()
	if got := f.changed(t, result.GetTree()); len(got) != 1 || got[0] != "internal/runner/b.go" {
		t.Errorf("the resulting tree changes %q, want internal/runner/b.go", got)
	}
	out := &workflowv1.ImplementationOutput{}
	if err := protojson.Unmarshal([]byte(result.GetOutput()), out); err != nil || out.GetSummary() != "done" {
		t.Errorf("the output %q parses to %v, %v; want the summary done", result.GetOutput(), out, err)
	}
	if u := result.GetUsage(); u.GetTokens() != 10 || u.GetCostMicrodollars() != 20 || u.GetSeconds() != 1 {
		t.Errorf("the usage is %v, want the adapter's", u)
	}
	if f.service.heartbeats == 0 {
		t.Error("the runner sent no heartbeat while the adapter ran")
	}
	checkEmpty(t, f.cfg.WorkDir)
}

func TestTheAdapterGetsTheHarnessInvocation(t *testing.T) {
	f := newFixture(t, "success")
	f.runOne(t)
	inv := sentInvocation(t, f.service.log())
	if !filepath.IsAbs(inv.GetWorkspace()) || filepath.Dir(filepath.Dir(inv.GetWorkspace())) != f.cfg.WorkDir {
		t.Errorf("the workspace is %s, want a new directory in %s", inv.GetWorkspace(), f.cfg.WorkDir)
	}
	if inv.GetInput().GetTree() != f.repo.tree || inv.GetInput().GetTask() != "Add b.go." {
		t.Errorf("the input is %v", inv.GetInput())
	}
	schema := inv.GetOutputSchema()
	if schema.GetMessage() != "pumi.workflow.v1.ImplementationOutput" || !strings.Contains(schema.GetJsonSchema(), `"summary"`) {
		t.Errorf("the output schema is %v, want ImplementationOutput's", schema)
	}
	if inv.GetGateway() != nil {
		t.Errorf("the gateway is %v, want none until E7 (decision 0185)", inv.GetGateway())
	}
	if strings.Contains(f.service.log(), "handle-never-sent") {
		t.Error("the adapter got a credential handle")
	}
}

func TestTheLogComesFromStandardError(t *testing.T) {
	f := newFixture(t, "success")
	f.runOne(t)
	if log := f.service.log(); !strings.Contains(log, fakeAdapterPrefix+"pid ") {
		t.Errorf("the log does not hold the adapter's standard error; it is\n%s", log)
	}
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

// TestTheLogIsWrittenToTheOutput: the runner also writes the log, the
// adapter's standard error and its own lines, to Config.Output, each line
// after the invocation's id, so that pumi run prints it (getpumi/pumi#88).
func TestTheLogIsWrittenToTheOutput(t *testing.T) {
	f := newFixture(t, "exit 3")
	var out syncBuffer
	f.cfg.Output = &out
	f.runOne(t)
	got := out.String()
	for _, want := range []string{"plan-1/1: " + fakeAdapterPrefix + "pid ", "plan-1/1: " + runnerPrefix} {
		if !strings.Contains(got, want) {
			t.Errorf("the output does not hold a line starting with %q; it is\n%s", want, got)
		}
	}
	for line := range strings.SplitSeq(strings.TrimSuffix(got, "\n"), "\n") {
		if !strings.HasPrefix(line, "plan-1/1: ") {
			t.Errorf("the output line %q does not start with the invocation", line)
		}
	}
}

func TestAnUnchangedWorkspaceGivesNoTree(t *testing.T) {
	f := newFixture(t, "unchanged")
	report := f.runOne(t)
	if report.GetOutcome() != eventsv1.InvocationFinished_OUTCOME_SUCCEEDED || report.GetResult().GetTree() != "" {
		t.Errorf("the outcome is %v with the tree %q, want succeeded with none (decision 0162)",
			report.GetOutcome(), report.GetResult().GetTree())
	}
}

func TestAFailedRun(t *testing.T) {
	f := newFixture(t, "failed")
	report := f.runOne(t)
	if report.GetOutcome() != eventsv1.InvocationFinished_OUTCOME_FAILED {
		t.Errorf("the outcome is %v, want failed", report.GetOutcome())
	}
	if report.GetResult().GetUsage().GetTokens() != 10 {
		t.Errorf("the usage is %v, want the adapter's", report.GetResult().GetUsage())
	}
}

func TestAnInvalidResultFailsTheInvocation(t *testing.T) {
	for _, mode := range []string{"not json", "no result", "exit 3", "unknown field", "no output", "stopped unasked"} {
		t.Run(mode, func(t *testing.T) {
			f := newFixture(t, mode)
			report := f.runOne(t)
			if report.GetOutcome() != eventsv1.InvocationFinished_OUTCOME_FAILED {
				t.Errorf("the outcome is %v, want failed", report.GetOutcome())
			}
			if r := report.GetResult(); r.GetTree() != "" || r.GetOutput() != "" {
				t.Errorf("the result holds the tree %q and the output %q, want neither", r.GetTree(), r.GetOutput())
			}
			if !strings.Contains(f.service.log(), runnerPrefix) {
				t.Errorf("the log does not say why the invocation failed; it is\n%s", f.service.log())
			}
		})
	}
}

func TestOutputFilesAreUploaded(t *testing.T) {
	f := newFixture(t, "output files")
	report := f.runOne(t)
	if report.GetOutcome() != eventsv1.InvocationFinished_OUTCOME_SUCCEEDED {
		t.Fatalf("the outcome is %v, want succeeded; the log is\n%s", report.GetOutcome(), f.service.log())
	}
	want := []*eventsv1.InvocationResult_OutputFile{
		{Path: "internal/runner/report.txt", BlobHash: blobHash("report\n")},
		{Path: "internal/runner/notes/b.txt", BlobHash: blobHash("notes\n")},
	}
	got := report.GetResult().GetOutputFiles()
	if len(got) != len(want) {
		t.Fatalf("the output files are %v, want %v", got, want)
	}
	for i := range want {
		if !proto.Equal(got[i], want[i]) {
			t.Errorf("output file %d is %v, want %v", i, got[i], want[i])
		}
	}
	stored := f.outputBlobs(report)
	if stored[blobHash("report\n")] != "report\n" || stored[blobHash("notes\n")] != "notes\n" || len(stored) != 2 {
		t.Errorf("the blob store holds %q, want the two files", stored)
	}
}

func TestABadOutputFileFailsTheInvocation(t *testing.T) {
	for mode, named := range map[string]string{
		"output file absolute":  `/pid" is not a path in the workspace`,
		"output file above":     `"../index" is not a path in the workspace`,
		"output file link":      `"internal/runner/link": openat internal/runner/link: path escapes from parent`,
		"output file missing":   `"internal/runner/never.txt": openat internal/runner/never.txt: no such file or directory`,
		"output file directory": `"internal/runner": it is not a regular file`,
		"output file empty":     `"" is not a path in the workspace`,
	} {
		t.Run(mode, func(t *testing.T) {
			f := newFixture(t, mode)
			report := f.runOne(t)
			if report.GetOutcome() != eventsv1.InvocationFinished_OUTCOME_FAILED {
				t.Errorf("the outcome is %v, want failed", report.GetOutcome())
			}
			if r := report.GetResult(); r.GetTree() != "" || r.GetOutput() != "" || len(r.GetOutputFiles()) != 0 {
				t.Errorf("the result holds the tree %q, the output %q and the output files %v, want none", r.GetTree(), r.GetOutput(), r.GetOutputFiles())
			}
			if stored := f.outputBlobs(report); len(stored) != 0 {
				t.Errorf("the runner stored %q besides the log, want nothing", stored)
			}
			if log := f.service.log(); !strings.Contains(log, runnerPrefix) || !strings.Contains(log, named) {
				t.Errorf("the log does not name %s as the reason the invocation failed; it is\n%s", named, log)
			}
		})
	}
}

func TestAFailedUploadFailsTheInvocation(t *testing.T) {
	f := newFixture(t, "output files")
	f.blobs.err = errors.New("the disk is full")
	report := f.runOne(t)
	if report.GetOutcome() != eventsv1.InvocationFinished_OUTCOME_FAILED {
		t.Errorf("the outcome is %v, want failed", report.GetOutcome())
	}
	if r := report.GetResult(); len(r.GetOutputFiles()) != 0 || r.GetTree() != "" {
		t.Errorf("the result holds the output files %v and the tree %q, want neither", r.GetOutputFiles(), r.GetTree())
	}
	if log := f.service.log(); !strings.Contains(log, "the disk is full") {
		t.Errorf("the log does not give the store's error; it is\n%s", log)
	}
}

func TestOutputFilesOfAnInvocationThatFailsAreNotUploaded(t *testing.T) {
	f := newFixture(t, "output files outside the paths")
	report := f.runOne(t)
	if report.GetOutcome() != eventsv1.InvocationFinished_OUTCOME_FAILED {
		t.Errorf("the outcome is %v, want failed (decision 0159)", report.GetOutcome())
	}
	if stored := f.outputBlobs(report); len(stored) != 0 {
		t.Errorf("the runner stored %q besides the log for an invocation that failed, want nothing", stored)
	}
}

func TestAWriteOutsideThePathsFailsTheInvocation(t *testing.T) {
	f := newFixture(t, "outside")
	report := f.runOne(t)
	if report.GetOutcome() != eventsv1.InvocationFinished_OUTCOME_FAILED {
		t.Errorf("the outcome is %v, want failed (decision 0159)", report.GetOutcome())
	}
	if r := report.GetResult(); r.GetTree() != "" || r.GetOutput() != "" {
		t.Errorf("the result holds the tree %q and the output %q, want neither", r.GetTree(), r.GetOutput())
	}
	if log := f.service.log(); !strings.Contains(log, "README.md") {
		t.Errorf("the log does not name the path outside; it is\n%s", log)
	}
}

func TestATreeTheRepositoryDoesNotHaveFailsTheInvocation(t *testing.T) {
	f := newFixture(t, "success")
	f.service.claim.Invocation.Input.Tree = "0123456789abcdef0123456789abcdef01234567"
	report := f.runOne(t)
	if report.GetOutcome() != eventsv1.InvocationFinished_OUTCOME_FAILED {
		t.Errorf("the outcome is %v, want failed", report.GetOutcome())
	}
	if strings.Contains(f.service.log(), fakeAdapterPrefix) {
		t.Error("the runner started the adapter without a workspace")
	}
}

func TestACancelStopsTheAdapter(t *testing.T) {
	f := newFixture(t, "stop")
	f.service.cancelAt = 2
	report := f.runOne(t)
	if report.GetOutcome() != eventsv1.InvocationFinished_OUTCOME_CANCELLED {
		t.Errorf("the outcome is %v, want cancelled (decision 0035); the log is\n%s", report.GetOutcome(), f.service.log())
	}
	if !strings.Contains(f.service.log(), fakeAdapterPrefix+"standard input closed") {
		t.Errorf("the adapter did not see standard input close; the log is\n%s", f.service.log())
	}
	out := &workflowv1.ImplementationOutput{}
	if err := protojson.Unmarshal([]byte(report.GetResult().GetOutput()), out); err != nil || out.GetSummary() != "partial" {
		t.Errorf("the output is %q, want the partial result; the log is\n%s", report.GetResult().GetOutput(), f.service.log())
	}
	if got := f.changed(t, report.GetResult().GetTree()); len(got) != 1 {
		t.Errorf("the resulting tree changes %q, want the partial change", got)
	}
}

func TestAnAdapterThatDoesNotStopIsKilledAfterTheGracePeriod(t *testing.T) {
	f := newFixture(t, "ignore stop")
	f.cancelOnceWritten("pid")
	f.cfg.Grace = 500 * time.Millisecond
	start := time.Now()
	report := f.runOne(t)
	if elapsed := time.Since(start); elapsed < f.cfg.Grace {
		t.Errorf("the runner killed the adapter after %v, before the grace period of %v", elapsed, f.cfg.Grace)
	}
	if report.GetOutcome() != eventsv1.InvocationFinished_OUTCOME_CANCELLED {
		t.Errorf("the outcome is %v, want cancelled", report.GetOutcome())
	}
	f.checkGone(t)
}

func TestProcessesTheAdapterStartedAreKilled(t *testing.T) {
	for _, c := range []struct {
		mode    string
		outcome eventsv1.InvocationFinished_Outcome
	}{
		{"leave child", eventsv1.InvocationFinished_OUTCOME_SUCCEEDED},
		{"ignore stop with child", eventsv1.InvocationFinished_OUTCOME_CANCELLED},
	} {
		t.Run(c.mode, func(t *testing.T) {
			f := newFixture(t, c.mode)
			if c.outcome == eventsv1.InvocationFinished_OUTCOME_CANCELLED {
				f.cancelOnceWritten("child")
			}
			f.cfg.Grace = 500 * time.Millisecond
			if report := f.runOne(t); report.GetOutcome() != c.outcome {
				t.Errorf("the outcome is %v, want %v; the log is\n%s", report.GetOutcome(), c.outcome, f.service.log())
			}
			f.checkGone(t)
		})
	}
}

func TestStoppingTheRunnerStopsTheAdapterWithoutAReport(t *testing.T) {
	for _, mode := range []string{"stop", "ignore stop"} {
		t.Run(mode, func(t *testing.T) {
			f := newFixture(t, mode)
			f.cfg.Grace = 500 * time.Millisecond
			ctx, cancel := context.WithCancel(t.Context())
			go func() {
				waitFor(t, func() bool { return strings.Contains(f.service.log(), fakeAdapterPrefix+"invocation") })
				cancel()
			}()
			ran, err := RunOne(ctx, f.cfg)
			if !ran || !errors.Is(err, context.Canceled) {
				t.Errorf("RunOne = %v, %v; want true and the context's error", ran, err)
			}
			if len(f.service.reports) != 0 {
				t.Errorf("the runner reported %v; a runner that stops reports nothing (decision 0182)", f.service.reports)
			}
			f.checkGone(t)
			checkEmpty(t, f.cfg.WorkDir)
		})
	}
}

func TestLosingTheLeaseStopsTheAdapterWithoutAReport(t *testing.T) {
	f := newFixture(t, "stop")
	f.service.heartbeatErr = connect.NewError(connect.CodeAborted, "the lease is not current")
	ran, err := RunOne(t.Context(), f.cfg)
	if !ran || err == nil || !strings.Contains(err.Error(), "the lease is not current") {
		t.Errorf("RunOne = %v, %v; want true and the heartbeat's error", ran, err)
	}
	if len(f.service.reports) != 0 {
		t.Errorf("the runner reported %v after it lost the lease", f.service.reports)
	}
	f.checkGone(t)
}

// unreachable is the error of a call of RunnerService that cannot reach
// the control plane.
var unreachable = connect.NewError(connect.CodeUnavailable, "the control plane cannot be reached")

// withLimit gives the fixture's invocation a budget with the wall-time
// limit seconds, and tokens and cost that the adapter's own figures are
// not.
func (f *fixture) withLimit(seconds int64) {
	f.service.claim.Invocation.Budget = &harnessv1.Usage{Tokens: 5_000, CostMicrodollars: 7_000, Seconds: seconds}
}

// elapsed reports the wall time of RunOne.
func (f *fixture) timedRunOne(t *testing.T) (*runnerv1.ReportRequest, time.Duration) {
	t.Helper()
	start := time.Now()
	report := f.runOne(t)
	return report, time.Since(start)
}

func TestTheWallTimeLimitStopsTheAdapter(t *testing.T) {
	f := newFixture(t, "stop")
	f.withLimit(1)
	report, elapsed := f.timedRunOne(t)
	if report.GetOutcome() != eventsv1.InvocationFinished_OUTCOME_CAP_REACHED {
		t.Errorf("the outcome is %v, want cap reached (decision 0193); the log is\n%s", report.GetOutcome(), f.service.log())
	}
	if elapsed < time.Second {
		t.Errorf("the runner stopped the adapter after %v, before the limit of 1s", elapsed)
	}
	if !strings.Contains(f.service.log(), fakeAdapterPrefix+"standard input closed") {
		t.Errorf("the adapter did not see standard input close; the log is\n%s", f.service.log())
	}
	if !strings.Contains(f.service.log(), runnerPrefix) || !strings.Contains(f.service.log(), "wall-time limit") {
		t.Errorf("the log does not say that the invocation reached its wall-time limit; it is\n%s", f.service.log())
	}
	// The adapter reported its usage, so the tokens and cost are its own;
	// the seconds are the measured wall time (decision 0204).
	u := report.GetResult().GetUsage()
	if u.GetTokens() != 10 || u.GetCostMicrodollars() != 20 {
		t.Errorf("the usage is %v, want the adapter's tokens and cost", u)
	}
	if u.GetSeconds() < 1 || u.GetSeconds() > int64(elapsed/time.Second)+1 {
		t.Errorf("the usage has %d seconds, want the measured wall time, %v", u.GetSeconds(), elapsed)
	}
	if got := f.changed(t, report.GetResult().GetTree()); len(got) != 1 {
		t.Errorf("the resulting tree changes %q, want the partial change", got)
	}
}

func TestAnAdapterKilledAtTheLimitCountsItsWholeBudget(t *testing.T) {
	f := newFixture(t, "ignore stop")
	f.withLimit(1)
	f.cfg.Grace = 500 * time.Millisecond
	report, elapsed := f.timedRunOne(t)
	if report.GetOutcome() != eventsv1.InvocationFinished_OUTCOME_CAP_REACHED {
		t.Errorf("the outcome is %v, want cap reached; the log is\n%s", report.GetOutcome(), f.service.log())
	}
	if elapsed < time.Second+f.cfg.Grace {
		t.Errorf("the runner killed the adapter after %v, before the limit and the grace period", elapsed)
	}
	// The adapter wrote no result, so the tokens and cost are the budget's
	// (decision 0204).
	u := report.GetResult().GetUsage()
	if u.GetTokens() != 5_000 || u.GetCostMicrodollars() != 7_000 {
		t.Errorf("the usage is %v, want the tokens and cost of the budget", u)
	}
	if u.GetSeconds() < 1 {
		t.Errorf("the usage has %d seconds, want the measured wall time, %v", u.GetSeconds(), elapsed)
	}
	f.checkGone(t)
}

func TestAnAdapterDoneBeforeTheLimitSucceeds(t *testing.T) {
	f := newFixture(t, "success")
	f.withLimit(3_600)
	report := f.runOne(t)
	if report.GetOutcome() != eventsv1.InvocationFinished_OUTCOME_SUCCEEDED {
		t.Errorf("the outcome is %v, want succeeded; the log is\n%s", report.GetOutcome(), f.service.log())
	}
	if u := report.GetResult().GetUsage(); u.GetTokens() != 10 || u.GetCostMicrodollars() != 20 || u.GetSeconds() != 1 {
		t.Errorf("the usage is %v, want the adapter's", u)
	}
}

func TestTheLimitHoldsWhileHeartbeatsFail(t *testing.T) {
	f := newFixture(t, "stop")
	// The lease ends 6 s after the claim, which no heartbeat renews, so the
	// limit of 3 s comes first (decision 0205).
	f.service.claim.LeaseTimeout = durationpb.New(6 * time.Second)
	f.service.heartbeatErr = unreachable
	f.withLimit(3)
	report, elapsed := f.timedRunOne(t)
	if report.GetOutcome() != eventsv1.InvocationFinished_OUTCOME_CAP_REACHED {
		t.Errorf("the outcome is %v, want cap reached; the log is\n%s", report.GetOutcome(), f.service.log())
	}
	if elapsed < 3*time.Second {
		t.Errorf("the runner stopped the adapter after %v, before the limit of 3s", elapsed)
	}
	if f.service.heartbeats == 0 {
		t.Error("the runner sent no heartbeat while the adapter ran")
	}
}

func TestAnUnreachableControlPlaneStopsTheAdapterWhenTheLeaseEnds(t *testing.T) {
	f := newFixture(t, "stop")
	f.service.claim.LeaseTimeout = durationpb.New(time.Second)
	f.service.heartbeatErr = unreachable
	start := time.Now()
	ran, err := RunOne(t.Context(), f.cfg)
	if !ran || err == nil || !strings.Contains(err.Error(), "lease") {
		t.Errorf("RunOne = %v, %v; want true and an error that says the lease ended", ran, err)
	}
	if elapsed := time.Since(start); elapsed < time.Second {
		t.Errorf("the runner stopped the adapter after %v, before the lease of 1s ended (decision 0205)", elapsed)
	}
	if f.service.heartbeats < 2 {
		t.Errorf("the runner sent %d heartbeats, want it to go on after the first failed", f.service.heartbeats)
	}
	if len(f.service.reports) != 0 {
		t.Errorf("the runner reported %v after the lease ended", f.service.reports)
	}
	f.checkGone(t)
}

func TestALogBatchThatCannotBeSentGoesAgain(t *testing.T) {
	f := newFixture(t, "success")
	f.service.claim.LeaseTimeout = durationpb.New(time.Minute)
	f.service.leaseTimeout = time.Minute
	f.service.logFailures = 1
	report := f.runOne(t)
	if report.GetOutcome() != eventsv1.InvocationFinished_OUTCOME_SUCCEEDED {
		t.Errorf("the outcome is %v, want succeeded; the log is\n%s", report.GetOutcome(), f.service.log())
	}
	if log := f.service.log(); !strings.Contains(log, fakeAdapterPrefix+"pid ") || !strings.Contains(log, fakeAdapterPrefix+"invocation ") {
		t.Errorf("the log lost the batch that could not be sent; it is\n%s", log)
	}
}

func TestNoInvocationToClaim(t *testing.T) {
	f := newFixture(t, "success")
	f.service.claim = nil
	ran, err := RunOne(t.Context(), f.cfg)
	if ran || err != nil {
		t.Errorf("RunOne = %v, %v; want false, nil", ran, err)
	}
}

func TestRunRunsInvocationsUntilStopped(t *testing.T) {
	f := newFixture(t, "success")
	ctx, cancel := context.WithCancel(t.Context())
	errs := make(chan error, 1)
	go func() { errs <- Run(ctx, f.cfg) }()
	waitFor(t, func() bool {
		f.service.mu.Lock()
		defer f.service.mu.Unlock()
		return len(f.service.reports) > 0
	})
	cancel()
	if err := <-errs; !errors.Is(err, context.Canceled) {
		t.Errorf("Run = %v, want the context's error", err)
	}
}

func TestABadConfigIsRefused(t *testing.T) {
	for name, change := range map[string]func(*Config){
		"no service":         func(c *Config) { c.Service = nil },
		"a relative adapter": func(c *Config) { c.Adapter = "adapter" },
		"no repository":      func(c *Config) { c.Repository = "" },
		"no work directory":  func(c *Config) { c.WorkDir = "" },
		"no blob store":      func(c *Config) { c.Blobs = nil },
	} {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t, "success")
			change(&f.cfg)
			if ran, err := RunOne(t.Context(), f.cfg); err == nil {
				t.Errorf("RunOne = %v, nil; want an error", ran)
			}
		})
	}
}

// waitFor waits until done reports true, for at most ten seconds, and
// fails the test when it does not.
func waitFor(t *testing.T, done func() bool) {
	t.Helper()
	for deadline := time.Now().Add(10 * time.Second); !done(); {
		if time.Now().After(deadline) {
			t.Error("waited ten seconds in vain")
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// checkGone fails the test when the process whose id the fake adapter
// wrote still runs, or the process it started, when it started one.
func (f *fixture) checkGone(t *testing.T) {
	t.Helper()
	pids := openRoot(t, f.pidDir)
	if _, err := pids.Stat("pid"); err != nil {
		t.Fatalf("the adapter wrote no process id: %v; the log is\n%s", err, f.service.log())
	}
	for _, name := range []string{"pid", "child"} {
		pid, err := pids.ReadFile(name)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		if running(t, string(pid)) {
			t.Errorf("the process %s, whose id the fake adapter wrote to %s, still runs", pid, name)
		}
	}
}

// running reports whether the process pid still runs and has not been
// killed: it exists, is not a zombie, which a container's first process
// may never reap, is not exiting and has no SIGKILL pending. kill(2)
// returns before the process it kills acts on SIGKILL, so a process that
// the runner killed can still be in the state R for a while, with the
// signal pending; under load that took more than 100 ms.
func running(t *testing.T, pid string) bool {
	t.Helper()
	proc := openRoot(t, "/proc")
	stat, err := proc.ReadFile(pid + "/stat")
	if os.IsNotExist(err) {
		return false
	}
	if err != nil {
		t.Fatal(err)
	}
	// The state follows the command name, which is in parentheses, and the
	// flags are the seventh field from the state.
	_, rest, ok := strings.Cut(string(stat), ") ")
	fields := strings.Fields(rest)
	if !ok || len(fields) < 7 {
		t.Fatalf("/proc/%s/stat is %q", pid, stat)
	}
	flags, err := strconv.ParseUint(fields[6], 10, 64)
	if err != nil {
		t.Fatalf("the flags of /proc/%s/stat: %v", pid, err)
	}
	if fields[0] == "Z" || flags&pfExiting != 0 {
		return false
	}
	status, err := proc.ReadFile(pid + "/status")
	if os.IsNotExist(err) {
		return false
	}
	if err != nil {
		t.Fatal(err)
	}
	return !killPending(t, pid, string(status))
}

// pfExiting is the flag PF_EXITING of /proc/<pid>/stat: the process is
// exiting.
const pfExiting = 0x4

// killPending reports whether status, the content of /proc/<pid>/status,
// shows SIGKILL pending for the thread (SigPnd) or the process (ShdPnd).
// Each is a hexadecimal mask in which signal n is bit n-1.
func killPending(t *testing.T, pid, status string) bool {
	t.Helper()
	for line := range strings.Lines(status) {
		name, value, ok := strings.Cut(strings.TrimSpace(line), ":")
		if !ok || (name != "SigPnd" && name != "ShdPnd") {
			continue
		}
		mask, err := strconv.ParseUint(strings.TrimSpace(value), 16, 64)
		if err != nil {
			t.Fatalf("the %s of /proc/%s/status: %v", name, pid, err)
		}
		if mask&(1<<(uint(syscall.SIGKILL)-1)) != 0 {
			return true
		}
	}
	return false
}
