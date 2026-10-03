package proxy

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

func TestUpstreamHTTPErrorsAreNeverPublishedForAnyProtocol(t *testing.T) {
	for _, route := range []string{"/v1/chat/completions", "/v1/responses", "/v1/messages"} {
		t.Run(route, func(t *testing.T) {
			calls := 0
			client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
				calls++
				return matrixResponse(http.StatusTooManyRequests, `{"error":{"message":"slow down"}}`, "application/json"), nil
			})}
			db := openTestStore(t, "record")
			h := New(db, Config{Client: client, Upstreams: map[string]Upstream{route: {URL: "https://upstream.invalid"}}})

			live := perform(h, route, `{}`)
			if live.Code != http.StatusTooManyRequests || !strings.Contains(live.Body.String(), "slow down") {
				t.Fatalf("live response = %d %q", live.Code, live.Body.String())
			}

			setMode(t, db, "replay")
			miss := perform(h, route, `{}`)
			if miss.Code != http.StatusNotFound {
				t.Fatalf("upstream error was published: %d %s", miss.Code, miss.Body.String())
			}
			if calls != 1 {
				t.Fatalf("upstream calls = %d, want 1", calls)
			}
		})
	}
}

func TestMalformedAndIncompleteSSEAreNeverPublished(t *testing.T) {
	tests := []struct {
		name, route, stream string
	}{
		{
			"chat malformed", "/v1/chat/completions",
			"data: {\"choices\":\n\n",
		},
		{
			"chat incomplete", "/v1/chat/completions",
			"data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"partial\"},\"finish_reason\":\"stop\"}]}\n\n",
		},
		{
			"responses malformed", "/v1/responses",
			"event: response.completed\ndata: not-json\n\n",
		},
		{
			"responses incomplete", "/v1/responses",
			"event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"item_id\":\"m1\",\"output_index\":0,\"content_index\":0,\"delta\":\"partial\"}\n\n",
		},
		{
			"messages malformed", "/v1/messages",
			"event: message_start\ndata: {\"type\":\"message_stop\"}\n\n",
		},
		{
			"messages incomplete frame", "/v1/messages",
			"event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"type\":\"message\",\"content\":[]}}\n",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
				return matrixResponse(http.StatusOK, tc.stream, "text/event-stream"), nil
			})}
			db := openTestStore(t, "record")
			h := New(db, Config{Client: client, Upstreams: map[string]Upstream{tc.route: {URL: "https://upstream.invalid"}}})

			live := perform(h, tc.route, `{"stream":true}`)
			if live.Code != http.StatusOK || live.Body.String() != tc.stream {
				t.Fatalf("live response = %d %q", live.Code, live.Body.String())
			}
			setMode(t, db, "replay")
			if replay := perform(h, tc.route, `{"stream":true}`); replay.Code != http.StatusNotFound {
				t.Fatalf("invalid stream was published: %d %s", replay.Code, replay.Body.String())
			}
		})
	}
}

