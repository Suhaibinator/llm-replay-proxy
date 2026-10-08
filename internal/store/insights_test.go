package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"math/rand/v2"
	"testing"
	"time"

	matching "github.com/local/llm-replay-proxy/internal/match"
	"github.com/local/llm-replay-proxy/internal/model"
)

const chatRoute = "/v1/chat/completions"

// publishChat records a non-streaming chat request with the given response.
func publishChat(t *testing.T, s *Store, cid int64, request any, response string) model.Entry {
	t.Helper()
	return publishRoute(t, s, cid, chatRoute, request, response)
}

func publishRoute(t *testing.T, s *Store, cid int64, route string, request any, response string) model.Entry {
	t.Helper()
	req, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	key, input, err := matching.Key(route, req, nil)
	if err != nil {
		t.Fatal(err)
	}
	return publishOne(t, s, model.Recording{CollectionID: cid, Key: key, Route: route, Request: req, MatchingInput: input},
		model.Revision{Status: 200, Headers: map[string]string{"Content-Type": "application/json"}, Body: response, Source: "recorded"})
}

func chatRequest(modelName string, turns ...string) map[string]any {
	messages := []map[string]string{}
	for i, text := range turns {
		role := "user"
		if i%2 == 1 {
			role = "assistant"
		}
		messages = append(messages, map[string]string{"role": role, "content": text})
	}
	return map[string]any{"model": modelName, "messages": messages}
}

func chatResponse(modelName string, usage string) string {
	m := ""
	if modelName != "" {
		m = fmt.Sprintf(`"model":%q,`, modelName)
	}
	return fmt.Sprintf(`{"id":"c",%s"choices":[{"index":0,"finish_reason":"stop","message":{"role":"assistant","content":"ok"}}],"usage":%s}`, m, usage)
}

func ms(v int64) *int64 { return &v }

func floatIs(p *float64, want float64) bool { return p != nil && math.Abs(*p-want) < 1e-9 }

