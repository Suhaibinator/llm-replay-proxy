package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	matching "github.com/local/llm-replay-proxy/internal/match"
	"github.com/local/llm-replay-proxy/internal/model"
)

func openTest(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "recordings.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func testRecording(cid int64, key string) model.Recording {
	req := []byte(fmt.Sprintf(`{"model":"m","input":%q,"stream":true}`, key))
	hash, input, err := matching.Key("/v1/responses", req, nil)
	if err != nil {
		panic(err)
	}
	return model.Recording{CollectionID: cid, Key: hash, Route: "/v1/responses", Request: req, MatchingInput: input, Streaming: true}
}
func messagesRecording(cid int64, key string) model.Recording {
	req := []byte(fmt.Sprintf(`{"model":"m","messages":[{"role":"user","content":%q}],"stream":true}`, key))
	hash, input, err := matching.Key("/v1/messages", req, nil)
	if err != nil {
		panic(err)
	}
	return model.Recording{CollectionID: cid, Key: hash, Route: "/v1/messages", Request: req, MatchingInput: input, Streaming: true}
}
func testRevision(body, source string) model.Revision {
	return model.Revision{Status: 200, Headers: map[string]string{"Content-Type": "text/event-stream"}, Events: []model.Event{
		{Data: "event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"item_id\":\"m1\",\"output_index\":0,\"content_index\":0,\"delta\":\"ok\"}\n\n", OffsetMS: 1},
		{Data: "event: response.output_text.done\ndata: {\"type\":\"response.output_text.done\",\"item_id\":\"m1\",\"output_index\":0,\"content_index\":0,\"text\":\"ok\"}\n\n", OffsetMS: 2},
		{Data: fmt.Sprintf("event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":%q,\"status\":\"completed\",\"output\":[{\"id\":\"m1\",\"type\":\"message\",\"role\":\"assistant\",\"content\":[{\"type\":\"output_text\",\"text\":\"ok\"}]}]}}\n\n", body), OffsetMS: 3},
	}, Source: source}
}

func TestDefaultsPublishRestoreAndReopen(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "db.sqlite")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	settings, err := s.Settings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if settings.Mode != "replay" || settings.ActiveCollectionID == 0 {
		t.Fatalf("bad defaults: %+v", settings)
	}
	one, err := s.Publish(ctx, testRecording(settings.ActiveCollectionID, "key"), testRevision(`{"id":"state_old"}`, "recorded"))
	if err != nil {
		t.Fatal(err)
	}
	two, err := s.Publish(ctx, testRecording(settings.ActiveCollectionID, "key"), testRevision(`{"id":"state_new"}`, "edited"))
	if err != nil {
		t.Fatal(err)
	}
	if one.Recording.ID != two.Recording.ID || one.Revision.ID == two.Revision.ID {
		t.Fatal("refresh did not create immutable revision")
	}
	entry, err := s.Restore(ctx, one.Recording.ID, one.Revision.ID)
	if err != nil {
		t.Fatal(err)
	}
	if entry.Revision.ID != one.Revision.ID {
		t.Fatal("restore did not activate old revision")
	}
	if _, err = s.Restore(ctx, one.Recording.ID, 999999); !errors.Is(err, ErrNotFound) {
		t.Fatalf("restore missing: %v", err)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	entry, err = s.Lookup(ctx, settings.ActiveCollectionID, testRecording(settings.ActiveCollectionID, "key").Key)
	if err != nil || entry.Revision.ID != one.Revision.ID {
		t.Fatalf("reopen: %+v %v", entry, err)
	}
}

