// SPDX-License-Identifier: Apache-2.0
// SPDX-FileCopyrightText: The Pumi Authors

package controlplane

import (
	"bytes"
	"errors"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"testing"

	"connectrpc.com/connect/v2"
	"github.com/cedar-policy/cedar-go"
	"google.golang.org/protobuf/proto"

	"getpumi.dev/pumi/api/events"
	cloudeventsv1 "getpumi.dev/pumi/api/gen/io/cloudevents/v1"
	eventsv1 "getpumi.dev/pumi/api/gen/pumi/events/v1"
	factsv1 "getpumi.dev/pumi/api/gen/pumi/facts/v1"
	harnessv1 "getpumi.dev/pumi/api/gen/pumi/harness/v1"
	runnerv1 "getpumi.dev/pumi/api/gen/pumi/runner/v1"
	"getpumi.dev/pumi/api/policy"
	"getpumi.dev/pumi/internal/controlplane/embeddedlog"
	"getpumi.dev/pumi/internal/controlplane/projections"
)

// testConfig is a wiring with a lower set that permits every request.
func testConfig() Config {
	return Config{
		Policies: []PolicyFile{{Name: "permit-all.cedar", Text: permitAllText}},
		Settings: Settings{
			MaxAttempts: map[string]int64{
				"ambiguity": 3, "budgets": 3, "conformance": 3, "dco": 3, "mergeApproval": 3,
				"outsideChecks": 3, "planReview": 3, "scope": 3, "spec": 3,
			},
			Reviewers: 1, WarningsCap: 5,
			UsageBudget: policy.Caps{Tokens: 1_000_000, CostMicrodollars: 10_000_000, Seconds: 3600},
		},
		HardCaps: policy.DefaultHardCaps(),
		Project:  Project{Name: "pumi", ProtectedPaths: []string{".github/**"}},
	}
}

// testService returns a control plane on a new log, with testConfig.
func testService(t *testing.T) *Service {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "main")
	if err := os.Mkdir(dir, 0o750); err != nil {
		t.Fatal(err)
	}
	for _, cmd := range []*exec.Cmd{
		exec.CommandContext(t.Context(), "git", "init", "--quiet", "--initial-branch=main"),
		exec.CommandContext(t.Context(), "git", "commit", "--quiet", "--allow-empty", "--message=first"),
	} {
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL="+os.DevNull, "GIT_CONFIG_NOSYSTEM=1",
			"GIT_AUTHOR_NAME=A", "GIT_AUTHOR_EMAIL=a@example.com", "GIT_COMMITTER_NAME=C", "GIT_COMMITTER_EMAIL=c@example.com")
		var stderr bytes.Buffer
		cmd.Stderr = &stderr
		if err := cmd.Run(); err != nil {
			t.Fatalf("%s: %v\n%s", cmd, err, stderr.String())
		}
	}
	log, err := embeddedlog.Open(t.Context(), dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = log.Close() })
	s, err := New(log, testConfig())
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// write appends events with data to the log directly, as the control
// plane's own appends do.
func write(t *testing.T, s *Service, subject string, data ...proto.Message) {
	t.Helper()
	evs := []*cloudeventsv1.CloudEvent{}
	for _, d := range data {
		e, err := events.New(d, events.Attributes{ID: newID(), Source: "/test", Subject: subject, Tenant: embeddedlog.Tenant})
		if err != nil {
			t.Fatal(err)
		}
		evs = append(evs, e)
	}
	if _, err := s.log.Append(evs, nil); err != nil {
		t.Fatal(err)
	}
}

// fixed is a projection of fact that gives msg once the log has a
// plan.added event, and nothing before.
func fixed(fact string, msg proto.Message) projections.Projection {
	return projections.Projection{
		Fact:  fact,
		Types: []string{events.TypePlanAdded},
		Project: func(_ projections.Keys, evs []*cloudeventsv1.CloudEvent) (proto.Message, error) {
			if len(evs) == 0 {
				return nil, nil
			}
			return msg, nil
		},
	}
}

