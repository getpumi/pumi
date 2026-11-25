// SPDX-License-Identifier: Apache-2.0
// SPDX-FileCopyrightText: The Pumi Authors

package gateway_test

import (
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	harnessv1 "getpumi.dev/pumi/api/gen/pumi/harness/v1"
	"getpumi.dev/pumi/internal/gateway"
)

// The provider's key and the harness's own sign-in, which the tests look
// for where they must not be.
const (
	providerKey = "sk-ant-api-test-provider-key"
	signIn      = "sk-ant-oat-test-harness-sign-in"
)

// streamed is a streaming response in the form Anthropic's API sent one to
// Claude Code 2.1.331 on 2026-11-24, shortened.
const streamed = "event: message_start\n" +
	`data: {"type":"message_start","message":{"model":"claude-sonnet-5-5","id":"msg_1","type":"message","role":"assistant","content":[],"stop_reason":null,"usage":{"input_tokens":2,"cache_creation_input_tokens":4683,"cache_read_input_tokens":34457,"cache_creation":{"ephemeral_5m_input_tokens":0,"ephemeral_1h_input_tokens":4683},"output_tokens":8,"service_tier":"standard","inference_geo":"global","speed":"standard"}}}` + "\n\n" +
	"event: content_block_start\n" +
	`data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}` + "\n\n" +
	"event: content_block_delta\n" +
	`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Woof!"}}` + "\n\n" +
	"event: content_block_stop\n" +
	`data: {"type":"content_block_stop","index":0}` + "\n\n" +
	"event: message_delta\n" +
	`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"input_tokens":2,"cache_creation_input_tokens":4683,"cache_read_input_tokens":34457,"output_tokens":10}}` + "\n\n" +
	"event: message_stop\n" +
	`data: {"type":"message_stop"}` + "\n\n"

// streamedTokens and streamedCost are what the gateway counts for
// streamed: 2 + 4,683 + 34,457 + 10 tokens, and 2 × $2 + 4,683 × $4 (1-hour
// writes) + 34,457 × $0.10 + 10 × $10 per million tokens,
// $0.0222817, rounded up to whole microdollars.
const (
	streamedTokens = 39152
	streamedCost   = 22282
)

// nonStreamed is a response that is not streamed, in the form Anthropic's
// API sent one to Claude Code 2.1.331 on 2026-11-24.
const nonStreamed = `{"model":"claude-sonnet-5-5","id":"msg_2","type":"message","role":"assistant","content":[{"type":"text","text":"{}"}],"stop_reason":"end_turn","usage":{"input_tokens":4,"cache_creation_input_tokens":0,"cache_read_input_tokens":4828,"cache_creation":{"ephemeral_5m_input_tokens":0,"ephemeral_1h_input_tokens":0},"output_tokens":13,"service_tier":"standard","inference_geo":"global","speed":"standard"}}`

// nonStreamedTokens and nonStreamedCost: 4 + 4,828 + 13 tokens, and 4 × $2
// + 4,828 × $0.10 + 13 × $10 per million tokens, $0.0006208, rounded up.
const (
	nonStreamedTokens = 4845
	nonStreamedCost   = 621
)

// request is one request that the fake provider received.
type request struct {
	method, path string
	header       http.Header
	body         []byte
}

// provider is a fake of Anthropic's API.
type provider struct {
	mu       sync.Mutex
	requests []request
	// respond writes the response to a request.
	respond func(w http.ResponseWriter, r *http.Request, body []byte)
}

func (p *provider) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	p.mu.Lock()
	p.requests = append(p.requests, request{method: r.Method, path: r.URL.RequestURI(), header: r.Header.Clone(), body: body})
	respond := p.respond
	p.mu.Unlock()
	respond(w, r, body)
}

func (p *provider) received() []request {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]request(nil), p.requests...)
}

// answer responds with streamed when the request asks for a stream and
// with nonStreamed otherwise.
func answer(w http.ResponseWriter, _ *http.Request, body []byte) {
	if bytes.Contains(body, []byte(`"stream":true`)) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, streamed)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = io.WriteString(w, nonStreamed)
}

