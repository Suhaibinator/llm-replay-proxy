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

func TestProviderHeaderSelectsUpstreamButNotMatching(t *testing.T) {
	const chat = "/v1/chat/completions"
	reply := `{"id":"c1","choices":[{"message":{"role":"assistant","content":"hello"},"finish_reason":"stop"}]}`
	var hosts, keys []string
	client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		hosts, keys = append(hosts, r.URL.Host), append(keys, r.Header.Get("Authorization"))
		if r.Header.Get(ProviderHeader) != "" || r.Header.Get("X-Tier") != map[string]string{"openai.invalid": "", "router.invalid": "fast"}[r.URL.Host] {
			t.Errorf("unexpected upstream headers %v", r.Header)
		}
		return response(reply, "application/json"), nil
	})}
	db := openTestStore(t, "auto")
	h := New(db, Config{Client: client, DefaultProvider: "openai", Providers: map[string]map[string]Upstream{
		"openai":     {chat: {URL: "https://openai.invalid/v1/chat/completions", APIKey: "openai-key"}, "/v1/responses": {URL: "https://openai.invalid/v1/responses"}},
		"openrouter": {chat: {URL: "https://router.invalid/api/v1/chat/completions", APIKey: "router-key", Headers: map[string]string{"x-tier": "fast"}}},
	}})
	send := func(provider, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodPost, chat, strings.NewReader(body))
		if provider != "" {
			r.Header.Set(ProviderHeader, provider)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}

	if w := send("", `{"model":"a","temperature":0}`); w.Code != 200 {
		t.Fatalf("default provider: %d %s", w.Code, w.Body.String())
	}
	if w := send(" OpenRouter ", `{"model":"b","temperature":0}`); w.Code != 200 {
		t.Fatalf("named provider: %d %s", w.Code, w.Body.String())
	}
	if !reflect.DeepEqual(hosts, []string{"openai.invalid", "router.invalid"}) || !reflect.DeepEqual(keys, []string{"Bearer openai-key", "Bearer router-key"}) {
		t.Fatalf("upstreams = %v %v", hosts, keys)
	}
	// The provider is not part of the key: a recording made through one
	// provider replays for every other.
	if w := send("openrouter", `{"model":"a","temperature":0}`); w.Code != 200 || w.Body.String() != reply {
		t.Fatalf("cross-provider replay: %d %s", w.Code, w.Body.String())
	}
	if len(hosts) != 2 {
		t.Fatalf("cross-provider hit went upstream: %v", hosts)
	}
	// Any other parameter still distinguishes requests.
	if w := send("openrouter", `{"model":"a","temperature":1}`); w.Code != 200 || len(hosts) != 3 {
		t.Fatalf("changed temperature did not miss: %d %v", w.Code, hosts)
	}
	history, err := db.History(context.Background(), activeCollectionID(t, db), 10)
	if err != nil {
		t.Fatal(err)
	}
	var providers []string
	for _, item := range history {
		providers = append(providers, item.Provider+"/"+item.Outcome)
	}
	if want := []string{"openrouter/recorded", "openrouter/hit", "openrouter/recorded", "openai/recorded"}; !reflect.DeepEqual(providers, want) {
		t.Fatalf("history providers = %v, want %v", providers, want)
	}

	// An explicitly named provider must exist and serve the route, even when
	// replay alone could answer.
	setMode(t, db, "replay")
	for _, tc := range []struct{ provider, route, code string }{
		{"missing", chat, "unknown_provider"},
		{"openrouter", "/v1/responses", "provider_route_not_configured"},
	} {
		r := httptest.NewRequest(http.MethodPost, tc.route, strings.NewReader(`{"model":"a","temperature":0}`))
		r.Header.Set(ProviderHeader, tc.provider)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), tc.code) {
			t.Fatalf("%s %s: %d %s", tc.provider, tc.route, w.Code, w.Body.String())
		}
	}
}

func TestReplayNeedsNoUpstreamButForwardingDoes(t *testing.T) {
	const chat = "/v1/chat/completions"
	reply := `{"id":"c1","choices":[{"message":{"role":"assistant","content":"hello"},"finish_reason":"stop"}]}`
	client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) { return response(reply, "application/json"), nil })}
	db := openTestStore(t, "record")
	recorder := New(db, Config{Client: client, DefaultProvider: "test", Providers: map[string]map[string]Upstream{"test": {chat: {URL: "https://upstream.invalid"}}}})
	if w := perform(recorder, chat, `{"model":"a"}`); w.Code != 200 {
		t.Fatalf("record: %d %s", w.Code, w.Body.String())
	}
	offline := New(db, Config{Client: client})
	setMode(t, db, "auto")
	if w := perform(offline, chat, `{"model":"a"}`); w.Code != 200 || w.Body.String() != reply {
		t.Fatalf("offline replay: %d %s", w.Code, w.Body.String())
	}
	if w := perform(offline, chat, `{"model":"b"}`); w.Code != http.StatusServiceUnavailable || !strings.Contains(w.Body.String(), "upstream_not_configured") {
		t.Fatalf("offline miss: %d %s", w.Code, w.Body.String())
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
			h := New(db, Config{Client: client, DefaultProvider: "test", Providers: map[string]map[string]Upstream{"test": {tc.route: {URL: "https://upstream.invalid", APIKey: "secret"}}}})
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
	h := New(db, Config{Client: client, DefaultProvider: "test", Providers: map[string]map[string]Upstream{"test": {"/v1/chat/completions": {URL: "https://upstream.invalid"}}}})
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
			h := New(db, Config{Client: client, DefaultProvider: "test", Providers: map[string]map[string]Upstream{"test": {tc.route: {URL: "https://upstream.invalid"}}}})
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