// dcoFacts registers, beside attempts, a projection for every other fact
// that the dco gate reads.
func dcoFacts(s *Service) {
	for _, p := range []projections.Projection{
		fixed("dco.signed", &factsv1.DcoSigned{HeadCommit: "commit-1", Value: true}),
		fixed("overrides", &factsv1.Overrides{Plan: "plan-a", Tree: "tree-1"}),
		fixed("usage", &factsv1.Usage{Invocation: "i", Plan: "plan-a", Value: &factsv1.Usage_Totals{Tokens: 10, CostMicrodollars: 20, Seconds: 30}}),
		fixed("warnings.open", &factsv1.WarningsOpen{Project: "pumi", Value: 0}),
	} {
		s.projections[p.Fact] = p
	}
}

// decideDCO decides the dco gate of plan-a as the kernel at the head.
func decideDCO(t *testing.T, s *Service) (decided, *eventsv1.DecisionMade) {
	t.Helper()
	at, hash := s.log.Snapshot()
	d, err := s.decide(at, hash, principal{caller: CallerKernel, id: "kernel"}, question{
		action: "dco", plan: "plan-a", keys: projections.Keys{HeadTree: "tree-1", HeadCommit: "commit-1"},
	})
	if err != nil {
		t.Fatal(err)
	}
	var data eventsv1.DecisionMade
	if err := d.event.GetProtoData().UnmarshalTo(&data); err != nil {
		t.Fatal(err)
	}
	if data.GetLogHead() != hash {
		t.Errorf("the decision was made at %q, want the head's hash %q", data.GetLogHead(), hash)
	}
	return d, &data
}

// basisNames returns the full names of the messages in the basis.
func basisNames(d *eventsv1.DecisionMade) []string {
	out := []string{}
	for _, f := range d.GetBasis().GetFacts() {
		out = append(out, string(f.MessageName()))
	}
	return out
}

func TestSettingsGiveEverySettingOfTheSchema(t *testing.T) {
	want := []string{}
	for _, s := range policy.Settings() {
		want = append(want, s.Name)
	}
	settings := Settings{MaxAttempts: map[string]int64{"dco": 3}}
	if got := slices.Sorted(maps.Keys(settings.values("dco"))); !slices.Equal(got, want) {
		t.Errorf("Settings gives %q, want the settings of policy.Settings %q", got, want)
	}
}

func TestMaxAttemptsIsTheGatesOwn(t *testing.T) {
	// The profile sets maxAttempts for each gate (decision 0044), and a
	// gate with no setting has none, so a decision that reads it denies.
	settings := Settings{MaxAttempts: map[string]int64{"dco": 3, "spec": 1}}
	for gate, want := range map[string]cedar.Value{"dco": cedar.Long(3), "spec": cedar.Long(1)} {
		if got := settings.values(gate)["maxAttempts"]; got != want {
			t.Errorf("maxAttempts of %s = %v, want %v", gate, got, want)
		}
	}
	if got, ok := settings.values("scope")["maxAttempts"]; ok {
		t.Errorf("maxAttempts of scope = %v, want none, because the profile sets none", got)
	}
}

// usageFact registers a projection of usage for plan-a under the caps.
func usageFact(s *Service) {
	p := fixed("usage", &factsv1.Usage{Invocation: "i", Plan: "plan-a", Value: &factsv1.Usage_Totals{Tokens: 10, CostMicrodollars: 20, Seconds: 30}})
	s.projections[p.Fact] = p
}

// decideAsKernel decides q as the kernel at the head and returns the
// decision and the data of its decision.made event.
func decideAsKernel(t *testing.T, s *Service, q question) (Decision, *eventsv1.DecisionMade) {
	t.Helper()
	at, hash := s.log.Snapshot()
	d, err := s.decide(at, hash, principal{caller: CallerKernel, id: "kernel"}, q)
	if err != nil {
		t.Fatal(err)
	}
	var data eventsv1.DecisionMade
	if err := d.event.GetProtoData().UnmarshalTo(&data); err != nil {
		t.Fatal(err)
	}
	return d.decision, &data
}

