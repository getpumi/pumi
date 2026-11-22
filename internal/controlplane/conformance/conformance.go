// SPDX-License-Identifier: Apache-2.0
// SPDX-FileCopyrightText: The Pumi Authors

package conformance

import (
	"errors"
	"fmt"
	"slices"
	"sync/atomic"
	"testing"
	"time"

	"connectrpc.com/connect/v2"
	"connectrpc.com/connect/v2/connectproto"
	"google.golang.org/protobuf/proto"

	"getpumi.dev/pumi/api/events"
	cloudeventsv1 "getpumi.dev/pumi/api/gen/io/cloudevents/v1"
	controlplanev1 "getpumi.dev/pumi/api/gen/pumi/controlplane/v1"
	"getpumi.dev/pumi/api/gen/pumi/controlplane/v1/controlplanev1connect"
	eventsv1 "getpumi.dev/pumi/api/gen/pumi/events/v1"
	"getpumi.dev/pumi/api/gen/pumi/runner/v1/runnerv1connect"
)

// Caller is a kind of caller that the suite needs a client for (decision
// 0156).
type Caller string

// The callers of decision 0156 that call the control plane from outside it.
const (
	// Kernel is the kernel.
	Kernel Caller = "kernel"
	// Person is a person.
	Person Caller = "person"
	// Runner is a runner, which may call only RunnerService (decision
	// 0152).
	Runner Caller = "runner"
)

// Callers are the callers that the suite gets clients for.
var Callers = []Caller{Kernel, Person, Runner}

// Clients returns a client of the control plane under test that calls with
// the identity of a caller of the kind given. Every client of one Clients
// reaches the same control plane.
type Clients func(Caller) controlplanev1connect.ControlPlaneServiceClient

// Role is a role that a runner may run invocations in (decision 0024).
type Role string

// The roles of the invocation document.
const (
	// Gate is the gate role, the only one that may write verdicts.
	Gate Role = "gate"
	// Agent is the agent role.
	Agent Role = "agent"
)

// Plane is a control plane under test, reached only through its clients.
type Plane struct {
	// Clients gives the clients of ControlPlaneService.
	Clients Clients
	// Runner returns a client of RunnerService that calls as the runner
	// name, which takes invocations in the roles given. Each call with a
	// name returns a client with the same identity.
	Runner func(name string, roles ...Role) runnerv1connect.RunnerServiceClient
	// RunnerServiceAs returns a client of RunnerService that calls with the
	// identity of a caller that is not a runner.
	RunnerServiceAs func(Caller) runnerv1connect.RunnerServiceClient
	// Advance moves the control plane's clock forward by d and returns once
	// everything that was due by then, such as the expiry of a lease, has
	// been done. The control plane's time is injected, so the suite does
	// not sleep.
	Advance func(d time.Duration)
}

// Start starts a new control plane with an empty log for one test and
// returns it. It stops the control plane when the test ends. The control
// plane is wired with the core invariants and a lower policy set that
// permits every request, so that only the invariants and the validation of
// a request deny (decisions 0142 and 0143).
type Start func(t *testing.T) Plane

// Run runs the suite against the control planes that start starts. Each
// test starts its own.
func Run(t *testing.T, start Start) {
	t.Helper()
	tests := []struct {
		name string
		test func(t *testing.T, c Clients)
	}{
		{"AppendAssignsDensePositionsInOrder", appendAssignsDensePositionsInOrder},
		{"AppendWritesTenantAndPrevhash", appendWritesTenantAndPrevhash},
		{"AppendChecksSubjectVersions", appendChecksSubjectVersions},
		{"AppendConflictNamesEveryFailedSubject", appendConflictNamesEveryFailedSubject},
		{"BatchWhoseSecondConditionFailsWritesNothing", batchWhoseSecondConditionFailsWritesNothing},
		{"TwoAppendsRacingOnOneVersion", twoAppendsRacingOnOneVersion},
		{"AppendRefusesAnInvalidRequest", appendRefusesAnInvalidRequest},
		{"AppendFollowsTheTable", appendFollowsTheTable},
		{"BatchWithARefusedTypeWritesNothing", batchWithARefusedTypeWritesNothing},
		{"RunnerMayNotCallControlPlaneService", runnerMayNotCallControlPlaneService},
		{"QueryFilters", queryFilters},
		{"QueryReadsPages", queryReadsPages},
		{"SubscribeCatchesUpThenFollows", subscribeCatchesUpThenFollows},
		{"SubscribeResumesAfterAPosition", subscribeResumesAfterAPosition},
		{"SubscribeFilters", subscribeFilters},
		{"SubscribeCatchesUpWithALongLog", subscribeCatchesUpWithALongLog},
		{"DecisionIsRecordedWithItsBasis", decisionIsRecordedWithItsBasis},
		{"AMissingFactDenies", aMissingFactDenies},
		{"DecideRefusesARequestItCannotDecide", decideRefusesARequestItCannotDecide},
		{"AVerdictFromOutsideTheGateRoleIsRefused", aVerdictFromOutsideTheGateRoleIsRefused},
		{"APermissionIsDecidedAndRecordedBeforeItsEvent", aPermissionIsDecidedAndRecordedBeforeItsEvent},
		{"TheWorkflowActionsAreDecided", theWorkflowActionsAreDecided},
		{"AFreshlyAddedPlanMayMove", aFreshlyAddedPlanMayMove},
		{"APlanNeverAddedIsDenied", aPlanNeverAddedIsDenied},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			tt.test(t, start(t).Clients)
		})
	}
	for _, tt := range runnerTests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			tt.test(t, start(t))
		})
	}
}

