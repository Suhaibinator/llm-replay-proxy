package rehearsal

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"git.per.com/suhaib/go-common/pkg/genai/apishape"
	"git.per.com/suhaib/go-common/pkg/proto_models"
)

func TestGoCommonConsumerUsesAllProxyRoutes(t *testing.T) {
	tests := []struct {
		name, path string
		api        proto_models.InferenceAPI
	}{
		{"chat", "/v1/chat/completions", apishape.ChatCompletions},
		{"responses", "/v1/responses", apishape.Responses},
		{"anthropic", "/v1/messages", apishape.AnthropicMessages},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != tc.path {
					t.Errorf("path = %q, want %q", r.URL.Path, tc.path)
				}
				var body map[string]any
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Fatal(err)
				}
				if body["model"] != "demo-model" {
					t.Errorf("model = %#v", body["model"])
				}
				w.Header().Set("Content-Type", "application/json")
				fmt.Fprint(w, textResponse(tc.api, "edited answer"))
			}))
			defer server.Close()
			client, err := New(context.Background(), server.URL, "demo-model")
			if err != nil {
				t.Fatal(err)
			}
			defer client.Close()
			got, err := client.Text(context.Background(), tc.api, "hello")
			if err != nil {
				t.Fatal(err)
			}
			if got != "edited answer" {
				t.Fatalf("answer = %q", got)
			}
		})
	}
}

func TestGoCommonConsumerToolRoundTrip(t *testing.T) {
	for _, tc := range []struct {
		name string
		api  proto_models.InferenceAPI
	}{
		{"chat", apishape.ChatCompletions},
		{"responses", apishape.Responses},
		{"anthropic", apishape.AnthropicMessages},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var mu sync.Mutex
			phase := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				mu.Lock()
				defer mu.Unlock()
				var body map[string]any
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Fatal(err)
				}
				w.Header().Set("Content-Type", "application/json")
				if phase == 0 {
					phase++
					fmt.Fprint(w, toolResponse(tc.api))
					return
				}
				raw, _ := json.Marshal(body)
				if !strings.Contains(string(raw), "sunny, 21 C") {
					t.Errorf("second request lacks tool output: %s", raw)
				}
				phase++
				fmt.Fprint(w, textResponse(tc.api, "It is sunny and 21 C."))
			}))
			defer server.Close()
			client, err := New(context.Background(), server.URL, "demo-model")
			if err != nil {
				t.Fatal(err)
			}
			defer client.Close()
			got, err := client.ToolRoundTrip(context.Background(), tc.api, "weather?")
			if err != nil {
				t.Fatal(err)
			}
			if got != "It is sunny and 21 C." {
				t.Fatalf("answer = %q", got)
			}
			if phase != 2 {
				t.Fatalf("requests = %d, want 2", phase)
			}
		})
	}
}

func textResponse(api proto_models.InferenceAPI, text string) string {
	encoded, _ := json.Marshal(text)
	switch api {
	case apishape.ChatCompletions:
		return fmt.Sprintf(`{"id":"chat_1","choices":[{"index":0,"message":{"role":"assistant","content":%s},"finish_reason":"stop"}]}`, encoded)
	case apishape.AnthropicMessages:
		return fmt.Sprintf(`{"id":"msg_1","type":"message","role":"assistant","model":"demo-model","content":[{"type":"text","text":%s}],"stop_reason":"end_turn","usage":{"input_tokens":2,"output_tokens":1}}`, encoded)
	default:
		return fmt.Sprintf(`{"id":"resp_1","object":"response","status":"completed","model":"demo-model","output":[{"type":"message","id":"msg_1","role":"assistant","status":"completed","content":[{"type":"output_text","text":%s,"annotations":[]}]}]}`, encoded)
	}
}

func toolResponse(api proto_models.InferenceAPI) string {
	switch api {
	case apishape.ChatCompletions:
		return `{"id":"chat_1","choices":[{"index":0,"message":{"role":"assistant","tool_calls":[{"id":"call_1","type":"function","function":{"name":"lookup_weather","arguments":"{\"city\":\"Paris\"}"}}]},"finish_reason":"tool_calls"}]}`
	case apishape.AnthropicMessages:
		return `{"id":"msg_1","type":"message","role":"assistant","model":"demo-model","content":[{"type":"tool_use","id":"call_1","name":"lookup_weather","input":{"city":"Paris"}}],"stop_reason":"tool_use","usage":{"input_tokens":2,"output_tokens":1}}`
	default:
		return `{"id":"resp_1","object":"response","status":"completed","model":"demo-model","output":[{"type":"function_call","id":"fc_1","call_id":"call_1","name":"lookup_weather","arguments":"{\"city\":\"Paris\"}","status":"completed"}]}`
	}
}
