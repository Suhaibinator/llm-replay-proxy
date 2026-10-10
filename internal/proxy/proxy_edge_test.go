package proxy

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/local/llm-replay-proxy/internal/model"
)

func TestStateGuardSurvivesActiveCollectionSwitch(t *testing.T) {
	db := openTestStore(t, "record")
	first, err := db.Settings(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		calls++
		return response(`{"id":"state_from_first","status":"completed","output":[{"type":"message","content":[{"type":"output_text","text":"first"}]}]}`, "application/json"), nil
	})}
	h := New(db, Config{Client: client, DefaultProvider: "test", Providers: map[string]map[string]Upstream{"test": {"/v1/responses": {URL: "https://upstream.invalid"}}}})
	if got := perform(h, "/v1/responses", `{"input":"start"}`); got.Code != 200 {
		t.Fatalf("warm response: %d %s", got.Code, got.Body.String())
	}
	second, err := db.CreateCollection(context.Background(), "Second", nil)
	if err != nil {
		t.Fatal(err)
	}
	first.ActiveCollectionID, first.Mode = second.ID, "auto"
	if err := db.SetSettings(context.Background(), first); err != nil {
		t.Fatal(err)
	}

	got := perform(h, "/v1/responses", `{"previous_response_id":"state_from_first","input":"continue"}`)
	if got.Code != http.StatusConflict || !strings.Contains(got.Body.String(), "recorded_state_unavailable") {
		t.Fatalf("cross-collection state request = %d %s", got.Code, got.Body.String())
	}
	if calls != 1 {
		t.Fatalf("unsafe request reached upstream; calls = %d", calls)
	}
}

func TestTransportCannotRetryOrFollowRedirects(t *testing.T) {
	for _, tc := range []struct {
		name      string
		roundTrip func(*http.Request) (*http.Response, error)
	}{
		{"transport error", func(r *http.Request) (*http.Response, error) {
			if r.GetBody != nil {
				t.Error("request body is replayable by transport")
			}
			return nil, errors.New("connection broke")
		}},
		{"redirect", func(r *http.Request) (*http.Response, error) {
			if r.GetBody != nil {
				t.Error("request body is replayable by transport")
			}
			resp := matrixResponse(http.StatusTemporaryRedirect, "redirect", "text/plain")
			resp.Header.Set("Location", "https://other.invalid/v1/responses")
			return resp, nil
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var calls atomic.Int32
			client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) { calls.Add(1); return tc.roundTrip(r) })}
			db := openTestStore(t, "record")
			h := New(db, Config{Client: client, DefaultProvider: "test", Providers: map[string]map[string]Upstream{"test": {"/v1/responses": {URL: "https://upstream.invalid", Headers: map[string]string{"Idempotency-Key": "configured"}}}}})
			_ = perform(h, "/v1/responses", `{"input":"x"}`)
			if calls.Load() != 1 {
				t.Fatalf("round trips = %d, want exactly one", calls.Load())
			}
		})
	}
}

func TestValidTerminalFrameWithTransportReadErrorIsNotPublished(t *testing.T) {
	payload := "data: {\"id\":\"c1\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"ok\"},\"finish_reason\":null}]}\n\ndata: {\"id\":\"c1\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n"
	client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		resp := response("", "text/event-stream")
		resp.Body = io.NopCloser(&oneReadError{data: []byte(payload), err: io.ErrUnexpectedEOF})
		return resp, nil
	})}
	db := openTestStore(t, "record")
	h := New(db, Config{Client: client, DefaultProvider: "test", Providers: map[string]map[string]Upstream{"test": {"/v1/chat/completions": {URL: "https://upstream.invalid"}}}})
	live := perform(h, "/v1/chat/completions", `{"stream":true}`)
	if live.Body.String() != payload {
		t.Fatalf("live bytes changed: %q", live.Body.String())
	}
	setMode(t, db, "replay")
	if got := perform(h, "/v1/chat/completions", `{"stream":true}`); got.Code != http.StatusNotFound {
		t.Fatalf("transport-error stream published: %d %s", got.Code, got.Body.String())
	}
}

func TestHTTPStreamFramesSplitAtEveryByteRecordExactly(t *testing.T) {
	payload := "\xef\xbb\xbf: keepalive " + string([]byte{0xff}) + "\r\n\r\n" + "data: {\"id\":\"c1\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"ok\"},\"finish_reason\":null}]}\r\n\r\ndata: {\"id\":\"c1\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\r\n\r\ndata: [DONE]\r\n\r\n"
	client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		resp := response("", "text/event-stream; charset=utf-8")
		resp.Body = io.NopCloser(&byteReader{data: []byte(payload)})
		return resp, nil
	})}
	db := openTestStore(t, "record")
	h := New(db, Config{Client: client, DefaultProvider: "test", Providers: map[string]map[string]Upstream{"test": {"/v1/chat/completions": {URL: "https://upstream.invalid"}}}})
	if got := perform(h, "/v1/chat/completions", `{"stream":true}`); got.Body.String() != payload {
		t.Fatal("live bytes changed")
	}
	setMode(t, db, "replay")
	got := perform(h, "/v1/chat/completions", `{"stream":true}`)
	if got.Code != 200 || got.Body.String() != payload {
		t.Fatalf("replay = %d %q", got.Code, got.Body.String())
	}
}

func TestCancellationAtTerminalReadPreventsPublication(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	body := `{"id":"r1","status":"completed","output":[{"type":"message","content":[{"type":"output_text","text":"done"}]}]}`
	client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		resp := response("", "application/json")
		resp.Body = io.NopCloser(&cancelReader{data: []byte(body), cancel: cancel})
		return resp, nil
	})}
	db := openTestStore(t, "record")
	h := New(db, Config{Client: client, DefaultProvider: "test", Providers: map[string]map[string]Upstream{"test": {"/v1/responses": {URL: "https://upstream.invalid"}}}})
	r := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"input":"x"}`)).WithContext(ctx)
	h.ServeHTTP(httptest.NewRecorder(), r)
	setMode(t, db, "replay")
	if got := perform(h, "/v1/responses", `{"input":"x"}`); got.Code != http.StatusNotFound {
		t.Fatalf("canceled request published: %d %s", got.Code, got.Body.String())
	}
	history, err := db.History(context.Background(), activeCollectionID(t, db), 10)
	if err != nil {
		t.Fatal(err)
	}
	interrupted := false
	for _, item := range history {
		interrupted = interrupted || item.Outcome == "interrupted"
	}
	if !interrupted {
		t.Fatalf("history = %+v", history)
	}
}

type oneReadError struct {
	data []byte
	err  error
	done bool
}

func (r *oneReadError) Read(p []byte) (int, error) {
	if r.done {
		return 0, io.EOF
	}
	r.done = true
	n := copy(p, r.data)
	return n, r.err
}

type cancelReader struct {
	data   []byte
	cancel context.CancelFunc
	done   bool
}

type byteReader struct {
	data  []byte
	index int
}

func (r *byteReader) Read(p []byte) (int, error) {
	if r.index == len(r.data) {
		return 0, io.EOF
	}
	p[0] = r.data[r.index]
	r.index++
	return 1, nil
}

func (r *cancelReader) Read(p []byte) (int, error) {
	if r.done {
		return 0, io.EOF
	}
	r.done = true
	n := copy(p, r.data)
	r.cancel()
	return n, io.EOF
}

func activeCollectionID(t *testing.T, db interface {
	Settings(context.Context) (model.Settings, error)
}) int64 {
	t.Helper()
	s, err := db.Settings(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return s.ActiveCollectionID
}
