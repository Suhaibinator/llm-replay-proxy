package mcpserver

import (
	"context"
	"encoding/json/v2"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/local/llm-replay-proxy/internal/match"
	"github.com/local/llm-replay-proxy/internal/model"
	"github.com/local/llm-replay-proxy/internal/store"
)

const chatRoute = "/v1/chat/completions"
const responsesRoute = "/v1/responses"

type fixture struct {
	db       *store.Store
	cid      int64
	stream   model.Entry // streaming Responses recording; two revisions
	first    model.Revision
	chat     model.Entry // non-streaming chat recording
	large    string      // the stream's request input, large enough for chunking and zstd
	hit      int64
	miss     int64
	otherCID int64
}

func seed(t *testing.T) *fixture {
	t.Helper()
	ctx := context.Background()
	db, err := store.Open(filepath.Join(t.TempDir(), "replay.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	settings, err := db.Settings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	f := &fixture{db: db, cid: settings.ActiveCollectionID}
	f.large = strings.Repeat("the quick brown fox jumps over the lazy dog. ", 4000)
	streamReq := fmt.Appendf(nil, `{"model":"m","input":%q,"stream":true}`, f.large)
	first, err := db.Publish(ctx, recording(f.cid, responsesRoute, streamReq, nil, true), streamRevision("first"))
	if err != nil {
		t.Fatal(err)
	}
	f.first = first.Revision
	if f.stream, err = db.Publish(ctx, recording(f.cid, responsesRoute, streamReq, nil, true), streamRevision("second")); err != nil {
		t.Fatal(err)
	}
	chatReq := []byte(`{"model":"m","temperature":0,"messages":[{"role":"user","content":"hello there"}]}`)
	if f.chat, err = db.Publish(ctx, recording(f.cid, chatRoute, chatReq, nil, false), chatRevision()); err != nil {
		t.Fatal(err)
	}
	f.hit = addHistory(t, db, model.History{CollectionID: f.cid, Route: responsesRoute, Key: f.stream.Recording.Key, Request: streamReq, Outcome: "hit", CacheStatus: "hit", Source: "replay", RecordingID: f.stream.Recording.ID, RevisionID: f.stream.Revision.ID})
	missReq := []byte(`{"model":"m","temperature":0.5,"messages":[{"role":"user","content":"hello there"}]}`)
	missKey, _, err := match.Key(chatRoute, missReq, nil)
	if err != nil {
		t.Fatal(err)
	}
	f.miss = addHistory(t, db, model.History{CollectionID: f.cid, Route: chatRoute, Key: missKey, Request: missReq, Outcome: "miss", CacheStatus: "miss", Source: "proxy", Detail: "replay mode"})
	other, err := db.CreateCollection(ctx, "other", []string{"/temperature"})
	if err != nil {
		t.Fatal(err)
	}
	f.otherCID = other.ID
	if _, err := db.Publish(ctx, recording(other.ID, chatRoute, chatReq, other.Exclusions, false), chatRevision()); err != nil {
		t.Fatal(err)
	}
	return f
}

func recording(cid int64, route string, req []byte, exclusions []string, streaming bool) model.Recording {
	key, input, err := match.Key(route, req, exclusions)
	if err != nil {
		panic(err)
	}
	return model.Recording{CollectionID: cid, Key: key, Route: route, Request: req, MatchingInput: input, Streaming: streaming}
}

func streamRevision(text string) model.Revision {
	return model.Revision{Status: 200, Headers: map[string]string{"Content-Type": "text/event-stream"}, Source: "recorded", Events: []model.Event{
		{Data: fmt.Sprintf("event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"item_id\":\"m1\",\"output_index\":0,\"content_index\":0,\"delta\":%q}\n\n", text), OffsetMS: 1},
		{Data: fmt.Sprintf("event: response.output_text.done\ndata: {\"type\":\"response.output_text.done\",\"item_id\":\"m1\",\"output_index\":0,\"content_index\":0,\"text\":%q}\n\n", text), OffsetMS: 2},
		{Data: fmt.Sprintf("event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_%s\",\"status\":\"completed\",\"output\":[{\"id\":\"m1\",\"type\":\"message\",\"role\":\"assistant\",\"content\":[{\"type\":\"output_text\",\"text\":%q}]}]}}\n\n", text, text), OffsetMS: 3},
	}}
}

func chatRevision() model.Revision {
	return model.Revision{Status: 200, Headers: map[string]string{"Content-Type": "application/json"}, Source: "recorded",
		Body: `{"id":"c1","object":"chat.completion","model":"m","choices":[{"index":0,"message":{"role":"assistant","content":"general kenobi"},"finish_reason":"stop"}]}`}
}

func addHistory(t *testing.T, db *store.Store, h model.History) int64 {
	t.Helper()
	if err := db.AddHistory(context.Background(), h); err != nil {
		t.Fatal(err)
	}
	rows, err := db.History(context.Background(), h.CollectionID, 1)
	if err != nil {
		t.Fatal(err)
	}
	return rows[0].ID
}

func connect(t *testing.T, db *store.Store) *mcp.ClientSession {
	t.Helper()
	server := httptest.NewServer(New(db))
	t.Cleanup(server.Close)
	client := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "1"}, nil)
	session, err := client.Connect(context.Background(), &mcp.StreamableClientTransport{Endpoint: server.URL}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = session.Close() })
	return session
}

