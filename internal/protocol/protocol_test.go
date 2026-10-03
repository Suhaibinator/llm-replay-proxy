package protocol

import (
	"fmt"
	"math"
	"strings"
	"testing"

	"github.com/local/llm-replay-proxy/internal/model"
)

func event(ms int64, event, data string) model.Event {
	prefix := ""
	if event != "" {
		prefix = "event: " + event + "\n"
	}
	return model.Event{OffsetMS: ms, Data: prefix + "data: " + data + "\n\n"}
}

func TestTextEditRoundTripsEmptyAndUnicode(t *testing.T) {
	for name, tc := range fixtures() {
		for _, replacement := range []string{"", "こんにちは 🌍\nnaïve"} {
			t.Run(name+replacement, func(t *testing.T) {
				edited, err := EditText(tc.route, tc.stream, tc.rev, replacement)
				if err != nil {
					t.Fatal(err)
				}
				got, reason := Text(tc.route, tc.stream, edited)
				if reason != "" || got != replacement {
					t.Fatalf("got %q reason %q", got, reason)
				}
			})
		}
	}
}

func TestOffsetOverflowRejected(t *testing.T) {
	tc := fixtures()["chat stream"]
	tc.rev.Events[len(tc.rev.Events)-1].OffsetMS = math.MaxInt64
	if err := Validate(tc.route, true, tc.rev); err == nil {
		t.Fatal("overflowing millisecond offset accepted")
	}
}

func TestUnreplayableStatusAndStreamingMIMERejected(t *testing.T) {
	for _, status := range []int{204, 205} {
		r := fixtures()["chat body"].rev
		r.Status = status
		if err := Validate(ChatRoute, false, r); err == nil {
			t.Fatalf("status %d accepted", status)
		}
	}
	r := fixtures()["chat stream"].rev
	r.Headers = map[string]string{"content-type": "application/json"}
	if err := Validate(ChatRoute, true, r); err == nil {
		t.Fatal("non-SSE streaming content type accepted")
	}
}

func TestStreamingChatRefusalUsesAdvancedEditor(t *testing.T) {
	r := model.Revision{Status: 200, Events: []model.Event{event(0, "", `{"id":"c","choices":[{"index":0,"delta":{"refusal":"cannot"},"finish_reason":null}]}`), event(1, "", `{"id":"c","choices":[{"index":0,"delta":{},"finish_reason":"content_filter"}]}`), event(2, "", `[DONE]`)}}
	if err := Validate(ChatRoute, true, r); err != nil {
		t.Fatal(err)
	}
	if _, reason := Text(ChatRoute, true, r); !strings.Contains(reason, "refusal") {
		t.Fatalf("reason=%q", reason)
	}
	if _, err := EditText(ChatRoute, true, r, "unsafe"); err == nil {
		t.Fatal("refusal became plain editable")
	}
}

func TestResponsesFunctionIdentityCannotChange(t *testing.T) {
	r := model.Revision{Status: 200, Events: []model.Event{event(0, "", `{"type":"response.output_item.added","output_index":0,"item":{"id":"fc","type":"function_call","call_id":"call-a","name":"safe","arguments":""}}`), event(1, "", `{"type":"response.output_item.done","output_index":0,"item":{"id":"fc","type":"function_call","call_id":"call-b","name":"other","arguments":"{}"}}`), event(2, "", `{"type":"response.completed","response":{"id":"r","status":"completed","output":[{"id":"fc","type":"function_call","call_id":"call-b","name":"other","arguments":"{}"}]}}`)}}
	if err := Validate(ResponsesRoute, true, r); err == nil {
		t.Fatal("changed function identity accepted")
	}
}

func TestRefusalWithTextCannotUsePlainEditor(t *testing.T) {
	for _, content := range []string{"", "partial"} {
		r := model.Revision{Status: 200, Body: `{"choices":[{"finish_reason":"stop","message":{"role":"assistant","content":` + fmt.Sprintf("%q", content) + `,"refusal":"Cannot assist"}}]}`}
		if err := Validate(ChatRoute, false, r); err != nil {
			t.Fatal(err)
		}
		if _, reason := Text(ChatRoute, false, r); !strings.Contains(reason, "refusal") {
			t.Fatalf("reason=%q", reason)
		}
		if _, err := EditText(ChatRoute, false, r, "replacement"); err == nil {
			t.Fatal("refusal was plain editable")
		}
	}
}

