package protocol

import (
	"fmt"
	"strings"

	"github.com/local/llm-replay-proxy/internal/model"
)

type streamedTool struct {
	id, kind, name string
	arguments      strings.Builder
}

// validateChatTools reconstructs fragmented tool arguments. A recording may
// only become replayable when every introduced call has a stable identity and
// complete JSON arguments.
func validateChatTools(rev model.Revision) error {
	_, objects, err := eventObjects(rev)
	if err != nil {
		return err
	}
	tools := map[string]*streamedTool{}
	legacy := map[string]*streamedTool{}
	for eventIndex, object := range objects {
		if object == nil {
			continue
		}
		choices, ok := asSlice(object["choices"])
		if !ok {
			continue
		}
		for _, rawChoice := range choices {
			choice, _ := asMap(rawChoice)
			choiceIndex, err := indexField(choice, "index")
			if err != nil {
				return fmt.Errorf("event %d choice: %w", eventIndex, err)
			}
			delta, _ := asMap(choice["delta"])
			// Gateways such as LiteLLM send explicit nulls for absent fields.
			if calls := delta["tool_calls"]; calls != nil {
				list, ok := asSlice(calls)
				if !ok {
					return fmt.Errorf("event %d tool_calls must be an array", eventIndex)
				}
				for position, rawCall := range list {
					call, ok := asMap(rawCall)
					if !ok {
						return fmt.Errorf("event %d tool call must be an object", eventIndex)
					}
					// Some compatible servers omit index; the call's position
					// within this chunk then identifies it.
					toolIndex := position
					if call["index"] != nil {
						toolIndex, err = indexField(call, "index")
						if err != nil {
							return fmt.Errorf("event %d tool call: %w", eventIndex, err)
						}
					}
					key := fmt.Sprintf("%d:%d", choiceIndex, toolIndex)
					state := tools[key]
					if state == nil {
						state = &streamedTool{}
						tools[key] = state
					}
					if err := mergeStable(&state.id, call["id"], "id", eventIndex); err != nil {
						return err
					}
					if err := mergeStable(&state.kind, call["type"], "type", eventIndex); err != nil {
						return err
					}
					if fnRaw, exists := call["function"]; exists {
						fn, ok := asMap(fnRaw)
						if !ok {
							return fmt.Errorf("event %d tool function must be an object", eventIndex)
						}
						if err := mergeStable(&state.name, fn["name"], "function name", eventIndex); err != nil {
							return err
						}
						if part, exists := fn["arguments"]; exists {
							s, ok := asString(part)
							if !ok {
								return fmt.Errorf("event %d tool arguments must be a string", eventIndex)
							}
							state.arguments.WriteString(s)
						}
					}
				}
			}
			if fnRaw := delta["function_call"]; fnRaw != nil {
				fn, ok := asMap(fnRaw)
				if !ok {
					return fmt.Errorf("event %d function_call must be an object", eventIndex)
				}
				key := fmt.Sprint(choiceIndex)
				state := legacy[key]
				if state == nil {
					state = &streamedTool{}
					legacy[key] = state
				}
				if err := mergeStable(&state.name, fn["name"], "function name", eventIndex); err != nil {
					return err
				}
				if part, exists := fn["arguments"]; exists {
					s, ok := asString(part)
					if !ok {
						return fmt.Errorf("event %d function arguments must be a string", eventIndex)
					}
					state.arguments.WriteString(s)
				}
			}
		}
	}
	for key, state := range tools {
		if state.id == "" || state.kind == "" {
			return fmt.Errorf("chat tool call %s is missing id or type", key)
		}
		if state.kind == "function" {
			if state.name == "" {
				return fmt.Errorf("chat tool call %s is missing function name", key)
			}
			if err := validArguments(state.arguments.String()); err != nil {
				return fmt.Errorf("chat tool call %s: %w", key, err)
			}
		}
	}
	seenIDs := map[string]string{}
	for key, state := range tools {
		if prior, exists := seenIDs[state.id]; exists {
			return fmt.Errorf("chat tool calls %s and %s share id %q", prior, key, state.id)
		}
		seenIDs[state.id] = key
	}
	for key, state := range legacy {
		if state.name == "" {
			return fmt.Errorf("chat function call %s has no name", key)
		}
		if err := validArguments(state.arguments.String()); err != nil {
			return fmt.Errorf("chat function call %s: %w", key, err)
		}
	}
	return nil
}

// mergeStable records a tool identity field. Continuation chunks may repeat
// it, omit it, or send null or "" (as several gateways do); none of those
// change the identity, and the final check rejects calls never identified.
func mergeStable(dst *string, value any, field string, eventIndex int) error {
	if value == nil || value == "" {
		return nil
	}
	s, ok := asString(value)
	if !ok || s == "" {
		return fmt.Errorf("event %d tool %s must be a nonempty string", eventIndex, field)
	}
	if *dst != "" && *dst != s {
		return fmt.Errorf("event %d changes tool %s", eventIndex, field)
	}
	*dst = s
	return nil
}
