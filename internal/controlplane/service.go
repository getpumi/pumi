// SPDX-License-Identifier: Apache-2.0
// SPDX-FileCopyrightText: The Pumi Authors

package controlplane

import (
	"context"
	"errors"
	"fmt"

	"connectrpc.com/connect/v2"
	"connectrpc.com/connect/v2/connectproto"
	"google.golang.org/protobuf/proto"

	"getpumi.dev/pumi/api/events"
	cloudeventsv1 "getpumi.dev/pumi/api/gen/io/cloudevents/v1"
	controlplanev1 "getpumi.dev/pumi/api/gen/pumi/controlplane/v1"
	"getpumi.dev/pumi/api/gen/pumi/controlplane/v1/controlplanev1connect"
	"getpumi.dev/pumi/internal/controlplane/embeddedlog"
)

// Caller is the kind of a caller of the control plane from outside it
// (decision 0156).
type Caller string

// The callers of decision 0156 that reach the control plane through its
// services. The control plane's own appends do not go through them.
const (
	// CallerKernel is the kernel.
	CallerKernel Caller = "kernel"
	// CallerPerson is a person.
	CallerPerson Caller = "person"
	// CallerRunner is a runner, which may call only RunnerService
	// (decision 0152).
	CallerRunner Caller = "runner"
)

// appenderOf gives the appender of the table of decision 0156 that each
// caller appends as through Append. A runner has none, because it appends
// only through RunnerService.
var appenderOf = map[Caller]events.Appender{
	CallerKernel: events.AppenderKernel,
	CallerPerson: events.AppenderPerson,
}

// Identity is who a client calls as. The wiring gives each client a fixed
// identity, and the control plane takes the caller and the tenant only from
// it, never from a request (decision 0156).
type Identity struct {
	// Caller is the kind of caller.
	Caller Caller
	// Tenant is the caller's tenant (decision 0039).
	Tenant string
}

// Service is the embedded control plane on one event log.
type Service struct {
	log *embeddedlog.Log
}

// New returns the embedded control plane on log. The caller closes log.
func New(log *embeddedlog.Log) *Service {
	return &Service{log: log}
}

// Server returns a Connect server that serves ControlPlaneService to a
// client with the identity id. The wiring gives each client its own
// server, built with that client's identity, so a client cannot call as
// anyone else. It refuses a caller that it does not know and a tenant other
// than the embedded log's.
func (s *Service) Server(id Identity) (*connect.Server, error) {
	switch id.Caller {
	case CallerKernel, CallerPerson, CallerRunner:
	default:
		return nil, fmt.Errorf("the control plane has no caller %q", id.Caller)
	}
	if id.Tenant != embeddedlog.Tenant {
		return nil, fmt.Errorf("the embedded control plane serves only the tenant %q, not %q", embeddedlog.Tenant, id.Tenant)
	}
	server := connect.NewServer()
	controlplanev1connect.RegisterControlPlaneServiceHandler(server, &handler{log: s.log, id: id})
	return server, nil
}

// handler serves ControlPlaneService to the callers with one identity.
// Decide is built with the projection of facts (decision 0155).
type handler struct {
	controlplanev1connect.UnimplementedControlPlaneServiceHandler

	log *embeddedlog.Log
	id  Identity
}

// refuseRunner returns the error of every verb of ControlPlaneService that
// a runner calls, because runners use RunnerService only (decision 0152),
// and nil for every other caller.
func (h *handler) refuseRunner() error {
	if h.id.Caller == CallerRunner {
		return connect.NewError(connect.CodePermissionDenied,
			"a runner calls only RunnerService, not ControlPlaneService (decision 0152)")
	}
	return nil
}

// Append writes the events, all or none, when every condition holds
// (decision 0153), and when the table of decision 0156 lets the caller
// append each event's type.
func (h *handler) Append(_ context.Context, req *controlplanev1.AppendRequest) (*controlplanev1.AppendResponse, error) {
	if err := h.refuseRunner(); err != nil {
		return nil, err
	}
	evs, err := h.checkEvents(req.GetEvents())
	if err != nil {
		return nil, err
	}
	if err := checkConditions(req.GetConditions()); err != nil {
		return nil, err
	}
	positions, err := h.log.Append(evs, func(version func(string) uint64) error {
		return conflicts(req.GetConditions(), version)
	})
	if err != nil {
		var conflict *conflictError
		if errors.As(err, &conflict) {
			return nil, conflict.connectError()
		}
		return nil, logError(err)
	}
	return &controlplanev1.AppendResponse{Positions: positions}, nil
}

