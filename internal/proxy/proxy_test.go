package proxy

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/local/llm-replay-proxy/internal/model"
	"github.com/local/llm-replay-proxy/internal/store"
)

func TestSSEParserPreservesFramesAcrossArbitraryChunks(t *testing.T) {
	p := newSSEParser()
	var got []string
	for _, chunk := range []string{"event: message\r", "\ndata: {\"x\":1}\r\n\r", "\n:data-comment\n", "data: [DONE]\n\n"} {
		got = append(got, p.Push([]byte(chunk))...)
	}
	want := []string{
		"event: message\r\ndata: {\"x\":1}\r\n\r\n",
		":data-comment\ndata: [DONE]\n\n",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("frames = %#v, want %#v", got, want)
	}
	if p.Pending() {
		t.Fatal("complete stream reported pending bytes")
	}
}

func TestSSEParserReportsIncompleteTail(t *testing.T) {
	p := newSSEParser()
	if got := p.Push([]byte("data: partial\n")); len(got) != 0 {
		t.Fatalf("unexpected frames: %q", got)
	}
	if !p.Pending() {
		t.Fatal("partial event was not retained")
	}
}

func TestScaledDelayKeepsFractionalMillisecondsAndCapsOverflow(t *testing.T) {
	if got := scaledDelay(1, .5); got != 500*time.Microsecond {
		t.Fatalf("scaled delay = %v", got)
	}
	if got := scaledDelay(int64(^uint64(0)>>1), 1_000_000); got != time.Duration(1<<63-1) {
		t.Fatalf("overflow delay = %v", got)
	}
}

func TestReplayClockPreservesOrderFractionalTimingAndFlushes(t *testing.T) {
	clock := &recordingClock{}
	h := &handler{clock: clock}
	w := &flushRecorder{ResponseRecorder: httptest.NewRecorder()}
	entry := model.Entry{
		Recording: model.Recording{Streaming: true},
		Revision: model.Revision{Status: 200, Events: []model.Event{
			{Data: "one", OffsetMS: 10}, {Data: "two", OffsetMS: 11}, {Data: "three", OffsetMS: 15},
		}},
	}
	h.replay(w, httptest.NewRequest(http.MethodPost, "/v1/responses", nil), entry, model.Settings{FirstEventDelayMS: 25, DelayMultiplier: .5})
	if got, want := w.Body.String(), "onetwothree"; got != want {
		t.Fatalf("event order = %q, want %q", got, want)
	}
	if w.flushes != 3 {
		t.Fatalf("flushes = %d, want 3", w.flushes)
	}
	wantDelays := []time.Duration{25 * time.Millisecond, 500 * time.Microsecond, 2 * time.Millisecond}
	if !reflect.DeepEqual(clock.sleeps, wantDelays) {
		t.Fatalf("sleep durations = %v, want %v", clock.sleeps, wantDelays)
	}
}

func TestReplayCancellationStopsBeforeFollowingEvent(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	clock := &recordingClock{cancelOn: 2, cancel: cancel}
	h := &handler{clock: clock}
	w := &flushRecorder{ResponseRecorder: httptest.NewRecorder()}
	entry := model.Entry{Recording: model.Recording{Streaming: true}, Revision: model.Revision{Status: 200, Events: []model.Event{
		{Data: "first", OffsetMS: 1}, {Data: "second", OffsetMS: 2}, {Data: "third", OffsetMS: 3},
	}}}
	r := httptest.NewRequest(http.MethodPost, "/v1/responses", nil).WithContext(ctx)
	h.replay(w, r, entry, model.Settings{DelayMultiplier: 1})
	if got := w.Body.String(); got != "first" {
		t.Fatalf("body after cancellation = %q", got)
	}
	if w.flushes != 1 {
		t.Fatalf("flushes after cancellation = %d", w.flushes)
	}
}

func TestReplayCancellationPreservesDeliveredFirstEventMetric(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	clock := &recordingClock{cancelOn: 2, cancel: cancel}
	h := &handler{clock: clock}
	w := &flushRecorder{ResponseRecorder: httptest.NewRecorder()}
	entry := model.Entry{Recording: model.Recording{Streaming: true}, Revision: model.Revision{Status: 200, Events: []model.Event{{Data: "data: first\n\n", OffsetMS: 1}, {Data: "data: second\n\n", OffsetMS: 2}}}}
	first, err := h.replay(w, httptest.NewRequest(http.MethodPost, "/v1/responses", nil).WithContext(ctx), entry, model.Settings{DelayMultiplier: 1})
	if !errors.Is(err, context.Canceled) || first == nil {
		t.Fatalf("first=%v err=%v", first, err)
	}
	if got := w.Body.String(); got != "data: first\n\n" {
		t.Fatalf("body=%q", got)
	}
}

