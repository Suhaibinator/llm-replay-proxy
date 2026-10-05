package devseed

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/local/llm-replay-proxy/internal/model"
)

// reply is what a synthetic model answered on one turn.
type reply struct {
	id, model   string
	text        string // assistant text; empty for a tool-only turn
	reasoning   string // visible reasoning text; may be empty when reasoned
	tool        *toolCall
	in, cached  int64 // prompt tokens, of which cached
	cacheWrite  int64 // Anthropic cache_creation_input_tokens
	out         int64 // completion tokens, including reasoning
	reasoningTk int64
	cost        *float64
	created     int64 // unix seconds
}

type toolCall struct{ id, name, args string }

func mustJSON(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return string(b)
}

// pieces splits text into streaming deltas of a few words each.
func pieces(text string, words int) []string {
	if text == "" {
		return nil
	}
	fields := strings.SplitAfter(text, " ")
	var out []string
	for len(fields) > 0 {
		n := min(words, len(fields))
		out = append(out, strings.Join(fields[:n], ""))
		fields = fields[n:]
	}
	return out
}

// frames spreads SSE frames between the first-event and total durations.
type frames struct {
	events      []model.Event
	first, last int64
	n           int
}

func (f *frames) add(event string, v any) {
	data := "data: " + mustJSON(v) + "\n\n"
	if event != "" {
		data = "event: " + event + "\n" + data
	}
	f.events = append(f.events, model.Event{Data: data})
}

func (f *frames) done() []model.Event {
	for i := range f.events {
		offset := f.first
		if len(f.events) > 1 {
			offset += (f.last - f.first) * int64(i) / int64(len(f.events)-1)
		}
		f.events[i].OffsetMS = offset
	}
	return f.events
}

func chatUsage(r reply) map[string]any {
	u := map[string]any{
		"prompt_tokens": r.in, "prompt_tokens_details": map[string]any{"cached_tokens": r.cached},
		"completion_tokens": r.out, "completion_tokens_details": map[string]any{"reasoning_tokens": r.reasoningTk},
		"total_tokens": r.in + r.out,
	}
	if r.cost != nil {
		u["cost"] = *r.cost
	}
	return u
}

func chatBody(r reply) string {
	msg := map[string]any{"role": "assistant", "content": r.text}
	finish := "stop"
	if r.reasoning != "" {
		msg["reasoning"] = r.reasoning
	}
	if r.tool != nil {
		finish = "tool_calls"
		if r.text == "" {
			msg["content"] = nil
		}
		msg["tool_calls"] = []any{map[string]any{"id": r.tool.id, "type": "function", "function": map[string]any{"name": r.tool.name, "arguments": r.tool.args}}}
	}
	return mustJSON(map[string]any{"id": r.id, "object": "chat.completion", "created": r.created, "model": r.model,
		"choices": []any{map[string]any{"index": 0, "message": msg, "finish_reason": finish}}, "usage": chatUsage(r)})
}

func chatStream(r reply, first, last int64) []model.Event {
	f := &frames{first: first, last: last}
	chunk := func(delta map[string]any, finish any) map[string]any {
		return map[string]any{"id": r.id, "object": "chat.completion.chunk", "created": r.created, "model": r.model,
			"choices": []any{map[string]any{"index": 0, "delta": delta, "finish_reason": finish}}}
	}
	f.add("", chunk(map[string]any{"role": "assistant", "content": ""}, nil))
	for _, p := range pieces(r.reasoning, 6) {
		f.add("", chunk(map[string]any{"reasoning": p}, nil))
	}
	for _, p := range pieces(r.text, 4) {
		f.add("", chunk(map[string]any{"content": p}, nil))
	}
	finish := "stop"
	if r.tool != nil {
		finish = "tool_calls"
		f.add("", chunk(map[string]any{"tool_calls": []any{map[string]any{"index": 0, "id": r.tool.id, "type": "function", "function": map[string]any{"name": r.tool.name, "arguments": ""}}}}, nil))
		f.add("", chunk(map[string]any{"tool_calls": []any{map[string]any{"index": 0, "function": map[string]any{"arguments": r.tool.args}}}}, nil))
	}
	f.add("", chunk(map[string]any{}, finish))
	f.add("", map[string]any{"id": r.id, "object": "chat.completion.chunk", "created": r.created, "model": r.model, "choices": []any{}, "usage": chatUsage(r)})
	f.events = append(f.events, model.Event{Data: "data: [DONE]\n\n"})
	return f.done()
}

