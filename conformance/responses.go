// SPDX-License-Identifier: Apache-2.0
// SPDX-FileCopyrightText: The Pumi Authors

package conformance

import (
	"net/http"
)

// The fake provider's side of OpenAI's Responses API: an answer as a
// response object, or as the stream of events that a call with
// "stream": true gets.

// createdAt is the creation time of every response, in Unix seconds.
const createdAt = 1795615306

// responsesUsage is the usage of an answer. The input tokens hold the
// cached input and the cache writes, and the output tokens hold the
// reasoning.
func responsesUsage() map[string]any {
	input := usageInput + usageRead + usageFiveMinute
	return map[string]any{
		"input_tokens":          input,
		"input_tokens_details":  map[string]any{"cached_tokens": usageRead, "cache_write_tokens": usageFiveMinute},
		"output_tokens":         usageOutput,
		"output_tokens_details": map[string]any{"reasoning_tokens": usageReasoning},
		"total_tokens":          input + usageOutput,
	}
}

// responsesItems returns the output items of an answer: its text as a
// message, then its tool calls, each done when done is set and as it
// starts otherwise.
func responsesItems(a answer, done bool) []map[string]any {
	status := "in_progress"
	if done {
		status = "completed"
	}
	items := []map[string]any{}
	if a.turn.Text != "" {
		content := []map[string]any{}
		if done {
			content = append(content, outputText(a.turn.Text))
		}
		items = append(items, map[string]any{"id": "msg_" + a.id, "type": "message", "status": status, "role": "assistant", "content": content})
	}
	for i, c := range a.turn.Calls {
		item := map[string]any{"status": status, "call_id": "call_" + a.ids[i], "name": c.Name}
		input := ""
		if done {
			input = c.Input
		}
		if c.Freeform {
			item["id"], item["type"], item["input"] = "ctc_"+a.ids[i], "custom_tool_call", input
		} else {
			item["id"], item["type"], item["arguments"] = "fc_"+a.ids[i], "function_call", input
		}
		items = append(items, item)
	}
	return items
}

func outputText(text string) map[string]any {
	return map[string]any{"type": "output_text", "text": text, "annotations": []any{}}
}

// responsesObject is the response of an answer with status and output.
func responsesObject(a answer, status string, output []map[string]any, usage any) map[string]any {
	return map[string]any{
		"id": "resp_" + a.id, "object": "response", "created_at": createdAt, "status": status,
		"model": a.model, "output": output, "usage": usage, "service_tier": "default",
		"error": nil, "incomplete_details": nil,
	}
}

// writeResponses writes the answer a, as a stream when the call asked for
// one.
func writeResponses(w http.ResponseWriter, a answer, stream bool) {
	done := responsesItems(a, true)
	if !stream {
		writeJSON(w, http.StatusOK, responsesObject(a, "completed", done, responsesUsage()))
		return
	}
	s := &responsesStream{e: startResponses(w, a), seq: 1}
	for i, item := range responsesItems(a, false) {
		s.send("response.output_item.added", map[string]any{"output_index": i, "item": item})
		s.item(i, done[i])
		s.send("response.output_item.done", map[string]any{"output_index": i, "item": done[i]})
	}
	s.send("response.completed", map[string]any{"response": responsesObject(a, "completed", done, responsesUsage())})
}

// responsesStream numbers the events of a stream.
type responsesStream struct {
	e   *events
	seq int
}

// send writes the event name with its type and sequence number added to
// data.
func (s *responsesStream) send(name string, data map[string]any) {
	data["type"], data["sequence_number"] = name, s.seq
	s.seq++
	s.e.send(name, data)
}

// item writes the events between the start and the end of the output item
// i, which done is when it ends.
func (s *responsesStream) item(i int, done map[string]any) {
	id := done["id"]
	switch done["type"] {
	case "message":
		text := ""
		if content, ok := done["content"].([]map[string]any); ok && len(content) > 0 {
			text, _ = content[0]["text"].(string)
		}
		at := map[string]any{"item_id": id, "output_index": i, "content_index": 0}
		s.send("response.content_part.added", with(at, "part", outputText("")))
		s.send("response.output_text.delta", with(at, "delta", text))
		s.send("response.output_text.done", with(at, "text", text))
		s.send("response.content_part.done", with(at, "part", outputText(text)))
	case "custom_tool_call":
		at := map[string]any{"item_id": id, "output_index": i}
		s.send("response.custom_tool_call_input.delta", with(at, "delta", done["input"]))
		s.send("response.custom_tool_call_input.done", with(at, "input", done["input"]))
	default:
		at := map[string]any{"item_id": id, "output_index": i}
		s.send("response.function_call_arguments.delta", with(at, "delta", done["arguments"]))
		s.send("response.function_call_arguments.done", with(at, "arguments", done["arguments"]))
	}
}

// with returns a copy of m with key set to v.
func with(m map[string]any, key string, v any) map[string]any {
	out := make(map[string]any, len(m)+1)
	for k, x := range m {
		out[k] = x
	}
	out[key] = v
	return out
}

// startResponses starts the stream of the answer a with its first event.
func startResponses(w http.ResponseWriter, a answer) *events {
	e := newEvents(w)
	e.send("response.created", map[string]any{
		"type": "response.created", "sequence_number": 0,
		"response": responsesObject(a, "in_progress", []map[string]any{}, nil),
	})
	return e
}
