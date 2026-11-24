// SPDX-License-Identifier: Apache-2.0
// SPDX-FileCopyrightText: The Pumi Authors

package controlplane

import (
	"crypto/rand"
	"errors"
	"slices"
	"sync"
	"time"

	"connectrpc.com/connect/v2"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/durationpb"

	"getpumi.dev/pumi/api/events"
	cloudeventsv1 "getpumi.dev/pumi/api/gen/io/cloudevents/v1"
	controlplanev1 "getpumi.dev/pumi/api/gen/pumi/controlplane/v1"
	eventsv1 "getpumi.dev/pumi/api/gen/pumi/events/v1"
	runnerv1 "getpumi.dev/pumi/api/gen/pumi/runner/v1"
	"getpumi.dev/pumi/internal/controlplane/embeddedlog"
)

// recentLogSize is how many bytes of an invocation's latest log output
// the control plane keeps while the invocation runs (decision 0034).
const recentLogSize = 1 << 20

// runnerSource is the source attribute of the events that a runner's
// calls write about its invocation (decision 0133).
const runnerSource = "/runner"

// lease is what the control plane keeps in memory about a current lease:
// its number, its holder, its timeout and the timer of its expiry. The
// lease itself is in the log.
type lease struct {
	number   int64
	holder   string
	timeout  time.Duration
	deadline time.Time
	timer    Timer
}

// runs is the state of RunnerService beside the log: the invocation
// documents read from the log, the current leases and their timers, and
// the recent log output of running invocations.
type runs struct {
	clock        Clock
	leaseTimeout time.Duration
	claimWait    time.Duration

	// docsMu guards docs, open and docsAfter.
	docsMu sync.Mutex
	// docs are the invocation documents by invocation.
	docs map[string]*runnerv1.Invocation
	// open are the invocations that have not finished, in the order of
	// their requests.
	open []string
	// docsAfter is the position up to which the requests have been read.
	docsAfter uint64

	// leaseMu guards leases and output. The expiry of a lease holds it
	// while it appends lease.expired.
	leaseMu sync.Mutex
	leases  map[string]*lease
	output  map[string][]byte
}

// newRuns returns the state of RunnerService for the wiring cfg.
func newRuns(cfg Config) *runs {
	r := &runs{
		clock: cfg.Clock, leaseTimeout: cfg.LeaseTimeout, claimWait: cfg.ClaimWait,
		docs: map[string]*runnerv1.Invocation{}, leases: map[string]*lease{}, output: map[string][]byte{},
	}
	if r.clock == nil {
		r.clock = systemClock{}
	}
	if r.leaseTimeout <= 0 {
		r.leaseTimeout = DefaultLeaseTimeout
	}
	if r.claimWait <= 0 {
		r.claimWait = DefaultClaimWait
	}
	return r
}

// invocationState is what the log says about an invocation's leases: the
// version of its subject, its latest lease and whether that lease ended,
// whether the invocation finished, and the number of leases granted.
type invocationState struct {
	version  uint64
	grant    *eventsv1.LeaseGranted
	grants   int64
	ended    bool
	finished bool
	// started is the id of the latest invocation.started event.
	started string
}

// holds reports whether the latest lease of the state is number, held by
// holder, and neither ended nor followed by the invocation's end.
func (st invocationState) holds(number int64, holder string) bool {
	return st.grant != nil && st.grant.GetNumber() == number && st.grant.GetHolder() == holder && !st.ended && !st.finished
}

// claimable reports whether a runner may claim the invocation: it has not
// finished and has no current lease.
func (st invocationState) claimable() bool {
	return !st.finished && (st.grant == nil || st.ended)
}

// state reads the events about invocation, whose subject is the
// invocation, and returns its state.
func (s *Service) state(invocation string) (invocationState, error) {
	found, _, err := s.log.Read(embeddedlog.Filter{Subject: invocation})
	if err != nil {
		return invocationState{}, err
	}
	st := invocationState{version: uint64(len(found))}
	for _, e := range found {
		st.apply(e.Event)
	}
	return st, nil
}

