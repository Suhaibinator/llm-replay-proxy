package protocol

import (
	"strings"
	"testing"

	"github.com/local/llm-replay-proxy/internal/model"
)

func structureRevision(events ...string) model.Revision {
	r := model.Revision{Status: 200}
	for _, s := range events {
		r.Events = append(r.Events, event(0, "", s))
	}
	return r
}
func TestResponseSnapshotsMustAgree(t *testing.T) {
	const delta = `{"type":"response.output_text.delta","output_index":0,"content_index":0,"item_id":"m1","delta":"ok"}`
	const done = `{"type":"response.output_text.done","output_index":0,"content_index":0,"item_id":"m1","text":"ok"}`
	const final = `{"type":"response.completed","response":{"id":"r1","status":"completed","output":[{"id":"m1","type":"message","content":[{"type":"output_text","text":"ok"}]}]}}`
	for _, snapshot := range []string{
		`{"type":"response.content_part.done","output_index":0,"content_index":0,"item_id":"m1","part":{"type":"output_text","text":"WRONG"}}`,
		`{"type":"response.output_item.done","output_index":0,"item":{"id":"m1","type":"message","content":[{"type":"output_text","text":"WRONG"}]}}`,
	} {
		if err := validateStructure(ResponsesRoute, true, structureRevision(delta, done, snapshot, final)); err == nil {
			t.Fatalf("accepted conflicting snapshot %s", snapshot)
		}
	}
	valid := structureRevision(delta, done, `{"type":"response.content_part.done","output_index":0,"content_index":0,"item_id":"m1","part":{"type":"output_text","text":"ok"}}`, `{"type":"response.output_item.done","output_index":0,"item":{"id":"m1","type":"message","content":[{"type":"output_text","text":"ok"}]}}`, final)
	if err := validateStructure(ResponsesRoute, true, valid); err != nil {
		t.Fatal(err)
	}
	for name, bad := range map[string]model.Revision{
		"identity":      structureRevision(delta, strings.Replace(done, `"m1"`, `"m2"`, 1), final),
		"missing index": structureRevision(strings.Replace(delta, `"content_index":0,`, "", 1), done, final),
		"late delta":    structureRevision(delta, done, delta, final),
		"orphan text":   structureRevision(final),
	} {
		t.Run(name, func(t *testing.T) {
			if err := validateStructure(ResponsesRoute, true, bad); err == nil {
				t.Fatal("accepted malformed stream")
			}
		})
	}
}
func TestResponsesMultipleTextParts(t *testing.T) {
	r := structureRevision(
		`{"type":"response.output_text.delta","output_index":0,"content_index":0,"item_id":"m1","delta":"first"}`,
		`{"type":"response.output_text.done","output_index":0,"content_index":0,"item_id":"m1","text":"first"}`,
		`{"type":"response.output_text.delta","output_index":0,"content_index":1,"item_id":"m1","delta":"second"}`,
		`{"type":"response.output_text.done","output_index":0,"content_index":1,"item_id":"m1","text":"second"}`,
		`{"type":"response.completed","response":{"status":"completed","output":[{"id":"m1","type":"message","content":[{"type":"output_text","text":"first"},{"type":"output_text","text":"second"}]}]}}`)
	if err := validateStructure(ResponsesRoute, true, r); err != nil {
		t.Fatal(err)
	}
}
func TestMessageStructuralValidation(t *testing.T) {
	start := `{"type":"message_start","message":{"id":"m1","type":"message","content":[]}}`
	stop := `{"type":"message_delta","delta":{"stop_reason":"end_turn"}}`
	end := `{"type":"message_stop"}`
	valid := []string{start, `{"type":"ping"}`, `{"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"tool1","name":"weather","input":{}}}`, `{"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{}"}}`, `{"type":"content_block_stop","index":0}`, stop, end}
	if err := validateStructure(MessagesRoute, true, structureRevision(valid...)); err != nil {
		t.Fatal(err)
	}
	for name, events := range map[string][]string{
		"missing message":  {`{"type":"message_start"}`, stop, end},
		"missing index":    {start, `{"type":"content_block_start","content_block":{"type":"text","text":""}}`, stop, end},
		"duplicate block":  {start, valid[2], valid[2], valid[4], stop, end},
		"mismatched delta": {start, valid[2], `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"bad"}}`, valid[4], stop, end},
		"late block":       {start, stop, valid[2], valid[4], end},
	} {
		t.Run(name, func(t *testing.T) {
			if err := validateStructure(MessagesRoute, true, structureRevision(events...)); err == nil {
				t.Fatal("accepted malformed stream")
			}
		})
	}
}
func TestChatStructuralValidation(t *testing.T) {
	valid := []string{`{"id":"c1","choices":[{"index":0,"delta":{"content":"ok"},"finish_reason":null}]}`, `{"id":"c1","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`, `[DONE]`}
	if err := validateStructure(ChatRoute, true, structureRevision(valid...)); err != nil {
		t.Fatal(err)
	}
	for _, events := range [][]string{
		{valid[0], strings.Replace(valid[1], `"c1"`, `"other"`, 1), valid[2]},
		{strings.Replace(valid[0], `"index":0`, `"index":-1`, 1), valid[1], valid[2]},
		{valid[0], strings.Replace(valid[1], `"delta":{},`, "", 1), valid[2]},
		{valid[0], valid[1], valid[0], valid[2]},
	} {
		if err := validateStructure(ChatRoute, true, structureRevision(events...)); err == nil {
			t.Fatal("accepted malformed chat stream")
		}
	}
}