func responsesOutput(r reply) []any {
	var out []any
	if r.reasoningTk > 0 {
		summary := []any{}
		if r.reasoning != "" {
			summary = append(summary, map[string]any{"type": "summary_text", "text": r.reasoning})
		}
		out = append(out, map[string]any{"id": "rs_" + r.id, "type": "reasoning", "summary": summary})
	}
	if r.text != "" {
		out = append(out, map[string]any{"id": "msg_" + r.id, "type": "message", "status": "completed", "role": "assistant",
			"content": []any{map[string]any{"type": "output_text", "text": r.text, "annotations": []any{}}}})
	}
	if r.tool != nil {
		out = append(out, map[string]any{"id": "fc_" + r.id, "type": "function_call", "status": "completed", "call_id": r.tool.id, "name": r.tool.name, "arguments": r.tool.args})
	}
	return out
}

func responsesObject(r reply, status string, output []any, usage bool) map[string]any {
	o := map[string]any{"id": "resp_" + r.id, "object": "response", "created_at": r.created, "status": status, "model": r.model, "output": output}
	if usage {
		u := map[string]any{"input_tokens": r.in, "input_tokens_details": map[string]any{"cached_tokens": r.cached},
			"output_tokens": r.out, "output_tokens_details": map[string]any{"reasoning_tokens": r.reasoningTk}, "total_tokens": r.in + r.out}
		if r.cost != nil {
			u["cost"] = *r.cost
		}
		o["usage"] = u
	}
	return o
}

func responsesBody(r reply) string {
	return mustJSON(responsesObject(r, "completed", responsesOutput(r), true))
}

func responsesStream(r reply, first, last int64) []model.Event {
	f := &frames{first: first, last: last}
	ev := func(typ string, fields map[string]any) {
		fields["type"] = typ
		f.add(typ, fields)
	}
	ev("response.created", map[string]any{"response": responsesObject(r, "in_progress", []any{}, false)})
	for i, raw := range responsesOutput(r) {
		item := raw.(map[string]any)
		id := item["id"]
		switch item["type"] {
		case "reasoning":
			ev("response.output_item.added", map[string]any{"output_index": i, "item": map[string]any{"id": id, "type": "reasoning", "summary": []any{}}})
			if r.reasoning != "" {
				ev("response.reasoning_summary_part.added", map[string]any{"item_id": id, "output_index": i, "summary_index": 0, "part": map[string]any{"type": "summary_text", "text": ""}})
				for _, p := range pieces(r.reasoning, 6) {
					ev("response.reasoning_summary_text.delta", map[string]any{"item_id": id, "output_index": i, "summary_index": 0, "delta": p})
				}
				ev("response.reasoning_summary_text.done", map[string]any{"item_id": id, "output_index": i, "summary_index": 0, "text": r.reasoning})
				ev("response.reasoning_summary_part.done", map[string]any{"item_id": id, "output_index": i, "summary_index": 0, "part": map[string]any{"type": "summary_text", "text": r.reasoning}})
			}
		case "message":
			ev("response.output_item.added", map[string]any{"output_index": i, "item": map[string]any{"id": id, "type": "message", "status": "in_progress", "role": "assistant", "content": []any{}}})
			ev("response.content_part.added", map[string]any{"item_id": id, "output_index": i, "content_index": 0, "part": map[string]any{"type": "output_text", "text": "", "annotations": []any{}}})
			for _, p := range pieces(r.text, 4) {
				ev("response.output_text.delta", map[string]any{"item_id": id, "output_index": i, "content_index": 0, "delta": p})
			}
			ev("response.output_text.done", map[string]any{"item_id": id, "output_index": i, "content_index": 0, "text": r.text})
			ev("response.content_part.done", map[string]any{"item_id": id, "output_index": i, "content_index": 0, "part": map[string]any{"type": "output_text", "text": r.text, "annotations": []any{}}})
		case "function_call":
			ev("response.output_item.added", map[string]any{"output_index": i, "item": map[string]any{"id": id, "type": "function_call", "status": "in_progress", "call_id": r.tool.id, "name": r.tool.name, "arguments": ""}})
			ev("response.function_call_arguments.delta", map[string]any{"item_id": id, "output_index": i, "delta": r.tool.args})
			ev("response.function_call_arguments.done", map[string]any{"item_id": id, "output_index": i, "arguments": r.tool.args})
		}
		ev("response.output_item.done", map[string]any{"output_index": i, "item": item})
	}
	ev("response.completed", map[string]any{"response": responsesObject(r, "completed", responsesOutput(r), true)})
	return f.done()
}

