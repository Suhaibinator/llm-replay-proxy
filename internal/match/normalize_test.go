package match

import "testing"

func mustKey(t *testing.T, route, body string, exclusions ...string) string {
	t.Helper()
	key, _, err := Key(route, []byte(body), exclusions)
	if err != nil {
		t.Fatal(err)
	}
	return key
}

func TestToolOrderDoesNotChangeKey(t *testing.T) {
	for _, tc := range []struct{ route, a, b string }{
		{"/v1/responses",
			`{"model":"m","input":"hi","tools":[{"type":"function","name":"b"},{"type":"web_search"},{"type":"function","name":"a"}]}`,
			`{"model":"m","input":"hi","tools":[{"type":"function","name":"a"},{"type":"function","name":"b"},{"type":"web_search"}]}`},
		{"/v1/chat/completions",
			`{"model":"m","messages":[],"tools":[{"type":"function","function":{"name":"b"}},{"type":"function","function":{"name":"a"}}]}`,
			`{"model":"m","messages":[],"tools":[{"type":"function","function":{"name":"a"}},{"type":"function","function":{"name":"b"}}]}`},
		{"/v1/messages",
			`{"model":"m","messages":[],"tools":[{"name":"b","input_schema":{}},{"name":"a","input_schema":{}}]}`,
			`{"model":"m","messages":[],"tools":[{"name":"a","input_schema":{}},{"name":"b","input_schema":{}}]}`},
	} {
		if mustKey(t, tc.route, tc.a) != mustKey(t, tc.route, tc.b) {
			t.Errorf("%s: reordered tools changed the key", tc.route)
		}
	}
	// A different tool set still changes the key.
	if mustKey(t, "/v1/responses", `{"input":"hi","tools":[{"type":"function","name":"a"}]}`) == mustKey(t, "/v1/responses", `{"input":"hi","tools":[{"type":"function","name":"b"}]}`) {
		t.Error("different tools matched")
	}
}

func TestEmbeddedToolJSONIsCanonical(t *testing.T) {
	for _, tc := range []struct{ name, route, a, b string }{
		{"responses arguments and output", "/v1/responses",
			`{"input":[{"type":"function_call","call_id":"c","name":"f","arguments":"{\"b\":1,\"a\":[2,{\"y\":1,\"x\":2}]}"},{"type":"function_call_output","call_id":"c","output":"{\"summary\":{},\"questions\":[]}"}]}`,
			`{"input":[{"type":"function_call","call_id":"c","name":"f","arguments":"{ \"a\": [2, {\"x\":2,\"y\":1}], \"b\": 1 }"},{"type":"function_call_output","call_id":"c","output":"{\"questions\":[],\"summary\":{}}"}]}`},
		{"responses output content parts", "/v1/responses",
			`{"input":[{"type":"function_call_output","call_id":"c","output":[{"type":"input_text","text":"{\"b\":1,\"a\":2}"}]}]}`,
			`{"input":[{"type":"function_call_output","call_id":"c","output":[{"type":"input_text","text":"{\"a\":2,\"b\":1}"}]}]}`},
		{"chat tool calls and results", "/v1/chat/completions",
			`{"messages":[{"role":"assistant","tool_calls":[{"id":"c","type":"function","function":{"name":"f","arguments":"{\"b\":1,\"a\":2}"}}]},{"role":"tool","tool_call_id":"c","content":"{\"b\":1,\"a\":2}"}]}`,
			`{"messages":[{"role":"assistant","tool_calls":[{"id":"c","type":"function","function":{"name":"f","arguments":"{\"a\":2,\"b\":1}"}}]},{"role":"tool","tool_call_id":"c","content":"{\"a\":2,\"b\":1}"}]}`},
		{"messages tool results", "/v1/messages",
			`{"messages":[{"role":"user","content":[{"type":"tool_result","tool_use_id":"t","content":"{\"b\":1,\"a\":2}"},{"type":"tool_result","tool_use_id":"u","content":[{"type":"text","text":"[{\"b\":1,\"a\":2}]"}]}]}]}`,
			`{"messages":[{"role":"user","content":[{"type":"tool_result","tool_use_id":"t","content":"{\"a\":2,\"b\":1}"},{"type":"tool_result","tool_use_id":"u","content":[{"type":"text","text":"[{\"a\":2,\"b\":1}]"}]}]}]}`},
	} {
		if mustKey(t, tc.route, tc.a) != mustKey(t, tc.route, tc.b) {
			t.Errorf("%s: key order inside tool JSON changed the key", tc.name)
		}
	}
}

