package protocol

import (
	"strings"
	"testing"

	"github.com/local/llm-replay-proxy/internal/model"
)

func chatStream(events ...string) model.Revision {
	r := model.Revision{Status: 200}
	for i, s := range events {
		r.Events = append(r.Events, event(int64(i), "", s))
	}
	return r
}

// textAndEditAgree asserts that the plain editor never offers text that
// EditText then refuses to write, and that refused recordings stay refused.
func textAndEditAgree(t *testing.T, route string, stream bool, rev model.Revision) (string, string) {
	t.Helper()
	text, reason := Text(route, stream, rev)
	edited, err := EditText(route, stream, rev, "replacement")
	if reason == "" {
		if err != nil {
			t.Fatalf("Text offered plain editing but EditText failed: %v", err)
		}
		if got, r := Text(route, stream, edited); got != "replacement" || r != "" {
			t.Fatalf("edited Text = %q, %q", got, r)
		}
	} else if err == nil {
		t.Fatalf("Text refused plain editing (%q) but EditText succeeded", reason)
	}
	return text, reason
}

func TestAnnotationsCitationsAndLogprobsNeedAdvancedEditor(t *testing.T) {
	const annotation = `{"type":"url_citation","start_index":4,"end_index":15,"url":"https://example.com","title":"Example"}`
	cases := map[string]struct {
		route  string
		stream bool
		rev    model.Revision
	}{
		"chat body annotations": {ChatRoute, false, model.Revision{Status: 200, Body: `{"id":"c","choices":[{"index":0,"finish_reason":"stop","message":{"role":"assistant","content":"See example.com for details","annotations":[` + annotation + `]}}]}`}},
		"chat body logprobs":    {ChatRoute, false, model.Revision{Status: 200, Body: `{"id":"c","choices":[{"index":0,"finish_reason":"stop","logprobs":{"content":[{"token":"hi","logprob":-0.1,"bytes":[104,105],"top_logprobs":[]}],"refusal":null},"message":{"role":"assistant","content":"hi"}}]}`}},
		"chat stream annotations": {ChatRoute, true, chatStream(
			`{"id":"c","choices":[{"index":0,"delta":{"role":"assistant","content":"See example.com for details"},"finish_reason":null}]}`,
			`{"id":"c","choices":[{"index":0,"delta":{"annotations":[`+annotation+`]},"finish_reason":null}]}`,
			`{"id":"c","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`, `[DONE]`)},
		"chat stream logprobs": {ChatRoute, true, chatStream(
			`{"id":"c","choices":[{"index":0,"delta":{"content":"hi"},"logprobs":{"content":[{"token":"hi","logprob":-0.1,"bytes":null,"top_logprobs":[]}]},"finish_reason":null}]}`,
			`{"id":"c","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`, `[DONE]`)},
		"responses body annotations": {ResponsesRoute, false, model.Revision{Status: 200, Body: `{"id":"r","status":"completed","output":[{"id":"m1","type":"message","role":"assistant","content":[{"type":"output_text","text":"See example.com for details","annotations":[` + annotation + `]}]}]}`}},
		"responses body logprobs":    {ResponsesRoute, false, model.Revision{Status: 200, Body: `{"id":"r","status":"completed","output":[{"id":"m1","type":"message","role":"assistant","content":[{"type":"output_text","text":"hi","annotations":[],"logprobs":[{"token":"hi","logprob":-0.1,"bytes":[],"top_logprobs":[]}]}]}]}`}},
		"responses stream annotation event": {ResponsesRoute, true, chatStream(
			`{"type":"response.output_text.delta","item_id":"m1","output_index":0,"content_index":0,"delta":"See example.com for details"}`,
			`{"type":"response.output_text.annotation.added","item_id":"m1","output_index":0,"content_index":0,"annotation_index":0,"annotation":`+annotation+`}`,
			`{"type":"response.output_text.done","item_id":"m1","output_index":0,"content_index":0,"text":"See example.com for details"}`,
			`{"type":"response.completed","response":{"id":"r","status":"completed","output":[{"id":"m1","type":"message","content":[{"type":"output_text","text":"See example.com for details","annotations":[]}]}]}}`)},
		"messages body citations": {MessagesRoute, false, model.Revision{Status: 200, Body: `{"id":"m","type":"message","role":"assistant","stop_reason":"end_turn","content":[{"type":"text","text":"The sky is blue.","citations":[{"type":"char_location","cited_text":"blue","document_index":0,"start_char_index":0,"end_char_index":4}]}]}`}},
		"messages stream start citations": {MessagesRoute, true, chatStream(
			`{"type":"message_start","message":{"id":"m","type":"message","content":[]}}`,
			`{"type":"content_block_start","index":0,"content_block":{"type":"text","text":"The sky is blue.","citations":[{"type":"char_location","cited_text":"blue","document_index":0,"start_char_index":0,"end_char_index":4}]}}`,
			`{"type":"content_block_stop","index":0}`,
			`{"type":"message_delta","delta":{"stop_reason":"end_turn"}}`, `{"type":"message_stop"}`)},
		"messages stream citations delta": {MessagesRoute, true, chatStream(
			`{"type":"message_start","message":{"id":"m","type":"message","content":[]}}`,
			`{"type":"content_block_start","index":0,"content_block":{"type":"text","text":"","citations":[]}}`,
			`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"blue"}}`,
			`{"type":"content_block_delta","index":0,"delta":{"type":"citations_delta","citation":{"type":"char_location","cited_text":"blue","document_index":0,"start_char_index":0,"end_char_index":4}}}`,
			`{"type":"content_block_stop","index":0}`,
			`{"type":"message_delta","delta":{"stop_reason":"end_turn"}}`, `{"type":"message_stop"}`)},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			if err := Validate(tc.route, tc.stream, tc.rev); err != nil {
				t.Fatalf("valid recording rejected: %v", err)
			}
			_, reason := textAndEditAgree(t, tc.route, tc.stream, tc.rev)
			if reason == "" {
				t.Fatal("annotated text was plain editable")
			}
		})
	}

	// Empty annotation and logprob containers remain plain editable.
	for name, tc := range map[string]struct {
		route  string
		stream bool
		rev    model.Revision
	}{
		"chat body":       {ChatRoute, false, model.Revision{Status: 200, Body: `{"id":"c","choices":[{"index":0,"finish_reason":"stop","logprobs":null,"message":{"role":"assistant","content":"hi","annotations":[]}}]}`}},
		"chat stream":     {ChatRoute, true, chatStream(`{"id":"c","choices":[{"index":0,"delta":{"content":"hi","annotations":[]},"logprobs":{"content":null,"refusal":null},"finish_reason":null}]}`, `{"id":"c","choices":[{"index":0,"delta":{},"logprobs":null,"finish_reason":"stop"}]}`, `[DONE]`)},
		"responses body":  {ResponsesRoute, false, model.Revision{Status: 200, Body: `{"id":"r","status":"completed","output":[{"id":"m1","type":"message","content":[{"type":"output_text","text":"hi","annotations":[],"logprobs":[]}]}]}`}},
		"messages body":   {MessagesRoute, false, model.Revision{Status: 200, Body: `{"id":"m","type":"message","stop_reason":"end_turn","content":[{"type":"text","text":"hi","citations":null}]}`}},
		"messages stream": {MessagesRoute, true, chatStream(`{"type":"message_start","message":{"id":"m","type":"message","content":[]}}`, `{"type":"content_block_start","index":0,"content_block":{"type":"text","text":"","citations":[]}}`, `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"hi"}}`, `{"type":"content_block_stop","index":0}`, `{"type":"message_delta","delta":{"stop_reason":"end_turn"}}`, `{"type":"message_stop"}`)},
	} {
		t.Run("empty "+name, func(t *testing.T) {
			if text, reason := textAndEditAgree(t, tc.route, tc.stream, tc.rev); reason != "" || text != "hi" {
				t.Fatalf("Text = %q, %q", text, reason)
			}
		})
	}
}