// checkEvents checks the events of an append and returns copies of them
// with the caller's tenant. It refuses an append with no events or with an
// event that is not a valid Pumi event, with invalid argument, and an event
// whose type the caller may not append, with permission denied. The log
// writes prevhash.
func (h *handler) checkEvents(evs []*cloudeventsv1.CloudEvent) ([]*cloudeventsv1.CloudEvent, error) {
	if len(evs) == 0 {
		return nil, connect.NewError(connect.CodeInvalidArgument, "an append needs at least one event")
	}
	out := make([]*cloudeventsv1.CloudEvent, 0, len(evs))
	for i, original := range evs {
		if original == nil {
			return nil, connect.Errorf(connect.CodeInvalidArgument, "event %d of the append is empty", i+1)
		}
		e, ok := proto.Clone(original).(*cloudeventsv1.CloudEvent)
		if !ok {
			return nil, connect.Errorf(connect.CodeInvalidArgument, "event %d of the append is not a CloudEvent", i+1)
		}
		if e.Attributes == nil {
			e.Attributes = map[string]*cloudeventsv1.CloudEvent_CloudEventAttributeValue{}
		}
		e.Attributes[events.ExtensionTenant] = &cloudeventsv1.CloudEvent_CloudEventAttributeValue{
			Attr: &cloudeventsv1.CloudEvent_CloudEventAttributeValue_CeString{CeString: h.id.Tenant},
		}
		delete(e.Attributes, events.ExtensionPrevHash)
		if err := events.Validate(e); err != nil {
			return nil, connect.Errorf(connect.CodeInvalidArgument, "event %d of the append: %v", i+1, err)
		}
		if a, ok := appenderOf[h.id.Caller]; !ok || !events.MayAppend(e.GetType(), a) {
			return nil, connect.Errorf(connect.CodePermissionDenied,
				"event %d of the append: a %s may not append %s (decision 0156)", i+1, h.id.Caller, e.GetType())
		}
		out = append(out, e)
	}
	return out, nil
}

// checkConditions refuses, with invalid argument, a condition with no
// subject, with no expected version or at version 0.
func checkConditions(conditions []*controlplanev1.Condition) error {
	for i, c := range conditions {
		if c.GetSubject() == "" {
			return connect.Errorf(connect.CodeInvalidArgument, "condition %d of the append has no subject", i+1)
		}
		switch expected := c.GetExpected().(type) {
		case *controlplanev1.Condition_NoEvents_:
		case *controlplanev1.Condition_Version:
			if expected.Version == 0 {
				return connect.Errorf(connect.CodeInvalidArgument,
					"condition %d of the append expects version 0 of %s; a subject with no events has the condition no_events",
					i+1, c.GetSubject())
			}
		default:
			return connect.Errorf(connect.CodeInvalidArgument,
				"condition %d of the append says nothing about what %s is expected to be at", i+1, c.GetSubject())
		}
	}
	return nil
}

// conflictError is the error of an append whose conditions did not all
// hold. It holds each failed subject's current version, in the order of the
// conditions.
type conflictError struct {
	subjects []*controlplanev1.AppendConflict_Subject
}

func (e *conflictError) Error() string {
	return fmt.Sprintf("%d conditions of the append failed", len(e.subjects))
}

// connectError returns the Connect error of e: the code aborted, with the
// AppendConflict detail.
func (e *conflictError) connectError() error {
	err := connect.Errorf(connect.CodeAborted,
		"the append's conditions did not all hold; nothing was written (decision 0153)")
	detail, derr := connectproto.NewErrorDetail(&controlplanev1.AppendConflict{Subjects: e.subjects})
	if derr != nil {
		return connect.NewError(connect.CodeInternal, "the append's conflict could not be reported").WithCause(derr)
	}
	return err.WithDetail(detail)
}