func TestEmbeddedToolJSONKeepsMeaningfulDifferences(t *testing.T) {
	call := func(arguments string) string {
		return `{"input":[{"type":"function_call","call_id":"c","name":"f","arguments":` + arguments + `}]}`
	}
	base := mustKey(t, "/v1/responses", call(`"{\"a\":1}"`))
	for _, other := range []string{
		`"{\"a\":2}"`,            // a different value
		`"{\"a\":1.0}"`,          // numbers keep their exact lexeme
		`"[1,2]"`,                // a different shape
		`{"a":1}`,                // an object is not the same as its encoding in a string
		`"{\"a\":1,\"a\":1}"`,    // duplicate keys are not canonicalized, so the text is kept
		`"plain {\"a\":1} text"`, // not JSON, kept verbatim
	} {
		if mustKey(t, "/v1/responses", call(other)) == base {
			t.Errorf("arguments %s matched {\"a\":1}", other)
		}
	}
	// Strings outside tool calls and results are never reinterpreted.
	user := func(text string) string { return `{"input":[{"role":"user","content":` + text + `}]}` }
	if mustKey(t, "/v1/responses", user(`"{\"b\":1,\"a\":2}"`)) == mustKey(t, "/v1/responses", user(`"{\"a\":2,\"b\":1}"`)) {
		t.Error("user message JSON was canonicalized")
	}
}

func TestExclusionsUseWireOrderBeforeNormalizing(t *testing.T) {
	// /tools/0 names the first tool as sent, before tools are sorted.
	a := mustKey(t, "/v1/responses", `{"input":"hi","tools":[{"type":"function","name":"volatile_1"},{"type":"function","name":"a"}]}`, "/tools/0")
	b := mustKey(t, "/v1/responses", `{"input":"hi","tools":[{"type":"function","name":"volatile_2"},{"type":"function","name":"a"}]}`, "/tools/0")
	if a != b {
		t.Error("exclusion did not remove the first tool as sent")
	}
}

func TestNormalizeShowsCanonicalStrings(t *testing.T) {
	var v any
	if err := DecodeJSON([]byte(`{"input":[{"type":"function_call","arguments":"{\"b\":1,\"a\":2}"}],"tools":[{"name":"b"},{"name":"a"}]}`), &v); err != nil {
		t.Fatal(err)
	}
	got := Normalize("/v1/responses", v).(map[string]any)
	if args := got["input"].([]any)[0].(map[string]any)["arguments"]; args != `{"a":2,"b":1}` {
		t.Errorf("arguments = %v", args)
	}
	if first := got["tools"].([]any)[0].(map[string]any)["name"]; first != "a" {
		t.Errorf("tools not sorted: %v", got["tools"])
	}
	if same := Normalize("/v1/embeddings", "x"); same != "x" {
		t.Error("unsupported route changed the value")
	}
}

func TestCanonicalFormIsJCSWithExactNumbers(t *testing.T) {
	_, input, err := Key("/v1/responses", []byte(`{"z":"<&>é\/","a":[1.0,9007199254740993123456789]}`), nil)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"version":3,"api":"responses","body":{"a":[1.0,9007199254740993123456789],"z":"<&>é/"}}`
	if string(input) != want {
		t.Fatalf("matching input\n got %s\nwant %s", input, want)
	}
	for _, bad := range []string{`{"a":1,"a":2}`, `{"a":"\ud800"}`, `{"a":"\udc00"}`, "{\"a\":\"\xff\"}", `{"a":1} {}`} {
		if _, _, err := Key("/v1/responses", []byte(bad), nil); err == nil {
			t.Errorf("accepted %q", bad)
		}
	}
}
