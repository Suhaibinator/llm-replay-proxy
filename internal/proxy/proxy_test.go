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
	"sync"
	"sync/atomic"
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

func TestTokensPerSecondPacingStreamsOutputAtTheTargetRate(t *testing.T) {
	events := []model.Event{
		{Data: ": OPENROUTER PROCESSING\n\n", OffsetMS: 900},
		{Data: ": OPENROUTER PROCESSING\n\n", OffsetMS: 1900},
		{Data: `data: {"type":"message_start","message":{"model":"m","usage":{"input_tokens":10}}}` + "\n\n", OffsetMS: 2000},
		{Data: `data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Hello"}}` + "\n\n", OffsetMS: 2500},
		{Data: `data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":150}}` + "\n\n", OffsetMS: 3500},
	}
	settings := model.Settings{FirstEventDelayMS: 25, Pacing: model.PacingTokensPerSecond, DelayMultiplier: 7, TargetTokensPerSecond: 50}
	for _, tc := range []struct {
		name   string
		events []model.Event
		want   []time.Duration
	}{
		// Recorded at 150 tokens per 1.5 s = 100 tok/s; at 50 tok/s the
		// 1.5 s after the first data frame take 3 s. Keep-alive gaps before
		// it are skipped, and the delay multiplier is ignored.
		{"with usage", events, []time.Duration{25 * time.Millisecond, 0, 0, time.Second, 2 * time.Second}},
		// Without an output token count, the recorded cadence is kept.
		{"without usage", events[:4], []time.Duration{25 * time.Millisecond, 0, 0, 500 * time.Millisecond}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			clock := &recordingClock{}
			h := &handler{clock: clock}
			entry := model.Entry{Recording: model.Recording{Route: "/v1/messages", Streaming: true}, Revision: model.Revision{Status: 200, Events: tc.events}}
			h.replay(&flushRecorder{ResponseRecorder: httptest.NewRecorder()}, httptest.NewRequest(http.MethodPost, "/v1/messages", nil), entry, settings)
			if !reflect.DeepEqual(clock.sleeps, tc.want) {
				t.Fatalf("sleep durations = %v, want %v", clock.sleeps, tc.want)
			}
		})
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

const responsesStream = "event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"item_id\":\"m1\",\"output_index\":0,\"content_index\":0,\"delta\":\"hello\"}\n\nevent: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"r1\",\"status\":\"completed\",\"output\":[{\"id\":\"m1\",\"type\":\"message\",\"content\":[{\"type\":\"output_text\",\"text\":\"hello\"}]}]}}\n\n"

// failingBody yields its prefix and then fails, like an upstream connection
// that drops mid-stream.
type failingBody struct {
	r   io.Reader
	err error
}

func (b *failingBody) Read(p []byte) (int, error) {
	n, err := b.r.Read(p)
	if err == io.EOF {
		return n, b.err
	}
	return n, err
}
func (b *failingBody) Close() error { return nil }

func droppedStream(prefix string) *http.Response {
	resp := response("", "text/event-stream")
	resp.Body = &failingBody{r: strings.NewReader(prefix), err: io.ErrUnexpectedEOF}
	return resp
}

func lastHistory(t *testing.T, db *store.Store) model.History {
	t.Helper()
	items, err := db.History(context.Background(), 1, 1)
	if err != nil || len(items) != 1 {
		t.Fatalf("history = %v, %v", items, err)
	}
	return items[0]
}

func TestStreamDroppedBeforeFirstEventIsRetried(t *testing.T) {
	calls := 0
	client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		calls++
		if calls == 1 {
			return droppedStream(": OPENROUTER PROCESSING\n\n"), nil
		}
		return response(responsesStream, "text/event-stream"), nil
	})}
	clock := &recordingClock{}
	db := openTestStore(t, "record")
	h := New(db, Config{Client: client, Clock: clock, DefaultProvider: "test", Providers: map[string]map[string]Upstream{"test": {"/v1/responses": {URL: "https://upstream.invalid"}}}})
	live := perform(h, "/v1/responses", `{"stream":true}`)
	if live.Code != 200 || live.Body.String() != ": OPENROUTER PROCESSING\n\n"+responsesStream {
		t.Fatalf("live response = %d %q", live.Code, live.Body.String())
	}
	if calls != 2 || !reflect.DeepEqual(clock.sleeps, []time.Duration{retryBackoff}) {
		t.Fatalf("upstream calls = %d, sleeps = %v", calls, clock.sleeps)
	}
	item := lastHistory(t, db)
	if item.Outcome != "recorded" || !strings.Contains(item.Detail, "retried upstream after 1 failure(s)") {
		t.Fatalf("history = %q %q", item.Outcome, item.Detail)
	}
	// Only the stream that succeeded is recorded.
	setMode(t, db, "replay")
	if replay := perform(h, "/v1/responses", `{"stream":true}`); replay.Body.String() != responsesStream {
		t.Fatalf("replay = %q", replay.Body.String())
	}
}

