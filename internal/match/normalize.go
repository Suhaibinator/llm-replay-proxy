package match

import (
	"encoding/json/jsontext"
	"sort"
)

// Normalize rewrites a decoded request so that differences that do not change
// what the model is asked do not change its key:
//
//   - the tools array is ordered by each tool's canonical JSON, so callers
//     that register the same tools in a different order match;
//   - JSON embedded in strings (tool-call arguments and tool results) is
//     re-encoded canonically, so object key order and whitespace inside it
//     do not matter. A string that is not a JSON object or array is unchanged,
//     and the value stays a string.
//
// value is modified in place and returned. An unsupported route leaves it
// unchanged. Exclusions apply to the request as sent, before Normalize.
func Normalize(route string, value any) any {
	api, err := API(route)
	if err != nil {
		return value
	}
	return normalize(api, value)
}

func normalize(api string, value any) any {
	request, ok := value.(map[string]any)
	if !ok {
		return value
	}
	if tools, ok := request["tools"].([]any); ok {
		sortTools(tools)
	}
	switch api {
	case "responses":
		for _, item := range objects(request["input"]) {
			switch item["type"] {
			case "function_call":
				canonicalField(item, "arguments")
			case "mcp_call":
				canonicalField(item, "arguments")
				canonicalField(item, "output")
			case "function_call_output", "custom_tool_call_output":
				canonicalContent(item, "output")
			}
		}
	case "chat_completions":
		for _, message := range objects(request["messages"]) {
			for _, call := range objects(message["tool_calls"]) {
				if function, ok := call["function"].(map[string]any); ok {
					canonicalField(function, "arguments")
				}
			}
			if function, ok := message["function_call"].(map[string]any); ok {
				canonicalField(function, "arguments")
			}
			if message["role"] == "tool" || message["role"] == "function" {
				canonicalContent(message, "content")
			}
		}
	case "messages":
		for _, message := range objects(request["messages"]) {
			for _, block := range objects(message["content"]) {
				if block["type"] == "tool_result" {
					canonicalContent(block, "content")
				}
			}
		}
	}
	return request
}

// objects returns the object elements of an array value.
func objects(value any) []map[string]any {
	array, _ := value.([]any)
	out := make([]map[string]any, 0, len(array))
	for _, v := range array {
		if o, ok := v.(map[string]any); ok {
			out = append(out, o)
		}
	}
	return out
}

// sortTools orders tools by their canonical encoding: a total order over any
// tool shape (functions, built-in tools, custom tools).
func sortTools(tools []any) {
	encoded := make([]string, len(tools))
	for i, tool := range tools {
		b, _ := encode(tool)
		encoded[i] = string(b)
	}
	sort.Sort(byEncoding{tools, encoded})
}

type byEncoding struct {
	values  []any
	encoded []string
}

func (b byEncoding) Len() int           { return len(b.values) }
func (b byEncoding) Less(i, j int) bool { return b.encoded[i] < b.encoded[j] }
func (b byEncoding) Swap(i, j int) {
	b.values[i], b.values[j] = b.values[j], b.values[i]
	b.encoded[i], b.encoded[j] = b.encoded[j], b.encoded[i]
}

// canonicalContent canonicalizes a tool result: a string, or an array of
// content blocks whose text may hold JSON.
func canonicalContent(o map[string]any, field string) {
	if _, ok := o[field].(string); ok {
		canonicalField(o, field)
		return
	}
	for _, block := range objects(o[field]) {
		canonicalField(block, "text")
	}
}

func canonicalField(o map[string]any, field string) {
	if s, ok := o[field].(string); ok {
		o[field] = canonicalText(s)
	}
}

// canonicalText re-encodes s canonically when it is a JSON object or array,
// keeping exact numbers; any other string, including invalid JSON, is
// returned unchanged.
func canonicalText(s string) string {
	value := jsontext.Value(s)
	if kind := value.Kind(); kind != '{' && kind != '[' {
		return s
	}
	if err := value.Canonicalize(canonicalOptions...); err != nil {
		return s
	}
	return string(value)
}
