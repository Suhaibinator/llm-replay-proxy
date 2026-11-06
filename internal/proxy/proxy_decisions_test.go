package proxy

import (
	"encoding/base64"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/local/llm-replay-proxy/internal/protocol"
)

const decisionsRoute = protocol.DecisionsRoute

// decisionsRequest is a SystemOne decisions body as go-common's decisions
// client sends it: JSON state plus a page image, and typed questions.
func decisionsRequest(image string) string {
	return `{"model":"perplexity/pplx-decider-v1.2","state":[{"submission_page":3,"ocr_text":"Q2 (b) ..."},{"type":"image_url","image_url":{"url":"data:image/png;base64,` + image + `"}}],` +
		`"questions":{"page_kind":{"type":"choice","instructions":"What is this page?","criteria":{"assignment":"work","cover":"cover sheet"}},"presence_2":{"type":"noul","instructions":"Does the page carry work for question 2?"}}}`
}

const decisionsReply = `{"id":"gen-1","model":"pplx-decider-v1.2","provider":"Perplexity","answers":{"page_kind":{"type":"choice","choice":"assignment","confidence":0.9645848383112524,"probabilities":{"assignment":0.9645848383112524,"cover":0.0354151616887476}},"presence_2":{"type":"noul","noul":0.8769533722831035}},"usage":{"input_tokens":1834,"output_tokens":3,"cost":0.00092}}`

func decisionsHandler(t *testing.T, mode string, reply string, calls *int, auth *string) (http.Handler, func(string)) {
	t.Helper()
	client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		*calls++
		if auth != nil {
			*auth = r.Header.Get("Authorization")
		}
		_, _ = io.Copy(io.Discard, r.Body)
		return response(reply, "application/json"), nil
	})}
	db := openTestStore(t, mode)
	h := New(db, Config{Client: client, DefaultProvider: "openrouter", Providers: map[string]map[string]Upstream{
		"openrouter": {decisionsRoute: {URL: "https://openrouter.ai/api/alpha/decisions", APIKey: "or-key"}},
	}})
	return h, func(m string) { setMode(t, db, m) }
}

// A page decision recorded once replays byte for byte, so the probabilities a
// re-run sees are exactly the recorded ones, even when the caller re-encodes
// the same page image.
func TestDecisionsRecordThenReplayByPageContent(t *testing.T) {
	calls, auth := 0, ""
	h, setMode := decisionsHandler(t, "auto", decisionsReply, &calls, &auth)
	png := []byte("\x89PNG\r\n\x1a\nnot really a png, but bytes all the same")
	image := base64.StdEncoding.EncodeToString(png)

	first := perform(h, decisionsRoute, decisionsRequest(image))
	if first.Code != 200 || first.Body.String() != decisionsReply {
		t.Fatalf("record: %d %s", first.Code, first.Body.String())
	}
	if auth != "Bearer or-key" {
		t.Fatalf("upstream authorization = %q", auth)
	}

	// The same bytes, encoded without padding, are the same page.
	second := perform(h, decisionsRoute, decisionsRequest(strings.TrimRight(image, "=")))
	if second.Code != 200 || second.Body.String() != decisionsReply {
		t.Fatalf("replay: %d %s", second.Code, second.Body.String())
	}
	if calls != 1 {
		t.Fatalf("upstream calls = %d, want 1", calls)
	}

	setMode("replay")
	other := base64.StdEncoding.EncodeToString(append(png, '!'))
	if miss := perform(h, decisionsRoute, decisionsRequest(other)); miss.Code != http.StatusNotFound || !strings.Contains(miss.Body.String(), "recording_not_found") {
		t.Fatalf("different page replayed: %d %s", miss.Code, miss.Body.String())
	}
}

func TestDecisionsRejectStreaming(t *testing.T) {
	calls := 0
	h, _ := decisionsHandler(t, "auto", decisionsReply, &calls, nil)
	w := perform(h, decisionsRoute, `{"model":"m","state":"s","questions":{"q":{"type":"noul"}},"stream":true}`)
	if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "invalid_stream") {
		t.Fatalf("stream accepted: %d %s", w.Code, w.Body.String())
	}
	if calls != 0 {
		t.Fatalf("upstream called %d times", calls)
	}
}

// OpenRouter can answer 200 with an error body, and a reply missing an answer
// envelope is not a decision. Both reach the caller and are never recorded,
// so a retry goes upstream again instead of replaying the failure.
func TestDecisionsFailuresAreDeliveredButNeverPublished(t *testing.T) {
	for name, reply := range map[string]string{
		"error body": `{"error":{"code":502,"message":"provider unavailable"}}`,
		"no answers": `{"id":"gen-1","model":"pplx-decider-v1.2","answers":{},"usage":{"input_tokens":1,"output_tokens":0}}`,
		"no usage":   `{"id":"gen-1","model":"pplx-decider-v1.2","answers":{"q":{"type":"noul","noul":0.5}}}`,
		// The request below asks q and r; an answer set missing one, or
		// answering with another type, would fail the client on every replay.
		"missing answer": `{"id":"gen-1","model":"pplx-decider-v1.2","answers":{"q":{"type":"noul","noul":0.5}},"usage":{"input_tokens":1,"output_tokens":1}}`,
		"wrong type":     `{"id":"gen-1","model":"pplx-decider-v1.2","answers":{"q":{"type":"noul","noul":0.5},"r":{"type":"noul","noul":0.5}},"usage":{"input_tokens":1,"output_tokens":1}}`,
	} {
		t.Run(name, func(t *testing.T) {
			calls := 0
			h, setMode := decisionsHandler(t, "record", reply, &calls, nil)
			body := `{"model":"m","state":"s","questions":{"q":{"type":"noul"},"r":{"type":"choice","criteria":{"a":"A","b":"B"}}}}`
			if live := perform(h, decisionsRoute, body); live.Code != 200 || live.Body.String() != reply {
				t.Fatalf("live: %d %s", live.Code, live.Body.String())
			}
			setMode("replay")
			if replay := perform(h, decisionsRoute, body); replay.Code != http.StatusNotFound {
				t.Fatalf("failure became replayable: %d %s", replay.Code, replay.Body.String())
			}
		})
	}
}

func TestDecisionsAcceptExplicitNonStreaming(t *testing.T) {
	for _, stream := range []string{`,"stream":false`, `,"stream":null`} {
		calls := 0
		h, _ := decisionsHandler(t, "auto", decisionsReply, &calls, nil)
		body := decisionsRequest("AAAA")
		body = body[:len(body)-1] + stream + "}"
		if w := perform(h, decisionsRoute, body); w.Code != 200 || w.Body.String() != decisionsReply {
			t.Fatalf("%s: %d %s", stream, w.Code, w.Body.String())
		}
	}
}
