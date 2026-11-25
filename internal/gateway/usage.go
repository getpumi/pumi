// SPDX-License-Identifier: Apache-2.0
// SPDX-FileCopyrightText: The Pumi Authors

package gateway

import (
	"bytes"
	"encoding/json"
	"slices"
)

// usage is what one call used, as the provider reported it in its
// response.
type usage struct {
	// model is the model that answered, when the response names it.
	model string
	// The tokens of each kind (decision 0227): input is the input that was
	// not read from or written to the cache, writeShort and writeLong the
	// tokens written for the cache's shorter and longer lifetime, and
	// output includes the reasoning or thinking.
	input, writeShort, writeLong, read, output int64
	// reasoning is the part of output that OpenAI reports as reasoning.
	reasoning int64
	// webSearches is the number of web searches the call made.
	webSearches int64
	// fast is a call in Anthropic's fast mode, usOnly a call with US-only
	// inference.
	fast, usOnly bool
	// tier is the OpenAI service tier of the call.
	tier tier
}

// tier is the service tier at which OpenAI answered a call, as its
// response reports it.
type tier int

const (
	// tierStandard is the tier "default", or no tier.
	tierStandard tier = iota
	// tierFast is the tier "priority", which OpenAI renamed Fast mode on
	// 2026-07-30, or "fast".
	tierFast
	tierFlex
	tierUltrafast
	// tierUnknown is a tier the gateway does not know, which it prices at
	// the model's highest rates, so that the cost cap never counts less.
	tierUnknown
)

// tierOf returns the tier of the service_tier that a response reports.
func tierOf(name string) tier {
	switch name {
	case "", "default", "auto":
		return tierStandard
	case "priority", "fast":
		return tierFast
	case "flex":
		return tierFlex
	case "ultrafast":
		return tierUltrafast
	default:
		return tierUnknown
	}
}

// tokens is the tokens of every kind, which count toward the token cap
// (decision 0227).
func (u usage) tokens() int64 {
	return u.input + u.writeShort + u.writeLong + u.read + u.output
}

// picodollars is the reference cost of the call at the model's list prices
// (decisions 0229 and 0239). A number of tokens times a price in
// microdollars per million tokens is a cost in picodollars, so the cost is
// exact.
func (m Model) picodollars(u usage) int64 {
	r := m.rates(u)
	prompt := u.input + u.writeShort + u.writeLong + u.read
	if m.LongPrompt != nil && prompt > m.LongPromptTokens {
		r = *m.LongPrompt
	}
	inputCost := u.input*r.Input + u.writeShort*r.CacheWriteShort + u.writeLong*r.CacheWriteLong + u.read*r.CacheRead
	cost := inputCost + u.output*r.Output
	if m.LongContextTokens > 0 && prompt > m.LongContextTokens {
		// Rounded up, so that the cost cap never counts less.
		cost = 2*inputCost + (3*u.output*r.Output+1)/2
	}
	if u.usOnly {
		// Rounded up, so that the cost cap never counts less.
		cost = (cost*usOnlyNumerator + usOnlyDenominator - 1) / usOnlyDenominator
	}
	return cost + u.webSearches*webSearchMicrodollars*1_000_000
}

// rates returns the rates of the call's speed or tier.
func (m Model) rates(u usage) Rates {
	if m.API == MessagesAPI {
		if u.fast && m.Fast != nil {
			return *m.Fast
		}
		return m.Standard
	}
	byTier := map[tier]*Rates{tierFast: m.Fast, tierFlex: m.Flex, tierUltrafast: m.Ultrafast}
	if r := byTier[u.tier]; r != nil {
		return *r
	}
	if u.tier == tierStandard {
		return m.Standard
	}
	// A tier the model does not have, or one the gateway does not know.
	highest := m.Standard
	for _, r := range []*Rates{m.Fast, m.Ultrafast} {
		if r != nil && r.Input > highest.Input {
			highest = *r
		}
	}
	return highest
}

// reported is the usage object of Anthropic's Messages API. A field that a
// message does not have stays nil, so that a later message of a stream
// changes only the fields it has.
type reported struct {
	InputTokens              *int64 `json:"input_tokens"`
	CacheCreationInputTokens *int64 `json:"cache_creation_input_tokens"`
	CacheReadInputTokens     *int64 `json:"cache_read_input_tokens"`
	OutputTokens             *int64 `json:"output_tokens"`
	CacheCreation            *struct {
		Ephemeral5m *int64 `json:"ephemeral_5m_input_tokens"`
		Ephemeral1h *int64 `json:"ephemeral_1h_input_tokens"`
	} `json:"cache_creation"`
	ServerToolUse *struct {
		WebSearchRequests *int64 `json:"web_search_requests"`
	} `json:"server_tool_use"`
	InferenceGeo *string `json:"inference_geo"`
	Speed        *string `json:"speed"`
}

