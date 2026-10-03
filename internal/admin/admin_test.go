package admin

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/local/llm-replay-proxy/internal/match"
	"github.com/local/llm-replay-proxy/internal/model"
	"github.com/local/llm-replay-proxy/internal/store"
)

func testAdmin(t *testing.T) (*store.Store, http.Handler) {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "test.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db, New(db)
}

func request(t *testing.T, h http.Handler, method, target string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var r io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		r = bytes.NewReader(b)
	}
	req := httptest.NewRequest(method, target, r)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	return w
}

func TestCollectionsSettingsAndOrigin(t *testing.T) {
	_, h := testAdmin(t)
	w := request(t, h, http.MethodPost, "/api/collections", map[string]any{"name": "demo", "exclusions": []string{"/seed"}})
	if w.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", w.Code, w.Body.String())
	}
	var c model.Collection
	if err := json.Unmarshal(w.Body.Bytes(), &c); err != nil {
		t.Fatal(err)
	}
	w = request(t, h, http.MethodPut, "/api/settings", model.Settings{Mode: "auto", ActiveCollectionID: c.ID, FirstEventDelayMS: 12, DelayMultiplier: .5})
	if w.Code != 200 {
		t.Fatalf("settings: %d %s", w.Code, w.Body.String())
	}
	w = request(t, h, http.MethodGet, "/api/settings", nil)
	var s model.Settings
	if err := json.Unmarshal(w.Body.Bytes(), &s); err != nil {
		t.Fatal(err)
	}
	if s.Mode != "auto" || s.ActiveCollectionID != c.ID || s.DelayMultiplier != .5 {
		t.Fatalf("settings: %+v", s)
	}
	req := httptest.NewRequest(http.MethodPut, "http://localhost/api/settings", bytes.NewBufferString(`{"mode":"replay","active_collection_id":1}`))
	req.Header.Set("Origin", "https://evil.example")
	w = httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusForbidden {
		t.Fatalf("cross-origin status %d", w.Code)
	}
	req = httptest.NewRequest(http.MethodPost, "http://localhost/api/collections", bytes.NewBufferString(`{"name":"fetch-metadata"}`))
	req.Header.Set("Sec-Fetch-Site", "cross-site")
	w = httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusForbidden {
		t.Fatalf("cross-site request without Origin status %d", w.Code)
	}
	req = httptest.NewRequest(http.MethodPost, "http://attacker.example/api/collections", bytes.NewBufferString(`{"name":"dns-rebind"}`))
	req.Host = "attacker.example"
	req.Header.Set("Origin", "http://attacker.example")
	w = httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusForbidden {
		t.Fatalf("non-loopback same-origin status %d", w.Code)
	}
	w = request(t, h, http.MethodPut, "/api/settings", model.Settings{Mode: "replay", ActiveCollectionID: c.ID, FirstEventDelayMS: maxDelayMS + 1, DelayMultiplier: 1})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("overflowing delay accepted: %d %s", w.Code, w.Body.String())
	}
	w = request(t, h, http.MethodPut, "/api/settings", model.Settings{Mode: "replay", ActiveCollectionID: c.ID, DelayMultiplier: maxDelayMultiplier + 1})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("overflowing multiplier accepted: %d %s", w.Code, w.Body.String())
	}
	w = request(t, h, http.MethodPost, "/api/collections", map[string]any{"name": "demo"})
	if w.Code != http.StatusConflict {
		t.Fatalf("duplicate collection: %d %s", w.Code, w.Body.String())
	}
}

