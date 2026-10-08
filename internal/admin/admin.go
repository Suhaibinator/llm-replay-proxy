// Package admin exposes the local control-panel API.
package admin

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"maps"
	"math"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/local/llm-replay-proxy/internal/match"
	"github.com/local/llm-replay-proxy/internal/model"
	"github.com/local/llm-replay-proxy/internal/protocol"
	"github.com/local/llm-replay-proxy/internal/store"
)

const maxImportSize = 1 << 30
const maxEditSize = 128 << 20
const maxDelayMultiplier = 1_000_000
const maxDelayMS = int64(24 * time.Hour / time.Millisecond)

type handler struct{ db *store.Store }

// New returns the admin API handler. Mutation requests carrying a browser Origin
// are accepted only when the origin matches the request Host.
func New(db *store.Store) http.Handler { return &handler{db: db} }

func (h *handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if mutating(r.Method) && !sameOrigin(r) {
		writeError(w, http.StatusForbidden, "origin_forbidden", "request origin does not match this server")
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	p := strings.TrimSuffix(r.URL.Path, "/")
	switch {
	case p == "/api/settings":
		h.settings(w, r)
	case p == "/api/collections":
		h.collections(w, r)
	case p == "/api/recordings":
		h.recordings(w, r)
	case p == "/api/history":
		h.history(w, r)
	case p == "/api/analytics":
		h.analytics(w, r)
	case p == "/api/insights":
		h.insights(w, r)
	case p == "/api/threads":
		h.threads(w, r)
	case strings.HasPrefix(p, "/api/threads/"):
		h.threadDetail(w, r, p)
	case p == "/api/import":
		h.importDB(w, r)
	case p == "/api/compare":
		h.compare(w, r)
	case strings.HasPrefix(p, "/api/collections/"):
		h.collectionAction(w, r, p)
	case strings.HasPrefix(p, "/api/recordings/"):
		h.recordingAction(w, r, p)
	case strings.HasPrefix(p, "/api/history/"):
		h.historyItem(w, r, p)
	default:
		writeError(w, http.StatusNotFound, "not_found", "endpoint not found")
	}
}

func (h *handler) analytics(w http.ResponseWriter, r *http.Request) {
	if !isRead(r) {
		methodNotAllowed(w, "GET, HEAD")
		return
	}
	id, ok := collectionQuery(w, h, r)
	if !ok {
		return
	}
	from, to, ok := boundedRange(w, r)
	if !ok {
		return
	}
	v, err := h.db.Analytics(r.Context(), id, from, to)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, 200, v)
}

// insights serves the dashboard aggregates; same range rules as analytics.
func (h *handler) insights(w http.ResponseWriter, r *http.Request) {
	if !isRead(r) {
		methodNotAllowed(w, "GET, HEAD")
		return
	}
	id, ok := collectionQuery(w, h, r)
	if !ok {
		return
	}
	from, to, ok := boundedRange(w, r)
	if !ok {
		return
	}
	v, err := h.db.Insights(r.Context(), id, from, to, store.InsightFilter{Model: r.URL.Query().Get("model"), Provider: r.URL.Query().Get("provider")})
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, 200, v)
}

func (h *handler) threads(w http.ResponseWriter, r *http.Request) {
	if !isRead(r) {
		methodNotAllowed(w, "GET, HEAD")
		return
	}
	id, ok := collectionQuery(w, h, r)
	if !ok {
		return
	}
	limit := 50
	if raw := r.URL.Query().Get("limit"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 || n > 500 {
			writeError(w, 400, "invalid_request", "limit must be between 1 and 500")
			return
		}
		limit = n
	}
	var from, to time.Time
	if !parseTime(w, r, "from", &from) || !parseTime(w, r, "to", &to) {
		return
	}
	if !from.IsZero() && !to.IsZero() && !from.Before(to) {
		writeError(w, 400, "invalid_request", "from must be before to")
		return
	}
	v, err := h.db.Threads(r.Context(), id, limit, from, to)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, 200, v)
}