// attemptsIn returns the attempts fact in the basis of d, or nil.
func attemptsIn(t *testing.T, d *eventsv1.DecisionMade) *factsv1.Attempts {
	t.Helper()
	for _, f := range d.GetBasis().GetFacts() {
		var a factsv1.Attempts
		if f.MessageIs(&a) {
			if err := f.UnmarshalTo(&a); err != nil {
				t.Fatal(err)
			}
			return &a
		}
	}
	return nil
}

func TestRetryReadsTheAttemptsAndTheMaxAttemptsOfTheGateItRetries(t *testing.T) {
	// retry reads attempts and the profile's maxAttempts for the gate of the
	// state the plan is in, which the plan's last transition entered, and
	// core.attempts-left forbids it when the gate has none left (decision
	// 0169).
	s := testService(t)
	s.cfg.Settings.MaxAttempts = map[string]int64{"planReview": 2, "dco": 5}
	usageFact(s)
	into := func(plan, gate string) *eventsv1.TransitionFired {
		return &eventsv1.TransitionFired{Plan: plan, FromState: "work", ToState: gate, Trigger: "t", Gate: gate}
	}
	write(t, s, "plan-a", &eventsv1.PlanAdded{Plan: "plan-a", Track: "main"}, into("plan-a", "planReview"), into("plan-a", "planReview"))
	write(t, s, "plan-b", &eventsv1.PlanAdded{Plan: "plan-b", Track: "main"}, into("plan-b", "planReview"), into("plan-b", "dco"))
	tests := []struct {
		plan, gate string
		attempts   int64
		allow      bool
		policies   []string
	}{
		{"plan-a", "planReview", 2, false, []string{"core.attempts-left"}},
		{"plan-b", "dco", 1, true, []string{"core.workflow-actions", "permit-all"}},
	}
	for _, tt := range tests {
		d, data := decideAsKernel(t, s, question{action: "retry", plan: tt.plan, keys: projections.Keys{Invocation: "i"}})
		if d.Allow != tt.allow || !slices.Equal(data.GetDeterminingPolicies(), tt.policies) {
			t.Errorf("retry of %s in %s: allow %v by %q (reasons %q), want %v by %q",
				tt.plan, tt.gate, d.Allow, data.GetDeterminingPolicies(), d.Reasons, tt.allow, tt.policies)
		}
		want := &factsv1.Attempts{Plan: tt.plan, Gate: tt.gate, Value: tt.attempts}
		if got := attemptsIn(t, data); !proto.Equal(got, want) {
			t.Errorf("retry of %s: the basis has the attempts %v, want %v", tt.plan, got, want)
		}
	}
}

func TestARetryOutsideAGateDenies(t *testing.T) {
	// A plan with no transition, or in a state with no gate, has no gate to
	// retry, so attempts and maxAttempts are missing (decision 0143).
	s := testService(t)
	usageFact(s)
	write(t, s, "plan-a", &eventsv1.PlanAdded{Plan: "plan-a", Track: "main"})
	write(t, s, "plan-b", &eventsv1.PlanAdded{Plan: "plan-b", Track: "main"},
		&eventsv1.TransitionFired{Plan: "plan-b", FromState: "dco", ToState: "implementing", Trigger: "t", Gate: "dco"},
		&eventsv1.TransitionFired{Plan: "plan-b", FromState: "dco", ToState: "implementing", Trigger: "t"})
	for _, plan := range []string{"plan-a", "plan-b"} {
		d, _ := decideAsKernel(t, s, question{action: "retry", plan: plan, keys: projections.Keys{Invocation: "i"}})
		if d.Allow || !slices.Contains(d.Reasons, "the context lacks the attribute maxAttempts") {
			t.Errorf("retry of %s: allow %v, reasons %q; want a deny that names maxAttempts", plan, d.Allow, d.Reasons)
		}
	}
}