func TestChatStreamNullToolFields(t *testing.T) {
	rev := chatStream(
		`{"id":"c","choices":[{"index":0,"delta":{"role":"assistant","content":"hi","tool_calls":null,"function_call":null},"finish_reason":null}]}`,
		`{"id":"c","choices":[{"index":0,"delta":{"tool_calls":null},"finish_reason":"stop"}]}`, `[DONE]`)
	if err := Validate(ChatRoute, true, rev); err != nil {
		t.Fatal(err)
	}
	if text, reason := textAndEditAgree(t, ChatRoute, true, rev); text != "hi" || reason != "" {
		t.Fatalf("Text = %q, %q", text, reason)
	}
}

func TestAzureChatStream(t *testing.T) {
	rev := chatStream(
		`{"choices":[],"created":0,"id":"","model":"","object":"","prompt_filter_results":[{"prompt_index":0,"content_filter_results":{"hate":{"filtered":false,"severity":"safe"}}}]}`,
		`{"choices":[{"content_filter_results":{},"delta":{"role":"assistant","content":""},"finish_reason":null,"index":0,"logprobs":null}],"created":1,"id":"chatcmpl-1","model":"gpt-4o","object":"chat.completion.chunk"}`,
		`{"choices":[{"content_filter_results":{"hate":{"filtered":false,"severity":"safe"}},"delta":{"content":"Hi"},"finish_reason":null,"index":0}],"created":1,"id":"chatcmpl-1","model":"gpt-4o","object":"chat.completion.chunk"}`,
		`{"choices":[{"content_filter_offsets":{"check_offset":0,"start_offset":0,"end_offset":2},"content_filter_results":{"hate":{"filtered":false,"severity":"safe"}},"finish_reason":null,"index":0}],"created":0,"id":"","model":"","object":""}`,
		`{"choices":[{"content_filter_results":{},"delta":{},"finish_reason":"stop","index":0}],"created":1,"id":"chatcmpl-1","model":"gpt-4o","object":"chat.completion.chunk"}`,
		`{"choices":[{"content_filter_offsets":{"check_offset":2,"start_offset":0,"end_offset":2},"content_filter_results":{"hate":{"filtered":false,"severity":"safe"}},"finish_reason":null,"index":0}],"created":0,"id":"","model":"","object":""}`,
		`{"choices":[{"delta":{},"finish_reason":null,"index":0}],"id":"chatcmpl-1"}`,
		`[DONE]`)
	if err := Validate(ChatRoute, true, rev); err != nil {
		t.Fatal(err)
	}
	if text, reason := textAndEditAgree(t, ChatRoute, true, rev); text != "Hi" || reason != "" {
		t.Fatalf("Text = %q, %q", text, reason)
	}
	head, tail := rev.Events[:5], rev.Events[7:]
	for name, bad := range map[string]string{
		"content after finish":          `{"choices":[{"delta":{"content":"late"},"finish_reason":null,"index":0}],"id":"chatcmpl-1"}`,
		"delta-less choice with data":   `{"choices":[{"message":{"content":"late"},"finish_reason":null,"index":0}],"id":""}`,
		"delta-less choice with finish": `{"choices":[{"content_filter_results":{},"finish_reason":"stop","index":0}],"id":""}`,
		"changed nonempty id":           `{"choices":[{"delta":{},"finish_reason":null,"index":0}],"id":"chatcmpl-2"}`,
	} {
		t.Run(name, func(t *testing.T) {
			r := model.Revision{Status: 200}
			r.Events = append(append(append([]model.Event(nil), head...), event(5, "", bad)), tail...)
			if err := Validate(ChatRoute, true, r); err == nil {
				t.Fatal("accepted malformed Azure stream")
			}
		})
	}
}