// call runs a tool and decodes its JSON text, failing on a tool error unless
// wantError.
func call(t *testing.T, s *mcp.ClientSession, name string, args map[string]any, wantError bool) map[string]any {
	t.Helper()
	res, err := s.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	text := res.Content[0].(*mcp.TextContent).Text
	if res.IsError != wantError {
		t.Fatalf("%s: IsError %v: %s", name, res.IsError, text)
	}
	if wantError {
		return map[string]any{"error": text}
	}
	var out map[string]any
	if err := json.Unmarshal([]byte(text), &out); err != nil {
		t.Fatalf("%s: %v: %s", name, err, text)
	}
	return out
}

func TestToolsAreReadOnly(t *testing.T) {
	s := connect(t, seed(t).db)
	res, err := s.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, tool := range res.Tools {
		names = append(names, tool.Name)
		if tool.Annotations == nil || !tool.Annotations.ReadOnlyHint {
			t.Errorf("%s is not marked read-only", tool.Name)
		}
	}
	slices.Sort(names)
	want := []string{"compare_requests", "diagnose_replay", "find_nearest_recordings", "get_history_item", "get_insights", "get_recording", "get_thread", "list_collections", "list_history", "list_recordings", "list_threads"}
	if !slices.Equal(names, want) {
		t.Fatalf("tools: %v", names)
	}
}

func TestRecordingsAreDecoded(t *testing.T) {
	f := seed(t)
	s := connect(t, f.db)

	cols := call(t, s, "list_collections", nil, false)
	if n := len(cols["collections"].([]any)); n != 2 {
		t.Fatalf("collections: %v", cols)
	}
	list := call(t, s, "list_recordings", map[string]any{"collection_id": f.cid, "query": "HELLO THERE"}, false)
	if list["total"].(float64) != 1 || list["recordings"].([]any)[0].(map[string]any)["id"].(float64) != float64(f.chat.Recording.ID) {
		t.Fatalf("query: %v", list)
	}

	rec := call(t, s, "get_recording", map[string]any{"recording_id": f.stream.Recording.ID, "max_bytes": 0}, false)
	if rec["request"] != string(f.stream.Recording.Request) || !strings.Contains(rec["request"].(string), f.large) {
		t.Fatal("request is not the exact stored bytes")
	}
	resp := rec["response"].(map[string]any)
	if resp["text"] != "second" || resp["event_count"].(float64) != 3 || len(resp["events"].([]any)) != 3 || rec["revision_is_active"] != true {
		t.Fatalf("response: %v", resp)
	}
	if rec["validation_error"] != nil || len(rec["revisions"].([]any)) != 2 {
		t.Fatalf("recording: %v", rec)
	}
	old := call(t, s, "get_recording", map[string]any{"recording_id": f.stream.Recording.ID, "revision_id": f.first.ID, "include_events": false}, false)
	if r := old["response"].(map[string]any); r["text"] != "first" || r["events"] != nil || r["events_omitted"].(float64) != 3 || old["revision_is_active"] != false {
		t.Fatalf("old revision: %v", old)
	}

	chat := call(t, s, "get_recording", map[string]any{"recording_id": f.chat.Recording.ID}, false)
	if r := chat["response"].(map[string]any); r["text"] != "general kenobi" || r["body"] != f.chat.Revision.Body {
		t.Fatalf("chat: %v", chat)
	}

	cut := call(t, s, "get_recording", map[string]any{"recording_id": f.stream.Recording.ID, "max_bytes": 1000}, false)
	truncated := cut["truncated"].(map[string]any)
	if len(cut["request"].(string)) > 1000 || truncated["request"].(float64) != float64(len(f.stream.Recording.Request)) || truncated["events"] == nil {
		t.Fatalf("truncation: %v", truncated)
	}

	if e := call(t, s, "get_recording", map[string]any{"recording_id": 999}, true); !strings.Contains(e["error"].(string), "not found") {
		t.Fatalf("missing: %v", e)
	}
}