// merge sets the fields of r that later has.
func (r *reported) merge(later *reported) {
	set := func(to **int64, from *int64) {
		if from != nil {
			*to = from
		}
	}
	set(&r.InputTokens, later.InputTokens)
	set(&r.CacheCreationInputTokens, later.CacheCreationInputTokens)
	set(&r.CacheReadInputTokens, later.CacheReadInputTokens)
	set(&r.OutputTokens, later.OutputTokens)
	if later.CacheCreation != nil {
		r.CacheCreation = later.CacheCreation
	}
	if later.ServerToolUse != nil {
		r.ServerToolUse = later.ServerToolUse
	}
	if later.InferenceGeo != nil {
		r.InferenceGeo = later.InferenceGeo
	}
	if later.Speed != nil {
		r.Speed = later.Speed
	}
}

func value(p *int64) int64 {
	if p == nil {
		return 0
	}
	return *p
}

// usage turns the reported usage into a call's usage. Cache writes that the
// response does not split by duration count as 5-minute writes, the cache's
// default duration.
func (r *reported) usage(model string) usage {
	u := usage{
		model:  model,
		input:  value(r.InputTokens),
		read:   value(r.CacheReadInputTokens),
		output: value(r.OutputTokens),
		fast:   r.Speed != nil && *r.Speed == "fast",
		usOnly: r.InferenceGeo != nil && *r.InferenceGeo == "us",
	}
	if r.CacheCreation != nil {
		u.writeShort = value(r.CacheCreation.Ephemeral5m)
		u.writeLong = value(r.CacheCreation.Ephemeral1h)
	}
	if rest := value(r.CacheCreationInputTokens) - u.writeShort - u.writeLong; rest > 0 {
		u.writeShort += rest
	}
	if r.ServerToolUse != nil {
		u.webSearches = value(r.ServerToolUse.WebSearchRequests)
	}
	return u
}

// maxResponse is the most of a response that is not streamed that the
// usage reader keeps to find its usage. A longer response is passed through
// but not counted, and the gateway logs that.
const maxResponse = 64 << 20

// usageReader is written a response body as it passes to the harness, and
// finds the usage the response reports. For the Messages API it is in the
// message_start and message_delta events of a stream, whose later values
// take the place of earlier ones, or in the usage of a whole response. For
// the Responses API it is in the response of a stream's final event,
// response.completed, response.incomplete or response.failed, or in a
// whole response.
type usageReader struct {
	api       API
	streaming bool
	// buf holds the stream's unfinished line, or the whole response.
	buf      bytes.Buffer
	found    bool
	tooLong  bool
	model    string
	reported reported
	// final is the usage of a Responses stream's final event.
	final usage
}

func newUsageReader(api API, streaming bool) *usageReader {
	return &usageReader{api: api, streaming: streaming}
}

// Write never fails, so that the response always reaches the harness.
func (r *usageReader) Write(p []byte) (int, error) {
	if !r.streaming {
		if r.buf.Len()+len(p) > maxResponse {
			r.tooLong = true
			r.buf.Reset()
		}
		if !r.tooLong {
			r.buf.Write(p)
		}
		return len(p), nil
	}
	r.buf.Write(p)
	for {
		i := bytes.IndexByte(r.buf.Bytes(), '\n')
		if i < 0 {
			// An unfinished line stays for the next write.
			return len(p), nil
		}
		r.event(r.buf.Next(i + 1))
	}
}

// event reads one line of a stream.
func (r *usageReader) event(line []byte) {
	data, ok := bytes.CutPrefix(bytes.TrimRight(line, "\r\n"), []byte("data:"))
	if !ok {
		return
	}
	if r.api == ResponsesAPI {
		r.responsesEvent(bytes.TrimSpace(data))
		return
	}
	var e struct {
		Type    string `json:"type"`
		Message *struct {
			Model string    `json:"model"`
			Usage *reported `json:"usage"`
		} `json:"message"`
		Usage *reported `json:"usage"`
	}
	if json.Unmarshal(bytes.TrimSpace(data), &e) != nil {
		return
	}
	switch {
	case e.Type == "message_start" && e.Message != nil:
		if e.Message.Model != "" {
			r.model = e.Message.Model
		}
		if e.Message.Usage != nil {
			r.reported.merge(e.Message.Usage)
			r.found = true
		}
	case e.Type == "message_delta" && e.Usage != nil:
		r.reported.merge(e.Usage)
		r.found = true
	}
}