// threadDetail serves /api/threads/{thread}; a thread is the 16 lowercase
// hex digits of a request summary's thread fingerprint.
func (h *handler) threadDetail(w http.ResponseWriter, r *http.Request, p string) {
	thread := strings.TrimPrefix(p, "/api/threads/")
	if !validThread(thread) {
		writeError(w, 404, "not_found", "endpoint not found")
		return
	}
	if !isRead(r) {
		methodNotAllowed(w, "GET, HEAD")
		return
	}
	id, ok := queryID(w, r, "collection_id")
	if !ok {
		return
	}
	v, err := h.db.Thread(r.Context(), id, thread)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, 200, v)
}

func validThread(v string) bool {
	if len(v) != 16 {
		return false
	}
	for i := 0; i < len(v); i++ {
		if !(v[i] >= '0' && v[i] <= '9' || v[i] >= 'a' && v[i] <= 'f') {
			return false
		}
	}
	return true
}

// collectionQuery reads collection_id and checks the collection exists.
func collectionQuery(w http.ResponseWriter, h *handler, r *http.Request) (int64, bool) {
	id, ok := queryID(w, r, "collection_id")
	if !ok {
		return 0, false
	}
	if _, err := h.db.Collection(r.Context(), id); err != nil {
		writeStoreError(w, err)
		return 0, false
	}
	return id, true
}

// parseTime reads an optional RFC 3339 query parameter into dst, in UTC.
func parseTime(w http.ResponseWriter, r *http.Request, name string, dst *time.Time) bool {
	raw := r.URL.Query().Get(name)
	if raw == "" {
		return true
	}
	v, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		writeError(w, 400, "invalid_request", name+" must be RFC3339")
		return false
	}
	*dst = v.UTC()
	return true
}

// boundedRange reads from/to: the last 24 hours by default, at most 366 days.
func boundedRange(w http.ResponseWriter, r *http.Request) (time.Time, time.Time, bool) {
	to := time.Now().UTC()
	from := to.Add(-24 * time.Hour)
	if !parseTime(w, r, "from", &from) || !parseTime(w, r, "to", &to) {
		return from, to, false
	}
	if !from.Before(to) {
		writeError(w, 400, "invalid_request", "from must be before to")
		return from, to, false
	}
	if to.Sub(from) > 366*24*time.Hour {
		writeError(w, 400, "invalid_request", "analytics range cannot exceed 366 days")
		return from, to, false
	}
	return from, to, true
}

func (h *handler) settings(w http.ResponseWriter, r *http.Request) {
	switch {
	case isRead(r):
		v, err := h.db.Settings(r.Context())
		if err != nil {
			writeStoreError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, v)
	case r.Method == http.MethodPut:
		var v model.Settings
		if !decodeJSON(w, r, &v) {
			return
		}
		if v.Mode != "record" && v.Mode != "replay" && v.Mode != "auto" {
			writeError(w, http.StatusBadRequest, "invalid_settings", "mode must be record, replay, or auto")
			return
		}
		if v.FirstEventDelayMS < 0 || v.FirstEventDelayMS > maxDelayMS {
			writeError(w, http.StatusBadRequest, "invalid_settings", fmt.Sprintf("first_event_delay_ms must be between 0 and %d", maxDelayMS))
			return
		}
		if v.DelayMultiplier < 0 || math.IsNaN(v.DelayMultiplier) || math.IsInf(v.DelayMultiplier, 0) || v.DelayMultiplier > maxDelayMultiplier {
			writeError(w, http.StatusBadRequest, "invalid_settings", fmt.Sprintf("delay_multiplier must be between 0 and %d", maxDelayMultiplier))
			return
		}
		if v.HistoryLimit < 0 || v.HistoryLimit > store.MaxHistoryLimit {
			writeError(w, http.StatusBadRequest, "invalid_settings", fmt.Sprintf("history_limit must be between 0 and %d", store.MaxHistoryLimit))
			return
		}
		if _, err := h.db.Collection(r.Context(), v.ActiveCollectionID); err != nil {
			if errors.Is(err, store.ErrNotFound) {
				writeError(w, 400, "invalid_settings", "active_collection_id does not identify a collection")
				return
			}
			writeStoreError(w, err)
			return
		}
		if err := h.db.SetSettings(r.Context(), v); err != nil {
			writeStoreError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, v)
	default:
		methodNotAllowed(w, "GET, HEAD, PUT")
	}
}