func TestStreamDroppedAfterFirstEventAbortsTheCaller(t *testing.T) {
	var calls atomic.Int32
	first := strings.SplitAfter(responsesStream, "\n\n")[0]
	client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		calls.Add(1)
		return droppedStream(first), nil
	})}
	db := openTestStore(t, "record")
	server := httptest.NewServer(New(db, Config{Client: client, DefaultProvider: "test", Providers: map[string]map[string]Upstream{"test": {"/v1/responses": {URL: "https://upstream.invalid"}}}}))
	defer server.Close()
	got, err := postStream(server.URL)
	if err == nil {
		t.Fatalf("truncated stream ended cleanly: %q", got)
	}
	if string(got) != first || calls.Load() != 1 {
		t.Fatalf("delivered %q after %d upstream calls", got, calls.Load())
	}
	if item := lastHistory(t, db); item.Outcome != "error" || item.Detail != "unexpected EOF" {
		t.Fatalf("history = %q %q", item.Outcome, item.Detail)
	}
}

func TestStreamRetriesStopAtTheAttemptLimit(t *testing.T) {
	var calls atomic.Int32
	client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		calls.Add(1)
		return droppedStream(""), nil
	})}
	db := openTestStore(t, "record")
	server := httptest.NewServer(New(db, Config{Client: client, Clock: &recordingClock{}, DefaultProvider: "test", Providers: map[string]map[string]Upstream{"test": {"/v1/responses": {URL: "https://upstream.invalid"}}}}))
	defer server.Close()
	if got, err := postStream(server.URL); err == nil {
		t.Fatalf("failed stream ended cleanly: %q", got)
	}
	if calls.Load() != maxUpstreamAttempts {
		t.Fatalf("upstream calls = %d, want %d", calls.Load(), maxUpstreamAttempts)
	}
	if item := lastHistory(t, db); item.Outcome != "error" || !strings.Contains(item.Detail, "retried upstream after 2 failure(s)") {
		t.Fatalf("history = %q %q", item.Outcome, item.Detail)
	}
}

func TestStreamRetryWithAnErrorStatusAbortsTheCaller(t *testing.T) {
	var calls atomic.Int32
	client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		if calls.Add(1) == 1 {
			return droppedStream(""), nil
		}
		resp := response(`{"error":{"message":"overloaded"}}`, "application/json")
		resp.StatusCode, resp.Status = 503, "503 Service Unavailable"
		return resp, nil
	})}
	db := openTestStore(t, "record")
	server := httptest.NewServer(New(db, Config{Client: client, Clock: &recordingClock{}, DefaultProvider: "test", Providers: map[string]map[string]Upstream{"test": {"/v1/responses": {URL: "https://upstream.invalid"}}}}))
	defer server.Close()
	if got, err := postStream(server.URL); err == nil || len(got) != 0 {
		t.Fatalf("caller got %q, %v", got, err)
	}
	if item := lastHistory(t, db); item.Outcome != "error" || !strings.Contains(item.Detail, "503 Service Unavailable") {
		t.Fatalf("history = %q %q", item.Outcome, item.Detail)
	}
}

// postStream sends a streaming request to a live proxy and returns what the
// caller received. An aborted response fails either before its status line,
// when nothing was flushed, or while reading the body.
func postStream(base string) ([]byte, error) {
	resp, err := http.Post(base+"/v1/responses", "application/json", strings.NewReader(`{"stream":true}`))
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	return io.ReadAll(resp.Body)
}

