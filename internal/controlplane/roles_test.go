// SPDX-License-Identifier: Apache-2.0
// SPDX-FileCopyrightText: The Pumi Authors

package controlplane_test

import (
	"testing"

	"connectrpc.com/connect/v2"
	"connectrpc.com/connect/v2/connectinprocess"

	"getpumi.dev/pumi/api/events"
	cloudeventsv1 "getpumi.dev/pumi/api/gen/io/cloudevents/v1"
	controlplanev1 "getpumi.dev/pumi/api/gen/pumi/controlplane/v1"
	"getpumi.dev/pumi/api/gen/pumi/controlplane/v1/controlplanev1connect"
	eventsv1 "getpumi.dev/pumi/api/gen/pumi/events/v1"
	harnessv1 "getpumi.dev/pumi/api/gen/pumi/harness/v1"
	runnerv1 "getpumi.dev/pumi/api/gen/pumi/runner/v1"
	"getpumi.dev/pumi/api/gen/pumi/runner/v1/runnerv1connect"
	"getpumi.dev/pumi/internal/controlplane"
	"getpumi.dev/pumi/internal/controlplane/embeddedlog"
)

// rolePlane is the embedded control plane with a kernel, an agent runner
// and a person, for the tests of the circuit breaker of decision 0246.
type rolePlane struct {
	plane
	agent  runnerv1connect.RunnerServiceClient
	person controlplanev1connect.ControlPlaneServiceClient
}

// rolePerson is the principal of the person of a rolePlane.
const rolePerson = "person-1"

// newRolePlane returns a rolePlane on log.
func newRolePlane(t *testing.T, log *embeddedlog.Log) rolePlane {
	t.Helper()
	p := newPlane(t, log)
	dial := func(id controlplane.Identity) *connect.Client {
		server, err := p.svc.Server(id)
		if err != nil {
			t.Fatal(err)
		}
		return connect.NewClient(connectinprocess.New(server))
	}
	agent := controlplane.Identity{Caller: controlplane.CallerRunner, Principal: "runner-1", Roles: []string{controlplane.RoleAgent}, Tenant: embeddedlog.Tenant}
	person := controlplane.Identity{Caller: controlplane.CallerPerson, Principal: rolePerson, Roles: []string{"maintainer"}, Tenant: embeddedlog.Tenant}
	return rolePlane{
		plane:  p,
		agent:  runnerv1connect.NewRunnerServiceClient(dial(agent)),
		person: controlplanev1connect.NewControlPlaneServiceClient(dial(person)),
	}
}

