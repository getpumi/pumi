// SPDX-License-Identifier: Apache-2.0
// SPDX-FileCopyrightText: The Pumi Authors

package conformance

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strconv"
	"strings"
	"sync"
)

// behavior is how the fake provider answers the model calls of a run.
type behavior int

const (
	// answerScript answers each call with the scripted turn.
	answerScript behavior = iota
	// holdCall starts the answer to the first call that carries the task and
	// then holds it open until the run ends, so that the run can be
	// stopped in the middle of a call.
	holdCall
	// limitCalls answers every model call with a usage or rate limit error.
	limitCalls
	// rejectCalls answers every model call with an error that no retry
	// changes.
	rejectCalls
)

// The usage of every model call that the fake provider answers: the input
// tokens that were not read from or written to the cache, the 5-minute
// and 1-hour cache writes, the cache reads, the output and, of the output,
// the reasoning. The Responses API counts the cached input and the cache
// writes in its input tokens and does not split the writes by duration.
const (
	usageInput      = 11
	usageFiveMinute = 7
	usageOneHour    = 3
	usageRead       = 13
	usageOutput     = 17
	usageReasoning  = 5
)

// endpoint is the kind of a call that the fake provider takes.
type endpoint int

const (
	notAModelCall endpoint = iota
	messagesCall
	countTokensCall
	responsesCall
)

// call is one call that the fake provider took.
type call struct {
	method, path string
	header       http.Header
	body         string
	endpoint     endpoint
	// model is the model that a model call names.
	model string
	// scripted reports whether the call carries the task, so that the
	// fake provider answered it from the script.
	scripted bool
	// answered reports whether the fake provider answered the call with
	// the model's answer and the usage.
	answered bool
}

// provider is the fake provider: it serves one provider API and scripts
// the model's side of every call.
type provider struct {
	api API
	// nonce is in the task, so a call that carries it is part of the
	// harness's conversation and not a call of its own, such as a check of
	// its quota.
	nonce string
	// turns are the scripted answers: one for each step, then the last.
	turns    []Turn
	behavior behavior
	server   *httptest.Server

	mu       sync.Mutex
	calls    []call
	held     chan struct{}
	heldOnce sync.Once
	// done is closed when the run ends, which ends a held call.
	done     chan struct{}
	doneOnce sync.Once
}

// newProvider starts a fake provider for api.
func newProvider(api API, nonce string, turns []Turn, b behavior) *provider {
	p := &provider{api: api, nonce: nonce, turns: turns, behavior: b, held: make(chan struct{}), done: make(chan struct{})}
	p.server = httptest.NewServer(p)
	return p
}

// URL is the fake provider's base URL.
func (p *provider) URL() string { return p.server.URL }

// close ends any held call and stops the server.
func (p *provider) close() {
	p.end()
	p.server.Close()
}

// end ends any held call.
func (p *provider) end() {
	p.doneOnce.Do(func() { close(p.done) })
}

// taken returns a copy of the calls that the fake provider took, in
// order.
func (p *provider) taken() []call {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]call, len(p.calls))
	copy(out, p.calls)
	return out
}

func (p *provider) record(c call) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.calls = append(p.calls, c)
}

// maxBody is the largest body of a call that the fake provider reads.
const maxBody = 64 << 20

func (p *provider) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(io.LimitReader(r.Body, maxBody))
	c := call{method: r.Method, path: r.URL.Path, header: r.Header.Clone(), body: string(body), endpoint: p.endpointOf(r)}
	if c.endpoint == notAModelCall {
		p.record(c)
		http.NotFound(w, r)
		return
	}
	var req struct {
		Model  string `json:"model"`
		Stream bool   `json:"stream"`
	}
	_ = json.Unmarshal(body, &req)
	c.model = req.Model
	c.scripted = strings.Contains(c.body, p.nonce)
	switch {
	case c.endpoint == countTokensCall:
		p.record(c)
		writeJSON(w, http.StatusOK, map[string]any{"input_tokens": usageInput})
		return
	case p.behavior == limitCalls:
		p.record(c)
		p.writeLimit(w)
		return
	case p.behavior == rejectCalls:
		p.record(c)
		p.writeReject(w)
		return
	}
	turn, k := Turn{Text: "ok"}, -1
	if c.scripted {
		k = p.next(c.body)
		turn = p.turns[min(k, len(p.turns)-1)]
	}
	if c.scripted && p.behavior == holdCall && p.holdFirst() {
		p.record(c)
		p.writeHeld(w, r, req.Model)
		return
	}
	c.answered = true
	p.record(c)
	a := answerOf(req.Model, turn, k)
	if p.api == APIMessages {
		writeMessages(w, a, req.Stream)
		return
	}
	writeResponses(w, a, req.Stream)
}

