package protocol

import (
	"strings"
	"testing"

	"github.com/local/llm-replay-proxy/internal/model"
)

func TestStreamingChatToolArguments(t *testing.T) {
	base := []model.Event{
		event(0, "", `{"id":"c","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"weather","arguments":"{\"city\":"}}]},"finish_reason":null}]}`),
		event(1, "", `{"id":"c","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"\"Paris\"}"}}]},"finish_reason":null}]}`),
		event(2, "", `{"id":"c","choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`), event(3, "", `[DONE]`),
	}
	rev := model.Revision{Status: 200, Events: base}
	if err := Validate(ChatRoute, true, rev); err != nil {
		t.Fatalf("complete tool stream rejected: %v", err)
	}
	if _, reason := Text(ChatRoute, true, rev); !strings.Contains(reason, "tool") {
		t.Fatalf("plain editor reason=%q", reason)
	}
	bad := rev
	bad.Events = append([]model.Event(nil), base...)
	bad.Events[1] = event(1, "", `{"id":"c","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"Paris"}}]},"finish_reason":null}]}`)
	if err := Validate(ChatRoute, true, bad); err == nil || !strings.Contains(err.Error(), "arguments") {
		t.Fatalf("malformed arguments accepted: %v", err)
	}
}

func TestChatRefusalIsReplayableButNotPlainEditable(t *testing.T) {
	rev := model.Revision{Status: 200, Body: `{"id":"c","choices":[{"index":0,"message":{"role":"assistant","content":null,"refusal":"cannot comply"},"finish_reason":"stop"}]}`}
	if err := Validate(ChatRoute, false, rev); err != nil {
		t.Fatal(err)
	}
	if _, reason := Text(ChatRoute, false, rev); !strings.Contains(reason, "refusal") {
		t.Fatalf("reason=%q", reason)
	}
}

func TestStreamingUnknownChatToolKindStaysOpaque(t *testing.T) {
	rev := model.Revision{Status: 200, Events: []model.Event{
		event(0, "", `{"id":"c","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"custom_1","type":"provider_custom","payload":{"value":1}}]},"finish_reason":null}]}`),
		event(1, "", `{"id":"c","choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`), event(2, "", `[DONE]`),
	}}
	if err := Validate(ChatRoute, true, rev); err != nil {
		t.Fatalf("opaque tool rejected: %v", err)
	}
	if _, reason := Text(ChatRoute, true, rev); !strings.Contains(reason, "tool") {
		t.Fatalf("plain editor reason=%q", reason)
	}
}

func TestNonstreamUnknownChatToolKindStaysOpaque(t *testing.T) {
	rev := model.Revision{Status: 200, Body: `{"choices":[{"finish_reason":"tool_calls","message":{"content":null,"tool_calls":[{"id":"c","type":"provider_custom","custom":{"input":"hello"}}]}}]}`}
	if err := Validate(ChatRoute, false, rev); err != nil {
		t.Fatal(err)
	}
	if _, reason := Text(ChatRoute, false, rev); !strings.Contains(reason, "tool") {
		t.Fatalf("reason=%q", reason)
	}
}
