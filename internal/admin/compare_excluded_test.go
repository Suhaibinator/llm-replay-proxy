package admin

import (
	"context"
	"net/http"
	"testing"

	"github.com/local/llm-replay-proxy/internal/match"
	"github.com/local/llm-replay-proxy/internal/model"
)

func TestCompareMarksExcludedDifferences(t *testing.T) {
	ctx := context.Background()
	db, h := testAdmin(t)
	c, err := db.CreateCollection(ctx, "excluding", []string{"/metadata", "/messages/0/name"})
	if err != nil {
		t.Fatal(err)
	}
	req := []byte(`{"model":"m","metadata":{"run":"a"},"messages":[{"role":"user","content":"hi","name":"x"}]}`)
	key, input, err := match.Key("/v1/chat/completions", req, c.Exclusions)
	if err != nil {
		t.Fatal(err)
	}
	e, err := db.Publish(ctx, model.Recording{CollectionID: c.ID, Key: key, Route: "/v1/chat/completions", Request: req, MatchingInput: input},
		model.Revision{Status: 200, Headers: map[string]string{"Content-Type": "application/json"}, Body: `{"id":"x","choices":[{"message":{"role":"assistant","content":"hello"},"finish_reason":"stop"}]}`, Source: "record"})
	if err != nil {
		t.Fatal(err)
	}
	w := request(t, h, http.MethodPost, "/api/compare", map[string]any{"recording_id": e.Recording.ID,
		"request": map[string]any{"model": "m2", "metadata": map[string]any{"run": "b"}, "metadataX": 1, "messages": []any{map[string]any{"role": "user", "content": "hi", "name": "y"}}}})
	if w.Code != 200 {
		t.Fatalf("compare: %d %s", w.Code, w.Body.String())
	}
	got := map[string]bool{}
	for _, d := range decodeAs[struct {
		Differences []map[string]any `json:"differences"`
	}](t, w.Body.Bytes()).Differences {
		got[d["path"].(string)] = d["excluded"].(bool)
	}
	// /metadataX shares a prefix with /metadata but is not under it.
	want := map[string]bool{"/model": false, "/metadata/run": true, "/metadataX": false, "/messages/0/name": true}
	if len(got) != len(want) {
		t.Fatalf("differences %v", got)
	}
	for path, excluded := range want {
		if v, ok := got[path]; !ok || v != excluded {
			t.Fatalf("differences %v, want %v", got, want)
		}
	}
}

func TestCompareIgnoresToolOrderAndToolJSONKeyOrder(t *testing.T) {
	ctx := context.Background()
	db, h := testAdmin(t)
	settings, err := db.Settings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	req := []byte(`{"model":"m","input":[{"type":"function_call_output","call_id":"c","output":"{\"b\":1,\"a\":2}"}],"tools":[{"type":"function","name":"b"},{"type":"function","name":"a"}]}`)
	key, input, err := match.Key("/v1/responses", req, nil)
	if err != nil {
		t.Fatal(err)
	}
	e, err := db.Publish(ctx, model.Recording{CollectionID: settings.ActiveCollectionID, Key: key, Route: "/v1/responses", Request: req, MatchingInput: input},
		model.Revision{Status: 200, Headers: map[string]string{"Content-Type": "application/json"}, Body: `{"id":"resp_1","object":"response","status":"completed","output":[{"id":"m1","type":"message","role":"assistant","status":"completed","content":[{"type":"output_text","text":"ok","annotations":[]}]}]}`, Source: "record"})
	if err != nil {
		t.Fatal(err)
	}
	// Tools reordered and tool-output keys reordered: the same key, so no differences.
	w := request(t, h, http.MethodPost, "/api/compare", map[string]any{"recording_id": e.Recording.ID, "request": map[string]any{
		"model": "m",
		"input": []any{map[string]any{"type": "function_call_output", "call_id": "c", "output": `{"a":2,"b":1}`}},
		"tools": []any{map[string]any{"type": "function", "name": "a"}, map[string]any{"type": "function", "name": "b"}},
	}})
	if w.Code != 200 {
		t.Fatalf("compare: %d %s", w.Code, w.Body.String())
	}
	if d := decodeAs[struct {
		Differences []map[string]any `json:"differences"`
	}](t, w.Body.Bytes()).Differences; len(d) != 0 {
		t.Fatalf("differences %v", d)
	}
}
