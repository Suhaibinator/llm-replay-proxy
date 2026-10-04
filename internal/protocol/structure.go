package protocol

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/local/llm-replay-proxy/internal/model"
)

// validateStructure checks relationships between events, independently of text
// editing eligibility. Optional lifecycle snapshots may be omitted by compatible
// providers, but snapshots that are present must agree with the emitted deltas.
func validateStructure(route string, streaming bool, rev model.Revision) error {
	if !streaming {
		return bodyStructure(route, rev.Body)
	}
	_, objects, err := eventObjects(rev)
	if err != nil {
		return err
	}
	switch route {
	case ChatRoute:
		return chatStructure(objects)
	case ResponsesRoute:
		return responsesStructure(objects)
	case MessagesRoute:
		return messagesStructure(objects)
	}
	return nil
}

func indexField(o map[string]any, field string) (int, error) {
	n, ok := o[field].(json.Number)
	if !ok {
		return 0, fmt.Errorf("%s must be a nonnegative integer", field)
	}
	i, err := strconv.ParseInt(string(n), 10, 32)
	if err != nil || i < 0 {
		return 0, fmt.Errorf("%s must be a nonnegative integer", field)
	}
	return int(i), nil
}
func requiredString(o map[string]any, field string) (string, error) {
	s, ok := o[field].(string)
	if !ok || s == "" {
		return "", fmt.Errorf("%s must be a nonempty string", field)
	}
	return s, nil
}
func consistentID(previous *string, o map[string]any, field string) error {
	raw, exists := o[field]
	if !exists {
		return nil
	}
	id, ok := raw.(string)
	if !ok || id == "" {
		return fmt.Errorf("%s must be a nonempty string", field)
	}
	if *previous != "" && *previous != id {
		return fmt.Errorf("%s changed from %q to %q", field, *previous, id)
	}
	*previous = id
	return nil
}
func chatStructure(objects []map[string]any) error {
	var id string
	finished := map[int]bool{}
	for _, o := range objects {
		if o == nil {
			continue
		}
		if err := consistentID(&id, o, "id"); err != nil {
			return err
		}
		choices, _ := asSlice(o["choices"])
		for _, raw := range choices {
			c, ok := asMap(raw)
			if !ok {
				return fmt.Errorf("chat choice must be an object")
			}
			idx, err := indexField(c, "index")
			if err != nil {
				return err
			}
			if finished[idx] {
				return fmt.Errorf("chat choice %d has data after its finish reason", idx)
			}
			if _, ok := asMap(c["delta"]); !ok {
				return fmt.Errorf("chat choice %d delta must be an object", idx)
			}
			if finish := c["finish_reason"]; finish != nil {
				if _, err := requiredString(c, "finish_reason"); err != nil {
					return err
				}
				finished[idx] = true
			}
		}
	}
	return nil
}

type responsePart struct {
	text     strings.Builder
	sawDelta bool
	done     bool
	added    bool
	closed   bool
}
type responseItem struct {
	id            string
	kind          string
	callID        string
	functionName  string
	added         bool
	closed        bool
	parts         map[int]*responsePart
	snapshot      map[string]any
	arguments     strings.Builder
	hasArguments  bool
	argumentsDone bool
}