// eventCount numbers the events the suite builds, so their ids differ.
var eventCount atomic.Int64

// nextID returns a new event id.
func nextID() string {
	return fmt.Sprintf("conformance-%d", eventCount.Add(1))
}

// planAdded returns a dev.getpumi.plan.added event about plan.
func planAdded(t *testing.T, plan string) *cloudeventsv1.CloudEvent {
	t.Helper()
	return build(t, &eventsv1.PlanAdded{Plan: plan, Track: "main"}, plan)
}

// transitionFired returns a dev.getpumi.transition.fired event about plan.
func transitionFired(t *testing.T, plan string) *cloudeventsv1.CloudEvent {
	t.Helper()
	return build(t, &eventsv1.TransitionFired{Plan: plan, FromState: "a", ToState: "b", Trigger: "t"}, plan)
}

// forgedTenant is the tenant that the suite's events carry. The control
// plane replaces it with the caller's tenant.
const forgedTenant = "forged-by-the-client"

// forgedPrevHash is the prevhash that the suite's events carry. The control
// plane replaces it.
const forgedPrevHash = "sha256:0000000000000000000000000000000000000000000000000000000000000000"

// build returns an event with data about subject, with a forged tenant and
// prevhash.
func build(t *testing.T, data proto.Message, subject string) *cloudeventsv1.CloudEvent {
	t.Helper()
	e, err := events.New(data, events.Attributes{
		ID: nextID(), Source: "/conformance", Subject: subject, Tenant: forgedTenant, PrevHash: forgedPrevHash,
	})
	if err != nil {
		t.Fatal(err)
	}
	return e
}

// mustAppend appends evs with conditions as caller and returns the
// positions.
func mustAppend(t *testing.T, c Clients, caller Caller, evs []*cloudeventsv1.CloudEvent,
	conditions ...*controlplanev1.Condition,
) []uint64 {
	t.Helper()
	res, err := c(caller).Append(t.Context(), &controlplanev1.AppendRequest{Events: evs, Conditions: conditions})
	if err != nil {
		t.Fatalf("Append as %s: %v", caller, err)
	}
	return res.GetPositions()
}

// query returns the positions and ids of the events that req returns, and
// the head.
func query(t *testing.T, c Clients, req *controlplanev1.QueryRequest) ([]uint64, uint64) {
	t.Helper()
	res, err := c(Kernel).Query(t.Context(), req)
	if err != nil {
		t.Fatalf("Query(%v): %v", req, err)
	}
	positions := []uint64{}
	for _, e := range res.GetEvents() {
		positions = append(positions, e.GetPosition())
	}
	return positions, res.GetHead()
}

// head returns the head of the log.
func head(t *testing.T, c Clients) uint64 {
	t.Helper()
	_, h := query(t, c, &controlplanev1.QueryRequest{})
	return h
}

// atVersion is a condition that subject is at version.
func atVersion(subject string, version uint64) *controlplanev1.Condition {
	return &controlplanev1.Condition{Subject: subject, Expected: &controlplanev1.Condition_Version{Version: version}}
}

// noEvents is a condition that subject has no events.
func noEvents(subject string) *controlplanev1.Condition {
	return &controlplanev1.Condition{
		Subject: subject, Expected: &controlplanev1.Condition_NoEvents_{NoEvents: &controlplanev1.Condition_NoEvents{}},
	}
}

// wantCode fails the test unless err is a Connect error with code.
func wantCode(t *testing.T, err error, code connect.Code) {
	t.Helper()
	if err == nil {
		t.Fatalf("the call succeeded, want the code %v", code)
	}
	if got := connect.CodeOf(err); got != code {
		t.Fatalf("the call failed with the code %v (%v), want %v", got, err, code)
	}
}

// conflict returns the AppendConflict detail of err, which must be an
// aborted Connect error, as the subjects and their current versions.
func conflict(t *testing.T, err error) map[string]uint64 {
	t.Helper()
	wantCode(t, err, connect.CodeAborted)
	var cerr *connect.Error
	if !errors.As(err, &cerr) {
		t.Fatalf("%v is not a Connect error", err)
	}
	for _, d := range cerr.Details() {
		msg, err := connectproto.UnmarshalErrorDetail(d)
		if err != nil {
			t.Fatalf("reading the error detail %s: %v", d.Type, err)
		}
		if c, ok := msg.(*controlplanev1.AppendConflict); ok {
			got := map[string]uint64{}
			for _, s := range c.GetSubjects() {
				got[s.GetSubject()] = s.GetCurrentVersion()
			}
			return got
		}
	}
	t.Fatalf("the error %v has no AppendConflict detail", err)
	return nil
}

// span returns the positions from first to last.
func span(first, last uint64) []uint64 {
	out := []uint64{}
	for p := first; p <= last; p++ {
		out = append(out, p)
	}
	return out
}

// equal fails the test unless got equals want.
func equal(t *testing.T, what string, got, want []uint64) {
	t.Helper()
	if !slices.Equal(got, want) {
		t.Fatalf("%s = %v, want %v", what, got, want)
	}
}