func TestCanceledRefreshPreservesPriorRevision(t *testing.T) {
	const route = "/v1/chat/completions"
	const requestBody = `{"model":"demo"}`
	oldBody := matrixChatBody("old")
	newBody := matrixChatBody("new")
	var cancel context.CancelFunc
	calls := 0
	client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		calls++
		if calls == 1 {
			return matrixResponse(http.StatusOK, oldBody, "application/json"), nil
		}
		resp := matrixResponse(http.StatusOK, "", "application/json")
		resp.Body = &cancelAtEOFReader{reader: strings.NewReader(newBody), cancel: cancel}
		return resp, nil
	})}
	db := openTestStore(t, "record")
	h := New(db, Config{Client: client, Upstreams: map[string]Upstream{route: {URL: "https://upstream.invalid"}}})

	if first := perform(h, route, requestBody); first.Code != http.StatusOK || first.Body.String() != oldBody {
		t.Fatalf("initial record = %d %q", first.Code, first.Body.String())
	}
	ctx, cancelRequest := context.WithCancel(context.Background())
	cancel = cancelRequest
	second := performWithContext(h, ctx, route, requestBody)
	if second.Code != http.StatusOK || second.Body.String() != newBody {
		t.Fatalf("interrupted live response = %d %q", second.Code, second.Body.String())
	}

	settings, err := db.Settings(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	recordings, err := db.Recordings(context.Background(), settings.ActiveCollectionID)
	if err != nil || len(recordings) != 1 {
		t.Fatalf("recordings = %d, err = %v", len(recordings), err)
	}
	revisions, err := db.Revisions(context.Background(), recordings[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(revisions) != 1 || revisions[0].Body != oldBody || recordings[0].ActiveRevisionID != revisions[0].ID {
		t.Fatalf("interrupted refresh changed revisions: recordings=%+v revisions=%+v", recordings, revisions)
	}
	history, err := db.History(context.Background(), settings.ActiveCollectionID, 10)
	if err != nil || len(history) < 2 || history[0].Outcome != "interrupted" {
		t.Fatalf("history = %+v, err = %v", history, err)
	}

	setMode(t, db, "replay")
	if replay := perform(h, route, requestBody); replay.Code != http.StatusOK || replay.Body.String() != oldBody {
		t.Fatalf("replay after interrupted refresh = %d %q", replay.Code, replay.Body.String())
	}
	if calls != 2 {
		t.Fatalf("upstream calls = %d, want 2", calls)
	}
}

func TestAutoModeRecordsMissesAndReplaysHits(t *testing.T) {
	const route = "/v1/chat/completions"
	calls := 0
	client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		calls++
		return matrixResponse(http.StatusOK, matrixChatBody(fmt.Sprintf("call-%d", calls)), "application/json"), nil
	})}
	db := openTestStore(t, "auto")
	h := New(db, Config{Client: client, Upstreams: map[string]Upstream{route: {URL: "https://upstream.invalid"}}})

	first := perform(h, route, `{"model":"demo","input":"same"}`)
	second := perform(h, route, `{"input":"same","model":"demo"}`)
	third := perform(h, route, `{"model":"demo","input":"different"}`)
	if first.Code != http.StatusOK || second.Code != http.StatusOK || third.Code != http.StatusOK {
		t.Fatalf("auto responses = %d, %d, %d", first.Code, second.Code, third.Code)
	}
	if second.Body.String() != first.Body.String() {
		t.Fatalf("auto hit = %q, want recorded %q", second.Body.String(), first.Body.String())
	}
	if third.Body.String() == first.Body.String() {
		t.Fatal("auto miss did not reach upstream")
	}
	if calls != 2 {
		t.Fatalf("upstream calls = %d, want 2", calls)
	}

	settings, _ := db.Settings(context.Background())
	history, err := db.History(context.Background(), settings.ActiveCollectionID, 10)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"recorded", "hit", "recorded"}
	if len(history) != len(want) {
		t.Fatalf("history = %+v", history)
	}
	for i, outcome := range want {
		if history[len(history)-1-i].Outcome != outcome {
			t.Fatalf("history outcomes = %+v", history)
		}
	}
	if history[0].CacheStatus != "miss" || history[1].CacheStatus != "hit" || history[2].CacheStatus != "miss" {
		t.Fatalf("lookup outcomes newest-first = %q, %q, %q", history[0].CacheStatus, history[1].CacheStatus, history[2].CacheStatus)
	}
}