func TestAPlanNeverAddedIsDeniedExceptForThePermissions(t *testing.T) {
	// A plan with no plan.added is unknown: a workflow action, a gate or
	// merge for it is denied before Cedar, with that reason and no facts
	// (decision 0176). A permission is still decided by Cedar (decision
	// 0164).
	s := testService(t)
	write(t, s, "plan-a", &eventsv1.PlanAdded{Plan: "plan-a", Track: "main"})
	never := "the plan \"plan-x\" was never added (decision 0176)"
	for _, action := range []string{"escalate", "advance", "retry", "spec", "merge"} {
		d, data := decideAsKernel(t, s, question{action: action, plan: "plan-x"})
		if d.Allow || !slices.Equal(d.Reasons, []string{never}) || len(data.GetBasis().GetFacts()) != 0 {
			t.Errorf("%s of a plan never added: allow %v, reasons %q, basis %v; want a deny for that reason with no facts",
				action, d.Allow, d.Reasons, data.GetBasis())
		}
	}
	d, _ := decideAsKernel(t, s, question{action: "cancel", plan: "plan-x"})
	if slices.Contains(d.Reasons, never) {
		t.Errorf("cancel of a plan never added: reasons %q; want it decided by Cedar", d.Reasons)
	}
}

func TestAGateWithNoMaxAttemptsDenies(t *testing.T) {
	s := testService(t)
	s.cfg.Settings.MaxAttempts = map[string]int64{"spec": 1}
	dcoFacts(s)
	write(t, s, "plan-a", &eventsv1.PlanAdded{Plan: "plan-a", Track: "main"},
		&eventsv1.TransitionFired{Plan: "plan-a", FromState: "a", ToState: "dco", Trigger: "t", Gate: "dco"})
	d, _ := decideDCO(t, s)
	if d.decision.Allow || !slices.Contains(d.decision.Reasons, "the context lacks the attribute maxAttempts") {
		t.Errorf("the dco gate with no maxAttempts: allow %v, reasons %q; want a deny that names maxAttempts", d.decision.Allow, d.decision.Reasons)
	}
}

func TestTheWorkflowActionsAreDecided(t *testing.T) {
	// The five workflow actions (decisions 0169 and 0171): escalate reads no
	// facts and a core policy permits it for the kernel; advance and resume
	// read the usage and the hard caps; ask reads questions.open, which has
	// no projection yet, so it is missing and ask denies (decision 0143).
	s := testService(t)
	usageFact(s)
	write(t, s, "plan-a", &eventsv1.PlanAdded{Plan: "plan-a", Track: "main"})
	tests := []struct {
		action   string
		allow    bool
		policies []string
		facts    []string
	}{
		{"escalate", true, []string{"core.escalate", "permit-all"}, []string{}},
		{"advance", true, []string{"core.workflow-actions", "permit-all"}, []string{"usage"}},
		{"resume", true, []string{"core.workflow-actions", "permit-all"}, []string{"usage"}},
		{"ask", false, nil, []string{}},
	}
	for _, tt := range tests {
		d, data := decideAsKernel(t, s, question{action: tt.action, plan: "plan-a", keys: projections.Keys{Invocation: "i"}})
		if d.Allow != tt.allow || !slices.Equal(data.GetDeterminingPolicies(), tt.policies) || !slices.Equal(d.Facts, tt.facts) {
			t.Errorf("%s: allow %v by %q with the facts %q (reasons %q), want %v by %q with %q",
				tt.action, d.Allow, data.GetDeterminingPolicies(), d.Facts, d.Reasons, tt.allow, tt.policies, tt.facts)
		}
	}
	d, _ := decideAsKernel(t, s, question{action: "ask", plan: "plan-a", keys: projections.Keys{Invocation: "i"}})
	if !slices.Contains(d.Reasons, "the context lacks the attribute questionsOpen") {
		t.Errorf("ask: the reasons are %q, want the missing questionsOpen named", d.Reasons)
	}
}