func TestNonstreamRequiredStructures(t *testing.T) {
	for _, tc := range []struct{ route, body string }{
		{ChatRoute, `{"choices":[{"finish_reason":true,"message":{"content":"ok"}}]}`},
		{ChatRoute, `{"choices":[{"finish_reason":"stop","message":{"content":"ok"}},{"finish_reason":"stop","message":{"content":42}}]}`},
		{MessagesRoute, `{"type":"message","stop_reason":{},"content":[]}`},
		{ResponsesRoute, `{"status":"completed","output":[{"content":[]}]}`},
	} {
		if err := validateStructure(tc.route, false, model.Revision{Status: 200, Body: tc.body}); err == nil {
			t.Fatalf("accepted malformed %s", tc.body)
		}
	}
}

func TestCompletedToolSchemas(t *testing.T) {
	cases := []struct{ route, valid string }{
		{ChatRoute, `{"choices":[{"finish_reason":"tool_calls","message":{"content":null,"tool_calls":[{"id":"call1","type":"function","function":{"name":"weather","arguments":"{}"}}]}}]}`},
		{ResponsesRoute, `{"status":"completed","output":[{"type":"function_call","call_id":"call1","name":"weather","arguments":"{}"}]}`},
		{MessagesRoute, `{"type":"message","stop_reason":"tool_use","content":[{"type":"tool_use","id":"call1","name":"weather","input":{}}]}`},
	}
	for _, tc := range cases {
		if err := bodyStructure(tc.route, tc.valid); err != nil {
			t.Fatal(err)
		}
		for _, bad := range []string{
			strings.Replace(tc.valid, `"name":"weather",`, "", 1),
			strings.Replace(tc.valid, `"arguments":"{}"`, `"arguments":"{"`, 1),
			strings.Replace(tc.valid, `"input":{}`, `"input":null`, 1),
			strings.Replace(tc.valid, `"call_id":"call1",`, "", 1),
			strings.Replace(tc.valid, `"id":"call1",`, "", 1),
		} {
			if bad == tc.valid {
				continue
			}
			if err := bodyStructure(tc.route, bad); err == nil {
				t.Fatalf("accepted malformed tool %s", bad)
			}
		}
	}
	if err := bodyStructure(ChatRoute, `{"choices":[{"finish_reason":"tool_calls","message":{"content":null,"tool_calls":[{"bad":true}]}}]}`); err == nil {
		t.Fatal("accepted empty tool object")
	}
}
func TestStreamedToolArgumentConsistency(t *testing.T) {
	delta := `{"type":"response.function_call_arguments.delta","output_index":0,"item_id":"fc1","delta":"{}"}`
	done := `{"type":"response.function_call_arguments.done","output_index":0,"item_id":"fc1","arguments":"{}"}`
	final := `{"type":"response.completed","response":{"status":"completed","output":[{"id":"fc1","type":"function_call","call_id":"call1","name":"weather","arguments":"{}"}]}}`
	if err := validateStructure(ResponsesRoute, true, structureRevision(delta, done, final)); err != nil {
		t.Fatal(err)
	}
	for _, events := range [][]string{
		{delta, strings.Replace(done, `"arguments":"{}"`, `"arguments":"{\"x\":1}"`, 1), final},
		{delta, done, strings.Replace(final, `"arguments":"{}"`, `"arguments":"{"`, 1)},
		{delta, done, strings.Replace(final, `"name":"weather",`, "", 1)},
	} {
		if err := validateStructure(ResponsesRoute, true, structureRevision(events...)); err == nil {
			t.Fatal("accepted malformed streamed function")
		}
	}
	start := `{"type":"message_start","message":{"id":"m1","type":"message","content":[]}}`
	block := `{"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"t1","name":"f","input":{}}}`
	invalid := `{"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{"}}`
	if err := validateStructure(MessagesRoute, true, structureRevision(start, block, invalid, `{"type":"content_block_stop","index":0}`, `{"type":"message_delta","delta":{"stop_reason":"tool_use"}}`, `{"type":"message_stop"}`)); err == nil {
		t.Fatal("accepted incomplete tool JSON")
	}
}
func TestMessagesParameterlessToolStream(t *testing.T) {
	start := `{"type":"message_start","message":{"id":"m1","type":"message","content":[]}}`
	block := `{"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"t1","name":"now","input":{}}}`
	empty := `{"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":""}}`
	stop := `{"type":"content_block_stop","index":0}`
	tail := []string{`{"type":"message_delta","delta":{"stop_reason":"tool_use"}}`, `{"type":"message_stop"}`}
	// Claude streams an empty partial_json for tools without parameters; the
	// start block's input ({}) stands.
	if err := validateStructure(MessagesRoute, true, structureRevision(append([]string{start, block, empty, stop}, tail...)...)); err != nil {
		t.Fatal(err)
	}
	// The leading empty delta also precedes real arguments.
	args := `{"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"tz\":\"UTC\"}"}}`
	if err := validateStructure(MessagesRoute, true, structureRevision(append([]string{start, block, empty, args, stop}, tail...)...)); err != nil {
		t.Fatal(err)
	}
}
