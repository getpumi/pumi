// SPDX-License-Identifier: Apache-2.0
// SPDX-FileCopyrightText: The Pumi Authors

package gateway

import (
	"bytes"
	"encoding/json"
)

// usage is what one call used, as the provider reported it in its
// response.
type usage struct {
	// model is the model that answered, when the response names it.
	model string
	// The tokens of each kind (decision 0227).
	input, write5m, write1h, read, output int64
	// webSearches is the number of web searches the call made.
	webSearches int64
	// fast is a call in fast mode, usOnly a call with US-only inference.
	fast, usOnly bool
}

// tokens is the tokens of every kind, which count toward the token cap
// (decision 0227).
func (u usage) tokens() int64 {
	return u.input + u.write5m + u.write1h + u.read + u.output
}

// picodollars is the reference cost of the call at the model's list prices
// (decisions 0229 and 0239). A number of tokens times a price in
// microdollars per million tokens is a cost in picodollars, so the cost is
// exact.
func (m Model) picodollars(u usage) int64 {
	r := m.Standard
	if u.fast && m.Fast != nil {
		r = *m.Fast
	}
	if m.LongPrompt != nil && u.input+u.write5m+u.write1h+u.read > m.LongPromptTokens {
		r = *m.LongPrompt
	}
	cost := u.input*r.Input + u.write5m*r.CacheWrite5m + u.write1h*r.CacheWrite1h + u.read*r.CacheRead + u.output*r.Output
	if u.usOnly {
		// Rounded up, so that the cost cap never counts less.
		cost = (cost*usOnlyNumerator + usOnlyDenominator - 1) / usOnlyDenominator
	}
	return cost + u.webSearches*webSearchMicrodollars*1_000_000
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
		u.write5m = value(r.CacheCreation.Ephemeral5m)
		u.write1h = value(r.CacheCreation.Ephemeral1h)
	}
	if rest := value(r.CacheCreationInputTokens) - u.write5m - u.write1h; rest > 0 {
		u.write5m += rest
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
// finds the usage the response reports: in the message_start and
// message_delta events of a stream, whose later values take the place of
// earlier ones, or in the usage of a whole response.
type usageReader struct {
	streaming bool
	// buf holds the stream's unfinished line, or the whole response.
	buf      bytes.Buffer
	found    bool
	tooLong  bool
	model    string
	reported reported
}

func newUsageReader(streaming bool) *usageReader {
	return &usageReader{streaming: streaming}
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
