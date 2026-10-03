package rehearsal

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"git.per.com/suhaib/go-common/pkg/genai/apishape"
	"git.per.com/suhaib/go-common/pkg/proto_models"
)

func TestEditedEmptyAndUnicodeAnswersReachGoCommon(t *testing.T) {
	for _, edited := range []string{"", "こんにちは 🌍 — café\nsecond line"} {
		name := "unicode"
		if edited == "" {
			name = "empty"
		}
		for _, tc := range protocolCases() {
			t.Run(name+"/"+tc.name, func(t *testing.T) {
				calls := 0
				upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					calls++
					w.Header().Set("Content-Type", "application/json")
					fmt.Fprint(w, textResponse(tc.api, "original"))
				}))
				db, proxyServer, client := integrationStack(t, tc, upstream.URL)
				defer db.Close()
				defer proxyServer.Close()
				defer client.Close()
				setMode(t, db, "record")
				if _, err := client.Text(context.Background(), tc.api, "boundary edit"); err != nil {
					t.Fatal(err)
				}
				recordings, err := db.Recordings(context.Background(), 1)
				if err != nil || len(recordings) != 1 {
					t.Fatalf("recordings = %d, %v", len(recordings), err)
				}
				body, _ := json.Marshal(map[string]any{"text": edited, "base_revision_id": recordings[0].ActiveRevisionID})
				resp, err := http.Post(proxyServer.URL+fmt.Sprintf("/api/recordings/%d/edit", recordings[0].ID), "application/json", bytes.NewReader(body))
				if err != nil {
					t.Fatal(err)
				}
				resp.Body.Close()
				if resp.StatusCode != http.StatusCreated {
					t.Fatalf("edit status = %d", resp.StatusCode)
				}
				setMode(t, db, "replay")
				upstream.Close()
				got, err := client.Text(context.Background(), tc.api, "boundary edit")
				if err != nil || got != edited {
					t.Fatalf("replay = %q, %v; want %q", got, err, edited)
				}
				if calls != 1 {
					t.Fatalf("upstream calls = %d, want 1", calls)
				}
			})
		}
	}
}

func TestProviderErrorsAreNotRetriedOrMadeReplayable(t *testing.T) {
	for _, tc := range protocolCases() {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				calls++
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusTooManyRequests)
				fmt.Fprint(w, `{"error":{"message":"busy","type":"rate_limit_error"}}`)
			}))
			db, proxyServer, client := integrationStack(t, tc, upstream.URL)
			defer db.Close()
			defer proxyServer.Close()
			defer client.Close()
			setMode(t, db, "record")
			if _, err := client.Text(context.Background(), tc.api, "fail once"); err == nil {
				t.Fatal("provider failure returned nil error")
			}
			if calls != 1 {
				t.Fatalf("upstream calls = %d, want no retry", calls)
			}
			recordings, err := db.Recordings(context.Background(), 1)
			if err != nil || len(recordings) != 0 {
				t.Fatalf("failed response recordings = %d, %v", len(recordings), err)
			}
			setMode(t, db, "replay")
			upstream.Close()
			if _, err := client.Text(context.Background(), tc.api, "fail once"); err == nil {
				t.Fatal("replay miss returned nil error")
			}
			if calls != 1 {
				t.Fatalf("replay miss contacted upstream: calls = %d", calls)
			}
		})
	}
}

func TestTwoToolResultBranchesReplayOffline(t *testing.T) {
	for _, tc := range protocolCases() {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				phase := calls % 2
				calls++
				w.Header().Set("Content-Type", "application/json")
				if phase == 0 {
					fmt.Fprint(w, toolResponse(tc.api))
					return
				}
				fmt.Fprint(w, textResponse(tc.api, "branch complete"))
			}))
			db, proxyServer, client := integrationStack(t, tc, upstream.URL)
			defer db.Close()
			defer proxyServer.Close()
			defer client.Close()
			setMode(t, db, "record")
			for _, result := range []string{"sunny", "rainy"} {
				if got, err := client.ToolRoundTripWithResult(context.Background(), tc.api, "weather branch", result); err != nil || got != "branch complete" {
					t.Fatalf("warm %q = %q, %v", result, got, err)
				}
			}
			if calls != 4 {
				t.Fatalf("warm calls = %d, want 4", calls)
			}
			setMode(t, db, "replay")
			upstream.Close()
			for _, result := range []string{"sunny", "rainy"} {
				if got, err := client.ToolRoundTripWithResult(context.Background(), tc.api, "weather branch", result); err != nil || got != "branch complete" {
					t.Fatalf("offline %q = %q, %v", result, got, err)
				}
			}
			if calls != 4 {
				t.Fatalf("offline contacted upstream: calls = %d", calls)
			}
		})
	}
}

