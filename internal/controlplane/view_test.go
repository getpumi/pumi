// SPDX-License-Identifier: Apache-2.0
// SPDX-FileCopyrightText: The Pumi Authors

package controlplane_test

import (
	"bytes"
	"errors"
	"io"
	"strings"
	"testing"

	"connectrpc.com/connect/v2"
	"connectrpc.com/connect/v2/connectinprocess"
	"google.golang.org/protobuf/proto"

	controlplanev1 "getpumi.dev/pumi/api/gen/pumi/controlplane/v1"
	"getpumi.dev/pumi/api/gen/pumi/controlplane/v1/controlplanev1connect"
	eventsv1 "getpumi.dev/pumi/api/gen/pumi/events/v1"
	runnerv1 "getpumi.dev/pumi/api/gen/pumi/runner/v1"
	"getpumi.dev/pumi/api/gen/pumi/runner/v1/runnerv1connect"
	workflowv1 "getpumi.dev/pumi/api/gen/pumi/workflow/v1"
	"getpumi.dev/pumi/internal/controlplane"
	"getpumi.dev/pumi/internal/controlplane/blobstore"
	"getpumi.dev/pumi/internal/controlplane/embeddedlog"
)

// viewPlane is a plane whose wiring gives the control plane a local blob
// store and a workflow, with a person's client of ViewService.
type viewPlane struct {
	plane

	blobs  *blobstore.Local
	person controlplanev1connect.ViewServiceClient
}

// testWorkflow is the workflow that the wiring of newViewPlane gives.
var testWorkflow = &workflowv1.Workflow{Name: "test", Version: "1", Initial: "implementing"}

// newViewPlane returns a viewPlane on a new log.
func newViewPlane(t *testing.T) viewPlane {
	t.Helper()
	log := openLog(t)
	blobs, err := blobstore.OpenLocal(log.Dir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = blobs.Close() })
	clock := newFakeClock()
	cfg := config()
	cfg.Clock = clock
	cfg.Blobs = blobs
	cfg.Workflow = testWorkflow
	svc, err := controlplane.New(log, cfg)
	if err != nil {
		t.Fatal(err)
	}
	return viewPlane{
		plane: plane{
			svc: svc, clock: clock,
			kernel: controlplanev1connect.NewControlPlaneServiceClient(viewDial(t, svc, controlplane.CallerKernel)),
			runner: runnerv1connect.NewRunnerServiceClient(viewDial(t, svc, controlplane.CallerRunner)),
		},
		blobs:  blobs,
		person: controlplanev1connect.NewViewServiceClient(viewDial(t, svc, controlplane.CallerPerson)),
	}
}

// viewDial returns an in-process client of svc's server for the identity
// of caller.
func viewDial(t *testing.T, svc *controlplane.Service, caller controlplane.Caller) *connect.Client {
	t.Helper()
	server, err := svc.Server(identity(caller))
	if err != nil {
		t.Fatal(err)
	}
	return connect.NewClient(connectinprocess.New(server))
}

// readLog calls ReadLog for invocation and returns the chunks' sources and
// their joined output.
func (p viewPlane) readLog(t *testing.T, invocation string) ([]controlplanev1.ReadLogResponse_Source, []byte, error) {
	t.Helper()
	stream, err := p.person.ReadLog(t.Context(), &controlplanev1.ReadLogRequest{Invocation: invocation})
	if err != nil {
		return nil, nil, err
	}
	var sources []controlplanev1.ReadLogResponse_Source
	var out []byte
	defer func() { _ = stream.Close() }()
	for {
		res, err := stream.Receive()
		if errors.Is(err, io.EOF) {
			return sources, out, nil
		}
		if err != nil {
			return sources, out, err
		}
		sources = append(sources, res.GetSource())
		out = append(out, res.GetOutput()...)
	}
}

// TestReadLogOfAnInvocationTheLogDoesNotHaveIsNotFound: ReadLog names an
// invocation of the log.
func TestReadLogOfAnInvocationTheLogDoesNotHaveIsNotFound(t *testing.T) {
	p := newViewPlane(t)
	_, _, err := p.readLog(t, "nope")
	if connect.CodeOf(err) != connect.CodeNotFound {
		t.Fatalf("ReadLog of an invocation the log does not have = %v, want the code not found", err)
	}
}

// TestReadLogSendsNothingBeforeTheInvocationHasALog: an invocation that is
// requested and not running has no log yet, and ReadLog sends no chunk.
func TestReadLogSendsNothingBeforeTheInvocationHasALog(t *testing.T) {
	p := newViewPlane(t)
	p.requestCheck(t, "invocation-1")
	sources, out, err := p.readLog(t, "invocation-1")
	if err != nil || len(sources) != 0 || len(out) != 0 {
		t.Fatalf("ReadLog of a requested invocation = %v, %q, %v; want no chunk", sources, out, err)
	}
}