func (h *handler) collections(w http.ResponseWriter, r *http.Request) {
	switch {
	case isRead(r):
		v, err := h.db.Collections(r.Context())
		if err != nil {
			writeStoreError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, v)
	case r.Method == http.MethodPost:
		var in struct {
			Name       string   `json:"name"`
			Exclusions []string `json:"exclusions"`
		}
		if !decodeJSON(w, r, &in) {
			return
		}
		in.Name = strings.TrimSpace(in.Name)
		if in.Name == "" {
			writeError(w, http.StatusBadRequest, "invalid_collection", "name is required")
			return
		}
		if err := match.ValidateExclusions(in.Exclusions); err != nil {
			writeError(w, http.StatusBadRequest, "invalid_collection", err.Error())
			return
		}
		v, err := h.db.CreateCollection(r.Context(), in.Name, in.Exclusions)
		if err != nil {
			writeStoreError(w, err)
			return
		}
		writeJSON(w, http.StatusCreated, v)
	default:
		methodNotAllowed(w, "GET, HEAD, POST")
	}
}

func (h *handler) recordings(w http.ResponseWriter, r *http.Request) {
	if !isRead(r) {
		methodNotAllowed(w, "GET, HEAD")
		return
	}
	id, ok := queryID(w, r, "collection_id")
	if !ok {
		return
	}
	v, err := h.db.Recordings(r.Context(), id)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, v)
}

func (h *handler) history(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodDelete {
		id, ok := queryID(w, r, "collection_id")
		if !ok {
			return
		}
		if err := h.db.ClearHistory(r.Context(), id); err != nil {
			writeStoreError(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if !isRead(r) {
		methodNotAllowed(w, "GET, HEAD, DELETE")
		return
	}
	id, ok := queryID(w, r, "collection_id")
	if !ok {
		return
	}
	limit := 200
	if raw := r.URL.Query().Get("limit"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 || n > 1000 {
			writeError(w, 400, "invalid_request", "limit must be between 1 and 1000")
			return
		}
		limit = n
	}
	var after int64
	if raw := r.URL.Query().Get("after_id"); raw != "" {
		n, ok := parseID(raw)
		if !ok {
			writeError(w, 400, "invalid_request", "after_id must be a positive integer")
			return
		}
		after = n
	}
	v, err := h.db.HistoryAfter(r.Context(), id, after, limit, r.URL.Query().Get("provider"))
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, v)
}

// historyItem serves one history row with its exact request text, and
// /api/history/{id}/nearest its closest recordings.
func (h *handler) historyItem(w http.ResponseWriter, r *http.Request, p string) {
	parts := strings.Split(strings.TrimPrefix(p, "/api/history/"), "/")
	id, ok := parseID(parts[0])
	if !ok || len(parts) > 2 || (len(parts) == 2 && parts[1] != "nearest") {
		writeError(w, 404, "not_found", "endpoint not found")
		return
	}
	if !isRead(r) {
		methodNotAllowed(w, "GET, HEAD")
		return
	}
	if len(parts) == 2 {
		v, err := h.db.Nearest(r.Context(), id)
		if err != nil {
			writeStoreError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"candidates": v})
		return
	}
	item, err := h.db.HistoryItem(r.Context(), id)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, struct {
		model.History
		RequestText string `json:"request_text"`
	}{item, string(item.Request)})
}

func (h *handler) recordingAction(w http.ResponseWriter, r *http.Request, p string) {
	rest := strings.TrimPrefix(p, "/api/recordings/")
	parts := strings.Split(rest, "/")
	// The path is validated before the method: a path that cannot name a
	// resource is 404 for every method, and only valid paths report 405.
	id, ok := parseID(parts[0])
	if !ok || len(parts) > 2 || (len(parts) == 2 && parts[1] != "edit" && parts[1] != "restore") {
		writeError(w, 404, "not_found", "endpoint not found")
		return
	}
	if len(parts) == 1 {
		if r.Method == http.MethodDelete {
			if err := h.db.DeleteRecording(r.Context(), id); err != nil {
				writeStoreError(w, err)
				return
			}
			w.WriteHeader(http.StatusNoContent)
			return
		}
		h.inspect(w, r, id)
		return
	}
	switch parts[1] {
	case "edit":
		h.edit(w, r, id)
	case "restore":
		h.restore(w, r, id)
	}
}

