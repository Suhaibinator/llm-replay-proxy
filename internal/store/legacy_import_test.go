package store

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	matching "github.com/local/llm-replay-proxy/internal/match"
)

// Snapshots exported before empty arrays stopped canonicalizing as null carry
// legacy keys. They must still import, and be re-keyed; other mismatched keys
// must still be rejected.
func TestImportRekeysLegacyEmptyArraySnapshots(t *testing.T) {
	ctx := context.Background()
	source := openTest(t)
	settings, _ := source.Settings(ctx)
	rec := testRecording(settings.ActiveCollectionID, "legacy")
	rec.Request = []byte(`{"model":"m","input":"legacy","tools":[],"stream":true}`)
	newKey, newInput, err := matching.Key(rec.Route, rec.UpstreamIdentity, rec.Request, nil)
	if err != nil {
		t.Fatal(err)
	}
	rec.Key, rec.MatchingInput = newKey, newInput
	rev := testRevision("r1", "recorded")
	rev.Request, rev.MatchingInput = rec.Request, rec.MatchingInput
	if _, err = source.Publish(ctx, rec, rev); err != nil {
		t.Fatal(err)
	}
	legacyKey, legacyInput, err := matching.LegacyKey(rec.Route, rec.UpstreamIdentity, rec.Request, nil)
	if err != nil || legacyKey == newKey {
		t.Fatalf("legacy key must differ: %v", err)
	}
	export := func(key string, input []byte) string {
		t.Helper()
		path := filepath.Join(t.TempDir(), "snapshot.sqlite")
		if err := source.Export(ctx, settings.ActiveCollectionID, path); err != nil {
			t.Fatal(err)
		}
		db, err := sql.Open("sqlite", sqliteFileURL(path))
		if err != nil {
			t.Fatal(err)
		}
		defer db.Close()
		if _, err = db.Exec("UPDATE recordings SET key=?,matching_input=?", key, input); err != nil {
			t.Fatal(err)
		}
		if _, err = db.Exec("UPDATE revisions SET matching_input=?", input); err != nil {
			t.Fatal(err)
		}
		return path
	}

	dest := openTest(t)
	imported, err := dest.Import(ctx, export(legacyKey, legacyInput))
	if err != nil {
		t.Fatalf("legacy snapshot rejected: %v", err)
	}
	var key string
	var input []byte
	if err = dest.db.QueryRow("SELECT key,matching_input FROM recordings WHERE collection_id=?", imported[0].ID).Scan(&key, &input); err != nil {
		t.Fatal(err)
	}
	if key != newKey || string(input) != string(newInput) {
		t.Fatalf("imported recording not re-keyed: %s", input)
	}
	var revInput []byte
	if err = dest.db.QueryRow("SELECT v.matching_input FROM revisions v JOIN recordings r ON r.id=v.recording_id WHERE r.collection_id=?", imported[0].ID).Scan(&revInput); err != nil {
		t.Fatal(err)
	}
	if string(revInput) != string(newInput) {
		t.Fatalf("imported revision not re-keyed: %s", revInput)
	}

	other, _, _ := matching.Key(rec.Route, rec.UpstreamIdentity, []byte(`{"model":"other"}`), nil)
	if _, err = dest.Import(ctx, export(other, newInput)); err == nil {
		t.Fatal("snapshot with a forged key was accepted")
	}
}
