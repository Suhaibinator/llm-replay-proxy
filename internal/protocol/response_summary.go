package protocol

import (
	"encoding/json"
	"fmt"
	"math"
	"strings"
	"unicode/utf8"

	"github.com/local/llm-replay-proxy/internal/model"
)

// SummarizeResponse describes a stored response for lists and analytics: the
// model, outcome, token usage and cost as the provider reported them, and
// how much text, reasoning and how many tool calls it produced. It reads JSON
// bodies and SSE frames of all three protocols, tolerates anything it does not
// recognise (those fields stay empty or nil) and never fails.
func SummarizeResponse(route string, streaming bool, rev model.Revision) model.ResponseSummary {
	var s model.ResponseSummary
	if !streaming {
		o, err := bodyObject(rev.Body)
		if err != nil {
			return s
		}
		switch route {
		case ChatRoute:
			summarizeChatBody(&s, o)
		case ResponsesRoute:
			summarizeResponseObject(&s, o)
		case MessagesRoute:
			summarizeMessageBody(&s, o)
		}
		return s
	}
	objects := streamObjects(rev)
	switch route {
	case ChatRoute:
		summarizeChatStream(&s, objects)
	case ResponsesRoute:
		summarizeResponsesStream(&s, objects)
	case MessagesRoute:
		summarizeMessagesStream(&s, objects)
	}
	return s
}

// streamObjects decodes every JSON data frame it can, skipping the rest.
func streamObjects(rev model.Revision) []map[string]any {
	out := make([]map[string]any, 0, len(rev.Events))
	for i, e := range rev.Events {
		f, err := parseEventFrame(i, e.Data)
		if err != nil || f.data == "" || f.data == "[DONE]" {
			continue
		}
		if o, err := decodeObject(f.data); err == nil {
			out = append(out, o)
		}
	}
	return out
}

func chars(v any) int {
	s, _ := asString(v)
	return utf8.RuneCountInString(s)
}

// count reads a JSON number as a token count; anything else is unreported.
func count(v any) *int64 {
	n, ok := v.(json.Number)
	if !ok {
		return nil
	}
	if i, err := n.Int64(); err == nil {
		return &i
	}
	f, err := n.Float64()
	if err != nil || math.IsNaN(f) || math.IsInf(f, 0) {
		return nil
	}
	i := int64(f)
	return &i
}

func cost(v any) *float64 {
	n, ok := v.(json.Number)
	if !ok {
		return nil
	}
	f, err := n.Float64()
	if err != nil || math.IsNaN(f) || math.IsInf(f, 0) {
		return nil
	}
	return &f
}

func sub(o map[string]any, keys ...string) any {
	var v any = o
	for _, k := range keys {
		m, ok := asMap(v)
		if !ok {
			return nil
		}
		v = m[k]
	}
	return v
}

// openAIUsage reads Chat (prompt/completion) or Responses (input/output)
// usage. A usage object that is absent leaves s untouched.
func openAIUsage(s *model.ResponseSummary, usage any, chat bool) {
	u, ok := asMap(usage)
	if !ok {
		return
	}
	in, out, inDetails, outDetails := "input_tokens", "output_tokens", "input_tokens_details", "output_tokens_details"
	if chat {
		in, out, inDetails, outDetails = "prompt_tokens", "completion_tokens", "prompt_tokens_details", "completion_tokens_details"
	}
	s.Usage = model.TokenUsage{
		Input:       count(u[in]),
		CachedInput: count(sub(u, inDetails, "cached_tokens")),
		Output:      count(u[out]),
		Reasoning:   count(sub(u, outDetails, "reasoning_tokens")),
		Total:       count(u["total_tokens"]),
	}
	s.Cost = cost(u["cost"])
}

// mergeUsage copies the reported fields of usage into merged, so a stream's
// final message_delta overrides what message_start reported, field by field.
func mergeUsage(merged map[string]any, usage any) {
	u, _ := asMap(usage)
	for k, v := range u {
		if v != nil {
			merged[k] = v
		}
	}
}