func TestHistoryAndReplayDiagnosis(t *testing.T) {
	f := seed(t)
	s := connect(t, f.db)

	hist := call(t, s, "list_history", map[string]any{"collection_id": f.cid, "outcomes": []string{"miss"}}, false)
	rows := hist["history"].([]any)
	if len(rows) != 1 || rows[0].(map[string]any)["id"].(float64) != float64(f.miss) {
		t.Fatalf("misses: %v", hist)
	}

	item := call(t, s, "get_history_item", map[string]any{"history_id": f.hit}, false)
	if item["response"].(map[string]any)["text"] != "second" || !strings.Contains(item["request_text"].(string), f.large) {
		t.Fatalf("hit item: %v", item["response"])
	}
	if item := call(t, s, "get_history_item", map[string]any{"history_id": f.miss}, false); item["response"] != nil || item["response_unavailable_reason"] == nil {
		t.Fatalf("miss item: %v", item)
	}

	d := call(t, s, "diagnose_replay", map[string]any{"history_id": f.miss}, false)
	closest := d["closest_recording_comparison"].(map[string]any)
	diffs := closest["differences"].([]any)
	if closest["recording_id"].(float64) != float64(f.chat.Recording.ID) || closest["matching_differences"].(float64) != 1 || diffs[0].(map[string]any)["path"] != "/temperature" {
		t.Fatalf("closest: %v", closest)
	}
	others := d["matches_in_other_collections"].([]any)
	if len(others) != 1 || others[0].(map[string]any)["collection_id"].(float64) != float64(f.otherCID) {
		t.Fatalf("other collections: %v", d)
	}
	if len(d["findings"].([]any)) < 3 {
		t.Fatalf("findings: %v", d["findings"])
	}
	if d := call(t, s, "diagnose_replay", map[string]any{"history_id": f.hit}, false); d["closest_recording_comparison"] != nil {
		t.Fatalf("hit diagnosis: %v", d)
	}

	cmp := call(t, s, "compare_requests", map[string]any{"recording_id": f.chat.Recording.ID, "request": `{"model":"m","messages":[{"role":"user","content":"hi"}]}`}, false)
	if cmp["matching_differences"].(float64) != 2 {
		t.Fatalf("compare: %v", cmp)
	}
	call(t, s, "compare_requests", map[string]any{"recording_id": f.chat.Recording.ID}, true)
}

func TestRejectsCrossOriginRequests(t *testing.T) {
	server := httptest.NewServer(New(seed(t).db))
	defer server.Close()
	req, _ := http.NewRequest(http.MethodPost, server.URL, strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set("Origin", "https://evil.example")
	req.Header.Set("Sec-Fetch-Site", "cross-site")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("status %d", resp.StatusCode)
	}
}

func TestValueCapAndRequestOmission(t *testing.T) {
	f := seed(t)
	s := connect(t, f.db)
	rec := call(t, s, "get_recording", map[string]any{"recording_id": f.stream.Recording.ID, "include_request": false, "max_value_bytes": 100}, false)
	if _, ok := rec["request"]; ok {
		t.Fatalf("request included: %v", rec["request"])
	}
	events := rec["response"].(map[string]any)["events"].([]any)
	if len(events) != 3 {
		t.Fatalf("events: %v", rec["response"])
	}
	truncated := rec["truncated"].(map[string]any)
	for i, e := range events {
		data := e.(map[string]any)["data"].(string)
		if len(data) > 100 || truncated[fmt.Sprintf("events[%d]", i)] == nil {
			t.Fatalf("event %d: %d bytes, truncated %v", i, len(data), truncated)
		}
	}
	item := call(t, s, "get_history_item", map[string]any{"history_id": f.hit, "include_request": false}, false)
	if _, ok := item["request_text"]; ok || item["response"] == nil {
		t.Fatalf("history item: %v", item)
	}
}