func TestTerminalResponseItemStatus(t *testing.T) {
	for _, kind := range []string{"message", "function_call"} {
		var body string
		if kind == "message" {
			body = `{"status":"completed","output":[{"id":"m","type":"message","status":"in_progress","content":[{"type":"output_text","text":"x"}]}]}`
		} else {
			body = `{"status":"completed","output":[{"id":"f","type":"function_call","status":"in_progress","call_id":"c","name":"f","arguments":"{}"}]}`
		}
		if err := Validate(ResponsesRoute, false, model.Revision{Status: 200, Body: body}); err == nil {
			t.Fatalf("accepted %s in_progress", kind)
		}
	}
}

func TestDuplicateKnownToolIDsRejected(t *testing.T) {
	cases := []struct {
		route string
		body  string
	}{
		{ChatRoute, `{"choices":[{"finish_reason":"tool_calls","message":{"content":null,"tool_calls":[{"id":"dup","type":"function","function":{"name":"a","arguments":"{}"}},{"id":"dup","type":"function","function":{"name":"b","arguments":"{}"}}]}}]}`},
		{ResponsesRoute, `{"status":"completed","output":[{"type":"function_call","call_id":"dup","name":"a","arguments":"{}"},{"type":"function_call","call_id":"dup","name":"b","arguments":"{}"}]}`},
		{MessagesRoute, `{"type":"message","stop_reason":"tool_use","content":[{"type":"tool_use","id":"dup","name":"a","input":{}},{"type":"tool_use","id":"dup","name":"b","input":{}}]}`},
	}
	for _, tc := range cases {
		if err := Validate(tc.route, false, model.Revision{Status: 200, Body: tc.body}); err == nil {
			t.Fatalf("duplicate accepted for %s", tc.route)
		}
	}
}

func TestResponsesMultipleSnapshotsValidate(t *testing.T) {
	rev := model.Revision{Status: 200, Events: []model.Event{
		event(0, "", `{"type":"response.output_text.delta","output_index":0,"content_index":0,"item_id":"m1","delta":"a"}`), event(1, "", `{"type":"response.output_text.done","output_index":0,"content_index":0,"item_id":"m1","text":"a"}`), event(2, "", `{"type":"response.content_part.done","output_index":0,"content_index":0,"item_id":"m1","part":{"type":"output_text","text":"a"}}`),
		event(3, "", `{"type":"response.output_text.delta","output_index":0,"content_index":1,"item_id":"m1","delta":"b"}`), event(4, "", `{"type":"response.output_text.done","output_index":0,"content_index":1,"item_id":"m1","text":"b"}`), event(5, "", `{"type":"response.content_part.done","output_index":0,"content_index":1,"item_id":"m1","part":{"type":"output_text","text":"b"}}`),
		event(6, "", `{"type":"response.completed","response":{"id":"r","status":"completed","output":[{"id":"m1","type":"message","content":[{"type":"output_text","text":"a"},{"type":"output_text","text":"b"}]}]}}`)}}
	if err := Validate(ResponsesRoute, true, rev); err != nil {
		t.Fatal(err)
	}
	if _, reason := Text(ResponsesRoute, true, rev); !strings.Contains(reason, "multiple text parts") {
		t.Fatalf("reason=%q", reason)
	}
}

func TestAnthropicStartOnlyTextCanBeEdited(t *testing.T) {
	rev := model.Revision{Status: 200, Events: []model.Event{event(0, "message_start", `{"type":"message_start","message":{"id":"m","type":"message","content":[]}}`), event(1, "content_block_start", `{"type":"content_block_start","index":0,"content_block":{"type":"text","text":"old"}}`), event(2, "content_block_stop", `{"type":"content_block_stop","index":0}`), event(3, "message_delta", `{"type":"message_delta","delta":{"stop_reason":"end_turn"}}`), event(4, "message_stop", `{"type":"message_stop"}`)}}
	edited, err := EditText(MessagesRoute, true, rev, "new")
	if err != nil {
		t.Fatal(err)
	}
	got, reason := Text(MessagesRoute, true, edited)
	if got != "new" || reason != "" {
		t.Fatalf("got %q reason %q", got, reason)
	}
}