// anthropicUsage reads a (merged) Messages usage object. Input counts every
// prompt token (uncached, cache writes and cache reads) so that cached input
// is part of input, as it is for the OpenAI protocols. Messages reports no
// reasoning or total count.
func anthropicUsage(s *model.ResponseSummary, u map[string]any) {
	base, created, read := count(u["input_tokens"]), count(u["cache_creation_input_tokens"]), count(u["cache_read_input_tokens"])
	if base != nil || created != nil || read != nil {
		var total int64
		for _, v := range []*int64{base, created, read} {
			if v != nil {
				total += *v
			}
		}
		s.Usage.Input = &total
	}
	s.Usage.CachedInput = read
	s.Usage.Output = count(u["output_tokens"])
	s.Cost = cost(u["cost"])
}

// chatContentChars counts text in a Chat message content: a string or an
// array of text parts.
func chatContentChars(v any) int {
	if parts, ok := asSlice(v); ok {
		n := 0
		for _, p := range parts {
			part, _ := asMap(p)
			n += chars(part["text"])
		}
		return n
	}
	return chars(v)
}

// chatReasoningChars counts reasoning text in a Chat message or delta, as
// OpenRouter (reasoning, reasoning_details) and DeepSeek-style servers
// (reasoning_content) return it.
func chatReasoningChars(m map[string]any) int {
	if n := chars(m["reasoning"]) + chars(m["reasoning_content"]); n > 0 {
		return n
	}
	n := 0
	details, _ := asSlice(m["reasoning_details"])
	for _, d := range details {
		detail, _ := asMap(d)
		n += chars(detail["text"]) + chars(detail["summary"])
	}
	return n
}

func summarizeChatBody(s *model.ResponseSummary, o map[string]any) {
	s.Model, _ = asString(o["model"])
	choices, _ := asSlice(o["choices"])
	for i, raw := range choices {
		c, _ := asMap(raw)
		if i == 0 {
			s.Outcome, _ = asString(c["finish_reason"])
		}
		m, _ := asMap(c["message"])
		s.OutputChars += chatContentChars(m["content"]) + chars(m["refusal"])
		s.ReasoningChars += chatReasoningChars(m)
		calls, _ := asSlice(m["tool_calls"])
		s.ToolCalls += len(calls)
		if m["function_call"] != nil {
			s.ToolCalls++
		}
	}
	openAIUsage(s, o["usage"], true)
}

func summarizeChatStream(s *model.ResponseSummary, objects []map[string]any) {
	calls := map[string]bool{}
	firstChoice := ""
	for _, o := range objects {
		if s.Model == "" {
			s.Model, _ = asString(o["model"])
		}
		if o["usage"] != nil {
			openAIUsage(s, o["usage"], true)
		}
		choices, _ := asSlice(o["choices"])
		for _, raw := range choices {
			c, _ := asMap(raw)
			index := fmt.Sprint(c["index"])
			if firstChoice == "" {
				firstChoice = index
			}
			if reason, ok := asString(c["finish_reason"]); ok && reason != "" && index == firstChoice {
				s.Outcome = reason
			}
			d, _ := asMap(c["delta"])
			s.OutputChars += chatContentChars(d["content"]) + chars(d["refusal"])
			s.ReasoningChars += chatReasoningChars(d)
			deltas, _ := asSlice(d["tool_calls"])
			for _, td := range deltas {
				call, _ := asMap(td)
				calls[index+"/"+fmt.Sprint(call["index"])] = true
			}
			if d["function_call"] != nil {
				calls[index+"/function_call"] = true
			}
		}
	}
	s.ToolCalls = len(calls)
}