// fixture is a gateway served on the loopback interface in front of a fake
// provider, with its log.
type fixture struct {
	gw       *gateway.Gateway
	url      string
	provider *provider
	log      *bytes.Buffer
	// invocationLog is the log of the invocation that open opens.
	invocationLog *syncBuffer
}

// testAdapter is the adapter of the invocation that open opens.
const testAdapter = "claude-code"

// apiKeys returns the API keys of a gateway that sends key for testAdapter,
// or none when key is empty.
func apiKeys(key string) map[string]string {
	if key == "" {
		return nil
	}
	return map[string]string{testAdapter: key}
}

func newFixture(t *testing.T, apiKey string) *fixture {
	t.Helper()
	return newFixtureWith(t, gateway.Config{APIKeys: apiKeys(apiKey)})
}

// newFixtureWith returns a fixture whose gateway has cfg, with the fake
// provider as its upstream and the fixture's log.
func newFixtureWith(t *testing.T, cfg gateway.Config) *fixture {
	t.Helper()
	p := &provider{respond: answer}
	upstream := httptest.NewServer(p)
	t.Cleanup(upstream.Close)
	u, err := url.Parse(upstream.URL)
	if err != nil {
		t.Fatal(err)
	}
	var logged bytes.Buffer
	cfg.Upstream = u
	cfg.Log = slog.New(slog.NewTextHandler(&syncWriter{w: &logged}, nil))
	gw, err := gateway.New(cfg)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	srv, err := gateway.Serve(t.Context(), gw)
	if err != nil {
		t.Fatalf("Serve() error = %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := srv.Shutdown(ctx); err != nil {
			t.Errorf("Shutdown() error = %v", err)
		}
	})
	return &fixture{gw: gw, url: srv.URL(), provider: p, log: &logged, invocationLog: &syncBuffer{}}
}

// syncWriter serializes writes to a buffer that several goroutines log to.
type syncWriter struct {
	mu sync.Mutex
	w  io.Writer
}

func (s *syncWriter) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.w.Write(p)
}

// syncBuffer is a buffer that several goroutines write to and the test
// reads.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.String()
}

func (f *fixture) open(t *testing.T, budget *harnessv1.Usage) string {
	t.Helper()
	token, err := f.gw.Open("inv-1", testAdapter, budget, f.invocationLog)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	if token == "" {
		t.Fatal("Open() returned an empty token")
	}
	return token
}

func (f *fixture) totals(t *testing.T, token string) gateway.Totals {
	t.Helper()
	totals, err := f.gw.Totals(token)
	if err != nil {
		t.Fatalf("Totals() error = %v", err)
	}
	return totals
}

// messages is the body of a call of the Messages API.
func messages(model string, stream bool) string {
	return fmt.Sprintf(`{"model":%q,"max_tokens":64000,"stream":%t,"messages":[{"role":"user","content":"say woof"}]}`, model, stream)
}

// call sends a request to the gateway as a harness does and returns the
// status and the body of the response.
func (f *fixture) call(t *testing.T, path string, header http.Header, body string) (int, http.Header, string) {
	t.Helper()
	return f.callBody(t, path, header, strings.NewReader(body))
}

func (f *fixture) callBody(t *testing.T, path string, header http.Header, body io.Reader) (int, http.Header, string) {
	t.Helper()
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, f.url+path, body)
	if err != nil {
		t.Fatal(err)
	}
	req.Header = header.Clone()
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Anthropic-Version", "2023-06-01")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST %s: %v", path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("reading the response of %s: %v", path, err)
	}
	return resp.StatusCode, resp.Header, string(b)
}

// signInHeader is what a harness that keeps its own sign-in sends: its
// credential, and the invocation token in the gateway's header.
func signInHeader(token string) http.Header {
	h := http.Header{}
	h.Set("Authorization", "Bearer "+signIn)
	h.Set("Anthropic-Beta", "oauth-2025-04-20")
	h.Set(gateway.InvocationHeader, token)
	return h
}

// keyHeader is what a harness sends when the gateway holds the API key: the
// invocation token as its API key.
func keyHeader(token string) http.Header {
	h := http.Header{}
	h.Set("X-Api-Key", token)
	return h
}