// writeAdapterUsage writes plan-a, its invocation i and the
// invocation.result of i with the usage that the adapter reported, and no
// gateway event.
func writeAdapterUsage(t *testing.T, s *Service, usage *eventsv1.InvocationResult_Usage) {
	t.Helper()
	requested, err := events.InvocationRequested("plan-a", &runnerv1.Invocation{
		Invocation: "i", IdempotencyKey: "key-i", Role: harnessv1.Role_ROLE_AGENT,
		Component: &harnessv1.Component{Kind: harnessv1.Component_KIND_AGENT_RUN, Name: "agent", Version: "1"},
	})
	if err != nil {
		t.Fatal(err)
	}
	write(t, s, "plan-a", &eventsv1.PlanAdded{Plan: "plan-a", Track: "main"}, requested,
		&eventsv1.InvocationResult{Invocation: "i", Usage: usage})
}

// usageIn returns the usage fact in the basis of d, or nil.
func usageIn(t *testing.T, d *eventsv1.DecisionMade) *factsv1.Usage {
	t.Helper()
	for _, f := range d.GetBasis().GetFacts() {
		var u factsv1.Usage
		if f.MessageIs(&u) {
			if err := f.UnmarshalTo(&u); err != nil {
				t.Fatal(err)
			}
			return &u
		}
	}
	return nil
}

func TestAdvanceReadsTheAdaptersUsageWhenNoGatewayCounted(t *testing.T) {
	// Until E7 no gateway counts an invocation, so the usage fact is the
	// usage that the adapter reported in the invocation's invocation.result,
	// and the hard caps apply to it (decision 0185). The usage projection is
	// the real one, and no gateway event is in the log.
	caps := policy.DefaultHardCaps()
	permitted, forbidden := []string{"core.workflow-actions", "permit-all"}, []string{"invariant.caps"}
	for _, c := range []struct {
		name     string
		usage    *eventsv1.InvocationResult_Usage
		allow    bool
		policies []string
	}{
		{"under the caps", &eventsv1.InvocationResult_Usage{Tokens: 10, CostMicrodollars: 20, Seconds: 30}, true, permitted},
		{"at the caps", &eventsv1.InvocationResult_Usage{Tokens: caps.Tokens, CostMicrodollars: caps.CostMicrodollars, Seconds: caps.Seconds}, true, permitted},
		{"over the token cap", &eventsv1.InvocationResult_Usage{Tokens: caps.Tokens + 1}, false, forbidden},
		{"over the cost cap", &eventsv1.InvocationResult_Usage{CostMicrodollars: caps.CostMicrodollars + 1}, false, forbidden},
		{"over the time cap", &eventsv1.InvocationResult_Usage{Seconds: caps.Seconds + 1}, false, forbidden},
	} {
		t.Run(c.name, func(t *testing.T) {
			s := testService(t)
			writeAdapterUsage(t, s, c.usage)
			d, data := decideAsKernel(t, s, question{action: "advance", plan: "plan-a", keys: projections.Keys{Invocation: "i"}})
			if d.Allow != c.allow || !slices.Equal(data.GetDeterminingPolicies(), c.policies) {
				t.Errorf("advance: allow %v by %q (reasons %q), want %v by %q",
					d.Allow, data.GetDeterminingPolicies(), d.Reasons, c.allow, c.policies)
			}
			want := &factsv1.Usage{Invocation: "i", Plan: "plan-a", Value: &factsv1.Usage_Totals{
				Tokens: c.usage.GetTokens(), CostMicrodollars: c.usage.GetCostMicrodollars(), Seconds: c.usage.GetSeconds(),
			}}
			if got := usageIn(t, data); !proto.Equal(got, want) {
				t.Errorf("the usage in the basis is %v, want the adapter's %v", got, want)
			}
		})
	}
}