func responsesStructure(objects []map[string]any) error {
	items := map[int]*responseItem{}
	callIDs := map[string]int{}
	var responseID string
	completed := false
	itemAt := func(index int) *responseItem {
		it := items[index]
		if it == nil {
			it = &responseItem{parts: map[int]*responsePart{}}
			items[index] = it
		}
		return it
	}
	registerCall := func(index int, it *responseItem) error {
		if it.kind != "function_call" || it.callID == "" {
			return nil
		}
		if prior, ok := callIDs[it.callID]; ok && prior != index {
			return fmt.Errorf("function call outputs %d and %d share call_id %q", prior, index, it.callID)
		}
		callIDs[it.callID] = index
		return nil
	}
	for eventIndex, o := range objects {
		if o == nil {
			continue
		}
		typ, err := requiredString(o, "type")
		if err != nil {
			return fmt.Errorf("event %d: %w", eventIndex, err)
		}
		if completed {
			return fmt.Errorf("event follows response.completed")
		}
		switch typ {
		case "response.created", "response.in_progress", "response.completed":
			response, ok := asMap(o["response"])
			if !ok {
				return fmt.Errorf("%s requires a response object", typ)
			}
			if err := consistentID(&responseID, response, "id"); err != nil {
				return err
			}
			if typ != "response.completed" {
				continue
			}
			if response["error"] != nil {
				return fmt.Errorf("completed response contains an error")
			}
			completed = true
			output, ok := asSlice(response["output"])
			if !ok {
				return fmt.Errorf("completed response output must be an array")
			}
			for i, raw := range output {
				final, ok := asMap(raw)
				if !ok {
					return fmt.Errorf("output item %d must be an object", i)
				}
				it := itemAt(i)
				if err := checkResponseItem(it, final, true); err != nil {
					return fmt.Errorf("output item %d: %w", i, err)
				}
				if err := registerCall(i, it); err != nil {
					return err
				}
				if it.added && !it.closed {
					return fmt.Errorf("output item %d never received output_item.done", i)
				}
				for partIndex, p := range it.parts {
					if p.added && !p.closed {
						return fmt.Errorf("content part %d/%d never received content_part.done", i, partIndex)
					}
				}
			}
			for i := range items {
				if i >= len(output) {
					return fmt.Errorf("stream refers to output item %d absent from final response", i)
				}
			}
			if raw, exists := response["output_text"]; exists {
				text, ok := raw.(string)
				if !ok {
					return fmt.Errorf("output_text must be a string")
				}
				var combined strings.Builder
				for _, raw := range output {
					item, _ := asMap(raw)
					content, _ := asSlice(item["content"])
					for _, raw := range content {
						part, _ := asMap(raw)
						if part["type"] == "output_text" {
							s, _ := part["text"].(string)
							combined.WriteString(s)
						}
					}
				}
				if text != combined.String() {
					return fmt.Errorf("output_text disagrees with final output")
				}
			}
		case "response.output_item.added", "response.output_item.done":
			idx, err := indexField(o, "output_index")
			if err != nil {
				return err
			}
			item, ok := asMap(o["item"])
			if !ok {
				return fmt.Errorf("%s requires an item object", typ)
			}
			it := itemAt(idx)
			if it.closed {
				return fmt.Errorf("output item %d received events after output_item.done", idx)
			}
			if typ == "response.output_item.added" {
				if it.added {
					return fmt.Errorf("duplicate output_item.added for %d", idx)
				}
				it.added = true
				if err := consistentID(&it.id, item, "id"); err != nil {
					return err
				}
				it.kind, _ = item["type"].(string)
				if it.kind == "function_call" {
					var err error
					it.callID, err = requiredString(item, "call_id")
					if err != nil {
						return err
					}
					it.functionName, err = requiredString(item, "name")
					if err != nil {
						return err
					}
				}
				if err := registerCall(idx, it); err != nil {
					return err
				}
			} else {
				if err := checkResponseItem(it, item, false); err != nil {
					return err
				}
				it.closed = true
				it.snapshot = item
				if err := registerCall(idx, it); err != nil {
					return err
				}
			}
		case "response.function_call_arguments.delta", "response.function_call_arguments.done":
			idx, err := indexField(o, "output_index")
			if err != nil {
				return err
			}
			it := itemAt(idx)
			if it.closed || it.argumentsDone {
				return fmt.Errorf("function arguments received after completion")
			}
			if err := consistentID(&it.id, o, "item_id"); err != nil {
				return err
			}
			if typ == "response.function_call_arguments.delta" {
				delta, ok := o["delta"].(string)
				if !ok {
					return fmt.Errorf("function arguments delta must be a string")
				}
				it.hasArguments = true
				it.arguments.WriteString(delta)
			} else {
				args, ok := o["arguments"].(string)
				if !ok {
					return fmt.Errorf("function arguments must be a string")
				}
				if err := validArguments(args); err != nil {
					return err
				}
				if it.hasArguments && args != it.arguments.String() {
					return fmt.Errorf("function arguments done disagrees with deltas")
				}
				if !it.hasArguments {
					it.arguments.WriteString(args)
					it.hasArguments = true
				}
				it.argumentsDone = true
			}
		case "response.content_part.added", "response.content_part.done", "response.output_text.delta", "response.output_text.done":
			idx, err := indexField(o, "output_index")
			if err != nil {
				return err
			}
			partIndex, err := indexField(o, "content_index")
			if err != nil {
				return err
			}
			it := itemAt(idx)
			if it.closed {
				return fmt.Errorf("content event follows output_item.done")
			}
			if err := consistentID(&it.id, o, "item_id"); err != nil {
				return err
			}
			p := it.parts[partIndex]
			if p == nil {
				p = &responsePart{}
				it.parts[partIndex] = p
			}
			if p.closed {
				return fmt.Errorf("content event follows content_part.done")
			}
			switch typ {
			case "response.content_part.added":
				if p.added || p.sawDelta || p.done {
					return fmt.Errorf("content_part.added is out of order")
				}
				p.added = true
				part, ok := asMap(o["part"])
				if !ok {
					return fmt.Errorf("content_part.added requires a part object")
				}
				if part["type"] == "output_text" {
					s, ok := part["text"].(string)
					if !ok {
						return fmt.Errorf("output_text part requires text")
					}
					p.text.WriteString(s)
				}
			case "response.output_text.delta":
				if p.done {
					return fmt.Errorf("text delta follows output_text.done")
				}
				s, ok := o["delta"].(string)
				if !ok {
					return fmt.Errorf("text delta must be a string")
				}
				p.sawDelta = true
				p.text.WriteString(s)
			case "response.output_text.done":
				if p.done {
					return fmt.Errorf("duplicate output_text.done")
				}
				s, ok := o["text"].(string)
				if !ok || s != p.text.String() {
					return fmt.Errorf("output_text.done disagrees with text deltas")
				}
				p.done = true
			case "response.content_part.done":
				part, ok := asMap(o["part"])
				if !ok {
					return fmt.Errorf("content_part.done requires a part object")
				}
				if part["type"] == "output_text" {
					s, ok := part["text"].(string)
					if !ok || s != p.text.String() {
						return fmt.Errorf("content_part.done disagrees with text deltas")
					}
				}
				p.closed = true
			}
		default:
			// Preserve tool, reasoning, audio and image event families. Their own
			// provider completion is still required; opaque deltas are not text-edited.
			if !strings.HasPrefix(typ, "response.") {
				return fmt.Errorf("unsupported Responses event type %q", typ)
			}
		}
	}
	return nil
}
func checkResponseItem(it *responseItem, item map[string]any, final bool) error {
	if raw, exists := item["status"]; exists {
		status, ok := raw.(string)
		if !ok || status != "completed" {
			return fmt.Errorf("terminal output item status must be completed")
		}
	}
	if it.id != "" {
		if _, exists := item["id"]; !exists {
			return fmt.Errorf("output item is missing previously observed id %q", it.id)
		}
	}
	if err := consistentID(&it.id, item, "id"); err != nil {
		return err
	}
	kind, err := requiredString(item, "type")
	if err != nil {
		return err
	}
	if it.kind != "" && it.kind != kind {
		return fmt.Errorf("output item type changed")
	}
	it.kind = kind
	if kind == "function_call" {
		if err := responseFunction(item); err != nil {
			return err
		}
		callID, _ := item["call_id"].(string)
		name, _ := item["name"].(string)
		if it.callID != "" && it.callID != callID {
			return fmt.Errorf("function call_id changed")
		}
		if it.functionName != "" && it.functionName != name {
			return fmt.Errorf("function name changed")
		}
		it.callID, it.functionName = callID, name
		if it.hasArguments && item["arguments"] != it.arguments.String() {
			return fmt.Errorf("function call arguments disagree with deltas")
		}
		if final && it.snapshot != nil {
			for _, field := range []string{"call_id", "name", "arguments"} {
				if item[field] != it.snapshot[field] {
					return fmt.Errorf("function call %s disagrees with output_item.done", field)
				}
			}
		}
	} else if it.hasArguments {
		return fmt.Errorf("function argument events belong to a non-function output item")
	}
	if kind != "message" {
		if len(it.parts) > 0 {
			return fmt.Errorf("text deltas belong to a non-message output item")
		}
		return nil
	}
	content, ok := asSlice(item["content"])
	if !ok {
		return fmt.Errorf("message content must be an array")
	}
	for i, raw := range content {
		part, ok := asMap(raw)
		if !ok {
			return fmt.Errorf("content part must be an object")
		}
		if part["type"] != "output_text" {
			continue
		}
		s, ok := part["text"].(string)
		if !ok {
			return fmt.Errorf("output_text requires text")
		}
		p := it.parts[i]
		if p == nil {
			if s != "" {
				return fmt.Errorf("final text has no corresponding text deltas")
			}
			continue
		}
		if s != p.text.String() {
			return fmt.Errorf("output item text disagrees with text deltas")
		}
	}
	for i, p := range it.parts {
		if (p.sawDelta || p.done) && i >= len(content) {
			return fmt.Errorf("text part %d absent from output item", i)
		}
	}
	if final && it.snapshot != nil {
		prior, _ := json.Marshal(it.snapshot["content"])
		current, _ := json.Marshal(item["content"])
		if string(prior) != string(current) {
			return fmt.Errorf("output_item.done content disagrees with final output")
		}
	}
	return nil
}

