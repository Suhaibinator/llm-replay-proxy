package protocol

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"unicode/utf8"

	"github.com/local/llm-replay-proxy/internal/model"
)

const previewRunes = 160

// SummarizeRequest describes a request body for lists and search. Fields it
// cannot recognise stay empty; it never fails.
func SummarizeRequest(route string, body []byte) model.RequestSummary {
	s := model.RequestSummary{Bytes: int64(len(body))}
	var req map[string]any
	if json.Unmarshal(body, &req) != nil {
		return s
	}
	s.Model, _ = req["model"].(string)
	var items []any
	opening := []any{route}
	switch route {
	case ResponsesRoute:
		opening = append(opening, req["instructions"])
		switch in := req["input"].(type) {
		case string:
			items = []any{map[string]any{"role": "user", "content": in}}
		case []any:
			items = in
		}
	case ChatRoute:
		items, _ = req["messages"].([]any)
	case MessagesRoute:
		opening = append(opening, req["system"])
		items, _ = req["messages"].([]any)
	}
	s.Items = len(items)
	firstUser := -1
	for i, raw := range items {
		item, _ := raw.(map[string]any)
		s.ToolCalls += toolCalls(item)
		s.Images += images(item)
		if text := userText(item); text != "" {
			s.Preview = text
			if firstUser < 0 {
				firstUser = i
			}
		}
	}
	s.Preview = preview(s.Preview)
	if firstUser >= 0 {
		// Every later turn re-sends this opening unchanged, so its hash
		// groups a conversation's requests.
		b, _ := json.Marshal(append(opening, items[:firstUser+1]...))
		sum := sha256.Sum256(b)
		s.Thread = hex.EncodeToString(sum[:8])
	}
	return s
}

func toolCalls(item map[string]any) int {
	typ, _ := item["type"].(string)
	if strings.HasSuffix(typ, "_call") {
		return 1 // Responses function_call, web_search_call, …
	}
	n := 0
	if calls, ok := item["tool_calls"].([]any); ok {
		n += len(calls) // Chat
	}
	if blocks, ok := item["content"].([]any); ok {
		for _, b := range blocks {
			if block, _ := b.(map[string]any); block != nil {
				if t, _ := block["type"].(string); t == "tool_use" || t == "server_tool_use" {
					n++ // Messages
				}
			}
		}
	}
	return n
}

// images counts image parts anywhere in v, including inside tool results.
func images(v any) int {
	switch x := v.(type) {
	case map[string]any:
		n := 0
		switch x["type"] {
		case "input_image", "image_url", "image":
			n = 1
		}
		for _, child := range x {
			n += images(child)
		}
		return n
	case []any:
		n := 0
		for _, child := range x {
			n += images(child)
		}
		return n
	}
	return 0
}

// userText returns the text a user wrote in item, or "" for other roles and
// for user turns that only carry tool results.
func userText(item map[string]any) string {
	if role, _ := item["role"].(string); role != "user" {
		return ""
	}
	switch content := item["content"].(type) {
	case string:
		return content
	case []any:
		var parts []string
		for _, p := range content {
			part, _ := p.(map[string]any)
			if t, _ := part["type"].(string); t == "text" || t == "input_text" {
				if text, _ := part["text"].(string); text != "" {
					parts = append(parts, text)
				}
			}
		}
		return strings.Join(parts, " ")
	}
	return ""
}

func preview(text string) string {
	text = strings.Join(strings.Fields(text), " ")
	if utf8.RuneCountInString(text) <= previewRunes {
		return text
	}
	runes := []rune(text)
	return strings.TrimSpace(string(runes[:previewRunes-1])) + "…"
}