func TestRealClockCancellationIsPrompt(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	started := time.Now()
	if err := (realClock{}).Sleep(ctx, time.Hour); !errors.Is(err, context.Canceled) {
		t.Fatalf("Sleep error = %v", err)
	}
	if elapsed := time.Since(started); elapsed > 100*time.Millisecond {
		t.Fatalf("canceled sleep took %v", elapsed)
	}
}

type recordingClock struct {
	sleeps          []time.Duration
	calls, cancelOn int
	cancel          context.CancelFunc
}

func (c *recordingClock) Now() time.Time { return time.Unix(0, 0) }
func (c *recordingClock) Sleep(ctx context.Context, d time.Duration) error {
	c.calls++
	c.sleeps = append(c.sleeps, d)
	if c.calls == c.cancelOn && c.cancel != nil {
		c.cancel()
	}
	return ctx.Err()
}

type flushRecorder struct {
	*httptest.ResponseRecorder
	flushes int
}

func (w *flushRecorder) Flush() { w.flushes++; w.ResponseRecorder.Flush() }

func TestUpstreamIdentityIsStableAndExcludesCredentials(t *testing.T) {
	a := upstreamIdentity(Upstream{URL: "https://example/v1", Identity: "model=x", APIKey: "secret", Headers: map[string]string{
		"X-Tenant": "one", "Authorization": "Bearer also-secret", "X-Mode": "fast",
	}})
	b := upstreamIdentity(Upstream{URL: "https://example/v1", Identity: "model=x", APIKey: "different", Headers: map[string]string{
		"x-mode": "fast", "x-tenant": "one", "X-Api-Key": "hidden",
	}})
	if a != b {
		t.Fatalf("identity depends on map order, spelling, or credentials:\n%q\n%q", a, b)
	}
	if a == upstreamIdentity(Upstream{URL: "https://other/v1", Identity: "model=x", Headers: map[string]string{"x-mode": "fast", "x-tenant": "one"}}) {
		t.Fatal("URL did not affect identity")
	}
	crafted := upstreamIdentity(Upstream{URL: "https://example/v1", Identity: "model=x\nx-mode:fast", Headers: map[string]string{"x-tenant": "one"}})
	if crafted == a {
		t.Fatal("structured identity collided with delimiter-like identity text")
	}
}

func TestRecordThenReplayAllProtocolsAndTransports(t *testing.T) {
	tests := []struct {
		name, route, response string
		stream                bool
	}{
		{"chat body", "/v1/chat/completions", `{"id":"c1","choices":[{"message":{"role":"assistant","content":"hello"},"finish_reason":"stop"}]}`, false},
		{"responses body", "/v1/responses", `{"id":"r1","status":"completed","output":[{"id":"m1","type":"message","role":"assistant","content":[{"type":"output_text","text":"hello"}]}]}`, false},
		{"messages body", "/v1/messages", `{"id":"m1","type":"message","role":"assistant","content":[{"type":"text","text":"hello"}],"stop_reason":"end_turn"}`, false},
		{"chat stream", "/v1/chat/completions", "data: {\"id\":\"c1\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"hello\"},\"finish_reason\":null}]}\n\ndata: {\"id\":\"c1\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n", true},
		{"responses stream", "/v1/responses", "event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"item_id\":\"m1\",\"output_index\":0,\"content_index\":0,\"delta\":\"hello\"}\n\nevent: response.output_text.done\ndata: {\"type\":\"response.output_text.done\",\"item_id\":\"m1\",\"output_index\":0,\"content_index\":0,\"text\":\"hello\"}\n\nevent: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"r1\",\"status\":\"completed\",\"output\":[{\"id\":\"m1\",\"type\":\"message\",\"content\":[{\"type\":\"output_text\",\"text\":\"hello\"}]}]}}\n\n", true},
		{"messages stream", "/v1/messages", "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"m1\",\"type\":\"message\",\"role\":\"assistant\",\"content\":[],\"stop_reason\":null}}\n\nevent: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\nevent: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"hello\"}}\n\nevent: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\nevent: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"}}\n\nevent: message_stop\ndata: {\"type\":\"message_stop\"}\n\n", true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
				calls++
				if got, _ := io.ReadAll(r.Body); !bytes.Contains(got, []byte(`"model":"demo"`)) {
					t.Errorf("original request changed: %s", got)
				}
				return response(tc.response, map[bool]string{true: "text/event-stream", false: "application/json"}[tc.stream]), nil
			})}
			db := openTestStore(t, "record")
			h := New(db, Config{Client: client, Upstreams: map[string]Upstream{tc.route: {URL: "https://upstream.invalid", APIKey: "secret"}}})
			body := `{"model":"demo","stream":` + map[bool]string{true: "true", false: "false"}[tc.stream] + `}`
			first := perform(h, tc.route, body)
			if first.Code != 200 || first.Body.String() != tc.response {
				t.Fatalf("record response = %d %q", first.Code, first.Body.String())
			}
			setMode(t, db, "replay")
			second := perform(h, tc.route, body)
			if second.Code != 200 || second.Body.String() != tc.response {
				t.Fatalf("replay response = %d %q", second.Code, second.Body.String())
			}
			if calls != 1 {
				t.Fatalf("upstream calls = %d, want 1", calls)
			}
			if second.Header().Get("X-Request-Id") != "req-1" {
				t.Fatal("allowlisted response header not replayed")
			}
		})
	}
}

