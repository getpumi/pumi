// SPDX-License-Identifier: Apache-2.0
// SPDX-FileCopyrightText: The Pumi Authors

package conformance

import (
	"encoding/json"
	"net/http"
)

// The fake provider's side of Anthropic's Messages API: an answer as a
// message, or as the stream of events that a call with "stream": true
// gets.

// messagesUsage is the usage of an answer.
func messagesUsage(output int) map[string]any {
	return map[string]any{
		"input_tokens":                usageInput,
		"cache_creation_input_tokens": usageFiveMinute + usageOneHour,
		"cache_read_input_tokens":     usageRead,
		"cache_creation": map[string]any{
			"ephemeral_5m_input_tokens": usageFiveMinute,
			"ephemeral_1h_input_tokens": usageOneHour,
		},
		"output_tokens": output,
		"service_tier":  "standard",
	}
}

// messagesContent returns the content blocks of an answer: its text, then
// its tool calls.
func messagesContent(a answer) []map[string]any {
	content := []map[string]any{}
	if a.turn.Text != "" {
		content = append(content, map[string]any{"type": "text", "text": a.turn.Text})
	}
	for i, c := range a.turn.Calls {
		content = append(content, map[string]any{
			"type": "tool_use", "id": "toolu_" + a.ids[i], "name": c.Name, "input": json.RawMessage(c.Input),
		})
	}
	return content
}

func messagesStopReason(a answer) string {
	if len(a.turn.Calls) > 0 {
		return "tool_use"
	}
	return "end_turn"
}

// messagesMessage is the message of an answer, with its content and usage.
func messagesMessage(a answer, content []map[string]any, stop any, output int) map[string]any {
	return map[string]any{
		"id": "msg_" + a.id, "type": "message", "role": "assistant", "model": a.model,
		"content": content, "stop_reason": stop, "stop_sequence": nil, "usage": messagesUsage(output),
	}
}

// writeMessages writes the answer a, as a stream when the call asked for
// one.
func writeMessages(w http.ResponseWriter, a answer, stream bool) {
	if !stream {
		writeJSON(w, http.StatusOK, messagesMessage(a, messagesContent(a), messagesStopReason(a), usageOutput))
		return
	}
	e := startMessages(w, a)
	for i, block := range messagesContent(a) {
		switch block["type"] {
		case "text":
			e.send("content_block_start", map[string]any{"type": "content_block_start", "index": i, "content_block": map[string]any{"type": "text", "text": ""}})
			e.send("content_block_delta", map[string]any{"type": "content_block_delta", "index": i, "delta": map[string]any{"type": "text_delta", "text": block["text"]}})
		default:
			start := map[string]any{"type": "tool_use", "id": block["id"], "name": block["name"], "input": map[string]any{}}
			e.send("content_block_start", map[string]any{"type": "content_block_start", "index": i, "content_block": start})
			partial, _ := block["input"].(json.RawMessage)
			e.send("content_block_delta", map[string]any{"type": "content_block_delta", "index": i, "delta": map[string]any{"type": "input_json_delta", "partial_json": string(partial)}})
		}
		e.send("content_block_stop", map[string]any{"type": "content_block_stop", "index": i})
	}
	usage := messagesUsage(usageOutput)
	delete(usage, "cache_creation")
	delete(usage, "service_tier")
	e.send("message_delta", map[string]any{"type": "message_delta", "delta": map[string]any{"stop_reason": messagesStopReason(a), "stop_sequence": nil}, "usage": usage})
	e.send("message_stop", map[string]any{"type": "message_stop"})
}

// startMessages starts the stream of the answer a with its first event.
func startMessages(w http.ResponseWriter, a answer) *events {
	e := newEvents(w)
	e.send("message_start", map[string]any{"type": "message_start", "message": messagesMessage(a, []map[string]any{}, nil, 1)})
	return e
}