func fixtures() map[string]struct {
	route  string
	stream bool
	rev    model.Revision
	want   string
} {
	return map[string]struct {
		route  string
		stream bool
		rev    model.Revision
		want   string
	}{
		"chat body": {ChatRoute, false, model.Revision{Status: 200, Body: `{"id":"c1","choices":[{"message":{"role":"assistant","content":"hello"},"finish_reason":"stop"}]}`}, "hello"},
		"chat stream": {ChatRoute, true, model.Revision{Status: 200, Events: []model.Event{
			event(2, "", `{"id":"c1","choices":[{"index":0,"delta":{"role":"assistant","content":"hel"},"finish_reason":null}]}`),
			event(4, "", `{"id":"c1","choices":[{"index":0,"delta":{"content":"lo"},"finish_reason":null}]}`),
			event(5, "", `{"id":"c1","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`), event(6, "", `[DONE]`)}}, "hello"},
		"responses body": {ResponsesRoute, false, model.Revision{Status: 200, Body: `{"id":"r1","status":"completed","output":[{"id":"m1","type":"message","role":"assistant","content":[{"type":"output_text","text":"hello","annotations":[]}]}]}`}, "hello"},
		"responses stream": {ResponsesRoute, true, model.Revision{Status: 200, Events: []model.Event{
			event(1, "response.output_text.delta", `{"type":"response.output_text.delta","item_id":"m1","output_index":0,"content_index":0,"delta":"hel"}`),
			event(2, "response.output_text.delta", `{"type":"response.output_text.delta","item_id":"m1","output_index":0,"content_index":0,"delta":"lo"}`),
			event(3, "response.output_text.done", `{"type":"response.output_text.done","item_id":"m1","output_index":0,"content_index":0,"text":"hello"}`),
			event(4, "response.completed", `{"type":"response.completed","response":{"id":"r1","status":"completed","output":[{"id":"m1","type":"message","role":"assistant","content":[{"type":"output_text","text":"hello"}]}]}}`)}}, "hello"},
		"messages body": {MessagesRoute, false, model.Revision{Status: 200, Body: `{"id":"m1","type":"message","role":"assistant","content":[{"type":"text","text":"hello"}],"stop_reason":"end_turn"}`}, "hello"},
		"messages stream": {MessagesRoute, true, model.Revision{Status: 200, Events: []model.Event{
			event(1, "message_start", `{"type":"message_start","message":{"id":"m1","type":"message","role":"assistant","content":[],"stop_reason":null}}`),
			event(2, "content_block_start", `{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`),
			event(3, "content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"hel"}}`),
			event(4, "content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"lo"}}`),
			event(5, "content_block_stop", `{"type":"content_block_stop","index":0}`),
			event(6, "message_delta", `{"type":"message_delta","delta":{"stop_reason":"end_turn","stop_sequence":null},"usage":{"output_tokens":1}}`),
			event(7, "message_stop", `{"type":"message_stop"}`)}}, "hello"},
	}
}

func TestFixturesValidateExtractAndEdit(t *testing.T) {
	for name, tc := range fixtures() {
		t.Run(name, func(t *testing.T) {
			if err := Validate(tc.route, tc.stream, tc.rev); err != nil {
				t.Fatalf("Validate: %v", err)
			}
			got, reason := Text(tc.route, tc.stream, tc.rev)
			if reason != "" || got != tc.want {
				t.Fatalf("Text = %q, %q", got, reason)
			}
			edited, err := EditText(tc.route, tc.stream, tc.rev, "replacement")
			if err != nil {
				t.Fatalf("EditText: %v", err)
			}
			if edited.Source != "edit" || edited.ID != 0 {
				t.Fatalf("new revision metadata: %+v", edited)
			}
			got, reason = Text(tc.route, tc.stream, edited)
			if reason != "" || got != "replacement" {
				t.Fatalf("edited Text = %q, %q", got, reason)
			}
			if tc.stream && len(edited.Events) != len(tc.rev.Events) {
				t.Fatal("event ordering changed")
			}
		})
	}
}

func TestIncompleteAndErrorsRejected(t *testing.T) {
	tests := map[string]struct {
		route  string
		stream bool
		rev    model.Revision
	}{
		"http error":            {ChatRoute, false, model.Revision{Status: 500, Body: `{"error":{}}`}},
		"chat missing done":     {ChatRoute, true, model.Revision{Status: 200, Events: []model.Event{event(0, "", `{"choices":[{"index":0,"delta":{"content":"x"},"finish_reason":"stop"}]}`)}}},
		"responses mismatch":    {ResponsesRoute, true, model.Revision{Status: 200, Events: []model.Event{event(0, "", `{"type":"response.output_text.delta","delta":"x"}`), event(1, "", `{"type":"response.output_text.done","text":"y"}`), event(2, "", `{"type":"response.completed","response":{"status":"completed","output":[{"type":"message","content":[{"type":"output_text","text":"y"}]}]}}`)}}},
		"messages missing stop": {MessagesRoute, true, model.Revision{Status: 200, Events: []model.Event{event(0, "message_start", `{"type":"message_start"}`)}}},
		"malformed frame":       {ChatRoute, true, model.Revision{Status: 200, Events: []model.Event{{Data: "data: {}\n", OffsetMS: 0}}}},
		"decreasing time":       {ChatRoute, true, model.Revision{Status: 200, Events: []model.Event{event(2, "", `{}`), event(1, "", `[DONE]`)}}},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			if err := Validate(tc.route, tc.stream, tc.rev); err == nil {
				t.Fatal("expected rejection")
			}
		})
	}
}