func publishFixture(t *testing.T, db *store.Store) model.Entry {
	t.Helper()
	cs, err := db.Collections(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	req := []byte(`{"model":"m","messages":[{"role":"user","content":"old"}],"large":9007199254740993123456789}`)
	key, canonical, err := match.Key("/v1/chat/completions", "", req, nil)
	if err != nil {
		t.Fatal(err)
	}
	e, err := db.Publish(context.Background(), model.Recording{CollectionID: cs[0].ID, Key: key, Route: "/v1/chat/completions", Request: req, MatchingInput: canonical, Streaming: false}, model.Revision{Status: 200, Headers: map[string]string{"Content-Type": "application/json"}, Body: `{"id":"x","choices":[{"message":{"role":"assistant","content":"hello"},"finish_reason":"stop"}]}`, Source: "record"})
	if err != nil {
		t.Fatal(err)
	}
	return e
}

func TestInspectEditRestoreCompareAndHistory(t *testing.T) {
	db, h := testAdmin(t)
	e := publishFixture(t, db)
	w := request(t, h, http.MethodGet, "/api/recordings/"+itoa(e.Recording.ID), nil)
	if w.Code != 200 || !bytes.Contains(w.Body.Bytes(), []byte(`"text":"hello"`)) {
		t.Fatalf("inspect: %d %s", w.Code, w.Body.String())
	}
	w = request(t, h, http.MethodPost, "/api/recordings/"+itoa(e.Recording.ID)+"/edit", map[string]any{"text": "changed", "base_revision_id": e.Revision.ID})
	if w.Code != 201 {
		t.Fatalf("edit: %d %s", w.Code, w.Body.String())
	}
	var edited model.Entry
	if err := json.Unmarshal(w.Body.Bytes(), &edited); err != nil {
		t.Fatal(err)
	}
	if edited.Revision.ID == e.Revision.ID || edited.Revision.Source != "edit" {
		t.Fatalf("edited revision: %+v", edited.Revision)
	}
	w = request(t, h, http.MethodPost, "/api/recordings/"+itoa(e.Recording.ID)+"/restore", map[string]any{"revision_id": e.Revision.ID, "base_revision_id": edited.Revision.ID})
	if w.Code != 200 {
		t.Fatalf("restore: %d %s", w.Code, w.Body.String())
	}
	w = request(t, h, http.MethodPost, "/api/compare", map[string]any{"recording_id": e.Recording.ID, "request": map[string]any{"model": "m", "messages": []any{map[string]any{"role": "user", "content": "new"}}, "temperature": 1}})
	if w.Code != 200 || !bytes.Contains(w.Body.Bytes(), []byte(`/messages/0/content`)) || !bytes.Contains(w.Body.Bytes(), []byte(`/temperature`)) {
		t.Fatalf("compare: %d %s", w.Code, w.Body.String())
	}
	if err := db.AddHistory(context.Background(), model.History{CollectionID: e.Recording.CollectionID, Route: e.Recording.Route, Key: "k", Request: json.RawMessage(`{"model":"m","messages":[{"role":"user","content":"from history"}],"large":9007199254740993}`), Outcome: "miss"}); err != nil {
		t.Fatal(err)
	}
	w = request(t, h, http.MethodGet, "/api/history?collection_id="+itoa(e.Recording.CollectionID), nil)
	if w.Code != 200 || !bytes.Contains(w.Body.Bytes(), []byte(`"outcome":"miss"`)) {
		t.Fatalf("history: %d %s", w.Code, w.Body.String())
	}
	var history []model.History
	if err := json.Unmarshal(w.Body.Bytes(), &history); err != nil || len(history) != 1 {
		t.Fatalf("decode history: %v %#v", err, history)
	}
	w = request(t, h, http.MethodPost, "/api/compare", map[string]any{"recording_id": e.Recording.ID, "history_id": history[0].ID})
	if w.Code != 200 || !bytes.Contains(w.Body.Bytes(), []byte(`9007199254740993`)) || !bytes.Contains(w.Body.Bytes(), []byte(`"request_exists":true`)) {
		t.Fatalf("history compare: %d %s", w.Code, w.Body.String())
	}
}

func TestAdvancedEditValidationAndSnapshotRoundTrip(t *testing.T) {
	db, h := testAdmin(t)
	e := publishFixture(t, db)
	w := request(t, h, http.MethodPost, "/api/recordings/"+itoa(e.Recording.ID)+"/edit", map[string]any{"revision": map[string]any{"status": 200, "body": "not-json", "events": []any{}}, "base_revision_id": e.Revision.ID})
	if w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("malformed edit accepted: %d %s", w.Code, w.Body.String())
	}
	w = request(t, h, http.MethodGet, "/api/collections/"+itoa(e.Recording.CollectionID)+"/export", nil)
	if w.Code != 200 || len(w.Body.Bytes()) < 16 || string(w.Body.Bytes()[:15]) != "SQLite format 3" {
		t.Fatalf("export: %d %q", w.Code, w.Body.Bytes())
	}
	_, h2 := testAdmin(t)
	req := httptest.NewRequest(http.MethodPost, "/api/import", bytes.NewReader(w.Body.Bytes()))
	out := httptest.NewRecorder()
	h2.ServeHTTP(out, req)
	if out.Code != http.StatusCreated {
		t.Fatalf("import: %d %s", out.Code, out.Body.String())
	}
}

