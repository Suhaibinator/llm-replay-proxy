package store

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand"
	"path/filepath"
	"strings"
	"testing"

	matching "github.com/local/llm-replay-proxy/internal/match"
	"github.com/local/llm-replay-proxy/internal/model"
)

func storedBytes(t *testing.T, s *Store) (bodies, chunks, chunkBytes int64) {
	t.Helper()
	if err := s.db.QueryRow("SELECT (SELECT count(*) FROM bodies),(SELECT count(*) FROM chunks),(SELECT coalesce(sum(length(data)),0) FROM chunks)").Scan(&bodies, &chunks, &chunkBytes); err != nil {
		t.Fatal(err)
	}
	return
}

func TestBodiesRoundTripExactBytes(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	rng := rand.New(rand.NewSource(1))
	random := make([]byte, 3<<20)
	rng.Read(random)
	bodies := [][]byte{
		[]byte(`{}`),
		[]byte("{ \"n\" : 900719925474099312345.000 ,\n\t\"s\":\"\\u00e9\\ud83d\\ude00 <&>\" }"),
		[]byte(`{"x":"` + strings.Repeat("a", chunkMin-8) + `"}`),
		[]byte(`{"x":"` + strings.Repeat("ab", chunkMax) + `"}`),
		[]byte(`{"image":"data:image/png;base64,` + base64.StdEncoding.EncodeToString(random) + `"}`),
	}
	for i, raw := range bodies {
		tx, err := s.db.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		id, err := internBody(ctx, tx, raw)
		if err != nil {
			t.Fatal(err)
		}
		again, err := internBody(ctx, tx, raw)
		if err != nil || again != id {
			t.Fatalf("body %d interned twice: %d then %d (%v)", i, id, again, err)
		}
		if err = tx.Commit(); err != nil {
			t.Fatal(err)
		}
		got, err := loadBody(ctx, s.db, id)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(got, raw) {
			t.Fatalf("body %d changed in storage", i)
		}
	}
}

// growingConversation returns the request for turn n of a Responses thread
// that re-sends instructions, tools and every earlier item on each turn.
func growingConversation(turns int) [][]byte {
	rng := rand.New(rand.NewSource(2))
	words := func(n int) string {
		var b strings.Builder
		for range n {
			fmt.Fprintf(&b, "w%x ", rng.Intn(1<<16))
		}
		return b.String()
	}
	image := make([]byte, 600<<10)
	rng.Read(image)
	request := map[string]any{"model": "m", "stream": true, "store": false, "instructions": words(1500), "tools": []any{map[string]any{"type": "function", "name": "lookup", "description": words(200)}}}
	input := []any{map[string]any{"role": "user", "content": []any{map[string]any{"type": "input_image", "image_url": "data:image/png;base64," + base64.StdEncoding.EncodeToString(image)}}}}
	var out [][]byte
	for turn := range turns {
		input = append(input,
			map[string]any{"role": "user", "content": []any{map[string]any{"type": "input_text", "text": words(80)}}},
			map[string]any{"type": "function_call", "call_id": fmt.Sprint("c", turn), "name": "lookup", "arguments": "{}"},
			map[string]any{"type": "function_call_output", "call_id": fmt.Sprint("c", turn), "output": words(400)},
			map[string]any{"role": "assistant", "content": []any{map[string]any{"type": "output_text", "text": words(300)}}},
		)
		request["input"] = input
		b, err := json.Marshal(request)
		if err != nil {
			panic(err)
		}
		out = append(out, b)
	}
	return out
}

