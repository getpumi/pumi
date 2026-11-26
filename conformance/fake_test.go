// SPDX-License-Identifier: Apache-2.0
// SPDX-FileCopyrightText: The Pumi Authors

package conformance

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	harnessv1 "getpumi.dev/pumi/api/gen/pumi/harness/v1"
	"google.golang.org/protobuf/encoding/protojson"
)

// The fake adapter is the test binary, run again with fakeEnv set. It is
// its own harness: it calls the fake provider itself, makes the tool calls
// of the answers with the capability list enforced, and writes the lines
// of the adapter protocol. The fault in fakeFaultEnv makes it break one
// rule, which the scenario that checks the rule must catch.
const (
	fakeEnv          = "PUMI_CONFORMANCE_FAKE_ADAPTER"
	fakeAPIEnv       = "PUMI_FAKE_API"
	fakeFaultEnv     = "PUMI_FAKE_FAULT"
	fakeProviderEnv  = "PUMI_FAKE_PROVIDER_URL"
	fakeSignInEnv    = "PUMI_FAKE_SIGN_IN"
	fakeNestedEnv    = "PUMI_FAKE_NESTED"
	responsesAPIName = "responses"
)

// The fake harness's own tools.
const (
	toolWrite  = "write"
	toolPatch  = "patch"
	toolRead   = "read"
	toolRun    = "run"
	toolFetch  = "fetch"
	toolOutput = "output"
)

// fake is one run of the fake adapter.
type fake struct {
	inv   *harnessv1.Invocation
	api   API
	fault string
	// base and header are where and with what the fake calls the
	// provider, and model the model it asks for.
	base   string
	header http.Header
	model  string
	// elsewhere is where the person's own settings point.
	elsewhere string
	root      *os.Root
	out       io.Writer
	log       io.Writer
	// conv is the conversation so far, as items of the API.
	conv     []any
	refusals []*harnessv1.Refusal
	output   string
}

// fakeAdapter runs the fake adapter and returns its exit status.
func fakeAdapter(stdin io.Reader, stdout, stderr io.Writer) int {
	in := bufio.NewReader(stdin)
	line, err := in.ReadBytes('\n')
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "fake adapter: %v\n", err)
		return 2
	}
	inv := &harnessv1.Invocation{}
	if err := protojson.Unmarshal(line, inv); err != nil {
		_, _ = fmt.Fprintf(stderr, "fake adapter: %v\n", err)
		return 2
	}
	f, err := newFake(inv, stdout, stderr)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "fake adapter: %v\n", err)
		return 2
	}
	defer func() { _ = f.root.Close() }()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if f.fault != "ignore-stdin" {
		go func() {
			_, _ = io.Copy(io.Discard, in)
			cancel()
		}()
	}
	status := f.run(ctx)
	return f.finish(status)
}

func newFake(inv *harnessv1.Invocation, stdout, stderr io.Writer) (*fake, error) {
	f := &fake{inv: inv, api: APIMessages, fault: os.Getenv(fakeFaultEnv), header: http.Header{}, out: stdout, log: stderr, elsewhere: os.Getenv(fakeProviderEnv)}
	if os.Getenv(fakeAPIEnv) == responsesAPIName {
		f.api = APIResponses
	}
	if gw := inv.GetGateway(); gw != nil {
		f.base, f.model = gw.GetBaseUrl(), inv.GetModelRole()
		f.gatewayHeader(gw.GetToken())
	} else {
		f.base, f.model = os.Getenv(fakeProviderEnv), inv.GetModel()
		if f.fault != "no-credential" {
			f.header.Set("Authorization", "Bearer "+os.Getenv(fakeSignInEnv))
		}
	}
	if f.fault == "model" {
		f.model = "some-other-model"
	}
	root, err := os.OpenRoot(inv.GetWorkspace())
	if err != nil {
		return nil, err
	}
	f.root = root
	return f, nil
}

