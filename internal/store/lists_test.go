package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/local/llm-replay-proxy/internal/model"
)

func addHistory(t *testing.T, s *Store, h model.History) {
	t.Helper()
	if h.Route == "" {
		h.Route = "/v1/responses"
	}
	if err := s.AddHistory(context.Background(), h); err != nil {
		t.Fatal(err)
	}
}

func TestRecordingsListSummarizesAndCountsHits(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	settings, _ := s.Settings(ctx)
	cid := settings.ActiveCollectionID
	a := publishOne(t, s, testRecording(cid, "first"), testRevision("resp_a", "recorded"))
	b := publishOne(t, s, testRecording(cid, "second"), testRevision("resp_b", "recorded"))
	publishOne(t, s, testRecording(cid, "second"), testRevision("resp_b2", "edit"))
	for i := range 3 {
		addHistory(t, s, model.History{CollectionID: cid, Key: a.Recording.Key, Request: a.Recording.Request, Outcome: "hit", RecordingID: a.Recording.ID, CreatedAt: fmt.Sprintf("2026-01-01T00:00:0%dZ", i)})
	}
	addHistory(t, s, model.History{CollectionID: cid, Key: "k", Request: []byte(`{"input":"x"}`), Outcome: "miss"})
	list, err := s.Recordings(ctx, cid)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 || list[0].ID != b.Recording.ID || list[1].ID != a.Recording.ID {
		t.Fatalf("order: %+v", list)
	}
	if list[0].Revisions != 2 || list[0].Source != "edit" || list[0].Hits != 0 || list[0].Summary == nil || list[0].Summary.Preview != "second" {
		t.Fatalf("edited recording: %+v %+v", list[0], list[0].Summary)
	}
	if list[1].Hits != 3 || list[1].LastHitAt != "2026-01-01T00:00:02Z" || list[1].Request != nil {
		t.Fatalf("replayed recording: %+v", list[1])
	}
	// Bodies are summarized when stored, the history-only one included.
	var stored int
	if err = s.db.QueryRow("SELECT count(*) FROM bodies WHERE summary IS NOT NULL AND summary_v=?", summaryVersion).Scan(&stored); err != nil || stored != 3 {
		t.Fatalf("summaries saved for %d bodies (%v), want all 3", stored, err)
	}
}

// A body stored without a current summary (by an older build, or before a
// summaryVersion change) is summarized by the read that needs it, which does
// not wait for the summary to be saved; the writer saves it afterwards.
func TestStaleSummaryIsComputedOnReadAndSavedInBackground(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "db.sqlite")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	addHistory(t, s, model.History{CollectionID: 1, Key: "k", Request: []byte(`{"input":"legacy"}`), Outcome: "miss"})
	if _, err = s.db.Exec("UPDATE bodies SET summary=NULL,summary_v=0,thread=NULL"); err != nil {
		t.Fatal(err)
	}
	list, err := s.History(ctx, 1, 10)
	if err != nil || len(list) != 1 || list[0].Summary == nil || list[0].Summary.Preview != "legacy" {
		t.Fatalf("history %+v %v", list, err)
	}
	// Close commits every queued write, the background summary included.
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	if s, err = Open(path); err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	var version int
	if err = s.db.QueryRow("SELECT summary_v FROM bodies").Scan(&version); err != nil || version != summaryVersion {
		t.Fatalf("saved summary version %d %v", version, err)
	}
}

func TestReadPoolRejectsWrites(t *testing.T) {
	s := openTest(t)
	if _, err := s.rdb.Exec("UPDATE settings SET mode='record'"); err == nil {
		t.Fatal("the read pool accepted a write")
	}
}

