package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"strings"

	"github.com/local/llm-replay-proxy/internal/model"
	"github.com/local/llm-replay-proxy/internal/protocol"
)

// Summaries are derived data: computed the first time a read needs them and
// saved next to what they describe, never on the proxy's write path. Each
// carries the version of the code that made it, so a build that summarizes
// differently recomputes older ones.
const (
	// summaryVersion changes whenever protocol.SummarizeRequest does.
	summaryVersion = 2
	// responseSummaryVersion changes whenever protocol.SummarizeResponse does.
	responseSummaryVersion = 1
)

// idBatch bounds the ids bound into one IN (...) list.
const idBatch = 500

func placeholders(n int) string {
	return strings.TrimSuffix(strings.Repeat("?,", n), ",")
}

// inBatches runs fn over ids in slices of at most idBatch.
func inBatches(ids []int64, fn func(args []any, list string) error) error {
	for len(ids) > 0 {
		n := min(len(ids), idBatch)
		args := make([]any, n)
		for i, id := range ids[:n] {
			args[i] = id
		}
		if err := fn(args, placeholders(n)); err != nil {
			return err
		}
		ids = ids[n:]
	}
	return nil
}

func decodeRequestSummary(version sql.NullInt64, raw []byte) *model.RequestSummary {
	if version.Int64 != summaryVersion || raw == nil {
		return nil
	}
	var v model.RequestSummary
	if json.Unmarshal(raw, &v) != nil {
		return nil
	}
	return &v
}

func decodeResponseSummary(version sql.NullInt64, raw []byte) *model.ResponseSummary {
	if version.Int64 != responseSummaryVersion || raw == nil {
		return nil
	}
	var v model.ResponseSummary
	if json.Unmarshal(raw, &v) != nil {
		return nil
	}
	return &v
}

// requestSummaries returns the summary of each body in need (body id to the
// route it was sent to). Missing or outdated summaries are computed and
// saved with the body, along with the thread column used to find a thread's
// rows. Ids that name no body are left out.
func (s *Store) requestSummaries(ctx context.Context, need map[int64]string) (map[int64]*model.RequestSummary, error) {
	out := make(map[int64]*model.RequestSummary, len(need))
	ids := make([]int64, 0, len(need))
	for id := range need {
		ids = append(ids, id)
	}
	var stale []int64
	err := inBatches(ids, func(args []any, list string) error {
		rows, err := s.db.QueryContext(ctx, "SELECT id,summary_v,summary FROM bodies WHERE id IN ("+list+")", args...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var id int64
			var version sql.NullInt64
			var raw []byte
			if err = rows.Scan(&id, &version, &raw); err != nil {
				return err
			}
			if v := decodeRequestSummary(version, raw); v != nil {
				out[id] = v
			} else {
				stale = append(stale, id)
			}
		}
		return rows.Err()
	})
	if err != nil {
		return nil, err
	}
	for _, id := range stale {
		body, err := loadBody(ctx, s.db, id)
		if err != nil {
			return nil, err
		}
		v := protocol.SummarizeRequest(need[id], body)
		encoded, err := json.Marshal(v)
		if err != nil {
			return nil, err
		}
		thread := sql.NullString{String: v.Thread, Valid: v.Thread != ""}
		if _, err = s.db.ExecContext(ctx, "UPDATE bodies SET summary=?,summary_v=?,thread=? WHERE id=?", string(encoded), summaryVersion, thread, id); err != nil {
			return nil, err
		}
		out[id] = &v
	}
	return out, nil
}

// responseSummaries returns the summary of each revision in ids, computing
// and saving the ones missing or outdated. Ids that name no revision (one
// deleted with its recording) are left out.
func (s *Store) responseSummaries(ctx context.Context, ids map[int64]bool) (map[int64]*model.ResponseSummary, error) {
	out := make(map[int64]*model.ResponseSummary, len(ids))
	list := make([]int64, 0, len(ids))
	for id := range ids {
		list = append(list, id)
	}
	var stale []int64
	err := inBatches(list, func(args []any, in string) error {
		rows, err := s.db.QueryContext(ctx, "SELECT id,response_summary_v,response_summary FROM revisions WHERE id IN ("+in+")", args...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var id int64
			var version sql.NullInt64
			var raw []byte
			if err = rows.Scan(&id, &version, &raw); err != nil {
				return err
			}
			if v := decodeResponseSummary(version, raw); v != nil {
				out[id] = v
			} else {
				stale = append(stale, id)
			}
		}
		return rows.Err()
	})
	if err != nil {
		return nil, err
	}
	for _, id := range stale {
		var route string
		var streaming bool
		row := s.db.QueryRowContext(ctx, "SELECT "+qualified("v.", revisionCols)+",r.route,r.streaming FROM revisions v JOIN recordings r ON r.id=v.recording_id WHERE v.id=?", id)
		rev, _, err := scanRevision(scanExtra{row, []any{&route, &streaming}})
		if err == ErrNotFound {
			continue
		}
		if err != nil {
			return nil, err
		}
		v := protocol.SummarizeResponse(route, streaming, rev)
		encoded, err := json.Marshal(v)
		if err != nil {
			return nil, err
		}
		if _, err = s.db.ExecContext(ctx, "UPDATE revisions SET response_summary=?,response_summary_v=? WHERE id=?", string(encoded), responseSummaryVersion, id); err != nil {
			return nil, err
		}
		out[id] = &v
	}
	return out, nil
}

// qualified prefixes each comma-separated column with a table alias.
func qualified(prefix, cols string) string {
	return prefix + strings.ReplaceAll(cols, ",", ","+prefix)
}

// scanExtra appends destinations to every Scan, so a row scanner can read
// columns selected after the ones it knows.
type scanExtra struct {
	row   interface{ Scan(...any) error }
	extra []any
}

func (s scanExtra) Scan(dest ...any) error { return s.row.Scan(append(dest, s.extra...)...) }

// ensureThreads summarizes every body a collection's history references that
// has no current summary, so the bodies.thread column can be queried.
func (s *Store) ensureThreads(ctx context.Context, cid int64) error {
	rows, err := s.db.QueryContext(ctx, `SELECT DISTINCT h.request_body,h.route FROM history h JOIN bodies b ON b.id=h.request_body
WHERE h.collection_id=? AND b.summary_v<>?`, cid, summaryVersion)
	if err != nil {
		return err
	}
	need := map[int64]string{}
	for rows.Next() {
		var id int64
		var route string
		if err = rows.Scan(&id, &route); err != nil {
			rows.Close()
			return err
		}
		need[id] = route
	}
	if err = rows.Close(); err != nil {
		return err
	}
	if len(need) == 0 {
		return nil
	}
	_, err = s.requestSummaries(ctx, need)
	return err
}
