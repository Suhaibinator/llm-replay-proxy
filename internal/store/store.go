package store

import (
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

type Store struct {
	db   *sql.DB
	path string
}

var memorySequence atomic.Uint64

func sqliteFileURL(path string) string { return (&url.URL{Scheme: "file", Path: path}).String() }

const schema = `
PRAGMA foreign_keys=ON;
CREATE TABLE IF NOT EXISTS collections (
 id INTEGER PRIMARY KEY, name TEXT NOT NULL UNIQUE, exclusions TEXT NOT NULL, created_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS settings (
 singleton INTEGER PRIMARY KEY CHECK(singleton=1), mode TEXT NOT NULL,
 active_collection_id INTEGER NOT NULL REFERENCES collections(id),
 first_event_delay_ms INTEGER NOT NULL, delay_multiplier REAL NOT NULL
);
CREATE TABLE IF NOT EXISTS recordings (
 id INTEGER PRIMARY KEY, collection_id INTEGER NOT NULL REFERENCES collections(id) ON DELETE CASCADE,
 key TEXT NOT NULL, route TEXT NOT NULL, request BLOB NOT NULL, matching_input BLOB NOT NULL,
 upstream_identity TEXT NOT NULL, streaming INTEGER NOT NULL, active_revision_id INTEGER,
 created_at TEXT NOT NULL, UNIQUE(collection_id,key)
);
CREATE TABLE IF NOT EXISTS revisions (
 id INTEGER PRIMARY KEY, recording_id INTEGER NOT NULL REFERENCES recordings(id) ON DELETE CASCADE,
 status INTEGER NOT NULL, headers TEXT NOT NULL, body TEXT NOT NULL, events TEXT NOT NULL,
 request BLOB NOT NULL, matching_input BLOB NOT NULL, source TEXT NOT NULL, created_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS history (
 id INTEGER PRIMARY KEY, collection_id INTEGER NOT NULL REFERENCES collections(id) ON DELETE CASCADE,
 route TEXT NOT NULL, key TEXT NOT NULL, request BLOB NOT NULL, outcome TEXT NOT NULL,
 detail TEXT NOT NULL, recording_id INTEGER NOT NULL DEFAULT 0, created_at TEXT NOT NULL,
 source TEXT NOT NULL DEFAULT '', duration_ms INTEGER, first_event_ms INTEGER,
 lookup_outcome TEXT NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS history_collection_created ON history(collection_id,id DESC);
CREATE INDEX IF NOT EXISTS revisions_recording ON revisions(recording_id,id DESC);
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
	if _, err = db.Exec(schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("initialize database: %w", err)
	}
	if err = s.ensureHistoryColumns(context.Background()); err != nil {
		db.Close()
		return nil, fmt.Errorf("migrate database: %w", err)
	}
	if err = s.ensureDefaults(context.Background()); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

func (s *Store) ensureHistoryColumns(ctx context.Context) error {
	rows, err := s.db.QueryContext(ctx, "PRAGMA table_info(history)")
	if err != nil {
		return err
	}
	seen := map[string]bool{}
	for rows.Next() {
		var cid, notnull, pk int
		var name, typ string
		var def any
		if err := rows.Scan(&cid, &name, &typ, &notnull, &def, &pk); err != nil {
			rows.Close()
			return err
		}
		seen[name] = true
	}
	if err := rows.Close(); err != nil {
		return err
	}
	for _, x := range []struct{ name, sql string }{{"source", "ALTER TABLE history ADD COLUMN source TEXT NOT NULL DEFAULT ''"}, {"duration_ms", "ALTER TABLE history ADD COLUMN duration_ms INTEGER"}, {"first_event_ms", "ALTER TABLE history ADD COLUMN first_event_ms INTEGER"}, {"lookup_outcome", "ALTER TABLE history ADD COLUMN lookup_outcome TEXT NOT NULL DEFAULT ''"}} {
		if !seen[x.name] {
			if _, err := s.db.ExecContext(ctx, x.sql); err != nil {
				return err
			}
		}
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
	_, err = tx.ExecContext(ctx, `INSERT OR IGNORE INTO settings(singleton,mode,active_collection_id,first_event_delay_ms,delay_multiplier) VALUES(1,'replay',?,0,1)`, id)
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
	err := s.db.QueryRowContext(ctx, "SELECT mode,active_collection_id,first_event_delay_ms,delay_multiplier FROM settings WHERE singleton=1").Scan(&v.Mode, &v.ActiveCollectionID, &v.FirstEventDelayMS, &v.DelayMultiplier)
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
	r, err := s.db.ExecContext(ctx, "UPDATE settings SET mode=?,active_collection_id=?,first_event_delay_ms=?,delay_multiplier=? WHERE singleton=1", v.Mode, v.ActiveCollectionID, v.FirstEventDelayMS, v.DelayMultiplier)
	if err != nil {
		return err
	}
	n, _ := r.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

const recordingCols = "id,collection_id,key,route,request,matching_input,upstream_identity,streaming,active_revision_id,created_at"

func scanRecording(row interface{ Scan(...any) error }) (model.Recording, error) {
	var r model.Recording
	var stream int
	var active sql.NullInt64
	err := row.Scan(&r.ID, &r.CollectionID, &r.Key, &r.Route, &r.Request, &r.MatchingInput, &r.UpstreamIdentity, &stream, &active, &r.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return r, ErrNotFound
	}
	r.Streaming = stream != 0
	if active.Valid {
		r.ActiveRevisionID = active.Int64
	}
	return r, err
}
func scanRevision(row interface{ Scan(...any) error }) (model.Revision, error) {
	var r model.Revision
	var h, e string
	err := row.Scan(&r.ID, &r.RecordingID, &r.Status, &h, &r.Body, &e, &r.Request, &r.MatchingInput, &r.Source, &r.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return r, ErrNotFound
	}
	if err != nil {
		return r, err
	}
	if err = json.Unmarshal([]byte(h), &r.Headers); err != nil {
		return r, err
	}
	if err = json.Unmarshal([]byte(e), &r.Events); err != nil {
		return r, err
	}
	return r, nil
}
func (s *Store) Lookup(ctx context.Context, cid int64, key string) (model.Entry, error) {
	r, err := scanRecording(s.db.QueryRowContext(ctx, "SELECT "+recordingCols+" FROM recordings WHERE collection_id=? AND key=?", cid, key))
	if err != nil {
		return model.Entry{}, err
	}
	if r.ActiveRevisionID == 0 {
		return model.Entry{}, ErrNotFound
	}
	v, err := scanRevision(s.db.QueryRowContext(ctx, "SELECT id,recording_id,status,headers,body,events,request,matching_input,source,created_at FROM revisions WHERE id=?", r.ActiveRevisionID))
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

func (s *Store) publish(ctx context.Context, rec model.Recording, rev model.Revision, expectedRevisionID *int64) (model.Entry, error) {
	if rec.CollectionID == 0 || rec.Key == "" || rec.Route == "" {
		return model.Entry{}, errors.New("recording collection, key, and route are required")
	}
	if !json.Valid(rec.Request) || !json.Valid(rec.MatchingInput) {
		return model.Entry{}, errors.New("request and matching input must be valid JSON")
	}
	collection, err := s.Collection(ctx, rec.CollectionID)
	if err != nil {
		return model.Entry{}, err
	}
	key, input, err := matching.Key(rec.Route, rec.UpstreamIdentity, rec.Request, collection.Exclusions)
	if err != nil {
		return model.Entry{}, err
	}
	if key != rec.Key || string(input) != string(rec.MatchingInput) {
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
	if len(rev.MatchingInput) == 0 {
		rev.MatchingInput = append(json.RawMessage(nil), rec.MatchingInput...)
	}
	vkey, vinput, err := matching.Key(rec.Route, rec.UpstreamIdentity, rev.Request, collection.Exclusions)
	if err != nil || vkey != rec.Key || string(vinput) != string(rev.MatchingInput) {
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
	ej, err := encode(rev.Events)
	if err != nil {
		return model.Entry{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return model.Entry{}, err
	}
	defer tx.Rollback()
	incomingRequest := append(json.RawMessage(nil), rec.Request...)
	incomingMatching := append(json.RawMessage(nil), rec.MatchingInput...)
	var existing model.Recording
	existing, err = scanRecording(tx.QueryRowContext(ctx, "SELECT "+recordingCols+" FROM recordings WHERE collection_id=? AND key=?", rec.CollectionID, rec.Key))
	if errors.Is(err, ErrNotFound) {
		if expectedRevisionID != nil && *expectedRevisionID != 0 {
			return model.Entry{}, ErrConflict
		}
		rec.CreatedAt = now()
		res, e := tx.ExecContext(ctx, `INSERT INTO recordings(collection_id,key,route,request,matching_input,upstream_identity,streaming,created_at) VALUES(?,?,?,?,?,?,?,?)`, rec.CollectionID, rec.Key, rec.Route, []byte(rec.Request), []byte(rec.MatchingInput), rec.UpstreamIdentity, rec.Streaming, rec.CreatedAt)
		if e != nil {
			return model.Entry{}, e
		}
		rec.ID, e = res.LastInsertId()
		if e != nil {
			return model.Entry{}, e
		}
	} else if err != nil {
		return model.Entry{}, err
	} else {
		if expectedRevisionID != nil && existing.ActiveRevisionID != *expectedRevisionID {
			return model.Entry{}, ErrConflict
		}
		rec = existing
		if rev.Source == "recorded" {
			rec.Request, rec.MatchingInput = incomingRequest, incomingMatching
		}
	}
	if len(rev.Request) == 0 {
		rev.Request = append(json.RawMessage(nil), rec.Request...)
	}
	if len(rev.MatchingInput) == 0 {
		rev.MatchingInput = append(json.RawMessage(nil), rec.MatchingInput...)
	}
	if !json.Valid(rev.Request) || !json.Valid(rev.MatchingInput) {
		return model.Entry{}, errors.New("revision request provenance must be valid JSON")
	}
	rev.RecordingID = rec.ID
	rev.CreatedAt = now()
	res, err := tx.ExecContext(ctx, `INSERT INTO revisions(recording_id,status,headers,body,events,request,matching_input,source,created_at) VALUES(?,?,?,?,?,?,?,?,?)`, rev.RecordingID, rev.Status, hj, rev.Body, ej, []byte(rev.Request), []byte(rev.MatchingInput), rev.Source, rev.CreatedAt)
	if err != nil {
		return model.Entry{}, err
	}
	rev.ID, err = res.LastInsertId()
	if err != nil {
		return model.Entry{}, err
	}
	_, err = tx.ExecContext(ctx, "UPDATE recordings SET active_revision_id=?,request=?,matching_input=? WHERE id=?", rev.ID, []byte(rev.Request), []byte(rev.MatchingInput), rec.ID)
	if err != nil {
		return model.Entry{}, err
	}
	rec.ActiveRevisionID = rev.ID
	rec.Request, rec.MatchingInput = append(json.RawMessage(nil), rev.Request...), append(json.RawMessage(nil), rev.MatchingInput...)
	if err = tx.Commit(); err != nil {
		return model.Entry{}, err
	}
	return model.Entry{Recording: rec, Revision: rev}, nil
}

func (s *Store) Recordings(ctx context.Context, cid int64) ([]model.Recording, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT "+recordingCols+" FROM recordings WHERE collection_id=? ORDER BY id DESC", cid)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []model.Recording{}
	for rows.Next() {
		r, e := scanRecording(rows)
		if e != nil {
			return nil, e
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
func (s *Store) Get(ctx context.Context, id int64) (model.Entry, error) {
	r, err := scanRecording(s.db.QueryRowContext(ctx, "SELECT "+recordingCols+" FROM recordings WHERE id=?", id))
	if err != nil {
		return model.Entry{}, err
	}
	if r.ActiveRevisionID == 0 {
		return model.Entry{}, ErrNotFound
	}
	v, err := scanRevision(s.db.QueryRowContext(ctx, "SELECT id,recording_id,status,headers,body,events,request,matching_input,source,created_at FROM revisions WHERE id=?", r.ActiveRevisionID))
	return model.Entry{Recording: r, Revision: v}, err
}
func (s *Store) Revisions(ctx context.Context, rid int64) ([]model.Revision, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT id,recording_id,status,headers,body,events,request,matching_input,source,created_at FROM revisions WHERE recording_id=? ORDER BY id DESC", rid)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []model.Revision{}
	for rows.Next() {
		r, e := scanRevision(rows)
		if e != nil {
			return nil, e
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
func (s *Store) Restore(ctx context.Context, rid, vid int64) (model.Entry, error) {
	return s.restore(ctx, rid, vid, nil)
}

func (s *Store) RestoreIfActive(ctx context.Context, rid, vid, expectedActiveID int64) (model.Entry, error) {
	return s.restore(ctx, rid, vid, &expectedActiveID)
}
func (s *Store) restore(ctx context.Context, rid, vid int64, expectedActiveID *int64) (model.Entry, error) {
	query := `UPDATE recordings SET active_revision_id=?,request=(SELECT request FROM revisions WHERE id=?),matching_input=(SELECT matching_input FROM revisions WHERE id=?) WHERE id=? AND EXISTS(SELECT 1 FROM revisions WHERE id=? AND recording_id=?)`
	args := []any{vid, vid, vid, rid, vid, rid}
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

func (s *Store) AddHistory(ctx context.Context, h model.History) error {
	if h.CreatedAt == "" {
		h.CreatedAt = now()
	}
	if h.Request == nil {
		h.Request = json.RawMessage(`null`)
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO history(collection_id,route,key,request,outcome,detail,recording_id,created_at,source,duration_ms,first_event_ms,lookup_outcome) VALUES(?,?,?,?,?,?,?,?,?,?,?,?)`, h.CollectionID, h.Route, h.Key, []byte(h.Request), h.Outcome, h.Detail, h.RecordingID, h.CreatedAt, h.Source, h.DurationMS, h.FirstEventMS, h.CacheStatus)
	return err
}
func (s *Store) History(ctx context.Context, cid int64, limit int) ([]model.History, error) {
	if limit <= 0 {
		limit = 100
	}
	if limit > 1000 {
		limit = 1000
	}
	rows, err := s.db.QueryContext(ctx, "SELECT id,collection_id,route,key,request,outcome,detail,recording_id,created_at,source,duration_ms,first_event_ms,lookup_outcome FROM history WHERE collection_id=? ORDER BY id DESC LIMIT ?", cid, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []model.History{}
	for rows.Next() {
		var h model.History
		if err := rows.Scan(&h.ID, &h.CollectionID, &h.Route, &h.Key, &h.Request, &h.Outcome, &h.Detail, &h.RecordingID, &h.CreatedAt, &h.Source, &h.DurationMS, &h.FirstEventMS, &h.CacheStatus); err != nil {
			return nil, err
		}
		out = append(out, h)
	}
	return out, rows.Err()
}

func (s *Store) HistoryItem(ctx context.Context, id int64) (model.History, error) {
	var h model.History
	err := s.db.QueryRowContext(ctx, "SELECT id,collection_id,route,key,request,outcome,detail,recording_id,created_at,source,duration_ms,first_event_ms,lookup_outcome FROM history WHERE id=?", id).Scan(&h.ID, &h.CollectionID, &h.Route, &h.Key, &h.Request, &h.Outcome, &h.Detail, &h.RecordingID, &h.CreatedAt, &h.Source, &h.DurationMS, &h.FirstEventMS, &h.CacheStatus)
	if errors.Is(err, sql.ErrNoRows) {
		return h, ErrNotFound
	}
	return h, err
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
	rows, err := s.db.QueryContext(ctx, `SELECT outcome,source,duration_ms,first_event_ms,created_at,lookup_outcome FROM history WHERE collection_id=? ORDER BY id`, cid)
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
		var body, events string
		if err := rows.Scan(&body, &events); err != nil {
			return false, err
		}
		if rawContainsState(body, stateID) {
			return true, nil
		}
		var es []model.Event
		if json.Unmarshal([]byte(events), &es) == nil {
			for _, e := range es {
				if rawContainsState(eventJSON(e.Data), stateID) {
					return true, nil
				}
			}
		}
	}
	return false, rows.Err()
}