// gatewayHeader sets the invocation token as the API key, in the header
// each API reads it from.
func (f *fake) gatewayHeader(token string) {
	if f.fault == "token" {
		token = "not-the-token"
	}
	if f.api == APIMessages {
		f.header.Set("X-Api-Key", token)
	} else {
		f.header.Set("Authorization", "Bearer "+token)
	}
	if f.fault == "sign-in-leak" {
		f.header.Set("X-Person-Sign-In", os.Getenv(fakeSignInEnv))
	}
}

// finish writes the result and returns the exit status.
func (f *fake) finish(status harnessv1.Result_Status) int {
	res := &harnessv1.Result{Status: status, Output: f.output, Refusals: f.refusals}
	switch f.fault {
	case "no-result":
		return 0
	case "bad-line":
		_, _ = io.WriteString(f.out, "this is not a line of the protocol\n")
	case "wrong-output":
		res.Output = `{"answers":[],"unknownField":1}`
	}
	f.writeLine(&harnessv1.Line{Kind: &harnessv1.Line_Result{Result: res}})
	switch f.fault {
	case "exit-status":
		return 1
	case "line-after-result":
		f.writeLine(&harnessv1.Line{Kind: &harnessv1.Line_Usage{Usage: &harnessv1.UsageFigure{Model: f.model}}})
	}
	return 0
}

func (f *fake) writeLine(l *harnessv1.Line) {
	data, _ := protojson.Marshal(l)
	_, _ = f.out.Write(append(data, '\n'))
}

func (f *fake) logf(format string, args ...any) {
	_, _ = fmt.Fprintf(f.log, "fake adapter: "+format+"\n", args...)
}

// run runs the conversation and returns how it ended.
func (f *fake) run(ctx context.Context) harnessv1.Result_Status {
	if f.fault == "nested" && os.Getenv(fakeNestedEnv) != "" {
		f.logf("refusing to start inside a session of itself")
		return harnessv1.Result_STATUS_FAILED
	}
	if f.fault == "elsewhere" {
		f.callElsewhere(ctx)
	}
	f.conv = []any{f.userItem(f.inv.GetInput().GetTask())}
	for range 20 {
		if status := f.turn(ctx); status != harnessv1.Result_STATUS_UNSPECIFIED {
			return status
		}
	}
	return harnessv1.Result_STATUS_FAILED
}

// turn makes one call and the tool calls of its answer. It returns the
// status that ends the run, or STATUS_UNSPECIFIED to go on.
func (f *fake) turn(ctx context.Context) harnessv1.Result_Status {
	reply, status := f.call(ctx)
	if status != harnessv1.Result_STATUS_UNSPECIFIED {
		return status
	}
	if f.fault != "no-usage" {
		f.writeLine(&harnessv1.Line{Kind: &harnessv1.Line_Usage{Usage: reply.usage}})
	}
	if len(reply.calls) == 0 {
		if f.inv.GetOutputSchema() != nil && f.output == "" {
			f.output = reply.text
		}
		return harnessv1.Result_STATUS_COMPLETED
	}
	f.conv = append(f.conv, reply.items...)
	for _, c := range reply.calls {
		f.conv = append(f.conv, f.resultItem(c, f.tool(ctx, c)))
	}
	return harnessv1.Result_STATUS_UNSPECIFIED
}

// callElsewhere makes one call where the person's own settings point.
func (f *fake) callElsewhere(ctx context.Context) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, f.elsewhere+"/v1/messages", strings.NewReader("{}"))
	if err != nil {
		return
	}
	if resp, err := http.DefaultClient.Do(req); err == nil {
		_ = resp.Body.Close()
	}
}

// toolUse is one tool call of the model's answer.
type toolUse struct {
	id, name, input string
	freeform        bool
}

// reply is the model's answer to one call.
type reply struct {
	text  string
	calls []toolUse
	// items are the answer's items, to send back in the conversation.
	items []any
	usage *harnessv1.UsageFigure
}

