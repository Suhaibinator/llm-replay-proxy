package store

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	matching "github.com/local/llm-replay-proxy/internal/match"
	"github.com/local/llm-replay-proxy/internal/model"
)

// exportWith publishes fixtures into a fresh store, exports the active
// collection, and applies raw SQL to the snapshot file.
func exportWith(t *testing.T, setup func(*Store, int64), mutations ...string) string {
	t.Helper()
	ctx := context.Background()
	source := openTest(t)
	settings, err := source.Settings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	setup(source, settings.ActiveCollectionID)
	path := filepath.Join(t.TempDir(), "snapshot.sqlite")
	if err = source.Export(ctx, settings.ActiveCollectionID, path); err != nil {
		t.Fatal(err)
	}
	raw, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	for _, m := range mutations {
		if _, err = raw.Exec(m); err != nil {
			t.Fatalf("%s: %v", m, err)
		}
	}
	return path
}

func publishOne(t *testing.T, s *Store, rec model.Recording, rev model.Revision) model.Entry {
	t.Helper()
	e, err := s.Publish(context.Background(), rec, rev)
	if err != nil {
		t.Fatal(err)
	}
	return e
}

func chatRecording(cid int64) model.Recording {
	req := []byte(`{"model":"m","messages":[{"role":"user","content":"hi"}]}`)
	key, input, err := matching.Key("/v1/chat/completions", "test", req, nil)
	if err != nil {
		panic(err)
	}
	return model.Recording{CollectionID: cid, Key: key, Route: "/v1/chat/completions", Request: req, MatchingInput: input, UpstreamIdentity: "test"}
}

func chatRevision() model.Revision {
	return model.Revision{Status: 200, Headers: map[string]string{"Content-Type": "application/json"}, Body: `{"id":"x","choices":[{"message":{"role":"assistant","content":"hello"},"finish_reason":"stop"}]}`, Source: "recorded"}
}

func TestExportOmitsRequestHistory(t *testing.T) {
	const marker = "history-only-request-marker-7f3c"
	ctx := context.Background()
	path := exportWith(t, func(s *Store, cid int64) {
		publishOne(t, s, testRecording(cid, "kept"), testRevision("resp_kept", "recorded"))
		for _, outcome := range []string{"miss", "upstream_error"} {
			if err := s.AddHistory(ctx, model.History{CollectionID: cid, Route: "/v1/responses", Key: "k", Request: json.RawMessage(`{"input":"` + marker + `"}`), Outcome: outcome, Detail: marker}); err != nil {
				t.Fatal(err)
			}
		}
	})
	raw, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	var n int
	err = raw.QueryRow("SELECT count(*) FROM history").Scan(&n)
	raw.Close()
	if err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("snapshot contains %d history rows", n)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(b, []byte(marker)) {
		t.Fatal("history request bytes remain in the snapshot file")
	}
	dest := openTest(t)
	cols, err := dest.Import(ctx, path)
	if err != nil || len(cols) != 1 {
		t.Fatalf("import after history purge: %v %+v", err, cols)
	}
}

// failingView replaces table with a view that raises a SQLite runtime error
// (integer overflow) for every row after the first, simulating a read error
// in the middle of an import cursor.
func failingView(table, columns, lastColumn string) []string {
	return []string{
		"ALTER TABLE " + table + " RENAME TO " + table + "_data",
		"CREATE VIEW " + table + " AS SELECT " + columns + ", CASE WHEN id>(SELECT min(id) FROM " + table + "_data) THEN abs(id-id-9223372036854775807-1) ELSE " + lastColumn + " END AS " + lastColumn + " FROM " + table + "_data",
	}
}

func TestImportFailsOnSourceIterationErrors(t *testing.T) {
	for _, tc := range []struct {
		name      string
		setup     func(*testing.T, *Store, int64)
		mutations []string
	}{
		{"collections", func(t *testing.T, s *Store, cid int64) {
			publishOne(t, s, testRecording(cid, "a"), testRevision("resp_a", "recorded"))
		}, append([]string{`INSERT INTO collections(name,exclusions,created_at) VALUES('second','[]','2026-01-01T00:00:00Z')`},
			failingView("collections", "id,name,exclusions", "created_at")...)},
		{"recordings", func(t *testing.T, s *Store, cid int64) {
			publishOne(t, s, testRecording(cid, "a"), testRevision("resp_a", "recorded"))
			publishOne(t, s, testRecording(cid, "b"), testRevision("resp_b", "recorded"))
		}, failingView("recordings", "id,collection_id,key,route,request,matching_input,upstream_identity,streaming,active_revision_id", "created_at")},
		{"revisions", func(t *testing.T, s *Store, cid int64) {
			first := publishOne(t, s, testRecording(cid, "a"), testRevision("resp_a", "recorded"))
			publishOne(t, s, testRecording(cid, "a"), testRevision("resp_a2", "edited"))
			// Keep the first revision active so a truncated read still finds it.
			if _, err := s.Restore(context.Background(), first.Recording.ID, first.Revision.ID); err != nil {
				t.Fatal(err)
			}
		}, failingView("revisions", "id,recording_id,status,headers,body,events,request,matching_input,source", "created_at")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := exportWith(t, func(s *Store, cid int64) { tc.setup(t, s, cid) }, tc.mutations...)
			dest := openTest(t)
			before, _ := dest.Collections(context.Background())
			_, err := dest.Import(context.Background(), path)
			if !errors.Is(err, ErrInvalidSnapshot) {
				t.Fatalf("import with a failing %s cursor: %v", tc.name, err)
			}
			after, _ := dest.Collections(context.Background())
			if len(after) != len(before) {
				t.Fatal("failed import committed collections")
			}
		})
	}
}