func TestPlainTextUnavailableForStructuredOutput(t *testing.T) {
	tests := []struct {
		route    string
		rev      model.Revision
		contains string
	}{
		{ChatRoute, model.Revision{Status: 200, Body: `{"choices":[{"message":{"role":"assistant","content":null,"tool_calls":[{"id":"call_1","type":"function","function":{"name":"f","arguments":"{}"}}]},"finish_reason":"tool_calls"}]}`}, "tool"},
		{ResponsesRoute, model.Revision{Status: 200, Body: `{"status":"completed","output":[{"type":"reasoning","summary":[]}]}`}, "reasoning"},
		{MessagesRoute, model.Revision{Status: 200, Body: `{"type":"message","content":[{"type":"tool_use","id":"tool_1","name":"f","input":{}}],"stop_reason":"tool_use"}`}, "tool"},
	}
	for _, tc := range tests {
		if err := Validate(tc.route, false, tc.rev); err != nil {
			t.Fatalf("structured response should be valid: %v", err)
		}
		_, reason := Text(tc.route, false, tc.rev)
		if !strings.Contains(strings.ToLower(reason), tc.contains) {
			t.Fatalf("unexpected reason %q", reason)
		}
		if _, err := EditText(tc.route, false, tc.rev, "x"); err == nil {
			t.Fatal("structured edit accepted")
		}
	}
}

func TestAdvancedValidationRejectsMalformedAndDoesNotRequireIDs(t *testing.T) {
	tc := fixtures()["responses body"]
	tc.rev.ID = 0
	tc.rev.RecordingID = 0
	if err := Validate(tc.route, tc.stream, tc.rev); err != nil {
		t.Fatal(err)
	}
	bad := tc.rev
	bad.Body = `{"status":"completed","output":[{"type":"message","content":[{"type":"output_text"}]}]}`
	if err := Validate(tc.route, false, bad); err == nil {
		t.Fatal("missing final text accepted")
	}
}

func TestPlainEditorEdgeCases(t *testing.T) {
	chat := model.Revision{Status: 200, Events: []model.Event{
		event(0, "", `{"id":"c","choices":[{"index":0,"delta":{"content":"a"},"finish_reason":"stop"}]}`),
		event(1, "", `{"id":"c","choices":[{"index":1,"delta":{"content":"b"},"finish_reason":"stop"}]}`), event(2, "", `[DONE]`),
	}}
	if err := Validate(ChatRoute, true, chat); err != nil {
		t.Fatal(err)
	}
	if _, reason := Text(ChatRoute, true, chat); !strings.Contains(reason, "multiple choices") {
		t.Fatalf("reason=%q", reason)
	}

	messages := fixtures()["messages stream"]
	messages.rev.Events = append(messages.rev.Events[:1], append([]model.Event{event(1, "ping", `{"type":"ping"}`)}, messages.rev.Events[1:]...)...)
	if err := Validate(MessagesRoute, true, messages.rev); err != nil {
		t.Fatalf("ping rejected: %v", err)
	}

	responses := model.Revision{Status: 200, Body: `{"id":"r","status":"completed","output":[{"type":"message","content":[{"type":"output_text","text":"a"},{"type":"output_text","text":"b"}]}]}`}
	if err := Validate(ResponsesRoute, false, responses); err != nil {
		t.Fatal(err)
	}
	if _, reason := Text(ResponsesRoute, false, responses); !strings.Contains(reason, "multiple text parts") {
		t.Fatalf("reason=%q", reason)
	}
}