func TestGrowingConversationStoresEachTurnOnce(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	settings, err := s.Settings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	requests := growingConversation(30)
	var raw int
	for _, req := range requests {
		raw += len(req)
		key, _, err := matching.Key("/v1/responses", "test", req, nil)
		if err != nil {
			t.Fatal(err)
		}
		rec := model.Recording{CollectionID: settings.ActiveCollectionID, Key: key, Route: "/v1/responses", Request: req, UpstreamIdentity: "test", Streaming: true}
		publishOne(t, s, rec, testRevision("resp", "recorded"))
		// The caller's own log of the same call adds no request bytes.
		if err = s.AddHistory(ctx, model.History{CollectionID: settings.ActiveCollectionID, Route: "/v1/responses", Key: key, Request: req, Outcome: "recorded"}); err != nil {
			t.Fatal(err)
		}
	}
	bodies, _, stored := storedBytes(t, s)
	final := len(requests[len(requests)-1])
	t.Logf("30 turns: %d request bytes sent twice each, %d bytes stored (final request %d)", raw, stored, final)
	if bodies != int64(len(requests)) {
		t.Fatalf("stored %d bodies for %d distinct requests", bodies, len(requests))
	}
	// Quadratic growth would store ~15x the final request; chunk sharing
	// stores each turn once plus the chunk around each append point.
	if stored > int64(final)*2 {
		t.Fatalf("stored %d bytes for a %d-byte final request", stored, final)
	}
	listed, err := s.History(ctx, settings.ActiveCollectionID, 1)
	if err != nil {
		t.Fatal(err)
	}
	if listed[0].Request != nil || listed[0].Summary == nil || listed[0].Summary.Items != 1+4*len(requests) {
		t.Fatalf("history list: request %d bytes, summary %+v", len(listed[0].Request), listed[0].Summary)
	}
	got, err := s.HistoryItem(ctx, listed[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got.Request, requests[len(requests)-1]) {
		t.Fatal("history request changed in storage")
	}
}

func TestLookupSkipsRequestAndGetDerivesMatchingInput(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	settings, _ := s.Settings(ctx)
	rec := testRecording(settings.ActiveCollectionID, "lookup")
	published := publishOne(t, s, rec, testRevision("resp_lookup", "recorded"))
	hit, err := s.Lookup(ctx, settings.ActiveCollectionID, rec.Key)
	if err != nil {
		t.Fatal(err)
	}
	if hit.Recording.Request != nil || hit.Revision.Request != nil || len(hit.Revision.Events) != 3 {
		t.Fatalf("lookup loaded the request or lost events: %+v", hit)
	}
	got, err := s.Get(ctx, published.Recording.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got.Recording.Request, rec.Request) || !bytes.Equal(got.Recording.MatchingInput, rec.MatchingInput) || !bytes.Equal(got.Revision.MatchingInput, rec.MatchingInput) {
		t.Fatalf("get: request=%s matching=%s", got.Recording.Request, got.Recording.MatchingInput)
	}
}

func TestDamagedBodyIsNeverReturned(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	settings, _ := s.Settings(ctx)
	published := publishOne(t, s, testRecording(settings.ActiveCollectionID, "damaged"), testRevision("resp_damaged", "recorded"))
	if _, err := s.db.Exec("UPDATE chunks SET data=CAST(upper(CAST(data AS TEXT)) AS BLOB)"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Get(ctx, published.Recording.ID); !errors.Is(err, errCorruptBody) {
		t.Fatalf("get of damaged body: %v", err)
	}
}

func TestOpenRefusesOtherStorageFormats(t *testing.T) {
	path := filepath.Join(t.TempDir(), "old.db")
	db, err := sql.Open("sqlite", sqliteFileURL(path))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`CREATE TABLE recordings(id INTEGER PRIMARY KEY, request BLOB)`); err != nil {
		t.Fatal(err)
	}
	db.Close()
	if s, err := Open(path); err == nil {
		s.Close()
		t.Fatal("opened a database in another storage format")
	} else if !strings.Contains(err.Error(), "Delete the file") {
		t.Fatalf("unhelpful error: %v", err)
	}
}

func TestExportSweepsUnreferencedBodies(t *testing.T) {
	ctx := context.Background()
	var kept []byte
	path := exportWith(t, func(s *Store, cid int64) {
		kept = publishOne(t, s, testRecording(cid, "kept"), testRevision("resp_kept", "recorded")).Recording.Request
		other, err := s.CreateCollection(ctx, "other", nil)
		if err != nil {
			t.Fatal(err)
		}
		publishOne(t, s, testRecording(other.ID, "other collection"), testRevision("resp_other", "recorded"))
		if err = s.AddHistory(ctx, model.History{CollectionID: cid, Route: "/v1/responses", Key: "k", Request: []byte(`{"input":"history only"}`), Outcome: "miss"}); err != nil {
			t.Fatal(err)
		}
	})
	raw, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	var bodies int
	var size int
	if err = raw.QueryRow("SELECT count(*),sum(size) FROM bodies").Scan(&bodies, &size); err != nil {
		t.Fatal(err)
	}
	if bodies != 1 || size != len(kept) {
		t.Fatalf("snapshot keeps %d bodies (%d bytes), expected only the exported request", bodies, size)
	}
	var orphans int
	if err = raw.QueryRow("SELECT count(*) FROM chunks").Scan(&orphans); err != nil || orphans != 1 {
		t.Fatalf("snapshot keeps %d chunks: %v", orphans, err)
	}
}
