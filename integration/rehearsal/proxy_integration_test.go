package rehearsal

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"testing"

	"git.per.com/suhaib/go-common/pkg/genai/apishape"
	"git.per.com/suhaib/go-common/pkg/proto_models"
	"github.com/local/llm-replay-proxy/internal/admin"
	"github.com/local/llm-replay-proxy/internal/model"
	"github.com/local/llm-replay-proxy/internal/proxy"
	"github.com/local/llm-replay-proxy/internal/store"
)

func TestRecordEditAndOfflineReplayThroughGoCommon(t *testing.T) {
	for _, tc := range protocolCases() {
		t.Run(tc.name, func(t *testing.T) {
			var mu sync.Mutex
			upstreamCalls := 0
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				mu.Lock()
				upstreamCalls++
				mu.Unlock()
				w.Header().Set("Content-Type", "application/json")
				fmt.Fprint(w, textResponse(tc.api, "recorded answer"))
			}))
			defer upstream.Close()

			db, proxyServer, client := integrationStack(t, tc, upstream.URL)
			defer db.Close()
			defer proxyServer.Close()
			defer client.Close()
			setMode(t, db, "record")
			if got, err := client.Text(context.Background(), tc.api, "edit me"); err != nil || got != "recorded answer" {
				t.Fatalf("record result = %q, %v", got, err)
			}
			recordings, err := db.Recordings(context.Background(), 1)
			if err != nil || len(recordings) != 1 {
				t.Fatalf("recordings = %d, %v", len(recordings), err)
			}
			body, _ := json.Marshal(map[string]any{"text": "edited answer", "base_revision_id": recordings[0].ActiveRevisionID})
			req, _ := http.NewRequest(http.MethodPost, proxyServer.URL+fmt.Sprintf("/api/recordings/%d/edit", recordings[0].ID), bytes.NewReader(body))
			req.Header.Set("Content-Type", "application/json")
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			resp.Body.Close()
			if resp.StatusCode != http.StatusCreated {
				t.Fatalf("edit status = %d", resp.StatusCode)
			}
			setMode(t, db, "replay")
			upstream.Close()
			if got, err := client.Text(context.Background(), tc.api, "edit me"); err != nil || got != "edited answer" {
				t.Fatalf("offline edited replay = %q, %v", got, err)
			}
			mu.Lock()
			gotCalls := upstreamCalls
			mu.Unlock()
			if gotCalls != 1 {
				t.Fatalf("upstream calls = %d, want 1", gotCalls)
			}
		})
	}
}

func TestRecordEditAndOfflineReplayStreamingThroughGoCommon(t *testing.T) {
	for _, tc := range protocolCases() {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				calls++
				w.Header().Set("Content-Type", "text/event-stream")
				fmt.Fprint(w, streamResponse(tc.api, "recorded stream"))
			}))
			db, proxyServer, client := integrationStack(t, tc, upstream.URL)
			defer db.Close()
			defer proxyServer.Close()
			defer client.Close()
			setMode(t, db, "record")
			if got, err := client.StreamingText(context.Background(), tc.api, "stream edit"); err != nil || got != "recorded stream" {
				t.Fatalf("record stream = %q, %v", got, err)
			}
			recordings, err := db.Recordings(context.Background(), 1)
			if err != nil || len(recordings) != 1 {
				t.Fatalf("recordings = %d, %v", len(recordings), err)
			}
			body, _ := json.Marshal(map[string]any{"text": "edited stream", "base_revision_id": recordings[0].ActiveRevisionID})
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
			if got, err := client.StreamingText(context.Background(), tc.api, "stream edit"); err != nil || got != "edited stream" {
				t.Fatalf("offline edited stream = %q, %v", got, err)
			}
			if calls != 1 {
				t.Fatalf("upstream calls = %d, want 1", calls)
			}
		})
	}
}

func TestMultiStepToolWorkflowReplaysWithUpstreamOffline(t *testing.T) {
	for _, tc := range protocolCases() {
		t.Run(tc.name, func(t *testing.T) {
			var mu sync.Mutex
			calls := 0
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				mu.Lock()
				phase := calls
				calls++
				mu.Unlock()
				w.Header().Set("Content-Type", "application/json")
				if phase == 0 {
					fmt.Fprint(w, toolResponse(tc.api))
					return
				}
				fmt.Fprint(w, textResponse(tc.api, "It is sunny and 21 C."))
			}))
			db, proxyServer, client := integrationStack(t, tc, upstream.URL)
			defer db.Close()
			defer proxyServer.Close()
			defer client.Close()
			setMode(t, db, "record")
			if got, err := client.ToolRoundTrip(context.Background(), tc.api, "weather?"); err != nil || got != "It is sunny and 21 C." {
				t.Fatalf("record workflow = %q, %v", got, err)
			}
			setMode(t, db, "replay")
			upstream.Close()
			if got, err := client.ToolRoundTrip(context.Background(), tc.api, "weather?"); err != nil || got != "It is sunny and 21 C." {
				t.Fatalf("offline workflow = %q, %v", got, err)
			}
			mu.Lock()
			gotCalls := calls
			mu.Unlock()
			if gotCalls != 2 {
				t.Fatalf("upstream calls = %d, want 2", gotCalls)
			}
		})
	}
}

type protocolCase struct {
	name, route string
	api         proto_models.InferenceAPI
}

func protocolCases() []protocolCase {
	return []protocolCase{
		{"chat", "/v1/chat/completions", apishape.ChatCompletions},
		{"responses", "/v1/responses", apishape.Responses},
		{"anthropic", "/v1/messages", apishape.AnthropicMessages},
	}
}

func integrationStack(t *testing.T, tc protocolCase, upstreamURL string) (*store.Store, *httptest.Server, *Client) {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "replay.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	mux.Handle(tc.route, proxy.New(db, proxy.Config{Upstreams: map[string]proxy.Upstream{
		tc.route: {URL: upstreamURL, Identity: "integration-upstream"},
	}}))
	mux.Handle("/api/", admin.New(db))
	server := httptest.NewServer(mux)
	client, err := New(context.Background(), server.URL, "demo-model")
	if err != nil {
		server.Close()
		db.Close()
		t.Fatal(err)
	}
	return db, server, client
}

func setMode(t *testing.T, db *store.Store, mode string) {
	t.Helper()
	if err := db.SetSettings(context.Background(), model.Settings{Mode: mode, ActiveCollectionID: 1, DelayMultiplier: 1}); err != nil {
		t.Fatal(err)
	}
}

func streamResponse(api proto_models.InferenceAPI, text string) string {
	encoded, _ := json.Marshal(text)
	switch api {
	case apishape.ChatCompletions:
		return fmt.Sprintf("data: {\"id\":\"chat_1\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":%s}}]}\n\ndata: {\"id\":\"chat_1\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n", encoded)
	case apishape.AnthropicMessages:
		return fmt.Sprintf("event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_1\",\"type\":\"message\",\"role\":\"assistant\",\"model\":\"demo-model\",\"content\":[],\"usage\":{\"input_tokens\":2,\"output_tokens\":0}}}\n\nevent: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\nevent: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":%s}}\n\nevent: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\nevent: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":1}}\n\nevent: message_stop\ndata: {\"type\":\"message_stop\"}\n\n", encoded)
	default:
		final := textResponse(api, text)
		return fmt.Sprintf("event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"delta\":%s,\"item_id\":\"msg_1\",\"output_index\":0,\"content_index\":0}\n\nevent: response.completed\ndata: {\"type\":\"response.completed\",\"response\":%s}\n\n", encoded, final)
	}
}