func TestServeListensOnLoopbackAtAPortTheSystemPicks(t *testing.T) {
	f := newFixture(t, "")
	u, err := url.Parse(f.url)
	if err != nil {
		t.Fatalf("URL() = %q: %v", f.url, err)
	}
	host, port, err := net.SplitHostPort(u.Host)
	if err != nil {
		t.Fatalf("URL() = %q: %v", f.url, err)
	}
	if u.Scheme != "http" || host != "127.0.0.1" || port == "" || port == "0" {
		t.Errorf("URL() = %q, want http://127.0.0.1:<port>", f.url)
	}
}

func TestStreamingCallIsPassedThroughAndCounted(t *testing.T) {
	f := newFixture(t, "")
	token := f.open(t, nil)

	status, header, body := f.call(t, "/v1/messages?beta=true", signInHeader(token), messages("claude-sonnet-5-5", true))

	if status != http.StatusOK || body != streamed {
		t.Fatalf("response = %d %q, want 200 and the provider's stream unchanged", status, body)
	}
	if got := header.Get("Content-Type"); got != "text/event-stream" {
		t.Errorf("Content-Type = %q, want text/event-stream", got)
	}
	got := f.totals(t, token)
	want := gateway.Totals{Tokens: streamedTokens, CostMicrodollars: streamedCost}
	if got != want {
		t.Errorf("Totals() = %+v, want %+v", got, want)
	}
	reqs := f.provider.received()
	if len(reqs) != 1 || reqs[0].path != "/v1/messages?beta=true" || string(reqs[0].body) != messages("claude-sonnet-5-5", true) {
		t.Errorf("the provider received %+v, want the call unchanged", reqs)
	}
}

func TestNonStreamingCallIsPassedThroughAndCounted(t *testing.T) {
	f := newFixture(t, "")
	token := f.open(t, nil)

	status, _, body := f.call(t, "/v1/messages", signInHeader(token), messages("claude-sonnet-5-5", false))

	if status != http.StatusOK || body != nonStreamed {
		t.Fatalf("response = %d %q, want 200 and the provider's response unchanged", status, body)
	}
	got := f.totals(t, token)
	want := gateway.Totals{Tokens: nonStreamedTokens, CostMicrodollars: nonStreamedCost}
	if got != want {
		t.Errorf("Totals() = %+v, want %+v", got, want)
	}
}

func TestCallsAddUp(t *testing.T) {
	f := newFixture(t, "")
	token := f.open(t, nil)

	f.call(t, "/v1/messages", signInHeader(token), messages("claude-sonnet-5-5", false))
	f.call(t, "/v1/messages", signInHeader(token), messages("claude-sonnet-5-5", true))

	got := f.totals(t, token)
	// The cost is rounded up once, from the exact sum of both calls.
	want := gateway.Totals{Tokens: nonStreamedTokens + streamedTokens, CostMicrodollars: 22903}
	if got != want {
		t.Errorf("Totals() = %+v, want %+v", got, want)
	}
}

func TestGzipRequestBodyIsReadAndPassedThrough(t *testing.T) {
	f := newFixture(t, "")
	token := f.open(t, nil)
	var zipped bytes.Buffer
	zw := gzip.NewWriter(&zipped)
	if _, err := io.WriteString(zw, messages("claude-sonnet-5-5", false)); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	sent := zipped.Bytes()
	header := signInHeader(token)
	header.Set("Content-Encoding", "gzip")

	status, _, body := f.callBody(t, "/v1/messages", header, bytes.NewReader(sent))

	if status != http.StatusOK {
		t.Fatalf("response = %d %q, want 200", status, body)
	}
	reqs := f.provider.received()
	if len(reqs) != 1 || !bytes.Equal(reqs[0].body, sent) || reqs[0].header.Get("Content-Encoding") != "gzip" {
		t.Errorf("the provider received %+v, want the compressed body unchanged", reqs)
	}
	if got := f.totals(t, token); got.Tokens != nonStreamedTokens {
		t.Errorf("Totals().Tokens = %d, want %d", got.Tokens, nonStreamedTokens)
	}
}