func TestInsightsAggregatesOutcomesTokensCostsAndLatency(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	cid := int64(1)
	base := time.Date(2026, 3, 1, 10, 0, 0, 0, time.UTC)
	at := func(minutes int) string {
		return base.Add(time.Duration(minutes) * time.Minute).Format(time.RFC3339Nano)
	}

	aReq := chatRequest("gpt-a", "hello a")
	a1 := publishChat(t, s, cid, aReq, chatResponse("gpt-a-2025", `{"prompt_tokens":100,"prompt_tokens_details":{"cached_tokens":40},"completion_tokens":50,"completion_tokens_details":{"reasoning_tokens":10},"total_tokens":150,"cost":0.01}`))
	bReq := chatRequest("claude-x", "hello b")
	b1 := publishChat(t, s, cid, bReq, chatResponse("", `{"prompt_tokens":10,"completion_tokens":5}`))
	// A later edit must not change what earlier rows are attributed.
	a2 := publishChat(t, s, cid, aReq, chatResponse("gpt-a-2025", `{"prompt_tokens":999,"completion_tokens":1}`))
	aBody, _ := json.Marshal(aReq)
	bBody, _ := json.Marshal(bReq)
	missBody, _ := json.Marshal(chatRequest("gpt-a", "never recorded"))

	row := func(minute int, outcome, lookup, source string, body []byte, rec model.Entry, revision int64, duration *int64) {
		addHistory(t, s, model.History{CollectionID: cid, Route: chatRoute, Key: "k", Request: body, Outcome: outcome, CacheStatus: lookup, Source: source,
			RecordingID: rec.Recording.ID, RevisionID: revision, DurationMS: duration, CreatedAt: at(minute)})
	}
	none := model.Entry{}
	row(-120, "hit", "hit", "replay", aBody, a1, a1.Revision.ID, ms(1)) // before the range
	row(0, "recorded", "miss", "upstream", aBody, a1, a1.Revision.ID, ms(120))
	row(1, "hit", "hit", "replay", aBody, a1, a1.Revision.ID, ms(5))
	row(2, "hit", "hit", "replay", aBody, a1, a1.Revision.ID, ms(20))
	row(3, "hit", "hit", "replay", aBody, a1, a1.Revision.ID, ms(70))
	row(4, "hit", "hit", "replay", aBody, a2, a2.Revision.ID, ms(50))
	row(5, "recorded", "bypass", "upstream", bBody, b1, b1.Revision.ID, ms(3000))
	row(65, "hit", "hit", "replay", bBody, b1, b1.Revision.ID, ms(100000))
	row(66, "hit", "hit", "replay", bBody, b1, b1.Revision.ID, nil)
	row(67, "miss", "miss", "proxy", missBody, none, 0, nil)
	row(68, "error", "miss", "upstream", missBody, none, 0, ms(10))
	row(69, "incomplete", "miss", "upstream", missBody, none, 0, ms(10))
	row(70, "interrupted", "hit", "replay", aBody, a1, 0, ms(2))
	row(71, "miss", "miss", "proxy", nil, none, 0, nil) // no request body

	from, to := base, base.Add(3*time.Hour)
	in, err := s.Insights(ctx, cid, from, to, InsightFilter{})
	if err != nil {
		t.Fatal(err)
	}
	tot := in.Totals
	if want := (model.OutcomeCounts{Requests: 13, Hits: 6, Misses: 2, Recorded: 2, Interrupted: 1, Errors: 2}); tot.OutcomeCounts != want {
		t.Fatalf("counts %+v, want %+v", tot.OutcomeCounts, want)
	}
	// Lookups that found a recording over all lookups, whatever followed:
	// the interrupted replay still hit, and the recorded, error and incomplete
	// calls after a lookup miss still missed; Record mode's bypass is neither.
	// 7 / (7 + 5).
	if !floatIs(tot.HitRate, 7.0/12) || tot.Lookups != 12 || tot.LookupHits != 7 {
		t.Fatalf("hit rate %v", tot.HitRate)
	}
	// /api/analytics reports the same rate for the same rows.
	if a, err := s.Analytics(ctx, cid, from, to); err != nil || !floatIs(a.HitRate, 7.0/12) {
		t.Fatalf("analytics hit rate %v (%v), insights %v", a.HitRate, err, *tot.HitRate)
	}
	var bucketHits, bucketRated int
	for _, b := range in.Series {
		if b.HitRate != nil {
			bucketRated++
			if b.Hits > 0 {
				bucketHits++
			}
		}
	}
	if bucketRated == 0 || bucketHits == 0 {
		t.Fatalf("series buckets carry no hit rate: %+v", in.Series)
	}
	if want := (model.TokenTotals{Input: 110, CachedInput: 40, Output: 55, Reasoning: 10}); tot.UpstreamTokens != want {
		t.Fatalf("upstream tokens %+v", tot.UpstreamTokens)
	}
	// Three hits of a1, one of a2 (served after the edit) and two of b1.
	if want := (model.TokenTotals{Input: 3*100 + 999 + 2*10, CachedInput: 3 * 40, Output: 3*50 + 1 + 2*5, Reasoning: 3 * 10}); tot.ReplayedTokens != want {
		t.Fatalf("replayed tokens %+v, want %+v", tot.ReplayedTokens, want)
	}
	if !floatIs(tot.UpstreamCost, 0.01) || !floatIs(tot.SavedCost, 0.03) {
		t.Fatalf("costs %v %v", tot.UpstreamCost, tot.SavedCost)
	}
	if tot.Threads != 3 || in.Bucket != "hour" || len(in.Series) != 3 || in.From != "2026-03-01T10:00:00Z" {
		t.Fatalf("threads %d bucket %s series %d from %s", tot.Threads, in.Bucket, len(in.Series), in.From)
	}
	if in.Series[0].Requests != 6 || in.Series[1].Requests != 7 || in.Series[0].UpstreamTokens.Input != 110 || in.Series[1].ReplayedTokens.Input != 20 {
		t.Fatalf("series %+v", in.Series)
	}
	names := map[string]model.ModelInsight{}
	for _, m := range in.Models {
		names[m.Model] = m
	}
	if len(in.Models) != 4 || names["gpt-a-2025"].Requests != 5 || names["claude-x"].Requests != 3 || names["gpt-a"].Requests != 4 || names["unknown"].Requests != 1 || in.Models[0].Model != "gpt-a-2025" {
		t.Fatalf("models %+v", in.Models)
	}
	if names["claude-x"].UpstreamCost != nil || names["claude-x"].SavedCost != nil || !floatIs(names["gpt-a-2025"].SavedCost, 0.03) {
		t.Fatalf("model costs %+v", names["claude-x"])
	}
	// Per-model hit rates by lookup: 4/(4+1 recorded after a miss); 2/2
	// (Record mode's bypass is no lookup); 1/4 (the interrupted replay hit;
	// the miss, error and incomplete calls missed); 0/1.
	if !floatIs(names["gpt-a-2025"].HitRate, 0.8) || !floatIs(names["claude-x"].HitRate, 1) || !floatIs(names["gpt-a"].HitRate, 0.25) || !floatIs(names["unknown"].HitRate, 0) {
		t.Fatalf("model hit rates %v %v %v %v", names["gpt-a-2025"].HitRate, names["claude-x"].HitRate, names["gpt-a"].HitRate, names["unknown"].HitRate)
	}
	if len(in.Routes) != 1 || in.Routes[0].Route != chatRoute || in.Routes[0].Requests != 13 || !floatIs(in.Routes[0].HitRate, 7.0/12) {
		t.Fatalf("routes %+v", in.Routes)
	}
	// Replay durations 5, 20, 70, 50, 100000, 2 (one hit has none).
	replay := in.Latency.Replay
	counts := make([]int64, len(replay.Histogram))
	for i, b := range replay.Histogram {
		counts[i] = b.Count
	}
	if fmt.Sprint(counts) != "[4 1 0 0 0 0 0 0 0 0 1]" || replay.Histogram[10].Le != nil || *replay.Histogram[0].Le != 50 || replay.Duration.Samples != 6 {
		t.Fatalf("replay histogram %v (%+v)", counts, replay.Duration)
	}
	if up := in.Latency.Upstream; up.Duration.Samples != 4 || *up.Duration.P50 != 10 || len(up.FirstEventHistogram) != 11 {
		t.Fatalf("upstream latency %+v", up)
	}
	if len(in.TopRecordings) != 2 || in.TopRecordings[0].RecordingID != a1.Recording.ID || in.TopRecordings[0].Hits != 4 ||
		in.TopRecordings[0].Preview != "hello a" || !floatIs(in.TopRecordings[0].SavedCost, 0.03) || in.TopRecordings[1].SavedCost != nil {
		t.Fatalf("top recordings %+v", in.TopRecordings)
	}
	if len(in.TopThreads) != 3 || in.TopThreads[0].Opening != "hello a" || in.TopThreads[0].Requests != 6 {
		t.Fatalf("top threads %+v", in.TopThreads)
	}

	only, err := s.Insights(ctx, cid, from, to, InsightFilter{Model: "claude-x"})
	if err != nil {
		t.Fatal(err)
	}
	if only.Totals.Requests != 3 || only.Totals.UpstreamCost != nil || only.Totals.SavedCost != nil || len(only.Models) != 1 ||
		only.Totals.ReplayedTokens.Input != 20 || len(only.TopRecordings) != 1 || only.TopRecordings[0].RecordingID != b1.Recording.ID {
		t.Fatalf("model filter %+v", only.Totals)
	}
	day, err := s.Insights(ctx, cid, base.AddDate(0, 0, -6), base.AddDate(0, 0, 1), InsightFilter{})
	if err != nil || day.Bucket != "day" || len(day.Series) != 8 || day.Totals.Requests != 14 {
		t.Fatalf("daily insights %s %d %d %v", day.Bucket, len(day.Series), day.Totals.Requests, err)
	}
	empty, err := s.Insights(ctx, cid, base.AddDate(1, 0, 0), base.AddDate(1, 0, 1), InsightFilter{})
	if err != nil || empty.Totals.HitRate != nil || empty.Totals.UpstreamCost != nil || empty.Models == nil || empty.TopThreads == nil {
		t.Fatalf("empty insights %+v %v", empty.Totals, err)
	}
	b, _ := json.Marshal(empty)
	var generic map[string]any
	_ = json.Unmarshal(b, &generic)
	if _, ok := generic["latency"].(map[string]any)["upstream"].(map[string]any)["histogram"].([]any); !ok {
		t.Fatalf("histogram not an array: %s", b)
	}
}