type messageBlock struct {
	kind         string
	id           string
	open         bool
	arguments    strings.Builder
	hasArguments bool
}

func messagesStructure(objects []map[string]any) error {
	started, finished, stopped := false, false, false
	blocks := map[int]*messageBlock{}
	toolIDs := map[string]int{}
	for _, o := range objects {
		if o == nil {
			continue
		}
		typ, err := requiredString(o, "type")
		if err != nil {
			return err
		}
		if typ == "ping" {
			continue
		}
		if stopped {
			return fmt.Errorf("event follows message_stop")
		}
		switch typ {
		case "message_start":
			if started {
				return fmt.Errorf("duplicate message_start")
			}
			started = true
			message, ok := asMap(o["message"])
			if !ok {
				return fmt.Errorf("message_start requires a message object")
			}
			if message["type"] != "message" {
				return fmt.Errorf("message_start message type must be message")
			}
			if _, err := requiredString(message, "id"); err != nil {
				return err
			}
			if _, ok := asSlice(message["content"]); !ok {
				return fmt.Errorf("message_start content must be an array")
			}
		case "content_block_start":
			if !started || finished {
				return fmt.Errorf("content block starts outside message content lifecycle")
			}
			idx, err := indexField(o, "index")
			if err != nil {
				return err
			}
			if blocks[idx] != nil {
				return fmt.Errorf("content block index %d is reused", idx)
			}
			block, ok := asMap(o["content_block"])
			if !ok {
				return fmt.Errorf("content_block_start requires a block object")
			}
			kind, err := requiredString(block, "type")
			if err != nil {
				return err
			}
			if err := anthropicTool(block); err != nil {
				return err
			}
			id := ""
			if kind == "tool_use" || kind == "server_tool_use" {
				id, _ = block["id"].(string)
				if prior, ok := toolIDs[id]; ok {
					return fmt.Errorf("tool blocks %d and %d share id %q", prior, idx, id)
				}
				toolIDs[id] = idx
			}
			blocks[idx] = &messageBlock{kind: kind, id: id, open: true}
		case "content_block_delta":
			idx, err := indexField(o, "index")
			if err != nil {
				return err
			}
			block := blocks[idx]
			if block == nil || !block.open {
				return fmt.Errorf("delta targets an unopened block")
			}
			d, ok := asMap(o["delta"])
			if !ok {
				return fmt.Errorf("content_block_delta requires a delta object")
			}
			kind, err := requiredString(d, "type")
			if err != nil {
				return err
			}
			field := ""
			switch kind {
			case "text_delta":
				if block.kind != "text" {
					return fmt.Errorf("text delta targets a non-text block")
				}
				field = "text"
			case "input_json_delta":
				if block.kind != "tool_use" && block.kind != "server_tool_use" {
					return fmt.Errorf("input JSON delta targets a non-tool block")
				}
				field = "partial_json"
			case "thinking_delta":
				if block.kind != "thinking" {
					return fmt.Errorf("thinking delta targets a non-thinking block")
				}
				field = "thinking"
			case "signature_delta":
				field = "signature"
			}
			if field != "" {
				if _, ok := d[field].(string); !ok {
					return fmt.Errorf("%s requires string %s", kind, field)
				}
			}
			if kind == "input_json_delta" {
				block.hasArguments = true
				block.arguments.WriteString(d["partial_json"].(string))
			}
		case "content_block_stop":
			idx, err := indexField(o, "index")
			if err != nil {
				return err
			}
			block := blocks[idx]
			if block == nil || !block.open {
				return fmt.Errorf("stop targets an unopened block")
			}
			// Parameterless tools stream only an empty partial_json; the
			// object input from content_block_start then stands.
			if block.hasArguments && block.arguments.Len() > 0 {
				if err := validArguments(block.arguments.String()); err != nil {
					return err
				}
			}
			block.open = false
		case "message_delta":
			if !started {
				return fmt.Errorf("message_delta precedes message_start")
			}
			for _, block := range blocks {
				if block.open {
					return fmt.Errorf("message_delta precedes content_block_stop")
				}
			}
			d, ok := asMap(o["delta"])
			if !ok {
				return fmt.Errorf("message_delta requires a delta object")
			}
			if d["stop_reason"] != nil {
				if _, err := requiredString(d, "stop_reason"); err != nil {
					return err
				}
				finished = true
			}
		case "message_stop":
			if !started || !finished {
				return fmt.Errorf("message_stop precedes completion")
			}
			stopped = true
		}
	}
	return nil
}