func TestSignInIsForwardedAndTheInvocationHeaderIsNot(t *testing.T) {
	f := newFixture(t, "")
	if !f.gw.KeepsSignIn(testAdapter) {
		t.Error("KeepsSignIn() = false with no API key, want true")
	}
	token := f.open(t, nil)

	f.call(t, "/v1/messages", signInHeader(token), messages("claude-sonnet-5-5", false))

	reqs := f.provider.received()
	if len(reqs) != 1 {
		t.Fatalf("the provider received %d requests, want 1", len(reqs))
	}
	h := reqs[0].header
	if got := h.Get("Authorization"); got != "Bearer "+signIn {
		t.Errorf("Authorization = %q, want the harness's sign-in", got)
	}
	if got := h.Get("Anthropic-Beta"); got != "oauth-2025-04-20" {
		t.Errorf("Anthropic-Beta = %q, want it unchanged", got)
	}
	if got := h.Get(gateway.InvocationHeader); got != "" {
		t.Errorf("%s = %q reached the provider, want it removed", gateway.InvocationHeader, got)
	}
}

func TestSignInRefusesATokenSentOnlyAsTheCredential(t *testing.T) {
	f := newFixture(t, "")
	token := f.open(t, nil)

	status, _, _ := f.call(t, "/v1/messages", keyHeader(token), messages("claude-sonnet-5-5", false))

	if status != http.StatusUnauthorized {
		t.Errorf("status = %d, want %d: with no API key the token must come in %s", status, http.StatusUnauthorized, gateway.InvocationHeader)
	}
	if n := len(f.provider.received()); n != 0 {
		t.Errorf("the provider received %d requests, want 0", n)
	}
}

func TestAPIKeyIsSentInPlaceOfTheHarnessCredential(t *testing.T) {
	for name, header := range map[string]func(string) http.Header{
		"token as API key": keyHeader,
		"token as bearer": func(token string) http.Header {
			h := http.Header{}
			h.Set("Authorization", "Bearer "+token)
			return h
		},
		"token in header beside a sign-in": signInHeader,
	} {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t, providerKey)
			if f.gw.KeepsSignIn(testAdapter) {
				t.Error("KeepsSignIn() = true with an API key, want false")
			}
			token := f.open(t, nil)

			status, _, body := f.call(t, "/v1/messages", header(token), messages("claude-sonnet-5-5", false))

			if status != http.StatusOK {
				t.Fatalf("response = %d %q, want 200", status, body)
			}
			assertOnlyTheKeyReachedTheProvider(t, f.provider)
			if got := f.totals(t, token); got.Tokens != nonStreamedTokens {
				t.Errorf("Totals().Tokens = %d, want %d", got.Tokens, nonStreamedTokens)
			}
		})
	}
}

func assertOnlyTheKeyReachedTheProvider(t *testing.T, p *provider) {
	t.Helper()
	reqs := p.received()
	if len(reqs) != 1 {
		t.Fatalf("the provider received %d requests, want 1", len(reqs))
	}
	h := reqs[0].header
	if got := h.Get("X-Api-Key"); got != providerKey {
		t.Errorf("X-Api-Key = %q, want the configured API key", got)
	}
	if got := h.Get("Authorization"); got != "" {
		t.Errorf("Authorization = %q reached the provider, want none", got)
	}
	if got := h.Get(gateway.InvocationHeader); got != "" {
		t.Errorf("%s = %q reached the provider, want none", gateway.InvocationHeader, got)
	}
}

func TestACallWithNoValidTokenIsRefused(t *testing.T) {
	f := newFixture(t, providerKey)
	closed := f.open(t, nil)
	if err := f.gw.Close(closed); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	for name, header := range map[string]http.Header{
		"no token":      {},
		"unknown token": keyHeader("not-a-token"),
		"closed token":  keyHeader(closed),
	} {
		t.Run(name, func(t *testing.T) {
			status, respHeader, body := f.call(t, "/v1/messages", header, messages("claude-sonnet-5-5", false))
			if status != http.StatusUnauthorized || !strings.Contains(body, `"authentication_error"`) {
				t.Errorf("response = %d %q, want 401 with an authentication_error", status, body)
			}
			if got := respHeader.Get("X-Should-Retry"); got != "false" {
				t.Errorf("X-Should-Retry = %q, want false", got)
			}
		})
	}
	if n := len(f.provider.received()); n != 0 {
		t.Errorf("the provider received %d requests, want 0", n)
	}
	if _, err := f.gw.Totals(closed); !errors.Is(err, gateway.ErrUnknownToken) {
		t.Errorf("Totals() of a closed token error = %v, want ErrUnknownToken", err)
	}
	if err := f.gw.Close(closed); !errors.Is(err, gateway.ErrUnknownToken) {
		t.Errorf("Close() of a closed token error = %v, want ErrUnknownToken", err)
	}
}