func TestChatToolContinuationEmptyStrings(t *testing.T) {
	rev := chatStream(
		`{"id":"c","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"weather","arguments":""}}]},"finish_reason":null}]}`,
		`{"id":"c","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"","type":"function","function":{"name":"","arguments":"{\"city\":"}}]},"finish_reason":null}]}`,
		`{"id":"c","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"","function":{"name":"","arguments":"\"Paris\"}"}}]},"finish_reason":null}]}`,
		`{"id":"c","choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`, `[DONE]`)
	if err := Validate(ChatRoute, true, rev); err != nil {
		t.Fatal(err)
	}
	// A call that never receives a nonempty identity is still incomplete.
	missing := chatStream(
		`{"id":"c","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"","type":"function","function":{"name":"weather","arguments":"{}"}}]},"finish_reason":null}]}`,
		`{"id":"c","choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`, `[DONE]`)
	if err := Validate(ChatRoute, true, missing); err == nil {
		t.Fatal("tool call without any id accepted")
	}
	changed := chatStream(
		`{"id":"c","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"weather","arguments":"{"}}]},"finish_reason":null}]}`,
		`{"id":"c","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_2","function":{"arguments":"}"}}]},"finish_reason":null}]}`,
		`{"id":"c","choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`, `[DONE]`)
	if err := Validate(ChatRoute, true, changed); err == nil {
		t.Fatal("changed tool id accepted")
	}
}