// Concurrent publications and history rows are committed in groups; every
// caller sees its own write once its call returns, and none is lost.
func TestConcurrentWritesAreAllCommittedAndVisible(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	var wg sync.WaitGroup
	errs := make(chan error, 200)
	for i := range 100 {
		wg.Go(func() {
			rec := testRecording(1, fmt.Sprint("key-", i))
			e, err := s.Publish(ctx, rec, testRevision(fmt.Sprint("resp_", i), "recorded"))
			if err != nil {
				errs <- err
				return
			}
			if got, err := s.Lookup(ctx, 1, rec.Key); err != nil || got.Revision.ID != e.Revision.ID {
				errs <- fmt.Errorf("lookup after publish: %v %v", got.Revision.ID, err)
			}
			errs <- s.AddHistory(ctx, model.History{CollectionID: 1, Route: "/v1/responses", Key: rec.Key, Request: rec.Request, Outcome: "recorded", RecordingID: e.Recording.ID})
		})
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	var recordings, history int
	if err := s.db.QueryRow("SELECT (SELECT count(*) FROM recordings),(SELECT count(*) FROM history)").Scan(&recordings, &history); err != nil || recordings != 100 || history != 100 {
		t.Fatalf("recordings %d history %d %v", recordings, history, err)
	}
}

// A failing write in a group is rolled back alone; the others commit.
func TestGroupCommitIsolatesFailingWrite(t *testing.T) {
	s := openTest(t)
	insert := func(name string) func(context.Context, *sql.Tx) error {
		return func(ctx context.Context, tx *sql.Tx) error {
			_, err := tx.ExecContext(ctx, "INSERT INTO collections(name,exclusions,created_at) VALUES(?,'[]','2026-01-01T00:00:00Z')", name)
			return err
		}
	}
	failing := func(ctx context.Context, tx *sql.Tx) error {
		if err := insert("partial")(ctx, tx); err != nil {
			return err
		}
		return insert("Default")(ctx, tx) // duplicate name
	}
	ops := []*writeOp{
		{fn: insert("a"), durable: true, done: make(chan error, 1)},
		{fn: failing, done: make(chan error, 1)},
		{fn: insert("b"), done: make(chan error, 1)},
	}
	s.w.commit(ops)
	if err := <-ops[0].done; err != nil {
		t.Fatal(err)
	}
	if err := <-ops[1].done; err == nil {
		t.Fatal("duplicate collection name accepted")
	}
	if err := <-ops[2].done; err != nil {
		t.Fatal(err)
	}
	var names string
	if err := s.db.QueryRow("SELECT group_concat(name,',') FROM (SELECT name FROM collections ORDER BY id)").Scan(&names); err != nil || names != "Default,a,b" {
		t.Fatalf("collections %q %v", names, err)
	}
	var synchronous int
	if err := s.db.QueryRow("PRAGMA synchronous").Scan(&synchronous); err != nil || synchronous != 2 {
		t.Fatalf("writer left at synchronous=%d %v, want FULL", synchronous, err)
	}
}

func TestWritesAfterCloseFail(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "db.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	s.Close()
	if err = s.AddHistory(context.Background(), model.History{CollectionID: 1, Outcome: "miss"}); !errors.Is(err, errClosed) {
		t.Fatalf("write after close: %v", err)
	}
}

func TestHistoryAfterReturnsOnlyNewerRows(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	for i := range 5 {
		addHistory(t, s, model.History{CollectionID: 1, Key: fmt.Sprint(i), Request: []byte(fmt.Sprintf(`{"input":"%d"}`, i)), Outcome: "miss"})
	}
	addHistory(t, s, model.History{CollectionID: 1, Key: "none", Outcome: "error"})
	all, err := s.History(ctx, 1, 10)
	if err != nil || len(all) != 6 {
		t.Fatalf("history: %d %v", len(all), err)
	}
	if all[0].Summary != nil || all[1].Summary == nil || all[1].Summary.Preview != "4" {
		t.Fatalf("summaries: %+v %+v", all[0].Summary, all[1].Summary)
	}
	newer, err := s.HistoryAfter(ctx, 1, all[2].ID, 10, "")
	if err != nil || len(newer) != 2 || newer[0].ID != all[0].ID || newer[1].ID != all[1].ID {
		t.Fatalf("after %d: %+v %v", all[2].ID, newer, err)
	}
	item, err := s.HistoryItem(ctx, all[0].ID)
	if err != nil || string(item.Request) != "null" || item.Summary != nil {
		t.Fatalf("bodiless item: %s %+v %v", item.Request, item.Summary, err)
	}
}

func TestHistoryBeforePagesBackAndFiltersOutcomes(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	outcomes := []string{"hit", "miss", "error", "hit", "miss"}
	for i, o := range outcomes {
		addHistory(t, s, model.History{CollectionID: 1, Key: fmt.Sprint(i), Outcome: o})
	}
	all, err := s.HistoryBefore(ctx, 1, 0, 10, "", nil)
	if err != nil || len(all) != 5 {
		t.Fatalf("all: %d %v", len(all), err)
	}
	older, err := s.HistoryBefore(ctx, 1, all[1].ID, 2, "", nil)
	if err != nil || len(older) != 2 || older[0].ID != all[2].ID || older[1].ID != all[3].ID {
		t.Fatalf("before %d: %+v %v", all[1].ID, older, err)
	}
	failed, err := s.HistoryBefore(ctx, 1, 0, 10, "", []string{"miss", "error"})
	if err != nil || len(failed) != 3 || failed[0].Outcome != "miss" || failed[1].Outcome != "error" || failed[2].Outcome != "miss" {
		t.Fatalf("failures: %+v %v", failed, err)
	}
}

func TestRevisionDetailLoadsItsRequest(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	first := publishOne(t, s, testRecording(1, "a"), testRevision("resp_1", "recorded"))
	if _, err := s.Publish(ctx, testRecording(1, "a"), testRevision("resp_2", "recorded")); err != nil {
		t.Fatal(err)
	}
	v, err := s.RevisionDetail(ctx, first.Recording.ID, first.Revision.ID)
	if err != nil || v.ID != first.Revision.ID || string(v.Request) != string(first.Recording.Request) || len(v.MatchingInput) == 0 || len(v.Events) != 3 {
		t.Fatalf("detail: %+v %v", v, err)
	}
	if _, err := s.RevisionDetail(ctx, first.Recording.ID+1, first.Revision.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("other recording: %v", err)
	}
}

func bodyCount(t *testing.T, s *Store) (bodies, chunks int64) {
	bodies, chunks, _ = storedBytes(t, s)
	return
}

func TestDeletesSweepRequestStorage(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	settings, _ := s.Settings(ctx)
	cid := settings.ActiveCollectionID
	kept := publishOne(t, s, testRecording(cid, "kept"), testRevision("resp_kept", "recorded"))
	gone := publishOne(t, s, testRecording(cid, "gone"), testRevision("resp_gone", "recorded"))
	addHistory(t, s, model.History{CollectionID: cid, Key: gone.Recording.Key, Request: gone.Recording.Request, Outcome: "hit", RecordingID: gone.Recording.ID})
	if err := s.DeleteRecording(ctx, gone.Recording.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteRecording(ctx, gone.Recording.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("second delete: %v", err)
	}
	if b, _ := bodyCount(t, s); b != 2 {
		t.Fatalf("history still references the deleted recording's request; %d bodies", b)
	}
	h, err := s.History(ctx, cid, 1)
	if err != nil || h[0].RecordingID != 0 {
		t.Fatalf("history row still links the deleted recording: %+v %v", h, err)
	}
	if err = s.ClearHistory(ctx, cid); err != nil {
		t.Fatal(err)
	}
	if b, c := bodyCount(t, s); b != 1 || c != 1 {
		t.Fatalf("after clearing history: %d bodies, %d chunks", b, c)
	}
	if _, err = s.Get(ctx, kept.Recording.ID); err != nil {
		t.Fatalf("kept recording: %v", err)
	}

	other, err := s.CreateCollection(ctx, "other", nil)
	if err != nil {
		t.Fatal(err)
	}
	publishOne(t, s, testRecording(other.ID, "elsewhere"), testRevision("resp_other", "recorded"))
	addHistory(t, s, model.History{CollectionID: other.ID, Key: "k", Request: []byte(`{"input":"other history"}`), Outcome: "miss"})
	if err = s.DeleteCollection(ctx, cid); !errors.Is(err, ErrActiveCollection) {
		t.Fatalf("deleting the active collection: %v", err)
	}
	if err = s.DeleteCollection(ctx, other.ID); err != nil {
		t.Fatal(err)
	}
	if b, _ := bodyCount(t, s); b != 1 {
		t.Fatalf("after deleting a collection: %d bodies", b)
	}
	if err = s.DeleteCollection(ctx, other.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("deleting a missing collection: %v", err)
	}
}

func TestPruneHistoryKeepsNewestRowsPerCollection(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	settings, _ := s.Settings(ctx)
	if settings.HistoryLimit != DefaultHistoryLimit {
		t.Fatalf("default history limit %d", settings.HistoryLimit)
	}
	other, err := s.CreateCollection(ctx, "other", nil)
	if err != nil {
		t.Fatal(err)
	}
	for i := range 5 {
		addHistory(t, s, model.History{CollectionID: 1, Key: fmt.Sprint(i), Request: []byte(fmt.Sprintf(`{"input":"%d"}`, i)), Outcome: "miss"})
		addHistory(t, s, model.History{CollectionID: other.ID, Key: fmt.Sprint(i), Request: []byte(`{"input":"shared"}`), Outcome: "miss"})
	}
	if n, err := s.PruneHistory(ctx); err != nil || n != 0 {
		t.Fatalf("prune under the limit: %d %v", n, err)
	}
	settings.HistoryLimit = 2
	if err = s.SetSettings(ctx, settings); err != nil {
		t.Fatal(err)
	}
	if n, err := s.PruneHistory(ctx); err != nil || n != 6 {
		t.Fatalf("prune: %d %v", n, err)
	}
	for _, cid := range []int64{1, other.ID} {
		h, err := s.History(ctx, cid, 10)
		if err != nil || len(h) != 2 || h[0].Key != "4" || h[1].Key != "3" {
			t.Fatalf("collection %d kept %+v %v", cid, h, err)
		}
	}
	if b, _ := bodyCount(t, s); b != 3 {
		t.Fatalf("pruned requests left %d bodies, want 3", b)
	}
	settings.HistoryLimit = -1
	if err = s.SetSettings(ctx, settings); err == nil {
		t.Fatal("negative history limit accepted")
	}
}

func TestHistoryAndInsightsFilterByProvider(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	for i, provider := range []string{"openai", "openrouter", "openai", ""} {
		addHistory(t, s, model.History{CollectionID: 1, Provider: provider, Key: fmt.Sprint(i), Request: []byte(fmt.Sprintf(`{"input":"%d"}`, i)), Outcome: "miss"})
	}
	for provider, want := range map[string]int{"": 4, "openai": 2, "openrouter": 1, "unknown": 1, "absent": 0} {
		rows, err := s.HistoryAfter(ctx, 1, 0, 10, provider)
		if err != nil || len(rows) != want {
			t.Fatalf("provider %q: %d rows (%v), want %d", provider, len(rows), err, want)
		}
		for _, row := range rows {
			if provider != "" && row.Provider != strings.TrimSuffix(provider, "unknown") {
				t.Fatalf("provider %q returned row for %q", provider, row.Provider)
			}
		}
	}
	from, to := time.Now().Add(-time.Hour), time.Now().Add(time.Hour)
	all, err := s.Insights(ctx, 1, from, to, InsightFilter{})
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, p := range all.Providers {
		got = append(got, fmt.Sprintf("%s=%d", p.Provider, p.Requests))
	}
	if want := []string{"openai=2", "openrouter=1", "unknown=1"}; !slices.Equal(got, want) {
		t.Fatalf("provider breakdown = %v, want %v", got, want)
	}
	only, err := s.Insights(ctx, 1, from, to, InsightFilter{Provider: "openrouter"})
	if err != nil || only.Totals.Requests != 1 || len(only.Providers) != 1 {
		t.Fatalf("filtered insights: %+v %v", only.Totals, err)
	}
}
