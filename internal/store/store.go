package store

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync/atomic"
	"time"

	matching "github.com/local/llm-replay-proxy/internal/match"
	"github.com/local/llm-replay-proxy/internal/model"
	"github.com/local/llm-replay-proxy/internal/protocol"
	_ "modernc.org/sqlite"
)

var ErrNotFound = errors.New("store: not found")
var ErrConflict = errors.New("store: active revision changed")

// ErrInvalidSnapshot wraps every Import failure caused by the snapshot itself:
// an unreadable or non-SQLite file, failed integrity or foreign key checks, an
// unexpected schema, or rows that violate store invariants.
var ErrInvalidSnapshot = errors.New("invalid snapshot")

type Store struct {
	db   *sql.DB
	path string
}

var memorySequence atomic.Uint64

func sqliteFileURL(path string) string { return (&url.URL{Scheme: "file", Path: path}).String() }

// schemaVersion is stored in PRAGMA user_version. A database or snapshot in
// any other format is refused rather than migrated.
const schemaVersion = 4

const schema = `
PRAGMA foreign_keys=ON;
CREATE TABLE IF NOT EXISTS collections (
 id INTEGER PRIMARY KEY, name TEXT NOT NULL UNIQUE, exclusions TEXT NOT NULL, created_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS settings (
 singleton INTEGER PRIMARY KEY CHECK(singleton=1), mode TEXT NOT NULL,
 active_collection_id INTEGER NOT NULL REFERENCES collections(id),
 first_event_delay_ms INTEGER NOT NULL, delay_multiplier REAL NOT NULL, history_limit INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS chunks (
 id INTEGER PRIMARY KEY, hash BLOB NOT NULL UNIQUE, codec INTEGER NOT NULL, size INTEGER NOT NULL, data BLOB NOT NULL
);
CREATE TABLE IF NOT EXISTS bodies (
 id INTEGER PRIMARY KEY, hash BLOB NOT NULL UNIQUE, size INTEGER NOT NULL, chunks BLOB NOT NULL,
 summary TEXT, summary_v INTEGER NOT NULL DEFAULT 0, thread TEXT
);
CREATE TABLE IF NOT EXISTS recordings (
 id INTEGER PRIMARY KEY, collection_id INTEGER NOT NULL REFERENCES collections(id) ON DELETE CASCADE,
 key TEXT NOT NULL, route TEXT NOT NULL, upstream_identity TEXT NOT NULL, streaming INTEGER NOT NULL,
 active_revision_id INTEGER, created_at TEXT NOT NULL, UNIQUE(collection_id,key)
);
CREATE TABLE IF NOT EXISTS revisions (
 id INTEGER PRIMARY KEY, recording_id INTEGER NOT NULL REFERENCES recordings(id) ON DELETE CASCADE,
 status INTEGER NOT NULL, headers TEXT NOT NULL, body TEXT NOT NULL, events BLOB NOT NULL,
 request_body INTEGER NOT NULL REFERENCES bodies(id), source TEXT NOT NULL, created_at TEXT NOT NULL,
 response_summary TEXT, response_summary_v INTEGER NOT NULL DEFAULT 0
);
CREATE TABLE IF NOT EXISTS history (
 id INTEGER PRIMARY KEY, collection_id INTEGER NOT NULL REFERENCES collections(id) ON DELETE CASCADE,
 route TEXT NOT NULL, key TEXT NOT NULL, request_body INTEGER REFERENCES bodies(id), outcome TEXT NOT NULL,
 detail TEXT NOT NULL, recording_id INTEGER NOT NULL DEFAULT 0, created_at TEXT NOT NULL,
 source TEXT NOT NULL DEFAULT '', duration_ms INTEGER, first_event_ms INTEGER,
 lookup_outcome TEXT NOT NULL DEFAULT '', revision_id INTEGER
);
CREATE INDEX IF NOT EXISTS history_collection_created ON history(collection_id,id DESC);
CREATE INDEX IF NOT EXISTS revisions_recording ON revisions(recording_id,id DESC);
CREATE INDEX IF NOT EXISTS history_collection_time ON history(collection_id,created_at);
CREATE INDEX IF NOT EXISTS history_recording ON history(recording_id) WHERE recording_id<>0;
CREATE INDEX IF NOT EXISTS history_body ON history(request_body) WHERE request_body IS NOT NULL;
CREATE INDEX IF NOT EXISTS bodies_thread ON bodies(thread) WHERE thread IS NOT NULL;
`

func Open(path string) (*Store, error) {
	if path == "" {
		return nil, errors.New("store: empty database path")
	}
	if path != ":memory:" {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return nil, err
		}
	}
	absPath, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	dsn := sqliteFileURL(absPath) + "?_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)"
	if path == ":memory:" {
		dsn = fmt.Sprintf("file:replay-%d?mode=memory&cache=shared&_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)", memorySequence.Add(1))
	}
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	s := &Store{db: db, path: path}
	if err = s.initSchema(context.Background()); err != nil {
		db.Close()
		return nil, err
	}
	if err = s.ensureDefaults(context.Background()); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

func (s *Store) initSchema(ctx context.Context) error {
	var version, tables int
	if err := s.db.QueryRowContext(ctx, "PRAGMA user_version").Scan(&version); err != nil {
		return fmt.Errorf("read database version: %w", err)
	}
	if err := s.db.QueryRowContext(ctx, "SELECT count(*) FROM sqlite_master WHERE type='table'").Scan(&tables); err != nil {
		return fmt.Errorf("read database version: %w", err)
	}
	if tables > 0 && version != schemaVersion {
		return fmt.Errorf("database %s uses storage format %d; this build needs format %d and does not migrate. Delete the file to start fresh", s.path, version, schemaVersion)
	}
	if _, err := s.db.ExecContext(ctx, schema); err != nil {
		return fmt.Errorf("initialize database: %w", err)
	}
	if _, err := s.db.ExecContext(ctx, fmt.Sprintf("PRAGMA user_version=%d", schemaVersion)); err != nil {
		return fmt.Errorf("initialize database: %w", err)
	}
	return nil
}

