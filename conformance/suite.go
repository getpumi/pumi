// SPDX-License-Identifier: Apache-2.0
// SPDX-FileCopyrightText: The Pumi Authors

package conformance

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"os"
	"slices"
	"testing"
	"time"

	harnessv1 "getpumi.dev/pumi/api/gen/pumi/harness/v1"
)

// T is what a scenario needs of a test. *testing.T has it.
type T interface {
	Helper()
	Context() context.Context
	Logf(format string, args ...any)
	Errorf(format string, args ...any)
	Fatalf(format string, args ...any)
	Skipf(format string, args ...any)
	Failed() bool
	Cleanup(func())
	TempDir() string
}

// Run runs every scenario against the adapter a, each as a parallel
// subtest named after it.
func Run(t *testing.T, a Adapter) {
	t.Helper()
	if err := a.validate(); err != nil {
		t.Fatal(err)
	}
	for _, s := range scenarios {
		t.Run(s.name, func(t *testing.T) {
			t.Parallel()
			play(t, &a, s)
		})
	}
}

// The model role of every invocation, and the models of the role on the
// sign-in path (decision 0254).
const (
	modelRole     = "frontier"
	model         = "pumi-conformance-model"
	fallbackModel = "pumi-conformance-fallback-model"
)

// world is what one scenario runs in.
type world struct {
	a    *Adapter
	mode Mode
	// workspace is the invocation's workspace, and outside a directory
	// outside it.
	workspace, outside string
	tree               string
	// nonce is in the task, secret is in secretFile, token is the
	// invocation token on the API key path and credential the person's
	// sign-in.
	nonce, secret, token, credential string
	provider                         *provider
	// elsewhere is where SignIn points the harness in the API key mode,
	// and host the server that fetches go to. No call may reach either.
	elsewhere, host *recorder
}

// random returns n random bytes in hex.
func random(t T, n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		t.Fatalf("conformance: %v", err)
	}
	return hex.EncodeToString(b)
}

// newWorld makes the workspace and the servers of a scenario.
func newWorld(t T, a *Adapter, mode Mode) *world {
	t.Helper()
	w := &world{
		a: a, mode: mode, workspace: t.TempDir(), outside: t.TempDir(),
		nonce:      "pumi-conformance-" + random(t, 8),
		secret:     "pumi-conformance-secret-" + random(t, 8),
		token:      "pumi-conformance-token-" + random(t, 16),
		credential: "pumi-conformance-sign-in-" + random(t, 16),
		elsewhere:  newRecorder(), host: newRecorder(),
	}
	t.Cleanup(w.elsewhere.server.Close)
	t.Cleanup(w.host.server.Close)
	tree, err := makeWorkspace(t.Context(), w.workspace, w.secret)
	if err != nil {
		t.Fatalf("conformance: the workspace could not be made: %v", err)
	}
	w.tree = tree
	return w
}

// task is the invocation's task. The fake provider recognizes the
// harness's conversation by the nonce in it.
func (w *world) task() string {
	return "This is a conformance run of Pumi (" + w.nonce + "). Make the tool calls you are given, then stop."
}

// invocation returns the harness invocation of the scenario s with the
// capability list caps.
func (w *world) invocation(s scenario, caps *harnessv1.Capabilities) *harnessv1.Invocation {
	inv := &harnessv1.Invocation{
		Invocation:     "pumi-conformance-" + s.name,
		IdempotencyKey: w.nonce,
		Component:      &harnessv1.Component{Kind: harnessv1.Component_KIND_AGENT_RUN, Name: "conformance", Version: "1"},
		Input:          &harnessv1.Input{Repo: "pumi-conformance", Tree: w.tree, Task: w.task()},
		Role:           harnessv1.Role_ROLE_AGENT,
		Capabilities:   caps,
		Budget:         &harnessv1.Usage{Tokens: 10_000_000, CostMicrodollars: 1_000_000_000},
		Workspace:      w.workspace,
		ModelRole:      modelRole,
	}
	if w.mode == ModeAPIKey {
		inv.Gateway = &harnessv1.Gateway{BaseUrl: w.provider.URL(), Token: w.token}
	} else {
		inv.Model, inv.FallbackModel = model, fallbackModel
	}
	if s.output != nil {
		inv.OutputSchema = &harnessv1.OutputSchema{Message: s.output.message, JsonSchema: s.output.schema}
	}
	return inv
}

// env returns the adapter's environment: the suite's, the adapter's own,
// the person's sign-in, which in the API key mode points elsewhere, and
// more.
func (w *world) env(more []string) []string {
	env := slices.Concat(os.Environ(), w.a.Env)
	if w.a.SignIn != nil {
		base := w.provider.URL()
		if w.mode == ModeAPIKey {
			base = w.elsewhere.server.URL
		}
		env = append(env, w.a.SignIn(base, w.credential)...)
	}
	return append(env, more...)
}