// call calls the provider with the conversation. It returns the status
// that ends the run when the call does not give an answer.
func (f *fake) call(ctx context.Context) (*reply, harnessv1.Result_Status) {
	body := map[string]any{"model": f.model, "stream": true}
	path := "/v1/messages"
	if f.api == APIMessages {
		body["max_tokens"], body["messages"] = 1024, f.conv
	} else {
		body["input"], path = f.conv, "/v1/responses"
	}
	data, _ := json.Marshal(body)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, f.base+path, bytes.NewReader(data))
	if err != nil {
		f.logf("%v", err)
		return nil, harnessv1.Result_STATUS_FAILED
	}
	req.Header = f.header.Clone()
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, f.callFailed(ctx, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, f.refused(ctx, resp)
	}
	r, err := f.read(resp.Body)
	if err != nil {
		return nil, f.callFailed(ctx, err)
	}
	return r, harnessv1.Result_STATUS_UNSPECIFIED
}

// callFailed returns the status of a call that failed with err: stopped
// when the runner closed standard input.
func (f *fake) callFailed(ctx context.Context, err error) harnessv1.Result_Status {
	if ctx.Err() != nil {
		f.logf("stopped on request")
		return harnessv1.Result_STATUS_STOPPED
	}
	f.logf("the call failed: %v", err)
	return harnessv1.Result_STATUS_FAILED
}

// refused returns the status of a call that the provider refused.
func (f *fake) refused(ctx context.Context, resp *http.Response) harnessv1.Result_Status {
	body, _ := io.ReadAll(resp.Body)
	f.logf("the provider answered %d: %s", resp.StatusCode, body)
	switch {
	case resp.StatusCode == http.StatusTooManyRequests && f.fault == "wait-limit":
		select {
		case <-ctx.Done():
			return harnessv1.Result_STATUS_STOPPED
		case <-time.After(time.Hour):
			return harnessv1.Result_STATUS_FAILED
		}
	case resp.StatusCode == http.StatusTooManyRequests:
		return harnessv1.Result_STATUS_LIMITED
	case f.fault == "not-failed":
		return harnessv1.Result_STATUS_COMPLETED
	}
	return harnessv1.Result_STATUS_FAILED
}

// read reads a streamed answer.
func (f *fake) read(body io.Reader) (*reply, error) {
	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 0, 64<<10), 16<<20)
	var events []map[string]json.RawMessage
	for scanner.Scan() {
		data, ok := strings.CutPrefix(scanner.Text(), "data: ")
		if !ok {
			continue
		}
		var e map[string]json.RawMessage
		if err := json.Unmarshal([]byte(data), &e); err != nil {
			return nil, err
		}
		events = append(events, e)
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	if f.api == APIMessages {
		return f.readMessages(events)
	}
	return f.readResponses(events)
}

// messagesEvent holds the fields of the Messages API's events that the
// fake reads.
type messagesEvent struct {
	Type    string `json:"type"`
	Message struct {
		Model string       `json:"model"`
		Usage messagesUsed `json:"usage"`
	} `json:"message"`
	ContentBlock struct {
		Type string `json:"type"`
		ID   string `json:"id"`
		Name string `json:"name"`
	} `json:"content_block"`
	Delta struct {
		Text        string `json:"text"`
		PartialJSON string `json:"partial_json"`
	} `json:"delta"`
	Usage messagesUsed `json:"usage"`
}

type messagesUsed struct {
	Input         int64 `json:"input_tokens"`
	Output        int64 `json:"output_tokens"`
	Read          int64 `json:"cache_read_input_tokens"`
	Created       int64 `json:"cache_creation_input_tokens"`
	CacheCreation struct {
		FiveMinute int64 `json:"ephemeral_5m_input_tokens"`
		OneHour    int64 `json:"ephemeral_1h_input_tokens"`
	} `json:"cache_creation"`
}

func (f *fake) readMessages(events []map[string]json.RawMessage) (*reply, error) {
	r := &reply{usage: &harnessv1.UsageFigure{}}
	var content []map[string]any
	for _, raw := range events {
		data, _ := json.Marshal(raw)
		var e messagesEvent
		if err := json.Unmarshal(data, &e); err != nil {
			return nil, err
		}
		f.readMessagesEvent(e, r, &content)
	}
	r.items = []any{map[string]any{"role": "assistant", "content": content}}
	return r, nil
}