func TestCollectionsHistoryProviderStateAndMemoryIsolation(t *testing.T) {
	ctx := context.Background()
	a, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	b, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	c, err := a.CreateCollection(ctx, "other", []string{"/metadata/id"})
	if err != nil {
		t.Fatal(err)
	}
	bc, _ := b.Collections(ctx)
	if len(bc) != 1 {
		t.Fatalf("memory stores leaked: %+v", bc)
	}
	e, err := a.Publish(ctx, testRecording(c.ID, "x"), testRevision(`{"id":"body_state"}`, "recorded"))
	if err != nil {
		t.Fatal(err)
	}
	_, err = a.Publish(ctx, testRecording(c.ID, "x"), testRevision("new_state", "edited"))
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{`{"id":"body_state"}`} {
		ok, err := a.HasProviderState(ctx, c.ID, id)
		if err != nil || !ok {
			t.Fatalf("state %q: %v %v", id, ok, err)
		}
	}
	if ok, err := a.HasProviderState(ctx, c.ID, "ok"); err != nil || ok {
		t.Fatalf("answer text produced state false positive: %v %v", ok, err)
	}
	multi := testRevision("multiline_state", "edited")
	multi.Events[len(multi.Events)-1].Data = "event: response.completed\ndata: {\"type\":\"response.completed\",\ndata: \"response\":{\"id\":\"multiline_state\",\"status\":\"completed\",\"output\":[{\"id\":\"m1\",\"type\":\"message\",\"role\":\"assistant\",\"content\":[{\"type\":\"output_text\",\"text\":\"ok\"}]}]}}\n\n"
	if _, err = a.Publish(ctx, testRecording(c.ID, "multi"), multi); err != nil {
		t.Fatal(err)
	}
	if ok, err := a.HasProviderState(ctx, c.ID, "multiline_state"); err != nil || !ok {
		t.Fatalf("multiline state: %v %v", ok, err)
	}
	stateEvents := testRevision("completed_state", "edited")
	for i := range stateEvents.Events {
		stateEvents.Events[i].Data = strings.ReplaceAll(stateEvents.Events[i].Data, "m1", "output_item_state")
	}
	baseEvents := stateEvents.Events
	stateEvents.Events = []model.Event{{Data: "event: response.output_item.added\ndata: {\"type\":\"response.output_item.added\",\"output_index\":0,\"note\":\"arbitrary_not_state\",\"item\":{\"id\":\"output_item_state\",\"type\":\"message\",\"content\":[]}}\n\n"}}
	stateEvents.Events = append(stateEvents.Events, baseEvents[:2]...)
	stateEvents.Events = append(stateEvents.Events, model.Event{Data: "event: response.output_item.done\ndata: {\"type\":\"response.output_item.done\",\"output_index\":0,\"item\":{\"id\":\"output_item_state\",\"type\":\"message\",\"role\":\"assistant\",\"content\":[{\"type\":\"output_text\",\"text\":\"ok\"}]}}\n\n", OffsetMS: 3})
	stateEvents.Events = append(stateEvents.Events, baseEvents[2:]...)
	if _, err = a.Publish(ctx, testRecording(c.ID, "state-fields"), stateEvents); err != nil {
		t.Fatal(err)
	}
	messageRevision := model.Revision{Status: 200, Headers: map[string]string{"Content-Type": "text/event-stream"}, Source: "recorded", Events: []model.Event{
		{Data: "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"ordinary_message_id\",\"type\":\"message\",\"role\":\"assistant\",\"content\":[],\"container\":{\"id\":\"anthropic_container_state\"},\"stop_reason\":null}}\n\n"},
		{Data: "event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\n", OffsetMS: 1},
		{Data: "event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"ok\"}}\n\n", OffsetMS: 2},
		{Data: "event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\n", OffsetMS: 3},
		{Data: "event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"}}\n\n", OffsetMS: 4},
		{Data: "event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n", OffsetMS: 5},
	}}
	if _, err = a.Publish(ctx, messagesRecording(c.ID, "anthropic-state"), messageRevision); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"output_item_state", "anthropic_container_state"} {
		if ok, err := a.HasProviderState(ctx, c.ID, id); err != nil || !ok {
			t.Fatalf("known state %q: %v %v", id, ok, err)
		}
	}
	for _, id := range []string{"ordinary_message_id", "arbitrary_not_state"} {
		if ok, err := a.HasProviderState(ctx, c.ID, id); err != nil || ok {
			t.Fatalf("non-state %q matched: %v %v", id, ok, err)
		}
	}
	if err = a.AddHistory(ctx, model.History{CollectionID: c.ID, Route: "/v1/responses", Key: "x", Request: []byte(`{}`), Outcome: "hit", RecordingID: e.Recording.ID}); err != nil {
		t.Fatal(err)
	}
	hs, err := a.History(ctx, c.ID, 10)
	if err != nil || len(hs) != 1 || hs[0].Outcome != "hit" {
		t.Fatalf("history: %+v %v", hs, err)
	}
}

