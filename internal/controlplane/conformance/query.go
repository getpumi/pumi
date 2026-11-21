// SPDX-License-Identifier: Apache-2.0
// SPDX-FileCopyrightText: The Pumi Authors

package conformance

import (
	"testing"

	"getpumi.dev/pumi/api/events"
	cloudeventsv1 "getpumi.dev/pumi/api/gen/io/cloudevents/v1"
	controlplanev1 "getpumi.dev/pumi/api/gen/pumi/controlplane/v1"
	eventsv1 "getpumi.dev/pumi/api/gen/pumi/events/v1"
)

// warningReviewed returns a dev.getpumi.warning.reviewed event about
// plan: an event that a person appends and that no permission guards, so
// the control plane writes nothing beside it.
func warningReviewed(t *testing.T, plan string) *cloudeventsv1.CloudEvent {
	t.Helper()
	return build(t, &eventsv1.WarningReviewed{Warning: "w", Person: "someone"}, plan)
}

// mixedLog appends seven events of three types about three plans:
//
//	1 plan.added a, 2 plan.added b, 3 transition.fired a,
//	4 transition.fired b, 5 transition.fired a, 6 plan.added c,
//	7 warning.reviewed a
func mixedLog(t *testing.T, c Clients) {
	t.Helper()
	mustAppend(t, c, Kernel, []*cloudeventsv1.CloudEvent{planAdded(t, "a"), planAdded(t, "b")})
	mustAppend(t, c, Kernel, []*cloudeventsv1.CloudEvent{transitionFired(t, "a"), transitionFired(t, "b")})
	mustAppend(t, c, Kernel, []*cloudeventsv1.CloudEvent{transitionFired(t, "a"), planAdded(t, "c")})
	mustAppend(t, c, Person, []*cloudeventsv1.CloudEvent{warningReviewed(t, "a")})
}

// filterCase is a filter and the positions of mixedLog that it selects.
type filterCase struct {
	name  string
	req   *controlplanev1.QueryRequest
	wants []uint64
}

// filterCases are the filters that Query and Subscribe are checked with.
func filterCases() []filterCase {
	transition := []string{events.TypeTransitionFired}
	return []filterCase{
		{"no filter", &controlplanev1.QueryRequest{}, span(1, 7)},
		{"a subject", &controlplanev1.QueryRequest{Filter: &controlplanev1.Filter{Subject: "a"}}, []uint64{1, 3, 5, 7}},
		{"one type", &controlplanev1.QueryRequest{Filter: &controlplanev1.Filter{Types: transition}}, []uint64{3, 4, 5}},
		{
			"two types",
			&controlplanev1.QueryRequest{Filter: &controlplanev1.Filter{
				Types: []string{events.TypeWarningReviewed, events.TypePlanAdded},
			}},
			[]uint64{1, 2, 6, 7},
		},
		{
			"a subject and a type",
			&controlplanev1.QueryRequest{Filter: &controlplanev1.Filter{Subject: "a", Types: transition}},
			[]uint64{3, 5},
		},
		{"after", &controlplanev1.QueryRequest{After: 4}, []uint64{5, 6, 7}},
		{"a subject after", &controlplanev1.QueryRequest{Filter: &controlplanev1.Filter{Subject: "a"}, After: 3}, []uint64{5, 7}},
		{"a type after", &controlplanev1.QueryRequest{Filter: &controlplanev1.Filter{Types: transition}, After: 3}, []uint64{4, 5}},
		{"a subject with no events", &controlplanev1.QueryRequest{Filter: &controlplanev1.Filter{Subject: "z"}}, []uint64{}},
		{
			"a type with no events",
			&controlplanev1.QueryRequest{Filter: &controlplanev1.Filter{Types: []string{events.TypeMergePerformed}}},
			[]uint64{},
		},
		{"after the head", &controlplanev1.QueryRequest{After: 7}, []uint64{}},
	}
}

// queryFilters: Query returns the events that match each filter, in log
// order, with the head of the whole log (decision 0154).
func queryFilters(t *testing.T, c Clients) {
	mixedLog(t, c)
	cases := append(filterCases(),
		filterCase{"a limit", &controlplanev1.QueryRequest{Limit: 2}, []uint64{1, 2}},
		filterCase{
			"a type, after and a limit",
			&controlplanev1.QueryRequest{Filter: &controlplanev1.Filter{Types: []string{events.TypeTransitionFired}}, After: 3, Limit: 1},
			[]uint64{4},
		},
		filterCase{"a limit above the count", &controlplanev1.QueryRequest{After: 5, Limit: 10}, []uint64{6, 7}},
	)
	for _, tt := range cases {
		for _, caller := range []Caller{Kernel, Person} {
			res, err := c(caller).Query(t.Context(), tt.req)
			if err != nil {
				t.Fatalf("Query with %s as %s: %v", tt.name, caller, err)
			}
			got := []uint64{}
			for _, e := range res.GetEvents() {
				got = append(got, e.GetPosition())
			}
			equal(t, "Query with "+tt.name, got, tt.wants)
			if res.GetHead() != 7 {
				t.Fatalf("Query with %s: the head is %d, want 7", tt.name, res.GetHead())
			}
		}
	}
	empty, err := c(Kernel).Query(t.Context(), &controlplanev1.QueryRequest{After: 100})
	if err != nil {
		t.Fatal(err)
	}
	if len(empty.GetEvents()) != 0 || empty.GetHead() != 7 {
		t.Fatalf("Query after 100 returned %d events and the head %d, want none and 7", len(empty.GetEvents()), empty.GetHead())
	}
}

// queryReadsPages: a long result is read in pages by passing the last
// position returned as after (decision 0154).
func queryReadsPages(t *testing.T, c Clients) {
	if h := head(t, c); h != 0 {
		t.Fatalf("the head of a new log is %d, want 0", h)
	}
	for range 10 {
		mustAppend(t, c, Kernel, []*cloudeventsv1.CloudEvent{transitionFired(t, "a")})
	}
	var pages [][]uint64
	after := uint64(0)
	for range 6 {
		got, h := query(t, c, &controlplanev1.QueryRequest{Filter: &controlplanev1.Filter{Subject: "a"}, After: after, Limit: 3})
		if h != 10 {
			t.Fatalf("the head is %d, want 10", h)
		}
		pages = append(pages, got)
		if len(got) == 0 {
			break
		}
		after = got[len(got)-1]
	}
	want := [][]uint64{{1, 2, 3}, {4, 5, 6}, {7, 8, 9}, {10}, {}}
	if len(pages) != len(want) {
		t.Fatalf("the pages are %v, want %v", pages, want)
	}
	for i := range want {
		equal(t, "a page", pages[i], want[i])
	}
}
