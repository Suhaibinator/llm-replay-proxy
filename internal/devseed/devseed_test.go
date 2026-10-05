package devseed

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/local/llm-replay-proxy/internal/store"
)

func open(t testing.TB) *store.Store {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "seed.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func TestSeedProducesValidVariedTraffic(t *testing.T) {
	ctx := context.Background()
	db := open(t)
	now := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	res, err := Seed(ctx, db, Options{Rows: 1500, Days: 30, Seed: 7, Now: now, Activate: true})
	if err != nil {
		t.Fatal(err)
	}
	if res.History < 1500 || res.Recordings == 0 {
		t.Fatalf("result %+v", res)
	}
	settings, _ := db.Settings(ctx)
	if settings.ActiveCollectionID != res.Collection.ID {
		t.Fatal("collection not activated")
	}
	in, err := db.Insights(ctx, res.Collection.ID, now.AddDate(0, 0, -30), now, "")
	if err != nil {
		t.Fatal(err)
	}
	tot := in.Totals
	if tot.Requests < 1400 || tot.Hits == 0 || tot.Misses == 0 || tot.Recorded == 0 || tot.Interrupted == 0 || tot.Errors == 0 {
		t.Fatalf("outcomes %+v", tot.OutcomeCounts)
	}
	if tot.UpstreamCost == nil || tot.SavedCost == nil || tot.ReplayedTokens.Reasoning == 0 || tot.ReplayedTokens.CachedInput == 0 || tot.Threads < 50 {
		t.Fatalf("totals %+v", tot)
	}
	if len(in.Models) != len(profiles) || len(in.Routes) != 3 || in.Bucket != "day" {
		t.Fatalf("models %d routes %d", len(in.Models), len(in.Routes))
	}
	recordings, err := db.Recordings(ctx, res.Collection.ID)
	if err != nil {
		t.Fatal(err)
	}
	kinds := map[string]bool{}
	for _, r := range recordings {
		if r.Response == nil || r.Response.Usage.Output == nil || r.Response.Model == "" {
			t.Fatalf("recording %d response %+v", r.ID, r.Response)
		}
		kinds[r.Route+map[bool]string{true: " sse", false: " json"}[r.Streaming]] = true
	}
	if len(kinds) != 6 {
		t.Fatalf("protocol/transport mix %v", kinds)
	}
	// Recordings date from the call that recorded them: spread over the
	// range, never after their active revision, never after a replay of them.
	parse := func(v string) time.Time {
		at, err := time.Parse(time.RFC3339Nano, v)
		if err != nil {
			t.Fatalf("timestamp %q: %v", v, err)
		}
		return at
	}
	created := map[int64]time.Time{}
	oldest, newest := now, now.AddDate(0, 0, -31)
	for _, r := range recordings {
		at := parse(r.CreatedAt)
		if at.Before(now.AddDate(0, 0, -31)) || at.After(now) {
			t.Fatalf("recording %d created %s, outside the seeded range", r.ID, r.CreatedAt)
		}
		if parse(r.UpdatedAt).Before(at) {
			t.Fatalf("recording %d active revision %s predates the recording %s", r.ID, r.UpdatedAt, r.CreatedAt)
		}
		created[r.ID] = at
		if at.Before(oldest) {
			oldest = at
		}
		if at.After(newest) {
			newest = at
		}
	}
	if newest.Sub(oldest) < 20*24*time.Hour {
		t.Fatalf("recordings span only %s to %s", oldest, newest)
	}
	rows, err := db.History(ctx, res.Collection.ID, 1000)
	if err != nil {
		t.Fatal(err)
	}
	for _, h := range rows {
		if at, ok := created[h.RecordingID]; ok && parse(h.CreatedAt).Before(at) {
			t.Fatalf("history %d (%s) uses recording %d before it was recorded (%s)", h.ID, h.CreatedAt, h.RecordingID, at)
		}
	}
	threads, err := db.Threads(ctx, res.Collection.ID, 500, time.Time{}, time.Time{})
	if err != nil || len(threads) == 0 {
		t.Fatalf("threads %d %v", len(threads), err)
	}
	grown := 0
	for _, th := range threads {
		if th.MaxItems > 4 {
			grown++
		}
	}
	if grown == 0 {
		t.Fatal("no conversation grew past two turns")
	}
}

// BenchmarkInsights10k measures the dashboard aggregate over 10,000 history
// rows once summaries exist (the first call computes and saves them).
func BenchmarkInsights10k(b *testing.B) {
	ctx := context.Background()
	db := open(b)
	now := time.Now().UTC()
	res, err := Seed(ctx, db, Options{Rows: 10000, Days: 30, Seed: 1, Now: now})
	if err != nil {
		b.Fatal(err)
	}
	started := time.Now()
	if _, err = db.Insights(ctx, res.Collection.ID, now.AddDate(0, 0, -30), now.Add(time.Minute), ""); err != nil {
		b.Fatal(err)
	}
	b.Logf("%d rows; first call (computes summaries) %v", res.History, time.Since(started))
	b.Run("insights", func(b *testing.B) {
		for b.Loop() {
			if _, err := db.Insights(ctx, res.Collection.ID, now.AddDate(0, 0, -30), now.Add(time.Minute), ""); err != nil {
				b.Fatal(err)
			}
		}
	})
	b.Run("threads", func(b *testing.B) {
		for b.Loop() {
			if _, err := db.Threads(ctx, res.Collection.ID, 50, time.Time{}, time.Time{}); err != nil {
				b.Fatal(err)
			}
		}
	})
	threads, _ := db.Threads(ctx, res.Collection.ID, 1, time.Time{}, time.Time{})
	b.Run("thread", func(b *testing.B) {
		for b.Loop() {
			if _, err := db.Thread(ctx, res.Collection.ID, threads[0].Thread); err != nil {
				b.Fatal(err)
			}
		}
	})
	history, _ := db.History(ctx, res.Collection.ID, 1)
	b.Run("nearest", func(b *testing.B) {
		for b.Loop() {
			if _, err := db.Nearest(ctx, history[0].ID); err != nil {
				b.Fatal(err)
			}
		}
	})
	b.Run("recordings", func(b *testing.B) {
		for b.Loop() {
			if _, err := db.Recordings(ctx, res.Collection.ID); err != nil {
				b.Fatal(err)
			}
		}
	})
}