// apply changes the state by the event e about the invocation.
func (st *invocationState) apply(e *cloudeventsv1.CloudEvent) {
	switch e.GetType() {
	case events.TypeLeaseGranted:
		g := &eventsv1.LeaseGranted{}
		if e.GetProtoData().UnmarshalTo(g) == nil {
			st.grant, st.ended = g, false
			st.grants++
		}
	case events.TypeLeaseExpired, events.TypeLeaseReleased:
		st.ended = true
	case events.TypeInvocationStarted:
		st.started = e.GetId()
	case events.TypeInvocationFinished:
		st.finished = true
	}
}

// refreshDocuments reads the invocation requests appended since the last
// read and keeps their documents. The first request of an invocation
// counts; the log refuses a request whose document does not match it
// (decision 0165).
func (s *Service) refreshDocuments() error {
	r := s.runs
	r.docsMu.Lock()
	defer r.docsMu.Unlock()
	found, head, err := s.log.Read(embeddedlog.Filter{Types: []string{events.TypeInvocationRequested}, After: r.docsAfter})
	if err != nil {
		return err
	}
	for _, e := range found {
		data := &eventsv1.InvocationRequested{}
		if e.Event.GetProtoData().UnmarshalTo(data) != nil {
			continue
		}
		doc, err := events.Document(data)
		if err != nil {
			continue
		}
		if _, seen := r.docs[doc.GetInvocation()]; seen {
			continue
		}
		r.docs[doc.GetInvocation()] = doc
		r.open = append(r.open, doc.GetInvocation())
	}
	r.docsAfter = max(r.docsAfter, head)
	return nil
}

// openInvocations returns the invocations that have not finished, in the
// order of their requests, with their documents.
func (s *Service) openInvocations() ([]*runnerv1.Invocation, error) {
	if err := s.refreshDocuments(); err != nil {
		return nil, err
	}
	r := s.runs
	r.docsMu.Lock()
	defer r.docsMu.Unlock()
	out := make([]*runnerv1.Invocation, 0, len(r.open))
	for _, inv := range r.open {
		out = append(out, r.docs[inv])
	}
	return out, nil
}

// closeInvocation drops a finished invocation from the open ones.
func (s *Service) closeInvocation(invocation string) {
	r := s.runs
	r.docsMu.Lock()
	defer r.docsMu.Unlock()
	r.open = slices.DeleteFunc(r.open, func(inv string) bool { return inv == invocation })
}

// document returns the document of invocation, or nil when the log has no
// request of it.
func (s *Service) document(invocation string) (*runnerv1.Invocation, error) {
	if err := s.refreshDocuments(); err != nil {
		return nil, err
	}
	r := s.runs
	r.docsMu.Lock()
	defer r.docsMu.Unlock()
	return r.docs[invocation], nil
}

// newEventID returns a new id, unique within its source, for an event that
// the control plane builds.
func newEventID(prefix string) string {
	return prefix + "-" + rand.Text()
}

// event builds the envelope of an event that the control plane writes
// about subject, with the source src and the cause cause, which may be
// empty.
func event(data proto.Message, src, subject, cause string) (*cloudeventsv1.CloudEvent, error) {
	return events.New(data, events.Attributes{
		ID: newEventID("event"), Source: src, Subject: subject, Tenant: embeddedlog.Tenant, Cause: cause,
	})
}

// atVersion returns the condition that subject is at version, or has no
// events when version is 0 (decision 0153).
func atVersion(subject string, version uint64) *controlplanev1.Condition {
	if version == 0 {
		return &controlplanev1.Condition{
			Subject: subject, Expected: &controlplanev1.Condition_NoEvents_{NoEvents: &controlplanev1.Condition_NoEvents{}},
		}
	}
	return &controlplanev1.Condition{Subject: subject, Expected: &controlplanev1.Condition_Version{Version: version}}
}