func TestIncompleteStreamIsDeliveredButNeverPublished(t *testing.T) {
	client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return response("data: {\"choices\":[{\"delta\":{\"content\":\"partial\"}}]}\n\n", "text/event-stream"), nil
	})}
	db := openTestStore(t, "record")
	h := New(db, Config{Client: client, Upstreams: map[string]Upstream{"/v1/chat/completions": {URL: "https://upstream.invalid"}}})
	recorded := perform(h, "/v1/chat/completions", `{"stream":true}`)
	if !strings.Contains(recorded.Body.String(), "partial") {
		t.Fatal("live partial response was not forwarded")
	}
	setMode(t, db, "replay")
	miss := perform(h, "/v1/chat/completions", `{"stream":true}`)
	if miss.Code != http.StatusNotFound {
		t.Fatalf("incomplete response became replayable: %d %s", miss.Code, miss.Body.String())
	}
}

func TestProtocolSSEErrorEventsAreDeliveredButNeverPublished(t *testing.T) {
	tests := []struct{ route, body string }{
		{"/v1/chat/completions", "data: {\"error\":{\"message\":\"failed\"}}\n\ndata: [DONE]\n\n"},
		{"/v1/responses", "event: error\ndata: {\"type\":\"error\",\"message\":\"failed\"}\n\n"},
		{"/v1/messages", "event: error\ndata: {\"type\":\"error\",\"error\":{\"message\":\"failed\"}}\n\n"},
	}
	for _, tc := range tests {
		t.Run(tc.route, func(t *testing.T) {
			client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) { return response(tc.body, "text/event-stream"), nil })}
			db := openTestStore(t, "record")
			h := New(db, Config{Client: client, Upstreams: map[string]Upstream{tc.route: {URL: "https://upstream.invalid"}}})
			live := perform(h, tc.route, `{"stream":true}`)
			if live.Code != 200 || live.Body.String() != tc.body {
				t.Fatalf("live error event = %d %q", live.Code, live.Body.String())
			}
			setMode(t, db, "replay")
			if replay := perform(h, tc.route, `{"stream":true}`); replay.Code != http.StatusNotFound {
				t.Fatalf("error event became replayable: %d %s", replay.Code, replay.Body.String())
			}
		})
	}
}

func openTestStore(t *testing.T, mode string) *store.Store {
	t.Helper()
	db, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	setMode(t, db, mode)
	return db
}
func setMode(t *testing.T, db *store.Store, mode string) {
	t.Helper()
	s, err := db.Settings(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	s.Mode = mode
	if err := db.SetSettings(context.Background(), s); err != nil {
		t.Fatal(err)
	}
}
func perform(h http.Handler, route, body string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(http.MethodPost, route, strings.NewReader(body))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func response(body, contentType string) *http.Response {
	h := make(http.Header)
	h.Set("Content-Type", contentType)
	h.Set("X-Request-Id", "req-1")
	return &http.Response{StatusCode: 200, Status: "200 OK", Header: h, Body: io.NopCloser(strings.NewReader(body))}
}
