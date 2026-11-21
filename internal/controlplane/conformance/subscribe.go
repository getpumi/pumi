// SPDX-License-Identifier: Apache-2.0
// SPDX-FileCopyrightText: The Pumi Authors

package conformance

import (
	"context"
	"testing"
	"time"

	cloudeventsv1 "getpumi.dev/pumi/api/gen/io/cloudevents/v1"
	controlplanev1 "getpumi.dev/pumi/api/gen/pumi/controlplane/v1"
)

// receiveTimeout is how long the suite waits for an event that a stream
// must deliver.
const receiveTimeout = 10 * time.Second

// quietTime is how long the suite watches a stream that must deliver
// nothing yet.
const quietTime = 100 * time.Millisecond

// subscription is an open Subscribe stream whose messages a goroutine
// receives.
type subscription struct {
	entries chan *controlplanev1.Entry
	err     chan error
	stop    func()
}

// subscribe opens a Subscribe stream as caller and stops it when the test
// ends.
func subscribe(t *testing.T, c Clients, caller Caller, req *controlplanev1.SubscribeRequest) *subscription {
	t.Helper()
	ctx, cancel := context.WithCancel(t.Context())
	stream, err := c(caller).Subscribe(ctx, req)
	if err != nil {
		cancel()
		t.Fatalf("Subscribe(%v) as %s: %v", req, caller, err)
	}
	s := &subscription{entries: make(chan *controlplanev1.Entry), err: make(chan error, 1)}
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			res, err := stream.Receive()
			if err != nil {
				s.err <- err
				return
			}
			select {
			case s.entries <- res.GetEntry():
			case <-ctx.Done():
				return
			}
		}
	}()
	s.stop = func() {
		cancel()
		<-done
		_ = stream.Close()
	}
	t.Cleanup(s.stop)
	return s
}

// next returns the next entry of s, and fails the test if none arrives in
// time or the stream ends.
func (s *subscription) next(t *testing.T) *controlplanev1.Entry {
	t.Helper()
	select {
	case e := <-s.entries:
		return e
	case err := <-s.err:
		t.Fatalf("the stream ended: %v", err)
	case <-time.After(receiveTimeout):
		t.Fatalf("no event arrived in %v", receiveTimeout)
	}
	return nil
}

// receive returns the positions of the next n entries of s, and checks
// that each is the event that was written at its position.
func (s *subscription) receive(t *testing.T, n int, written map[uint64]string) []uint64 {
	t.Helper()
	out := make([]uint64, 0, n)
	for range n {
		e := s.next(t)
		if id, ok := written[e.GetPosition()]; !ok || id != e.GetEvent().GetId() {
			t.Fatalf("the stream sent %s at %d, which is not the event written there", e.GetEvent().GetId(), e.GetPosition())
		}
		out = append(out, e.GetPosition())
	}
	return out
}

// quiet fails the test if s delivers an entry or ends within quietTime.
func (s *subscription) quiet(t *testing.T) {
	t.Helper()
	select {
	case e := <-s.entries:
		t.Fatalf("the stream sent the event at %d, want nothing yet", e.GetPosition())
	case err := <-s.err:
		t.Fatalf("the stream ended: %v", err)
	case <-time.After(quietTime):
	}
}

// log appends evs as caller and records the id written at each position
// in written.
func log(t *testing.T, c Clients, caller Caller, written map[uint64]string, evs ...*cloudeventsv1.CloudEvent) {
	t.Helper()
	positions := mustAppend(t, c, caller, evs)
	if len(positions) != len(evs) {
		t.Fatalf("an append of %d events returned %d positions", len(evs), len(positions))
	}
	for i, e := range evs {
		written[positions[i]] = e.GetId()
	}
}

// subscribeCatchesUpThenFollows: Subscribe sends the matching events in
// the log, then new ones as they are appended (decision 0154).
func subscribeCatchesUpThenFollows(t *testing.T, c Clients) {
	written := map[uint64]string{}
	log(t, c, Kernel, written, planAdded(t, "a"), transitionFired(t, "a"), planAdded(t, "b"))
	s := subscribe(t, c, Kernel, &controlplanev1.SubscribeRequest{})
	equal(t, "the events the stream caught up with", s.receive(t, 3, written), []uint64{1, 2, 3})
	s.quiet(t)
	log(t, c, Kernel, written, transitionFired(t, "b"))
	equal(t, "the first new event", s.receive(t, 1, written), []uint64{4})
	log(t, c, Person, written, approvalRecorded(t, "a"), approvalRecorded(t, "b"))
	equal(t, "the next new events", s.receive(t, 2, written), []uint64{5, 6})
	s.quiet(t)

	empty := subscribe(t, c, Person, &controlplanev1.SubscribeRequest{After: 6})
	empty.quiet(t)
	log(t, c, Kernel, written, transitionFired(t, "a"))
	equal(t, "the event after the head", empty.receive(t, 1, written), []uint64{7})
	equal(t, "the event on the first stream", s.receive(t, 1, written), []uint64{7})
}