func TestTheCoreAndTheInvariantsAreInEveryPolicySet(t *testing.T) {
	files := policyFiles(Config{Policies: []PolicyFile{{Name: "lower.cedar", Text: permitAllText}}})
	names := []string{}
	for _, f := range files {
		names = append(names, f.Name)
	}
	if want := []string{"invariants.cedar", "core.cedar", "lower.cedar"}; !slices.Equal(names, want) {
		t.Errorf("the policy files are %q, want %q", names, want)
	}
	if files[1].Text != policy.Core {
		t.Error("core.cedar is not the core policies of the api module")
	}
}

func TestDecideAllowsWithEveryFactAndRecordsTheBasis(t *testing.T) {
	s := testService(t)
	dcoFacts(s)
	write(t, s, "plan-a", &eventsv1.PlanAdded{Plan: "plan-a", Track: "main"},
		&eventsv1.TransitionFired{Plan: "plan-a", FromState: "a", ToState: "dco", Trigger: "t", Gate: "dco"})
	d, data := decideDCO(t, s)
	if !d.decision.Allow || data.GetOutcome() != eventsv1.DecisionMade_OUTCOME_ALLOW {
		t.Fatalf("the decision is %+v, want allow", d.decision)
	}
	want := []string{"pumi.facts.v1.Attempts", "pumi.facts.v1.DcoSigned", "pumi.facts.v1.Overrides", "pumi.facts.v1.Usage", "pumi.facts.v1.WarningsOpen"}
	if got := basisNames(data); !slices.Equal(got, want) {
		t.Errorf("the basis holds %q, want %q, in the order of their context names", got, want)
	}
	if !slices.Equal(data.GetDeterminingPolicies(), []string{"permit-all"}) {
		t.Errorf("the determining policies are %q, want permit-all", data.GetDeterminingPolicies())
	}
	if data.GetPolicySetDigest() != policySetDigest(policyFiles(testConfig())) {
		t.Errorf("the policy set digest is %q", data.GetPolicySetDigest())
	}
}

func TestDecideDeniesWithoutAFactAndLeavesItOutOfTheBasis(t *testing.T) {
	s := testService(t)
	dcoFacts(s)
	delete(s.projections, "dco.signed")
	write(t, s, "plan-a", &eventsv1.PlanAdded{Plan: "plan-a", Track: "main"},
		&eventsv1.TransitionFired{Plan: "plan-a", FromState: "a", ToState: "dco", Trigger: "t", Gate: "dco"})
	d, data := decideDCO(t, s)
	if d.decision.Allow || data.GetOutcome() != eventsv1.DecisionMade_OUTCOME_DENY {
		t.Fatalf("the decision is %+v, want deny", d.decision)
	}
	if slices.Contains(basisNames(data), "pumi.facts.v1.DcoSigned") || len(data.GetBasis().GetFacts()) != 4 {
		t.Errorf("the basis holds %q, want the four facts other than dco.signed", basisNames(data))
	}
	if !slices.Contains(d.decision.Reasons, "the context lacks the attribute dcoSigned") {
		t.Errorf("the reasons are %q, want the missing dcoSigned named", d.decision.Reasons)
	}
}