func TestChatToolCallsWithoutIndex(t *testing.T) {
	rev := chatStream(
		`{"id":"c","choices":[{"index":0,"delta":{"tool_calls":[{"id":"call_1","type":"function","function":{"name":"a","arguments":"{}"}},{"id":"call_2","type":"function","function":{"name":"b","arguments":"{\"x\":1}"}}]},"finish_reason":null}]}`,
		`{"id":"c","choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`, `[DONE]`)
	if err := Validate(ChatRoute, true, rev); err != nil {
		t.Fatal(err)
	}
	// Positional indexes still keep calls separate: an invalid second call fails.
	bad := chatStream(
		`{"id":"c","choices":[{"index":0,"delta":{"tool_calls":[{"id":"call_1","type":"function","function":{"name":"a","arguments":"{}"}},{"id":"call_2","type":"function","function":{"name":"b","arguments":"{"}}]},"finish_reason":null}]}`,
		`{"id":"c","choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`, `[DONE]`)
	if err := Validate(ChatRoute, true, bad); err == nil {
		t.Fatal("malformed positional tool call accepted")
	}
}

func responsesReasoningTextStream(reasoningDone string) model.Revision {
	return chatStream(
		`{"type":"response.created","response":{"id":"r","status":"in_progress","output":[]}}`,
		`{"type":"response.output_item.added","output_index":0,"item":{"id":"rs_1","type":"reasoning","summary":[],"content":[]}}`,
		`{"type":"response.content_part.added","item_id":"rs_1","output_index":0,"content_index":0,"part":{"type":"reasoning_text","text":""}}`,
		`{"type":"response.reasoning_text.delta","item_id":"rs_1","output_index":0,"content_index":0,"delta":"think"}`,
		`{"type":"response.reasoning_text.done","item_id":"rs_1","output_index":0,"content_index":0,"text":"`+reasoningDone+`"}`,
		`{"type":"response.content_part.done","item_id":"rs_1","output_index":0,"content_index":0,"part":{"type":"reasoning_text","text":"think"}}`,
		`{"type":"response.output_item.done","output_index":0,"item":{"id":"rs_1","type":"reasoning","summary":[],"content":[{"type":"reasoning_text","text":"think"}]}}`,
		`{"type":"response.output_item.added","output_index":1,"item":{"id":"m1","type":"message","status":"in_progress","role":"assistant","content":[]}}`,
		`{"type":"response.content_part.added","item_id":"m1","output_index":1,"content_index":0,"part":{"type":"output_text","text":"","annotations":[]}}`,
		`{"type":"response.output_text.delta","item_id":"m1","output_index":1,"content_index":0,"delta":"hi"}`,
		`{"type":"response.output_text.done","item_id":"m1","output_index":1,"content_index":0,"text":"hi"}`,
		`{"type":"response.content_part.done","item_id":"m1","output_index":1,"content_index":0,"part":{"type":"output_text","text":"hi","annotations":[]}}`,
		`{"type":"response.output_item.done","output_index":1,"item":{"id":"m1","type":"message","status":"completed","role":"assistant","content":[{"type":"output_text","text":"hi","annotations":[]}]}}`,
		`{"type":"response.completed","response":{"id":"r","status":"completed","output":[{"id":"rs_1","type":"reasoning","summary":[],"content":[{"type":"reasoning_text","text":"think"}]},{"id":"m1","type":"message","status":"completed","role":"assistant","content":[{"type":"output_text","text":"hi","annotations":[]}]}]}}`)
}

