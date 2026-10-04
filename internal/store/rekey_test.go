package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	matching "github.com/local/llm-replay-proxy/internal/match"
	"github.com/local/llm-replay-proxy/internal/model"
)

const rekeyBody = `{"id":"r1","status":"completed","output":[{"id":"m1","type":"message","role":"assistant","content":[{"type":"output_text","text":"hello"}]}]}`

// legacyInput reproduces the canonical-version-1 matching input written before
// empty arrays were preserved; canonicalBody is supplied in that old form.
func legacyInput(canonicalBody string) (string, []byte) {
	input := []byte(`{"version":1,"route":"/v1/responses","identity":"test","body":` + canonicalBody + `}`)
	sum := sha256.Sum256(input)
	return hex.EncodeToString(sum[:]), input
}

type legacyRevision struct{ request, canonical, text string }

// insertLegacy writes a recording directly, bypassing Publish validation, as a
// pre-fix binary would have stored it. The last revision is active.
func insertLegacy(t *testing.T, s *Store, cid int64, revisions ...legacyRevision) int64 {
	t.Helper()
	ctx := context.Background()
	active := revisions[len(revisions)-1]
	key, input := legacyInput(active.canonical)
	res, err := s.db.ExecContext(ctx, `INSERT INTO recordings(collection_id,key,route,request,matching_input,upstream_identity,streaming,created_at) VALUES(?,?,?,?,?,?,0,?)`, cid, key, "/v1/responses", []byte(active.request), input, "test", now())
	if err != nil {
		t.Fatal(err)
	}
	rid, _ := res.LastInsertId()
	var vid int64
	for _, v := range revisions {
		_, vin := legacyInput(v.canonical)
		body := `{"id":"r1","status":"completed","output":[{"id":"m1","type":"message","role":"assistant","content":[{"type":"output_text","text":"` + v.text + `"}]}]}`
		res, err = s.db.ExecContext(ctx, `INSERT INTO revisions(recording_id,status,headers,body,events,request,matching_input,source,created_at) VALUES(?,200,'{"Content-Type":"application/json"}',?,'[]',?,?,'recorded',?)`, rid, body, []byte(v.request), vin, now())
		if err != nil {
			t.Fatal(err)
		}
		vid, _ = res.LastInsertId()
	}
	if _, err = s.db.ExecContext(ctx, "UPDATE recordings SET active_revision_id=? WHERE id=?", vid, rid); err != nil {
		t.Fatal(err)
	}
	return rid
}

func lookupText(t *testing.T, s *Store, cid int64, request string, exclusions []string) (model.Entry, error) {
	t.Helper()
	key, _, err := matching.Key("/v1/responses", "test", []byte(request), exclusions)
	if err != nil {
		t.Fatal(err)
	}
	return s.Lookup(context.Background(), cid, key)
}

