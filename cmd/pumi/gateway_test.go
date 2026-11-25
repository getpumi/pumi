// SPDX-License-Identifier: Apache-2.0
// SPDX-FileCopyrightText: The Pumi Authors

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"getpumi.dev/pumi/api/events"
	eventsv1 "getpumi.dev/pumi/api/gen/pumi/events/v1"
	harnessv1 "getpumi.dev/pumi/api/gen/pumi/harness/v1"
	"getpumi.dev/pumi/api/policy"
	"getpumi.dev/pumi/internal/gateway"
	"google.golang.org/protobuf/encoding/protojson"
)

// The calls of the test adapter, and what the fake provider answers.
const (
	// testModel is the model of each call, which the price table lists:
	// $2 for a million input tokens and $10 for a million output tokens.
	testModel = "claude-sonnet-5-5"
	// testFallback is the fallback model of the test clones' model role.
	testFallback             = "claude-haiku-5-5"
	sonnetInputMicrodollars  = 2
	sonnetOutputMicrodollars = 10
	// testInputTokens are the input tokens of each answer.
	testInputTokens = 10
	// testOutputTokens are the output tokens of one call of a mode that
	// makes one.
	testOutputTokens = 100
	// capCallOutputTokens are the output tokens of each call of
	// adapterCallsUntilRefused, $1 each, so that the fifth call reaches
	// the budget's $5.
	capCallOutputTokens = 100_000
	// harnessSignIn is the credential of the harness's own sign-in that
	// the test adapter sends to the provider on the sign-in path.
	harnessSignIn = "Bearer the-harness-sign-in"
)

// callCost is the cost in microdollars of a call with outputTokens output
// tokens.
func callCost(outputTokens int64) int64 {
	return testInputTokens*sonnetInputMicrodollars + outputTokens*sonnetOutputMicrodollars
}

// testProviderEnv names the variable that holds the fake provider's base
// URL, which the test adapter calls directly on the sign-in path.
const testProviderEnv = "PUMI_TEST_PROVIDER_URL"

// callModel makes one call of the Messages API for outputTokens output
// tokens and returns its status: through the gateway of inv, with the
// token as its API key, when inv has one (decision 0225), and otherwise
// on the sign-in path to the fake provider directly, with the harness's own
// sign-in and the model that inv names, passing the call's usage to the
// runner as a figure (decisions 0240, 0241 and 0254).
func callModel(inv *harnessv1.Invocation, outputTokens int) (int, error) {
	g := inv.GetGateway()
	// Through the gateway, the harness asks for the invocation's model
	// role, which the gateway maps to testModel (decision 0233).
	model, base, client := inv.GetModelRole(), g.GetBaseUrl(), http.DefaultClient
	if g == nil {
		model, base, client = inv.GetModel(), "http://provider", providerClient()
	}
	if model == "" {
		return 0, fmt.Errorf("the invocation %s has neither a gateway nor the model of its role", inv.GetInvocation())
	}
	body := fmt.Sprintf(`{"model":%q,"max_tokens":%d,"messages":[{"role":"user","content":"Implement the plan."}]}`, model, outputTokens)
	ctx, cancel := context.WithTimeout(context.Background(), commandWait)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/v1/messages", strings.NewReader(body))
	if err != nil {
		return 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	if g != nil {
		req.Header.Set("X-Api-Key", g.GetToken())
	} else {
		req.Header.Set("Authorization", harnessSignIn)
	}
	res, err := client.Do(req)
	if err != nil {
		return 0, err
	}
	defer func() { _ = res.Body.Close() }()
	answer, err := io.ReadAll(res.Body)
	if err != nil {
		return 0, err
	}
	if g == nil && res.StatusCode == http.StatusOK {
		if err := passFigure(answer); err != nil {
			return 0, err
		}
	}
	return res.StatusCode, nil
}

// providerClient returns a client whose every connection goes to the
// address of the fake provider in testProviderEnv, whatever the URL's host,
// so that the URL of a call is constant (gosec G704).
func providerClient() *http.Client {
	addr := strings.TrimPrefix(os.Getenv(testProviderEnv), "http://")
	dialer := &net.Dialer{}
	return &http.Client{Transport: &http.Transport{
		DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
			return dialer.DialContext(ctx, network, addr)
		},
	}}
}