func TestRecordedProviderStateAllowsExactReplayButGuardsLiveRequests(t *testing.T) {
	const route = "/v1/responses"
	const stateID = "resp_recorded_state"
	const exact = `{"model":"demo","previous_response_id":"resp_recorded_state","input":"continue"}`
	const producer = `{"model":"demo","input":"seed"}`
	calls := 0
	client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		calls++
		body, _ := io.ReadAll(r.Body)
		if bytes.Contains(body, []byte(`"input":"seed"`)) {
			return matrixResponse(http.StatusOK, matrixResponsesBody(stateID, "seeded"), "application/json"), nil
		}
		return matrixResponse(http.StatusOK, matrixResponsesBody("resp_dependent", "continued"), "application/json"), nil
	})}
	db := openTestStore(t, "record")
	h := New(db, Config{Client: client, Upstreams: map[string]Upstream{route: {URL: "https://upstream.invalid"}}})

	exactRecorded := perform(h, route, exact)
	if exactRecorded.Code != http.StatusOK {
		t.Fatalf("record exact request = %d %s", exactRecorded.Code, exactRecorded.Body.String())
	}
	if seeded := perform(h, route, producer); seeded.Code != http.StatusOK {
		t.Fatalf("record provider state = %d %s", seeded.Code, seeded.Body.String())
	}

	setMode(t, db, "auto")
	if hit := perform(h, route, exact); hit.Code != http.StatusOK || hit.Body.String() != exactRecorded.Body.String() {
		t.Fatalf("exact replay = %d %q", hit.Code, hit.Body.String())
	}
	if miss := perform(h, route, `{"model":"demo","previous_response_id":"resp_recorded_state","input":"different"}`); miss.Code != http.StatusConflict || !strings.Contains(miss.Body.String(), "recorded_state_unavailable") {
		t.Fatalf("auto miss with recorded state = %d %s", miss.Code, miss.Body.String())
	}
	setMode(t, db, "record")
	if refresh := perform(h, route, exact); refresh.Code != http.StatusConflict || !strings.Contains(refresh.Body.String(), "recorded_state_unavailable") {
		t.Fatalf("record request with recorded state = %d %s", refresh.Code, refresh.Body.String())
	}
	if calls != 2 {
		t.Fatalf("guarded requests reached upstream: calls = %d", calls)
	}
}

func TestConcurrentIdenticalAutoMissesPublishSafely(t *testing.T) {
	const n = 12
	const route = "/v1/chat/completions"
	const requestBody = `{"model":"demo","input":"same"}`
	var calls atomic.Int32
	release := make(chan struct{})
	client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		call := calls.Add(1)
		if call == n {
			close(release)
		}
		<-release
		return matrixResponse(http.StatusOK, matrixChatBody(fmt.Sprintf("call-%d", call)), "application/json"), nil
	})}
	db := openTestStore(t, "auto")
	h := New(db, Config{Client: client, Upstreams: map[string]Upstream{route: {URL: "https://upstream.invalid"}}})

	results := make(chan *httptest.ResponseRecorder, n)
	var wg sync.WaitGroup
	for range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results <- perform(h, route, requestBody)
		}()
	}
	wg.Wait()
	close(results)
	for result := range results {
		if result.Code != http.StatusOK || !strings.Contains(result.Body.String(), `"finish_reason":"stop"`) {
			t.Fatalf("concurrent response = %d %q", result.Code, result.Body.String())
		}
	}
	if calls.Load() != n {
		t.Fatalf("upstream calls = %d, want %d", calls.Load(), n)
	}

	settings, _ := db.Settings(context.Background())
	recordings, err := db.Recordings(context.Background(), settings.ActiveCollectionID)
	if err != nil || len(recordings) != 1 {
		t.Fatalf("recordings = %d, err = %v", len(recordings), err)
	}
	revisions, err := db.Revisions(context.Background(), recordings[0].ID)
	if err != nil || len(revisions) != n {
		t.Fatalf("revisions = %d, err = %v", len(revisions), err)
	}
	setMode(t, db, "replay")
	if replay := perform(h, route, requestBody); replay.Code != http.StatusOK {
		t.Fatalf("replay = %d %s", replay.Code, replay.Body.String())
	}
}