func TestRekeyMigratesLegacyEmptyArrayKeys(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	settings, _ := s.Settings(ctx)
	cid := settings.ActiveCollectionID
	excluding, err := s.CreateCollection(ctx, "excluding", []string{"/tools/0"})
	if err != nil {
		t.Fatal(err)
	}

	// Plain re-key: an empty array used to canonicalize as null.
	plain := insertLegacy(t, s, cid, legacyRevision{`{"model":"m","tools":[],"stream":false}`, `{"model":"m","stream":false,"tools":null}`, "plain"})
	// Split: an older revision sent [] while the active one sent null. Both
	// shared one legacy key; now each request shape has its own recording.
	insertLegacy(t, s, cid,
		legacyRevision{`{"model":"split","tools":[],"stream":false}`, `{"model":"split","stream":false,"tools":null}`, "old-empty"},
		legacyRevision{`{"model":"split","tools":null,"stream":false}`, `{"model":"split","stream":false,"tools":null}`, "new-null"})
	// Collision: with /tools/0 excluded, [] and ["x"] had different legacy keys
	// (null versus an exclusion-emptied []) but now share one key.
	insertLegacy(t, s, excluding.ID, legacyRevision{`{"model":"c","tools":[],"stream":false}`, `{"model":"c","stream":false,"tools":null}`, "older"})
	insertLegacy(t, s, excluding.ID, legacyRevision{`{"model":"c","tools":["x"],"stream":false}`, `{"model":"c","stream":false,"tools":[]}`, "newer"})
	// Already-correct recordings are untouched.
	untouched, err := s.Publish(ctx, testRecording(cid, "untouched"), testRevision("u", "recorded"))
	if err != nil {
		t.Fatal(err)
	}

	report, err := s.rekeyOnce(ctx, "test-rekey")
	if err != nil {
		t.Fatal(err)
	}
	if report != (RekeyReport{Rekeyed: 1, Merged: 1, Split: 1}) {
		t.Fatalf("report = %+v", report)
	}
	if again, err := s.rekeyOnce(ctx, "test-rekey"); err != nil || again.changed() {
		t.Fatalf("second run = %+v, %v", again, err)
	}

	entry, err := lookupText(t, s, cid, `{"model":"m","tools":[],"stream":false}`, nil)
	if err != nil || entry.Recording.ID != plain {
		t.Fatalf("plain lookup = %+v, %v", entry.Recording, err)
	}
	if _, err := lookupText(t, s, cid, `{"model":"m","tools":null,"stream":false}`, nil); !errors.Is(err, ErrNotFound) {
		t.Fatalf("null request still matches empty-array recording: %v", err)
	}
	// Edits re-validate provenance against the recomputed key.
	edit := entry.Revision
	edit.ID, edit.Source = 0, "edit"
	if _, err := s.PublishIfActive(ctx, entry.Recording, edit, entry.Revision.ID); err != nil {
		t.Fatalf("edit after migration: %v", err)
	}

	for request, want := range map[string]string{
		`{"model":"split","tools":null,"stream":false}`: "new-null",
		`{"model":"split","tools":[],"stream":false}`:   "old-empty",
	} {
		entry, err := lookupText(t, s, cid, request, nil)
		if err != nil || !containsText(entry.Revision.Body, want) {
			t.Fatalf("split lookup %s = %q, %v", request, entry.Revision.Body, err)
		}
	}

	merged, err := lookupText(t, s, excluding.ID, `{"model":"c","tools":[],"stream":false}`, []string{"/tools/0"})
	if err != nil || !containsText(merged.Revision.Body, "newer") {
		t.Fatalf("collision winner = %q, %v", merged.Revision.Body, err)
	}
	revisions, err := s.Revisions(ctx, merged.Recording.ID)
	if err != nil || len(revisions) != 2 {
		t.Fatalf("merged revisions = %d, %v", len(revisions), err)
	}
	if recs, _ := s.Recordings(ctx, excluding.ID); len(recs) != 1 {
		t.Fatalf("collection still has %d recordings", len(recs))
	}

	if got, err := s.Get(ctx, untouched.Recording.ID); err != nil || got.Recording.Key != untouched.Recording.Key {
		t.Fatalf("untouched recording changed: %+v, %v", got.Recording, err)
	}

	// Every migrated recording and revision passes snapshot validation.
	for _, id := range []int64{cid, excluding.ID} {
		snapshot := filepath.Join(t.TempDir(), "snapshot.db")
		if err := s.Export(ctx, id, snapshot); err != nil {
			t.Fatal(err)
		}
		if _, err := openTest(t).Import(ctx, snapshot); err != nil {
			t.Fatalf("migrated collection %d does not import: %v", id, err)
		}
	}
}

func TestRekeyRunsAtOpen(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "legacy.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	settings, _ := s.Settings(ctx)
	insertLegacy(t, s, settings.ActiveCollectionID, legacyRevision{`{"model":"m","tools":[],"stream":false}`, `{"model":"m","stream":false,"tools":null}`, "plain"})
	// Simulate a database last written before the migration existed.
	if _, err = s.db.ExecContext(ctx, "DROP TABLE store_migrations"); err != nil {
		t.Fatal(err)
	}
	s.Close()
	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if _, err := lookupText(t, s, settings.ActiveCollectionID, `{"model":"m","tools":[],"stream":false}`, nil); err != nil {
		t.Fatalf("legacy recording not re-keyed at open: %v", err)
	}
}

func containsText(body, text string) bool {
	return strings.Contains(body, `"text":"`+text+`"`)
}