// passFigure writes the usage of the provider's answer to standard output
// as a usage figure (decision 0252).
func passFigure(answer []byte) error {
	var a struct {
		Model string `json:"model"`
		Usage struct {
			InputTokens  int64 `json:"input_tokens"`
			OutputTokens int64 `json:"output_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(answer, &a); err != nil {
		return err
	}
	out, err := protojson.Marshal(&harnessv1.Line{Kind: &harnessv1.Line_Usage{Usage: &harnessv1.UsageFigure{
		Model: a.Model, InputTokens: a.Usage.InputTokens, OutputTokens: a.Usage.OutputTokens,
	}}})
	if err != nil {
		return err
	}
	fmt.Println(string(out))
	return nil
}

// callOnce makes one model call and logs why when it does not succeed.
func callOnce(inv *harnessv1.Invocation, outputTokens int) bool {
	status, err := callModel(inv, outputTokens)
	if err != nil || status != http.StatusOK {
		fmt.Fprintf(os.Stderr, "%sthe model call gave %d, %v\n", testAdapterPrefix, status, err)
		return false
	}
	return true
}

// adapterCalls makes the model calls of the test adapter's mode for inv,
// and reports whether they went as the mode expects. closed is closed when
// the runner closes standard input.
func adapterCalls(mode string, inv *harnessv1.Invocation, closed <-chan struct{}) bool {
	switch mode {
	case adapterSucceeds, adapterReportsAFile:
		return callOnce(inv, testOutputTokens)
	case adapterOverCaps:
		// The output tokens alone cost the hard cost cap.
		return callOnce(inv, int(policy.DefaultHardCaps().CostMicrodollars/sonnetOutputMicrodollars))
	case adapterCallsUntilRefused:
		for n := 1; ; n++ {
			status, err := callModel(inv, capCallOutputTokens)
			if err != nil {
				fmt.Fprintln(os.Stderr, testAdapterPrefix+"the call failed:", err)
				return false
			}
			if status != http.StatusOK {
				fmt.Fprintf(os.Stderr, "%sthe gateway refused call %d with the status %d\n", testAdapterPrefix, n, status)
				return true
			}
			// On the sign-in path the runner stops the run by closing
			// standard input once the figures reach the cap; the wait lets
			// it read the figure before the next call (decision 0241).
			if inv.GetGateway() != nil {
				continue
			}
			select {
			case <-closed:
				fmt.Fprintf(os.Stderr, "%sthe runner stopped the run after call %d\n", testAdapterPrefix, n)
				return true
			case <-time.After(callPause):
			}
		}
	}
	return true
}

// callPause is how long the mode adapterCallsUntilRefused waits after each
// call for the runner to stop it.
const callPause = 500 * time.Millisecond

// providerCall is what the fake provider received of one call.
type providerCall struct {
	header http.Header
	body   []byte
}

// provider is the fake of Anthropic's API that the gateway of the test
// clones' pumi run sends calls to.
type provider struct {
	mu    sync.Mutex
	calls []providerCall
	// limited are the models whose calls it answers with a rate limit.
	limited map[string]bool
}

// fakeProvider serves a new fake provider, makes pumi run's gateway send
// its calls there for the test and returns it.
func fakeProvider(t *testing.T) *provider {
	t.Helper()
	p := &provider{}
	srv := httptest.NewServer(p)
	t.Cleanup(srv.Close)
	old := providerURL
	providerURL = srv.URL
	t.Setenv(testProviderEnv, srv.URL)
	t.Cleanup(func() { providerURL = old })
	return p
}

// ServeHTTP answers a call of the Messages API with testInputTokens input
// tokens and as many output tokens as the call's max_tokens.
func (p *provider) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil || r.URL.Path != "/v1/messages" {
		http.Error(w, "not a call of the Messages API", http.StatusBadRequest)
		return
	}
	var req struct {
		Model     string `json:"model"`
		MaxTokens int64  `json:"max_tokens"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	p.mu.Lock()
	p.calls = append(p.calls, providerCall{header: r.Header.Clone(), body: body})
	limited := p.limited[req.Model]
	p.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	if limited {
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = io.WriteString(w, `{"type":"error","error":{"type":"rate_limit_error","message":"This request would exceed your account's rate limit."}}`)
		return
	}
	_, _ = fmt.Fprintf(w, `{"id":"msg_1","type":"message","role":"assistant","model":%q,"content":[{"type":"text","text":"Done."}],"stop_reason":"end_turn","usage":{"input_tokens":%d,"output_tokens":%d}}`,
		req.Model, testInputTokens, req.MaxTokens)
}

// received returns the calls the provider received.
func (p *provider) received() []providerCall {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]providerCall(nil), p.calls...)
}

