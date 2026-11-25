// SPDX-License-Identifier: Apache-2.0
// SPDX-FileCopyrightText: The Pumi Authors

package main

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"getpumi.dev/pumi/internal/gateway"
)

// The parts of the model configurations of the tests of the Responses
// API: claude-code and codex, both in the API key mode.
var (
	claudeKeyed = adapterText("claude-code", `executable: "bin/adapter-claude-code" sha256: "`+validSHA+`" credential_mode: CREDENTIAL_MODE_API_KEY api_key_env: "PUMI_TEST_ANTHROPIC_KEY"`)
	codexKeyed  = adapterText("codex", `executable: "bin/adapter-codex" sha256: "`+validSHA+`" credential_mode: CREDENTIAL_MODE_API_KEY api_key_env: "PUMI_TEST_OPENAI_KEY"`)
	twoAPIs     = `roles { key: "implementer" value { adapter: "claude-code" model: "claude-opus-5-5" fallback { adapter: "codex" model: "gpt-6.1-sol" } } } ` +
		`roles { key: "reviewer" value { adapter: "codex" model: "gpt-6.2-sol" fallback { adapter: "codex" model: "gpt-6.1-sol" } } }`
)

// TestEachAdapterUsesTheAPIOfItsModels: pumi run gives the gateway the API
// of each adapter from the models that its roles and fallbacks name, as the
// price table lists them (decisions 0243 and 0261).
func TestEachAdapterUsesTheAPIOfItsModels(t *testing.T) {
	cfg, err := readConfig(writeTestConfig(t, models(claudeKeyed, codexKeyed, twoAPIs, `check_adapter: "claude-code"`)))
	if err != nil {
		t.Fatal(err)
	}
	got, err := adapterAPIs(cfg)
	if err != nil {
		t.Fatalf("adapterAPIs = %v", err)
	}
	if len(got) != 2 || got["claude-code"] != gateway.MessagesAPI || got["codex"] != gateway.ResponsesAPI {
		t.Errorf("adapterAPIs = %v, want claude-code on the Messages API and codex on the Responses API", got)
	}
}

// TestAnAdapterWhoseModelsUseTwoAPIsIsRefused: one harness speaks one API,
// so pumi run refuses an adapter whose roles name models of two.
func TestAnAdapterWhoseModelsUseTwoAPIsIsRefused(t *testing.T) {
	mixed := `roles { key: "reviewer" value { adapter: "codex" model: "gpt-6.2-sol" } } ` +
		`roles { key: "implementer" value { adapter: "claude-code" model: "claude-opus-5-5" fallback { adapter: "codex" model: "claude-sonnet-5-5" } } }`
	cfg, err := readConfig(writeTestConfig(t, models(claudeKeyed, codexKeyed, mixed, `check_adapter: "claude-code"`)))
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("PUMI_TEST_ANTHROPIC_KEY", "sk-ant-key")
	t.Setenv("PUMI_TEST_OPENAI_KEY", "sk-proj-key")
	gw, err := serveGateway(t.Context(), cfg, io.Discard)
	if err == nil {
		gw.shutdown(t.Context())
		t.Fatal("serveGateway succeeded, want an error")
	}
	for _, want := range []string{`"codex"`, "gpt-6.2-sol", "claude-sonnet-5-5"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("serveGateway error = %v, want it to name %s", err, want)
		}
	}
}

// recorder is a fake provider that records the paths and credentials of
// the calls it receives and answers them with body.
type recorder struct {
	mu    sync.Mutex
	calls []string
	body  string
}

func (p *recorder) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	p.mu.Lock()
	p.calls = append(p.calls, r.URL.Path+" "+r.Header.Get("Authorization")+r.Header.Get("X-Api-Key"))
	p.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	_, _ = io.WriteString(w, p.body)
}

func (p *recorder) received() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string(nil), p.calls...)
}

// postAs posts body to url with token as the bearer token, as Codex and
// Claude Code do when the gateway holds the key, and returns the status.
func postAs(t *testing.T, url, token, body string) int {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, url, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()
	return resp.StatusCode
}

// TestTheGatewaySendsEachAdapterToItsProvider: the gateway of pumi run
// sends the calls of a codex invocation to OpenAI with the OpenAI key, and
// those of a claude-code invocation to Anthropic with the Anthropic key.
func TestTheGatewaySendsEachAdapterToItsProvider(t *testing.T) {
	anthropic := &recorder{body: `{"model":"claude-opus-5-5","usage":{"input_tokens":3,"output_tokens":4}}`}
	openai := &recorder{body: `{"object":"response","model":"gpt-6.2-sol","service_tier":"default","usage":{"input_tokens":5,"input_tokens_details":{"cached_tokens":0,"cache_write_tokens":0},"output_tokens":6}}`}
	old := providerURLs
	t.Cleanup(func() { providerURLs = old })
	providerURLs = map[gateway.API]string{}
	for api, p := range map[gateway.API]http.Handler{gateway.MessagesAPI: anthropic, gateway.ResponsesAPI: openai} {
		srv := httptest.NewServer(p)
		t.Cleanup(srv.Close)
		providerURLs[api] = srv.URL
	}
	t.Setenv("PUMI_TEST_ANTHROPIC_KEY", "sk-ant-key")
	t.Setenv("PUMI_TEST_OPENAI_KEY", "sk-proj-key")
	cfg, err := readConfig(writeTestConfig(t, models(claudeKeyed, codexKeyed, twoAPIs, `check_adapter: "claude-code"`)))
	if err != nil {
		t.Fatal(err)
	}
	gw, err := serveGateway(t.Context(), cfg, io.Discard)
	if err != nil {
		t.Fatalf("serveGateway = %v", err)
	}
	t.Cleanup(func() { gw.shutdown(context.Background()) })

	for _, c := range []struct{ adapter, path, body string }{
		{"codex", "/v1/responses", `{"model":"reviewer","input":"say woof"}`},
		{"claude-code", "/v1/messages", `{"model":"implementer","max_tokens":10,"messages":[]}`},
	} {
		token, err := gw.Open("inv-"+c.adapter, c.adapter, nil, nil)
		if err != nil {
			t.Fatal(err)
		}
		if status := postAs(t, gw.URL()+c.path, token, c.body); status != http.StatusOK {
			t.Errorf("the %s call = %d, want 200", c.adapter, status)
		}
	}
	if got := openai.received(); len(got) != 1 || got[0] != "/v1/responses Bearer sk-proj-key" {
		t.Errorf("OpenAI's fake received %q, want the Responses call with the OpenAI key", got)
	}
	if got := anthropic.received(); len(got) != 1 || got[0] != "/v1/messages sk-ant-key" {
		t.Errorf("Anthropic's fake received %q, want the Messages call with the Anthropic key", got)
	}
}
