// SPDX-License-Identifier: Apache-2.0
// SPDX-FileCopyrightText: The Pumi Authors

package gateway_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"

	harnessv1 "getpumi.dev/pumi/api/gen/pumi/harness/v1"
	"getpumi.dev/pumi/internal/gateway"
)

// openAIKey is the adopter's OpenAI API key, which the tests look for
// where it must not be.
const openAIKey = "sk-proj-test-openai-key"

// codexAdapter is the adapter of the invocations that openCodex opens,
// whose harness uses OpenAI's Responses API.
const codexAdapter = "codex"

// The usage of the responses of the tests, in the form of OpenAI's
// Responses API: input_tokens holds the cached input and the cache writes,
// and output_tokens holds the reasoning output.
const (
	// streamedUsage: 1,000 uncached input tokens, 36,000 cached, 4,000
	// written to the cache, 900 output tokens of which 600 are reasoning.
	streamedUsage = `{"input_tokens":41000,"input_tokens_details":{"cached_tokens":36000,"cache_write_tokens":4000},"output_tokens":900,"output_tokens_details":{"reasoning_tokens":600},"total_tokens":41900}`
	// plainUsage: 10,000 uncached input tokens and 1,000 output tokens.
	plainUsage = `{"input_tokens":10000,"input_tokens_details":{"cached_tokens":0,"cache_write_tokens":0},"output_tokens":1000,"output_tokens_details":{"reasoning_tokens":400},"total_tokens":11000}`
)

// streamedResponseTokens and streamedResponseCost are what the gateway
// counts for a stream with streamedUsage on gpt-6.1-sol: 41,000 + 900
// tokens, and 1,000 × $2 + 36,000 × $0.10 + 4,000 × $2.50 (30-minute
// writes) + 900 × $10 per million tokens, $0.0246. plainTokens and
// plainCost are those of plainUsage: 10,000 × $2 + 1,000 × $10 per million
// tokens, $0.03.
const (
	streamedResponseTokens = 41900
	streamedResponseCost   = 24600
	plainTokens            = 11000
	plainCost              = 30000
)

// openAIModel and openAIFallback are a model role's model and fallback of
// the Responses API, which the price table lists.
const (
	openAIRole     = "reviewer"
	openAIModel    = "gpt-6.2-sol"
	openAIFallback = "gpt-6.1-sol"
)

// responseObject is a response of the Responses API with its model, its
// service tier, its usage (JSON, or null) and its output items after the
// message.
func responseObject(model, status, tier, usage string, items ...string) string {
	output := append([]string{`{"id":"msg_1","type":"message","status":"completed","role":"assistant","content":[{"type":"output_text","text":"woof","annotations":[]}]}`}, items...)
	return fmt.Sprintf(`{"id":"resp_1","object":"response","created_at":1795615306,"status":%q,"model":%q,"output":[%s],"service_tier":%q,"usage":%s}`,
		status, model, strings.Join(output, ","), tier, usage)
}

// responseStream is a stream of the Responses API, in the form that the
// fake provider sent one to Codex CLI 0.172.0 on 2026-11-25, whose final
// event is final with the response that responseObject gives.
func responseStream(model, final, tier, usage string, items ...string) string {
	created := fmt.Sprintf(`{"type":"response.created","sequence_number":0,"response":{"id":"resp_1","object":"response","created_at":1795615306,"status":"in_progress","model":%q,"output":[],"service_tier":"auto","usage":null}}`, model)
	status := strings.TrimPrefix(final, "response.")
	last := fmt.Sprintf(`{"type":%q,"sequence_number":3,"response":%s}`, final, responseObject(model, status, tier, usage, items...))
	return "event: response.created\ndata: " + created + "\n\n" +
		"event: response.output_item.added\n" +
		`data: {"type":"response.output_item.added","sequence_number":1,"output_index":0,"item":{"id":"msg_1","type":"message","status":"in_progress","role":"assistant","content":[]}}` + "\n\n" +
		"event: response.output_text.delta\n" +
		`data: {"type":"response.output_text.delta","sequence_number":2,"item_id":"msg_1","output_index":0,"content_index":0,"delta":"woof"}` + "\n\n" +
		"event: " + final + "\ndata: " + last + "\n\n"
}

