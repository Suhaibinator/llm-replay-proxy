package store

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
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
	var stored int
	if err = s.db.QueryRow("SELECT count(*) FROM bodies WHERE summary IS NOT NULL").Scan(&stored); err != nil || stored != 2 {
		t.Fatalf("summaries saved for %d bodies (%v), want the 2 listed", stored, err)
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