func (s *Store) Close() error { return s.db.Close() }

func now() string { return time.Now().UTC().Format(time.RFC3339Nano) }

func (s *Store) ensureDefaults(ctx context.Context) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var id int64
	err = tx.QueryRowContext(ctx, "SELECT id FROM collections ORDER BY id LIMIT 1").Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		r, e := tx.ExecContext(ctx, "INSERT INTO collections(name,exclusions,created_at) VALUES(?,?,?)", "Default", "[]", now())
		if e != nil {
			return e
		}
		id, e = r.LastInsertId()
		if e != nil {
			return e
		}
	} else if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT OR IGNORE INTO settings(singleton,mode,active_collection_id,first_event_delay_ms,delay_multiplier,history_limit) VALUES(1,'replay',?,0,1,?)`, id, DefaultHistoryLimit)
	if err != nil {
		return err
	}
	return tx.Commit()
}

func encode(v any) (string, error) { b, err := json.Marshal(v); return string(b), err }

func (s *Store) Collections(ctx context.Context) ([]model.Collection, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT id,name,exclusions,created_at FROM collections ORDER BY id")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []model.Collection{}
	for rows.Next() {
		var c model.Collection
		var ex string
		if err := rows.Scan(&c.ID, &c.Name, &ex, &c.CreatedAt); err != nil {
			return nil, err
		}
		if err = json.Unmarshal([]byte(ex), &c.Exclusions); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func (s *Store) CreateCollection(ctx context.Context, name string, exclusions []string) (model.Collection, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return model.Collection{}, errors.New("collection name is required")
	}
	if exclusions == nil {
		exclusions = []string{}
	}
	if err := matching.ValidateExclusions(exclusions); err != nil {
		return model.Collection{}, err
	}
	ex, err := encode(exclusions)
	if err != nil {
		return model.Collection{}, err
	}
	c := model.Collection{Name: name, Exclusions: exclusions, CreatedAt: now()}
	r, err := s.db.ExecContext(ctx, "INSERT INTO collections(name,exclusions,created_at) VALUES(?,?,?)", name, ex, c.CreatedAt)
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE constraint failed: collections.name") {
			return model.Collection{}, ErrConflict
		}
		return model.Collection{}, err
	}
	c.ID, err = r.LastInsertId()
	return c, err
}

func scanCollection(row interface{ Scan(...any) error }) (model.Collection, error) {
	var c model.Collection
	var ex string
	err := row.Scan(&c.ID, &c.Name, &ex, &c.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return c, ErrNotFound
	}
	if err != nil {
		return c, err
	}
	err = json.Unmarshal([]byte(ex), &c.Exclusions)
	return c, err
}
func (s *Store) Collection(ctx context.Context, id int64) (model.Collection, error) {
	return scanCollection(s.db.QueryRowContext(ctx, "SELECT id,name,exclusions,created_at FROM collections WHERE id=?", id))
}

func (s *Store) Settings(ctx context.Context) (model.Settings, error) {
	var v model.Settings
	err := s.db.QueryRowContext(ctx, "SELECT mode,active_collection_id,first_event_delay_ms,delay_multiplier,history_limit FROM settings WHERE singleton=1").Scan(&v.Mode, &v.ActiveCollectionID, &v.FirstEventDelayMS, &v.DelayMultiplier, &v.HistoryLimit)
	if errors.Is(err, sql.ErrNoRows) {
		return v, ErrNotFound
	}
	return v, err
}
func (s *Store) SetSettings(ctx context.Context, v model.Settings) error {
	if v.Mode != "record" && v.Mode != "replay" && v.Mode != "auto" {
		return errors.New("mode must be record, replay, or auto")
	}
	if v.FirstEventDelayMS < 0 || v.FirstEventDelayMS > int64((24*time.Hour)/time.Millisecond) {
		return errors.New("first event delay must be between 0 and 24 hours")
	}
	if math.IsNaN(v.DelayMultiplier) || math.IsInf(v.DelayMultiplier, 0) || v.DelayMultiplier < 0 || v.DelayMultiplier > 1_000_000 {
		return errors.New("delay multiplier must be finite and between 0 and 1000000")
	}
	if v.HistoryLimit < 0 || v.HistoryLimit > MaxHistoryLimit {
		return fmt.Errorf("history limit must be between 0 and %d", MaxHistoryLimit)
	}
	r, err := s.db.ExecContext(ctx, "UPDATE settings SET mode=?,active_collection_id=?,first_event_delay_ms=?,delay_multiplier=?,history_limit=? WHERE singleton=1", v.Mode, v.ActiveCollectionID, v.FirstEventDelayMS, v.DelayMultiplier, v.HistoryLimit)
	if err != nil {
		return err
	}
	n, _ := r.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

const recordingCols = "id,collection_id,key,route,upstream_identity,streaming,active_revision_id,created_at"

func scanRecording(row interface{ Scan(...any) error }) (model.Recording, error) {
	var r model.Recording
	var stream int
	var active sql.NullInt64
	err := row.Scan(&r.ID, &r.CollectionID, &r.Key, &r.Route, &r.UpstreamIdentity, &stream, &active, &r.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return r, ErrNotFound
	}
	r.Streaming = stream != 0
	if active.Valid {
		r.ActiveRevisionID = active.Int64
	}
	return r, err
}

const revisionCols = "id,recording_id,status,headers,body,events,request_body,source,created_at"

// scanRevision reads a revision without its request; bodyID names the stored
// request for callers that need it.
func scanRevision(row interface{ Scan(...any) error }) (r model.Revision, bodyID sql.NullInt64, err error) {
	var h string
	var e []byte
	err = row.Scan(&r.ID, &r.RecordingID, &r.Status, &h, &r.Body, &e, &bodyID, &r.Source, &r.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return r, bodyID, ErrNotFound
	}
	if err != nil {
		return r, bodyID, err
	}
	if err = json.Unmarshal([]byte(h), &r.Headers); err != nil {
		return r, bodyID, err
	}
	r.Events, err = decodeEvents(e)
	return r, bodyID, err
}

// Lookup serves the replay hot path: it reads the response but never the
// request, which can be megabytes and is not needed to replay.
func (s *Store) Lookup(ctx context.Context, cid int64, key string) (model.Entry, error) {
	r, err := scanRecording(s.db.QueryRowContext(ctx, "SELECT "+recordingCols+" FROM recordings WHERE collection_id=? AND key=?", cid, key))
	if err != nil {
		return model.Entry{}, err
	}
	if r.ActiveRevisionID == 0 {
		return model.Entry{}, ErrNotFound
	}
	v, _, err := scanRevision(s.db.QueryRowContext(ctx, "SELECT "+revisionCols+" FROM revisions WHERE id=?", r.ActiveRevisionID))
	return model.Entry{Recording: r, Revision: v}, err
}

func (s *Store) Publish(ctx context.Context, rec model.Recording, rev model.Revision) (model.Entry, error) {
	return s.publish(ctx, rec, rev, nil)
}

// PublishIfActive prevents a stale editor from replacing a revision activated
// after it loaded the recording. expectedRevisionID is zero for a new key.
func (s *Store) PublishIfActive(ctx context.Context, rec model.Recording, rev model.Revision, expectedRevisionID int64) (model.Entry, error) {
	return s.publish(ctx, rec, rev, &expectedRevisionID)
}

// publish stores rev as the active revision of rec's key. The recording's
// request is always its active revision's request; MatchingInput is derived
// from it and, when a caller supplies one, must agree.
func (s *Store) publish(ctx context.Context, rec model.Recording, rev model.Revision, expectedRevisionID *int64) (model.Entry, error) {
	if rec.CollectionID == 0 || rec.Key == "" || rec.Route == "" {
		return model.Entry{}, errors.New("recording collection, key, and route are required")
	}
	if !json.Valid(rec.Request) {
		return model.Entry{}, errors.New("request must be valid JSON")
	}
	collection, err := s.Collection(ctx, rec.CollectionID)
	if err != nil {
		return model.Entry{}, err
	}
	key, input, err := matching.Key(rec.Route, rec.UpstreamIdentity, rec.Request, collection.Exclusions)
	if err != nil {
		return model.Entry{}, err
	}
	if key != rec.Key || (len(rec.MatchingInput) > 0 && string(input) != string(rec.MatchingInput)) {
		return model.Entry{}, errors.New("recording key or matching input does not match request")
	}
	if requestStreaming(rec.Request) != rec.Streaming {
		return model.Entry{}, errors.New("recording streaming flag disagrees with request")
	}
	if rev.Headers == nil {
		rev.Headers = map[string]string{}
	}
	if rev.Events == nil {
		rev.Events = []model.Event{}
	}
	if len(rev.Request) == 0 {
		rev.Request = append(json.RawMessage(nil), rec.Request...)
	}
	// Canonicalizing a multi-megabyte request is the costliest validation step;
	// skip the second pass when the revision carries the same bytes.
	vkey, vinput := key, input
	if !bytes.Equal(rev.Request, rec.Request) {
		if vkey, vinput, err = matching.Key(rec.Route, rec.UpstreamIdentity, rev.Request, collection.Exclusions); err != nil {
			return model.Entry{}, errors.New("revision provenance does not match recording key")
		}
	}
	if vkey != rec.Key || (len(rev.MatchingInput) > 0 && string(vinput) != string(rev.MatchingInput)) {
		return model.Entry{}, errors.New("revision provenance does not match recording key")
	}
	if requestStreaming(rev.Request) != rec.Streaming {
		return model.Entry{}, errors.New("revision streaming flag disagrees with request")
	}
	if err = validateImportedHeaders(rev.Headers); err != nil {
		return model.Entry{}, err
	}
	if err = protocol.Validate(rec.Route, rec.Streaming, rev); err != nil {
		return model.Entry{}, fmt.Errorf("invalid revision: %w", err)
	}
	hj, err := encode(rev.Headers)
	if err != nil {
		return model.Entry{}, err
	}
	ej, err := encodeEvents(rev.Events)
	if err != nil {
		return model.Entry{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return model.Entry{}, err
	}
	defer tx.Rollback()
	existing, err := scanRecording(tx.QueryRowContext(ctx, "SELECT "+recordingCols+" FROM recordings WHERE collection_id=? AND key=?", rec.CollectionID, rec.Key))
	if errors.Is(err, ErrNotFound) {
		if expectedRevisionID != nil && *expectedRevisionID != 0 {
			return model.Entry{}, ErrConflict
		}
		rec.CreatedAt = now()
		res, e := tx.ExecContext(ctx, `INSERT INTO recordings(collection_id,key,route,upstream_identity,streaming,created_at) VALUES(?,?,?,?,?,?)`, rec.CollectionID, rec.Key, rec.Route, rec.UpstreamIdentity, rec.Streaming, rec.CreatedAt)
		if e != nil {
			return model.Entry{}, e
		}
		if rec.ID, e = res.LastInsertId(); e != nil {
			return model.Entry{}, e
		}
	} else if err != nil {
		return model.Entry{}, err
	} else {
		if expectedRevisionID != nil && existing.ActiveRevisionID != *expectedRevisionID {
			return model.Entry{}, ErrConflict
		}
		rec = existing
	}
	bodyID, err := internBody(ctx, tx, rev.Request)
	if err != nil {
		return model.Entry{}, err
	}
	rev.RecordingID = rec.ID
	rev.CreatedAt = now()
	res, err := tx.ExecContext(ctx, `INSERT INTO revisions(recording_id,status,headers,body,events,request_body,source,created_at) VALUES(?,?,?,?,?,?,?,?)`, rev.RecordingID, rev.Status, hj, rev.Body, ej, bodyID, rev.Source, rev.CreatedAt)
	if err != nil {
		return model.Entry{}, err
	}
	if rev.ID, err = res.LastInsertId(); err != nil {
		return model.Entry{}, err
	}
	if _, err = tx.ExecContext(ctx, "UPDATE recordings SET active_revision_id=? WHERE id=?", rev.ID, rec.ID); err != nil {
		return model.Entry{}, err
	}
	if err = tx.Commit(); err != nil {
		return model.Entry{}, err
	}
	rev.MatchingInput = vinput
	rec.ActiveRevisionID = rev.ID
	rec.Request, rec.MatchingInput = append(json.RawMessage(nil), rev.Request...), vinput
	return model.Entry{Recording: rec, Revision: rev}, nil
}

// derived recomputes matching input for display; it is never stored.
type derived struct {
	route, identity string
	exclusions      []string
	inputs          map[string]json.RawMessage
}

func (s *Store) derivation(ctx context.Context, r model.Recording) (*derived, error) {
	c, err := s.Collection(ctx, r.CollectionID)
	if err != nil {
		return nil, err
	}
	return &derived{route: r.Route, identity: r.UpstreamIdentity, exclusions: c.Exclusions, inputs: map[string]json.RawMessage{}}, nil
}

func (d *derived) input(request json.RawMessage) (json.RawMessage, error) {
	if in, ok := d.inputs[string(request)]; ok {
		return in, nil
	}
	_, in, err := matching.Key(d.route, d.identity, request, d.exclusions)
	if err != nil {
		return nil, err
	}
	d.inputs[string(request)] = in
	return in, nil
}

func (s *Store) Get(ctx context.Context, id int64) (model.Entry, error) {
	r, err := scanRecording(s.db.QueryRowContext(ctx, "SELECT "+recordingCols+" FROM recordings WHERE id=?", id))
	if err != nil {
		return model.Entry{}, err
	}
	if r.ActiveRevisionID == 0 {
		return model.Entry{}, ErrNotFound
	}
	v, bodyID, err := scanRevision(s.db.QueryRowContext(ctx, "SELECT "+revisionCols+" FROM revisions WHERE id=?", r.ActiveRevisionID))
	if err != nil {
		return model.Entry{}, err
	}
	if v.Request, err = loadBody(ctx, s.db, bodyID.Int64); err != nil {
		return model.Entry{}, err
	}
	d, err := s.derivation(ctx, r)
	if err != nil {
		return model.Entry{}, err
	}
	if v.MatchingInput, err = d.input(v.Request); err != nil {
		return model.Entry{}, err
	}
	r.Request, r.MatchingInput = v.Request, v.MatchingInput
	return model.Entry{Recording: r, Revision: v}, nil
}

func (s *Store) Revisions(ctx context.Context, rid int64) ([]model.Revision, error) {
	r, err := scanRecording(s.db.QueryRowContext(ctx, "SELECT "+recordingCols+" FROM recordings WHERE id=?", rid))
	if errors.Is(err, ErrNotFound) {
		return []model.Revision{}, nil
	}
	if err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, "SELECT "+revisionCols+" FROM revisions WHERE recording_id=? ORDER BY id DESC", rid)
	if err != nil {
		return nil, err
	}
	out := []model.Revision{}
	var bodies []sql.NullInt64
	for rows.Next() {
		v, body, e := scanRevision(rows)
		if e != nil {
			rows.Close()
			return nil, e
		}
		out = append(out, v)
		bodies = append(bodies, body)
	}
	if err = rows.Close(); err != nil {
		return nil, err
	}
	d, err := s.derivation(ctx, r)
	if err != nil {
		return nil, err
	}
	cache := bodyCache{}
	for i := range out {
		if out[i].Request, err = cache.load(ctx, s.db, bodies[i]); err != nil {
			return nil, err
		}
		if out[i].MatchingInput, err = d.input(out[i].Request); err != nil {
			return nil, err
		}
	}
	return out, nil
}
func (s *Store) Restore(ctx context.Context, rid, vid int64) (model.Entry, error) {
	return s.restore(ctx, rid, vid, nil)
}

func (s *Store) RestoreIfActive(ctx context.Context, rid, vid, expectedActiveID int64) (model.Entry, error) {
	return s.restore(ctx, rid, vid, &expectedActiveID)
}
func (s *Store) restore(ctx context.Context, rid, vid int64, expectedActiveID *int64) (model.Entry, error) {
	query := `UPDATE recordings SET active_revision_id=? WHERE id=? AND EXISTS(SELECT 1 FROM revisions WHERE id=? AND recording_id=?)`
	args := []any{vid, rid, vid, rid}
	if expectedActiveID != nil {
		query += " AND active_revision_id=?"
		args = append(args, *expectedActiveID)
	}
	res, err := s.db.ExecContext(ctx, query, args...)
	if err != nil {
		return model.Entry{}, err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		if expectedActiveID != nil {
			var active int64
			if e := s.db.QueryRowContext(ctx, "SELECT active_revision_id FROM recordings WHERE id=?", rid).Scan(&active); e == nil && active != *expectedActiveID {
				return model.Entry{}, ErrConflict
			}
		}
		return model.Entry{}, ErrNotFound
	}
	return s.Get(ctx, rid)
}

// AddHistory logs one proxied call. A request seen before (every replay hit of
// a recorded request) adds only the row; its body is already stored.
func (s *Store) AddHistory(ctx context.Context, h model.History) error {
	if h.CreatedAt == "" {
		h.CreatedAt = now()
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var body sql.NullInt64
	if len(h.Request) > 0 && string(h.Request) != "null" {
		if body.Int64, err = internBody(ctx, tx, h.Request); err != nil {
			return err
		}
		body.Valid = true
	}
	revision := sql.NullInt64{Int64: h.RevisionID, Valid: h.RevisionID != 0}
	if _, err = tx.ExecContext(ctx, `INSERT INTO history(collection_id,route,key,request_body,outcome,detail,recording_id,created_at,source,duration_ms,first_event_ms,lookup_outcome,revision_id) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?)`, h.CollectionID, h.Route, h.Key, body, h.Outcome, h.Detail, h.RecordingID, h.CreatedAt, h.Source, h.DurationMS, h.FirstEventMS, h.CacheStatus, revision); err != nil {
		return err
	}
	return tx.Commit()
}

// Analytics aggregates the complete matching history range. Legacy rows keep
// NULL timings and therefore never become artificial zero-duration samples.
func (s *Store) Analytics(ctx context.Context, cid int64, from, to time.Time) (model.Analytics, error) {
	if !from.Before(to) {
		return model.Analytics{}, errors.New("analytics start must be before end")
	}
	a := model.Analytics{Series: []model.AnalyticsPoint{}, Sources: map[string]model.SourceAnalytics{}}
	if err := s.db.QueryRowContext(ctx, "SELECT count(*) FROM history WHERE collection_id=?", cid).Scan(&a.LifetimeTotal); err != nil {
		return a, err
	}
	// created_at is RFC3339 text, possibly with a non-UTC offset from imports,
	// so it isn't strictly ordered as a string. Narrow the scan by date prefix
	// with a margin wider than any UTC offset; the exact range check is below.
	lower := from.UTC().AddDate(0, 0, -1).Format(time.DateOnly)
	query := `SELECT outcome,source,duration_ms,first_event_ms,created_at,lookup_outcome FROM history WHERE collection_id=? AND created_at>=?`
	args := []any{cid, lower}
	// A year past 9999 formats with five digits ("10000-01-01"), which sorts
	// before every four-digit year; no upper text bound is needed there.
	if upper := to.UTC().AddDate(0, 0, 2); upper.Year() <= 9999 {
		query += ` AND created_at<?`
		args = append(args, upper.Format(time.DateOnly))
	}
	rows, err := s.db.QueryContext(ctx, query+` ORDER BY id`, args...)
	if err != nil {
		return model.Analytics{}, err
	}
	defer rows.Close()
	type sampleSet struct {
		total             int64
		durations, firsts []int64
	}
	samples := map[string]*sampleSet{}
	step := time.Hour
	if to.Sub(from) > 48*time.Hour {
		step = 24 * time.Hour
	}
	bucketStart := from.UTC().Truncate(step)
	buckets := map[time.Time]*model.AnalyticsPoint{}
	for t := bucketStart; t.Before(to); t = t.Add(step) {
		p := &model.AnalyticsPoint{Start: t.Format(time.RFC3339)}
		buckets[t] = p
	}
	for rows.Next() {
		var outcome, source, created, cacheStatus string
		var duration, first sql.NullInt64
		if err := rows.Scan(&outcome, &source, &duration, &first, &created, &cacheStatus); err != nil {
			return a, err
		}
		stamp, parseErr := time.Parse(time.RFC3339Nano, created)
		if parseErr != nil || stamp.Before(from) || !stamp.Before(to) {
			continue
		}
		a.Total++
		if source == "" {
			source = "legacy"
		}
		set := samples[source]
		if set == nil {
			set = &sampleSet{}
			samples[source] = set
		}
		set.total++
		if cacheStatus == "" && (outcome == "hit" || outcome == "miss") {
			cacheStatus = outcome
		}
		switch cacheStatus {
		case "hit":
			a.Hits++
		case "miss":
			a.Misses++
		}
		switch outcome {
		case "recorded":
			a.Recorded++
		case "hit", "miss":
		default:
			a.Errors++
		}
		if duration.Valid {
			set.durations = append(set.durations, duration.Int64)
		}
		if first.Valid {
			set.firsts = append(set.firsts, first.Int64)
		}
		if p := buckets[stamp.UTC().Truncate(step)]; p != nil {
			p.Total++
			switch cacheStatus {
			case "hit":
				p.Hits++
			case "miss":
				p.Misses++
			}
			if outcome != "recorded" && outcome != "hit" && outcome != "miss" {
				p.Errors++
			}
		}
	}
	if err := rows.Err(); err != nil {
		return a, err
	}
	den := a.Hits + a.Misses
	if den > 0 {
		rate := float64(a.Hits) / float64(den)
		a.HitRate = &rate
	}
	for source, set := range samples {
		a.Sources[source] = model.SourceAnalytics{Total: set.total, Duration: percentiles(set.durations), FirstEvent: percentiles(set.firsts)}
	}
	for t := bucketStart; t.Before(to); t = t.Add(step) {
		a.Series = append(a.Series, *buckets[t])
	}
	return a, nil
}

func percentiles(values []int64) model.Percentiles {
	p := model.Percentiles{Samples: len(values)}
	if len(values) == 0 {
		return p
	}
	sort.Slice(values, func(i, j int) bool { return values[i] < values[j] })
	pick := func(q float64) *int64 {
		i := int(math.Ceil(q*float64(len(values)))) - 1
		if i < 0 {
			i = 0
		}
		v := values[i]
		return &v
	}
	p.P50, p.P95, p.P99 = pick(.5), pick(.95), pick(.99)
	return p
}

func stateField(v any, want string) bool {
	switch x := v.(type) {
	case string:
		return x == want
	case map[string]any:
		id, _ := x["id"].(string)
		return id == want
	}
	return false
}
func rawContainsState(raw string, want string) bool {
	var o map[string]any
	if json.Unmarshal([]byte(raw), &o) != nil {
		return false
	}
	if stateField(o["id"], want) || stateField(o["conversation"], want) || stateField(o["container"], want) {
		return true
	}
	if message, ok := o["message"].(map[string]any); ok && stateField(message["container"], want) {
		return true
	}
	if item, ok := o["item"].(map[string]any); ok {
		typ, _ := o["type"].(string)
		if (typ == "response.output_item.added" || typ == "response.output_item.done") && stateField(item["id"], want) {
			return true
		}
	}
	outputHasID := func(output any) bool {
		items, ok := output.([]any)
		if !ok {
			return false
		}
		for _, rawItem := range items {
			if item, ok := rawItem.(map[string]any); ok && stateField(item["id"], want) {
				return true
			}
		}
		return false
	}
	if outputHasID(o["output"]) {
		return true
	}
	if response, ok := o["response"].(map[string]any); ok {
		return stateField(response["id"], want) || stateField(response["conversation"], want) || stateField(response["container"], want) || outputHasID(response["output"])
	}
	return false
}
func eventJSON(frame string) string {
	var data []string
	for _, line := range strings.Split(strings.ReplaceAll(frame, "\r\n", "\n"), "\n") {
		if strings.HasPrefix(line, "data:") {
			data = append(data, strings.TrimSpace(strings.TrimPrefix(line, "data:")))
		}
	}
	return strings.Join(data, "\n")
}

var safeResponseHeaders = map[string]bool{"content-type": true, "cache-control": true, "request-id": true, "x-request-id": true, "openai-request-id": true, "anthropic-request-id": true}

func validateImportedHeaders(headers map[string]string) error {
	for k, v := range headers {
		if !safeResponseHeaders[strings.ToLower(http.CanonicalHeaderKey(k))] {
			return fmt.Errorf("snapshot revision contains disallowed response header %q", k)
		}
		if strings.ContainsAny(v, "\r\n\x00") {
			return fmt.Errorf("snapshot revision header %q contains invalid control characters", k)
		}
	}
	return nil
}
func requestStreaming(raw json.RawMessage) bool {
	var x struct {
		Stream bool `json:"stream"`
	}
	return json.Unmarshal(raw, &x) == nil && x.Stream
}
func (s *Store) HasProviderState(ctx context.Context, cid int64, stateID string) (bool, error) {
	if stateID == "" {
		return false, nil
	}
	rows, err := s.db.QueryContext(ctx, `SELECT v.body,v.events FROM recordings r JOIN revisions v ON v.recording_id=r.id WHERE r.collection_id=?`, cid)
	if err != nil {
		return false, err
	}
	defer rows.Close()
	for rows.Next() {
		var body string
		var events []byte
		if err := rows.Scan(&body, &events); err != nil {
			return false, err
		}
		if rawContainsState(body, stateID) {
			return true, nil
		}
		if es, err := decodeEvents(events); err == nil {
			for _, e := range es {
				if rawContainsState(eventJSON(e.Data), stateID) {
					return true, nil
				}
			}
		}
	}
	return false, rows.Err()
}

// Export creates a transactionally consistent, collection-scoped SQLite snapshot
// containing the collection, its recordings and revisions, but no request
// history. The destination must not already exist, preventing accidental
// replacement.
func (s *Store) Export(ctx context.Context, collectionID int64, destPath string) error {
	if destPath == "" {
		return errors.New("export destination is required")
	}
	if _, err := s.Collection(ctx, collectionID); err != nil {
		return err
	}
	if _, err := os.Stat(destPath); err == nil {
		return errors.New("export destination already exists")
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(destPath), 0o755); err != nil {
		return err
	}
	// VACUUM INTO takes a consistent snapshot even while WAL mode is active.
	quoted := strings.ReplaceAll(destPath, "'", "''")
	if _, err := s.db.ExecContext(ctx, "VACUUM INTO '"+quoted+"'"); err != nil {
		return fmt.Errorf("create snapshot: %w", err)
	}
	absDest, err := filepath.Abs(destPath)
	if err != nil {
		return err
	}
	out, err := sql.Open("sqlite", sqliteFileURL(absDest)+"?_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)")
	if err != nil {
		return err
	}
	defer out.Close()
	if _, err = out.ExecContext(ctx, "PRAGMA foreign_keys=ON"); err != nil {
		return err
	}
	tx, err := out.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, "UPDATE settings SET active_collection_id=? WHERE singleton=1", collectionID); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, "DELETE FROM collections WHERE id<>?", collectionID); err != nil {
		return err
	}
	// History holds every logged request body, including misses and errors.
	// Import never reads it, so it must not leave this machine in a snapshot.
	if _, err = tx.ExecContext(ctx, "DELETE FROM history"); err != nil {
		return err
	}
	// Bodies are shared, so deleting rows leaves their bytes behind. Sweep
	// them, or history-only requests and other collections would ship.
	if err = sweepBodies(ctx, tx); err != nil {
		return err
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	// Rebuild the file so pages freed by the deletes do not retain their bytes.
	if _, err = out.ExecContext(ctx, "VACUUM"); err != nil {
		return fmt.Errorf("compact snapshot: %w", err)
	}
	return nil
}

func uniqueImportedName(ctx context.Context, tx *sql.Tx, name string) (string, error) {
	candidate := name
	for i := 1; ; i++ {
		var n int
		err := tx.QueryRowContext(ctx, "SELECT count(*) FROM collections WHERE name=?", candidate).Scan(&n)
		if err != nil {
			return "", err
		}
		if n == 0 {
			return candidate, nil
		}
		candidate = fmt.Sprintf("%s (imported %d)", name, i)
	}
}

type importedRecording struct {
	r         model.Recording
	revisions []model.Revision
}

type importedCollection struct {
	c          model.Collection
	oldID      int64
	recordings []importedRecording
}

// validTimestamp reports whether v uses the RFC 3339 form the store writes.
func validTimestamp(v string) bool {
	_, err := time.Parse(time.RFC3339Nano, v)
	return err == nil
}

// Import copies complete collections and their immutable revision history from a
// validated replay-proxy SQLite snapshot. IDs are remapped and the local active
// collection and timing settings are intentionally retained. Request history is
// never imported.
//
// Every failure caused by the snapshot's contents wraps ErrInvalidSnapshot.
// Collection names are trimmed and must not be blank; null exclusions and
// revision headers are normalised to empty values exactly as publication does;
// created_at values that are not RFC 3339 timestamps reject the snapshot rather
// than being silently replaced.
func (s *Store) Import(ctx context.Context, sourcePath string) ([]model.Collection, error) {
	if sourcePath == "" {
		return nil, errors.New("import source is required")
	}
	abs, err := filepath.Abs(sourcePath)
	if err != nil {
		return nil, err
	}
	// Import files are completed uploads/snapshots. immutable=1 gives every
	// validation query the same file image and prevents journal side effects.
	src, err := sql.Open("sqlite", sqliteFileURL(abs)+"?mode=ro&immutable=1")
	if err != nil {
		return nil, err
	}
	defer src.Close()
	src.SetMaxOpenConns(4)
	// Fully read and validate the immutable source before taking the destination
	// write lock. The transaction below then contains inserts only.
	imports, err := readSnapshot(ctx, src)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, ctxErr
		}
		if errors.Is(err, ErrInvalidSnapshot) {
			return nil, err
		}
		return nil, fmt.Errorf("%w: %w", ErrInvalidSnapshot, err)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	out := make([]model.Collection, 0, len(imports))
	for _, item := range imports {
		name, e := uniqueImportedName(ctx, tx, item.c.Name)
		if e != nil {
			return nil, e
		}
		ex, e := encode(item.c.Exclusions)
		if e != nil {
			return nil, e
		}
		res, e := tx.ExecContext(ctx, "INSERT INTO collections(name,exclusions,created_at) VALUES(?,?,?)", name, ex, item.c.CreatedAt)
		if e != nil {
			return nil, e
		}
		newCID, e := res.LastInsertId()
		if e != nil {
			return nil, e
		}
		item.c.ID = newCID
		item.c.Name = name
		out = append(out, item.c)
		for _, loaded := range item.recordings {
			r := loaded.r
			res, e = tx.ExecContext(ctx, `INSERT INTO recordings(collection_id,key,route,upstream_identity,streaming,created_at) VALUES(?,?,?,?,?,?)`, newCID, r.Key, r.Route, r.UpstreamIdentity, r.Streaming, r.CreatedAt)
			if e != nil {
				return nil, e
			}
			newRID, e := res.LastInsertId()
			if e != nil {
				return nil, e
			}
			var newActive int64
			for _, v := range loaded.revisions {
				hj, e := encode(v.Headers)
				if e != nil {
					return nil, e
				}
				ej, e := encodeEvents(v.Events)
				if e != nil {
					return nil, e
				}
				bodyID, e := internBody(ctx, tx, v.Request)
				if e != nil {
					return nil, e
				}
				res, e = tx.ExecContext(ctx, `INSERT INTO revisions(recording_id,status,headers,body,events,request_body,source,created_at) VALUES(?,?,?,?,?,?,?,?)`, newRID, v.Status, hj, v.Body, ej, bodyID, v.Source, v.CreatedAt)
				if e != nil {
					return nil, e
				}
				newVID, e := res.LastInsertId()
				if e != nil {
					return nil, e
				}
				if v.ID == r.ActiveRevisionID {
					newActive = newVID
				}
			}
			if _, e = tx.ExecContext(ctx, "UPDATE recordings SET active_revision_id=? WHERE id=?", newActive, newRID); e != nil {
				return nil, e
			}
		}
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return out, nil
}

// readSnapshot loads and validates every row Import copies. Any error it returns
// describes the snapshot (including read failures of the uploaded file), so the
// caller classifies all of them as ErrInvalidSnapshot.
func readSnapshot(ctx context.Context, src *sql.DB) ([]importedCollection, error) {
	var version int
	if err := src.QueryRowContext(ctx, "PRAGMA user_version").Scan(&version); err != nil {
		return nil, err
	}
	if version != schemaVersion {
		return nil, fmt.Errorf("snapshot uses storage format %d; this build reads format %d", version, schemaVersion)
	}
	var integrity string
	if err := src.QueryRowContext(ctx, "PRAGMA integrity_check").Scan(&integrity); err != nil {
		return nil, err
	}
	if integrity != "ok" {
		return nil, fmt.Errorf("integrity check: %s", integrity)
	}
	if err := checkSnapshotForeignKeys(ctx, src); err != nil {
		return nil, err
	}
	imports, err := readSnapshotCollections(ctx, src)
	if err != nil {
		return nil, err
	}
	if len(imports) == 0 {
		return nil, errors.New("snapshot contains no collections")
	}
	bodies := bodyCache{}
	for i := range imports {
		if imports[i].recordings, err = readSnapshotRecordings(ctx, src, imports[i].oldID, imports[i].c.Exclusions, bodies); err != nil {
			return nil, err
		}
	}
	return imports, nil
}

func checkSnapshotForeignKeys(ctx context.Context, src *sql.DB) error {
	rows, err := src.QueryContext(ctx, "PRAGMA foreign_key_check")
	if err != nil {
		return fmt.Errorf("foreign keys: %w", err)
	}
	defer rows.Close()
	if rows.Next() {
		return errors.New("foreign key violation")
	}
	if err = rows.Err(); err != nil {
		return fmt.Errorf("foreign keys: %w", err)
	}
	return rows.Close()
}

func readSnapshotCollections(ctx context.Context, src *sql.DB) ([]importedCollection, error) {
	rows, err := src.QueryContext(ctx, "SELECT id,name,exclusions,created_at FROM collections ORDER BY id")
	if err != nil {
		return nil, fmt.Errorf("schema: %w", err)
	}
	defer rows.Close()
	var imports []importedCollection
	for rows.Next() {
		var x importedCollection
		var ex string
		if err = rows.Scan(&x.oldID, &x.c.Name, &ex, &x.c.CreatedAt); err != nil {
			return nil, err
		}
		x.c.Name = strings.TrimSpace(x.c.Name)
		if x.c.Name == "" {
			return nil, errors.New("collection name is blank")
		}
		if !validTimestamp(x.c.CreatedAt) {
			return nil, fmt.Errorf("collection %q has an invalid created_at", x.c.Name)
		}
		if err = json.Unmarshal([]byte(ex), &x.c.Exclusions); err != nil {
			return nil, fmt.Errorf("invalid exclusions: %w", err)
		}
		if x.c.Exclusions == nil {
			x.c.Exclusions = []string{}
		}
		if err = matching.ValidateExclusions(x.c.Exclusions); err != nil {
			return nil, fmt.Errorf("invalid exclusions: %w", err)
		}
		imports = append(imports, x)
	}
	if err = rows.Err(); err != nil {
		return nil, fmt.Errorf("read collections: %w", err)
	}
	return imports, rows.Close()
}

func readSnapshotRecordings(ctx context.Context, src *sql.DB, collectionID int64, exclusions []string, bodies bodyCache) ([]importedRecording, error) {
	rows, err := src.QueryContext(ctx, "SELECT "+recordingCols+" FROM recordings WHERE collection_id=? ORDER BY id", collectionID)
	if err != nil {
		return nil, fmt.Errorf("recordings: %w", err)
	}
	defer rows.Close()
	var out []importedRecording
	for rows.Next() {
		r, err := scanRecording(rows)
		if err != nil {
			return nil, err
		}
		if r.ActiveRevisionID == 0 {
			return nil, errors.New("recording has no active revision")
		}
		if !validTimestamp(r.CreatedAt) {
			return nil, errors.New("recording has an invalid created_at")
		}
		out = append(out, importedRecording{r: r})
	}
	if err = rows.Err(); err != nil {
		return nil, fmt.Errorf("read recordings: %w", err)
	}
	if err = rows.Close(); err != nil {
		return nil, err
	}
	// Revisions are read after the recordings cursor is closed, so a source read
	// error in either query surfaces instead of silently shortening the import.
	for i := range out {
		if out[i].revisions, err = readSnapshotRevisions(ctx, src, &out[i].r, exclusions, bodies); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// readSnapshotRevisions loads r's revisions, verifying each request against its
// stored hash and against r's key, and sets r.Request to the active request.
func readSnapshotRevisions(ctx context.Context, src *sql.DB, r *model.Recording, exclusions []string, bodies bodyCache) ([]model.Revision, error) {
	rows, err := src.QueryContext(ctx, "SELECT "+revisionCols+" FROM revisions WHERE recording_id=? ORDER BY id", r.ID)
	if err != nil {
		return nil, fmt.Errorf("revisions: %w", err)
	}
	defer rows.Close()
	var out []model.Revision
	var bodyIDs []sql.NullInt64
	for rows.Next() {
		v, body, err := scanRevision(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
		bodyIDs = append(bodyIDs, body)
	}
	if err = rows.Err(); err != nil {
		return nil, fmt.Errorf("read revisions: %w", err)
	}
	if err = rows.Close(); err != nil {
		return nil, err
	}
	keys := map[int64]string{}
	active := false
	for i := range out {
		v := &out[i]
		// Match publication: a JSON null is the empty value, never stored as null.
		if v.Headers == nil {
			v.Headers = map[string]string{}
		}
		if v.Events == nil {
			v.Events = []model.Event{}
		}
		if !validTimestamp(v.CreatedAt) {
			return nil, errors.New("revision has an invalid created_at")
		}
		if v.Request, err = bodies.load(ctx, src, bodyIDs[i]); err != nil {
			return nil, err
		}
		key, seen := keys[bodyIDs[i].Int64]
		if !seen {
			if key, _, err = matching.Key(r.Route, r.UpstreamIdentity, v.Request, exclusions); err != nil {
				return nil, fmt.Errorf("revision request: %w", err)
			}
			keys[bodyIDs[i].Int64] = key
		}
		if key != r.Key {
			return nil, errors.New("revision provenance does not match recording key")
		}
		if requestStreaming(v.Request) != r.Streaming {
			return nil, errors.New("revision streaming flag disagrees with request")
		}
		if err = validateImportedHeaders(v.Headers); err != nil {
			return nil, err
		}
		if err = protocol.Validate(r.Route, r.Streaming, *v); err != nil {
			return nil, fmt.Errorf("invalid revision: %w", err)
		}
		if v.ID == r.ActiveRevisionID {
			r.Request, active = v.Request, true
		}
	}
	if !active {
		return nil, errors.New("recording references missing active revision")
	}
	return out, nil
}
