// SPDX-License-Identifier: Apache-2.0
// SPDX-FileCopyrightText: The Pumi Authors

package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"google.golang.org/protobuf/encoding/prototext"

	"getpumi.dev/pumi/api/events"
	cloudeventsv1 "getpumi.dev/pumi/api/gen/io/cloudevents/v1"
	configv1 "getpumi.dev/pumi/api/gen/pumi/config/v1"
	eventsv1 "getpumi.dev/pumi/api/gen/pumi/events/v1"
)

// The shown runs through the model gateway (decision 0236,
// getpumi/pumi#102). Like the other shown runs, they are skipped unless the
// environment names a ScaleWholesale clone and an archived plan.
//
// claude keeps its own sign-in, unless PUMI_SHOWN_API_KEY_ENV names the
// environment variable that holds the provider's API key: then the
// configuration file's gateway.api_key_env names it, and the gateway sends
// the key (decision 0225).
const envProviderVariable = "PUMI_SHOWN_API_KEY_ENV"

// The model role of the spike workflow and the models it maps to in the
// shown runs (decision 0233).
const (
	shownRole     = "frontier"
	shownModel    = "claude-opus-5-5"
	shownFallback = "claude-sonnet-5-5"
)

// shownGateway is the gateway block of the shown runs' configuration.
func shownGateway() *configv1.Gateway {
	return &configv1.Gateway{
		ApiKeyEnv:  os.Getenv(envProviderVariable),
		ModelRoles: map[string]*configv1.ModelRole{shownRole: {Model: shownModel, Fallback: shownFallback}},
	}
}

// The plans of the shown runs through the gateway. Both implement the
// archived plan in .pumi/plan.md of the trunk.
const (
	// shownCountedPlan is the plan that runs to done, whose usage the
	// gateway counts.
	shownCountedPlan = "counted-by-the-gateway"
	// shownCappedPlan is the plan whose agent run has a small token cap.
	shownCappedPlan = "capped-by-the-gateway"
)

// shownTokenCap is the token cap of the capped plan's agent run: less than
// the tokens of claude's first few calls, so the gateway refuses a call
// soon after the run starts (decision 0228).
const shownTokenCap = 10_000

// cappedConfig is the configuration file of the capped plan, beside the
// repository: the shown runs' file with the token cap in the budget.
const cappedConfig = "../pumi-capped.txtpb"

// gatewayToken is the invocation token that claude was given, and how it
// sends it.
type gatewayToken struct {
	value string
	// header is true when claude keeps its sign-in and sends the token in
	// the header X-Pumi-Token, false when it sends the token as its bearer
	// token.
	header bool
}

