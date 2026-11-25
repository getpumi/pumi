// SPDX-License-Identifier: Apache-2.0
// SPDX-FileCopyrightText: The Pumi Authors

package gateway

import (
	"strings"
	"testing"

	harnessv1 "getpumi.dev/pumi/api/gen/pumi/harness/v1"
)

func TestPicodollarsPricesEachKindOfToken(t *testing.T) {
	opus, _ := Price("claude-opus-5-5")
	haiku, _ := Price("claude-haiku-5-5")
	tests := []struct {
		name  string
		model Model
		usage usage
		want  int64
	}{
		{"input", opus, usage{input: 1_000_000}, 4_000_000_000_000},
		{"5-minute cache writes", opus, usage{writeShort: 1_000_000}, 5_000_000_000_000},
		{"1-hour cache writes", opus, usage{writeLong: 1_000_000}, 8_000_000_000_000},
		{"cache reads", opus, usage{read: 1_000_000}, 200_000_000_000},
		{"output", opus, usage{output: 1_000_000}, 20_000_000_000_000},
		{"fast mode", opus, usage{input: 1_000_000, output: 1_000_000, fast: true}, 48_000_000_000_000},
		{"US-only inference", opus, usage{input: 1_000_000, output: 1_000_000, usOnly: true}, 26_400_000_000_000},
		{"web searches", opus, usage{webSearches: 3}, 30_000_000_000},
		{"US-only inference does not change web searches", opus, usage{webSearches: 1, usOnly: true}, 10_000_000_000},
		{"Haiku 5.5 at 100,000 prompt tokens", haiku, usage{input: 50_000, read: 50_000, output: 1_000}, 50_000*100_000 + 50_000*10_000 + 1_000*500_000},
		{"Haiku 5.5 over 100,000 prompt tokens", haiku, usage{input: 50_000, read: 50_000, writeLong: 1, output: 1_000}, 50_000*500_000 + 50_000*50_000 + 1*1_000_000 + 1_000*2_500_000},
		{"fast mode on a model without it", haiku, usage{input: 1_000, fast: true}, 1_000 * 100_000},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.model.picodollars(tt.usage); got != tt.want {
				t.Errorf("picodollars(%+v) = %d, want %d", tt.usage, got, tt.want)
			}
		})
	}
}

func TestTokensCountsEveryKind(t *testing.T) {
	u := usage{input: 1, writeShort: 20, writeLong: 300, read: 4_000, output: 50_000, webSearches: 7}
	if got := u.tokens(); got != 54_321 {
		t.Errorf("tokens() = %d, want 54321", got)
	}
}

func TestEveryModelHasAPrice(t *testing.T) {
	for name, m := range prices {
		for _, r := range []Rates{m.Standard} {
			if r.Input <= 0 || r.CacheWriteShort <= r.Input || r.CacheWriteLong <= r.CacheWriteShort || r.CacheRead <= 0 || r.CacheRead >= r.Input || r.Output <= r.Input {
				t.Errorf("%s has rates %+v, want input < 5-minute write < 1-hour write, 0 < read < input < output", name, r)
			}
		}
		if m.API != MessagesAPI && m.API != ResponsesAPI {
			t.Errorf("%s names the API %q, want the Messages or the Responses API", name, m.API)
		}
		if (m.LongPrompt == nil) != (m.LongPromptTokens == 0) {
			t.Errorf("%s has long-prompt rates %v over %d tokens, want both or neither", name, m.LongPrompt, m.LongPromptTokens)
		}
	}
}

func TestUsageReaderReadsAStream(t *testing.T) {
	r := newUsageReader(MessagesAPI, true)
	// A stream arrives in pieces that split its lines.
	stream := "event: message_start\n" +
		`data: {"type":"message_start","message":{"model":"claude-opus-5-5","usage":{"input_tokens":5,"cache_creation_input_tokens":300,"cache_read_input_tokens":40,"cache_creation":{"ephemeral_5m_input_tokens":100,"ephemeral_1h_input_tokens":200},"output_tokens":1,"inference_geo":"us","speed":"fast"}}}` + "\r\n\r\n" +
		"event: ping\ndata: {\"type\": \"ping\"}\n\n" +
		"event: message_delta\n" +
		`data: {"type":"message_delta","usage":{"input_tokens":5,"cache_creation_input_tokens":300,"cache_read_input_tokens":40,"output_tokens":77,"server_tool_use":{"web_search_requests":2}}}` + "\n\n"
	for len(stream) > 0 {
		n := min(7, len(stream))
		if _, err := r.Write([]byte(stream[:n])); err != nil {
			t.Fatal(err)
		}
		stream = stream[n:]
	}
	got, ok := r.usage()
	want := usage{model: "claude-opus-5-5", input: 5, writeShort: 100, writeLong: 200, read: 40, output: 77, webSearches: 2, fast: true, usOnly: true}
	if !ok || got != want {
		t.Errorf("usage() = %+v, %t, want %+v, true", got, ok, want)
	}
}

