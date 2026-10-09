package store

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
)

func TestImportRejectsCorruptionWithoutPartialCollections(t *testing.T) {
	for _, tc := range []struct{ name, mutation string }{
		{"matching key", `UPDATE recordings SET key='forged' WHERE id=(SELECT min(id) FROM recordings)`},
		{"matching rules", `UPDATE collections SET exclusions='["/stream"]'`},
		{"incomplete stream", `UPDATE revisions SET events=X'28b52ffd04581100005b5d561f7f61'`},
		{"unreadable events", `UPDATE revisions SET events='[]'`},
		{"unreadable response body", `UPDATE revisions SET body=X'ff'`},
		{"response headers", `UPDATE revisions SET headers='{"Set-Cookie":"session=forged"}'`},
		{"provenance", `UPDATE revisions SET request_body=(SELECT max(request_body) FROM revisions)`},
		{"chunk bytes", `UPDATE chunks SET data=zeroblob(length(data))`},
		{"body hash", `UPDATE bodies SET hash=zeroblob(32) WHERE id=(SELECT min(id) FROM bodies)`},
		{"chunk list", `UPDATE bodies SET chunks=X'ff'`},
		{"chunk size", `UPDATE chunks SET size=size+1`},
		{"storage format", `PRAGMA user_version=1`},
		{"previous storage format", `PRAGMA user_version=5`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			source := openTest(t)
			settings, err := source.Settings(ctx)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = source.Publish(ctx, testRecording(settings.ActiveCollectionID, "original"), testRevision("resp_original", "recorded")); err != nil {
				t.Fatal(err)
			}
			if _, err = source.Publish(ctx, testRecording(settings.ActiveCollectionID, "other"), testRevision("resp_other", "recorded")); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(t.TempDir(), "snapshot.sqlite")
			if err = source.Export(ctx, settings.ActiveCollectionID, path); err != nil {
				t.Fatal(err)
			}
			raw, err := sql.Open("sqlite", path)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = raw.Exec(tc.mutation); err != nil {
				raw.Close()
				t.Fatal(err)
			}
			if err = raw.Close(); err != nil {
				t.Fatal(err)
			}
			destination := openTest(t)
			before, err := destination.Collections(ctx)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = destination.Import(ctx, path); err == nil {
				t.Fatal("corrupted snapshot imported")
			}
			after, err := destination.Collections(ctx)
			if err != nil {
				t.Fatal(err)
			}
			if len(after) != len(before) {
				t.Fatalf("failed import left %d collections, expected %d", len(after), len(before))
			}
		})
	}
}