// Export creates a transactionally consistent, collection-scoped SQLite snapshot.
// The destination must not already exist, preventing accidental replacement.
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
	if err = tx.Commit(); err != nil {
		return err
	}
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

// Import copies complete collections and their immutable revision history from a
// validated replay-proxy SQLite snapshot. IDs are remapped and the local active
// collection and timing settings are intentionally retained.
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
	var integrity string
	if err = src.QueryRowContext(ctx, "PRAGMA integrity_check").Scan(&integrity); err != nil {
		return nil, fmt.Errorf("invalid snapshot: %w", err)
	}
	if integrity != "ok" {
		return nil, fmt.Errorf("invalid snapshot: integrity check: %s", integrity)
	}
	fkRows, fkErr := src.QueryContext(ctx, "PRAGMA foreign_key_check")
	if fkErr != nil {
		return nil, fmt.Errorf("invalid snapshot foreign keys: %w", fkErr)
	}
	if fkRows.Next() {
		fkRows.Close()
		return nil, errors.New("invalid snapshot: foreign key violation")
	}
	if fkErr = fkRows.Close(); fkErr != nil {
		return nil, fkErr
	}
	rows, err := src.QueryContext(ctx, "SELECT id,name,exclusions,created_at FROM collections ORDER BY id")
	if err != nil {
		return nil, fmt.Errorf("invalid snapshot schema: %w", err)
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
	var imports []importedCollection
	for rows.Next() {
		var x importedCollection
		var ex string
		if err = rows.Scan(&x.oldID, &x.c.Name, &ex, &x.c.CreatedAt); err != nil {
			rows.Close()
			return nil, err
		}
		if err = json.Unmarshal([]byte(ex), &x.c.Exclusions); err != nil {
			rows.Close()
			return nil, fmt.Errorf("invalid exclusions: %w", err)
		}
		if err = matching.ValidateExclusions(x.c.Exclusions); err != nil {
			rows.Close()
			return nil, fmt.Errorf("invalid exclusions: %w", err)
		}
		imports = append(imports, x)
	}
	if err = rows.Close(); err != nil {
		return nil, err
	}
	if len(imports) == 0 {
		return nil, errors.New("snapshot contains no collections")
	}
	// Fully read and validate the immutable source before taking the destination
	// write lock. The transaction below then contains inserts only.
	for ci := range imports {
		item := &imports[ci]
		rrows, e := src.QueryContext(ctx, "SELECT "+recordingCols+" FROM recordings WHERE collection_id=? ORDER BY id", item.oldID)
		if e != nil {
			return nil, fmt.Errorf("invalid snapshot recordings: %w", e)
		}
		for rrows.Next() {
			r, e := scanRecording(rrows)
			if e != nil {
				rrows.Close()
				return nil, e
			}
			if r.ActiveRevisionID == 0 {
				rrows.Close()
				return nil, errors.New("snapshot recording has no active revision")
			}
			if !json.Valid(r.Request) || !json.Valid(r.MatchingInput) {
				rrows.Close()
				return nil, errors.New("snapshot contains invalid recording JSON")
			}
			key, input, e := matching.Key(r.Route, r.UpstreamIdentity, r.Request, item.c.Exclusions)
			if e != nil || key != r.Key || string(input) != string(r.MatchingInput) {
				rrows.Close()
				return nil, errors.New("snapshot recording key or matching input does not match request")
			}
			if requestStreaming(r.Request) != r.Streaming {
				rrows.Close()
				return nil, errors.New("snapshot recording streaming flag disagrees with request")
			}
			loaded := importedRecording{r: r}
			vrows, e := src.QueryContext(ctx, "SELECT id,recording_id,status,headers,body,events,request,matching_input,source,created_at FROM revisions WHERE recording_id=? ORDER BY id", r.ID)
			if e != nil {
				rrows.Close()
				return nil, e
			}
			var active *model.Revision
			for vrows.Next() {
				v, e := scanRevision(vrows)
				if e != nil {
					vrows.Close()
					rrows.Close()
					return nil, e
				}
				vk, vi, ve := matching.Key(r.Route, r.UpstreamIdentity, v.Request, item.c.Exclusions)
				if ve != nil || vk != r.Key || string(vi) != string(v.MatchingInput) {
					vrows.Close()
					rrows.Close()
					return nil, errors.New("snapshot revision provenance does not match recording key")
				}
				if requestStreaming(v.Request) != r.Streaming {
					vrows.Close()
					rrows.Close()
					return nil, errors.New("snapshot revision streaming flag disagrees with request")
				}
				if e = validateImportedHeaders(v.Headers); e != nil {
					vrows.Close()
					rrows.Close()
					return nil, e
				}
				if e = protocol.Validate(r.Route, r.Streaming, v); e != nil {
					vrows.Close()
					rrows.Close()
					return nil, fmt.Errorf("invalid revision: %w", e)
				}
				loaded.revisions = append(loaded.revisions, v)
				if v.ID == r.ActiveRevisionID {
					copy := v
					active = &copy
				}
			}
			if e = vrows.Close(); e != nil {
				rrows.Close()
				return nil, e
			}
			if active == nil {
				rrows.Close()
				return nil, errors.New("snapshot recording references missing active revision")
			}
			if string(active.Request) != string(r.Request) || string(active.MatchingInput) != string(r.MatchingInput) {
				rrows.Close()
				return nil, errors.New("snapshot recording provenance disagrees with active revision")
			}
			item.recordings = append(item.recordings, loaded)
		}
		if e = rrows.Close(); e != nil {
			return nil, e
		}
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
		ex, _ := encode(item.c.Exclusions)
		created := item.c.CreatedAt
		if created == "" {
			created = now()
		}
		res, e := tx.ExecContext(ctx, "INSERT INTO collections(name,exclusions,created_at) VALUES(?,?,?)", name, ex, created)
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
			res, e = tx.ExecContext(ctx, `INSERT INTO recordings(collection_id,key,route,request,matching_input,upstream_identity,streaming,created_at) VALUES(?,?,?,?,?,?,?,?)`, newCID, r.Key, r.Route, []byte(r.Request), []byte(r.MatchingInput), r.UpstreamIdentity, r.Streaming, r.CreatedAt)
			if e != nil {
				return nil, e
			}
			newRID, e := res.LastInsertId()
			if e != nil {
				return nil, e
			}
			var newActive int64
			for _, v := range loaded.revisions {
				hj, _ := encode(v.Headers)
				ej, _ := encode(v.Events)
				res, e = tx.ExecContext(ctx, `INSERT INTO revisions(recording_id,status,headers,body,events,request,matching_input,source,created_at) VALUES(?,?,?,?,?,?,?,?,?)`, newRID, v.Status, hj, v.Body, ej, []byte(v.Request), []byte(v.MatchingInput), v.Source, v.CreatedAt)
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
