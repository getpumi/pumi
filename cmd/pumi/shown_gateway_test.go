// SPDX-License-Identifier: Apache-2.0
// SPDX-FileCopyrightText: The Pumi Authors

package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
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
// getpumi/pumi#102) and on a sign-in (decision 0251, getpumi/pumi#125).
// Like the other shown runs, they are skipped unless the environment names
// a ScaleWholesale clone and an archived plan.
//
// The harness keeps the person's own sign-in, unless PUMI_SHOWN_API_KEY_ENV
// names the environment variable that holds the provider's API key: then
// the model configuration's adapter entry names it, and the gateway sends
// the key (decisions 0225 and 0240).
const envProviderVariable = "PUMI_SHOWN_API_KEY_ENV"

// envHarness names the harness of the shown runs: claude-code, the
// default, or codex.
const envHarness = "PUMI_SHOWN_HARNESS"

// shownRole is the model role of the spike workflow (decision 0233).
const shownRole = "frontier"

// shownHarness is what the shown runs need to know about one harness.
type shownHarness struct {
	// name is the adapter's name in the model configuration.
	name string
	// binary is the adapter's executable on the PATH.
	binary string
	// process is the command name of the harness that the adapter runs.
	process string
	// model and fallback are the models that the role maps to.
	model, fallback string
	// keyPathHeader is true when, on the API key path, the harness sends
	// the invocation token in the header X-Pumi-Token, and false when it
	// sends it as its bearer token.
	keyPathHeader bool
	// tokens are the prefixes of the variables in the harness's
	// environment that hold the invocation token, each with whether the
	// harness sends it in the header.
	tokens []tokenVariable
}

// tokenVariable is the prefix of one variable that holds the invocation
// token, up to the token.
type tokenVariable struct {
	prefix string
	header bool
}

// shownHarnesses are the harnesses of the shown runs, by the value of
// PUMI_SHOWN_HARNESS.
var shownHarnesses = map[string]shownHarness{
	"claude-code": {
		name: "claude-code", binary: "adapter-claude-code", process: "claude",
		model: "claude-opus-5-5", fallback: "claude-sonnet-5-5",
		tokens: []tokenVariable{
			{prefix: "ANTHROPIC_CUSTOM_HEADERS=X-Pumi-Token: ", header: true},
			{prefix: "ANTHROPIC_AUTH_TOKEN="},
		},
	},
	// The Codex adapter gives codex the token in PUMI_CODEX_GATEWAY_HEADER,
	// which codex sends in the header X-Pumi-Token, and the gateway sends
	// the key (getpumi/adapter-codex#1).
	"codex": {
		name: "codex", binary: "adapter-codex", process: "codex",
		model: "gpt-6.2-sol", fallback: "gpt-6.1-sol",
		keyPathHeader: true,
		tokens:        []tokenVariable{{prefix: "PUMI_CODEX_GATEWAY_HEADER=", header: true}},
	},
}

// shownHarnessOf returns the harness that PUMI_SHOWN_HARNESS names, or
// Claude Code when it is not set.
func shownHarnessOf() (shownHarness, error) {
	name := os.Getenv(envHarness)
	if name == "" {
		name = "claude-code"
	}
	h, ok := shownHarnesses[name]
	if !ok {
		return shownHarness{}, fmt.Errorf("%s is %q, want claude-code or codex", envHarness, name)
	}
	return h, nil
}

// tokenIn returns the invocation token in environ, the content of a
// process's /proc/<pid>/environ.
func (h shownHarness) tokenIn(environ []byte) (gatewayToken, bool) {
	for kv := range strings.SplitSeq(string(environ), "\x00") {
		for _, v := range h.tokens {
			if tok, ok := strings.CutPrefix(kv, v.prefix); ok && tok != "" {
				return gatewayToken{value: tok, header: v.header}, true
			}
		}
	}
	return gatewayToken{}, false
}

