// SPDX-License-Identifier: Apache-2.0
// SPDX-FileCopyrightText: The Pumi Authors

package controlplane

import (
	"connectrpc.com/connect/v2"

	"getpumi.dev/pumi/api/events"
	cloudeventsv1 "getpumi.dev/pumi/api/gen/io/cloudevents/v1"
	eventsv1 "getpumi.dev/pumi/api/gen/pumi/events/v1"
	harnessv1 "getpumi.dev/pumi/api/gen/pumi/harness/v1"
	runnerv1 "getpumi.dev/pumi/api/gen/pumi/runner/v1"
	"getpumi.dev/pumi/internal/controlplane/embeddedlog"
)

// roleSwitched reports whether the model role of doc's agent run is on its
// fallback: whether the last model_role.switched of the role in the log
// switched it to its fallback, which names the invocation that a limit
// stopped, and not back, which names the person (decision 0246). A check
// and an external action name no model role.
func (s *Service) roleSwitched(doc *runnerv1.Invocation) (bool, error) {
	role := doc.GetModelRole()
	if doc.GetComponent().GetKind() != harnessv1.Component_KIND_AGENT_RUN || role == "" {
		return false, nil
	}
	found, _, err := s.log.Read(embeddedlog.Filter{Types: []string{events.TypeModelRoleSwitched}})
	if err != nil {
		return false, err
	}
	switched := false
	for _, e := range found {
		var d eventsv1.ModelRoleSwitched
		if e.Event.GetProtoData().UnmarshalTo(&d) != nil || d.GetRole() != role {
			continue
		}
		switched = d.GetInvocation() != ""
	}
	return switched, nil
}

// checkSwitch refuses, with invalid argument, event i of an append or a
// report when it is a model_role.switched that its appender may not write
// (decision 0246). Every switch names the role, the adapter and model it
// switched to and the limit's adapter and model. A switch that a runner
// reports switches the model role of doc, the agent run it reports, to its
// fallback, and names the invocation and no person; doc is nil for a
// person's append, which switches the role back, names the person and no
// invocation.
func checkSwitch(i int, e *cloudeventsv1.CloudEvent, doc *runnerv1.Invocation) error {
	if e.GetType() != events.TypeModelRoleSwitched {
		return nil
	}
	var d eventsv1.ModelRoleSwitched
	if err := e.GetProtoData().UnmarshalTo(&d); err != nil {
		return connect.Errorf(connect.CodeInvalidArgument, "event %d: %v", i+1, err)
	}
	refuse := func(why string) error {
		return connect.Errorf(connect.CodeInvalidArgument, "event %d, the model_role.switched of %q, %s (decision 0246)", i+1, d.GetRole(), why)
	}
	switch {
	case d.GetRole() == "" || d.GetAdapter() == "" || d.GetModel() == "":
		return refuse("does not name the role, the adapter and the model it switched to")
	case d.GetLimit().GetAdapter() == "" || d.GetLimit().GetModel() == "":
		return refuse("does not name the adapter and the model of the limit")
	case doc != nil && d.GetRole() != doc.GetModelRole():
		return refuse("switches another model role than the agent run's, " + doc.GetModelRole())
	case doc != nil && d.GetSwitchedBy() != "":
		return refuse("names a person, but a runner switches a role only to its fallback")
	case doc == nil && d.GetInvocation() != "":
		return refuse("names an invocation, but a person switches a role only back")
	case doc == nil && d.GetSwitchedBy() == "":
		return refuse("does not name the person who switched the role back")
	}
	return nil
}