// summarizeResponseObject reads a Responses response object: a JSON body or
// the response carried by a stream's terminal event.
func summarizeResponseObject(s *model.ResponseSummary, o map[string]any) {
	s.Model, _ = asString(o["model"])
	s.Outcome, _ = asString(o["status"])
	output, _ := asSlice(o["output"])
	s.OutputChars, s.ReasoningChars, s.ToolCalls = 0, 0, 0
	for _, raw := range output {
		item, _ := asMap(raw)
		typ, _ := asString(item["type"])
		switch {
		case typ == "message":
			content, _ := asSlice(item["content"])
			for _, p := range content {
				part, _ := asMap(p)
				s.OutputChars += chars(part["text"]) + chars(part["refusal"])
			}
		case typ == "reasoning":
			for _, field := range []string{"summary", "content"} {
				parts, _ := asSlice(item[field])
				for _, p := range parts {
					part, _ := asMap(p)
					s.ReasoningChars += chars(part["text"])
				}
			}
		case strings.HasSuffix(typ, "_call"):
			s.ToolCalls++ // function_call, web_search_call, …
		}
	}
	openAIUsage(s, o["usage"], false)
}

func summarizeResponsesStream(s *model.ResponseSummary, objects []map[string]any) {
	// Without a terminal response (a recording should always have one),
	// fall back to the deltas and the last response snapshot seen.
	var snapshot map[string]any
	var output, reasoning int
	calls := map[string]bool{}
	for _, o := range objects {
		typ, _ := asString(o["type"])
		switch typ {
		case "response.completed", "response.incomplete", "response.failed":
			if r, ok := asMap(o["response"]); ok {
				summarizeResponseObject(s, r)
				return
			}
		case "response.created", "response.in_progress":
			if r, ok := asMap(o["response"]); ok {
				snapshot = r
			}
		case "response.output_text.delta", "response.refusal.delta":
			output += chars(o["delta"])
		case "response.reasoning_text.delta", "response.reasoning_summary_text.delta":
			reasoning += chars(o["delta"])
		case "response.output_item.added":
			item, _ := asMap(o["item"])
			if t, _ := asString(item["type"]); strings.HasSuffix(t, "_call") {
				calls[fmt.Sprint(o["output_index"])] = true
			}
		}
	}
	if snapshot != nil {
		summarizeResponseObject(s, snapshot)
	}
	s.OutputChars, s.ReasoningChars, s.ToolCalls = output, reasoning, len(calls)
}

func summarizeMessageBody(s *model.ResponseSummary, o map[string]any) {
	s.Model, _ = asString(o["model"])
	s.Outcome, _ = asString(o["stop_reason"])
	content, _ := asSlice(o["content"])
	for _, raw := range content {
		block, _ := asMap(raw)
		summarizeMessageBlock(s, block)
	}
	usage, _ := asMap(o["usage"])
	anthropicUsage(s, usage)
}

func summarizeMessageBlock(s *model.ResponseSummary, block map[string]any) {
	typ, _ := asString(block["type"])
	switch {
	case typ == "text":
		s.OutputChars += chars(block["text"])
	case typ == "thinking":
		s.ReasoningChars += chars(block["thinking"])
	case anthropicToolKind(typ):
		s.ToolCalls++
	}
}

func summarizeMessagesStream(s *model.ResponseSummary, objects []map[string]any) {
	usage := map[string]any{}
	defer anthropicUsage(s, usage)
	for _, o := range objects {
		typ, _ := asString(o["type"])
		switch typ {
		case "message_start":
			m, _ := asMap(o["message"])
			s.Model, _ = asString(m["model"])
			mergeUsage(usage, m["usage"])
		case "content_block_start":
			block, _ := asMap(o["content_block"])
			summarizeMessageBlock(s, block)
		case "content_block_delta":
			d, _ := asMap(o["delta"])
			switch d["type"] {
			case "text_delta":
				s.OutputChars += chars(d["text"])
			case "thinking_delta":
				s.ReasoningChars += chars(d["thinking"])
			}
		case "message_delta":
			d, _ := asMap(o["delta"])
			if reason, ok := asString(d["stop_reason"]); ok && reason != "" {
				s.Outcome = reason
			}
			mergeUsage(usage, o["usage"])
		}
	}
}
