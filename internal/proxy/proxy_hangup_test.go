package proxy

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"syscall"
	"testing"
)

const (
	hangupResponsesFrames = "event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"item_id\":\"m1\",\"output_index\":0,\"content_index\":0,\"delta\":\"hi\"}\n\nevent: response.output_text.done\ndata: {\"type\":\"response.output_text.done\",\"item_id\":\"m1\",\"output_index\":0,\"content_index\":0,\"text\":\"hi\"}\n\n"
	hangupResponsesDone   = "event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"r1\",\"status\":\"completed\",\"output\":[{\"id\":\"m1\",\"type\":\"message\",\"content\":[{\"type\":\"output_text\",\"text\":\"hi\"}]}]}}\n\ndata: [DONE]\n\n"
	hangupChatFrames      = "data: {\"id\":\"c1\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"ok\"},\"finish_reason\":null}]}\n\ndata: {\"id\":\"c1\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n"
)

// hangupBody delivers its payload, then behaves like a transport body whose
// request context the departing caller canceled before the upstream ended.
type hangupBody struct {
	ctx     context.Context
	cancel  context.CancelFunc
	payload string
	sent    bool
}

func (b *hangupBody) Read(p []byte) (int, error) {
	if !b.sent {
		b.sent = true
		return copy(p, b.payload), nil
	}
	b.cancel()
	<-b.ctx.Done()
	return 0, b.ctx.Err()
}

func (*hangupBody) Close() error { return nil }

func TestCallerHangupAfterStreamEnd(t *testing.T) {
	cases := []struct {
		name, route, payload string
		recorded             bool
	}{
		{"responses complete", "/v1/responses", hangupResponsesFrames + hangupResponsesDone, true},
		{"responses truncated", "/v1/responses", hangupResponsesFrames, false},
		{"chat complete", "/v1/chat/completions", hangupChatFrames + "data: [DONE]\n\n", true},
		{"chat without DONE", "/v1/chat/completions", hangupChatFrames, false},
		{"partial frame", "/v1/responses", hangupResponsesFrames + hangupResponsesDone + "data: {", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			const request = `{"model":"m","stream":true}`
			ctx, cancel := context.WithCancel(context.Background())
			client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
				resp := response("", "text/event-stream")
				resp.Body = &hangupBody{ctx: r.Context(), cancel: cancel, payload: tc.payload}
				return resp, nil
			})}
			db := openTestStore(t, "record")
			h := New(db, Config{Client: client, Upstreams: map[string]Upstream{tc.route: {URL: "https://upstream.invalid"}}})
			if live := performWithContext(h, ctx, tc.route, request); live.Body.String() != tc.payload {
				t.Fatalf("live body = %q", live.Body.String())
			}
			got := latestHistory(t, db)
			setMode(t, db, "replay")
			replay := perform(h, tc.route, request)
			if !tc.recorded {
				if got.Outcome != "interrupted" || got.RecordingID != 0 || !strings.Contains(got.Detail, "stream incomplete at disconnect: ") {
					t.Fatalf("history = %+v", got)
				}
				if replay.Code != http.StatusNotFound {
					t.Fatalf("incomplete stream published: %d %q", replay.Code, replay.Body.String())
				}
				return
			}
			if got.Outcome != "recorded" || got.RecordingID == 0 || got.Detail == "" {
				t.Fatalf("history = %+v", got)
			}
			if replay.Code != http.StatusOK || replay.Body.String() != tc.payload {
				t.Fatalf("replay = %d %q", replay.Code, replay.Body.String())
			}
		})
	}
}

// pipeAfterDoneWriter accepts writes until the caller has read [DONE], then
// fails as a socket the caller closed does.
type pipeAfterDoneWriter struct {
	*httptest.ResponseRecorder
	closed bool
}

func (w *pipeAfterDoneWriter) Write(p []byte) (int, error) {
	if w.closed {
		return 0, syscall.EPIPE
	}
	w.closed = strings.Contains(string(p), "[DONE]")
	return w.ResponseRecorder.Write(p)
}

func TestCallerHangupBeforeTrailingUpstreamBytes(t *testing.T) {
	const request = `{"model":"m","stream":true}`
	payload := hangupResponsesFrames + hangupResponsesDone
	client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		resp := response("", "text/event-stream")
		resp.Body = &chunkedBody{chunks: []string{payload, ": keep-alive\n\n"}}
		return resp, nil
	})}
	db := openTestStore(t, "record")
	h := New(db, Config{Client: client, Upstreams: map[string]Upstream{"/v1/responses": {URL: "https://upstream.invalid"}}})
	h.ServeHTTP(&pipeAfterDoneWriter{ResponseRecorder: httptest.NewRecorder()}, httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(request)))
	if got := latestHistory(t, db); got.Outcome != "recorded" {
		t.Fatalf("history = %+v", got)
	}
	setMode(t, db, "replay")
	if replay := perform(h, "/v1/responses", request); replay.Code != http.StatusOK || replay.Body.String() != payload {
		t.Fatalf("replay = %d %q", replay.Code, replay.Body.String())
	}
}

// chunkedBody returns one chunk per read, as a streaming upstream does.
type chunkedBody struct{ chunks []string }

func (b *chunkedBody) Read(p []byte) (int, error) {
	if len(b.chunks) == 0 {
		return 0, io.EOF
	}
	n := copy(p, b.chunks[0])
	b.chunks = b.chunks[1:]
	return n, nil
}

func (*chunkedBody) Close() error { return nil }

func TestCallerHangupRacingUpstreamEOF(t *testing.T) {
	const request = `{"model":"m","stream":true}`
	payload := hangupResponsesFrames + hangupResponsesDone
	ctx, cancel := context.WithCancel(context.Background())
	client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		resp := response("", "text/event-stream")
		resp.Body = &cancelAtEOFReader{reader: strings.NewReader(payload), cancel: cancel}
		return resp, nil
	})}
	db := openTestStore(t, "record")
	h := New(db, Config{Client: client, Upstreams: map[string]Upstream{"/v1/responses": {URL: "https://upstream.invalid"}}})
	performWithContext(h, ctx, "/v1/responses", request)
	if got := latestHistory(t, db); got.Outcome != "recorded" || got.Detail == "" {
		t.Fatalf("history = %+v", got)
	}
	setMode(t, db, "replay")
	if replay := perform(h, "/v1/responses", request); replay.Code != http.StatusOK || replay.Body.String() != payload {
		t.Fatalf("replay = %d %q", replay.Code, replay.Body.String())
	}
}