// play runs the scenario s against the adapter a.
func play(t T, a *Adapter, s scenario) {
	t.Helper()
	mode := s.mode
	switch {
	case mode == 0:
		mode = a.Modes[0]
	case !slices.Contains(a.Modes, mode):
		t.Skipf("the adapter does not offer the %v", mode)
	}
	if reason, ok := a.Unenforced[s.item]; ok && s.item != 0 {
		t.Skipf("the adapter's harness cannot refuse what the capability list leaves out of %v (%s); the runner's confinement check covers it (decision 0159)", s.item, reason)
	}
	if s.limits && !a.ReportsLimits {
		t.Skipf("the adapter does not say that its harness reports a provider's limit")
	}
	w := newWorld(t, a, mode)
	var plan []planned
	if s.steps != nil {
		plan = s.steps(w)
	}
	w.provider = newProvider(a.API, w.nonce, turns(t, a, plan), s.behavior)
	t.Cleanup(w.provider.close)
	caps := &harnessv1.Capabilities{Network: &harnessv1.Capabilities_Network{Access: harnessv1.Capabilities_Network_ACCESS_GATEWAY}}
	if s.caps != nil {
		caps = s.caps(w)
	}
	var more []string
	if s.env != nil {
		more = s.env(a)
	}
	o := runAdapter(t, w, w.invocation(s, caps), w.env(more), s.behavior == holdCall)
	if o == nil {
		return
	}
	checkContract(t, s, o)
	checkCalls(t, w)
	checkPlan(t, w, plan, o)
	if s.check != nil {
		s.check(t, w, o)
	}
	if t.Failed() {
		t.Logf("the adapter's log (its standard error):\n%s", o.stderr)
	}
}

// turns returns the fake provider's script: the adapter's turn for each
// step, then a turn that ends the run.
func turns(t T, a *Adapter, plan []planned) []Turn {
	t.Helper()
	out := make([]Turn, 0, len(plan)+1)
	for _, p := range plan {
		turn := a.Turn(p.step)
		for _, c := range turn.Calls {
			if c.Name == "" || (!c.Freeform && !json.Valid([]byte(c.Input))) || (c.Freeform && a.API != APIResponses) {
				t.Fatalf("conformance: Turn(%v step) gave the tool call %+v, which needs a name and an input that is JSON, or is freeform only on the Responses API", p.step.Kind, c)
			}
		}
		out = append(out, turn)
	}
	return append(out, Turn{Text: "done"})
}

// runAdapter starts the adapter, waits for it to end and returns how it
// ended. When stop is set, it closes the adapter's standard input once
// the fake provider holds a call. It returns nil when it had to kill the
// adapter.
func runAdapter(t T, w *world, inv *harnessv1.Invocation, env []string, stop bool) *outcome {
	t.Helper()
	p, err := start(w.a.Path, w.workspace, env, inv, w.a.stopGrace())
	if err != nil {
		t.Fatalf("conformance: %v", err)
	}
	t.Cleanup(p.end)
	if stop {
		return stopAdapter(t, w, p)
	}
	if !p.waitFor(w.a.timeout()) {
		t.Errorf("the adapter did not exit within %v", w.a.timeout())
		p.closeStdin()
		w.provider.end()
		if !p.waitFor(w.a.stopGrace()) {
			p.kill()
		}
		t.Logf("the adapter's log (its standard error):\n%s", p.outcome().stderr)
		return nil
	}
	return p.outcome()
}

// stopAdapter waits until the fake provider holds a call, closes the
// adapter's standard input and checks that it exits within the stop grace
// period (decision 0158).
func stopAdapter(t T, w *world, p *process) *outcome {
	t.Helper()
	timer := time.NewTimer(w.a.timeout())
	defer timer.Stop()
	select {
	case <-w.provider.held:
	case <-p.done:
		t.Errorf("the adapter exited before its harness made a model call that the suite could hold")
		return p.outcome()
	case <-timer.C:
		t.Errorf("the adapter's harness made no model call that carries the task within %v", w.a.timeout())
		p.kill()
		return nil
	}
	p.closeStdin()
	if !p.waitFor(w.a.stopGrace()) {
		t.Errorf("the adapter did not exit within %v of its standard input closing (decision 0158)", w.a.stopGrace())
		p.kill()
		t.Logf("the adapter's log (its standard error):\n%s", p.outcome().stderr)
		return nil
	}
	return p.outcome()
}

// checkContract checks the process contract: exit status 0, standard
// output as lines with the result last, and the status the scenario wants
// (decisions 0158 and 0252).
func checkContract(t T, s scenario, o *outcome) {
	t.Helper()
	if o.exitErr != nil {
		t.Errorf("the adapter exited with %v, want exit status 0 with a result (decision 0158)", o.exitErr)
	}
	if o.protocolErr != nil {
		t.Errorf("%v (decision 0252)", o.protocolErr)
	}
	if o.result == nil {
		return
	}
	if got := o.result.GetStatus(); got != s.status {
		t.Errorf("the result's status is %v, want %v", got, s.status)
	}
	if s.output == nil && o.result.GetOutput() != "" {
		t.Errorf("the result has the structured output %q, but the invocation names no output schema", o.result.GetOutput())
	}
}