func (h *handler) inspect(w http.ResponseWriter, r *http.Request, id int64) {
	if !isRead(r) {
		methodNotAllowed(w, "GET, HEAD, DELETE")
		return
	}
	e, err := h.db.Get(r.Context(), id)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	revs, err := h.db.Revisions(r.Context(), id)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	text, reason := protocol.Text(e.Recording.Route, e.Recording.Streaming, e.Revision)
	writeJSON(w, 200, struct {
		Recording         model.Recording  `json:"recording"`
		Revision          model.Revision   `json:"revision"`
		Revisions         []model.Revision `json:"revisions"`
		Text              string           `json:"text"`
		Reason            string           `json:"text_unavailable_reason"`
		RequestText       string           `json:"request_text"`
		MatchingInputText string           `json:"matching_input_text"`
	}{e.Recording, e.Revision, revs, text, reason, string(e.Recording.Request), string(e.Recording.MatchingInput)})
}

func (h *handler) edit(w http.ResponseWriter, r *http.Request, id int64) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w, "POST")
		return
	}
	e, err := h.db.Get(r.Context(), id)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	var raw struct {
		Text           *string         `json:"text"`
		Revision       *model.Revision `json:"revision"`
		BaseRevisionID int64           `json:"base_revision_id"`
	}
	if !decodeJSONLimit(w, r, &raw, maxEditSize) {
		return
	}
	if (raw.Text == nil) == (raw.Revision == nil) {
		writeError(w, 400, "invalid_edit", "provide exactly one of text or revision")
		return
	}
	if raw.BaseRevisionID <= 0 {
		writeError(w, 400, "invalid_edit", "base_revision_id is required")
		return
	}
	if raw.BaseRevisionID != e.Revision.ID {
		writeRevisionConflict(w)
		return
	}
	var revision model.Revision
	if raw.Text != nil {
		revision, err = protocol.EditText(e.Recording.Route, e.Recording.Streaming, e.Revision, *raw.Text)
	} else {
		revision = *raw.Revision
		if revision.Status != e.Revision.Status || !maps.Equal(revision.Headers, e.Revision.Headers) {
			writeError(w, 422, "invalid_revision", "response status and headers cannot be edited")
			return
		}
	}
	// Request provenance is immutable metadata, not part of the editable response.
	// Always retain its exact bytes; browser JSON parsing can otherwise round large
	// request numbers even when the operator only edits response events.
	revision.Request = append(json.RawMessage(nil), e.Revision.Request...)
	revision.MatchingInput = append(json.RawMessage(nil), e.Revision.MatchingInput...)
	if err == nil {
		err = validateHeaders(revision.Headers)
	}
	if err == nil {
		err = protocol.Validate(e.Recording.Route, e.Recording.Streaming, revision)
	}
	if err != nil {
		writeError(w, 422, "invalid_revision", err.Error())
		return
	}
	revision.ID, revision.RecordingID, revision.Source, revision.CreatedAt = 0, id, "edit", ""
	out, err := h.db.PublishIfActive(r.Context(), e.Recording, revision, raw.BaseRevisionID)
	if errors.Is(err, store.ErrConflict) {
		writeRevisionConflict(w)
		return
	}
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, 201, out)
}

func (h *handler) restore(w http.ResponseWriter, r *http.Request, id int64) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w, "POST")
		return
	}
	var in struct {
		RevisionID     int64 `json:"revision_id"`
		BaseRevisionID int64 `json:"base_revision_id"`
	}
	if !decodeJSON(w, r, &in) {
		return
	}
	if in.RevisionID <= 0 {
		writeError(w, 400, "invalid_request", "revision_id is required")
		return
	}
	if in.BaseRevisionID <= 0 {
		writeError(w, 400, "invalid_request", "base_revision_id is required")
		return
	}
	v, err := h.db.RestoreIfActive(r.Context(), id, in.RevisionID, in.BaseRevisionID)
	if errors.Is(err, store.ErrConflict) {
		writeRevisionConflict(w)
		return
	}
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, 200, v)
}