func (f *fake) readMessagesEvent(e messagesEvent, r *reply, content *[]map[string]any) {
	switch e.Type {
	case "message_start":
		u := e.Message.Usage
		r.usage = &harnessv1.UsageFigure{
			Model: e.Message.Model, InputTokens: u.Input, CacheReadTokens: u.Read,
			FiveMinuteCacheWriteTokens: u.CacheCreation.FiveMinute, OneHourCacheWriteTokens: u.CacheCreation.OneHour,
		}
		if f.fault == "wrong-usage" {
			r.usage.FiveMinuteCacheWriteTokens, r.usage.OneHourCacheWriteTokens = u.Created, 0
		}
	case "content_block_start":
		if e.ContentBlock.Type == "tool_use" {
			r.calls = append(r.calls, toolUse{id: e.ContentBlock.ID, name: e.ContentBlock.Name})
		}
	case "content_block_delta":
		if n := len(r.calls); e.Delta.PartialJSON != "" && n > 0 {
			r.calls[n-1].input += e.Delta.PartialJSON
		}
		r.text += e.Delta.Text
	case "message_delta":
		r.usage.OutputTokens = e.Usage.Output
	case "content_block_stop":
		*content = f.messagesBlocks(r)
	}
}

// messagesBlocks returns the content blocks of the answer so far.
func (f *fake) messagesBlocks(r *reply) []map[string]any {
	var blocks []map[string]any
	if r.text != "" {
		blocks = append(blocks, map[string]any{"type": "text", "text": r.text})
	}
	for _, c := range r.calls {
		blocks = append(blocks, map[string]any{"type": "tool_use", "id": c.id, "name": c.name, "input": json.RawMessage(orEmpty(c.input))})
	}
	return blocks
}

func orEmpty(input string) string {
	if input == "" {
		return "{}"
	}
	return input
}

// responsesCompleted holds the fields of the Responses API's
// response.completed event that the fake reads.
type responsesCompleted struct {
	Response struct {
		Model  string            `json:"model"`
		Output []json.RawMessage `json:"output"`
		Usage  struct {
			Input   int64 `json:"input_tokens"`
			Details struct {
				Cached int64 `json:"cached_tokens"`
				Writes int64 `json:"cache_write_tokens"`
			} `json:"input_tokens_details"`
			Output int64 `json:"output_tokens"`
		} `json:"usage"`
	} `json:"response"`
}

// responsesItem holds the fields of an output item that the fake reads.
type responsesItem struct {
	Type      string `json:"type"`
	CallID    string `json:"call_id"`
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
	Input     string `json:"input"`
	Content   []struct {
		Text string `json:"text"`
	} `json:"content"`
}

func (f *fake) readResponses(events []map[string]json.RawMessage) (*reply, error) {
	for _, raw := range events {
		if string(raw["type"]) != `"response.completed"` {
			continue
		}
		data, _ := json.Marshal(raw)
		var e responsesCompleted
		if err := json.Unmarshal(data, &e); err != nil {
			return nil, err
		}
		return f.readResponse(e)
	}
	return nil, errors.New("the stream has no response.completed")
}

func (f *fake) readResponse(e responsesCompleted) (*reply, error) {
	u := e.Response.Usage
	r := &reply{usage: &harnessv1.UsageFigure{
		Model: e.Response.Model, InputTokens: u.Input - u.Details.Cached - u.Details.Writes,
		CacheReadTokens: u.Details.Cached, FiveMinuteCacheWriteTokens: u.Details.Writes, OutputTokens: u.Output,
	}}
	if f.fault == "wrong-usage" {
		r.usage.InputTokens = u.Input
	}
	for _, raw := range e.Response.Output {
		var item responsesItem
		if err := json.Unmarshal(raw, &item); err != nil {
			return nil, err
		}
		r.items = append(r.items, raw)
		switch item.Type {
		case "message":
			for _, c := range item.Content {
				r.text += c.Text
			}
		case "function_call":
			r.calls = append(r.calls, toolUse{id: item.CallID, name: item.Name, input: item.Arguments})
		case "custom_tool_call":
			r.calls = append(r.calls, toolUse{id: item.CallID, name: item.Name, input: item.Input, freeform: true})
		}
	}
	return r, nil
}