// responsesCall is the body of a call of the Responses API, with extra
// fields at its end.
func responsesCall(model string, stream bool, extra string) string {
	return fmt.Sprintf(`{"model":%q,"stream":%t,"input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"say woof"}]}]%s}`, model, stream, extra)
}

// answerResponses answers a call of the Responses API with a stream when
// it asks for one, with streamedUsage, and otherwise with a response with
// plainUsage, on the model the call names.
func answerResponses(w http.ResponseWriter, r *http.Request, body []byte) {
	var req struct {
		Model  string `json:"model"`
		Stream bool   `json:"stream"`
	}
	if r.URL.Path != "/v1/responses" || json.Unmarshal(body, &req) != nil {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	if req.Stream {
		w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
		_, _ = io.WriteString(w, responseStream(req.Model, "response.completed", "default", streamedUsage))
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = io.WriteString(w, responseObject(req.Model, "completed", "default", plainUsage))
}

// bearer is what Codex sends when the gateway holds the API key: the
// invocation token as its OpenAI API key.
func bearer(token string) http.Header {
	h := http.Header{}
	h.Set("Authorization", "Bearer "+token)
	return h
}

// openAIFixture returns a fixture whose gateway holds openAIKey for
// codexAdapter, maps openAIRole, and whose waits before a retry return at
// once and are recorded.
func openAIFixture(t *testing.T) (*fixture, *waits) {
	t.Helper()
	f := newFixtureWith(t, gateway.Config{
		APIKeys:   map[string]string{codexAdapter: openAIKey, testAdapter: providerKey},
		Roles:     map[string]gateway.Role{openAIRole: {Model: openAIModel, Fallback: openAIFallback}},
		RetryWait: retryWait,
	})
	w := &waits{}
	gateway.SetWait(f.gw, w.wait)
	return f, w
}

func (f *fixture) openCodex(t *testing.T, budget *harnessv1.Usage) string {
	t.Helper()
	token, err := f.gw.Open("inv-codex", codexAdapter, budget, f.invocationLog)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	return token
}

// do sends a request with the method to the gateway and returns the
// status and the body of the response.
func (f *fixture) do(t *testing.T, method, path string, header http.Header, body string) (int, string) {
	t.Helper()
	req, err := http.NewRequestWithContext(context.Background(), method, f.url+path, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header = header.Clone()
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return resp.StatusCode, string(b)
}

// openAIError reads an error in the form of OpenAI's API, and fails the
// test when the body is not one.
func openAIError(t *testing.T, body string) (kind, message string) {
	t.Helper()
	var e struct {
		Type  *string `json:"type"`
		Error *struct {
			Message string `json:"message"`
			Type    string `json:"type"`
		} `json:"error"`
	}
	if err := json.Unmarshal([]byte(body), &e); err != nil || e.Error == nil || e.Type != nil {
		t.Fatalf("the body %q is not an error in the form of OpenAI's API", body)
	}
	return e.Error.Type, e.Error.Message
}

func TestAResponsesCallIsStreamedThroughAndCounted(t *testing.T) {
	f, _ := openAIFixture(t)
	token := f.openCodex(t, nil)
	sent := responsesCall("gpt-6.1-sol", true, "")

	status, header, body := f.call(t, "/v1/responses", bearer(token), sent)

	if want := responseStream("gpt-6.1-sol", "response.completed", "default", streamedUsage); status != http.StatusOK || body != want {
		t.Fatalf("response = %d %q, want 200 and the provider's stream unchanged", status, body)
	}
	if got := header.Get("Content-Type"); !strings.HasPrefix(got, "text/event-stream") {
		t.Errorf("Content-Type = %q, want text/event-stream", got)
	}
	want := gateway.Totals{Tokens: streamedResponseTokens, CostMicrodollars: streamedResponseCost}
	if got := f.totals(t, token); got != want {
		t.Errorf("Totals() = %+v, want %+v", got, want)
	}
	reqs := f.openai.received()
	if len(reqs) != 1 || reqs[0].path != "/v1/responses" || string(reqs[0].body) != sent {
		t.Fatalf("OpenAI's fake received %+v, want the call unchanged", reqs)
	}
	h := reqs[0].header
	if h.Get("Authorization") != "Bearer "+openAIKey || h.Get("X-Api-Key") != "" || h.Get(gateway.InvocationHeader) != "" {
		t.Errorf("the call reached OpenAI with Authorization %q, X-Api-Key %q and %s %q, want only the OpenAI key as the bearer token",
			h.Get("Authorization"), h.Get("X-Api-Key"), gateway.InvocationHeader, h.Get(gateway.InvocationHeader))
	}
	if n := len(f.provider.received()); n != 0 {
		t.Errorf("Anthropic's fake received %d requests, want 0", n)
	}
}

func TestAResponsesCallThatIsNotStreamedIsCounted(t *testing.T) {
	f, _ := openAIFixture(t)
	token := f.openCodex(t, nil)

	status, _, body := f.call(t, "/v1/responses", bearer(token), responsesCall("gpt-6.1-sol", false, ""))

	if status != http.StatusOK || body != responseObject("gpt-6.1-sol", "completed", "default", plainUsage) {
		t.Fatalf("response = %d %q, want 200 and the provider's response unchanged", status, body)
	}
	want := gateway.Totals{Tokens: plainTokens, CostMicrodollars: plainCost}
	if got := f.totals(t, token); got != want {
		t.Errorf("Totals() = %+v, want %+v", got, want)
	}
}

// TestEachAdapterReachesItsOwnProvider: the calls of an invocation go to
// the provider of its adapter's API, each with its own key.
func TestEachAdapterReachesItsOwnProvider(t *testing.T) {
	f, _ := openAIFixture(t)
	codex := f.openCodex(t, nil)
	claude := f.open(t, nil)

	if status, _, body := f.call(t, "/v1/messages", keyHeader(claude), messages("claude-sonnet-5-5", false)); status != http.StatusOK {
		t.Fatalf("the Messages call = %d %q, want 200", status, body)
	}
	if status, _, body := f.call(t, "/v1/responses", bearer(codex), responsesCall("gpt-6.1-sol", false, "")); status != http.StatusOK {
		t.Fatalf("the Responses call = %d %q, want 200", status, body)
	}
	assertOnlyTheKeyReachedTheProvider(t, f.provider)
	if reqs := f.openai.received(); len(reqs) != 1 || reqs[0].header.Get("Authorization") != "Bearer "+openAIKey {
		t.Errorf("OpenAI's fake received %+v, want one call with the OpenAI key", reqs)
	}
	if got := f.totals(t, claude); got.Tokens != nonStreamedTokens {
		t.Errorf("the Messages invocation's Totals() = %+v, want %d tokens", got, nonStreamedTokens)
	}
	if got := f.totals(t, codex); got.Tokens != plainTokens {
		t.Errorf("the Responses invocation's Totals() = %+v, want %d tokens", got, plainTokens)
	}
}

// TestAResponsesCallIsPricedAsOpenAIBillsIt: the gateway prices a call
// from its final event, at the rates of the service tier the response
// reports, the cache writes at the lifetime the call asks for, prompts
// over 272,000 input tokens at the long-context rates, and each web
// search.
func TestAResponsesCallIsPricedAsOpenAIBillsIt(t *testing.T) {
	const webSearch = `{"id":"ws_1","type":"web_search_call","status":"completed","action":{"type":"search","query":"pumi"}}`
	long := `{"input_tokens":300000,"input_tokens_details":{"cached_tokens":0,"cache_write_tokens":0},"output_tokens":1000,"output_tokens_details":{"reasoning_tokens":0},"total_tokens":301000}`
	atLimit := `{"input_tokens":272000,"input_tokens_details":{"cached_tokens":0,"cache_write_tokens":0},"output_tokens":1000,"output_tokens_details":{"reasoning_tokens":0},"total_tokens":273000}`
	writes := `{"input_tokens":10000,"input_tokens_details":{"cached_tokens":0,"cache_write_tokens":10000},"output_tokens":1000,"output_tokens_details":{"reasoning_tokens":0},"total_tokens":11000}`
	tests := []pricedCall{
		{name: "standard", final: "response.completed", tier: "default", usage: plainUsage, tokens: plainTokens, cost: plainCost},
		{name: "fast mode, requested as priority", extra: `,"service_tier":"priority"`, final: "response.completed", tier: "priority", usage: plainUsage, tokens: plainTokens, cost: 2 * plainCost},
		{name: "fast mode", final: "response.completed", tier: "fast", usage: plainUsage, tokens: plainTokens, cost: 2 * plainCost},
		// gpt-6.1-sol's Flex rates are $1 and $5.
		{name: "flex", final: "response.completed", tier: "flex", usage: plainUsage, tokens: plainTokens, cost: 15_000},
		// gpt-6.1-sol's Ultrafast rates are $12 and $60.
		{name: "ultrafast", final: "response.completed", tier: "ultrafast", usage: plainUsage, tokens: plainTokens, cost: 180_000},
		{name: "a tier the table does not know is priced at the highest", final: "response.completed", tier: "scale", usage: plainUsage, tokens: plainTokens, cost: 180_000},
		// 300,000 × $4 + 1,000 × $15 per million tokens.
		{name: "over 272,000 input tokens", final: "response.completed", tier: "default", usage: long, tokens: 301_000, cost: 1_215_000},
		{name: "over 272,000 input tokens in fast mode", final: "response.completed", tier: "priority", usage: long, tokens: 301_000, cost: 2_430_000},
		{name: "272,000 input tokens", final: "response.completed", tier: "default", usage: atLimit, tokens: 273_000, cost: 554_000},
		// 10,000 × $2.50 + 1,000 × $10 per million tokens.
		{name: "cache writes for 30 minutes", final: "response.completed", tier: "default", usage: writes, tokens: 11_000, cost: 35_000},
		// 10,000 × $5 + 1,000 × $10 per million tokens.
		{name: "cache writes for 12 hours", extra: `,"prompt_cache_options":{"mode":"implicit","ttl":"12h"}`, final: "response.completed", tier: "default", usage: writes, tokens: 11_000, cost: 60_000},
		{name: "a 12-hour breakpoint", extra: `,"prompt_cache_options":{"mode":"explicit"},"tools":[{"type":"function","name":"f","prompt_cache_breakpoint":{"mode":"explicit","ttl":"12h"}}]`, final: "response.completed", tier: "default", usage: writes, tokens: 11_000, cost: 60_000},
		{name: "12h in the prompt's text", extra: `,"instructions":"\"ttl\":\"12h\""`, final: "response.completed", tier: "default", usage: writes, tokens: 11_000, cost: 35_000},
		{name: "web searches", final: "response.completed", tier: "default", usage: plainUsage, items: []string{webSearch, webSearch}, tokens: plainTokens, cost: plainCost + 20_000},
		{name: "an incomplete response", final: "response.incomplete", tier: "default", usage: plainUsage, tokens: plainTokens, cost: plainCost},
		{name: "a failed response with no usage", final: "response.failed", tier: "default", usage: "null"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.check(t, true)
			tt.check(t, false)
		})
	}
}

// pricedCall is a case of TestAResponsesCallIsPricedAsOpenAIBillsIt.
type pricedCall struct {
	name string
	// extra is added to the call's body.
	extra string
	// final, tier, usage and items are those of the response's final
	// event.
	final, tier, usage string
	items              []string
	tokens, cost       int64
}

// check sends the case's call on gpt-6.1-sol, streamed or not, and checks
// the totals.
func (tt pricedCall) check(t *testing.T, stream bool) {
	t.Helper()
	f, _ := openAIFixture(t)
	f.openai.respond = func(w http.ResponseWriter, _ *http.Request, _ []byte) {
		if stream {
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = io.WriteString(w, responseStream("gpt-6.1-sol", tt.final, tt.tier, tt.usage, tt.items...))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, responseObject("gpt-6.1-sol", strings.TrimPrefix(tt.final, "response."), tt.tier, tt.usage, tt.items...))
	}
	token := f.openCodex(t, nil)

	status, _, body := f.call(t, "/v1/responses", bearer(token), responsesCall("gpt-6.1-sol", stream, tt.extra))

	if status != http.StatusOK {
		t.Fatalf("stream %t: response = %d %q, want 200", stream, status, body)
	}
	want := gateway.Totals{Tokens: tt.tokens, CostMicrodollars: tt.cost}
	if got := f.totals(t, token); got != want {
		t.Errorf("stream %t: Totals() = %+v, want %+v", stream, got, want)
	}
}

func TestAResponsesCallAtTheCapIsRefusedInOpenAIsForm(t *testing.T) {
	f, _ := openAIFixture(t)
	token := f.openCodex(t, &harnessv1.Usage{Tokens: plainTokens})

	first, _, _ := f.call(t, "/v1/responses", bearer(token), responsesCall("gpt-6.1-sol", false, ""))
	second, header, body := f.call(t, "/v1/responses", bearer(token), responsesCall("gpt-6.1-sol", false, ""))

	if first != http.StatusOK {
		t.Errorf("the first call = %d, want 200", first)
	}
	if second != http.StatusForbidden {
		t.Errorf("the call after the cap = %d %q, want 403", second, body)
	}
	if kind, message := openAIError(t, body); kind != "permission_error" || !strings.Contains(message, "cap") {
		t.Errorf("the refusal = %s %q, want a permission_error about the cap", kind, message)
	}
	if got := header.Get("X-Should-Retry"); got != "false" {
		t.Errorf("X-Should-Retry = %q, want false", got)
	}
	if n := len(f.openai.received()); n != 1 {
		t.Errorf("OpenAI's fake received %d requests, want 1", n)
	}
	want := gateway.Totals{Tokens: plainTokens, CostMicrodollars: plainCost, CapReached: true}
	if got := f.totals(t, token); got != want {
		t.Errorf("Totals() = %+v, want %+v", got, want)
	}
}

func TestAResponsesCallWithNoValidTokenIsRefused(t *testing.T) {
	f, _ := openAIFixture(t)
	closed := f.openCodex(t, nil)
	if err := f.gw.Close(closed); err != nil {
		t.Fatal(err)
	}
	for name, header := range map[string]http.Header{
		"no token":      {},
		"unknown token": bearer("not-a-token"),
		"closed token":  bearer(closed),
	} {
		t.Run(name, func(t *testing.T) {
			status, _, body := f.call(t, "/v1/responses", header, responsesCall("gpt-6.1-sol", true, ""))
			if status != http.StatusUnauthorized {
				t.Errorf("response = %d %q, want 401", status, body)
			}
			if kind, _ := openAIError(t, body); kind != "authentication_error" {
				t.Errorf("the refusal's type = %q, want authentication_error", kind)
			}
		})
	}
	if n := len(f.openai.received()) + len(f.provider.received()); n != 0 {
		t.Errorf("the providers received %d requests, want 0", n)
	}
}

// TestACallToAModelOfAnotherAPIIsRefused: a call names a model that the
// price table lists for the API it uses, or the gateway refuses it, because
// it could not price it.
func TestACallToAModelOfAnotherAPIIsRefused(t *testing.T) {
	f, _ := openAIFixture(t)
	codex := f.openCodex(t, nil)
	claude := f.open(t, nil)

	for _, model := range []string{"claude-sonnet-5-5", "gpt-unknown"} {
		status, _, body := f.call(t, "/v1/responses", bearer(codex), responsesCall(model, true, ""))
		if status != http.StatusBadRequest || !strings.Contains(body, model) {
			t.Errorf("a Responses call to %s = %d %q, want 400 that names the model", model, status, body)
		}
		if status == http.StatusBadRequest {
			if kind, _ := openAIError(t, body); kind != "invalid_request_error" {
				t.Errorf("the refusal's type = %q, want invalid_request_error", kind)
			}
		}
	}
	status, _, body := f.call(t, "/v1/messages", keyHeader(claude), messages("gpt-6.1-sol", false))
	if status != http.StatusBadRequest || !strings.Contains(body, `"invalid_request_error"`) {
		t.Errorf("a Messages call to gpt-6.1-sol = %d %q, want 400", status, body)
	}
	if n := len(f.openai.received()) + len(f.provider.received()); n != 0 {
		t.Errorf("the providers received %d requests, want 0", n)
	}
}

func TestAResponsesRoleFallsBackOnceOnALimit(t *testing.T) {
	f, w := openAIFixture(t)
	limited := `{"error":{"message":"Rate limit reached for gpt-6.2-sol","type":"requests","param":null,"code":"rate_limit_exceeded"}}`
	n := 0
	f.openai.respond = func(w http.ResponseWriter, r *http.Request, body []byte) {
		n++
		if n == 1 {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = io.WriteString(w, limited)
			return
		}
		answerResponses(w, r, body)
	}
	token := f.openCodex(t, nil)

	status, _, body := f.call(t, "/v1/responses", bearer(token), responsesCall(openAIRole, true, ""))

	if want := responseStream(openAIFallback, "response.completed", "default", streamedUsage); status != http.StatusOK || body != want {
		t.Errorf("response = %d %q, want the fallback's stream", status, body)
	}
	reqs := f.openai.received()
	if len(reqs) != 2 || string(reqs[0].body) != responsesCall(openAIModel, true, "") || string(reqs[1].body) != responsesCall(openAIFallback, true, "") {
		t.Errorf("OpenAI's fake received %+v, want the call on %s and then on %s", reqs, openAIModel, openAIFallback)
	}
	if got := w.durations(); len(got) != 0 {
		t.Errorf("the gateway waited %v before the fallback, want no wait", got)
	}
	if lines := f.logLines("fell back", openAIRole, openAIModel, openAIFallback, "429"); len(lines) != 1 {
		t.Errorf("the invocation's log = %q, want one line about the fallback", f.invocationLog.String())
	}
	// The fallback's tokens are priced at gpt-6.1-sol's rates.
	want := gateway.Totals{Tokens: streamedResponseTokens, CostMicrodollars: streamedResponseCost}
	if got := f.totals(t, token); got != want {
		t.Errorf("Totals() = %+v, want %+v", got, want)
	}
}

func TestAResponsesServerErrorIsRetriedOnceAfterAWait(t *testing.T) {
	f, w := openAIFixture(t)
	n := 0
	f.openai.respond = func(w http.ResponseWriter, r *http.Request, body []byte) {
		n++
		if n == 1 {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = io.WriteString(w, `{"error":{"message":"The server had an error","type":"server_error","param":null,"code":null}}`)
			return
		}
		answerResponses(w, r, body)
	}
	token := f.openCodex(t, nil)

	status, _, body := f.call(t, "/v1/responses", bearer(token), responsesCall(openAIRole, false, ""))

	if status != http.StatusOK || body != responseObject(openAIModel, "completed", "default", plainUsage) {
		t.Errorf("response = %d %q, want the retry's answer", status, body)
	}
	reqs := f.openai.received()
	if len(reqs) != 2 || string(reqs[1].body) != responsesCall(openAIModel, false, "") {
		t.Errorf("OpenAI's fake received %+v, want the call twice on %s", reqs, openAIModel)
	}
	if got := w.durations(); len(got) != 1 || got[0] != retryWait {
		t.Errorf("the gateway waited %v, want %v once", got, retryWait)
	}
	if lines := f.logLines("retries", openAIModel, "500"); len(lines) != 1 {
		t.Errorf("the invocation's log = %q, want one line about the retry", f.invocationLog.String())
	}
}

// TestCodexsOtherRequests: the gateway passes the free requests of the
// Responses API through, and refuses every other request of an
// invocation of the Responses API in OpenAI's form, because it cannot
// count them.
func TestCodexsOtherRequests(t *testing.T) {
	f, _ := openAIFixture(t)
	const list = `{"object":"list","data":[{"id":"gpt-6.1-sol","object":"model","created":1,"owned_by":"openai"}]}`
	f.openai.respond = func(w http.ResponseWriter, r *http.Request, body []byte) {
		if r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/v1/models") {
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, list)
			return
		}
		answerResponses(w, r, body)
	}
	token := f.openCodex(t, nil)

	for _, path := range []string{"/v1/models", "/v1/models?client_version=0.172.0", "/v1/models/gpt-6.1-sol"} {
		status, body := f.do(t, http.MethodGet, path, bearer(token), "")
		if status != http.StatusOK || body != list {
			t.Errorf("GET %s = %d %q, want the provider's answer", path, status, body)
		}
	}
	for _, r := range []struct{ method, path string }{
		{http.MethodPost, "/v1/chat/completions"}, {http.MethodPost, "/v1/responses/compact"},
		{http.MethodPost, "/v1/embeddings"}, {http.MethodGet, "/v1/responses/resp_1"},
		{http.MethodPost, "/v1/messages"}, {http.MethodGet, "/v1/responses"},
		{http.MethodPost, "/v1/models"},
	} {
		f.assertNotPassed(t, r.method, r.path, token)
	}
	reqs := f.openai.received()
	if len(reqs) != 3 {
		t.Errorf("OpenAI's fake received %d requests, want the 3 model lists", len(reqs))
	}
	for _, r := range reqs {
		if r.header.Get("Authorization") != "Bearer "+openAIKey {
			t.Errorf("%s reached OpenAI with Authorization %q, want the OpenAI key", r.path, r.header.Get("Authorization"))
		}
	}
	if got := f.totals(t, token); got != (gateway.Totals{}) {
		t.Errorf("Totals() = %+v, want zero", got)
	}
}

