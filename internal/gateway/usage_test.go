// SPDX-License-Identifier: Apache-2.0
// SPDX-FileCopyrightText: The Pumi Authors

package gateway

import (
	"strings"
	"testing"
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
		{"5-minute cache writes", opus, usage{write5m: 1_000_000}, 5_000_000_000_000},
		{"1-hour cache writes", opus, usage{write1h: 1_000_000}, 8_000_000_000_000},
		{"cache reads", opus, usage{read: 1_000_000}, 200_000_000_000},
		{"output", opus, usage{output: 1_000_000}, 20_000_000_000_000},
		{"fast mode", opus, usage{input: 1_000_000, output: 1_000_000, fast: true}, 48_000_000_000_000},
		{"US-only inference", opus, usage{input: 1_000_000, output: 1_000_000, usOnly: true}, 26_400_000_000_000},
		{"web searches", opus, usage{webSearches: 3}, 30_000_000_000},
		{"US-only inference does not change web searches", opus, usage{webSearches: 1, usOnly: true}, 10_000_000_000},
		{"Haiku 5.5 at 100,000 prompt tokens", haiku, usage{input: 50_000, read: 50_000, output: 1_000}, 50_000*100_000 + 50_000*10_000 + 1_000*500_000},
		{"Haiku 5.5 over 100,000 prompt tokens", haiku, usage{input: 50_000, read: 50_000, write1h: 1, output: 1_000}, 50_000*500_000 + 50_000*50_000 + 1*1_000_000 + 1_000*2_500_000},
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
	u := usage{input: 1, write5m: 20, write1h: 300, read: 4_000, output: 50_000, webSearches: 7}
	if got := u.tokens(); got != 54_321 {
		t.Errorf("tokens() = %d, want 54321", got)
	}
}

func TestEveryModelHasAPrice(t *testing.T) {
	for name, m := range prices {
		for _, r := range []Rates{m.Standard} {
			if r.Input <= 0 || r.CacheWrite5m <= r.Input || r.CacheWrite1h <= r.CacheWrite5m || r.CacheRead <= 0 || r.CacheRead >= r.Input || r.Output <= r.Input {
				t.Errorf("%s has rates %+v, want input < 5-minute write < 1-hour write, 0 < read < input < output", name, r)
			}
		}
		if (m.LongPrompt == nil) != (m.LongPromptTokens == 0) {
			t.Errorf("%s has long-prompt rates %v over %d tokens, want both or neither", name, m.LongPrompt, m.LongPromptTokens)
		}
	}
}

func TestUsageReaderReadsAStream(t *testing.T) {
	r := newUsageReader(true)
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
	want := usage{model: "claude-opus-5-5", input: 5, write5m: 100, write1h: 200, read: 40, output: 77, webSearches: 2, fast: true, usOnly: true}
	if !ok || got != want {
		t.Errorf("usage() = %+v, %t, want %+v, true", got, ok, want)
	}
}

func TestUsageReaderReadsAResponse(t *testing.T) {
	r := newUsageReader(false)
	body := `{"model":"claude-sonnet-5-5","content":[{"type":"text","text":"usage"}],"usage":{"input_tokens":4,"cache_creation_input_tokens":10,"cache_read_input_tokens":4828,"output_tokens":13}}`
	for _, part := range strings.SplitAfter(body, ",") {
		if _, err := r.Write([]byte(part)); err != nil {
			t.Fatal(err)
		}
	}
	got, ok := r.usage()
	// Cache writes the response does not split by duration count as
	// 5-minute writes, the cache's default duration.
	want := usage{model: "claude-sonnet-5-5", input: 4, write5m: 10, read: 4828, output: 13}
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
			r := newUsageReader(tt.streaming)
			if _, err := r.Write([]byte(tt.body)); err != nil {
				t.Fatal(err)
			}
			if got, ok := r.usage(); ok {
				t.Errorf("usage() = %+v, true, want false", got)
			}
		})
	}
}