func bodyStructure(route, body string) error {
	o, err := bodyObject(body)
	if err != nil {
		return err
	}
	switch route {
	case ChatRoute:
		toolIDs := map[string]bool{}
		choices, _ := asSlice(o["choices"])
		for _, raw := range choices {
			c, _ := asMap(raw)
			if _, err := requiredString(c, "finish_reason"); err != nil {
				return err
			}
			message, _ := asMap(c["message"])
			if message["refusal"] != nil {
				if _, err := requiredString(message, "refusal"); err != nil {
					return err
				}
			}
			switch content := message["content"].(type) {
			case string:
			case nil:
				if message["tool_calls"] == nil && message["function_call"] == nil && message["refusal"] == nil {
					return fmt.Errorf("chat choice has neither content, tools nor refusal")
				}
			case []any:
				for _, part := range content {
					if _, ok := asMap(part); !ok {
						return fmt.Errorf("chat content part must be an object")
					}
				}
			default:
				return fmt.Errorf("chat content must be text, structured content or null")
			}
			if tools, exists := message["tool_calls"]; exists && tools != nil {
				calls, ok := asSlice(tools)
				if !ok {
					return fmt.Errorf("tool_calls must be an array")
				}
				for _, raw := range calls {
					call, ok := asMap(raw)
					if !ok {
						return fmt.Errorf("tool call must be an object")
					}
					id, err := requiredString(call, "id")
					if err != nil {
						return err
					}
					if toolIDs[id] {
						return fmt.Errorf("duplicate tool call id %q", id)
					}
					toolIDs[id] = true
					kind, err := requiredString(call, "type")
					if err != nil {
						return err
					}
					if kind == "function" {
						f, ok := asMap(call["function"])
						if !ok {
							return fmt.Errorf("function tool requires a function object")
						}
						if err := functionArguments(f); err != nil {
							return err
						}
					}
				}
			}
			if legacy, exists := message["function_call"]; exists && legacy != nil {
				f, ok := asMap(legacy)
				if !ok {
					return fmt.Errorf("function_call must be an object")
				}
				if err := functionArguments(f); err != nil {
					return err
				}
			}
		}
	case MessagesRoute:
		if _, err := requiredString(o, "stop_reason"); err != nil {
			return err
		}
		content, _ := asSlice(o["content"])
		toolIDs := map[string]bool{}
		for _, raw := range content {
			part, ok := asMap(raw)
			if !ok {
				return fmt.Errorf("message content must contain objects")
			}
			if _, err := requiredString(part, "type"); err != nil {
				return err
			}
			if err := anthropicTool(part); err != nil {
				return err
			}
			if part["type"] == "tool_use" || part["type"] == "server_tool_use" {
				id, _ := part["id"].(string)
				if toolIDs[id] {
					return fmt.Errorf("duplicate tool use id %q", id)
				}
				toolIDs[id] = true
			}
		}
	case ResponsesRoute:
		output, _ := asSlice(o["output"])
		callIDs := map[string]bool{}
		for _, raw := range output {
			item, ok := asMap(raw)
			if !ok {
				return fmt.Errorf("response output must contain objects")
			}
			if _, err := requiredString(item, "type"); err != nil {
				return err
			}
			if item["type"] == "function_call" {
				if err := responseFunction(item); err != nil {
					return err
				}
				id, _ := item["call_id"].(string)
				if callIDs[id] {
					return fmt.Errorf("duplicate function call_id %q", id)
				}
				callIDs[id] = true
			}
			if item["type"] == "message" {
				content, _ := asSlice(item["content"])
				for _, raw := range content {
					part, _ := asMap(raw)
					if _, err := requiredString(part, "type"); err != nil {
						return err
					}
				}
			}
			if item["type"] == "message" || item["type"] == "function_call" {
				if raw, exists := item["status"]; exists {
					status, ok := raw.(string)
					if !ok || status != "completed" {
						return fmt.Errorf("terminal output item status must be completed")
					}
				}
			}
		}
	}
	return nil
}

// Completed function arguments must form one JSON object. This is a structural
// check only: tool-specific application schemas remain the application's job.
func validArguments(arguments string) error {
	if _, err := bodyObject(arguments); err != nil {
		return fmt.Errorf("tool arguments must be a complete JSON object: %w", err)
	}
	return nil
}
func functionArguments(function map[string]any) error {
	if _, err := requiredString(function, "name"); err != nil {
		return err
	}
	arguments, ok := function["arguments"].(string)
	if !ok {
		return fmt.Errorf("function arguments must be a string")
	}
	return validArguments(arguments)
}
func responseFunction(item map[string]any) error {
	if _, err := requiredString(item, "call_id"); err != nil {
		return err
	}
	return functionArguments(item)
}
func anthropicTool(block map[string]any) error {
	if block["type"] != "tool_use" && block["type"] != "server_tool_use" {
		return nil
	}
	if _, err := requiredString(block, "id"); err != nil {
		return err
	}
	if _, err := requiredString(block, "name"); err != nil {
		return err
	}
	if _, ok := asMap(block["input"]); !ok {
		return fmt.Errorf("tool input must be an object")
	}
	return nil
}