// endpointOf returns the kind of the call r. The base URL that a harness
// is given may end before or after /v1, so the fake provider goes by the
// end of the path.
func (p *provider) endpointOf(r *http.Request) endpoint {
	if r.Method != http.MethodPost {
		return notAModelCall
	}
	path := strings.TrimSuffix(r.URL.Path, "/")
	switch {
	case p.api == APIMessages && strings.HasSuffix(path, "/messages/count_tokens"):
		return countTokensCall
	case p.api == APIMessages && strings.HasSuffix(path, "/messages"):
		return messagesCall
	case p.api == APIResponses && strings.HasSuffix(path, "/responses"):
		return responsesCall
	}
	return notAModelCall
}

// holdFirst reports whether this is the first call to hold, and marks it
// held.
func (p *provider) holdFirst() bool {
	first := false
	p.heldOnce.Do(func() { first = true })
	return first
}

// toolCallMark matches the id that the fake provider gives a tool call of
// the answer to step k: pumi_k_i, for its i-th tool call.
var toolCallMark = regexp.MustCompile(`pumi_([0-9]+)_[0-9]+`)

// next returns the index of the scripted turn that answers a call with
// body: one after the highest step whose tool calls the body holds, which
// it holds with their results, or 0.
func (p *provider) next(body string) int {
	k := 0
	for _, m := range toolCallMark.FindAllStringSubmatch(body, -1) {
		if n, err := strconv.Atoi(m[1]); err == nil && n+1 > k {
			k = n + 1
		}
	}
	return k
}

// writeHeld starts an answer and holds it until the run ends or the
// harness ends the call.
func (p *provider) writeHeld(w http.ResponseWriter, r *http.Request, model string) {
	start := answerOf(model, Turn{}, 0)
	if p.api == APIMessages {
		startMessages(w, start)
	} else {
		startResponses(w, start)
	}
	close(p.held)
	select {
	case <-r.Context().Done():
	case <-p.done:
	}
}

func (p *provider) writeLimit(w http.ResponseWriter) {
	// A retry after an hour: a harness that waits it out does not end
	// within the suite's time.
	w.Header().Set("Retry-After", "3600")
	if p.api == APIMessages {
		w.Header().Set("Anthropic-Ratelimit-Unified-Status", "rejected")
		writeJSON(w, http.StatusTooManyRequests, map[string]any{
			"type":  "error",
			"error": map[string]any{"type": "rate_limit_error", "message": "This request would exceed your rate limit."},
		})
		return
	}
	writeJSON(w, http.StatusTooManyRequests, map[string]any{"error": map[string]any{
		"message": "You exceeded your current quota, please check your plan and billing details.",
		"type":    "insufficient_quota", "param": nil, "code": "insufficient_quota",
	}})
}

func (p *provider) writeReject(w http.ResponseWriter) {
	const message = "The conformance suite's fake provider refuses this call."
	if p.api == APIMessages {
		writeJSON(w, http.StatusBadRequest, map[string]any{
			"type":  "error",
			"error": map[string]any{"type": "invalid_request_error", "message": message},
		})
		return
	}
	writeJSON(w, http.StatusBadRequest, map[string]any{"error": map[string]any{
		"message": message, "type": "invalid_request_error", "param": nil, "code": nil,
	}})
}

// answer is a turn ready to be written in either API: the model and the
// turn, with an id for the answer and for each tool call.
type answer struct {
	model string
	id    string
	turn  Turn
	ids   []string
}

// answerOf gives the turn that answers step k its ids. A side call, which
// is not part of the script, has k -1.
func answerOf(model string, t Turn, k int) answer {
	mark := "side"
	if k >= 0 {
		mark = strconv.Itoa(k)
	}
	a := answer{model: model, id: "pumi_" + mark, turn: t}
	for i := range t.Calls {
		a.ids = append(a.ids, "pumi_"+mark+"_"+strconv.Itoa(i))
	}
	return a
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// events writes server-sent events.
type events struct {
	w http.ResponseWriter
}

func newEvents(w http.ResponseWriter) *events {
	w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(http.StatusOK)
	return &events{w: w}
}

// send writes one event whose data is v as JSON, and flushes it.
func (e *events) send(name string, v any) {
	data, _ := json.Marshal(v)
	_, _ = io.WriteString(e.w, "event: "+name+"\ndata: "+string(data)+"\n\n")
	if f, ok := e.w.(http.Flusher); ok {
		f.Flush()
	}
}

// recorder is a server that records the requests it takes: the address
// of a host that the run must not reach.
type recorder struct {
	server *httptest.Server
	mu     sync.Mutex
	paths  []string
}

func newRecorder() *recorder {
	r := &recorder{}
	r.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		r.mu.Lock()
		r.paths = append(r.paths, req.URL.Path)
		r.mu.Unlock()
		_, _ = io.WriteString(w, "pumi conformance\n")
	}))
	return r
}

// requested returns the paths of the requests that the recorder took.
func (r *recorder) requested() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.paths...)
}
