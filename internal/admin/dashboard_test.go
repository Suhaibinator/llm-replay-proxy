package admin

import (
	"context"
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/local/llm-replay-proxy/internal/model"
)

func decodeAs[T any](t *testing.T, body []byte) T {
	t.Helper()
	var v T
	if err := json.Unmarshal(body, &v); err != nil {
		t.Fatalf("decode %s: %v", body, err)
	}
	return v
}

func keys(v map[string]any) string {
	out := make([]string, 0, len(v))
	for k := range v {
		out = append(out, k)
	}
	slices.Sort(out)
	return strings.Join(out, ",")
}

func TestDashboardEndpoints(t *testing.T) {
	ctx := context.Background()
	db, h := testAdmin(t)
	e := publishFixture(t, db)
	cid := itoa(e.Recording.CollectionID)
	now := time.Now().UTC()
	stamp := func(d time.Duration) string { return now.Add(d).Format(time.RFC3339Nano) }
	later := []byte(`{"model":"m","messages":[{"role":"user","content":"old"},{"role":"assistant","content":"hello"},{"role":"user","content":"newer"}],"large":9007199254740993123456789}`)
	for _, row := range []model.History{
		{Outcome: "recorded", CacheStatus: "miss", Source: "upstream", Request: e.Recording.Request, RecordingID: e.Recording.ID, RevisionID: e.Revision.ID, CreatedAt: stamp(-3 * time.Minute)},
		{Outcome: "hit", CacheStatus: "hit", Source: "replay", Request: e.Recording.Request, RecordingID: e.Recording.ID, RevisionID: e.Revision.ID, CreatedAt: stamp(-2 * time.Minute)},
		{Outcome: "miss", CacheStatus: "miss", Source: "proxy", Request: later, CreatedAt: stamp(-time.Minute)},
	} {
		row.CollectionID, row.Route, row.Key = e.Recording.CollectionID, e.Recording.Route, "k"
		if err := db.AddHistory(ctx, row); err != nil {
			t.Fatal(err)
		}
	}

	w := request(t, h, http.MethodGet, "/api/insights?collection_id="+cid, nil)
	if w.Code != 200 {
		t.Fatalf("insights: %d %s", w.Code, w.Body.String())
	}
	raw := decodeAs[map[string]any](t, w.Body.Bytes())
	if got := keys(raw); got != "bucket,from,latency,models,routes,series,to,top_recordings,top_threads,totals" {
		t.Fatalf("insights keys %s", got)
	}
	totals := raw["totals"].(map[string]any)
	if got := keys(totals); got != "errors,hit_rate,hits,interrupted,lookup_hits,lookups,misses,recorded,replayed_tokens,requests,saved_cost,threads,upstream_cost,upstream_tokens" {
		t.Fatalf("totals keys %s", got)
	}
	if totals["requests"] != 3.0 || totals["hit_rate"] != 1.0/3 || totals["saved_cost"] != nil || totals["threads"] != 1.0 {
		t.Fatalf("totals %v", totals)
	}
	if got := keys(raw["latency"].(map[string]any)["replay"].(map[string]any)); got != "duration_ms,first_event_histogram,first_event_ms,histogram" {
		t.Fatalf("latency keys %s", got)
	}
	if w = request(t, h, http.MethodGet, "/api/insights?collection_id="+cid+"&model=nope", nil); !strings.Contains(w.Body.String(), `"requests":0`) {
		t.Fatalf("model filter: %s", w.Body.String())
	}

	w = request(t, h, http.MethodGet, "/api/threads?collection_id="+cid+"&limit=5", nil)
	threads := decodeAs[[]map[string]any](t, w.Body.Bytes())
	if w.Code != 200 || len(threads) != 1 {
		t.Fatalf("threads: %d %s", w.Code, w.Body.String())
	}
	if got := keys(threads[0]); got != "errors,first_at,hits,interrupted,last_at,last_outcome,latest,max_items,misses,model,opening,recorded,requests,route,thread,upstream_tokens" {
		t.Fatalf("thread keys %s", got)
	}
	if threads[0]["opening"] != "old" || threads[0]["latest"] != "newer" || threads[0]["max_items"] != 3.0 {
		t.Fatalf("thread %v", threads[0])
	}
	thread := threads[0]["thread"].(string)

	w = request(t, h, http.MethodGet, "/api/threads/"+thread+"?collection_id="+cid, nil)
	detail := decodeAs[model.ThreadDetail](t, w.Body.Bytes())
	if w.Code != 200 || len(detail.Turns) != 3 || detail.Turns[0].Response == nil || detail.Turns[2].Response != nil || detail.Turns[0].Response.Outcome != "stop" {
		t.Fatalf("thread detail: %d %s", w.Code, w.Body.String())
	}
	turn := decodeAs[map[string]any](t, w.Body.Bytes())["turns"].([]any)[0].(map[string]any)
	if got := keys(turn); got != "created_at,detail,duration_ms,first_event_ms,history_id,items,lookup_outcome,outcome,preview,recording_id,response,route,source" {
		t.Fatalf("turn keys %s", got)
	}
	if turn["route"] != "/v1/chat/completions" || turn["source"] != "upstream" {
		t.Fatalf("turn route/source %v", turn)
	}
	if got := keys(turn["response"].(map[string]any)); got != "cost,model,outcome,output_chars,reasoning_chars,tool_calls,usage" {
		t.Fatalf("response keys %s", got)
	}

	missID := detail.Turns[2].HistoryID
	w = request(t, h, http.MethodGet, "/api/history/"+itoa(missID)+"/nearest", nil)
	nearest := decodeAs[struct {
		Candidates []map[string]any `json:"candidates"`
	}](t, w.Body.Bytes())
	if w.Code != 200 || len(nearest.Candidates) != 1 || nearest.Candidates[0]["reason"] != "same_thread" || nearest.Candidates[0]["recording_id"] != float64(e.Recording.ID) {
		t.Fatalf("nearest: %d %s", w.Code, w.Body.String())
	}
	if got := keys(nearest.Candidates[0]); got != "created_at,items,model,preview,reason,recording_id,similarity,thread" {
		t.Fatalf("candidate keys %s", got)
	}
	if c := nearest.Candidates[0]; c["thread"] != thread || c["created_at"] != e.Revision.CreatedAt {
		t.Fatalf("candidate thread/created_at %v (want %s %s)", c, thread, e.Revision.CreatedAt)
	}
	if w = request(t, h, http.MethodHead, "/api/history/"+itoa(missID)+"/nearest", nil); w.Code != 200 {
		t.Fatalf("HEAD nearest: %d", w.Code)
	}
	if w = request(t, h, http.MethodHead, "/api/threads/"+thread+"?collection_id="+cid, nil); w.Code != 200 {
		t.Fatalf("HEAD thread: %d", w.Code)
	}

	w = request(t, h, http.MethodGet, "/api/recordings?collection_id="+cid, nil)
	recs := decodeAs[[]map[string]any](t, w.Body.Bytes())
	if len(recs) != 1 || recs[0]["response"] == nil || recs[0]["request"].(map[string]any)["opening"] != "old" {
		t.Fatalf("recordings: %s", w.Body.String())
	}
	w = request(t, h, http.MethodPost, "/api/insights?collection_id="+cid, nil)
	if allow := w.Header().Get("Allow"); w.Code != 405 || allow != "GET, HEAD" {
		t.Fatalf("insights POST: %d Allow %q", w.Code, allow)
	}
}