func (h *handler) collectionAction(w http.ResponseWriter, r *http.Request, p string) {
	parts := strings.Split(strings.TrimPrefix(p, "/api/collections/"), "/")
	id, ok := parseID(parts[0])
	if !ok || len(parts) > 2 || (len(parts) == 2 && parts[1] != "export") {
		writeError(w, 404, "not_found", "endpoint not found")
		return
	}
	if len(parts) == 1 {
		if r.Method != http.MethodDelete {
			methodNotAllowed(w, "DELETE")
			return
		}
		if err := h.db.DeleteCollection(r.Context(), id); err != nil {
			writeStoreError(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if !isRead(r) {
		methodNotAllowed(w, "GET, HEAD")
		return
	}
	dir, err := os.MkdirTemp("", "llm-replay-export-")
	if err != nil {
		writeStoreError(w, err)
		return
	}
	defer os.RemoveAll(dir)
	path := filepath.Join(dir, "collection.sqlite")
	if err := h.db.Export(r.Context(), id, path); err != nil {
		writeStoreError(w, err)
		return
	}
	w.Header().Set("Content-Type", "application/vnd.sqlite3")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="collection-%d.sqlite"`, id))
	http.ServeFile(w, r, path)
}

func (h *handler) importDB(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w, "POST")
		return
	}
	f, err := os.CreateTemp("", "llm-replay-import-*.sqlite")
	if err != nil {
		writeStoreError(w, err)
		return
	}
	path := f.Name()
	defer os.Remove(path)
	n, copyErr := io.Copy(f, http.MaxBytesReader(w, r.Body, maxImportSize))
	closeErr := f.Close()
	if tooLarge(copyErr) {
		writeError(w, http.StatusRequestEntityTooLarge, "request_too_large", fmt.Sprintf("SQLite snapshot exceeds %d bytes", int64(maxImportSize)))
		return
	}
	if copyErr != nil || closeErr != nil {
		writeError(w, 400, "invalid_import", "could not read SQLite snapshot")
		return
	}
	if n == 0 {
		writeError(w, 400, "invalid_import", "empty SQLite snapshot")
		return
	}
	v, err := h.db.Import(r.Context(), path)
	if errors.Is(err, store.ErrInvalidSnapshot) {
		writeError(w, 422, "invalid_import", err.Error())
		return
	}
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, 201, v)
}

type difference struct {
	Path            string `json:"path"`
	Request         any    `json:"request"`
	Recorded        any    `json:"recorded"`
	RequestExists   bool   `json:"request_exists"`
	RecordedExists  bool   `json:"recorded_exists"`
	RequestDisplay  string `json:"request_display"`
	RecordedDisplay string `json:"recorded_display"`
	// Excluded marks a path under one of the collection's match exclusions:
	// the difference did not affect matching.
	Excluded bool `json:"excluded"`
}

// markExcluded flags the differences that the collection's exclusions hide
// from matching.
func markExcluded(d []difference, exclusions []string) []difference {
	for i := range d {
		d[i].Excluded = match.Excluded(d[i].Path, exclusions)
	}
	return d
}

func (h *handler) compare(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w, "POST")
		return
	}
	var in struct {
		Request     json.RawMessage `json:"request"`
		RecordingID int64           `json:"recording_id"`
		HistoryID   int64           `json:"history_id"`
	}
	if !decodeJSON(w, r, &in) {
		return
	}
	if in.RecordingID <= 0 || (len(in.Request) == 0) == (in.HistoryID == 0) {
		writeError(w, 400, "invalid_request", "provide recording_id and exactly one of request or history_id")
		return
	}
	e, err := h.db.Get(r.Context(), in.RecordingID)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	collection, err := h.db.Collection(r.Context(), e.Recording.CollectionID)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	if in.HistoryID != 0 {
		history, historyErr := h.db.HistoryItem(r.Context(), in.HistoryID)
		if historyErr != nil {
			writeStoreError(w, historyErr)
			return
		}
		if history.CollectionID != e.Recording.CollectionID {
			writeError(w, 400, "invalid_request", "history and recording belong to different collections")
			return
		}
		in.Request = history.Request
		if history.Route != e.Recording.Route {
			d := []difference{newDifference("/$route", history.Route, e.Recording.Route, true, true)}
			var a, b any
			if decodeJSONValue(in.Request, &a) != nil || decodeJSONValue(e.Recording.Request, &b) != nil {
				writeError(w, 400, "invalid_request", "stored request is not valid JSON")
				return
			}
			diffJSON("", a, b, &d)
			writeJSON(w, 200, map[string]any{"differences": markExcluded(d, collection.Exclusions)})
			return
		}
	}
	var a, b any
	if decodeJSONValue(in.Request, &a) != nil || decodeJSONValue(e.Recording.Request, &b) != nil {
		writeError(w, 400, "invalid_request", "request must be a JSON value")
		return
	}
	if _, ok := a.(map[string]any); !ok {
		writeError(w, 400, "invalid_request", "request must be a JSON object")
		return
	}
	d := make([]difference, 0)
	diffJSON("", a, b, &d)
	writeJSON(w, 200, map[string]any{"differences": markExcluded(d, collection.Exclusions)})
}

func diffJSON(path string, a, b any, out *[]difference) {
	am, aok := a.(map[string]any)
	bm, bok := b.(map[string]any)
	if aok && bok {
		keys := map[string]struct{}{}
		for k := range am {
			keys[k] = struct{}{}
		}
		for k := range bm {
			keys[k] = struct{}{}
		}
		names := make([]string, 0, len(keys))
		for k := range keys {
			names = append(names, k)
		}
		sortStrings(names)
		for _, k := range names {
			av, aexists := am[k]
			bv, bexists := bm[k]
			p := path + "/" + escapePointer(k)
			if !aexists {
				*out = append(*out, newDifference(p, nil, bv, false, true))
			} else if !bexists {
				*out = append(*out, newDifference(p, av, nil, true, false))
			} else {
				diffJSON(p, av, bv, out)
			}
		}
		return
	}
	aa, aok := a.([]any)
	ba, bok := b.([]any)
	if aok && bok {
		n := len(aa)
		if len(ba) > n {
			n = len(ba)
		}
		for i := 0; i < n; i++ {
			p := fmt.Sprintf("%s/%d", path, i)
			if i >= len(aa) {
				*out = append(*out, newDifference(p, nil, ba[i], false, true))
			} else if i >= len(ba) {
				*out = append(*out, newDifference(p, aa[i], nil, true, false))
			} else {
				diffJSON(p, aa[i], ba[i], out)
			}
		}
		return
	}
	if !equalJSON(a, b) {
		if path == "" {
			path = "/"
		}
		*out = append(*out, newDifference(path, a, b, true, true))
	}
}

func newDifference(path string, request, recorded any, requestExists, recordedExists bool) difference {
	return difference{Path: path, Request: request, Recorded: recorded, RequestExists: requestExists, RecordedExists: recordedExists, RequestDisplay: displayJSON(request, requestExists), RecordedDisplay: displayJSON(recorded, recordedExists)}
}

func displayJSON(v any, exists bool) string {
	if !exists {
		return "(missing)"
	}
	b, _ := json.Marshal(v)
	return string(b)
}

func sortStrings(v []string) {
	for i := 1; i < len(v); i++ {
		for j := i; j > 0 && v[j] < v[j-1]; j-- {
			v[j], v[j-1] = v[j-1], v[j]
		}
	}
}
func escapePointer(v string) string {
	return strings.ReplaceAll(strings.ReplaceAll(v, "~", "~0"), "/", "~1")
}
func equalJSON(a, b any) bool {
	x, _ := json.Marshal(a)
	y, _ := json.Marshal(b)
	return string(x) == string(y)
}

func decodeJSONValue(raw []byte, dst any) error {
	d := json.NewDecoder(strings.NewReader(string(raw)))
	d.UseNumber()
	return d.Decode(dst)
}

var responseHeaderAllowlist = map[string]bool{
	"content-type": true, "cache-control": true, "request-id": true,
	"x-request-id": true, "openai-request-id": true, "anthropic-request-id": true,
}

func validateHeaders(headers map[string]string) error {
	for name := range headers {
		if !responseHeaderAllowlist[strings.ToLower(name)] {
			return fmt.Errorf("response header %q is not allowed", name)
		}
	}
	return nil
}

func decodeJSON(w http.ResponseWriter, r *http.Request, v any) bool {
	return decodeJSONLimit(w, r, v, 16<<20)
}
func decodeJSONLimit(w http.ResponseWriter, r *http.Request, v any, limit int64) bool {
	d := json.NewDecoder(http.MaxBytesReader(w, r.Body, limit))
	d.DisallowUnknownFields()
	if err := d.Decode(v); err != nil {
		if tooLarge(err) {
			writeError(w, http.StatusRequestEntityTooLarge, "request_too_large", fmt.Sprintf("request body exceeds %d bytes", limit))
			return false
		}
		writeError(w, 400, "invalid_json", err.Error())
		return false
	}
	if err := d.Decode(&struct{}{}); err != io.EOF {
		if tooLarge(err) {
			writeError(w, http.StatusRequestEntityTooLarge, "request_too_large", fmt.Sprintf("request body exceeds %d bytes", limit))
			return false
		}
		writeError(w, 400, "invalid_json", "request body must contain one JSON value")
		return false
	}
	return true
}
func tooLarge(err error) bool {
	var maxErr *http.MaxBytesError
	return errors.As(err, &maxErr)
}

// parseID accepts only a canonical positive decimal id: ASCII digits with no
// sign, whitespace or other prefix that strconv.ParseInt would tolerate.
func parseID(raw string) (int64, bool) {
	if raw == "" {
		return 0, false
	}
	for i := 0; i < len(raw); i++ {
		if raw[i] < '0' || raw[i] > '9' {
			return 0, false
		}
	}
	id, err := strconv.ParseInt(raw, 10, 64)
	return id, err == nil && id > 0
}
func queryID(w http.ResponseWriter, r *http.Request, name string) (int64, bool) {
	id, ok := parseID(r.URL.Query().Get(name))
	if !ok {
		writeError(w, 400, "invalid_request", name+" must be a positive integer")
		return 0, false
	}
	return id, true
}

// isRead reports a GET or HEAD request; every GET endpoint also serves HEAD.
func isRead(r *http.Request) bool { return r.Method == http.MethodGet || r.Method == http.MethodHead }
func mutating(m string) bool {
	return m != http.MethodGet && m != http.MethodHead && m != http.MethodOptions
}
func sameOrigin(r *http.Request) bool {
	raw := r.Header.Get("Origin")
	if raw == "" {
		// Non-browser clients do not send Fetch Metadata. A browser cross-site
		// request without Origin must not gain an exception through that path.
		return !strings.EqualFold(r.Header.Get("Sec-Fetch-Site"), "cross-site")
	}
	u, err := url.Parse(raw)
	return err == nil && strings.EqualFold(u.Host, r.Host) && (u.Scheme == "http" || u.Scheme == "https") && loopbackHostname(u.Hostname())
}
func loopbackHostname(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
func methodNotAllowed(w http.ResponseWriter, allow string) {
	w.Header().Set("Allow", allow)
	writeError(w, 405, "method_not_allowed", "method not allowed")
}
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
func writeError(w http.ResponseWriter, status int, code, msg string) {
	writeJSON(w, status, map[string]any{"error": map[string]string{"code": code, "message": msg}})
}
func writeRevisionConflict(w http.ResponseWriter) {
	writeError(w, 409, "revision_conflict", "recording changed; reload it before retrying")
}

// writeStoreError maps store sentinels to client errors. Anything else is an
// internal failure whose text (SQL, file paths) is logged but never returned.
func writeStoreError(w http.ResponseWriter, err error) {
	if errors.Is(err, store.ErrActiveCollection) {
		writeError(w, 409, "collection_active", "switch to another collection before deleting this one")
		return
	}
	if errors.Is(err, store.ErrConflict) {
		writeError(w, 409, "conflict", "resource changed or already exists")
		return
	}
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, 404, "not_found", "resource not found")
		return
	}
	log.Printf("admin: internal error: %v", err)
	writeError(w, 500, "internal_error", "internal server error")
}