func TestConcurrentPublishAndSnapshotRoundTrip(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	settings, _ := s.Settings(ctx)
	c2, err := s.CreateCollection(ctx, "excluded", nil)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = s.Publish(ctx, testRecording(c2.ID, "other"), testRevision(`{}`, "recorded"))
	const n = 12
	var wg sync.WaitGroup
	errs := make(chan error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, e := s.Publish(ctx, testRecording(settings.ActiveCollectionID, fmt.Sprintf("k%d", i)), testRevision(fmt.Sprintf(`{"n":%d}`, i), "recorded"))
			errs <- e
		}(i)
	}
	wg.Wait()
	close(errs)
	for e := range errs {
		if e != nil {
			t.Fatal(e)
		}
	}
	dest := filepath.Join(t.TempDir(), "export.db")
	if err = s.Export(ctx, settings.ActiveCollectionID, dest); err != nil {
		t.Fatal(err)
	}
	recv := openTest(t)
	cols, err := recv.Import(ctx, dest)
	if err != nil {
		t.Fatal(err)
	}
	if len(cols) != 1 {
		t.Fatalf("imported collections=%d", len(cols))
	}
	rs, err := recv.Recordings(ctx, cols[0].ID)
	if err != nil || len(rs) != n {
		t.Fatalf("recordings=%d err=%v", len(rs), err)
	}
	if _, err = recv.Import(ctx, dest); err != nil {
		t.Fatal(err)
	}
	all, err := recv.Collections(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 3 {
		t.Fatalf("collections=%d", len(all))
	}
}

func TestSettingsValidation(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	cur, _ := s.Settings(ctx)
	cur.DelayMultiplier = 0.5
	cur.FirstEventDelayMS = 25
	if err := s.SetSettings(ctx, cur); err != nil {
		t.Fatal(err)
	}
	cur.Mode = "bad"
	if err := s.SetSettings(ctx, cur); err == nil {
		t.Fatal("accepted bad mode")
	}
}

func TestHistoryTimingAndAnalytics(t *testing.T) {
	s := openTest(t)
	// A row without a source or timings, counted as "legacy" by analytics.
	if err := s.AddHistory(context.Background(), model.History{CollectionID: 1, Route: "/v1/responses", Key: "old", Request: []byte(`{}`), Outcome: "hit", CreatedAt: "2026-01-01T00:00:00.100Z"}); err != nil {
		t.Fatal(err)
	}
	from := time.Date(2026, 1, 1, 0, 0, 0, 500_000_000, time.UTC)
	to := from.Add(time.Hour)
	d0 := int64(10)
	f0 := int64(4)
	d1 := int64(30)
	for i := 0; i < 250; i++ {
		stamp := from.Add(time.Duration(i) * time.Second).Format(time.RFC3339Nano)
		source, outcome := "upstream", "recorded"
		duration, first := &d1, (*int64)(nil)
		if i < 100 {
			source = "replay"
			outcome = "hit"
			duration = &d0
			first = &f0
		}
		if i >= 200 {
			outcome = "miss"
		}
		if err := s.AddHistory(context.Background(), model.History{CollectionID: 1, Route: "/v1/responses", Key: fmt.Sprint(i), Request: []byte(`{}`), Outcome: outcome, Source: source, DurationMS: duration, FirstEventMS: first, CreatedAt: stamp}); err != nil {
			t.Fatal(err)
		}
	}
	a, err := s.Analytics(context.Background(), 1, from, to)
	if err != nil {
		t.Fatal(err)
	}
	if a.Total != 250 || a.LifetimeTotal != 251 || a.Hits != 100 || a.Misses != 50 || a.Recorded != 100 {
		t.Fatalf("bad totals: %+v", a)
	}
	if a.Sources["replay"].Duration.Samples != 100 || *a.Sources["replay"].Duration.P50 != 10 || a.Sources["upstream"].FirstEvent.Samples != 0 {
		t.Fatalf("bad timing: %+v", a.Sources)
	}
	// The legacy row precedes the fractional lower bound and has no fabricated timing.
	if _, ok := a.Sources["legacy"]; ok {
		t.Fatalf("fractional range admitted legacy row: %+v", a)
	}
}