// resultOf returns the one invocation.result of the clone's log.
func (c *commandClone) resultOf(t *testing.T) *eventsv1.InvocationResult {
	t.Helper()
	results := ofType(c.log(), events.TypeInvocationResult)
	if len(results) != 1 {
		t.Fatalf("the log has %d invocation.result events, want 1", len(results))
	}
	return dataOf(t, results[0], &eventsv1.InvocationResult{})
}

// TestRunCountsTheUsageAtTheGateway: pumi run serves the gateway, and the
// usage of the result is what the gateway counted, not what the adapter
// reported (decisions 0230 and 0231).
func TestRunCountsTheUsageAtTheGateway(t *testing.T) {
	c := newCommandClone(t, "tracks", adapterSucceeds)
	r := c.start()
	c.must("add", "--track", "a", "a1")
	c.waitState(r, "a1", "merged")
	r.stop(t)
	u := c.resultOf(t).GetUsage()
	if u.GetTokens() != testInputTokens+testOutputTokens || u.GetCostMicrodollars() != callCost(testOutputTokens) || u.GetSeconds() < 1 {
		t.Errorf("the usage is %v, want the gateway's count of one call, %d tokens and %d microdollars, and the measured seconds",
			u, testInputTokens+testOutputTokens, callCost(testOutputTokens))
	}
	if n := len(c.provider.received()); n != 1 {
		t.Fatalf("the provider received %d calls, want 1", n)
	}
	if !strings.Contains(r.out.String(), "the adapter reported 10 tokens, 20 microdollars and 1 s") {
		t.Errorf("the output of pumi run does not hold the adapter's own figures; it is\n%s", r.out.String())
	}
}

// testKeyEnv is the environment variable that holds the API key in the
// configuration file of TestRunSendsTheAPIKeyOfTheNamedVariable.
const testKeyEnv = "PUMI_TEST_PROVIDER_KEY"

// TestRunSendsTheAPIKeyOfTheNamedVariable: when the configuration file
// names the API key's environment variable, the gateway sends that key on
// every call, the harness has no sign-in of its own and sends the token as
// its API key, and the token never reaches the provider (decision 0225).
func TestRunSendsTheAPIKeyOfTheNamedVariable(t *testing.T) {
	t.Setenv(testKeyEnv, testProviderKey)
	c := newCommandCloneWith(t, "tracks", adapterSucceeds, testModels(t, apiKeyMode))
	r := c.start()
	c.must("add", "--track", "a", "a1")
	c.waitState(r, "a1", "merged")
	r.stop(t)
	calls := c.provider.received()
	if len(calls) != 1 {
		t.Fatalf("the provider received %d calls, want 1", len(calls))
	}
	h := calls[0].header
	if h.Get("X-Api-Key") != "sk-ant-the-adopters-key" || h.Get("Authorization") != "" || h.Get(gateway.InvocationHeader) != "" {
		t.Errorf("the provider received X-Api-Key %q, Authorization %q and %s %q; want the API key only",
			h.Get("X-Api-Key"), h.Get("Authorization"), gateway.InvocationHeader, h.Get(gateway.InvocationHeader))
	}
	if u := c.resultOf(t).GetUsage(); u.GetTokens() != testInputTokens+testOutputTokens {
		t.Errorf("the usage is %v, want the gateway's count of one call", u)
	}
}

