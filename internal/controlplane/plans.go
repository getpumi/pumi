// SPDX-License-Identifier: Apache-2.0
// SPDX-FileCopyrightText: The Pumi Authors

package controlplane

import (
	"connectrpc.com/connect/v2"

	"getpumi.dev/pumi/api/events"
	cloudeventsv1 "getpumi.dev/pumi/api/gen/io/cloudevents/v1"
	eventsv1 "getpumi.dev/pumi/api/gen/pumi/events/v1"
	"getpumi.dev/pumi/internal/controlplane/embeddedlog"
)

// addedPlans returns the data of each plan.added of evs, by its index in
// evs. It refuses, with invalid argument, a plan.added that names no plan
// or no track, or whose subject is not the plan.
func addedPlans(evs []*cloudeventsv1.CloudEvent) (map[int]*eventsv1.PlanAdded, error) {
	out := map[int]*eventsv1.PlanAdded{}
	for i, e := range evs {
		if e.GetType() != events.TypePlanAdded {
			continue
		}
		var a eventsv1.PlanAdded
		if err := e.GetProtoData().UnmarshalTo(&a); err != nil {
			return nil, connect.Errorf(connect.CodeInvalidArgument, "event %d of the append: %v", i+1, err)
		}
		switch {
		case a.GetPlan() == "":
			return nil, connect.Errorf(connect.CodeInvalidArgument, "event %d of the append: the plan.added names no plan", i+1)
		case a.GetTrack() == "":
			return nil, connect.Errorf(connect.CodeInvalidArgument, "event %d of the append: the plan.added of %s names no track",
				i+1, a.GetPlan())
		case e.GetAttributes()["subject"].GetCeString() != a.GetPlan():
			return nil, connect.Errorf(connect.CodeInvalidArgument,
				"event %d of the append: the subject of the plan.added of %s is not the plan", i+1, a.GetPlan())
		}
		out[i] = &a
	}
	return out, nil
}

// checkAdded refuses, with failed precondition, a plan.added of added, the
// plan.added events of an append of n events by their index, that adds a
// plan which the log as of at or an earlier event of the append already
// adds, or that depends on a plan that neither adds earlier in the same
// track (decision 0196).
func (s *Service) checkAdded(at uint64, added map[int]*eventsv1.PlanAdded, n int) error {
	trackOf, err := s.tracksAt(at)
	if err != nil {
		return err
	}
	for i := range n {
		a, ok := added[i]
		if !ok {
			continue
		}
		if err := checkPlanAdded(i, a, trackOf); err != nil {
			return err
		}
		trackOf[a.GetPlan()] = a.GetTrack()
	}
	return nil
}

// tracksAt returns the track of each plan that the log as of at adds.
func (s *Service) tracksAt(at uint64) (map[string]string, error) {
	entries, _, err := s.log.Read(embeddedlog.Filter{Types: []string{events.TypePlanAdded}})
	if err != nil {
		return nil, logError(err)
	}
	trackOf := map[string]string{}
	for _, e := range entries {
		if e.Position > at {
			break
		}
		var a eventsv1.PlanAdded
		if err := e.Event.GetProtoData().UnmarshalTo(&a); err != nil {
			return nil, logError(err)
		}
		if _, ok := trackOf[a.GetPlan()]; !ok {
			trackOf[a.GetPlan()] = a.GetTrack()
		}
	}
	return trackOf, nil
}

// checkPlanAdded refuses a, event i of an append, when trackOf, the tracks
// of the plans added before it, has its plan or lacks a plan it depends on
// in its track.
func checkPlanAdded(i int, a *eventsv1.PlanAdded, trackOf map[string]string) error {
	if _, dup := trackOf[a.GetPlan()]; dup {
		return connect.Errorf(connect.CodeFailedPrecondition, "event %d of the append: the plan %s was already added",
			i+1, a.GetPlan())
	}
	for _, d := range a.GetDependsOn() {
		if track, ok := trackOf[d]; !ok || track != a.GetTrack() {
			return connect.Errorf(connect.CodeFailedPrecondition,
				"event %d of the append: the plan %s depends on %s, which is not a plan added earlier in the track %s (decision 0196)",
				i+1, a.GetPlan(), d, a.GetTrack())
		}
	}
	return nil
}