// userItem is the task as the first item of the conversation.
func (f *fake) userItem(text string) any {
	if f.api == APIMessages {
		return map[string]any{"role": "user", "content": text}
	}
	return map[string]any{"type": "message", "role": "user", "content": []any{map[string]any{"type": "input_text", "text": text}}}
}

// resultItem is the result of the tool call c.
func (f *fake) resultItem(c toolUse, result string) any {
	switch {
	case f.api == APIMessages:
		return map[string]any{"role": "user", "content": []any{map[string]any{"type": "tool_result", "tool_use_id": c.id, "content": result}}}
	case c.freeform:
		return map[string]any{"type": "custom_tool_call_output", "call_id": c.id, "output": result}
	}
	return map[string]any{"type": "function_call_output", "call_id": c.id, "output": result}
}

// toolInput is the input of the fake harness's tools.
type toolInput struct {
	Path    string          `json:"path"`
	Content string          `json:"content"`
	Command string          `json:"command"`
	URL     string          `json:"url"`
	Output  json.RawMessage `json:"output"`
}

// tool makes the tool call c with the capability list enforced and
// returns its result.
func (f *fake) tool(ctx context.Context, c toolUse) string {
	var in toolInput
	if c.freeform {
		in.Path, in.Content, _ = strings.Cut(c.input, "\n")
	} else if err := json.Unmarshal([]byte(c.input), &in); err != nil {
		return "the input does not parse: " + err.Error()
	}
	kind, ok := map[string]harnessv1.Capabilities_ToolKind{toolWrite: edit, toolPatch: edit, toolRead: read, toolRun: run, toolFetch: web}[c.name]
	if ok && !f.hasKind(kind) {
		return "no such tool: " + c.name
	}
	switch c.name {
	case toolWrite, toolPatch:
		return f.write(in.Path, in.Content)
	case toolRead:
		data, err := f.root.ReadFile(f.rel(in.Path))
		if err != nil {
			return err.Error()
		}
		return string(data)
	case toolRun:
		return f.runCommand(in.Command)
	case toolFetch:
		return f.fetch(ctx, in.URL)
	case toolOutput:
		f.output = string(in.Output)
		return "the structured output is recorded"
	}
	return "no such tool: " + c.name
}

// hasKind reports whether the capability list has the kind of tool k, or
// the fault ignores it.
func (f *fake) hasKind(k harnessv1.Capabilities_ToolKind) bool {
	name := map[harnessv1.Capabilities_ToolKind]string{read: "tool-read", edit: "tool-edit", run: "tool-run", web: "tool-web"}[k]
	return f.fault == name || slices.Contains(f.inv.GetCapabilities().GetTools(), k)
}

func (f *fake) refuse(r *harnessv1.Refusal) string {
	if f.fault != "no-refusals" {
		f.refusals = append(f.refusals, r)
	}
	return "refused by the settings"
}

// rel returns p relative to the workspace, or "" for a path outside it.
func (f *fake) rel(p string) string {
	if filepath.IsAbs(p) {
		r, err := filepath.Rel(f.inv.GetWorkspace(), p)
		if err != nil {
			return ""
		}
		p = r
	}
	p = filepath.ToSlash(filepath.Clean(p))
	if p == ".." || strings.HasPrefix(p, "../") {
		return ""
	}
	return p
}

func (f *fake) write(p, content string) string {
	rel := f.rel(p)
	if !f.mayWrite(rel) {
		return f.refuse(&harnessv1.Refusal{Tool: edit, Asked: &harnessv1.Refusal_Path{Path: p}})
	}
	if err := f.root.MkdirAll(filepath.Dir(rel), 0o750); err != nil {
		return err.Error()
	}
	if err := f.root.WriteFile(rel, []byte(content), 0o600); err != nil {
		return err.Error()
	}
	return "written"
}