// assertNotPassed checks that the gateway refuses the request with 404 in
// OpenAI's form.
func (f *fixture) assertNotPassed(t *testing.T, method, path, token string) {
	t.Helper()
	status, body := f.do(t, method, path, bearer(token), responsesCall("gpt-6.1-sol", false, ""))
	if status != http.StatusNotFound {
		t.Errorf("%s %s = %d %q, want 404", method, path, status, body)
		return
	}
	if kind, _ := openAIError(t, body); kind != "invalid_request_error" {
		t.Errorf("%s %s: the refusal's type = %q, want invalid_request_error", method, path, kind)
	}
}

// TestABackgroundResponseIsRefused: the gateway cannot count a response
// that runs in the background, whose usage comes only to a later request.
func TestABackgroundResponseIsRefused(t *testing.T) {
	f, _ := openAIFixture(t)
	token := f.openCodex(t, nil)

	status, _, body := f.call(t, "/v1/responses", bearer(token), responsesCall("gpt-6.1-sol", false, `,"background":true`))

	if status != http.StatusBadRequest {
		t.Errorf("response = %d %q, want 400", status, body)
	} else if kind, _ := openAIError(t, body); kind != "invalid_request_error" {
		t.Errorf("the refusal's type = %q, want invalid_request_error", kind)
	}
	if n := len(f.openai.received()); n != 0 {
		t.Errorf("OpenAI's fake received %d requests, want 0", n)
	}
}