func TestAnalyticsEndpointValidationAndFullHistory(t *testing.T) {
	db, h := testAdmin(t)
	settings, _ := db.Settings(context.Background())
	base := time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC)
	d := int64(7)
	for i := 0; i < 225; i++ {
		if err := db.AddHistory(context.Background(), model.History{CollectionID: settings.ActiveCollectionID, Route: "/v1/responses", Key: strconv.Itoa(i), Request: []byte(`{}`), Outcome: "hit", CacheStatus: "hit", Source: "replay", DurationMS: &d, CreatedAt: base.Add(time.Duration(i) * time.Second).Format(time.RFC3339Nano)}); err != nil {
			t.Fatal(err)
		}
	}
	target := "/api/analytics?collection_id=" + itoa(settings.ActiveCollectionID) + "&from=" + base.Format(time.RFC3339) + "&to=" + base.Add(time.Hour).Format(time.RFC3339)
	w := request(t, h, http.MethodGet, target, nil)
	if w.Code != 200 {
		t.Fatalf("analytics: %d %s", w.Code, w.Body.String())
	}
	var a model.Analytics
	if err := json.Unmarshal(w.Body.Bytes(), &a); err != nil {
		t.Fatal(err)
	}
	if a.Total != 225 || a.LifetimeTotal != 225 || a.Hits != 225 || a.Sources["replay"].Duration.Samples != 225 {
		t.Fatalf("analytics ignored full history: %+v", a)
	}
	w = request(t, h, http.MethodGet, "/api/analytics?collection_id="+itoa(settings.ActiveCollectionID)+"&from=2026-01-03T00:00:00Z&to=2026-01-02T00:00:00Z", nil)
	if w.Code != 400 {
		t.Fatalf("inverted range: %d", w.Code)
	}
}

func TestAdvancedEditCannotChangeRequestProvenance(t *testing.T) {
	db, h := testAdmin(t)
	e := publishFixture(t, db)
	changed := e.Revision
	changed.Request = json.RawMessage(`{"large":9007199254740992}`)
	changed.MatchingInput = json.RawMessage(`{"tampered":true}`)
	w := request(t, h, http.MethodPost, "/api/recordings/"+itoa(e.Recording.ID)+"/edit", map[string]any{"revision": changed, "base_revision_id": e.Revision.ID})
	if w.Code != http.StatusCreated {
		t.Fatalf("advanced edit: %d %s", w.Code, w.Body.String())
	}
	var got model.Entry
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if string(got.Revision.Request) != string(e.Revision.Request) || string(got.Revision.MatchingInput) != string(e.Revision.MatchingInput) {
		t.Fatalf("provenance changed: request=%s matching=%s", got.Revision.Request, got.Revision.MatchingInput)
	}
}

func TestInspectRawPrecisionAndRevisionConflicts(t *testing.T) {
	db, h := testAdmin(t)
	e := publishFixture(t, db)
	w := request(t, h, http.MethodGet, "/api/recordings/"+itoa(e.Recording.ID), nil)
	var inspect struct {
		RequestText       string `json:"request_text"`
		MatchingInputText string `json:"matching_input_text"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &inspect); err != nil {
		t.Fatal(err)
	}
	if inspect.RequestText != string(e.Recording.Request) || inspect.MatchingInputText != string(e.Recording.MatchingInput) {
		t.Fatalf("raw inspect mismatch: %#v", inspect)
	}
	if !strings.Contains(inspect.RequestText, "9007199254740993123456789") {
		t.Fatalf("large number missing from raw request: %s", inspect.RequestText)
	}
	changed := e.Revision
	changed.Status = http.StatusNoContent
	w = request(t, h, http.MethodPost, "/api/recordings/"+itoa(e.Recording.ID)+"/edit", map[string]any{"revision": changed, "base_revision_id": e.Revision.ID})
	if w.Code != http.StatusUnprocessableEntity || !strings.Contains(w.Body.String(), "status and headers") {
		t.Fatalf("status edit: %d %s", w.Code, w.Body.String())
	}
	w = request(t, h, http.MethodPost, "/api/recordings/"+itoa(e.Recording.ID)+"/edit", map[string]any{"text": "first", "base_revision_id": e.Revision.ID})
	if w.Code != http.StatusCreated {
		t.Fatalf("first edit: %d %s", w.Code, w.Body.String())
	}
	w = request(t, h, http.MethodPost, "/api/recordings/"+itoa(e.Recording.ID)+"/edit", map[string]any{"text": "stale", "base_revision_id": e.Revision.ID})
	if w.Code != http.StatusConflict {
		t.Fatalf("stale edit: %d %s", w.Code, w.Body.String())
	}
	w = request(t, h, http.MethodPost, "/api/recordings/"+itoa(e.Recording.ID)+"/restore", map[string]any{"revision_id": e.Revision.ID, "base_revision_id": e.Revision.ID})
	if w.Code != http.StatusConflict {
		t.Fatalf("stale restore: %d %s", w.Code, w.Body.String())
	}
}

func itoa(v int64) string { return strconv.FormatInt(v, 10) }