func TestUsageReaderReadsAResponse(t *testing.T) {
	r := newUsageReader(MessagesAPI, false)
	body := `{"model":"claude-sonnet-5-5","content":[{"type":"text","text":"usage"}],"usage":{"input_tokens":4,"cache_creation_input_tokens":10,"cache_read_input_tokens":4828,"output_tokens":13}}`
	for _, part := range strings.SplitAfter(body, ",") {
		if _, err := r.Write([]byte(part)); err != nil {
			t.Fatal(err)
		}
	}
	got, ok := r.usage()
	// Cache writes the response does not split by duration count as
	// 5-minute writes, the cache's default duration.
	want := usage{model: "claude-sonnet-5-5", input: 4, writeShort: 10, read: 4828, output: 13}
	if !ok || got != want {
		t.Errorf("usage() = %+v, %t, want %+v, true", got, ok, want)
	}
}

func TestUsageReaderWithoutUsage(t *testing.T) {
	for name, tt := range map[string]struct {
		streaming bool
		body      string
	}{
		"cut stream":         {true, "event: message_start\ndata: {\"type\":\"mess"},
		"not JSON":           {false, "<html>bad gateway</html>"},
		"JSON with no usage": {false, `{"type":"message"}`},
	} {
		t.Run(name, func(t *testing.T) {
			r := newUsageReader(MessagesAPI, tt.streaming)
			if _, err := r.Write([]byte(tt.body)); err != nil {
				t.Fatal(err)
			}
			if got, ok := r.usage(); ok {
				t.Errorf("usage() = %+v, true, want false", got)
			}
		})
	}
}

// TestFigureCostPricesAFigureAsACall: a usage figure that an adapter
// passes on the sign-in path costs what the same call through the gateway
// costs, and a figure of a model the table does not list is not priced
// (decision 0241).
func TestFigureCostPricesAFigureAsACall(t *testing.T) {
	figure := &harnessv1.UsageFigure{
		Model: "claude-opus-5-5", InputTokens: 1_000, FiveMinuteCacheWriteTokens: 20, OneHourCacheWriteTokens: 300,
		CacheReadTokens: 4_000, OutputTokens: 50_000, WebSearches: 2, Speed: "fast", InferenceGeo: "us",
	}
	opus, _ := Price("claude-opus-5-5")
	want := opus.picodollars(usage{input: 1_000, writeShort: 20, writeLong: 300, read: 4_000, output: 50_000, webSearches: 2, fast: true, usOnly: true})
	if got, listed := FigureCost(figure); got != want || !listed {
		t.Errorf("FigureCost = %d, %v; want %d, true", got, listed, want)
	}
	figure.Model = "not-a-model"
	if got, listed := FigureCost(figure); got != 0 || listed {
		t.Errorf("FigureCost of an unlisted model = %d, %v; want 0, false", got, listed)
	}
}

func TestOpenAIModelsArePricedByTierAndPromptLength(t *testing.T) {
	sol, _ := Price("gpt-6.1-sol")
	tests := []struct {
		name  string
		usage usage
		want  int64
	}{
		{"input", usage{input: 100_000}, 200_000_000_000},
		{"cached input", usage{read: 100_000}, 10_000_000_000},
		{"30-minute cache writes", usage{writeShort: 100_000}, 250_000_000_000},
		{"12-hour cache writes", usage{writeLong: 100_000}, 500_000_000_000},
		{"output", usage{output: 1_000_000}, 10_000_000_000_000},
		{"fast mode", usage{input: 100_000, tier: tierFast}, 400_000_000_000},
		{"flex", usage{input: 100_000, tier: tierFlex}, 100_000_000_000},
		{"ultrafast", usage{input: 100_000, tier: tierUltrafast}, 1_200_000_000_000},
		{"a tier the model does not have is priced at its highest", usage{input: 100_000, tier: tierUnknown}, 1_200_000_000_000},
		{"web searches", usage{webSearches: 1}, 10_000_000_000},
		// Over 272,000 input tokens, input and cache rates double and
		// output costs 1.5 times, for the whole call.
		{"a long prompt", usage{input: 200_000, read: 72_000, writeShort: 1, output: 1_000}, 200_000*4_000_000 + 72_000*200_000 + 1*5_000_000 + 1_000*15_000_000},
		{"a long prompt in fast mode", usage{input: 272_001, output: 1, tier: tierFast}, 272_001*8_000_000 + 30_000_000},
		{"a prompt of 272,000 tokens", usage{input: 272_000, output: 1}, 272_000*2_000_000 + 10_000_000},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := sol.picodollars(tt.usage); got != tt.want {
				t.Errorf("picodollars(%+v) = %d, want %d", tt.usage, got, tt.want)
			}
		})
	}
}

