package admin

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"maps"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
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
	key, canonical, err := match.Key("/v1/chat/completions", req, nil)
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

func TestLazyRevisionInspectionPreservesDetailsAndScope(t *testing.T) {
	db, h := testAdmin(t)
	first := publishFixture(t, db)
	newRevision := first.Revision
	newRevision.Body = strings.Replace(newRevision.Body, "hello", "updated", 1)
	second, err := db.Publish(context.Background(), first.Recording, newRevision)
	if err != nil {
		t.Fatal(err)
	}
	recordings := []model.Entry{second}
	// Exercise stream details as well as non-streaming bodies.
	rec := first.Recording
	rec.Request = []byte(`{"model":"m","messages":[{"role":"user","content":"stream"}],"stream":true}`)
	rec.Key, rec.MatchingInput, err = match.Key(rec.Route, rec.Request, nil)
	if err != nil {
		t.Fatal(err)
	}
	rec.Streaming = true
	stream, err := db.Publish(context.Background(), rec, model.Revision{Status: 200, Headers: map[string]string{"Content-Type": "text/event-stream"}, Source: "recorded", Events: []model.Event{
		{Data: "data: {\"id\":\"x\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"hello\"},\"finish_reason\":null}]}\r\n\r\n", OffsetMS: 17},
		{Data: "data: {\"id\":\"x\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n", OffsetMS: 17},
		{Data: "data: [DONE]\n\n", OffsetMS: 99},
	}})
	if err != nil {
		t.Fatal(err)
	}
	recordings = append(recordings, stream)
	for _, entry := range recordings {
		base := "/api/recordings/" + itoa(entry.Recording.ID)
		full := request(t, h, http.MethodGet, base, nil)
		lazy := request(t, h, http.MethodGet, base+"?revision_details=lazy", nil)
		var original struct {
			Revision    model.Revision
			Revisions   []model.Revision
			RequestText string `json:"request_text"`
		}
		var light struct {
			Revision    model.Revision
			Summaries   []model.RevisionSummary `json:"revision_summaries"`
			RequestText string                  `json:"request_text"`
		}
		if full.Code != 200 || lazy.Code != 200 {
			t.Fatalf("inspect: %d %d", full.Code, lazy.Code)
		}
		if err = json.Unmarshal(full.Body.Bytes(), &original); err != nil {
			t.Fatal(err)
		}
		if err = json.Unmarshal(lazy.Body.Bytes(), &light); err != nil {
			t.Fatal(err)
		}
		var shape map[string]json.RawMessage
		if err = json.Unmarshal(lazy.Body.Bytes(), &shape); err != nil {
			t.Fatal(err)
		}
		if _, exists := shape["revisions"]; exists {
			t.Fatal("lazy inspection returned full history")
		}
		if !reflect.DeepEqual(original.Revision, light.Revision) || original.RequestText != light.RequestText || len(original.Revisions) != len(light.Summaries) {
			t.Fatal("lazy inspection lost active content or revisions")
		}
		for i, summary := range light.Summaries {
			expected := original.Revisions[i]
			if summary.ID != expected.ID || summary.RecordingID != expected.RecordingID || summary.Status != expected.Status || summary.Source != expected.Source || summary.CreatedAt != expected.CreatedAt {
				t.Fatal("metadata changed")
			}
			detail := request(t, h, http.MethodGet, base+"/revisions/"+itoa(summary.ID), nil)
			var got model.Revision
			if detail.Code != 200 {
				t.Fatalf("detail: %d %s", detail.Code, detail.Body.String())
			}
			if err = json.Unmarshal(detail.Body.Bytes(), &got); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, expected) {
				t.Fatal("lazy detail differs from full API")
			}
			if head := request(t, h, http.MethodHead, base+"/revisions/"+itoa(summary.ID), nil); head.Code != 200 || head.Body.Len() != 0 {
				t.Fatalf("HEAD: %d %s", head.Code, head.Body.String())
			}
		}
	}
	base := "/api/recordings/" + itoa(first.Recording.ID)
	for _, path := range []string{base + "/revisions/" + itoa(stream.Revision.ID), base + "/revisions/999999", base + "/revisions/-1", base + "/revisions/1/extra"} {
		if w := request(t, h, http.MethodGet, path, nil); w.Code != 404 {
			t.Fatalf("unscoped/invalid detail %s: %d", path, w.Code)
		}
	}
	detail := base + "/revisions/" + itoa(first.Revision.ID)
	if w := request(t, h, http.MethodPost, detail, nil); w.Code != 405 {
		t.Fatalf("POST detail: %d", w.Code)
	}
	if w := request(t, h, http.MethodPost, base+"/restore", map[string]any{"revision_id": first.Revision.ID, "base_revision_id": first.Revision.ID}); w.Code != 409 {
		t.Fatalf("stale restore: %d", w.Code)
	}
	if err = db.DeleteRecording(context.Background(), first.Recording.ID); err != nil {
		t.Fatal(err)
	}
	if w := request(t, h, http.MethodGet, detail, nil); w.Code != 404 {
		t.Fatalf("deleted detail: %d", w.Code)
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
	var changed map[string]any
	b, _ := json.Marshal(e.Revision)
	if err := json.Unmarshal(b, &changed); err != nil {
		t.Fatal(err)
	}
	if _, ok := changed["request"]; ok {
		t.Fatal("revisions expose request bytes")
	}
	for field, value := range map[string]any{"request": map[string]any{"large": 9007199254740992}, "matching_input": map[string]any{"tampered": true}} {
		tampered := maps.Clone(changed)
		tampered[field] = value
		w := request(t, h, http.MethodPost, "/api/recordings/"+itoa(e.Recording.ID)+"/edit", map[string]any{"revision": tampered, "base_revision_id": e.Revision.ID})
		if w.Code != http.StatusBadRequest {
			t.Fatalf("edit carrying %s: %d %s", field, w.Code, w.Body.String())
		}
	}
	w := request(t, h, http.MethodPost, "/api/recordings/"+itoa(e.Recording.ID)+"/edit", map[string]any{"revision": changed, "base_revision_id": e.Revision.ID})
	if w.Code != http.StatusCreated {
		t.Fatalf("advanced edit: %d %s", w.Code, w.Body.String())
	}
	got, err := db.Get(context.Background(), e.Recording.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Revision.ID == e.Revision.ID || string(got.Revision.Request) != string(e.Revision.Request) || string(got.Revision.MatchingInput) != string(e.Revision.MatchingInput) {
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

func TestListsCarrySummariesAndDetailsCarryRequestOnce(t *testing.T) {
	db, h := testAdmin(t)
	e := publishFixture(t, db)
	cid := itoa(e.Recording.CollectionID)
	marker := `"large":9007199254740993123456789`
	if err := db.AddHistory(context.Background(), model.History{CollectionID: e.Recording.CollectionID, Route: e.Recording.Route, Key: e.Recording.Key, Request: e.Recording.Request, Outcome: "hit", RecordingID: e.Recording.ID}); err != nil {
		t.Fatal(err)
	}
	for _, target := range []string{"/api/recordings?collection_id=" + cid, "/api/history?collection_id=" + cid} {
		w := request(t, h, http.MethodGet, target, nil)
		if w.Code != 200 || strings.Contains(w.Body.String(), "9007199254740993123456789") || !strings.Contains(w.Body.String(), `"preview":"old"`) {
			t.Fatalf("%s: %d %s", target, w.Code, w.Body.String())
		}
	}
	w := request(t, h, http.MethodGet, "/api/recordings/"+itoa(e.Recording.ID), nil)
	if n := strings.Count(w.Body.String(), "9007199254740993123456789"); n != 2 {
		t.Fatalf("inspect carries the request %d times, want request_text and matching_input_text only", n)
	}
	var listed []model.History
	w = request(t, h, http.MethodGet, "/api/history?collection_id="+cid, nil)
	if err := json.Unmarshal(w.Body.Bytes(), &listed); err != nil || len(listed) != 1 {
		t.Fatalf("history: %v %s", err, w.Body.String())
	}
	w = request(t, h, http.MethodGet, "/api/history/"+itoa(listed[0].ID), nil)
	var item struct {
		RequestText string `json:"request_text"`
		Outcome     string `json:"outcome"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &item); err != nil || item.Outcome != "hit" || !strings.Contains(item.RequestText, marker) {
		t.Fatalf("history item: %d %s", w.Code, w.Body.String())
	}
	if w = request(t, h, http.MethodGet, "/api/history?collection_id="+cid+"&after_id="+itoa(listed[0].ID), nil); w.Body.String() != "[]\n" {
		t.Fatalf("after_id: %s", w.Body.String())
	}
	if w = request(t, h, http.MethodGet, "/api/history?collection_id="+cid+"&after_id=-1", nil); w.Code != 400 {
		t.Fatalf("invalid after_id: %d", w.Code)
	}
}

func TestDeleteEndpoints(t *testing.T) {
	db, h := testAdmin(t)
	e := publishFixture(t, db)
	cid := itoa(e.Recording.CollectionID)
	if w := request(t, h, http.MethodDelete, "/api/collections/"+cid, nil); w.Code != 409 || !strings.Contains(w.Body.String(), "collection_active") {
		t.Fatalf("delete active collection: %d %s", w.Code, w.Body.String())
	}
	if w := request(t, h, http.MethodDelete, "/api/history?collection_id="+cid, nil); w.Code != 204 {
		t.Fatalf("clear history: %d %s", w.Code, w.Body.String())
	}
	if w := request(t, h, http.MethodDelete, "/api/recordings/"+itoa(e.Recording.ID), nil); w.Code != 204 {
		t.Fatalf("delete recording: %d %s", w.Code, w.Body.String())
	}
	if w := request(t, h, http.MethodGet, "/api/recordings/"+itoa(e.Recording.ID), nil); w.Code != 404 {
		t.Fatalf("deleted recording: %d", w.Code)
	}
	other, err := db.CreateCollection(context.Background(), "other", nil)
	if err != nil {
		t.Fatal(err)
	}
	if w := request(t, h, http.MethodDelete, "/api/collections/"+itoa(other.ID), nil); w.Code != 204 {
		t.Fatalf("delete collection: %d %s", w.Code, w.Body.String())
	}
	if w := request(t, h, http.MethodDelete, "/api/collections/"+itoa(other.ID), nil); w.Code != 404 {
		t.Fatalf("delete missing collection: %d", w.Code)
	}
}