func TestOpenGivesEachInvocationItsOwnToken(t *testing.T) {
	f := newFixture(t, "")
	a, err := f.gw.Open("inv-a", testAdapter, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	b, err := f.gw.Open("inv-b", testAdapter, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if a == b || len(a) < 32 {
		t.Errorf("Open() = %q and %q, want two different random tokens", a, b)
	}
	if _, err := f.gw.Open("inv-a", testAdapter, nil, nil); err == nil {
		t.Error("Open() of an invocation that is open succeeded, want an error")
	}

	f.call(t, "/v1/messages", signInHeader(a), messages("claude-sonnet-5-5", false))

	if got := f.totals(t, b); got != (gateway.Totals{}) {
		t.Errorf("Totals() of the other invocation = %+v, want zero", got)
	}
}

func TestAModelThePriceTableDoesNotListIsRefused(t *testing.T) {
	f := newFixture(t, "")
	token := f.open(t, nil)

	status, header, body := f.call(t, "/v1/messages", signInHeader(token), messages("frontier", false))

	if status != http.StatusBadRequest || !strings.Contains(body, `"invalid_request_error"`) || !strings.Contains(body, "frontier") {
		t.Errorf("response = %d %q, want 400 with an invalid_request_error that names the model", status, body)
	}
	if got := header.Get("X-Should-Retry"); got != "false" {
		t.Errorf("X-Should-Retry = %q, want false", got)
	}
	if n := len(f.provider.received()); n != 0 {
		t.Errorf("the provider received %d requests, want 0", n)
	}
}

func TestBatchesAreRefused(t *testing.T) {
	f := newFixture(t, "")
	token := f.open(t, nil)

	status, _, body := f.call(t, "/v1/messages/batches", signInHeader(token), `{"requests":[]}`)

	if status != http.StatusNotFound {
		t.Errorf("response = %d %q, want 404: the gateway cannot count a batch", status, body)
	}
	if n := len(f.provider.received()); n != 0 {
		t.Errorf("the provider received %d requests, want 0", n)
	}
}

func TestOtherPathsArePassedThroughUncounted(t *testing.T) {
	f := newFixture(t, providerKey)
	f.provider.respond = func(w http.ResponseWriter, _ *http.Request, _ []byte) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"input_tokens":12}`)
	}
	token := f.open(t, nil)

	status, _, body := f.call(t, "/v1/messages/count_tokens", keyHeader(token), messages("claude-sonnet-5-5", false))

	if status != http.StatusOK || body != `{"input_tokens":12}` {
		t.Errorf("response = %d %q, want the provider's answer", status, body)
	}
	reqs := f.provider.received()
	if len(reqs) != 1 || reqs[0].header.Get("X-Api-Key") != providerKey {
		t.Errorf("the provider received %+v, want the call with the API key", reqs)
	}
	if got := f.totals(t, token); got != (gateway.Totals{}) {
		t.Errorf("Totals() = %+v, want zero", got)
	}
}

func TestAnErrorResponseIsPassedBackAndNotCounted(t *testing.T) {
	f := newFixture(t, "")
	const invalid = `{"type":"error","error":{"type":"invalid_request_error","message":"max_tokens: too large"}}`
	f.provider.respond = func(w http.ResponseWriter, _ *http.Request, _ []byte) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = io.WriteString(w, invalid)
	}
	token := f.open(t, nil)

	status, _, body := f.call(t, "/v1/messages", signInHeader(token), messages("claude-sonnet-5-5", false))

	if n := len(f.provider.received()); status != http.StatusBadRequest || body != invalid || n != 1 {
		t.Errorf("response = %d %q after %d requests, want the provider's error unchanged after one", status, body, n)
	}
	if got := f.totals(t, token); got != (gateway.Totals{}) {
		t.Errorf("Totals() = %+v, want zero", got)
	}
}

func TestTheNextCallIsRefusedAtTheTokenCap(t *testing.T) {
	f := newFixture(t, "")
	token := f.open(t, &harnessv1.Usage{Tokens: nonStreamedTokens + 1})

	first, _, _ := f.call(t, "/v1/messages", signInHeader(token), messages("claude-sonnet-5-5", false))
	if got := f.totals(t, token); first != http.StatusOK || got.CapReached {
		t.Fatalf("the first call = %d with totals %+v, want 200 and the cap not reached", first, got)
	}
	// The second call starts below the cap and finishes over it.
	second, _, _ := f.call(t, "/v1/messages", signInHeader(token), messages("claude-sonnet-5-5", false))
	third, header, body := f.call(t, "/v1/messages", signInHeader(token), messages("claude-sonnet-5-5", false))

	if second != http.StatusOK {
		t.Errorf("the second call = %d, want 200: it started below the cap", second)
	}
	assertCapRefusal(t, third, header, body)
	if n := len(f.provider.received()); n != 2 {
		t.Errorf("the provider received %d requests, want 2", n)
	}
	want := gateway.Totals{Tokens: 2 * nonStreamedTokens, CostMicrodollars: 1242, CapReached: true}
	if got := f.totals(t, token); got != want {
		t.Errorf("Totals() = %+v, want %+v", got, want)
	}
}

func TestTheNextCallIsRefusedAtTheCostCap(t *testing.T) {
	f := newFixture(t, providerKey)
	// The call costs 22,281.7 microdollars, so it reaches a cap of 22,281.
	token := f.open(t, &harnessv1.Usage{CostMicrodollars: streamedCost - 1})

	first, _, _ := f.call(t, "/v1/messages", keyHeader(token), messages("claude-sonnet-5-5", true))
	second, header, body := f.call(t, "/v1/messages", keyHeader(token), messages("claude-sonnet-5-5", true))

	if first != http.StatusOK {
		t.Errorf("the first call = %d, want 200", first)
	}
	assertCapRefusal(t, second, header, body)
	want := gateway.Totals{Tokens: streamedTokens, CostMicrodollars: streamedCost, CapReached: true}
	if got := f.totals(t, token); got != want {
		t.Errorf("Totals() = %+v, want %+v", got, want)
	}
}

func TestACallInProgressFinishesWhenTheCapIsReached(t *testing.T) {
	f := newFixture(t, "")
	release := make(chan struct{})
	started := make(chan struct{})
	f.provider.respond = func(w http.ResponseWriter, r *http.Request, body []byte) {
		if !bytes.Contains(body, []byte(`"stream":true`)) {
			answer(w, r, body)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		head, tail, _ := strings.Cut(streamed, "event: message_delta\n")
		_, _ = io.WriteString(w, head)
		if err := http.NewResponseController(w).Flush(); err != nil {
			panic(err)
		}
		close(started)
		<-release
		_, _ = io.WriteString(w, "event: message_delta\n"+tail)
	}
	token := f.open(t, &harnessv1.Usage{Tokens: nonStreamedTokens})

	type result struct {
		status int
		body   string
	}
	done := make(chan result, 1)
	go func() {
		status, _, body := f.call(t, "/v1/messages", signInHeader(token), messages("claude-sonnet-5-5", true))
		done <- result{status, body}
	}()
	<-started
	reached, _, _ := f.call(t, "/v1/messages", signInHeader(token), messages("claude-sonnet-5-5", false))
	refused, header, body := f.call(t, "/v1/messages", signInHeader(token), messages("claude-sonnet-5-5", false))
	close(release)
	inProgress := <-done

	if reached != http.StatusOK {
		t.Errorf("the call that reached the cap = %d, want 200", reached)
	}
	assertCapRefusal(t, refused, header, body)
	if inProgress.status != http.StatusOK || inProgress.body != streamed {
		t.Errorf("the call in progress = %d %q, want 200 and the whole stream", inProgress.status, inProgress.body)
	}
	want := gateway.Totals{Tokens: nonStreamedTokens + streamedTokens, CostMicrodollars: 22903, CapReached: true}
	if got := f.totals(t, token); got != want {
		t.Errorf("Totals() = %+v, want %+v", got, want)
	}
}

func assertCapRefusal(t *testing.T, status int, header http.Header, body string) {
	t.Helper()
	if status != http.StatusForbidden || !strings.Contains(body, `"permission_error"`) || !strings.Contains(body, "cap") {
		t.Errorf("the call after the cap = %d %q, want 403 with a permission_error about the cap", status, body)
	}
	if got := header.Get("X-Should-Retry"); got != "false" {
		t.Errorf("X-Should-Retry = %q, want false", got)
	}
}

func TestTheProviderKeyIsNeverLogged(t *testing.T) {
	f := newFixture(t, providerKey)
	token := f.open(t, &harnessv1.Usage{Tokens: 1})
	f.provider.respond = func(w http.ResponseWriter, r *http.Request, body []byte) {
		if bytes.Contains(body, []byte("hang up")) {
			// The provider drops the connection, so the gateway logs a
			// failed call.
			conn, _, err := http.NewResponseController(w).Hijack()
			if err == nil {
				_ = conn.Close()
			}
			return
		}
		answer(w, r, body)
	}

	f.call(t, "/v1/messages", keyHeader(token), `{"model":"claude-sonnet-5-5","messages":[{"role":"user","content":"hang up"}]}`)
	f.call(t, "/v1/messages", keyHeader(token), messages("claude-sonnet-5-5", false))
	f.call(t, "/v1/messages", keyHeader(token), messages("claude-sonnet-5-5", false))
	f.call(t, "/v1/messages", keyHeader(token), messages("frontier", false))
	f.call(t, "/v1/messages", keyHeader("not-a-token"), messages("claude-sonnet-5-5", false))

	logged := f.log.String()
	if logged == "" {
		t.Fatal("the gateway logged nothing, want its refusals and the failed call")
	}
	if strings.Contains(logged, providerKey) {
		t.Errorf("the log holds the provider key:\n%s", logged)
	}
	if strings.Contains(logged, token) {
		t.Errorf("the log holds the invocation token:\n%s", logged)
	}
}

// TestEachAdapterHasItsOwnCredential: the gateway sends the API key of an
// invocation's adapter, and forwards the sign-in of an invocation whose
// adapter has no key, whose token must come in the invocation header
// (decisions 0225, 0240 and 0245).
func TestEachAdapterHasItsOwnCredential(t *testing.T) {
	f := newFixtureWith(t, gateway.Config{APIKeys: map[string]string{"keyed": providerKey}})
	if f.gw.KeepsSignIn("keyed") || !f.gw.KeepsSignIn("signed-in") {
		t.Errorf("KeepsSignIn(keyed), KeepsSignIn(signed-in) = %v, %v; want false, true", f.gw.KeepsSignIn("keyed"), f.gw.KeepsSignIn("signed-in"))
	}
	keyed, err := f.gw.Open("inv-keyed", "keyed", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	signed, err := f.gw.Open("inv-signed", "signed-in", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if status, _, body := f.call(t, "/v1/messages", keyHeader(keyed), messages("claude-sonnet-5-5", false)); status != http.StatusOK {
		t.Fatalf("the keyed call = %d %q, want 200", status, body)
	}
	assertOnlyTheKeyReachedTheProvider(t, f.provider)
	if status, _, _ := f.call(t, "/v1/messages", keyHeader(signed), messages("claude-sonnet-5-5", false)); status != http.StatusUnauthorized {
		t.Errorf("the signed-in token sent as the API key = %d, want %d", status, http.StatusUnauthorized)
	}
	if status, _, body := f.call(t, "/v1/messages", signInHeader(signed), messages("claude-sonnet-5-5", false)); status != http.StatusOK {
		t.Fatalf("the signed-in call = %d %q, want 200", status, body)
	}
	reqs := f.provider.received()
	if len(reqs) != 2 {
		t.Fatalf("the provider received %d requests, want 2", len(reqs))
	}
	if h := reqs[1].header; h.Get("Authorization") != "Bearer "+signIn || h.Get("X-Api-Key") != "" {
		t.Errorf("the signed-in call reached the provider with Authorization %q and X-Api-Key %q, want the sign-in and no key", h.Get("Authorization"), h.Get("X-Api-Key"))
	}
}