// isConflict reports whether err is the error of an append whose
// conditions did not hold.
func isConflict(err error) bool {
	var conflict *conflictError
	return errors.As(err, &conflict)
}

// grant appends lease.granted and invocation.started for doc's invocation,
// held by holder, when the invocation is still at the state st, and starts
// the lease's timer. It returns false, with no error, when the append's
// condition failed because the invocation changed.
func (s *Service) grant(doc *runnerv1.Invocation, holder string, st invocationState) (*runnerv1.ClaimResponse, bool, error) {
	inv := doc.GetInvocation()
	number := st.grants + 1
	timeout := s.runs.leaseTimeout
	granted, err := event(&eventsv1.LeaseGranted{
		Lease: inv, Holder: holder, Number: number, Timeout: durationpb.New(timeout),
	}, source, inv, "")
	if err != nil {
		return nil, false, err
	}
	started, err := event(&eventsv1.InvocationStarted{Invocation: inv, Runner: holder, LeaseNumber: number},
		runnerSource, inv, granted.GetId())
	if err != nil {
		return nil, false, err
	}
	condition := []*controlplanev1.Condition{atVersion(inv, st.version)}
	_, err = s.log.Append([]*cloudeventsv1.CloudEvent{granted, started},
		func(version func(string) uint64) error { return conflicts(condition, version) })
	if isConflict(err) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	s.startLease(inv, &lease{number: number, holder: holder, timeout: timeout})
	return &runnerv1.ClaimResponse{Invocation: doc, LeaseNumber: number, LeaseTimeout: durationpb.New(timeout)}, true, nil
}

// startLease keeps l as the current lease of invocation and sets the timer
// of its expiry.
func (s *Service) startLease(invocation string, l *lease) {
	r := s.runs
	r.leaseMu.Lock()
	defer r.leaseMu.Unlock()
	if old, ok := r.leases[invocation]; ok {
		old.timer.Stop()
	}
	l.deadline = r.clock.Now().Add(l.timeout)
	number := l.number
	l.timer = r.clock.AfterFunc(l.timeout, func() { s.expire(invocation, number) })
	r.leases[invocation] = l
	delete(r.output, invocation)
}

// staleLease is the error of a call under a lease that is not current.
func staleLease(invocation string, number int64) error {
	return connect.Errorf(connect.CodeAborted,
		"lease %d of the invocation %q is not the current lease of this runner; stop the invocation (decision 0035)",
		number, invocation)
}

// current checks that holder holds lease number of invocation, in the log
// and in time, and returns the invocation's state. A lease that is not
// current is aborted.
func (s *Service) current(invocation string, number int64, holder string) (invocationState, error) {
	if invocation == "" || number <= 0 {
		return invocationState{}, connect.NewError(connect.CodeInvalidArgument,
			"the call names no invocation or no lease number from Claim")
	}
	st, err := s.state(invocation)
	if err != nil {
		return invocationState{}, logError(err)
	}
	if !st.holds(number, holder) {
		return invocationState{}, staleLease(invocation, number)
	}
	r := s.runs
	r.leaseMu.Lock()
	defer r.leaseMu.Unlock()
	if l := r.leases[invocation]; l == nil || l.number != number || !r.clock.Now().Before(l.deadline) {
		return invocationState{}, staleLease(invocation, number)
	}
	return st, nil
}

// renew gives lease number of invocation its full timeout again, and
// returns the timeout. A lease that is no longer current is aborted.
func (s *Service) renew(invocation string, number int64) (time.Duration, error) {
	r := s.runs
	r.leaseMu.Lock()
	defer r.leaseMu.Unlock()
	l := r.leases[invocation]
	if l == nil || l.number != number || !r.clock.Now().Before(l.deadline) {
		return 0, staleLease(invocation, number)
	}
	l.timer.Stop()
	l.deadline = r.clock.Now().Add(l.timeout)
	l.timer = r.clock.AfterFunc(l.timeout, func() { s.expire(invocation, number) })
	return l.timeout, nil
}