// TestReadLogSendsTheRecentOutputOfARunningInvocation: while the
// invocation runs, ReadLog sends the output its runner sent with SendLog,
// marked recent, and a running invocation with no output yet sends one
// empty recent chunk.
func TestReadLogSendsTheRecentOutputOfARunningInvocation(t *testing.T) {
	p := newViewPlane(t)
	p.requestCheck(t, "invocation-1")
	res := p.claim(t)
	sources, out, err := p.readLog(t, "invocation-1")
	if err != nil || len(sources) != 1 || sources[0] != controlplanev1.ReadLogResponse_SOURCE_RECENT || len(out) != 0 {
		t.Fatalf("ReadLog of a running invocation with no output = %v, %q, %v; want one empty recent chunk", sources, out, err)
	}
	big := append(bytes.Repeat([]byte("x"), 200_000), '\n')
	for _, batch := range [][]byte{[]byte("first\n"), big} {
		if _, err := p.runner.SendLog(t.Context(), &runnerv1.SendLogRequest{
			Invocation: "invocation-1", LeaseNumber: res.GetLeaseNumber(), Output: batch,
		}); err != nil {
			t.Fatal(err)
		}
	}
	sources, out, err = p.readLog(t, "invocation-1")
	if err != nil {
		t.Fatal(err)
	}
	if want := append([]byte("first\n"), big...); !bytes.Equal(out, want) {
		t.Errorf("ReadLog sent %d bytes, want the %d bytes of both batches", len(out), len(want))
	}
	if len(sources) < 2 {
		t.Errorf("ReadLog sent %d bytes in %d chunks, want them split into chunks", len(out), len(sources))
	}
	for _, s := range sources {
		if s != controlplanev1.ReadLogResponse_SOURCE_RECENT {
			t.Errorf("a chunk is marked %v, want recent", s)
		}
	}
}

// TestReadLogSendsTheStoredLogOfAnInvocationThatReported: after the
// invocation reports, ReadLog sends the full log that its result stores in
// the blob store, marked stored.
func TestReadLogSendsTheStoredLogOfAnInvocationThatReported(t *testing.T) {
	p := newViewPlane(t)
	p.requestCheck(t, "invocation-1")
	res := p.claim(t)
	full := strings.Repeat("a line of the full log\n", 10_000)
	hash, err := p.blobs.Put(t.Context(), embeddedlog.Tenant, strings.NewReader(full))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.runner.Report(t.Context(), &runnerv1.ReportRequest{
		Invocation: "invocation-1", LeaseNumber: res.GetLeaseNumber(),
		Result:  &eventsv1.InvocationResult{Invocation: "invocation-1", LogBlobHash: hash},
		Outcome: eventsv1.InvocationFinished_OUTCOME_FAILED,
	}); err != nil {
		t.Fatal(err)
	}
	sources, out, err := p.readLog(t, "invocation-1")
	if err != nil {
		t.Fatal(err)
	}
	if string(out) != full {
		t.Errorf("ReadLog sent %d bytes, want the %d bytes of the stored log", len(out), len(full))
	}
	if len(sources) < 2 {
		t.Errorf("ReadLog sent the stored log in %d chunks, want it split into chunks", len(sources))
	}
	for _, s := range sources {
		if s != controlplanev1.ReadLogResponse_SOURCE_STORED {
			t.Errorf("a chunk is marked %v, want stored", s)
		}
	}
}

// TestReadLogOfAStoredLogTheBlobStoreDoesNotHoldFails: a result whose log
// is missing from the blob store is an error, not a log.
func TestReadLogOfAStoredLogTheBlobStoreDoesNotHoldFails(t *testing.T) {
	p := newViewPlane(t)
	p.requestCheck(t, "invocation-2")
	res := p.claim(t)
	missing := "sha256:" + strings.Repeat("0", 64)
	if _, err := p.runner.Report(t.Context(), &runnerv1.ReportRequest{
		Invocation: "invocation-2", LeaseNumber: res.GetLeaseNumber(),
		Result:  &eventsv1.InvocationResult{Invocation: "invocation-2", LogBlobHash: missing},
		Outcome: eventsv1.InvocationFinished_OUTCOME_FAILED,
	}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := p.readLog(t, "invocation-2"); err == nil || !strings.Contains(err.Error(), missing) {
		t.Errorf("ReadLog of a log the blob store does not hold = %v, want an error that names %s", err, missing)
	}
}

// TestGetWorkflowReturnsTheWorkflowOfTheWiring: GetWorkflow returns the
// workflow that the kernel runs, as the wiring gives it.
func TestGetWorkflowReturnsTheWorkflowOfTheWiring(t *testing.T) {
	p := newViewPlane(t)
	res, err := p.person.GetWorkflow(t.Context(), &controlplanev1.GetWorkflowRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if !proto.Equal(res.GetWorkflow(), testWorkflow) {
		t.Errorf("GetWorkflow = %v, want %v", res.GetWorkflow(), testWorkflow)
	}
}

// TestGetWorkflowWithNoWorkflowIsNotFound: a wiring that gives no workflow
// has none to return.
func TestGetWorkflowWithNoWorkflowIsNotFound(t *testing.T) {
	svc := newService(t)
	client := controlplanev1connect.NewViewServiceClient(viewDial(t, svc, controlplane.CallerPerson))
	_, err := client.GetWorkflow(t.Context(), &controlplanev1.GetWorkflowRequest{})
	if connect.CodeOf(err) != connect.CodeNotFound {
		t.Errorf("GetWorkflow with no workflow = %v, want the code not found", err)
	}
}

// TestViewServiceRefusesARunner: a runner calls only RunnerService
// (decision 0152).
func TestViewServiceRefusesARunner(t *testing.T) {
	p := newViewPlane(t)
	runner := controlplanev1connect.NewViewServiceClient(viewDial(t, p.svc, controlplane.CallerRunner))
	_, err := runner.GetWorkflow(t.Context(), &controlplanev1.GetWorkflowRequest{})
	if connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Errorf("a runner's GetWorkflow = %v, want the code permission denied", err)
	}
	sources, _, err := viewPlane{person: runner}.readLog(t, "invocation-1")
	if len(sources) != 0 {
		t.Error("a runner's ReadLog sent a chunk")
	}
	if connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Errorf("a runner's ReadLog = %v, want the code permission denied", err)
	}
}