func TestResponsesReasoningTextParts(t *testing.T) {
	rev := responsesReasoningTextStream("think")
	if err := Validate(ResponsesRoute, true, rev); err != nil {
		t.Fatal(err)
	}
	if _, reason := textAndEditAgree(t, ResponsesRoute, true, rev); !strings.Contains(reason, "reasoning") {
		t.Fatalf("reason=%q", reason)
	}
	if err := Validate(ResponsesRoute, true, responsesReasoningTextStream("other")); err == nil {
		t.Fatal("reasoning_text.done disagreeing with deltas accepted")
	}
	// Output text still belongs only to message items.
	bad := chatStream(
		`{"type":"response.output_item.added","output_index":0,"item":{"id":"rs_1","type":"reasoning","summary":[]}}`,
		`{"type":"response.output_text.delta","item_id":"rs_1","output_index":0,"content_index":0,"delta":"x"}`,
		`{"type":"response.output_item.done","output_index":0,"item":{"id":"rs_1","type":"reasoning","summary":[]}}`,
		`{"type":"response.completed","response":{"id":"r","status":"completed","output":[{"id":"rs_1","type":"reasoning","summary":[]}]}}`)
	if err := validateStructure(ResponsesRoute, true, bad); err == nil {
		t.Fatal("output_text on a reasoning item accepted")
	}
	mixed := chatStream(
		`{"type":"response.content_part.added","item_id":"m1","output_index":0,"content_index":0,"part":{"type":"reasoning_text","text":""}}`,
		`{"type":"response.output_text.delta","item_id":"m1","output_index":0,"content_index":0,"delta":"x"}`)
	if err := validateStructure(ResponsesRoute, true, mixed); err == nil {
		t.Fatal("output_text delta on a reasoning_text part accepted")
	}
}

func TestAnthropicMCPToolUseStream(t *testing.T) {
	rev := chatStream(
		`{"type":"message_start","message":{"id":"m","type":"message","content":[]}}`,
		`{"type":"content_block_start","index":0,"content_block":{"type":"mcp_tool_use","id":"mcptoolu_1","name":"echo","server_name":"srv","input":{}}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"text\":"}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"\"hi\"}"}}`,
		`{"type":"content_block_stop","index":0}`,
		`{"type":"content_block_start","index":1,"content_block":{"type":"mcp_tool_result","tool_use_id":"mcptoolu_1","is_error":false,"content":[{"type":"text","text":"hi"}]}}`,
		`{"type":"content_block_stop","index":1}`,
		`{"type":"message_delta","delta":{"stop_reason":"end_turn"}}`, `{"type":"message_stop"}`)
	if err := Validate(MessagesRoute, true, rev); err != nil {
		t.Fatal(err)
	}
	bad := rev
	bad.Events = append([]model.Event(nil), rev.Events...)
	bad.Events[3] = event(3, "", `{"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"\"hi\""}}`)
	if err := Validate(MessagesRoute, true, bad); err == nil {
		t.Fatal("incomplete MCP tool input accepted")
	}
	dup := `{"type":"message","stop_reason":"end_turn","content":[{"type":"mcp_tool_use","id":"d","name":"a","server_name":"s","input":{}},{"type":"mcp_tool_use","id":"d","name":"b","server_name":"s","input":{}}]}`
	if err := Validate(MessagesRoute, false, model.Revision{Status: 200, Body: dup}); err == nil {
		t.Fatal("duplicate MCP tool ids accepted")
	}
}