// mayWrite reports whether a write paths of the capability list allows
// rel, a path relative to the workspace: a glob dir/** allows the files
// under dir in the workspace, ** every file, and any other glob that file.
func (f *fake) mayWrite(rel string) bool {
	if f.fault == "write-anywhere" {
		return true
	}
	if rel == "" {
		return false
	}
	for _, g := range f.inv.GetCapabilities().GetWritePaths() {
		dir, all := strings.CutSuffix(g, "/**")
		switch {
		case g == "**", rel == g, all && strings.HasPrefix(rel, dir+"/"):
			return true
		case all && f.fault == "unanchored" && strings.Contains("/"+rel, "/"+dir+"/"):
			return true
		}
	}
	return false
}

// runCommand runs a command with the capability list enforced. The fake
// harness knows only touch and mkdir, and refuses any shell operator.
func (f *fake) runCommand(command string) string {
	parts := []string{command}
	if f.fault == "chain" {
		parts = strings.Split(command, "&&")
	}
	if !f.mayRun(parts[0]) {
		return f.refuse(&harnessv1.Refusal{Tool: run, Asked: &harnessv1.Refusal_Command{Command: command}})
	}
	var out []string
	for _, part := range parts {
		out = append(out, f.exec(part))
	}
	return strings.Join(out, "\n")
}

// words splits a command into its leading NAME=value assignments and the
// rest of its words.
func words(command string) (assignments, args []string) {
	for _, w := range strings.Fields(command) {
		if name, _, ok := strings.Cut(w, "="); ok && len(args) == 0 && name != "" {
			assignments = append(assignments, w)
			continue
		}
		args = append(args, w)
	}
	return assignments, args
}

// mayRun reports whether an entry of the capability list allows command:
// the command starts with the entry's words and has exactly its
// assignments (decision 0248).
func (f *fake) mayRun(command string) bool {
	if f.fault == "commands-any" {
		return true
	}
	if f.fault != "chain" && strings.ContainsAny(command, ";&|`$()<>\n") {
		return false
	}
	assignments, args := words(command)
	for _, entry := range f.inv.GetCapabilities().GetCommands() {
		want, prefix := words(entry)
		sameAssignments := slices.Equal(assignments, want) || f.fault == "assignments-ignored"
		if sameAssignments && len(args) >= len(prefix) && slices.Equal(args[:len(prefix)], prefix) {
			return true
		}
	}
	return false
}

// exec runs one command of the fake harness.
func (f *fake) exec(command string) string {
	_, args := words(command)
	if len(args) < 2 {
		return "the fake harness runs only touch and mkdir with a path"
	}
	for _, p := range args[1:] {
		rel := f.rel(p)
		if rel == "" {
			return "outside the workspace: " + p
		}
		var err error
		switch args[0] {
		case "touch":
			err = f.root.WriteFile(rel, nil, 0o600)
		case "mkdir":
			err = f.root.Mkdir(rel, 0o750)
		default:
			return "command not found: " + args[0]
		}
		if err != nil {
			return err.Error()
		}
	}
	return ""
}

// fetch fetches u when the network access lists its host.
func (f *fake) fetch(ctx context.Context, u string) string {
	parsed, err := url.Parse(u)
	if err != nil {
		return err.Error()
	}
	network := f.inv.GetCapabilities().GetNetwork()
	listed := network.GetAccess() == harnessv1.Capabilities_Network_ACCESS_HOSTS && slices.Contains(network.GetHosts(), parsed.Hostname())
	if !listed && f.fault != "network-any" {
		return f.refuse(&harnessv1.Refusal{Tool: web, Asked: &harnessv1.Refusal_Host{Host: parsed.Hostname()}})
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, parsed.String(), nil)
	if err != nil {
		return err.Error()
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err.Error()
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(resp.Body)
	return string(body)
}
