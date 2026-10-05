package admin

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/local/llm-replay-proxy/internal/model"
	_ "modernc.org/sqlite"
)

func errorCode(t *testing.T, w *httptest.ResponseRecorder) (string, string) {
	t.Helper()
	var body struct {
		Error struct{ Code, Message string } `json:"error"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode error body %q: %v", w.Body.String(), err)
	}
	return body.Error.Code, body.Error.Message
}

func postImport(h http.Handler, body []byte) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/api/import", bytes.NewReader(body))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	return w
}

func TestImportStatusDistinguishesInvalidSnapshotFromInternalFailure(t *testing.T) {
	db, h := testAdmin(t)
	publishFixture(t, db)
	settings, _ := db.Settings(context.Background())
	export := request(t, h, http.MethodGet, "/api/collections/"+itoa(settings.ActiveCollectionID)+"/export", nil)
	if export.Code != 200 {
		t.Fatalf("export: %d", export.Code)
	}
	snapshot := export.Body.Bytes()

	// A file that is not a snapshot is the client's problem.
	_, h2 := testAdmin(t)
	w := postImport(h2, []byte("definitely not a sqlite database, but long enough"))
	if code, _ := errorCode(t, w); w.Code != http.StatusUnprocessableEntity || code != "invalid_import" {
		t.Fatalf("invalid snapshot: %d %s", w.Code, w.Body.String())
	}

	// A valid snapshot that fails on the destination is a server error whose
	// details are not echoed.
	db3, h3 := testAdmin(t)
	if err := db3.Close(); err != nil {
		t.Fatal(err)
	}
	w = postImport(h3, snapshot)
	code, msg := errorCode(t, w)
	if w.Code != http.StatusInternalServerError || code != "internal_error" || strings.Contains(msg, "closed") || strings.Contains(msg, "sql") {
		t.Fatalf("destination failure: %d %s", w.Code, w.Body.String())
	}
}

func TestInternalErrorsDoNotLeakDetails(t *testing.T) {
	db, h := testAdmin(t)
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	for _, target := range []string{"/api/settings", "/api/collections", "/api/recordings?collection_id=1"} {
		w := request(t, h, http.MethodGet, target, nil)
		code, msg := errorCode(t, w)
		if w.Code != 500 || code != "internal_error" || msg != "internal server error" {
			t.Fatalf("%s: %d %s", target, w.Code, w.Body.String())
		}
	}
}

func TestRestoreConflictUsesRevisionConflictCode(t *testing.T) {
	db, h := testAdmin(t)
	e := publishFixture(t, db)
	w := request(t, h, http.MethodPost, "/api/recordings/"+itoa(e.Recording.ID)+"/edit", map[string]any{"text": "first", "base_revision_id": e.Revision.ID})
	if w.Code != http.StatusCreated {
		t.Fatalf("edit: %d %s", w.Code, w.Body.String())
	}
	w = request(t, h, http.MethodPost, "/api/recordings/"+itoa(e.Recording.ID)+"/restore", map[string]any{"revision_id": e.Revision.ID, "base_revision_id": e.Revision.ID})
	if code, _ := errorCode(t, w); w.Code != http.StatusConflict || code != "revision_conflict" {
		t.Fatalf("stale restore: %d %s", w.Code, w.Body.String())
	}
}

// racingBody activates another revision while the edit handler reads the
// request, after it has loaded the recording, so the store reports the
// conflict instead of the handler's early base-revision check.
type racingBody struct {
	r    io.Reader
	race func()
	done bool
}

func (b *racingBody) Read(p []byte) (int, error) {
	if !b.done {
		b.done = true
		b.race()
	}
	return b.r.Read(p)
}

func TestEditStoreConflictUsesRevisionConflictCode(t *testing.T) {
	db, h := testAdmin(t)
	e := publishFixture(t, db)
	payload, _ := json.Marshal(map[string]any{"text": "mine", "base_revision_id": e.Revision.ID})
	body := &racingBody{r: bytes.NewReader(payload), race: func() {
		rev := e.Revision
		rev.ID, rev.Source = 0, "recorded"
		if _, err := db.Publish(context.Background(), e.Recording, rev); err != nil {
			t.Error(err)
		}
	}}
	req := httptest.NewRequest(http.MethodPost, "/api/recordings/"+itoa(e.Recording.ID)+"/edit", body)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if code, _ := errorCode(t, w); w.Code != http.StatusConflict || code != "revision_conflict" {
		t.Fatalf("racing edit: %d %s", w.Code, w.Body.String())
	}
}

func TestOversizedJSONBodyIsRequestTooLarge(t *testing.T) {
	_, h := testAdmin(t)
	big := `{"mode":"` + strings.Repeat("a", 17<<20) + `"}`
	req := httptest.NewRequest(http.MethodPut, "/api/settings", strings.NewReader(big))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if code, _ := errorCode(t, w); w.Code != http.StatusRequestEntityTooLarge || code != "request_too_large" {
		t.Fatalf("oversized body: %d %.200s", w.Code, w.Body.String())
	}
}

func TestRoutingValidatesPathBeforeMethod(t *testing.T) {
	db, h := testAdmin(t)
	e := publishFixture(t, db)
	rid, cid := itoa(e.Recording.ID), itoa(e.Recording.CollectionID)
	for _, tc := range []struct {
		method, target string
		status         int
	}{
		{http.MethodPost, "/api/collections/abc/whatever", 404},
		{http.MethodGet, "/api/collections/abc/whatever", 404},
		{http.MethodPost, "/api/collections/" + cid + "/whatever", 404},
		{http.MethodGet, "/api/collections/abc/export", 404},
		{http.MethodGet, "/api/collections/+" + cid + "/export", 404},
		{http.MethodPost, "/api/collections/" + cid + "/export", 405},
		{http.MethodDelete, "/api/recordings/abc", 404},
		{http.MethodGet, "/api/recordings/abc", 404},
		{http.MethodGet, "/api/recordings/+" + rid, 404},
		{http.MethodPost, "/api/recordings/" + rid + "/unknown", 404},
		{http.MethodGet, "/api/recordings/" + rid + "/edit/extra", 404},
		{http.MethodPut, "/api/recordings/" + rid, 405},
		{http.MethodPut, "/api/collections/" + cid, 405},
		{http.MethodGet, "/api/history/abc", 404},
		{http.MethodGet, "/api/history/1/extra", 404},
		{http.MethodPost, "/api/history/1", 405},
		{http.MethodPut, "/api/history?collection_id=" + cid, 405},
		{http.MethodGet, "/api/recordings/" + rid + "/edit", 405},
		{http.MethodGet, "/api/recordings?collection_id=%2B" + cid, 400},
		{http.MethodGet, "/api/recordings?collection_id=%20" + cid, 400},
		{http.MethodGet, "/api/recordings/" + rid, 200},
		{http.MethodGet, "/api/history/abc/nearest", 404},
		{http.MethodGet, "/api/history/1/nearest/extra", 404},
		{http.MethodGet, "/api/history/1/closest", 404},
		{http.MethodPost, "/api/history/1/nearest", 405},
		{http.MethodGet, "/api/history/999/nearest", 404},
		{http.MethodPost, "/api/insights?collection_id=" + cid, 405},
		{http.MethodGet, "/api/insights?collection_id=x", 400},
		{http.MethodGet, "/api/insights?collection_id=999", 404},
		{http.MethodGet, "/api/insights?collection_id=" + cid + "&from=2026-01-02T00:00:00Z&to=2026-01-01T00:00:00Z", 400},
		{http.MethodGet, "/api/insights?collection_id=" + cid + "&from=2024-01-01T00:00:00Z&to=2026-01-01T00:00:00Z", 400},
		{http.MethodDelete, "/api/threads?collection_id=" + cid, 405},
		{http.MethodGet, "/api/threads?collection_id=" + cid + "&limit=0", 400},
		{http.MethodGet, "/api/threads?collection_id=" + cid + "&limit=501", 400},
		{http.MethodGet, "/api/threads?collection_id=" + cid + "&from=yesterday", 400},
		{http.MethodGet, "/api/threads?collection_id=999", 404},
		{http.MethodGet, "/api/threads/not-a-thread", 404},
		{http.MethodGet, "/api/threads/0123456789ABCDEF?collection_id=" + cid, 404},
		{http.MethodPost, "/api/threads/0123456789abcdef/extra", 404},
		{http.MethodPost, "/api/threads/0123456789abcdef", 405},
		{http.MethodGet, "/api/threads/0123456789abcdef", 400},
		{http.MethodGet, "/api/threads/0123456789abcdef?collection_id=" + cid, 404},
	} {
		w := request(t, h, tc.method, tc.target, nil)
		if w.Code != tc.status {
			t.Errorf("%s %s: %d, want %d (%s)", tc.method, tc.target, w.Code, tc.status, w.Body.String())
		}
	}
}

func TestHeadIsAllowedWhereverGetIs(t *testing.T) {
	db, h := testAdmin(t)
	e := publishFixture(t, db)
	rid, cid := itoa(e.Recording.ID), itoa(e.Recording.CollectionID)
	for _, target := range []string{
		"/api/settings", "/api/collections", "/api/recordings?collection_id=" + cid, "/api/recordings/" + rid,
		"/api/history?collection_id=" + cid, "/api/analytics?collection_id=" + cid, "/api/collections/" + cid + "/export",
		"/api/insights?collection_id=" + cid, "/api/threads?collection_id=" + cid,
	} {
		w := request(t, h, http.MethodHead, target, nil)
		if w.Code != 200 {
			t.Errorf("HEAD %s: %d %s", target, w.Code, w.Body.String())
		}
	}
	w := request(t, h, http.MethodDelete, "/api/settings", nil)
	if allow := w.Header().Get("Allow"); w.Code != 405 || !strings.Contains(allow, "HEAD") {
		t.Fatalf("Allow header %q", allow)
	}
}

func TestExportOmitsHistory(t *testing.T) {
	db, h := testAdmin(t)
	e := publishFixture(t, db)
	if err := db.AddHistory(context.Background(), model.History{CollectionID: e.Recording.CollectionID, Route: e.Recording.Route, Key: "k", Request: json.RawMessage(`{"secret":"history-marker-91ab"}`), Outcome: "miss"}); err != nil {
		t.Fatal(err)
	}
	w := request(t, h, http.MethodGet, "/api/collections/"+itoa(e.Recording.CollectionID)+"/export", nil)
	if w.Code != 200 {
		t.Fatalf("export: %d", w.Code)
	}
	if bytes.Contains(w.Body.Bytes(), []byte("history-marker-91ab")) {
		t.Fatal("exported snapshot contains a history request")
	}
	path := filepath.Join(t.TempDir(), "export.sqlite")
	if err := os.WriteFile(path, w.Body.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	raw, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	var n int
	if err = raw.QueryRow("SELECT count(*) FROM history").Scan(&n); err != nil || n != 0 {
		t.Fatalf("history rows=%d err=%v", n, err)
	}
}