func TestDeletedRecordingKeepsHistoryButDropsAttribution(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	e := publishChat(t, s, 1, chatRequest("m", "x"), chatResponse("m", `{"prompt_tokens":5,"completion_tokens":1}`))
	body, _ := json.Marshal(chatRequest("m", "x"))
	now := time.Now().UTC()
	addHistory(t, s, model.History{CollectionID: 1, Route: chatRoute, Key: "k", Request: body, Outcome: "hit", RecordingID: e.Recording.ID, RevisionID: e.Revision.ID, CreatedAt: now.Format(time.RFC3339Nano)})
	if err := s.DeleteRecording(ctx, e.Recording.ID); err != nil {
		t.Fatal(err)
	}
	in, err := s.Insights(ctx, 1, now.Add(-time.Hour), now.Add(time.Hour), InsightFilter{})
	if err != nil || in.Totals.Hits != 1 || in.Totals.ReplayedTokens.Input != 0 || len(in.TopRecordings) != 0 {
		t.Fatalf("after delete %+v %+v %v", in.Totals, in.TopRecordings, err)
	}
}

func TestThreadsGroupByConversationAndListTurns(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	cid := int64(1)
	base := time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)
	at := func(minutes int) string {
		return base.Add(time.Duration(minutes) * time.Minute).Format(time.RFC3339Nano)
	}
	turn1 := chatRequest("m", "plan a trip")
	turn2 := chatRequest("m", "plan a trip", "where to?", "Lisbon please")
	turn3 := chatRequest("m", "plan a trip", "where to?", "Lisbon please", "booked", "and hotels")
	r1 := publishChat(t, s, cid, turn1, chatResponse("m-1", `{"prompt_tokens":10,"completion_tokens":2}`))
	r2 := publishChat(t, s, cid, turn2, chatResponse("m-1", `{"prompt_tokens":30,"completion_tokens":4}`))
	other := chatRequest("m", "unrelated")
	body := func(v any) []byte { b, _ := json.Marshal(v); return b }

	add := func(minute int, outcome string, req any, rec model.Entry) {
		h := model.History{CollectionID: cid, Route: chatRoute, Key: "k", Outcome: outcome, RecordingID: rec.Recording.ID, CreatedAt: at(minute)}
		if req != nil {
			h.Request = body(req)
		}
		if outcome == "hit" || outcome == "recorded" {
			h.RevisionID = rec.Revision.ID
		}
		addHistory(t, s, h)
	}
	add(10, "recorded", turn1, r1)
	add(20, "recorded", turn2, r2)
	add(5, "miss", other, model.Entry{})
	add(30, "hit", turn2, r2)
	add(40, "miss", turn3, model.Entry{})
	add(50, "error", nil, model.Entry{})
	add(15, "hit", turn1, r1) // logged out of order: last_at must still be the latest

	threads, err := s.Threads(ctx, cid, 50, time.Time{}, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if len(threads) != 2 {
		t.Fatalf("threads %+v", threads)
	}
	trip := threads[0]
	if trip.Opening != "plan a trip" || trip.Latest != "and hotels" || trip.MaxItems != 5 || trip.Requests != 5 || trip.Hits != 2 ||
		trip.Recorded != 2 || trip.Misses != 1 || trip.FirstAt != at(10) || trip.LastAt != at(40) || trip.LastOutcome != "miss" ||
		trip.UpstreamTokens.Input != 40 || trip.Route != chatRoute || trip.Model != "m" {
		t.Fatalf("trip thread %+v", trip)
	}
	if threads[1].Opening != "unrelated" || threads[1].Requests != 1 {
		t.Fatalf("other thread %+v", threads[1])
	}
	if limited, err := s.Threads(ctx, cid, 1, time.Time{}, time.Time{}); err != nil || len(limited) != 1 || limited[0].Thread != trip.Thread {
		t.Fatalf("limit: %+v %v", limited, err)
	}
	ranged, err := s.Threads(ctx, cid, 50, base, base.Add(12*time.Minute))
	if err != nil || len(ranged) != 2 || ranged[0].Requests != 1 || ranged[0].LastAt != at(10) {
		t.Fatalf("ranged: %+v %v", ranged, err)
	}

	detail, err := s.Thread(ctx, cid, trip.Thread)
	if err != nil {
		t.Fatal(err)
	}
	if detail.ThreadSummary != trip || len(detail.Turns) != 5 {
		t.Fatalf("detail %+v", detail.ThreadSummary)
	}
	var order []string
	for _, turn := range detail.Turns {
		order = append(order, fmt.Sprintf("%s/%d/%v", turn.Outcome, turn.Items, turn.Response != nil))
	}
	if fmt.Sprint(order) != "[recorded/1/true hit/1/true recorded/3/true hit/3/true miss/5/false]" {
		t.Fatalf("turns %v", order)
	}
	if r := detail.Turns[2].Response; r.Model != "m-1" || *r.Usage.Input != 30 || detail.Turns[4].Preview != "and hotels" {
		t.Fatalf("turn response %+v", r)
	}
	if _, err = s.Thread(ctx, cid, "0000000000000000"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown thread: %v", err)
	}
	if _, err = s.Thread(ctx, 99, trip.Thread); !errors.Is(err, ErrNotFound) {
		t.Fatalf("other collection: %v", err)
	}
}