func TestNewRefusesARoleOrAnAdapterItCannotUse(t *testing.T) {
	u, err := url.Parse("https://api.openai.com")
	if err != nil {
		t.Fatal(err)
	}
	upstreams := map[gateway.API]*url.URL{gateway.MessagesAPI: u, gateway.ResponsesAPI: u}
	for name, cfg := range map[string]gateway.Config{
		"a role whose models use two APIs": {
			Upstreams: upstreams, Roles: map[string]gateway.Role{openAIRole: {Model: openAIModel, Fallback: "claude-sonnet-5-5"}},
		},
		"an adapter's API with no provider": {
			Upstreams: map[gateway.API]*url.URL{gateway.MessagesAPI: u}, AdapterAPIs: map[string]gateway.API{codexAdapter: gateway.ResponsesAPI},
		},
		"an adapter with an API the gateway does not serve": {
			Upstreams: upstreams, AdapterAPIs: map[string]gateway.API{codexAdapter: "completions"},
		},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := gateway.New(cfg); err == nil {
				t.Error("New() succeeded, want an error")
			}
		})
	}
}

func TestOpenRefusesAnAdapterWithNoAPI(t *testing.T) {
	f, _ := openAIFixture(t)
	if _, err := f.gw.Open("inv-x", "unknown-adapter", nil, nil); err == nil || !strings.Contains(err.Error(), "unknown-adapter") {
		t.Errorf("Open() error = %v, want one that names the adapter", err)
	}
}