func TestChatAudio(t *testing.T) {
	body := model.Revision{Status: 200, Body: `{"id":"c","choices":[{"index":0,"finish_reason":"stop","message":{"role":"assistant","content":null,"refusal":null,"audio":{"id":"audio_1","data":"AAAA","expires_at":1,"transcript":"hello"}}}]}`}
	if err := Validate(ChatRoute, false, body); err != nil {
		t.Fatal(err)
	}
	if _, reason := textAndEditAgree(t, ChatRoute, false, body); !strings.Contains(reason, "audio") {
		t.Fatalf("reason=%q", reason)
	}
	length := model.Revision{Status: 200, Body: `{"id":"c","choices":[{"index":0,"finish_reason":"length","message":{"role":"assistant","content":null}}]}`}
	if err := Validate(ChatRoute, false, length); err != nil {
		t.Fatal(err)
	}
	textAndEditAgree(t, ChatRoute, false, length)
	if err := Validate(ChatRoute, false, model.Revision{Status: 200, Body: `{"choices":[{"index":0,"finish_reason":"stop","message":{"role":"assistant","content":null}}]}`}); err == nil {
		t.Fatal("empty stop message accepted")
	}
	stream := chatStream(
		`{"id":"c","choices":[{"index":0,"delta":{"role":"assistant","audio":{"id":"audio_1","transcript":"hel"}},"finish_reason":null}]}`,
		`{"id":"c","choices":[{"index":0,"delta":{"audio":{"data":"AAAA","transcript":"lo"}},"finish_reason":null}]}`,
		`{"id":"c","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`, `[DONE]`)
	if err := Validate(ChatRoute, true, stream); err != nil {
		t.Fatal(err)
	}
	if _, reason := textAndEditAgree(t, ChatRoute, true, stream); !strings.Contains(reason, "audio") {
		t.Fatalf("reason=%q", reason)
	}
}

func TestTextAndEditTextAgreeWithoutTextDeltas(t *testing.T) {
	chat := chatStream(
		`{"id":"c","choices":[{"index":0,"delta":{"role":"assistant"},"finish_reason":null}]}`,
		`{"id":"c","choices":[{"index":0,"delta":{},"finish_reason":"length"}]}`, `[DONE]`)
	if err := Validate(ChatRoute, true, chat); err != nil {
		t.Fatal(err)
	}
	textAndEditAgree(t, ChatRoute, true, chat)

	responses := chatStream(
		`{"type":"response.output_item.added","output_index":0,"item":{"id":"m1","type":"message","status":"in_progress","content":[]}}`,
		`{"type":"response.content_part.added","item_id":"m1","output_index":0,"content_index":0,"part":{"type":"output_text","text":""}}`,
		`{"type":"response.output_text.done","item_id":"m1","output_index":0,"content_index":0,"text":""}`,
		`{"type":"response.content_part.done","item_id":"m1","output_index":0,"content_index":0,"part":{"type":"output_text","text":""}}`,
		`{"type":"response.output_item.done","output_index":0,"item":{"id":"m1","type":"message","status":"completed","content":[{"type":"output_text","text":""}]}}`,
		`{"type":"response.completed","response":{"id":"r","status":"completed","output":[{"id":"m1","type":"message","status":"completed","content":[{"type":"output_text","text":""}]}]}}`)
	if err := Validate(ResponsesRoute, true, responses); err != nil {
		t.Fatal(err)
	}
	textAndEditAgree(t, ResponsesRoute, true, responses)
}