func TestImportRejectsInvalidSnapshotRows(t *testing.T) {
	for _, tc := range []struct{ name, mutation string }{
		{"blank name", `UPDATE collections SET name='   '`},
		{"empty name", `UPDATE collections SET name=''`},
		{"collection created_at", `UPDATE collections SET created_at='yesterday'`},
		{"empty collection created_at", `UPDATE collections SET created_at=''`},
		{"recording created_at", `UPDATE recordings SET created_at='2026-13-45'`},
		{"revision created_at", `UPDATE revisions SET created_at='not a time'`},
		{"missing table", `DROP TABLE revisions`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := exportWith(t, func(s *Store, cid int64) {
				publishOne(t, s, testRecording(cid, "a"), testRevision("resp_a", "recorded"))
			}, tc.mutation)
			_, err := openTest(t).Import(context.Background(), path)
			if !errors.Is(err, ErrInvalidSnapshot) {
				t.Fatalf("expected ErrInvalidSnapshot, got %v", err)
			}
		})
	}
	t.Run("not sqlite", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "junk.sqlite")
		if err := os.WriteFile(path, []byte("this is not a database file at all, just text padding it out"), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := openTest(t).Import(context.Background(), path); !errors.Is(err, ErrInvalidSnapshot) {
			t.Fatalf("expected ErrInvalidSnapshot, got %v", err)
		}
	})
}

func TestImportNormalisesNullJSONAndTrimsNames(t *testing.T) {
	ctx := context.Background()
	path := exportWith(t, func(s *Store, cid int64) {
		publishOne(t, s, chatRecording(cid), chatRevision())
	}, `UPDATE collections SET name='  spaced  ', exclusions='null'`, `UPDATE revisions SET headers='null', events='null'`)
	dest := openTest(t)
	cols, err := dest.Import(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	if len(cols) != 1 || cols[0].Name != "spaced" || cols[0].Exclusions == nil {
		t.Fatalf("imported collection: %+v", cols)
	}
	var exclusions, headers, events string
	if err = dest.db.QueryRow("SELECT exclusions FROM collections WHERE id=?", cols[0].ID).Scan(&exclusions); err != nil {
		t.Fatal(err)
	}
	if err = dest.db.QueryRow("SELECT v.headers,v.events FROM revisions v JOIN recordings r ON r.id=v.recording_id WHERE r.collection_id=?", cols[0].ID).Scan(&headers, &events); err != nil {
		t.Fatal(err)
	}
	if exclusions != "[]" || headers != "{}" || events != "[]" {
		t.Fatalf("stored exclusions=%s headers=%s events=%s", exclusions, headers, events)
	}
	listed, err := dest.Collections(ctx)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(listed[len(listed)-1])
	if !bytes.Contains(b, []byte(`"exclusions":[]`)) {
		t.Fatalf("collection JSON: %s", b)
	}
}

func TestAnalyticsNearMaximumYear(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	settings, _ := s.Settings(ctx)
	from := time.Date(9999, 12, 30, 0, 0, 0, 0, time.UTC)
	to := time.Date(9999, 12, 31, 23, 59, 59, 0, time.UTC)
	for i, stamp := range []string{"9999-12-30T12:00:00Z", "9999-12-31T06:00:00Z", "9999-12-29T12:00:00Z"} {
		if err := s.AddHistory(ctx, model.History{CollectionID: settings.ActiveCollectionID, Route: "/v1/responses", Key: string(rune('a' + i)), Request: []byte(`{}`), Outcome: "hit", Source: "replay", CreatedAt: stamp}); err != nil {
			t.Fatal(err)
		}
	}
	a, err := s.Analytics(ctx, settings.ActiveCollectionID, from, to)
	if err != nil {
		t.Fatal(err)
	}
	if a.Total != 2 {
		t.Fatalf("total %d, want 2", a.Total)
	}
}