func TestToolCallsRecordAndReplayAcrossProtocolsAndTransports(t *testing.T) {
	tests := []struct {
		name, route, body string
		stream            bool
	}{
		{
			"chat body", "/v1/chat/completions",
			`{"id":"chat_1","choices":[{"index":0,"message":{"role":"assistant","content":null,"tool_calls":[{"id":"call_1","type":"function","function":{"name":"lookup_weather","arguments":"{\"city\":\"Paris\"}"}}]},"finish_reason":"tool_calls"}]}`,
			false,
		},
		{
			"responses body", "/v1/responses",
			`{"id":"resp_1","status":"completed","output":[{"type":"function_call","id":"fc_1","call_id":"call_1","name":"lookup_weather","arguments":"{\"city\":\"Paris\"}","status":"completed"}]}`,
			false,
		},
		{
			"messages body", "/v1/messages",
			`{"id":"msg_1","type":"message","role":"assistant","content":[{"type":"tool_use","id":"call_1","name":"lookup_weather","input":{"city":"Paris"}}],"stop_reason":"tool_use"}`,
			false,
		},
		{
			"chat stream", "/v1/chat/completions",
			"data: {\"id\":\"chat_1\",\"choices\":[{\"index\":0,\"delta\":{\"tool_calls\":[{\"index\":0,\"id\":\"call_1\",\"type\":\"function\",\"function\":{\"name\":\"lookup_weather\",\"arguments\":\"{\\\"city\\\":\\\"Paris\\\"}\"}}]},\"finish_reason\":null}]}\n\ndata: {\"id\":\"chat_1\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"tool_calls\"}]}\n\ndata: [DONE]\n\n",
			true,
		},
		{
			"responses stream", "/v1/responses",
			"event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_1\",\"status\":\"completed\",\"output\":[{\"type\":\"function_call\",\"id\":\"fc_1\",\"call_id\":\"call_1\",\"name\":\"lookup_weather\",\"arguments\":\"{\\\"city\\\":\\\"Paris\\\"}\",\"status\":\"completed\"}]}}\n\n",
			true,
		},
		{
			"messages stream", "/v1/messages",
			"event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_1\",\"type\":\"message\",\"role\":\"assistant\",\"content\":[],\"stop_reason\":null}}\n\nevent: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"tool_use\",\"id\":\"call_1\",\"name\":\"lookup_weather\",\"input\":{}}}\n\nevent: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"input_json_delta\",\"partial_json\":\"{\\\"city\\\":\\\"Paris\\\"}\"}}\n\nevent: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\nevent: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"tool_use\"}}\n\nevent: message_stop\ndata: {\"type\":\"message_stop\"}\n\n",
			true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
				calls++
				contentType := "application/json"
				if tc.stream {
					contentType = "text/event-stream"
				}
				return matrixResponse(http.StatusOK, tc.body, contentType), nil
			})}
			db := openTestStore(t, "record")
			h := New(db, Config{Client: client, Upstreams: map[string]Upstream{tc.route: {URL: "https://upstream.invalid"}}})
			requestBody := fmt.Sprintf(`{"model":"demo","stream":%t}`, tc.stream)

			live := perform(h, tc.route, requestBody)
			if live.Code != http.StatusOK || live.Body.String() != tc.body {
				t.Fatalf("record = %d %q", live.Code, live.Body.String())
			}
			setMode(t, db, "replay")
			replay := perform(h, tc.route, requestBody)
			if replay.Code != http.StatusOK || replay.Body.String() != tc.body {
				t.Fatalf("replay = %d %q", replay.Code, replay.Body.String())
			}
			if calls != 1 {
				t.Fatalf("upstream calls = %d, want 1", calls)
			}
		})
	}
}

func matrixResponse(status int, body, contentType string) *http.Response {
	headers := make(http.Header)
	headers.Set("Content-Type", contentType)
	return &http.Response{
		StatusCode: status,
		Status:     fmt.Sprintf("%d %s", status, http.StatusText(status)),
		Header:     headers,
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}

func matrixChatBody(text string) string {
	return fmt.Sprintf(`{"id":"chat_1","choices":[{"index":0,"message":{"role":"assistant","content":%q},"finish_reason":"stop"}]}`, text)
}

func matrixResponsesBody(id, text string) string {
	return fmt.Sprintf(`{"id":%q,"status":"completed","output":[{"id":"m1","type":"message","role":"assistant","content":[{"type":"output_text","text":%q}]}]}`, id, text)
}

func performWithContext(h http.Handler, ctx context.Context, route, body string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(http.MethodPost, route, strings.NewReader(body)).WithContext(ctx)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

type cancelAtEOFReader struct {
	reader *strings.Reader
	cancel context.CancelFunc
	once   sync.Once
}

func (r *cancelAtEOFReader) Read(p []byte) (int, error) {
	n, err := r.reader.Read(p)
	if err == io.EOF {
		r.once.Do(r.cancel)
	}
	return n, err
}

func (*cancelAtEOFReader) Close() error { return nil }