func TestResponsesNullItemStatus(t *testing.T) {
	stream := chatStream(
		`{"type":"response.output_item.added","output_index":0,"item":{"id":"rs","type":"reasoning","status":null,"summary":[]}}`,
		`{"type":"response.output_item.done","output_index":0,"item":{"id":"rs","type":"reasoning","status":null,"summary":[]}}`,
		`{"type":"response.output_text.delta","item_id":"m1","output_index":1,"content_index":0,"delta":"hi"}`,
		`{"type":"response.output_item.done","output_index":1,"item":{"id":"m1","type":"message","status":null,"content":[{"type":"output_text","text":"hi"}]}}`,
		`{"type":"response.completed","response":{"id":"r","status":"completed","output":[{"id":"rs","type":"reasoning","status":null,"summary":[]},{"id":"m1","type":"message","status":null,"content":[{"type":"output_text","text":"hi"}]}]}}`)
	if err := Validate(ResponsesRoute, true, stream); err != nil {
		t.Fatal(err)
	}
	body := model.Revision{Status: 200, Body: `{"id":"r","status":"completed","output":[{"id":"rs","type":"reasoning","status":null,"summary":[]},{"id":"m1","type":"message","status":null,"content":[{"type":"output_text","text":"hi"}]}]}`}
	if err := Validate(ResponsesRoute, false, body); err != nil {
		t.Fatal(err)
	}
	inProgress := strings.Replace(body.Body, `"type":"message","status":null`, `"type":"message","status":"in_progress"`, 1)
	if err := Validate(ResponsesRoute, false, model.Revision{Status: 200, Body: inProgress}); err == nil {
		t.Fatal("in-progress item accepted")
	}
}

func TestSSEBareFieldsAndBOM(t *testing.T) {
	base := fixtures()["chat stream"].rev
	bare := base
	bare.Events = append([]model.Event(nil), base.Events...)
	bare.Events = append(bare.Events[:1], append([]model.Event{{OffsetMS: 3, Data: ": keepalive\ndata\n\n"}}, bare.Events[1:]...)...)
	bare.Events[2].Data = "event\nid\n" + bare.Events[2].Data
	if err := Validate(ChatRoute, true, bare); err != nil {
		t.Fatal(err)
	}
	if text, reason := textAndEditAgree(t, ChatRoute, true, bare); text != "hello" || reason != "" {
		t.Fatalf("Text = %q, %q", text, reason)
	}

	bom := base
	bom.Events = append([]model.Event(nil), base.Events...)
	bom.Events[0].Data = "\xef\xbb\xbf" + bom.Events[0].Data
	if err := Validate(ChatRoute, true, bom); err != nil {
		t.Fatal(err)
	}
	if text, reason := textAndEditAgree(t, ChatRoute, true, bom); text != "hello" || reason != "" {
		t.Fatalf("Text = %q, %q", text, reason)
	}
	edited, err := EditText(ChatRoute, true, bom, "x")
	if err != nil || !strings.HasPrefix(edited.Events[0].Data, "\xef\xbb\xbfdata: ") {
		t.Fatalf("BOM not preserved: %q %v", edited.Events[0].Data, err)
	}

	for name, data := range map[string]string{
		"late BOM":      "\xef\xbb\xbf" + base.Events[1].Data,
		"unknown field": "bogus\n" + base.Events[1].Data,
	} {
		r := base
		r.Events = append([]model.Event(nil), base.Events...)
		r.Events[1].Data = data
		if err := Validate(ChatRoute, true, r); err == nil {
			t.Fatalf("%s accepted", name)
		}
	}
}