func TestComparisonShowsAnExcerptAroundTheDifference(t *testing.T) {
	f := seed(t)
	s := connect(t, f.db)
	// Change the last sentence, far past the default excerpt length.
	i := strings.LastIndex(f.large, "lazy dog")
	changed := f.large[:i] + "lazy cat" + f.large[i+len("lazy dog"):]
	cmp := call(t, s, "compare_requests", map[string]any{"recording_id": f.stream.Recording.ID, "request": fmt.Sprintf(`{"model":"m","input":%q,"stream":true}`, changed)}, false)
	d := cmp["differences"].([]any)[0].(map[string]any)
	req, recd := d["request"].(string), d["recorded"].(string)
	from := int(d["excerpt_from"].(float64))
	if d["path"] != "/input" || len(req) > defaultDiffValueBytes || len(recd) > defaultDiffValueBytes || from == 0 {
		t.Fatalf("excerpt: path %v from %d, %d/%d bytes", d["path"], from, len(req), len(recd))
	}
	// The excerpt starts shortly before the first difference.
	if !strings.Contains(req, "lazy cat") || !strings.Contains(recd, "lazy dog") || strings.Index(req, "cat") > defaultDiffValueBytes/2 {
		t.Fatalf("excerpt misses the difference: %.200q", req)
	}
	whole := call(t, s, "compare_requests", map[string]any{"recording_id": f.stream.Recording.ID, "request": fmt.Sprintf(`{"model":"m","input":%q,"stream":true}`, changed), "max_value_bytes": 0}, false)
	if d := whole["differences"].([]any)[0].(map[string]any); len(d["request"].(string)) < len(changed) || d["excerpt_from"] != nil {
		t.Fatalf("whole values: %d bytes", len(d["request"].(string)))
	}
}

func TestDiagnosisSkipsTheDiffWhenAnExactMatchNowExists(t *testing.T) {
	f := seed(t)
	s := connect(t, f.db)
	// A miss logged before the chat recording existed.
	early := addHistory(t, f.db, model.History{CollectionID: f.cid, Route: chatRoute, Key: f.chat.Recording.Key, Request: f.chat.Recording.Request, Outcome: "miss", CacheStatus: "miss", Source: "proxy", Detail: "replay mode"})
	d := call(t, s, "diagnose_replay", map[string]any{"history_id": early}, false)
	if d["exact_match_recording_id_now"].(float64) != float64(f.chat.Recording.ID) || d["closest_recording_comparison"] != nil {
		t.Fatalf("diagnosis: %v", d)
	}
}

func TestDiagnosisDiffsTheRecordingWithFewestDifferences(t *testing.T) {
	f := seed(t)
	s := connect(t, f.db)
	ctx := context.Background()
	var essay strings.Builder
	for i := range 3000 {
		fmt.Fprintf(&essay, "paragraph %d argues point %d. ", i, i*7919%1000)
	}
	opening := essay.String()
	req := func(temperature string, extra string) []byte {
		return fmt.Appendf(nil, `{"model":"m","temperature":%s,"messages":[{"role":"user","content":%q}%s]}`, temperature, opening, extra)
	}
	// A longer conversation holds all of the request's content, so it ranks
	// first by shared content, but it differs in more places.
	longer, err := f.db.Publish(ctx, recording(f.cid, chatRoute, req("0", `,{"role":"assistant","content":"a"},{"role":"user","content":"b"},{"role":"assistant","content":"c"}`), nil, false), chatRevision())
	if err != nil {
		t.Fatal(err)
	}
	sameLength, err := f.db.Publish(ctx, recording(f.cid, chatRoute, req("1", ""), nil, false), chatRevision())
	if err != nil {
		t.Fatal(err)
	}
	missReq := req("0", "")
	key, _, err := match.Key(chatRoute, missReq, nil)
	if err != nil {
		t.Fatal(err)
	}
	miss := addHistory(t, f.db, model.History{CollectionID: f.cid, Route: chatRoute, Key: key, Request: missReq, Outcome: "miss", CacheStatus: "miss", Source: "proxy", Detail: "replay mode"})
	d := call(t, s, "diagnose_replay", map[string]any{"history_id": miss}, false)
	nearest := d["nearest_recordings"].([]any)
	if nearest[0].(map[string]any)["recording_id"].(float64) != float64(longer.Recording.ID) {
		t.Fatalf("setup: the longer recording should rank first by shared content: %v", nearest)
	}
	closest := d["closest_recording_comparison"].(map[string]any)
	if closest["recording_id"].(float64) != float64(sameLength.Recording.ID) || closest["matching_differences"].(float64) != 1 {
		t.Fatalf("closest: %v", closest)
	}
}