// endLease drops lease number of invocation, after the report that ended
// it, with its timer and its recent output.
func (s *Service) endLease(invocation string, number int64) {
	r := s.runs
	r.leaseMu.Lock()
	defer r.leaseMu.Unlock()
	if l := r.leases[invocation]; l != nil && l.number == number {
		l.timer.Stop()
		delete(r.leases, invocation)
		delete(r.output, invocation)
	}
}

// expire appends lease.expired for lease number of invocation when it is
// still current and its deadline has passed, and drops it. Its timer calls
// it. An error of the log leaves the lease to the next start of the
// control plane, which sets its timer again.
func (s *Service) expire(invocation string, number int64) {
	r := s.runs
	r.leaseMu.Lock()
	defer r.leaseMu.Unlock()
	l := r.leases[invocation]
	if l == nil || l.number != number || r.clock.Now().Before(l.deadline) {
		return
	}
	for {
		st, err := s.state(invocation)
		if err != nil || !st.holds(number, l.holder) {
			break
		}
		e, err := event(&eventsv1.LeaseExpired{Lease: invocation, Number: number}, source, invocation, "")
		if err != nil {
			break
		}
		condition := []*controlplanev1.Condition{atVersion(invocation, st.version)}
		_, err = s.log.Append([]*cloudeventsv1.CloudEvent{e},
			func(version func(string) uint64) error { return conflicts(condition, version) })
		if !isConflict(err) {
			break
		}
	}
	delete(r.leases, invocation)
	delete(r.output, invocation)
}

// restoreLeases sets the timers of the leases that the log holds as
// current when the control plane starts, each with its full timeout from
// now, because the times of the last heartbeats are not kept.
func (s *Service) restoreLeases() error {
	open, err := s.openInvocations()
	if err != nil {
		return err
	}
	for _, doc := range open {
		st, err := s.state(doc.GetInvocation())
		if err != nil {
			return err
		}
		if st.grant != nil && !st.ended && !st.finished {
			s.startLease(doc.GetInvocation(), &lease{
				number: st.grant.GetNumber(), holder: st.grant.GetHolder(), timeout: st.grant.GetTimeout().AsDuration(),
			})
		}
	}
	return nil
}

// keepOutput adds output to the recent log output of invocation, keeping
// the last recentLogSize bytes.
func (s *Service) keepOutput(invocation string, number int64, output []byte) error {
	r := s.runs
	r.leaseMu.Lock()
	defer r.leaseMu.Unlock()
	if l := r.leases[invocation]; l == nil || l.number != number {
		return staleLease(invocation, number)
	}
	kept := append(r.output[invocation], output...)
	if len(kept) > recentLogSize {
		kept = slices.Clone(kept[len(kept)-recentLogSize:])
	}
	r.output[invocation] = kept
	return nil
}

// recentLog returns a copy of the recent log output of invocation and
// true while it holds a lease, or nil and false when it is not running.
func (s *Service) recentLog(invocation string) ([]byte, bool) {
	r := s.runs
	r.leaseMu.Lock()
	defer r.leaseMu.Unlock()
	if r.leases[invocation] == nil {
		return nil, false
	}
	return slices.Clone(r.output[invocation]), true
}

// RecentLog returns a copy of the recent log output of a running
// invocation, the last bytes that its runner sent with SendLog, or nil when
// the invocation is not running. The output is not in the event log
// (decision 0034).
func (s *Service) RecentLog(invocation string) []byte {
	r := s.runs
	r.leaseMu.Lock()
	defer r.leaseMu.Unlock()
	return slices.Clone(r.output[invocation])
}