// subscribeResumesAfterAPosition: a client whose stream ended calls
// Subscribe again with the last position it handled, and gets the events
// after it (decision 0154).
func subscribeResumesAfterAPosition(t *testing.T, c Clients) {
	written := map[uint64]string{}
	log(t, c, Kernel, written, planAdded(t, "a"), planAdded(t, "b"), planAdded(t, "c"), planAdded(t, "d"), planAdded(t, "e"))
	first := subscribe(t, c, Kernel, &controlplanev1.SubscribeRequest{})
	equal(t, "the events before the stream ended", first.receive(t, 2, written), []uint64{1, 2})
	first.stop()

	log(t, c, Kernel, written, transitionFired(t, "a"))
	again := subscribe(t, c, Kernel, &controlplanev1.SubscribeRequest{After: 2})
	equal(t, "the events after resuming", again.receive(t, 4, written), []uint64{3, 4, 5, 6})
	again.quiet(t)
	log(t, c, Kernel, written, transitionFired(t, "b"))
	equal(t, "the new event after resuming", again.receive(t, 1, written), []uint64{7})
}

// subscribeFilters: Subscribe takes the filters of Query, for the events
// in the log and for new ones.
func subscribeFilters(t *testing.T, c Clients) {
	mixedLog(t, c)
	res, err := c(Kernel).Query(t.Context(), &controlplanev1.QueryRequest{})
	if err != nil {
		t.Fatal(err)
	}
	written := map[uint64]string{}
	for _, e := range res.GetEvents() {
		written[e.GetPosition()] = e.GetEvent().GetId()
	}
	streams := []*subscription{}
	cases := filterCases()
	for _, tt := range cases {
		s := subscribe(t, c, Kernel, &controlplanev1.SubscribeRequest{Filter: tt.req.GetFilter(), After: tt.req.GetAfter()})
		equal(t, "Subscribe with "+tt.name, s.receive(t, len(tt.wants), written), tt.wants)
		streams = append(streams, s)
	}
	for _, s := range streams {
		s.quiet(t)
	}
	// 8 transition.fired b, 9 transition.fired a.
	log(t, c, Kernel, written, transitionFired(t, "b"), transitionFired(t, "a"))
	follow := map[string][]uint64{
		"no filter": {8, 9}, "a subject": {9}, "one type": {8, 9}, "two types": {},
		"a subject and a type": {9}, "after": {8, 9}, "a subject after": {9}, "a type after": {8, 9},
		"a subject with no events": {}, "a type with no events": {}, "after the head": {8, 9},
	}
	for i, tt := range cases {
		want, ok := follow[tt.name]
		if !ok {
			t.Fatalf("no new events are given for the filter %s", tt.name)
		}
		equal(t, "the new events of Subscribe with "+tt.name, streams[i].receive(t, len(want), written), want)
	}
	for _, s := range streams {
		s.quiet(t)
	}
}

// longLog is the number of events of the long log that Subscribe catches
// up with, more than a control plane is likely to read from its store at
// once.
const longLog = 600

// subscribeCatchesUpWithALongLog: Subscribe sends every event of a long
// log in order, then follows.
func subscribeCatchesUpWithALongLog(t *testing.T, c Clients) {
	written := map[uint64]string{}
	for range longLog / 100 {
		batch := make([]*cloudeventsv1.CloudEvent, 0, 100)
		for range 100 {
			batch = append(batch, transitionFired(t, "a"))
		}
		log(t, c, Kernel, written, batch...)
	}
	s := subscribe(t, c, Kernel, &controlplanev1.SubscribeRequest{})
	equal(t, "the events of the long log", s.receive(t, longLog, written), span(1, longLog))
	odd := subscribe(t, c, Person, &controlplanev1.SubscribeRequest{Filter: &controlplanev1.Filter{Subject: "a"}, After: 1})
	equal(t, "the events of the long log after 1", odd.receive(t, longLog-1, written), span(2, longLog))
	log(t, c, Kernel, written, planAdded(t, "b"))
	equal(t, "the next event", s.receive(t, 1, written), []uint64{longLog + 1})
	odd.quiet(t)
}