func TestDecideReadsTheLogOnlyUpToTheHeadItDecidesAt(t *testing.T) {
	s := testService(t)
	write(t, s, "plan-a", &eventsv1.PlanAdded{Plan: "plan-a", Track: "main"},
		&eventsv1.TransitionFired{Plan: "plan-a", FromState: "a", ToState: "dco", Trigger: "t", Gate: "dco"})
	at, hash := s.log.Snapshot()
	write(t, s, "plan-a", &eventsv1.TransitionFired{Plan: "plan-a", FromState: "a", ToState: "dco", Trigger: "t", Gate: "dco"})
	d, err := s.decide(at, hash, principal{caller: CallerKernel, id: "kernel"}, question{action: "dco", plan: "plan-a"})
	if err != nil {
		t.Fatal(err)
	}
	var data eventsv1.DecisionMade
	if err := d.event.GetProtoData().UnmarshalTo(&data); err != nil {
		t.Fatal(err)
	}
	var a factsv1.Attempts
	for _, f := range data.GetBasis().GetFacts() {
		if f.MessageIs(&a) && f.UnmarshalTo(&a) != nil {
			t.Fatal("the attempts of the basis do not parse")
		}
	}
	if a.GetValue() != 1 {
		t.Errorf("the basis is %v, want attempts 1, counted up to the head the decision was made at", data.GetBasis())
	}
}

func TestAFailingOrWrongProjectionGivesNoFact(t *testing.T) {
	s := testService(t)
	write(t, s, "plan-a", &eventsv1.PlanAdded{Plan: "plan-a", Track: "main"})
	for name, p := range map[string]projections.Projection{
		"fails": {Fact: "attempts", Types: []string{events.TypePlanAdded},
			Project: func(projections.Keys, []*cloudeventsv1.CloudEvent) (proto.Message, error) {
				return &factsv1.Attempts{Plan: "plan-a", Gate: "dco", Value: 1}, errors.New("broken")
			}},
		"another fact's message": fixed("attempts", &factsv1.DcoSigned{HeadCommit: "c", Value: true}),
	} {
		s.projections["attempts"] = p
		_, data := decideDCO(t, s)
		if slices.Contains(basisNames(data), "pumi.facts.v1.Attempts") || data.GetOutcome() != eventsv1.DecisionMade_OUTCOME_DENY {
			t.Errorf("a projection that %s: the decision is %v with the basis %q, want a deny with no attempts",
				name, data.GetOutcome(), basisNames(data))
		}
	}
}

// verdict returns a verdict.recorded event for plan-a, as a gate-role
// invocation's Report writes it.
func verdict(t *testing.T) *cloudeventsv1.CloudEvent {
	t.Helper()
	e, err := events.New(&eventsv1.VerdictRecorded{
		Gate: "planReview", GateVersion: "1", Tree: "tree-1", Verdict: eventsv1.VerdictRecorded_VERDICT_PASS,
		Plan: "plan-a", Invocation: "invocation-1",
	}, events.Attributes{ID: newID(), Source: "/runner/r1", Subject: "invocation-1", Tenant: embeddedlog.Tenant})
	if err != nil {
		t.Fatal(err)
	}
	return e
}

func TestAVerdictIsWrittenOnlyFromTheGateRole(t *testing.T) {
	s := testService(t)
	gate := principal{caller: CallerRunner, id: "r1", role: "gate"}
	positions, err := s.appendAs(t.Context(), gate, []*cloudeventsv1.CloudEvent{verdict(t)}, nil)
	if err != nil || !slices.Equal(positions, []uint64{2}) {
		t.Fatalf("a verdict from the gate role: %v, %v; want it at 2, after its decision", positions, err)
	}

	agent := principal{caller: CallerRunner, id: "r2", role: "agent"}
	_, err = s.appendAs(t.Context(), agent, []*cloudeventsv1.CloudEvent{verdict(t)}, nil)
	if connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("a verdict from the agent role: %v, want permission denied", err)
	}
	found, head, err := s.log.Read(embeddedlog.Filter{After: 2})
	if err != nil {
		t.Fatal(err)
	}
	if head != 3 || len(found) != 1 || found[0].Event.GetType() != events.TypeDecisionMade {
		t.Fatalf("after the refused verdict the log has the head %d and %d new events, want only its decision at 3", head, len(found))
	}
	var data eventsv1.DecisionMade
	if err := found[0].Event.GetProtoData().UnmarshalTo(&data); err != nil {
		t.Fatal(err)
	}
	if data.GetOutcome() != eventsv1.DecisionMade_OUTCOME_DENY ||
		!slices.Equal(data.GetDeterminingPolicies(), []string{"invariant.verdicts-by-gate-role"}) {
		t.Errorf("the recorded decision is %v by %q, want a deny by invariant.verdicts-by-gate-role",
			data.GetOutcome(), data.GetDeterminingPolicies())
	}
}

