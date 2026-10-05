package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"log"
	"time"

	"github.com/local/llm-replay-proxy/internal/model"
	"github.com/local/llm-replay-proxy/internal/protocol"
)

// DefaultHistoryLimit is the history rows kept per collection on a new database.
const DefaultHistoryLimit = 10_000

// MaxHistoryLimit bounds the history_limit setting.
const MaxHistoryLimit = 100_000_000

// ErrActiveCollection rejects deleting the collection traffic is using.
var ErrActiveCollection = errors.New("store: collection is active")

// summaryVersion changes whenever protocol.SummarizeRequest does, so stored
// summaries made by an older build are recomputed.
const summaryVersion = 1

type storedSummary struct {
	Version int `json:"v"`
	model.RequestSummary
}

// summaries returns the summary of each body in need (body id to the route it
// was sent to). A summary is computed the first time a list needs it and saved
// with the body, keeping the write path free of request parsing.
func (s *Store) summaries(ctx context.Context, need map[int64]string) (map[int64]*model.RequestSummary, error) {
	out := make(map[int64]*model.RequestSummary, len(need))
	for id, route := range need {
		var raw sql.NullString
		if err := s.db.QueryRowContext(ctx, "SELECT summary FROM bodies WHERE id=?", id).Scan(&raw); err != nil {
			return nil, err
		}
		var stored storedSummary
		if raw.Valid && json.Unmarshal([]byte(raw.String), &stored) == nil && stored.Version == summaryVersion {
			out[id] = &stored.RequestSummary
			continue
		}
		body, err := loadBody(ctx, s.db, id)
		if err != nil {
			return nil, err
		}
		stored = storedSummary{Version: summaryVersion, RequestSummary: protocol.SummarizeRequest(route, body)}
		encoded, err := json.Marshal(stored)
		if err != nil {
			return nil, err
		}
		if _, err = s.db.ExecContext(ctx, "UPDATE bodies SET summary=? WHERE id=?", string(encoded), id); err != nil {
			return nil, err
		}
		out[id] = &stored.RequestSummary
	}
	return out, nil
}

