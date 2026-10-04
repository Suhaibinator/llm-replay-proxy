package proxy

import (
	"compress/gzip"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"syscall"
	"testing"

	"github.com/local/llm-replay-proxy/internal/model"
	"github.com/local/llm-replay-proxy/internal/store"
)

func latestHistory(t *testing.T, db *store.Store) model.History {
	t.Helper()
	items, err := db.History(context.Background(), activeCollectionID(t, db), 1)
	if err != nil || len(items) == 0 {
		t.Fatalf("history = %v, %v", items, err)
	}
	return items[0]
}

func TestClientCancelBeforeUpstreamHeadersIsInterrupted(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		cancel()
		<-r.Context().Done()
		return nil, r.Context().Err()
	})}
	db := openTestStore(t, "record")
	h := New(db, Config{Client: client, Upstreams: map[string]Upstream{"/v1/responses": {URL: "https://upstream.invalid"}}})
	w := performWithContext(h, ctx, "/v1/responses", `{"input":"x"}`)
	if w.Body.Len() != 0 {
		t.Fatalf("wrote %d %q to a departed client", w.Code, w.Body.String())
	}
	if got := latestHistory(t, db); got.Outcome != "interrupted" {
		t.Fatalf("history = %+v", got)
	}
}

// brokenPipeWriter fails every body write as a closed client connection does,
// without the request context having been canceled yet.
type brokenPipeWriter struct{ header http.Header }

func (w *brokenPipeWriter) Header() http.Header       { return w.header }
func (w *brokenPipeWriter) WriteHeader(int)           {}
func (w *brokenPipeWriter) Write([]byte) (int, error) { return 0, syscall.EPIPE }

func TestReplayWriteFailureIsInterrupted(t *testing.T) {
	for _, stream := range []bool{false, true} {
		body, upstreamBody, contentType := `{"model":"m"}`, matrixResponsesBody("r1", "hi"), "application/json"
		if stream {
			body, contentType = `{"model":"m","stream":true}`, "text/event-stream"
			upstreamBody = "event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"item_id\":\"m1\",\"output_index\":0,\"content_index\":0,\"delta\":\"hi\"}\n\nevent: response.output_text.done\ndata: {\"type\":\"response.output_text.done\",\"item_id\":\"m1\",\"output_index\":0,\"content_index\":0,\"text\":\"hi\"}\n\nevent: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"r1\",\"status\":\"completed\",\"output\":[{\"id\":\"m1\",\"type\":\"message\",\"content\":[{\"type\":\"output_text\",\"text\":\"hi\"}]}]}}\n\n"
		}
		client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) { return response(upstreamBody, contentType), nil })}
		db := openTestStore(t, "record")
		h := New(db, Config{Client: client, Upstreams: map[string]Upstream{"/v1/responses": {URL: "https://upstream.invalid"}}})
		if got := perform(h, "/v1/responses", body); got.Code != 200 {
			t.Fatalf("record = %d %s", got.Code, got.Body.String())
		}
		setMode(t, db, "replay")
		h.ServeHTTP(&brokenPipeWriter{header: http.Header{}}, httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(body)))
		if got := latestHistory(t, db); got.Outcome != "interrupted" || got.Source != "replay" {
			t.Fatalf("stream=%v history = %+v", stream, got)
		}
	}
}

func TestLiveWriteFailureIsInterrupted(t *testing.T) {
	client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return response(matrixResponsesBody("r1", "hi"), "application/json"), nil
	})}
	db := openTestStore(t, "record")
	h := New(db, Config{Client: client, Upstreams: map[string]Upstream{"/v1/responses": {URL: "https://upstream.invalid"}}})
	h.ServeHTTP(&brokenPipeWriter{header: http.Header{}}, httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"m"}`)))
	if got := latestHistory(t, db); got.Outcome != "interrupted" {
		t.Fatalf("history = %+v", got)
	}
}

func TestStreamingUpstreamErrorStatusLeadsHistoryDetail(t *testing.T) {
	const errorBody = `{"error":{"message":"slow down"}}`
	client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return matrixResponse(http.StatusTooManyRequests, errorBody, "application/json"), nil
	})}
	db := openTestStore(t, "record")
	h := New(db, Config{Client: client, Upstreams: map[string]Upstream{"/v1/chat/completions": {URL: "https://upstream.invalid"}}})
	live := perform(h, "/v1/chat/completions", `{"stream":true}`)
	if live.Code != http.StatusTooManyRequests || live.Body.String() != errorBody {
		t.Fatalf("live = %d %q", live.Code, live.Body.String())
	}
	if got := latestHistory(t, db); got.Outcome != "error" || got.Detail != "429 Too Many Requests" {
		t.Fatalf("history = %+v", got)
	}
}

func TestNullStreamIsNonStreaming(t *testing.T) {
	calls := 0
	client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		calls++
		return response(matrixResponsesBody("r1", "hi"), "application/json"), nil
	})}
	db := openTestStore(t, "record")
	h := New(db, Config{Client: client, Upstreams: map[string]Upstream{"/v1/responses": {URL: "https://upstream.invalid"}}})
	if got := perform(h, "/v1/responses", `{"model":"m","stream":null}`); got.Code != 200 {
		t.Fatalf("record = %d %s", got.Code, got.Body.String())
	}
	setMode(t, db, "replay")
	if got := perform(h, "/v1/responses", `{"model":"m","stream":null}`); got.Code != 200 || calls != 1 {
		t.Fatalf("replay = %d %s (calls %d)", got.Code, got.Body.String(), calls)
	}
}

func TestCompressedUpstreamIsDecoded(t *testing.T) {
	payload := matrixResponsesBody("r1", "compressed")
	// The server compresses regardless of what was requested.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Content-Encoding", "gzip")
		zw := gzip.NewWriter(w)
		_, _ = io.WriteString(zw, payload)
		_ = zw.Close()
	}))
	defer server.Close()
	for name, tc := range map[string]struct {
		client  *http.Client
		headers map[string]string
	}{
		"configured accept-encoding": {server.Client(), map[string]string{"Accept-Encoding": "gzip"}},
		"transport compression off":  {&http.Client{Transport: &http.Transport{DisableCompression: true}}, nil},
	} {
		t.Run(name, func(t *testing.T) {
			db := openTestStore(t, "record")
			h := New(db, Config{Client: tc.client, Upstreams: map[string]Upstream{"/v1/responses": {URL: server.URL, Headers: tc.headers}}})
			live := perform(h, "/v1/responses", `{"model":"m"}`)
			if live.Code != 200 || live.Body.String() != payload {
				t.Fatalf("live = %d %q", live.Code, live.Body.String())
			}
			if got := latestHistory(t, db); got.Outcome != "recorded" {
				t.Fatalf("history = %+v", got)
			}
			setMode(t, db, "replay")
			if got := perform(h, "/v1/responses", `{"model":"m"}`); got.Code != 200 || got.Body.String() != payload {
				t.Fatalf("replay = %d %q", got.Code, got.Body.String())
			}
		})
	}
}