func TestAPermissionIsDecidedAgainWhenTheHeadMoves(t *testing.T) {
	s := testService(t)
	moved := false
	s.trace = func(string) {
		if !moved {
			moved = true
			write(t, s, "plan-b", &eventsv1.PlanAdded{Plan: "plan-b", Track: "main"})
		}
	}
	positions, err := s.appendAs(t.Context(), principal{caller: CallerRunner, id: "r1", role: "gate"},
		[]*cloudeventsv1.CloudEvent{verdict(t)}, nil)
	if err != nil || !slices.Equal(positions, []uint64{3}) {
		t.Fatalf("appendAs = %v, %v; want the verdict at 3, after the event appended in between and its decision", positions, err)
	}
	found, _, err := s.log.Read(embeddedlog.Filter{Types: []string{events.TypeDecisionMade}})
	if err != nil {
		t.Fatal(err)
	}
	if len(found) != 1 || found[0].Position != 2 {
		t.Fatalf("the log has %d decisions, want one, at 2", len(found))
	}
	hash := found[0].Event.GetAttributes()[events.ExtensionPrevHash].GetCeString()
	var data eventsv1.DecisionMade
	if err := found[0].Event.GetProtoData().UnmarshalTo(&data); err != nil {
		t.Fatal(err)
	}
	if data.GetLogHead() != hash {
		t.Errorf("the decision was made at %q, want the head after the event appended in between, %q", data.GetLogHead(), hash)
	}
}

// cancel returns a cancel.requested event with data, whose subject is
// subject.
func cancel(t *testing.T, subject string, data *eventsv1.CancelRequested) *cloudeventsv1.CloudEvent {
	t.Helper()
	e, err := events.New(data, events.Attributes{ID: newID(), Source: "/test", Subject: subject, Tenant: embeddedlog.Tenant})
	if err != nil {
		t.Fatal(err)
	}
	return e
}

func TestTheCancelPermissionIsDecidedForThePlanTheCancelNames(t *testing.T) {
	// A cancel with a plan and no invocation cancels the plan; one with an
	// invocation cancels that invocation (decision 0170). The plan the data
	// names is the Cedar resource, and the subject is when it names none.
	tests := []struct {
		name       string
		event      *cloudeventsv1.CloudEvent
		plan       string
		invocation string
	}{
		{"a plan", cancel(t, "plan-b", &eventsv1.CancelRequested{Plan: "plan-b", RequestedBy: "someone"}), "plan-b", ""},
		{"a plan and another subject", cancel(t, "plan-a", &eventsv1.CancelRequested{Plan: "plan-b", RequestedBy: "someone"}), "plan-b", ""},
		{"an invocation", cancel(t, "plan-a", &eventsv1.CancelRequested{Invocation: "invocation-1", RequestedBy: "someone"}), "plan-a", "invocation-1"},
		{"both", cancel(t, "plan-a", &eventsv1.CancelRequested{Plan: "plan-a", Invocation: "invocation-1", RequestedBy: "someone"}), "plan-a", "invocation-1"},
	}
	for _, tt := range tests {
		perms, err := permissionsOf([]*cloudeventsv1.CloudEvent{tt.event})
		if err != nil {
			t.Fatalf("a cancel of %s: %v", tt.name, err)
		}
		if len(perms) != 1 || perms[0].q.action != "cancel" || perms[0].q.plan != tt.plan || perms[0].q.keys.Invocation != tt.invocation {
			t.Errorf("a cancel of %s: the permissions are %+v, want cancel for %s with the invocation %q", tt.name, perms, tt.plan, tt.invocation)
		}
	}
}