func TestStreamingToolWorkflowReplaysOffline(t *testing.T) {
	for _, tc := range protocolCases() {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				phase := calls
				calls++
				w.Header().Set("Content-Type", "text/event-stream")
				if phase == 0 {
					fmt.Fprint(w, streamingToolResponse(tc.api))
					return
				}
				fmt.Fprint(w, streamResponse(tc.api, "stream branch complete"))
			}))
			db, proxyServer, client := integrationStack(t, tc, upstream.URL)
			defer db.Close()
			defer proxyServer.Close()
			defer client.Close()
			setMode(t, db, "record")
			if got, err := client.StreamingToolRoundTrip(context.Background(), tc.api, "stream weather", "sunny"); err != nil || got != "stream branch complete" {
				t.Fatalf("warm stream tool = %q, %v", got, err)
			}
			setMode(t, db, "replay")
			upstream.Close()
			if got, err := client.StreamingToolRoundTrip(context.Background(), tc.api, "stream weather", "sunny"); err != nil || got != "stream branch complete" {
				t.Fatalf("offline stream tool = %q, %v", got, err)
			}
			if calls != 2 {
				t.Fatalf("upstream calls = %d, want 2", calls)
			}
		})
	}
}

func streamingToolResponse(api proto_models.InferenceAPI) string {
	switch api {
	case apishape.ChatCompletions:
		return "data: {\"id\":\"chat_1\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"tool_calls\":[{\"index\":0,\"id\":\"call_1\",\"type\":\"function\",\"function\":{\"name\":\"lookup_weather\",\"arguments\":\"{\\\"city\\\":\\\"Paris\\\"}\"}}]}}]}\n\ndata: {\"id\":\"chat_1\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"tool_calls\"}]}\n\ndata: [DONE]\n\n"
	case apishape.AnthropicMessages:
		return "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_1\",\"type\":\"message\",\"role\":\"assistant\",\"model\":\"demo-model\",\"content\":[],\"usage\":{\"input_tokens\":2,\"output_tokens\":0}}}\n\nevent: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"tool_use\",\"id\":\"call_1\",\"name\":\"lookup_weather\",\"input\":{}}}\n\nevent: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"input_json_delta\",\"partial_json\":\"{\\\"city\\\":\\\"Paris\\\"}\"}}\n\nevent: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\nevent: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"tool_use\"},\"usage\":{\"output_tokens\":1}}\n\nevent: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"
	default:
		return "data: {\"type\":\"response.output_item.added\",\"sequence_number\":1,\"output_index\":0,\"item\":{\"type\":\"function_call\",\"id\":\"fc_1\",\"call_id\":\"call_1\",\"name\":\"lookup_weather\",\"arguments\":\"\",\"status\":\"in_progress\"}}\n\ndata: {\"type\":\"response.function_call_arguments.done\",\"sequence_number\":2,\"output_index\":0,\"item_id\":\"fc_1\",\"name\":\"lookup_weather\",\"arguments\":\"{\\\"city\\\":\\\"Paris\\\"}\"}\n\ndata: {\"type\":\"response.output_item.done\",\"sequence_number\":3,\"output_index\":0,\"item\":{\"type\":\"function_call\",\"id\":\"fc_1\",\"call_id\":\"call_1\",\"name\":\"lookup_weather\",\"arguments\":\"{\\\"city\\\":\\\"Paris\\\"}\",\"status\":\"completed\"}}\n\ndata: {\"type\":\"response.completed\",\"sequence_number\":4,\"response\":{\"id\":\"resp_1\",\"object\":\"response\",\"status\":\"completed\",\"model\":\"demo-model\",\"output\":[{\"type\":\"function_call\",\"id\":\"fc_1\",\"call_id\":\"call_1\",\"name\":\"lookup_weather\",\"arguments\":\"{\\\"city\\\":\\\"Paris\\\"}\",\"status\":\"completed\"}]}}\n\n"
	}
}
