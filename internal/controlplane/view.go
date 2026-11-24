// SPDX-License-Identifier: Apache-2.0
// SPDX-FileCopyrightText: The Pumi Authors

package controlplane

import (
	"context"
	"errors"

	"connectrpc.com/connect/v2"
	"google.golang.org/protobuf/proto"

	"getpumi.dev/pumi/api/events"
	controlplanev1 "getpumi.dev/pumi/api/gen/pumi/controlplane/v1"
	"getpumi.dev/pumi/api/gen/pumi/controlplane/v1/controlplanev1connect"
	eventsv1 "getpumi.dev/pumi/api/gen/pumi/events/v1"
	"getpumi.dev/pumi/internal/controlplane/blobstore"
)

// logChunkSize is the most output that one chunk of ReadLog holds, well
// under the message size that Connect reads by default.
const logChunkSize = 64 << 10

// viewHandler serves ViewService to the callers with one identity.
type viewHandler struct {
	controlplanev1connect.UnimplementedViewServiceHandler

	svc *Service
	id  Identity
}

// refuseRunner returns the error of every call of ViewService that a
// runner makes, because runners use RunnerService only (decision 0152),
// and nil for every other caller.
func (h *viewHandler) refuseRunner() error {
	if h.id.Caller == CallerRunner {
		return connect.NewError(connect.CodePermissionDenied,
			"a runner calls only RunnerService, not ViewService (decision 0152)")
	}
	return nil
}

// ReadLog sends the invocation's log in chunks: the recent output while it
// runs, or else the full log that its result stores in the blob store, or
// nothing when it has neither (decisions 0034 and 0232).
func (h *viewHandler) ReadLog(ctx context.Context, req *controlplanev1.ReadLogRequest,
	stream controlplanev1connect.ViewServiceReadLogServerStream,
) error {
	if err := h.refuseRunner(); err != nil {
		return err
	}
	inv := req.GetInvocation()
	doc, err := h.svc.document(inv)
	if err != nil {
		return logError(err)
	}
	if doc == nil {
		return connect.Errorf(connect.CodeNotFound, "the log has no invocation %q", inv)
	}
	if recent, running := h.svc.recentLog(inv); running {
		return sendLog(stream, controlplanev1.ReadLogResponse_SOURCE_RECENT, recent)
	}
	hash, err := h.svc.logBlobOf(inv)
	if err != nil || hash == "" {
		return err
	}
	if h.svc.cfg.Blobs == nil {
		return connect.Errorf(connect.CodeFailedPrecondition,
			"the invocation %s stores its log as %s, and the wiring gives the control plane no blob store", inv, hash)
	}
	content, err := h.svc.cfg.Blobs.Get(ctx, h.id.Tenant, hash)
	switch {
	case errors.Is(err, blobstore.ErrNotFound):
		return connect.Errorf(connect.CodeDataLoss, "the blob store does not hold %s, the log of the invocation %s", hash, inv)
	case err != nil:
		return connect.Errorf(connect.CodeInternal, "reading %s, the log of the invocation %s, from the blob store", hash, inv).WithCause(err)
	}
	return sendLog(stream, controlplanev1.ReadLogResponse_SOURCE_STORED, content)
}

// sendLog sends output in chunks of at most logChunkSize bytes, marked
// source, and one empty chunk when output is empty.
func sendLog(stream controlplanev1connect.ViewServiceReadLogServerStream, source controlplanev1.ReadLogResponse_Source, output []byte) error {
	for first := true; first || len(output) > 0; first = false {
		n := min(len(output), logChunkSize)
		if err := stream.Send(&controlplanev1.ReadLogResponse{Source: source, Output: output[:n]}); err != nil {
			return err
		}
		output = output[n:]
	}
	return nil
}

// logBlobOf returns the hash of the blob that holds the full log of
// invocation, from its invocation.result, or empty when it has none.
func (s *Service) logBlobOf(invocation string) (string, error) {
	found, _, err := s.log.Read(filter(&controlplanev1.Filter{Types: []string{events.TypeInvocationResult}}, 0, 0))
	if err != nil {
		return "", logError(err)
	}
	for _, e := range found {
		r := &eventsv1.InvocationResult{}
		if e.Event.GetProtoData().UnmarshalTo(r) == nil && r.GetInvocation() == invocation {
			return r.GetLogBlobHash(), nil
		}
	}
	return "", nil
}

// GetWorkflow returns the workflow that the wiring gives.
func (h *viewHandler) GetWorkflow(_ context.Context, _ *controlplanev1.GetWorkflowRequest) (*controlplanev1.GetWorkflowResponse, error) {
	if err := h.refuseRunner(); err != nil {
		return nil, err
	}
	if h.svc.cfg.Workflow == nil {
		return nil, connect.NewError(connect.CodeNotFound, "the wiring gives the control plane no workflow")
	}
	return &controlplanev1.GetWorkflowResponse{Workflow: proto.CloneOf(h.svc.cfg.Workflow)}, nil
}