// Recordings lists a collection, newest first, with each active request
// summarized and replay hits counted from history. No request bytes are read
// unless a summary has not been computed yet.
func (s *Store) Recordings(ctx context.Context, cid int64) ([]model.RecordingSummary, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT r.id,r.collection_id,r.key,r.route,r.upstream_identity,r.streaming,r.active_revision_id,r.created_at,
 v.request_body,coalesce(v.created_at,''),coalesce(v.source,''),(SELECT count(*) FROM revisions x WHERE x.recording_id=r.id)
FROM recordings r LEFT JOIN revisions v ON v.id=r.active_revision_id WHERE r.collection_id=? ORDER BY r.id DESC`, cid)
	if err != nil {
		return nil, err
	}
	out := []model.RecordingSummary{}
	var bodies []sql.NullInt64
	for rows.Next() {
		var r model.RecordingSummary
		var stream int
		var active, body sql.NullInt64
		if err = rows.Scan(&r.ID, &r.CollectionID, &r.Key, &r.Route, &r.UpstreamIdentity, &stream, &active, &r.CreatedAt, &body, &r.UpdatedAt, &r.Source, &r.Revisions); err != nil {
			rows.Close()
			return nil, err
		}
		r.Streaming, r.ActiveRevisionID = stream != 0, active.Int64
		out = append(out, r)
		bodies = append(bodies, body)
	}
	if err = rows.Close(); err != nil {
		return nil, err
	}
	need := map[int64]string{}
	for i, body := range bodies {
		if body.Valid {
			need[body.Int64] = out[i].Route
		}
	}
	sums, err := s.summaries(ctx, need)
	if err != nil {
		return nil, err
	}
	index := make(map[int64]int, len(out))
	for i := range out {
		out[i].Summary = sums[bodies[i].Int64]
		index[out[i].ID] = i
	}
	rows, err = s.db.QueryContext(ctx, "SELECT recording_id,count(*),max(created_at) FROM history WHERE collection_id=? AND outcome='hit' AND recording_id<>0 GROUP BY recording_id", cid)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id, hits int64
		var last string
		if err = rows.Scan(&id, &hits, &last); err != nil {
			return nil, err
		}
		if i, ok := index[id]; ok {
			out[i].Hits, out[i].LastHitAt = hits, last
		}
	}
	return out, rows.Err()
}

const historyCols = "id,collection_id,route,key,request_body,outcome,detail,recording_id,created_at,source,duration_ms,first_event_ms,lookup_outcome"

func scanHistory(row interface{ Scan(...any) error }) (h model.History, body sql.NullInt64, err error) {
	err = row.Scan(&h.ID, &h.CollectionID, &h.Route, &h.Key, &body, &h.Outcome, &h.Detail, &h.RecordingID, &h.CreatedAt, &h.Source, &h.DurationMS, &h.FirstEventMS, &h.CacheStatus)
	return
}

// History lists a collection's newest calls with their requests summarized.
func (s *Store) History(ctx context.Context, cid int64, limit int) ([]model.History, error) {
	return s.HistoryAfter(ctx, cid, 0, limit)
}

// HistoryAfter lists calls newer than afterID, newest first, so a poller can
// fetch only what it has not seen. History rows never change once written.
func (s *Store) HistoryAfter(ctx context.Context, cid, afterID int64, limit int) ([]model.History, error) {
	if limit <= 0 {
		limit = 100
	}
	if limit > 1000 {
		limit = 1000
	}
	rows, err := s.db.QueryContext(ctx, "SELECT "+historyCols+" FROM history WHERE collection_id=? AND id>? ORDER BY id DESC LIMIT ?", cid, afterID, limit)
	if err != nil {
		return nil, err
	}
	out := []model.History{}
	var bodies []sql.NullInt64
	for rows.Next() {
		h, body, e := scanHistory(rows)
		if e != nil {
			rows.Close()
			return nil, e
		}
		out = append(out, h)
		bodies = append(bodies, body)
	}
	if err = rows.Close(); err != nil {
		return nil, err
	}
	need := map[int64]string{}
	for i, body := range bodies {
		if body.Valid {
			need[body.Int64] = out[i].Route
		}
	}
	sums, err := s.summaries(ctx, need)
	if err != nil {
		return nil, err
	}
	for i, body := range bodies {
		if body.Valid {
			out[i].Summary = sums[body.Int64]
		}
	}
	return out, nil
}

// HistoryItem returns one call with its exact request bytes and summary.
func (s *Store) HistoryItem(ctx context.Context, id int64) (model.History, error) {
	h, body, err := scanHistory(s.db.QueryRowContext(ctx, "SELECT "+historyCols+" FROM history WHERE id=?", id))
	if errors.Is(err, sql.ErrNoRows) {
		return h, ErrNotFound
	}
	if err != nil {
		return h, err
	}
	if h.Request, err = (bodyCache{}).load(ctx, s.db, body); err != nil || !body.Valid {
		return h, err
	}
	sums, err := s.summaries(ctx, map[int64]string{body.Int64: h.Route})
	h.Summary = sums[body.Int64]
	return h, err
}

// DeleteRecording removes a recording and every revision of it. History rows
// that replayed it stay, unlinked.
func (s *Store) DeleteRecording(ctx context.Context, id int64) error {
	return s.deleting(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, "DELETE FROM recordings WHERE id=?", id)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return ErrNotFound
		}
		_, err = tx.ExecContext(ctx, "UPDATE history SET recording_id=0 WHERE recording_id=?", id)
		return err
	})
}

// DeleteCollection removes a collection with its recordings and history. The
// active collection cannot be deleted.
func (s *Store) DeleteCollection(ctx context.Context, id int64) error {
	return s.deleting(ctx, func(tx *sql.Tx) error {
		var active int64
		if err := tx.QueryRowContext(ctx, "SELECT active_collection_id FROM settings WHERE singleton=1").Scan(&active); err != nil {
			return err
		}
		if active == id {
			return ErrActiveCollection
		}
		res, err := tx.ExecContext(ctx, "DELETE FROM collections WHERE id=?", id)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return ErrNotFound
		}
		return nil
	})
}

// ClearHistory deletes a collection's request history.
func (s *Store) ClearHistory(ctx context.Context, cid int64) error {
	return s.deleting(ctx, func(tx *sql.Tx) error {
		if _, err := scanCollection(tx.QueryRowContext(ctx, "SELECT id,name,exclusions,created_at FROM collections WHERE id=?", cid)); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, "DELETE FROM history WHERE collection_id=?", cid)
		return err
	})
}

// PruneHistory keeps each collection's newest history_limit rows and reports
// how many it deleted. The bodies sweep runs only when rows were deleted.
func (s *Store) PruneHistory(ctx context.Context) (int64, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	var limit int64
	if err = tx.QueryRowContext(ctx, "SELECT history_limit FROM settings WHERE singleton=1").Scan(&limit); err != nil || limit == 0 {
		return 0, err
	}
	res, err := tx.ExecContext(ctx, `DELETE FROM history WHERE id IN (SELECT id FROM (
 SELECT id, row_number() OVER (PARTITION BY collection_id ORDER BY id DESC) AS n FROM history) WHERE n>?)`, limit)
	if err != nil {
		return 0, err
	}
	deleted, err := res.RowsAffected()
	if err != nil || deleted == 0 {
		return 0, err
	}
	if err = sweepBodies(ctx, tx); err != nil {
		return 0, err
	}
	return deleted, tx.Commit()
}

// deleting runs fn and then sweeps the request bodies it left unreferenced,
// in one transaction.
func (s *Store) deleting(ctx context.Context, fn func(*sql.Tx) error) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err = fn(tx); err != nil {
		return err
	}
	if err = sweepBodies(ctx, tx); err != nil {
		return err
	}
	return tx.Commit()
}

// MaintainHistory prunes history now and then every interval until ctx ends.
func (s *Store) MaintainHistory(ctx context.Context, interval time.Duration) {
	for {
		if n, err := s.PruneHistory(ctx); err != nil && ctx.Err() == nil {
			log.Printf("store: prune history: %v", err)
		} else if n > 0 {
			log.Printf("store: pruned %d history rows beyond the history limit", n)
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(interval):
		}
	}
}