func TestRecordingsCarryResponseSummary(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	publishOne(t, s, testRecording(1, "first"), testRevision("resp_a", "recorded"))
	publishChat(t, s, 1, chatRequest("m", "x"), chatResponse("m-2", `{"prompt_tokens":5,"completion_tokens":1,"cost":0.5}`))
	for range 2 { // the second read uses the saved summaries
		list, err := s.Recordings(ctx, 1)
		if err != nil {
			t.Fatal(err)
		}
		if len(list) != 2 || list[0].Response == nil || list[0].Response.Model != "m-2" || !floatIs(list[0].Response.Cost, 0.5) ||
			list[1].Response == nil || list[1].Response.Outcome != "completed" || list[1].Response.OutputChars != 2 {
			t.Fatalf("responses %+v %+v", list[0].Response, list[1].Response)
		}
	}
	var saved int
	if err := s.db.QueryRow("SELECT count(*) FROM revisions WHERE response_summary_v=?", responseSummaryVersion).Scan(&saved); err != nil || saved != 2 {
		t.Fatalf("saved %d %v", saved, err)
	}
}

func TestNearestComparesSmallRequestsWithFineChunks(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	system := bigText(3, 1500)
	ask := func(modelName, question string) map[string]any {
		return map[string]any{"model": modelName, "temperature": 0.2, "messages": []map[string]string{{"role": "system", "content": system}, {"role": "user", "content": question}}}
	}
	resp := chatResponse("m", `{"prompt_tokens":1,"completion_tokens":1}`)
	similar := publishChat(t, s, 1, ask("m", "what is the refund policy for annual plans?"), resp)
	publishChat(t, s, 1, ask("other-model", "what is the refund policy for annual plans?"), resp)
	publishChat(t, s, 1, chatRequest("m", "unrelated"), resp)
	body, _ := json.Marshal(ask("m", "what is the refund policy for monthly plans?"))
	addHistory(t, s, model.History{CollectionID: 1, Route: chatRoute, Key: "k", Request: body, Outcome: "miss"})
	rows, _ := s.History(ctx, 1, 1)
	got, err := s.Nearest(ctx, rows[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	// One stored chunk each, so only the fine comparison finds the overlap;
	// the other model's request is not compared.
	if len(got) != 1 || got[0].RecordingID != similar.Recording.ID || got[0].Reason != "shared_content" || got[0].Similarity < 0.8 || got[0].Similarity >= 1 {
		t.Fatalf("candidates %+v", got)
	}
}

// bigText is incompressible-looking text large enough to span many chunks.
func bigText(seed uint64, n int) string {
	r := rand.New(rand.NewPCG(seed, seed))
	const letters = "abcdefghijklmnopqrstuvwxyz     "
	b := make([]byte, n)
	for i := range b {
		b[i] = letters[r.IntN(len(letters))]
	}
	return string(b)
}

func TestNearestRanksSameThreadThenSharedContent(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	cid := int64(1)
	shared := bigText(1, 96<<10)
	request := func(input ...string) map[string]any {
		items := []map[string]string{}
		for i, text := range input {
			role := "user"
			if i%2 == 1 {
				role = "assistant"
			}
			items = append(items, map[string]string{"role": role, "content": text})
		}
		return map[string]any{"model": "m", "instructions": shared, "input": items}
	}
	resp := `{"id":"r","status":"completed","output":[{"type":"message","status":"completed","role":"assistant","content":[{"type":"output_text","text":"ok"}]}]}`
	missed := request("hello", "hi", "tell me more")
	sameThread := publishRoute(t, s, cid, "/v1/responses", request("hello"), resp)
	sharedContent := publishRoute(t, s, cid, "/v1/responses", request("a different question"), resp)
	publishRoute(t, s, cid, "/v1/responses", map[string]any{"model": "m", "input": "tiny unrelated " + bigText(2, 8<<10)}, resp)
	exact := publishRoute(t, s, cid, "/v1/responses", missed, resp)
	body, _ := json.Marshal(missed)
	addHistory(t, s, model.History{CollectionID: cid, Route: "/v1/responses", Key: exact.Recording.Key, Request: body, Outcome: "miss"})
	rows, err := s.History(ctx, cid, 1)
	if err != nil {
		t.Fatal(err)
	}
	got, err := s.Nearest(ctx, rows[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].RecordingID != sameThread.Recording.ID || got[0].Reason != "same_thread" ||
		got[1].RecordingID != sharedContent.Recording.ID || got[1].Reason != "shared_content" {
		t.Fatalf("candidates %+v", got)
	}
	if got[1].Similarity < 0.8 || got[1].Similarity > 1 || got[0].Preview != "hello" || got[0].Items != 1 || got[1].Model != "m" {
		t.Fatalf("candidate details %+v", got)
	}
	if _, err = s.Nearest(ctx, 9999); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing row: %v", err)
	}
	addHistory(t, s, model.History{CollectionID: cid, Route: "/v1/responses", Key: "k", Outcome: "error"})
	rows, _ = s.History(ctx, cid, 1)
	if none, err := s.Nearest(ctx, rows[0].ID); err != nil || none == nil || len(none) != 0 {
		t.Fatalf("bodiless row: %+v %v", none, err)
	}
}