// tokenOf returns the gateway token in the environment of a claude process
// in the workspace, read from /proc while the agent run runs, so that the
// test can look for it in the logs. It never shows the token.
func (s *shownCommands) tokenOf(b *background) gatewayToken {
	s.t.Helper()
	deadline := time.Now().Add(commandWait)
	for {
		for _, p := range inDir(s.run.WorkDir) {
			pid, name, _ := strings.Cut(p, " ")
			if name != "(claude)" {
				continue
			}
			if tok, ok := tokenInEnviron(pid); ok {
				return tok
			}
		}
		select {
		case err := <-b.done:
			b.done <- err
			s.t.Fatalf("pumi run stopped with %v before claude ran; its output:\n%s", err, b.out.String())
		default:
		}
		if time.Now().After(deadline) {
			s.t.Fatalf("no claude process with a gateway token ran in the workspace within %s", commandWait)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// tokenInEnviron returns the gateway token in the environment of the
// process pid, read through an os.Root, which gosec does not report
// (G304).
func tokenInEnviron(pid string) (gatewayToken, bool) {
	root, err := os.OpenRoot("/proc")
	if err != nil {
		return gatewayToken{}, false
	}
	defer func() { _ = root.Close() }()
	b, err := root.ReadFile(pid + "/environ")
	if err != nil {
		return gatewayToken{}, false
	}
	for kv := range strings.SplitSeq(string(b), "\x00") {
		if v, ok := strings.CutPrefix(kv, "ANTHROPIC_CUSTOM_HEADERS=X-Pumi-Token: "); ok && v != "" {
			return gatewayToken{value: v, header: true}, true
		}
		if v, ok := strings.CutPrefix(kv, "ANTHROPIC_AUTH_TOKEN="); ok && v != "" {
			return gatewayToken{value: v}, true
		}
	}
	return gatewayToken{}, false
}

// checkTokenPath fails the test unless claude sends the token as the
// configuration's credential path says: in the header when it keeps its
// sign-in, as its bearer token when the gateway sends the API key
// (decision 0225).
func checkTokenPath(t *testing.T, invocation string, tok gatewayToken) {
	t.Helper()
	keyEnv := os.Getenv(envProviderVariable)
	switch {
	case keyEnv == "" && tok.header:
		t.Logf("claude of %s keeps its sign-in and sends the gateway token in X-Pumi-Token", invocation)
	case keyEnv != "" && !tok.header:
		t.Logf("claude of %s sends the gateway token as its bearer token; the gateway sends the API key of %s", invocation, keyEnv)
	default:
		t.Errorf("claude of %s sends the gateway token in the header %v, but %s is %q", invocation, tok.header, envProviderVariable, keyEnv)
	}
}

// noToken fails the test when text, what is named, holds the token.
func noToken(t *testing.T, what, text string, tok gatewayToken) {
	t.Helper()
	if strings.Contains(text, tok.value) {
		t.Errorf("%s holds the gateway token", what)
		return
	}
	t.Logf("%s does not hold the gateway token", what)
}

// writeCappedConfig writes cappedConfig: the configuration file of the
// shown runs with the token cap in the base invocation's budget.
func (s *shownCommands) writeCappedConfig() {
	s.t.Helper()
	top := filepath.Dir(s.run.Repository)
	cfg := &configv1.Config{}
	if err := prototext.Unmarshal(readFile(s.t, top, "pumi.txtpb"), cfg); err != nil {
		s.t.Fatal(err)
	}
	cfg.GetBase().GetBudget().Tokens = shownTokenCap
	b, err := prototext.MarshalOptions{Multiline: true}.Marshal(cfg)
	if err != nil {
		s.t.Fatal(err)
	}
	writeFile(s.t, top, filepath.Base(cappedConfig), string(b))
	s.t.Logf("the configuration file %s:\n%s", cappedConfig, b)
}

// TestShownRunsThroughTheGateway runs real Claude Code through the model
// gateway that pumi run serves (decisions 0225, 0226 and 0231). The first
// plan runs to done: its invocation.result holds the usage and the
// reference cost that the gateway counted, and the adapter's own figures
// are in its log (decisions 0229 and 0230). The second plan's agent run has
// a small token cap: the gateway refuses a call at the cap, the run stops
// with the outcome cap_reached and the plan escalates (decisions 0203 and
// 0228). pumi log prints each invocation's log, and neither it, the output
// of pumi run nor the event log holds the gateway token (decision 0232).
func TestShownRunsThroughTheGateway(t *testing.T) {
	clone, plan, adapter := shownSetup(t)
	s := prepareCommands(t, clone, plan, adapter)
	s.writeCappedConfig()
	ctx := t.Context()

	s.pumi(exec.CommandContext(ctx, "pumi", "add", "--config", "../pumi.txtpb", "--track", "counted", shownCountedPlan))
	first := s.start(exec.CommandContext(ctx, "pumi", "run", "--config", "../pumi.txtpb"))
	s.waitLog(first, commandWait, "invocation.started of the first plan", ofTypeFor(events.TypeInvocationStarted, shownCountedPlan+"/1"))
	counted := s.tokenOf(first)
	checkTokenPath(t, shownCountedPlan+"/1", counted)
	s.waitLog(first, runTimeout, "transition of the first plan to done", s.entered(shownCountedPlan, "done"))
	s.pumi(exec.CommandContext(ctx, "pumi", "status"))
	if err := s.stop(first, syscall.SIGINT); err != nil {
		t.Errorf("pumi run exited with %v after SIGINT, want status 0", err)
	}
	noToken(t, "the output of the first pumi run", first.out.String(), counted)
	countedLog := s.pumi(exec.CommandContext(ctx, "pumi", "log", "--config", "../pumi.txtpb", shownCountedPlan+"/1"))
	noToken(t, "pumi log of "+shownCountedPlan+"/1", countedLog, counted)
	if !strings.Contains(countedLog, "the adapter reported") {
		t.Errorf("pumi log of %s/1 has no line with the adapter's own figures", shownCountedPlan)
	}

	s.pumi(exec.CommandContext(ctx, "pumi", "add", "--config", cappedConfig, "--track", "capped", shownCappedPlan))
	second := s.start(exec.CommandContext(ctx, "pumi", "run", "--config", cappedConfig))
	s.waitLog(second, commandWait, "invocation.started of the second plan", ofTypeFor(events.TypeInvocationStarted, shownCappedPlan+"/1"))
	capped := s.tokenOf(second)
	checkTokenPath(t, shownCappedPlan+"/1", capped)
	s.waitLog(second, runTimeout, "escalation.raised of the second plan", ofTypeFor(events.TypeEscalationRaised, shownCappedPlan))
	s.pumi(exec.CommandContext(ctx, "pumi", "status"))
	cappedLog := s.pumi(exec.CommandContext(ctx, "pumi", "log", shownCappedPlan+"/1"))
	noToken(t, "pumi log of "+shownCappedPlan+"/1", cappedLog, capped)
	if err := s.stop(second, syscall.SIGINT); err != nil {
		t.Errorf("pumi run exited with %v after SIGINT, want status 0", err)
	}
	noToken(t, "the output of the second pumi run", second.out.String(), capped)

	evs := readLog(t, s.run)
	showLog(t, "shown runs through the gateway", s.run)
	logPath, _ := logFiles(s.run.Repository)
	logText := string(readFile(t, filepath.Dir(logPath), filepath.Base(logPath)))
	noToken(t, "the event log", logText, counted)
	noToken(t, "the event log", logText, capped)
	checkCounted(t, evs)
	checkCapped(t, evs)
	showChange(t, s.run, evs, shownCountedPlan+"/1")
}

// resultOf returns the one invocation.result of invocation in evs.
func resultOf(t *testing.T, evs []*cloudeventsv1.CloudEvent, invocation string) *eventsv1.InvocationResult {
	t.Helper()
	results := ofType(ofTypesFor(evs, invocation), events.TypeInvocationResult)
	if len(results) != 1 {
		t.Fatalf("the invocation %s has %d results, want 1", invocation, len(results))
	}
	return dataOf(t, results[0], &eventsv1.InvocationResult{})
}

// outcomeOf returns the outcome of the one invocation.finished of
// invocation in evs.
func outcomeOf(t *testing.T, evs []*cloudeventsv1.CloudEvent, invocation string) eventsv1.InvocationFinished_Outcome {
	t.Helper()
	finished := ofType(ofTypesFor(evs, invocation), events.TypeInvocationFinished)
	if len(finished) != 1 {
		t.Fatalf("the invocation %s finished %d times, want once", invocation, len(finished))
	}
	return dataOf(t, finished[0], &eventsv1.InvocationFinished{}).GetOutcome()
}

// checkCounted checks the first plan: its invocation succeeded with the
// tokens and cost that the gateway counted, and the plan reached done.
func checkCounted(t *testing.T, evs []*cloudeventsv1.CloudEvent) {
	t.Helper()
	inv := shownCountedPlan + "/1"
	if got, want := strings.Join(planFired(t, evs, shownCountedPlan), ", "), "implementing -result-> done"; got != want {
		t.Errorf("the transitions of %s are %s, want %s", shownCountedPlan, got, want)
	}
	if o := outcomeOf(t, evs, inv); o != eventsv1.InvocationFinished_OUTCOME_SUCCEEDED {
		t.Errorf("%s finished as %v, want succeeded", inv, o)
	}
	u := resultOf(t, evs, inv).GetUsage()
	if u.GetTokens() <= 0 || u.GetCostMicrodollars() <= 0 {
		t.Errorf("the usage of %s is %v, want the tokens and cost that the gateway counted", inv, u)
	}
	t.Logf("the result of %s: %d tokens, %d microdollars, %d s", inv, u.GetTokens(), u.GetCostMicrodollars(), u.GetSeconds())
}

// checkCapped checks the second plan: its invocation's usage is over the
// token cap, it finished with the outcome cap reached, and the plan went
// to implementing-escalated on cap-reached.
func checkCapped(t *testing.T, evs []*cloudeventsv1.CloudEvent) {
	t.Helper()
	inv := shownCappedPlan + "/1"
	if got, want := strings.Join(planFired(t, evs, shownCappedPlan), ", "), "implementing -cap-reached-> implementing-escalated"; got != want {
		t.Errorf("the transitions of %s are %s, want %s", shownCappedPlan, got, want)
	}
	if o := outcomeOf(t, evs, inv); o != eventsv1.InvocationFinished_OUTCOME_CAP_REACHED {
		t.Errorf("%s finished as %v, want cap reached", inv, o)
	}
	u := resultOf(t, evs, inv).GetUsage()
	if u.GetTokens() < shownTokenCap {
		t.Errorf("the usage of %s is %v, under the token cap %d", inv, u, shownTokenCap)
	}
	t.Logf("the result of %s: %d tokens, %d microdollars, %d s, with the token cap %d", inv, u.GetTokens(), u.GetCostMicrodollars(), u.GetSeconds(), shownTokenCap)
}
