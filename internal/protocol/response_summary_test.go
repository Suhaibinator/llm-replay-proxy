package protocol

import (
	"bytes"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/local/llm-replay-proxy/internal/model"
)

func sse(objects ...string) []model.Event {
	out := make([]model.Event, 0, len(objects))
	for i, o := range objects {
		data := o
		if o != "[DONE]" {
			var compact bytes.Buffer
			if err := json.Compact(&compact, []byte(o)); err != nil {
				panic(fmt.Sprintf("fixture %d: %v", i, err))
			}
			data = compact.String()
		}
		out = append(out, model.Event{Data: "data: " + data + "\n\n", OffsetMS: int64(i)})
	}
	return out
}

func n(v int64) *int64 { return &v }

func usageString(u model.TokenUsage) string {
	f := func(p *int64) string {
		if p == nil {
			return "nil"
		}
		return fmt.Sprint(*p)
	}
	return fmt.Sprintf("in=%s cached=%s out=%s reasoning=%s total=%s", f(u.Input), f(u.CachedInput), f(u.Output), f(u.Reasoning), f(u.Total))
}

func TestSummarizeResponse(t *testing.T) {
	cost := 0.0123
	for _, tc := range []struct {
		name      string
		route     string
		streaming bool
		rev       model.Revision
		want      model.ResponseSummary
	}{
		{
			name: "chat json with tools, reasoning and OpenRouter cost", route: ChatRoute,
			rev: model.Revision{Body: `{"id":"c","model":"openai/o4-mini","choices":[{"index":0,"finish_reason":"tool_calls","message":{"role":"assistant","content":"héllo","reasoning":"think hard",
				"tool_calls":[{"id":"1","type":"function","function":{"name":"a","arguments":"{}"}},{"id":"2","type":"function","function":{"name":"b","arguments":"{}"}}]}}],
				"usage":{"prompt_tokens":100,"prompt_tokens_details":{"cached_tokens":40},"completion_tokens":50,"completion_tokens_details":{"reasoning_tokens":30},"total_tokens":150,"cost":0.0123}}`},
			want: model.ResponseSummary{Model: "openai/o4-mini", Outcome: "tool_calls", Usage: model.TokenUsage{Input: n(100), CachedInput: n(40), Output: n(50), Reasoning: n(30), Total: n(150)}, Cost: &cost, OutputChars: 5, ReasoningChars: 10, ToolCalls: 2},
		},
		{
			name: "chat json without usage", route: ChatRoute,
			rev:  model.Revision{Body: `{"model":"m","choices":[{"index":0,"finish_reason":"stop","message":{"role":"assistant","content":"hi"}}]}`},
			want: model.ResponseSummary{Model: "m", Outcome: "stop", OutputChars: 2},
		},
		{
			name: "chat stream with include_usage chunk", route: ChatRoute, streaming: true,
			rev: model.Revision{Events: sse(
				`{"id":"c","model":"gpt-4.1","choices":[{"index":0,"delta":{"role":"assistant","content":"Hel"},"finish_reason":null}],"usage":null}`,
				`{"id":"c","model":"gpt-4.1","choices":[{"index":0,"delta":{"reasoning_content":"why"},"finish_reason":null}],"usage":null}`,
				`{"id":"c","model":"gpt-4.1","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"t","type":"function","function":{"name":"f","arguments":""}}]},"finish_reason":null}]}`,
				`{"id":"c","model":"gpt-4.1","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{}"}}]},"finish_reason":null}]}`,
				`{"id":"c","model":"gpt-4.1","choices":[{"index":0,"delta":{"content":"lo"},"finish_reason":"stop"}]}`,
				`{"id":"c","model":"gpt-4.1","choices":[],"usage":{"prompt_tokens":10,"prompt_tokens_details":{"cached_tokens":0},"completion_tokens":5,"completion_tokens_details":{"reasoning_tokens":2},"total_tokens":15}}`,
				`[DONE]`)},
			want: model.ResponseSummary{Model: "gpt-4.1", Outcome: "stop", Usage: model.TokenUsage{Input: n(10), CachedInput: n(0), Output: n(5), Reasoning: n(2), Total: n(15)}, OutputChars: 5, ReasoningChars: 3, ToolCalls: 1},
		},
		{
			name: "responses json", route: ResponsesRoute,
			rev: model.Revision{Body: `{"id":"r","model":"o3","status":"incomplete","output":[
				{"type":"reasoning","summary":[{"type":"summary_text","text":"plan"}],"encrypted_content":"xxx"},
				{"type":"function_call","call_id":"c1","name":"f","arguments":"{}"},
				{"type":"web_search_call","status":"completed"},
				{"type":"message","role":"assistant","content":[{"type":"output_text","text":"done!"}]}],
				"usage":{"input_tokens":1000,"input_tokens_details":{"cached_tokens":800},"output_tokens":300,"output_tokens_details":{"reasoning_tokens":256},"total_tokens":1300}}`},
			want: model.ResponseSummary{Model: "o3", Outcome: "incomplete", Usage: model.TokenUsage{Input: n(1000), CachedInput: n(800), Output: n(300), Reasoning: n(256), Total: n(1300)}, OutputChars: 5, ReasoningChars: 4, ToolCalls: 2},
		},
		{
			name: "responses stream uses the terminal response", route: ResponsesRoute, streaming: true,
			rev: model.Revision{Events: sse(
				`{"type":"response.created","response":{"id":"r","model":"gpt-5","status":"in_progress","output":[]}}`,
				`{"type":"response.reasoning_summary_text.delta","item_id":"rs","output_index":0,"summary_index":0,"delta":"hmm"}`,
				`{"type":"response.output_text.delta","item_id":"m","output_index":1,"content_index":0,"delta":"ok"}`,
				`{"type":"response.completed","response":{"id":"r","model":"gpt-5","status":"completed","output":[
					{"type":"reasoning","summary":[{"type":"summary_text","text":"hmm"}]},
					{"type":"message","role":"assistant","content":[{"type":"output_text","text":"ok"}]}],
					"usage":{"input_tokens":20,"input_tokens_details":{"cached_tokens":5},"output_tokens":9,"output_tokens_details":{"reasoning_tokens":7},"total_tokens":29,"cost":0.0123}}}`)},
			want: model.ResponseSummary{Model: "gpt-5", Outcome: "completed", Usage: model.TokenUsage{Input: n(20), CachedInput: n(5), Output: n(9), Reasoning: n(7), Total: n(29)}, Cost: &cost, OutputChars: 2, ReasoningChars: 3},
		},
		{
			name: "responses stream without a terminal event", route: ResponsesRoute, streaming: true,
			rev: model.Revision{Events: sse(
				`{"type":"response.created","response":{"id":"r","model":"gpt-5","status":"in_progress","output":[]}}`,
				`{"type":"response.output_item.added","output_index":0,"item":{"type":"function_call","call_id":"c","name":"f","arguments":""}}`,
				`{"type":"response.output_text.delta","item_id":"m","output_index":1,"content_index":0,"delta":"partial"}`)},
			want: model.ResponseSummary{Model: "gpt-5", Outcome: "in_progress", OutputChars: 7, ToolCalls: 1},
		},
		{
			name: "messages json with thinking, tools and cache reads", route: MessagesRoute,
			rev: model.Revision{Body: `{"id":"msg","type":"message","model":"claude-sonnet-4-5","stop_reason":"tool_use","content":[
				{"type":"thinking","thinking":"let me see","signature":"s"},
				{"type":"text","text":"Checking"},
				{"type":"tool_use","id":"t1","name":"weather","input":{}}],
				"usage":{"input_tokens":12,"cache_creation_input_tokens":100,"cache_read_input_tokens":2000,"output_tokens":80}}`},
			want: model.ResponseSummary{Model: "claude-sonnet-4-5", Outcome: "tool_use", Usage: model.TokenUsage{Input: n(2112), CachedInput: n(2000), Output: n(80)}, OutputChars: 8, ReasoningChars: 10, ToolCalls: 1},
		},
		{
			name: "messages stream merges message_start and message_delta usage", route: MessagesRoute, streaming: true,
			rev: model.Revision{Events: sse(
				`{"type":"message_start","message":{"id":"m","type":"message","role":"assistant","model":"claude-opus-4-1","content":[],"stop_reason":null,"usage":{"input_tokens":30,"cache_read_input_tokens":500,"output_tokens":1}}}`,
				`{"type":"content_block_start","index":0,"content_block":{"type":"thinking","thinking":""}}`,
				`{"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"abc"}}`,
				`{"type":"content_block_stop","index":0}`,
				`{"type":"content_block_start","index":1,"content_block":{"type":"text","text":""}}`,
				`{"type":"ping"}`,
				`{"type":"content_block_delta","index":1,"delta":{"type":"text_delta","text":"Hello"}}`,
				`{"type":"content_block_stop","index":1}`,
				`{"type":"content_block_start","index":2,"content_block":{"type":"server_tool_use","id":"s","name":"web_search","input":{}}}`,
				`{"type":"content_block_stop","index":2}`,
				`{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":42,"cost":0.0123}}`,
				`{"type":"message_stop"}`)},
			want: model.ResponseSummary{Model: "claude-opus-4-1", Outcome: "end_turn", Usage: model.TokenUsage{Input: n(530), CachedInput: n(500), Output: n(42)}, Cost: &cost, OutputChars: 5, ReasoningChars: 3, ToolCalls: 1},
		},
		{
			name: "unparseable body", route: ChatRoute,
			rev:  model.Revision{Body: `{`},
			want: model.ResponseSummary{},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := SummarizeResponse(tc.route, tc.streaming, tc.rev)
			if usageString(got.Usage) != usageString(tc.want.Usage) {
				t.Errorf("usage %s, want %s", usageString(got.Usage), usageString(tc.want.Usage))
			}
			if (got.Cost == nil) != (tc.want.Cost == nil) || (got.Cost != nil && *got.Cost != *tc.want.Cost) {
				t.Errorf("cost %v, want %v", got.Cost, tc.want.Cost)
			}
			got.Usage, got.Cost, tc.want.Usage, tc.want.Cost = model.TokenUsage{}, nil, model.TokenUsage{}, nil
			if got != tc.want {
				t.Errorf("summary %+v, want %+v", got, tc.want)
			}
		})
	}
}

func TestSummarizeResponseNullsAreSerialized(t *testing.T) {
	b, err := json.Marshal(SummarizeResponse(ChatRoute, false, model.Revision{Body: `{"model":"m","choices":[{"index":0,"finish_reason":"stop","message":{"content":"x"}}]}`}))
	if err != nil {
		t.Fatal(err)
	}
	want := `{"model":"m","outcome":"stop","usage":{"input":null,"cached_input":null,"output":null,"reasoning":null,"total":null},"cost":null,"output_chars":1,"reasoning_chars":0,"tool_calls":0}`
	if string(b) != want {
		t.Fatalf("got  %s\nwant %s", b, want)
	}
}