func TestOptimisticRevisionActivation(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	settings, _ := s.Settings(ctx)
	initial, err := s.Publish(ctx, testRecording(settings.ActiveCollectionID, "optimistic"), testRevision("initial", "recorded"))
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, err := s.PublishIfActive(ctx, testRecording(settings.ActiveCollectionID, "optimistic"), testRevision(fmt.Sprintf("edit-%d", i), "edited"), initial.Revision.ID)
			results <- err
		}(i)
	}
	wg.Wait()
	close(results)
	wins, conflicts := 0, 0
	for err := range results {
		if err == nil {
			wins++
		} else if errors.Is(err, ErrConflict) {
			conflicts++
		} else {
			t.Fatal(err)
		}
	}
	if wins != 1 || conflicts != 1 {
		t.Fatalf("wins=%d conflicts=%d", wins, conflicts)
	}
	current, err := s.Get(ctx, initial.Recording.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.RestoreIfActive(ctx, initial.Recording.ID, initial.Revision.ID, initial.Revision.ID); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale restore: %v", err)
	}
	if _, err = s.RestoreIfActive(ctx, initial.Recording.ID, initial.Revision.ID, current.Revision.ID); err != nil {
		t.Fatal(err)
	}
	revisions, err := s.Revisions(ctx, initial.Recording.ID)
	if err != nil || len(revisions) != 2 {
		t.Fatalf("revisions=%d err=%v", len(revisions), err)
	}
}

func TestConnectionPragmas(t *testing.T) {
	s := openTest(t)
	var foreign, busy int
	if err := s.db.QueryRow("PRAGMA foreign_keys").Scan(&foreign); err != nil {
		t.Fatal(err)
	}
	if err := s.db.QueryRow("PRAGMA busy_timeout").Scan(&busy); err != nil {
		t.Fatal(err)
	}
	if foreign != 1 || busy != 5000 {
		t.Fatalf("foreign_keys=%d busy_timeout=%d", foreign, busy)
	}
}

func TestDatabasePathWithURICharacters(t *testing.T) {
	path := filepath.Join(t.TempDir(), "recordings ? #.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestImportRejectsTamperedSnapshot(t *testing.T) {
	for _, tc := range []struct{ name, sql string }{{"stream flag", `UPDATE recordings SET streaming=0`}, {"unsafe header", `UPDATE revisions SET headers='{"Set-Cookie":"secret"}'`}, {"missing active", `UPDATE recordings SET active_revision_id=NULL`}} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			source := openTest(t)
			settings, _ := source.Settings(ctx)
			if _, err := source.Publish(ctx, testRecording(settings.ActiveCollectionID, "tamper"), testRevision("state", "recorded")); err != nil {
				t.Fatal(err)
			}
			snapshot := filepath.Join(t.TempDir(), "snapshot.db")
			if err := source.Export(ctx, settings.ActiveCollectionID, snapshot); err != nil {
				t.Fatal(err)
			}
			db, err := sql.Open("sqlite", snapshot)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = db.Exec(tc.sql); err != nil {
				db.Close()
				t.Fatal(err)
			}
			db.Close()
			dest := openTest(t)
			if _, err = dest.Import(ctx, snapshot); err == nil {
				t.Fatal("tampered snapshot imported")
			}
		})
	}
}

func TestAnalyticsDateWindowHandlesOffsets(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	settings, _ := s.Settings(ctx)
	from := time.Date(2026, 10, 3, 0, 0, 0, 0, time.UTC)
	to := from.Add(24 * time.Hour)
	for i, stamp := range []string{
		"2025-01-01T00:00:00Z",      // far outside: excluded by SQL
		"2026-10-02T23:59:59.9Z",    // just before: excluded
		"2026-10-02T20:00:00-12:00", // 10-03T08:00Z: local date precedes
		"2026-10-04T13:00:00+14:00", // 10-03T23:00Z: local date follows
		"2026-10-03T12:00:00.5Z",    // plainly inside
		"2026-10-04T00:00:00Z",      // end is exclusive
		"2026-10-03 12:00:00",       // unparseable: skipped
	} {
		if err := s.AddHistory(ctx, model.History{CollectionID: settings.ActiveCollectionID, Route: "/v1/responses", Key: fmt.Sprint(i), Request: []byte(`{}`), Outcome: "hit", Source: "replay", CreatedAt: stamp}); err != nil {
			t.Fatal(err)
		}
	}
	a, err := s.Analytics(ctx, settings.ActiveCollectionID, from, to)
	if err != nil {
		t.Fatal(err)
	}
	if a.Total != 3 || a.LifetimeTotal != 7 {
		t.Fatalf("total %d lifetime %d, want 3 and 7", a.Total, a.LifetimeTotal)
	}
}