// TestRunRefusesAnAPIKeyVariableThatIsNotSet: pumi run does not start when
// the variable that the configuration file names is not set.
func TestRunRefusesAnAPIKeyVariableThatIsNotSet(t *testing.T) {
	t.Setenv(testKeyEnv, "")
	c := newCommandCloneWith(t, "tracks", adapterSucceeds, testModels(t, apiKeyMode))
	ctx, cancel := context.WithTimeout(t.Context(), commandWait)
	defer cancel()
	var out bytes.Buffer
	err := execute(ctx, c.dir, []string{"run", "--config", c.config}, &out, &out)
	if err == nil || !strings.Contains(err.Error(), testKeyEnv) {
		t.Errorf("pumi run = %v; want an error that names %s; its output:\n%s", err, testKeyEnv, out.String())
	}
}

// TestAnAgentRunAtItsCostCapEscalates: the gateway refuses the call after
// the one that reaches the budget's cost, the runner reports cap reached
// although the adapter completed, with the gateway's totals, and the plan
// escalates on cap_reached (decisions 0203, 0228 and 0230).
func TestAnAgentRunAtItsCostCapEscalates(t *testing.T) {
	c := newCommandClone(t, "tracks", adapterCallsUntilRefused)
	r := c.start()
	c.must("add", "--track", "a", "a1")
	c.waitState(r, "a1", "implementing-escalated")
	r.stop(t)
	finished := ofType(c.log(), events.TypeInvocationFinished)
	if len(finished) != 1 || dataOf(t, finished[0], &eventsv1.InvocationFinished{}).GetOutcome() != eventsv1.InvocationFinished_OUTCOME_CAP_REACHED {
		t.Fatalf("the log has the invocation.finished events %v, want one with the outcome cap reached", finished)
	}
	// $1 for each call: the fifth reaches the $5 of the budget, and the
	// gateway refuses the sixth.
	u := c.resultOf(t).GetUsage()
	if u.GetTokens() != 5*(testInputTokens+capCallOutputTokens) || u.GetCostMicrodollars() != 5*callCost(capCallOutputTokens) {
		t.Errorf("the usage is %v, want the gateway's count of five calls", u)
	}
	if want := testAdapterPrefix + "the gateway refused call 6 with the status 403"; !strings.Contains(r.out.String(), want) {
		t.Errorf("the output of pumi run has no line %q; it is\n%s", want, r.out.String())
	}
	if n := len(c.provider.received()); n != 5 {
		t.Errorf("the provider received %d calls, want 5", n)
	}
}

// waitGone waits until pumi run's gateway no longer answers at url.
func waitGone(t *testing.T, url string) {
	t.Helper()
	deadline := time.Now().Add(commandWait)
	for {
		req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, url, nil)
		if err != nil {
			t.Fatal(err)
		}
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			return
		}
		_ = res.Body.Close()
		if time.Now().After(deadline) {
			t.Fatalf("the gateway still answers at %s", url)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// gatewayLine is the line in which pumi run says where its gateway serves.
var gatewayLine = regexp.MustCompile(`The model gateway serves at (http://127\.0\.0\.1:[0-9]+)`)

// TestRunServesTheGatewayUntilItStops: pumi run serves the gateway on
// 127.0.0.1 at a port the system picks, refuses a call with no token, and
// stops serving it when it stops (decision 0231).
func TestRunServesTheGatewayUntilItStops(t *testing.T) {
	c := newCommandClone(t, "tracks", adapterSucceeds)
	r := c.start()
	m := gatewayLine.FindStringSubmatch(r.out.String())
	if m == nil {
		t.Fatalf("the output of pumi run does not say where the gateway serves; it is\n%s", r.out.String())
	}
	if !strings.Contains(r.out.String(), "for "+testAdapterName+" it sends the API key of "+testKeyEnv) {
		t.Errorf("the output of pumi run does not say which API key the gateway sends; it is\n%s", r.out.String())
	}
	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, m[1]+"/v1/messages", strings.NewReader("{}"))
	if err != nil {
		t.Fatal(err)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = res.Body.Close()
	if res.StatusCode != http.StatusUnauthorized {
		t.Errorf("a call with no token got the status %d, want 401", res.StatusCode)
	}
	r.stop(t)
	waitGone(t, m[1])
}