func messagesUsage(r reply) map[string]any {
	u := map[string]any{"input_tokens": r.in - r.cached - r.cacheWrite, "cache_creation_input_tokens": r.cacheWrite, "cache_read_input_tokens": r.cached, "output_tokens": r.out}
	if r.cost != nil {
		u["cost"] = *r.cost
	}
	return u
}

func messagesContent(r reply) []any {
	var out []any
	if r.reasoningTk > 0 {
		out = append(out, map[string]any{"type": "thinking", "thinking": r.reasoning, "signature": "sig_" + r.id})
	}
	if r.text != "" {
		out = append(out, map[string]any{"type": "text", "text": r.text})
	}
	if r.tool != nil {
		var input map[string]any
		_ = json.Unmarshal([]byte(r.tool.args), &input)
		out = append(out, map[string]any{"type": "tool_use", "id": r.tool.id, "name": r.tool.name, "input": input})
	}
	return out
}

func stopReason(r reply) string {
	if r.tool != nil {
		return "tool_use"
	}
	return "end_turn"
}

func messagesBody(r reply) string {
	return mustJSON(map[string]any{"id": "msg_" + r.id, "type": "message", "role": "assistant", "model": r.model,
		"content": messagesContent(r), "stop_reason": stopReason(r), "stop_sequence": nil, "usage": messagesUsage(r)})
}

func messagesStream(r reply, first, last int64) []model.Event {
	f := &frames{first: first, last: last}
	ev := func(typ string, fields map[string]any) {
		fields["type"] = typ
		f.add(typ, fields)
	}
	start := messagesUsage(r)
	start["output_tokens"] = 1
	delete(start, "cost")
	ev("message_start", map[string]any{"message": map[string]any{"id": "msg_" + r.id, "type": "message", "role": "assistant", "model": r.model,
		"content": []any{}, "stop_reason": nil, "stop_sequence": nil, "usage": start}})
	ev("ping", map[string]any{})
	for i, raw := range messagesContent(r) {
		block := raw.(map[string]any)
		switch block["type"] {
		case "thinking":
			ev("content_block_start", map[string]any{"index": i, "content_block": map[string]any{"type": "thinking", "thinking": ""}})
			for _, p := range pieces(r.reasoning, 6) {
				ev("content_block_delta", map[string]any{"index": i, "delta": map[string]any{"type": "thinking_delta", "thinking": p}})
			}
			ev("content_block_delta", map[string]any{"index": i, "delta": map[string]any{"type": "signature_delta", "signature": block["signature"]}})
		case "text":
			ev("content_block_start", map[string]any{"index": i, "content_block": map[string]any{"type": "text", "text": ""}})
			for _, p := range pieces(r.text, 4) {
				ev("content_block_delta", map[string]any{"index": i, "delta": map[string]any{"type": "text_delta", "text": p}})
			}
		case "tool_use":
			ev("content_block_start", map[string]any{"index": i, "content_block": map[string]any{"type": "tool_use", "id": r.tool.id, "name": r.tool.name, "input": map[string]any{}}})
			ev("content_block_delta", map[string]any{"index": i, "delta": map[string]any{"type": "input_json_delta", "partial_json": r.tool.args}})
		}
		ev("content_block_stop", map[string]any{"index": i})
	}
	final := map[string]any{"output_tokens": r.out}
	if r.cost != nil {
		final["cost"] = *r.cost
	}
	ev("message_delta", map[string]any{"delta": map[string]any{"stop_reason": stopReason(r), "stop_sequence": nil}, "usage": final})
	ev("message_stop", map[string]any{})
	return f.done()
}

// revision builds the stored response for a reply.
func revision(route string, streaming bool, r reply, first, last int64) model.Revision {
	rev := model.Revision{Status: 200, Source: "recorded", Headers: map[string]string{"Content-Type": "application/json", "X-Request-Id": "req_" + r.id}}
	if streaming {
		rev.Headers["Content-Type"] = "text/event-stream"
	}
	switch {
	case route == chatRoute && streaming:
		rev.Events = chatStream(r, first, last)
	case route == chatRoute:
		rev.Body = chatBody(r)
	case route == responsesRoute && streaming:
		rev.Events = responsesStream(r, first, last)
	case route == responsesRoute:
		rev.Body = responsesBody(r)
	case route == messagesRoute && streaming:
		rev.Events = messagesStream(r, first, last)
	case route == messagesRoute:
		rev.Body = messagesBody(r)
	default:
		panic(fmt.Sprintf("unknown route %q", route))
	}
	return rev
}