// TestAnthropicModelsIgnoreTheOpenAITiers: a tier is OpenAI's; Anthropic's
// calls are priced by their speed.
func TestAnthropicModelsIgnoreTheOpenAITiers(t *testing.T) {
	opus, _ := Price("claude-opus-5-5")
	if got, want := opus.picodollars(usage{input: 1_000_000, tier: tierUltrafast}), int64(4_000_000_000_000); got != want {
		t.Errorf("picodollars = %d, want %d", got, want)
	}
}

func TestUsageReaderReadsAResponsesStream(t *testing.T) {
	r := newUsageReader(ResponsesAPI, true)
	stream := "event: response.created\n" +
		`data: {"type":"response.created","response":{"model":"gpt-6.2-sol","service_tier":"auto","usage":null}}` + "\r\n\r\n" +
		"event: response.output_text.delta\n" +
		`data: {"type":"response.output_text.delta","delta":"woof"}` + "\n\n" +
		"event: response.completed\n" +
		`data: {"type":"response.completed","response":{"model":"gpt-6.2-sol-2026-10-01","service_tier":"priority","output":[{"type":"web_search_call"},{"type":"message"}],"usage":{"input_tokens":4100,"input_tokens_details":{"cached_tokens":3000,"cache_write_tokens":100},"output_tokens":77,"output_tokens_details":{"reasoning_tokens":50}}}}` + "\n\n"
	for len(stream) > 0 {
		n := min(7, len(stream))
		if _, err := r.Write([]byte(stream[:n])); err != nil {
			t.Fatal(err)
		}
		stream = stream[n:]
	}
	got, ok := r.usage()
	want := usage{model: "gpt-6.2-sol-2026-10-01", input: 1000, writeShort: 100, read: 3000, output: 77, reasoning: 50, webSearches: 1, tier: tierFast}
	if !ok || got != want {
		t.Errorf("usage() = %+v, %t, want %+v, true", got, ok, want)
	}
}

func TestUsageReaderReadsAResponsesResponse(t *testing.T) {
	r := newUsageReader(ResponsesAPI, false)
	body := `{"object":"response","model":"gpt-6.2-sol","service_tier":"flex","output":[],"usage":{"input_tokens":40,"input_tokens_details":{"cached_tokens":0},"output_tokens":13,"output_tokens_details":{"reasoning_tokens":0}}}`
	if _, err := r.Write([]byte(body)); err != nil {
		t.Fatal(err)
	}
	got, ok := r.usage()
	want := usage{model: "gpt-6.2-sol", input: 40, output: 13, tier: tierFlex}
	if !ok || got != want {
		t.Errorf("usage() = %+v, %t, want %+v, true", got, ok, want)
	}
}

func TestLongCacheWritesReadsTheRequest(t *testing.T) {
	for body, want := range map[string]bool{
		`{"model":"m"}`: false,
		`{"model":"m","prompt_cache_options":{"ttl":"30m"}}`:                                      false,
		`{"model":"m","prompt_cache_options":{"mode":"implicit","ttl":"12h"}}`:                    true,
		`{"input":[{"content":[{"type":"input_text","prompt_cache_breakpoint":{"ttl":"12h"}}]}]}`: true,
		`{"instructions":"{\"prompt_cache_options\":{\"ttl\":\"12h\"}}"}`:                         false,
		`{"metadata":{"ttl":"12h"}}`:                                                              false,
		`not JSON "12h"`:                                                                          false,
	} {
		if got := longCacheWrites([]byte(body)); got != want {
			t.Errorf("longCacheWrites(%s) = %t, want %t", body, got, want)
		}
	}
}