// usage returns the usage the response reported, and false when it
// reported none.
func (r *usageReader) usage() (usage, bool) {
	if r.api == ResponsesAPI {
		if !r.streaming && !r.tooLong {
			return responseUsage(r.buf.Bytes())
		}
		return r.final, r.found
	}
	if !r.streaming && !r.tooLong {
		var m struct {
			Model string    `json:"model"`
			Usage *reported `json:"usage"`
		}
		if json.Unmarshal(r.buf.Bytes(), &m) == nil && m.Usage != nil {
			return m.Usage.usage(m.Model), true
		}
		return usage{}, false
	}
	if !r.found {
		return usage{}, false
	}
	return r.reported.usage(r.model), true
}

// responsesEvent reads the data of one event of a Responses stream. Only
// a final event holds the usage.
func (r *usageReader) responsesEvent(data []byte) {
	var e struct {
		Type     string          `json:"type"`
		Response json.RawMessage `json:"response"`
	}
	if json.Unmarshal(data, &e) != nil {
		return
	}
	switch e.Type {
	case "response.completed", "response.incomplete", "response.failed":
		if u, ok := responseUsage(e.Response); ok {
			r.final, r.found = u, true
		}
	}
}

// responseUsage reads the usage of a response object of the Responses API:
// its model, its service tier, its web searches and its usage, whose
// input_tokens hold the cached input and the cache writes. Its cache
// writes count as writes for the shorter lifetime; the call's request
// decides (longCacheWrites). It returns false when the response has no
// usage.
func responseUsage(body []byte) (usage, bool) {
	var resp struct {
		Model       string `json:"model"`
		ServiceTier string `json:"service_tier"`
		Output      []struct {
			Type string `json:"type"`
		} `json:"output"`
		Usage *struct {
			InputTokens        int64 `json:"input_tokens"`
			InputTokensDetails struct {
				CachedTokens     int64 `json:"cached_tokens"`
				CacheWriteTokens int64 `json:"cache_write_tokens"`
			} `json:"input_tokens_details"`
			OutputTokens        int64 `json:"output_tokens"`
			OutputTokensDetails struct {
				ReasoningTokens int64 `json:"reasoning_tokens"`
			} `json:"output_tokens_details"`
		} `json:"usage"`
	}
	if json.Unmarshal(body, &resp) != nil || resp.Usage == nil {
		return usage{}, false
	}
	d := resp.Usage.InputTokensDetails
	u := usage{
		model:      resp.Model,
		input:      max(0, resp.Usage.InputTokens-d.CachedTokens-d.CacheWriteTokens),
		read:       d.CachedTokens,
		writeShort: d.CacheWriteTokens,
		output:     resp.Usage.OutputTokens,
		reasoning:  resp.Usage.OutputTokensDetails.ReasoningTokens,
		tier:       tierOf(resp.ServiceTier),
	}
	for _, item := range resp.Output {
		if item.Type == "web_search_call" {
			u.webSearches++
		}
	}
	return u, true
}

// longCacheWrites reports whether a call of the Responses API asks for a
// 12-hour cache lifetime anywhere: in prompt_cache_options or in a
// prompt_cache_breakpoint. The response does not say which lifetime its
// cache writes have, so they are then all priced as 12-hour writes, so
// that the cost cap never counts less.
func longCacheWrites(plain []byte) bool {
	var v any
	if json.Unmarshal(plain, &v) != nil {
		return false
	}
	return asksLongCache(v, "")
}

// asksLongCache reports whether v, the value of the key parent, or a value
// in it, is a prompt_cache_options or prompt_cache_breakpoint object with
// the ttl "12h".
func asksLongCache(v any, parent string) bool {
	switch v := v.(type) {
	case map[string]any:
		if ttl, _ := v["ttl"].(string); ttl == "12h" && (parent == "prompt_cache_options" || parent == "prompt_cache_breakpoint") {
			return true
		}
		for key, child := range v {
			if asksLongCache(child, key) {
				return true
			}
		}
	case []any:
		return slices.ContainsFunc(v, func(child any) bool { return asksLongCache(child, "") })
	}
	return false
}