// shownModels is the model configuration of the shown runs, with the
// adapter of the harness that PUMI_SHOWN_HARNESS names at path (decision
// 0245): in the API key mode when PUMI_SHOWN_API_KEY_ENV names the key's
// variable, and in the sign-in mode otherwise, running the model role and
// the checks.
func shownModels(path string) (*configv1.Models, error) {
	h, err := shownHarnessOf()
	if err != nil {
		return nil, err
	}
	hash, err := hashOf(path)
	if err != nil {
		return nil, err
	}
	a := &configv1.Adapter{Executable: path, Sha256: hash, CredentialMode: configv1.Adapter_CREDENTIAL_MODE_SIGN_IN}
	if key := os.Getenv(envProviderVariable); key != "" {
		a.CredentialMode, a.ApiKeyEnv = configv1.Adapter_CREDENTIAL_MODE_API_KEY, key
	}
	return &configv1.Models{
		Adapters: map[string]*configv1.Adapter{h.name: a},
		Roles: map[string]*configv1.Role{shownRole: {
			Adapter: h.name, Model: h.model,
			Fallback: &configv1.Fallback{Adapter: h.name, Model: h.fallback},
		}},
		CheckAdapter: h.name,
	}, nil
}

// hashOf returns the SHA-256 of the file at path, an absolute path, in
// lowercase hexadecimal, read through a root of its directory (gosec G304).
func hashOf(path string) (string, error) {
	root, err := os.OpenRoot(filepath.Dir(path))
	if err != nil {
		return "", err
	}
	defer func() { _ = root.Close() }()
	b, err := root.ReadFile(filepath.Base(path))
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:]), nil
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