// requestAgentRun has the kernel request an agent run of the model role
// named invocation.
func (p rolePlane) requestAgentRun(t *testing.T, invocation, role string) {
	t.Helper()
	data, err := events.InvocationRequested("plan-a", &runnerv1.Invocation{
		Invocation: invocation, IdempotencyKey: "key-" + invocation, Role: harnessv1.Role_ROLE_AGENT, ModelRole: role,
		Component: &harnessv1.Component{Kind: harnessv1.Component_KIND_AGENT_RUN, Name: "agent", Version: "1"},
	})
	if err != nil {
		t.Fatal(err)
	}
	e, err := events.New(data, events.Attributes{ID: "request-" + invocation, Source: "/kernel", Subject: "plan-a", Tenant: "x"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.kernel.Append(t.Context(), &controlplanev1.AppendRequest{Events: []*cloudeventsv1.CloudEvent{e}}); err != nil {
		t.Fatal(err)
	}
}

// claimAgentRun claims the next agent run and checks that it is
// invocation and that the claim says whether its model role is on its
// fallback as switched says.
func (p rolePlane) claimAgentRun(t *testing.T, invocation string, switched bool) *runnerv1.ClaimResponse {
	t.Helper()
	res, err := p.agent.Claim(t.Context(), &runnerv1.ClaimRequest{ProtocolVersions: []*runnerv1.ProtocolVersion{{Major: 1}}})
	if err != nil || res.GetInvocation().GetInvocation() != invocation {
		t.Fatalf("Claim = %v, %v; want %s", res, err, invocation)
	}
	if res.GetModelRoleSwitched() != switched {
		t.Errorf("the claim of %s says the model role %s is switched: %v, want %v", invocation, res.GetInvocation().GetModelRole(), res.GetModelRoleSwitched(), switched)
	}
	return res
}

// toFallback is the switch of the model role frontier to its fallback,
// which a limit on the run of invocation caused.
func toFallback(invocation string) *eventsv1.ModelRoleSwitched {
	return &eventsv1.ModelRoleSwitched{
		Role: "frontier", Adapter: "codex", Model: "astra-1", Invocation: invocation,
		Limit: &eventsv1.ModelRoleSwitched_Limit{Adapter: "claude-code", Model: "claude-fable-5-2"},
	}
}

// back is the switch of the model role frontier back, by the person.
func back(by string) *eventsv1.ModelRoleSwitched {
	return &eventsv1.ModelRoleSwitched{
		Role: "frontier", Adapter: "claude-code", Model: "claude-fable-5-2", SwitchedBy: by,
		Limit: &eventsv1.ModelRoleSwitched_Limit{Adapter: "claude-code", Model: "claude-fable-5-2"},
	}
}

// switchEvent returns the event of the switch s with the id id.
func switchEvent(t *testing.T, id string, s *eventsv1.ModelRoleSwitched) *cloudeventsv1.CloudEvent {
	t.Helper()
	e, err := events.New(s, events.Attributes{ID: id, Source: "/test", Subject: s.GetRole(), Tenant: "x"})
	if err != nil {
		t.Fatal(err)
	}
	return e
}

// reportWith reports the claimed agent run res as succeeded, with the
// events evs.
func (p rolePlane) reportWith(t *testing.T, res *runnerv1.ClaimResponse, evs ...*cloudeventsv1.CloudEvent) error {
	t.Helper()
	inv := res.GetInvocation().GetInvocation()
	_, err := p.agent.Report(t.Context(), &runnerv1.ReportRequest{
		Invocation: inv, LeaseNumber: res.GetLeaseNumber(), Events: evs,
		Result: &eventsv1.InvocationResult{Invocation: inv}, Outcome: eventsv1.InvocationFinished_OUTCOME_SUCCEEDED,
	})
	return err
}

// TestAModelRoleStaysOnItsFallbackUntilAPersonSwitchesItBack: the claim
// of an agent run says whether its model role is on its fallback, which
// the last model_role.switched of the role in the log says: from the
// Report that switched it, for every later agent run of the role, also
// after the control plane starts again on the log, and no longer after a
// person switched the role back (decision 0246). Another role is not
// switched.
func TestAModelRoleStaysOnItsFallbackUntilAPersonSwitchesItBack(t *testing.T) {
	dir := newClone(t)
	log, err := embeddedlog.Open(t.Context(), dir)
	if err != nil {
		t.Fatal(err)
	}
	first := newRolePlane(t, log)
	first.requestAgentRun(t, "plan-a/1", "frontier")
	res := first.claimAgentRun(t, "plan-a/1", false)
	if err := first.reportWith(t, res, switchEvent(t, "switch-1", toFallback("plan-a/1"))); err != nil {
		t.Fatalf("the Report with the switch: %v", err)
	}
	if n := first.count(t, events.TypeModelRoleSwitched); n != 1 {
		t.Fatalf("the log holds %d model_role.switched events, want 1", n)
	}
	first.requestAgentRun(t, "plan-a/2", "frontier")
	first.requestAgentRun(t, "plan-a/3", "review")
	first.claimAgentRun(t, "plan-a/2", true)
	first.claimAgentRun(t, "plan-a/3", false)
	if err := log.Close(); err != nil {
		t.Fatal(err)
	}

	second := newRolePlane(t, openLogIn(t, dir))
	second.requestAgentRun(t, "plan-a/4", "frontier")
	second.claimAgentRun(t, "plan-a/4", true)
	if _, err := second.person.Append(t.Context(), &controlplanev1.AppendRequest{
		Events: []*cloudeventsv1.CloudEvent{switchEvent(t, "back-1", back(rolePerson))},
	}); err != nil {
		t.Fatalf("the person's switch back: %v", err)
	}
	second.requestAgentRun(t, "plan-a/5", "frontier")
	second.claimAgentRun(t, "plan-a/5", false)
}

// TestABadSwitchIsRefused: a runner switches only the model role of the
// agent run it reports, to its fallback, naming the invocation and the
// limit; a person switches a role back, naming themselves and no
// invocation (decision 0246). Nothing of a refused append is written.
func TestABadSwitchIsRefused(t *testing.T) {
	other := toFallback("plan-a/1")
	other.Role = "review"
	byPerson := toFallback("plan-a/1")
	byPerson.SwitchedBy = rolePerson
	noLimit := toFallback("plan-a/1")
	noLimit.Limit = nil
	noModel := toFallback("plan-a/1")
	noModel.Model = ""
	for name, s := range map[string]*eventsv1.ModelRoleSwitched{
		"another role":         other,
		"a person's switch":    byPerson,
		"no limit":             noLimit,
		"no model":             noModel,
		"another invocation":   toFallback("plan-a/9"),
		"a switch back by run": back(""),
	} {
		t.Run("runner "+name, func(t *testing.T) {
			p := newRolePlane(t, openLog(t))
			p.requestAgentRun(t, "plan-a/1", "frontier")
			res := p.claimAgentRun(t, "plan-a/1", false)
			err := p.reportWith(t, res, switchEvent(t, "switch-1", s))
			if code := connect.CodeOf(err); code != connect.CodeInvalidArgument {
				t.Errorf("the Report = %v, want invalid argument", err)
			}
			if n := p.count(t, events.TypeModelRoleSwitched); n != 0 {
				t.Errorf("the log holds %d model_role.switched events, want none", n)
			}
		})
	}
	withInvocation := back(rolePerson)
	withInvocation.Invocation = "plan-a/1"
	noAdapter := back(rolePerson)
	noAdapter.Adapter = ""
	for name, s := range map[string]*eventsv1.ModelRoleSwitched{
		"with an invocation": withInvocation,
		"no adapter":         noAdapter,
		"no person":          toFallback(""),
	} {
		t.Run("person "+name, func(t *testing.T) {
			p := newRolePlane(t, openLog(t))
			_, err := p.person.Append(t.Context(), &controlplanev1.AppendRequest{Events: []*cloudeventsv1.CloudEvent{switchEvent(t, "back-1", s)}})
			if code := connect.CodeOf(err); code != connect.CodeInvalidArgument {
				t.Errorf("the append = %v, want invalid argument", err)
			}
		})
	}
}
