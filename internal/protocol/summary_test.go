package protocol

import (
	"strings"
	"testing"
)

func TestSummarizeRequest(t *testing.T) {
	for _, tc := range []struct {
		name, route, body    string
		model, preview       string
		items, calls, images int
	}{
		{"responses", ResponsesRoute, `{"model":"gpt","instructions":"Be brief","input":[
			{"role":"system","content":[{"type":"input_text","text":"rules"}]},
			{"role":"user","content":[{"type":"input_text","text":"first   question"},{"type":"input_image","image_url":"data:image/png;base64,AA=="}]},
			{"type":"function_call","call_id":"c1","name":"f","arguments":"{}"},
			{"type":"function_call_output","call_id":"c1","output":[{"type":"input_image","image_url":"data:image/png;base64,AA=="}]},
			{"role":"assistant","content":[{"type":"output_text","text":"answer"}]},
			{"role":"user","content":"follow up"}]}`, "gpt", "follow up", 6, 1, 2},
		{"responses string input", ResponsesRoute, `{"model":"m","input":"hello"}`, "m", "hello", 1, 0, 0},
		{"chat", ChatRoute, `{"model":"c","messages":[
			{"role":"system","content":"rules"},
			{"role":"user","content":[{"type":"text","text":"look"},{"type":"image_url","image_url":{"url":"https://x/a.png"}}]},
			{"role":"assistant","content":null,"tool_calls":[{"id":"1"},{"id":"2"}]},
			{"role":"tool","tool_call_id":"1","content":"ok"}]}`, "c", "look", 4, 2, 1},
		{"messages", MessagesRoute, `{"model":"claude","system":"rules","messages":[
			{"role":"user","content":"start"},
			{"role":"assistant","content":[{"type":"tool_use","id":"t","name":"f","input":{}}]},
			{"role":"user","content":[{"type":"tool_result","tool_use_id":"t","content":[{"type":"image","source":{"type":"base64","media_type":"image/png","data":"AA=="}}]}]}]}`, "claude", "start", 3, 1, 1},
		{"not json", ResponsesRoute, `{`, "", "", 0, 0, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := SummarizeRequest(tc.route, []byte(tc.body))
			if s.Model != tc.model || s.Preview != tc.preview || s.Items != tc.items || s.ToolCalls != tc.calls || s.Images != tc.images || s.Bytes != int64(len(tc.body)) {
				t.Fatalf("summary %+v", s)
			}
			if (s.Thread == "") != (tc.preview == "") {
				t.Fatalf("thread %q", s.Thread)
			}
		})
	}
}

func TestSummaryThreadIsSharedByLaterTurns(t *testing.T) {
	first := SummarizeRequest(ResponsesRoute, []byte(`{"instructions":"x","input":[{"role":"user","content":"hi"}]}`))
	later := SummarizeRequest(ResponsesRoute, []byte(`{"instructions":"x","input":[{"role":"user","content":"hi"},{"role":"assistant","content":"yo"},{"role":"user","content":"more"}]}`))
	other := SummarizeRequest(ResponsesRoute, []byte(`{"instructions":"y","input":[{"role":"user","content":"hi"}]}`))
	if first.Thread == "" || first.Thread != later.Thread || first.Thread == other.Thread {
		t.Fatalf("threads %q %q %q", first.Thread, later.Thread, other.Thread)
	}
	if later.Preview != "more" || later.Opening != "hi" || first.Opening != "hi" {
		t.Fatalf("preview %q opening %q %q", later.Preview, later.Opening, first.Opening)
	}
	tools := SummarizeRequest(MessagesRoute, []byte(`{"messages":[{"role":"user","content":[{"type":"tool_result","tool_use_id":"t"}]},{"role":"user","content":"  real   start "},{"role":"user","content":"last"}]}`))
	if tools.Opening != "real start" || tools.Preview != "last" {
		t.Fatalf("opening %q preview %q", tools.Opening, tools.Preview)
	}
}

func TestSummaryPreviewIsBounded(t *testing.T) {
	s := SummarizeRequest(ChatRoute, []byte(`{"messages":[{"role":"user","content":"`+strings.Repeat("é", 500)+`"}]}`))
	if r := []rune(s.Preview); len(r) != previewRunes || r[len(r)-1] != '…' {
		t.Fatalf("preview has %d runes", len(r))
	}
}