func TestIdenticalMissesInFlightAreForwardedOnce(t *testing.T) {
	var calls atomic.Int32
	entered, unblock := make(chan struct{}), make(chan struct{})
	client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		if calls.Add(1) == 1 {
			close(entered)
			<-unblock
		}
		return response(responsesStream, "text/event-stream"), nil
	})}
	db := openTestStore(t, "auto")
	h := New(db, Config{Client: client, DefaultProvider: "test", Providers: map[string]map[string]Upstream{"test": {"/v1/responses": {URL: "https://upstream.invalid"}}}})
	const callers = 4
	bodies := make([]string, callers)
	var wg sync.WaitGroup
	wg.Add(1)
	go func() { defer wg.Done(); bodies[0] = perform(h, "/v1/responses", `{"stream":true}`).Body.String() }()
	<-entered
	for i := 1; i < callers; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); bodies[i] = perform(h, "/v1/responses", `{"stream":true}`).Body.String() }()
	}
	// Give the followers time to queue behind the request in flight.
	time.Sleep(50 * time.Millisecond)
	close(unblock)
	wg.Wait()
	if calls.Load() != 1 {
		t.Fatalf("upstream calls = %d, want 1", calls.Load())
	}
	for i, body := range bodies {
		if body != responsesStream {
			t.Fatalf("caller %d got %q", i, body)
		}
	}
	items, err := db.History(context.Background(), 1, callers)
	if err != nil {
		t.Fatal(err)
	}
	outcomes := map[string]int{}
	waited := 0
	for _, item := range items {
		outcomes[item.Outcome]++
		if strings.Contains(item.Detail, "identical request in flight") {
			waited++
		}
	}
	if outcomes["recorded"] != 1 || outcomes["hit"] != callers-1 || waited != callers-1 {
		t.Fatalf("outcomes = %v, waited = %d", outcomes, waited)
	}
}

func TestFollowerForwardsWhenTheRequestInFlightRecordsNothing(t *testing.T) {
	var calls atomic.Int32
	entered, unblock := make(chan struct{}), make(chan struct{})
	client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		if calls.Add(1) == 1 {
			close(entered)
			<-unblock
			resp := response(`{"error":{"message":"overloaded"}}`, "application/json")
			resp.StatusCode, resp.Status = 503, "503 Service Unavailable"
			return resp, nil
		}
		return response(responsesStream, "text/event-stream"), nil
	})}
	db := openTestStore(t, "auto")
	h := New(db, Config{Client: client, DefaultProvider: "test", Providers: map[string]map[string]Upstream{"test": {"/v1/responses": {URL: "https://upstream.invalid"}}}})
	var leader, follower *httptest.ResponseRecorder
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); leader = perform(h, "/v1/responses", `{"stream":true}`) }()
	<-entered
	go func() { defer wg.Done(); follower = perform(h, "/v1/responses", `{"stream":true}`) }()
	time.Sleep(50 * time.Millisecond)
	close(unblock)
	wg.Wait()
	if leader.Code != 503 || follower.Code != 200 || follower.Body.String() != responsesStream || calls.Load() != 2 {
		t.Fatalf("leader %d, follower %d %q, upstream calls %d", leader.Code, follower.Code, follower.Body.String(), calls.Load())
	}
}

func TestRecordModeForwardsIdenticalRequestsIndependently(t *testing.T) {
	var calls atomic.Int32
	entered, unblock := make(chan struct{}), make(chan struct{})
	client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		if calls.Add(1) == 1 {
			close(entered)
			<-unblock
		}
		return response(responsesStream, "text/event-stream"), nil
	})}
	db := openTestStore(t, "record")
	h := New(db, Config{Client: client, DefaultProvider: "test", Providers: map[string]map[string]Upstream{"test": {"/v1/responses": {URL: "https://upstream.invalid"}}}})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() { defer wg.Done(); perform(h, "/v1/responses", `{"stream":true}`) }()
	<-entered
	perform(h, "/v1/responses", `{"stream":true}`)
	close(unblock)
	wg.Wait()
	if calls.Load() != 2 {
		t.Fatalf("upstream calls = %d, want 2", calls.Load())
	}
}