// tokenOf returns the gateway token in the environment of the harness's
// process in the workspace, read from /proc while the agent run runs, so
// that the test can look for it in the logs. It never shows the token.
func (s *shownCommands) tokenOf(b *background) gatewayToken {
	s.t.Helper()
	h, err := shownHarnessOf()
	if err != nil {
		s.t.Fatal(err)
	}
	deadline := time.Now().Add(commandWait)
	for {
		for _, p := range inDir(s.run.WorkDir) {
			pid, name, _ := strings.Cut(p, " ")
			if name != "("+h.process+")" {
				continue
			}
			if tok, ok := h.tokenIn(environOf(pid)); ok {
				return tok
			}
		}
		select {
		case err := <-b.done:
			b.done <- err
			s.t.Fatalf("pumi run stopped with %v before %s ran; its output:\n%s", err, h.process, b.out.String())
		default:
		}
		if time.Now().After(deadline) {
			s.t.Fatalf("no %s process with a gateway token ran in the workspace within %s", h.process, commandWait)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// environOf returns the environment of the process pid, read through an
// os.Root, which gosec does not report (G304), or nothing when it cannot
// be read.
func environOf(pid string) []byte {
	root, err := os.OpenRoot("/proc")
	if err != nil {
		return nil
	}
	defer func() { _ = root.Close() }()
	b, err := root.ReadFile(pid + "/environ")
	if err != nil {
		return nil
	}
	return b
}

// checkTokenPath fails the test unless the harness sends the token as it
// does on the API key path, where the gateway sends the API key
// (decisions 0225 and 0240): claude as its bearer token, codex in the
// header X-Pumi-Token.
func checkTokenPath(t *testing.T, invocation string, tok gatewayToken) {
	t.Helper()
	h, err := shownHarnessOf()
	if err != nil {
		t.Fatal(err)
	}
	keyEnv := os.Getenv(envProviderVariable)
	switch {
	case keyEnv != "" && tok.header == h.keyPathHeader:
		t.Logf("%s of %s sends the gateway token (in the header: %v); the gateway sends the API key of %s", h.process, invocation, tok.header, keyEnv)
	default:
		t.Errorf("%s of %s sends the gateway token in the header %v, but %s is %q", h.process, invocation, tok.header, envProviderVariable, keyEnv)
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
//
// Since decision 0240 only an adapter in the API key mode goes through the
// gateway, so the test is skipped unless PUMI_SHOWN_API_KEY_ENV names the
// key's variable; the shown run on a sign-in is the Claude Code adapter's
// (getpumi/adapter-claude-code#5).
func TestShownRunsThroughTheGateway(t *testing.T) {
	if os.Getenv(envProviderVariable) == "" {
		t.Skipf("%s does not name the provider's API key; only the API key mode goes through the gateway (decision 0240)", envProviderVariable)
	}
	clone, plan, adapter := shownSetup(t)
	s := prepareCommands(t, clone, plan, adapter)
	s.writeCappedConfig()
	ctx := t.Context()

	s.pumi(exec.CommandContext(ctx, "pumi", "add", "--config", "../pumi.txtpb", "--track", "counted", shownCountedPlan))
	first := s.start(exec.CommandContext(ctx, "pumi", "run", "--config", "../pumi.txtpb"))
	s.waitLog(first, commandWait, "invocation.started of the first plan", ofTypeFor(events.TypeInvocationStarted, shownCountedPlan+"/1"))
	counted := s.tokenOf(first)
	checkTokenPath(t, shownCountedPlan+"/1", counted)
	s.waitCountedDone(first, shownCountedPlan)
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
	checkCounted(t, evs, shownCountedPlan)
	checkCapped(t, evs, shownCappedPlan)
	showChange(t, s.run, evs, shownCountedPlan+"/1")
}

// waitCountedDone waits until plan, the first plan, reaches done. When the plan
// escalates first, as it does when its agent run fails, it shows pumi
// status and the log of each of the plan's invocations, and fails the test
// at once. The configuration allows one more attempt, so the plan has at
// most two. The command's arguments are constants, because gosec reports a
// command built from variables (G204).
func (s *shownCommands) waitCountedDone(b *background, plan string) {
	s.t.Helper()
	done, raised := s.entered(plan, "done"), ofTypeFor(events.TypeEscalationRaised, plan)
	s.waitLog(b, runTimeout, "transition of the first plan to done or its escalation", func(e *cloudeventsv1.CloudEvent) bool {
		return done(e) || raised(e)
	})
	evs := readLog(s.t, s.run)
	for _, e := range evs {
		if done(e) {
			return
		}
	}
	ctx := s.t.Context()
	s.pumi(exec.CommandContext(ctx, "pumi", "status"))
	s.pumi(countedLog(ctx, plan, false))
	if has(ofTypesFor(evs, plan+"/2"), events.TypeInvocationStarted) {
		s.pumi(countedLog(ctx, plan, true))
	}
	s.t.Fatalf("%s escalated before it reached done; pumi status and the logs above say why", plan)
}

// countedLog returns the pumi log command of the first or, with second, the
// second invocation of plan, one of the counted plans of the shown runs. Each
// command's arguments are constants, because gosec reports a command built
// from variables (G204).
func countedLog(ctx context.Context, plan string, second bool) *exec.Cmd {
	switch {
	case plan == signInCountedPlan && second:
		return exec.CommandContext(ctx, "pumi", "log", signInCountedPlan+"/2")
	case plan == signInCountedPlan:
		return exec.CommandContext(ctx, "pumi", "log", signInCountedPlan+"/1")
	case second:
		return exec.CommandContext(ctx, "pumi", "log", shownCountedPlan+"/2")
	default:
		return exec.CommandContext(ctx, "pumi", "log", shownCountedPlan+"/1")
	}
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

// checkCounted checks plan, the first plan: its invocation succeeded with
// the tokens and cost that the gateway or the runner counted, and the plan
// reached done.
func checkCounted(t *testing.T, evs []*cloudeventsv1.CloudEvent, plan string) {
	t.Helper()
	inv := plan + "/1"
	if got, want := strings.Join(planFired(t, evs, plan), ", "), "implementing -result-> done"; got != want {
		t.Errorf("the transitions of %s are %s, want %s", plan, got, want)
	}
	if o := outcomeOf(t, evs, inv); o != eventsv1.InvocationFinished_OUTCOME_SUCCEEDED {
		t.Errorf("%s finished as %v, want succeeded", inv, o)
	}
	u := resultOf(t, evs, inv).GetUsage()
	if u.GetTokens() <= 0 || u.GetCostMicrodollars() <= 0 {
		t.Errorf("the usage of %s is %v, want the tokens and cost that were counted", inv, u)
	}
	t.Logf("the result of %s: %d tokens, %d microdollars, %d s", inv, u.GetTokens(), u.GetCostMicrodollars(), u.GetSeconds())
}

// checkCapped checks plan, the second plan: its invocation's usage is
// over the token cap, it finished with the outcome cap reached, and the
// plan went to implementing-escalated on cap-reached.
func checkCapped(t *testing.T, evs []*cloudeventsv1.CloudEvent, plan string) {
	t.Helper()
	inv := plan + "/1"
	if got, want := strings.Join(planFired(t, evs, plan), ", "), "implementing -cap-reached-> implementing-escalated"; got != want {
		t.Errorf("the transitions of %s are %s, want %s", plan, got, want)
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