// conflicts returns a *conflictError with every subject whose condition
// does not hold when version gives the subjects' versions, or nil when all
// hold. A subject named by more than one failed condition is listed once.
func conflicts(conditions []*controlplanev1.Condition, version func(string) uint64) error {
	var failed []*controlplanev1.AppendConflict_Subject
	listed := map[string]bool{}
	for _, c := range conditions {
		current := version(c.GetSubject())
		want := uint64(0)
		if v, ok := c.GetExpected().(*controlplanev1.Condition_Version); ok {
			want = v.Version
		}
		if current == want || listed[c.GetSubject()] {
			continue
		}
		listed[c.GetSubject()] = true
		failed = append(failed, &controlplanev1.AppendConflict_Subject{Subject: c.GetSubject(), CurrentVersion: current})
	}
	if len(failed) > 0 {
		return &conflictError{subjects: failed}
	}
	return nil
}

// logError returns the Connect error of an error of the log. Its text stays
// on the server, as the cause.
func logError(err error) error {
	if errors.Is(err, embeddedlog.ErrClosed) {
		return connect.NewError(connect.CodeUnavailable, "the event log is closed").WithCause(err)
	}
	return connect.NewError(connect.CodeInternal, "the event log failed").WithCause(err)
}

// filter returns the log's filter for f, after and limit. The tenant is the
// caller's, and the embedded log holds only that tenant (decision 0154).
func filter(f *controlplanev1.Filter, after uint64, limit int) embeddedlog.Filter {
	return embeddedlog.Filter{Subject: f.GetSubject(), Types: f.GetTypes(), After: after, Limit: limit}
}

// entries returns the entries of the log as the API's.
func entries(in []embeddedlog.Entry) []*controlplanev1.Entry {
	out := make([]*controlplanev1.Entry, 0, len(in))
	for _, e := range in {
		out = append(out, &controlplanev1.Entry{Position: e.Position, Event: e.Event})
	}
	return out
}

// Query returns the events that match the filter, in log order, and the
// head (decision 0154).
func (h *handler) Query(_ context.Context, req *controlplanev1.QueryRequest) (*controlplanev1.QueryResponse, error) {
	if err := h.refuseRunner(); err != nil {
		return nil, err
	}
	found, head, err := h.log.Read(filter(req.GetFilter(), req.GetAfter(), int(req.GetLimit())))
	if err != nil {
		return nil, logError(err)
	}
	return &controlplanev1.QueryResponse{Events: entries(found), Head: head}, nil
}

// subscribePage is the most events Subscribe reads from the log at once.
const subscribePage = 256

// Subscribe sends the events that match the filter after the position
// after: those in the log, then new ones as they are appended, in log order
// (decision 0154). It keeps no state beyond the stream and ends when the
// client's context is done or the log closes.
func (h *handler) Subscribe(ctx context.Context, req *controlplanev1.SubscribeRequest,
	stream controlplanev1connect.ControlPlaneServiceSubscribeServerStream,
) error {
	if err := h.refuseRunner(); err != nil {
		return err
	}
	// Over HTTP, the client's call returns when the headers arrive, so they
	// are sent before there may be an event to send.
	if err := stream.SendHeaders(); err != nil {
		return err
	}
	after := req.GetAfter()
	for {
		var err error
		if after, err = h.send(req.GetFilter(), after, stream); err != nil {
			return err
		}
		if err := h.log.Wait(ctx, after); err != nil {
			if ctx.Err() != nil {
				return connect.NewError(connect.CodeCanceled, "the subscription ended").WithCause(err)
			}
			return logError(err)
		}
	}
}

// send sends the events in the log that match f after the position after,
// in pages, and returns the position up to which it has sent every
// matching event: the head when it read the last page, or after when that
// is later.
func (h *handler) send(f *controlplanev1.Filter, after uint64,
	stream controlplanev1connect.ControlPlaneServiceSubscribeServerStream,
) (uint64, error) {
	for {
		found, head, err := h.log.Read(filter(f, after, subscribePage))
		if err != nil {
			return 0, logError(err)
		}
		for _, e := range entries(found) {
			if err := stream.Send(&controlplanev1.SubscribeResponse{Entry: e}); err != nil {
				return 0, err
			}
		}
		if len(found) < subscribePage {
			return max(after, head), nil
		}
		after = found[len(found)-1].Position
	}
}
