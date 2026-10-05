package store

import (
	"cmp"
	"context"
	"database/sql"
	"encoding/binary"
	"errors"
	"hash/fnv"
	"slices"
	"sort"
	"time"

	"github.com/local/llm-replay-proxy/internal/model"
)

// historyRow is one history row with the summaries the dashboard reads: its
// request's and, when it served one, its revision's.
type historyRow struct {
	id, recordingID                                   int64
	route, outcome, lookup, source, detail, createdAt string
	duration, first                                   *int64
	at                                                time.Time
	req                                               *model.RequestSummary
	resp                                              *model.ResponseSummary
}

// model attributes a row to the model that answered it, else the one asked.
func (r *historyRow) model() string {
	if r.resp != nil && r.resp.Model != "" {
		return r.resp.Model
	}
	if r.req != nil && r.req.Model != "" {
		return r.req.Model
	}
	return "unknown"
}

func (r *historyRow) thread() string {
	if r.req == nil {
		return ""
	}
	return r.req.Thread
}

const historyRowCols = `h.id,h.recording_id,h.route,h.outcome,h.lookup_outcome,h.source,h.detail,h.created_at,h.duration_ms,h.first_event_ms,
 h.request_body,b.summary_v,b.summary,h.revision_id,v.response_summary_v,v.response_summary`

// historyFrom joins history rows (h) to their request bodies (b) and served
// revisions (v); threadFrom starts from a thread's bodies instead, through
// the bodies.thread and history.request_body indexes (CROSS JOIN fixes that
// join order for SQLite's planner).
const (
	historyFrom = `history h LEFT JOIN bodies b ON b.id=h.request_body LEFT JOIN revisions v ON v.id=h.revision_id `
	threadFrom  = `bodies b CROSS JOIN history h ON h.request_body=b.id LEFT JOIN revisions v ON v.id=h.revision_id `
)

// historyRows reads history rows (from is historyFrom or threadFrom plus a
// WHERE clause) with their summaries. Each distinct body or revision summary
// is decoded once; any not yet computed is computed and saved afterwards, in
// bulk.
func (s *Store) historyRows(ctx context.Context, from string, args ...any) ([]historyRow, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+historyRowCols+` FROM `+from, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []historyRow
	var bodies, revisions []int64
	reqs := map[int64]*model.RequestSummary{}
	resps := map[int64]*model.ResponseSummary{}
	needReq := map[int64]string{}
	needResp := map[int64]bool{}
	for rows.Next() {
		var r historyRow
		var body, revision, reqV, respV sql.NullInt64
		var reqRaw, respRaw sql.RawBytes
		if err = rows.Scan(&r.id, &r.recordingID, &r.route, &r.outcome, &r.lookup, &r.source, &r.detail, &r.createdAt, &r.duration, &r.first,
			&body, &reqV, &reqRaw, &revision, &respV, &respRaw); err != nil {
			return nil, err
		}
		if t, e := time.Parse(time.RFC3339Nano, r.createdAt); e == nil {
			r.at = t
		}
		if body.Valid {
			if _, seen := reqs[body.Int64]; !seen {
				reqs[body.Int64] = decodeRequestSummary(reqV, reqRaw)
				if reqs[body.Int64] == nil {
					needReq[body.Int64] = r.route
				}
			}
		}
		if revision.Valid {
			if _, seen := resps[revision.Int64]; !seen {
				resps[revision.Int64] = decodeResponseSummary(respV, respRaw)
				if resps[revision.Int64] == nil && respV.Valid {
					needResp[revision.Int64] = true
				}
			}
		}
		out = append(out, r)
		bodies = append(bodies, body.Int64)
		revisions = append(revisions, revision.Int64)
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	rows.Close()
	if len(needReq) > 0 {
		computed, err := s.requestSummaries(ctx, needReq)
		if err != nil {
			return nil, err
		}
		for id, v := range computed {
			reqs[id] = v
		}
	}
	if len(needResp) > 0 {
		computed, err := s.responseSummaries(ctx, needResp)
		if err != nil {
			return nil, err
		}
		for id, v := range computed {
			resps[id] = v
		}
	}
	for i := range out {
		out[i].req, out[i].resp = reqs[bodies[i]], resps[revisions[i]]
	}
	return out, nil
}

// rangeClause narrows a collection's history scan to [from,to). created_at is
// RFC 3339 text, possibly with a non-UTC offset from older builds, so the text
// bounds carry a margin wider than any offset; callers check exactly.
func rangeClause(cid int64, from, to time.Time) (string, []any) {
	where := `WHERE h.collection_id=?`
	args := []any{cid}
	if !from.IsZero() {
		where += ` AND h.created_at>=?`
		args = append(args, from.UTC().AddDate(0, 0, -1).Format(time.DateOnly))
	}
	// A year past 9999 formats with five digits, which sorts before every
	// four-digit year; no upper text bound is needed there.
	if upper := to.UTC().AddDate(0, 0, 2); !to.IsZero() && upper.Year() <= 9999 {
		where += ` AND h.created_at<?`
		args = append(args, upper.Format(time.DateOnly))
	}
	return where, args
}

func inRange(r *historyRow, from, to time.Time) bool {
	if from.IsZero() && to.IsZero() {
		return true
	}
	if r.at.IsZero() {
		return false
	}
	return (from.IsZero() || !r.at.Before(from)) && (to.IsZero() || r.at.Before(to))
}

// histogramBounds are the latency histogram's upper bucket bounds in ms; a
// final unbounded bucket follows.
var histogramBounds = []int64{50, 100, 250, 500, 1000, 2500, 5000, 10000, 30000, 60000}

func histogram(values []int64) []model.HistogramBucket {
	out := make([]model.HistogramBucket, len(histogramBounds)+1)
	for i := range histogramBounds {
		le := histogramBounds[i]
		out[i].Le = &le
	}
	for _, v := range values {
		out[sort.Search(len(histogramBounds), func(i int) bool { return v <= histogramBounds[i] })].Count++
	}
	return out
}

type latencySet struct{ durations, firsts []int64 }

func (l *latencySet) add(r *historyRow) {
	if r.duration != nil {
		l.durations = append(l.durations, *r.duration)
	}
	if r.first != nil {
		l.firsts = append(l.firsts, *r.first)
	}
}

func (l *latencySet) stats() model.LatencyStats {
	return model.LatencyStats{
		Duration: percentiles(l.durations), FirstEvent: percentiles(l.firsts),
		Histogram: histogram(l.durations), FirstEventHistogram: histogram(l.firsts),
	}
}

// latencies splits timings by where the response came from; upstream and
// replay timings are never combined.
type latencies struct{ upstream, replay latencySet }

func (l *latencies) add(r *historyRow) {
	switch r.source {
	case "upstream":
		l.upstream.add(r)
	case "replay":
		l.replay.add(r)
	}
}

// optionalSum is a cost total that stays null until some row reports one.
type optionalSum struct {
	sum      float64
	reported bool
}

func (o *optionalSum) add(v *float64) {
	if v != nil {
		o.sum += *v
		o.reported = true
	}
}

func (o optionalSum) value() *float64 {
	if !o.reported {
		return nil
	}
	v := o.sum
	return &v
}

// tokenSet accumulates what upstream calls cost and what replay saved:
// recorded rows' revisions were fetched upstream, hit rows' were replayed.
type tokenSet struct {
	upstream, replayed model.TokenTotals
	upstreamCost       optionalSum
	savedCost          optionalSum
}

func (t *tokenSet) add(r *historyRow) {
	if r.resp == nil {
		return
	}
	switch r.outcome {
	case "recorded":
		t.upstream.Add(r.resp.Usage)
		t.upstreamCost.add(r.resp.Cost)
	case "hit":
		t.replayed.Add(r.resp.Usage)
		t.savedCost.add(r.resp.Cost)
	}
}

// threadAgg builds a ThreadSummary from a thread's rows in any order.
type threadAgg struct {
	model.ThreadSummary
	first, last     time.Time
	firstID, lastID int64
}

func before(at time.Time, id int64, than time.Time, thanID int64) bool {
	if !at.Equal(than) {
		return at.Before(than)
	}
	return id < thanID
}

func (a *threadAgg) add(r *historyRow) {
	a.Count(r.outcome)
	if r.outcome == "recorded" && r.resp != nil {
		a.UpstreamTokens.Add(r.resp.Usage)
	}
	if r.req != nil && r.req.Items > a.MaxItems {
		a.MaxItems = r.req.Items
	}
	if a.firstID == 0 || before(r.at, r.id, a.first, a.firstID) {
		a.first, a.firstID, a.FirstAt = r.at, r.id, r.createdAt
		a.Opening = r.req.Opening
	}
	if a.lastID == 0 || before(a.last, a.lastID, r.at, r.id) {
		a.last, a.lastID, a.LastAt = r.at, r.id, r.createdAt
		a.Latest, a.Route, a.Model, a.LastOutcome = r.req.Preview, r.route, r.model(), r.outcome
	}
}

// groupThreads aggregates rows with a thread, most recently active first.
func groupThreads(rows []historyRow) []*threadAgg {
	byThread := map[string]*threadAgg{}
	var out []*threadAgg
	for i := range rows {
		r := &rows[i]
		thread := r.thread()
		if thread == "" {
			continue
		}
		a := byThread[thread]
		if a == nil {
			a = &threadAgg{ThreadSummary: model.ThreadSummary{Thread: thread}}
			byThread[thread] = a
			out = append(out, a)
		}
		a.add(r)
	}
	slices.SortFunc(out, func(x, y *threadAgg) int {
		if before(x.last, x.lastID, y.last, y.lastID) {
			return 1
		}
		return -1
	})
	return out
}

// Insights aggregates a collection's history in [from,to) for the dashboard,
// optionally restricted to the rows attributed to one model.
func (s *Store) Insights(ctx context.Context, cid int64, from, to time.Time, modelFilter string) (model.Insights, error) {
	if !from.Before(to) {
		return model.Insights{}, errors.New("insights start must be before end")
	}
	from, to = from.UTC(), to.UTC()
	step, bucket := time.Hour, "hour"
	if to.Sub(from) > 48*time.Hour {
		step, bucket = 24*time.Hour, "day"
	}
	where, args := rangeClause(cid, from, to)
	rows, err := s.historyRows(ctx, historyFrom+where, args...)
	if err != nil {
		return model.Insights{}, err
	}
	kept := rows[:0]
	for i := range rows {
		if inRange(&rows[i], from, to) && (modelFilter == "" || rows[i].model() == modelFilter) {
			kept = append(kept, rows[i])
		}
	}
	rows = kept

	out := model.Insights{From: from.Format(time.RFC3339), To: to.Format(time.RFC3339), Bucket: bucket,
		Series: []model.InsightBucket{}, Models: []model.ModelInsight{}, Routes: []model.RouteInsight{},
		TopRecordings: []model.TopRecording{}, TopThreads: []model.ThreadSummary{}}
	type bucketAgg struct {
		model.InsightBucket
		tokens tokenSet
	}
	type modelAgg struct {
		counts model.OutcomeCounts
		tokens tokenSet
		lat    latencies
	}
	type recordingAgg struct {
		hits    int64
		tokens  tokenSet
		preview string
		model   string
		latest  int64
	}
	start := from.Truncate(step)
	buckets := map[time.Time]*bucketAgg{}
	var order []*bucketAgg
	for t := start; t.Before(to); t = t.Add(step) {
		b := &bucketAgg{InsightBucket: model.InsightBucket{Start: t.Format(time.RFC3339)}}
		buckets[t] = b
		order = append(order, b)
	}
	models := map[string]*modelAgg{}
	routes := map[string]*model.RouteInsight{}
	recordings := map[int64]*recordingAgg{}
	var totals tokenSet
	var lat latencies
	var recordedAfterMiss int64
	threads := map[string]bool{}
	for i := range rows {
		r := &rows[i]
		out.Totals.Count(r.outcome)
		totals.add(r)
		lat.add(r)
		if r.outcome == "recorded" && r.lookup == "miss" {
			recordedAfterMiss++
		}
		if t := r.thread(); t != "" {
			threads[t] = true
		}
		if b := buckets[r.at.Truncate(step)]; b != nil {
			b.Count(r.outcome)
			b.tokens.add(r)
		}
		name := r.model()
		m := models[name]
		if m == nil {
			m = &modelAgg{}
			models[name] = m
		}
		m.counts.Count(r.outcome)
		m.tokens.add(r)
		m.lat.add(r)
		route := routes[r.route]
		if route == nil {
			route = &model.RouteInsight{Route: r.route}
			routes[r.route] = route
		}
		route.Count(r.outcome)
		if r.outcome == "hit" && r.recordingID != 0 {
			rec := recordings[r.recordingID]
			if rec == nil {
				rec = &recordingAgg{}
				recordings[r.recordingID] = rec
			}
			rec.hits++
			rec.tokens.add(r)
			if r.id > rec.latest {
				rec.latest, rec.model = r.id, name
				if r.req != nil {
					rec.preview = r.req.Preview
				}
			}
		}
	}
	if den := out.Totals.Hits + out.Totals.Misses + recordedAfterMiss; den > 0 {
		rate := float64(out.Totals.Hits) / float64(den)
		out.Totals.HitRate = &rate
	}
	out.Totals.UpstreamTokens, out.Totals.ReplayedTokens = totals.upstream, totals.replayed
	out.Totals.UpstreamCost, out.Totals.SavedCost = totals.upstreamCost.value(), totals.savedCost.value()
	out.Totals.Threads = int64(len(threads))
	for _, b := range order {
		b.UpstreamTokens, b.ReplayedTokens = b.tokens.upstream, b.tokens.replayed
		out.Series = append(out.Series, b.InsightBucket)
	}
	for name, m := range models {
		out.Models = append(out.Models, model.ModelInsight{OutcomeCounts: m.counts, Model: name,
			UpstreamTokens: m.tokens.upstream, ReplayedTokens: m.tokens.replayed,
			UpstreamCost: m.tokens.upstreamCost.value(), SavedCost: m.tokens.savedCost.value(),
			Upstream: m.lat.upstream.stats(), Replay: m.lat.replay.stats()})
	}
	slices.SortFunc(out.Models, func(a, b model.ModelInsight) int {
		return cmp.Or(cmp.Compare(b.Requests, a.Requests), cmp.Compare(a.Model, b.Model))
	})
	for _, r := range routes {
		out.Routes = append(out.Routes, *r)
	}
	slices.SortFunc(out.Routes, func(a, b model.RouteInsight) int {
		return cmp.Or(cmp.Compare(b.Requests, a.Requests), cmp.Compare(a.Route, b.Route))
	})
	out.Latency.Upstream, out.Latency.Replay = lat.upstream.stats(), lat.replay.stats()
	for id, rec := range recordings {
		out.TopRecordings = append(out.TopRecordings, model.TopRecording{RecordingID: id, Preview: rec.preview, Model: rec.model,
			Hits: rec.hits, ReplayedTokens: rec.tokens.replayed, SavedCost: rec.tokens.savedCost.value()})
	}
	slices.SortFunc(out.TopRecordings, func(a, b model.TopRecording) int {
		return cmp.Or(cmp.Compare(b.Hits, a.Hits), cmp.Compare(b.RecordingID, a.RecordingID))
	})
	out.TopRecordings = out.TopRecordings[:min(len(out.TopRecordings), 10)]
	grouped := groupThreads(rows)
	// groupThreads orders by recency; a stable sort keeps that among equals.
	slices.SortStableFunc(grouped, func(a, b *threadAgg) int { return cmp.Compare(b.Requests, a.Requests) })
	for _, t := range grouped[:min(len(grouped), 10)] {
		out.TopThreads = append(out.TopThreads, t.ThreadSummary)
	}
	return out, nil
}

// Threads lists a collection's conversation threads, most recently active
// first. A zero from or to leaves that side of the range open.
func (s *Store) Threads(ctx context.Context, cid int64, limit int, from, to time.Time) ([]model.ThreadSummary, error) {
	if limit <= 0 {
		limit = 50
	}
	where, args := rangeClause(cid, from, to)
	rows, err := s.historyRows(ctx, historyFrom+where+` AND h.request_body IS NOT NULL`, args...)
	if err != nil {
		return nil, err
	}
	kept := rows[:0]
	for i := range rows {
		if inRange(&rows[i], from, to) {
			kept = append(kept, rows[i])
		}
	}
	grouped := groupThreads(kept)
	out := make([]model.ThreadSummary, 0, min(len(grouped), limit))
	for _, t := range grouped[:min(len(grouped), limit)] {
		out = append(out, t.ThreadSummary)
	}
	return out, nil
}

// Thread returns one thread with every turn, oldest first, each with the
// summary of the response it was served. ErrNotFound when no row of the
// collection belongs to the thread.
func (s *Store) Thread(ctx context.Context, cid int64, thread string) (model.ThreadDetail, error) {
	if thread == "" {
		return model.ThreadDetail{}, ErrNotFound
	}
	// The thread column is filled as summaries are computed; fill it for
	// this collection first so the indexed lookup misses nothing.
	if err := s.ensureThreads(ctx, cid); err != nil {
		return model.ThreadDetail{}, err
	}
	rows, err := s.historyRows(ctx, threadFrom+`WHERE b.thread=? AND h.collection_id=?`, thread, cid)
	if err != nil {
		return model.ThreadDetail{}, err
	}
	grouped := groupThreads(rows)
	if len(grouped) == 0 {
		return model.ThreadDetail{}, ErrNotFound
	}
	slices.SortFunc(rows, func(a, b historyRow) int {
		if before(a.at, a.id, b.at, b.id) {
			return -1
		}
		return 1
	})
	out := model.ThreadDetail{ThreadSummary: grouped[0].ThreadSummary, Turns: make([]model.ThreadTurn, 0, len(rows))}
	for i := range rows {
		r := &rows[i]
		turn := model.ThreadTurn{HistoryID: r.id, CreatedAt: r.createdAt, Outcome: r.outcome, LookupOutcome: r.lookup, Detail: r.detail,
			RecordingID: r.recordingID, DurationMS: r.duration, FirstEventMS: r.first, Response: r.resp}
		if r.req != nil {
			turn.Items, turn.Preview = r.req.Items, r.req.Preview
		}
		out.Turns = append(out.Turns, turn)
	}
	return out, nil
}

// nearestLimit and nearestThreshold bound Nearest's candidates. Requests up
// to fineLimit bytes are compared with fine chunks (see fineChunks), at most
// fineCandidates recordings per call.
const (
	nearestLimit     = 5
	nearestThreshold = 0.2
	fineLimit        = 16 << 10
	fineCandidates   = 300
)

func manifestIDs(manifest []byte) ([]int64, error) {
	var out []int64
	for len(manifest) > 0 {
		id, n := binary.Uvarint(manifest)
		if n <= 0 {
			return nil, errCorruptBody
		}
		out = append(out, int64(id))
		manifest = manifest[n:]
	}
	return out, nil
}

// fineChunks splits b at content-defined boundaries about every 64 bytes
// (16 to 256) and returns each chunk's hash and length. Stored chunks average
// 8 KiB, so a small request is a single stored chunk and any edit makes it
// share nothing; these finer chunks keep similarity meaningful there.
func fineChunks(b []byte) (hashes []uint64, sizes []int) {
	const minSize, maxSize, mask = 16, 256, 1<<6 - 1
	for len(b) > 0 {
		n := len(b)
		if n > minSize {
			var h uint64
			end := min(n, maxSize)
			n = end
			for i := minSize; i < end; i++ {
				h = h<<1 + gear[b[i]]
				if h&mask == 0 {
					n = i + 1
					break
				}
			}
		}
		f := fnv.New64a()
		f.Write(b[:n])
		hashes, sizes = append(hashes, f.Sum64()), append(sizes, n)
		b = b[n:]
	}
	return
}

// fineSimilarity is the share of a's bytes, by fine chunk, also found in b.
func fineSimilarity(aHashes []uint64, aSizes []int, b []byte) float64 {
	bHashes, _ := fineChunks(b)
	present := make(map[uint64]bool, len(bHashes))
	for _, h := range bHashes {
		present[h] = true
	}
	var shared, total int
	for i, h := range aHashes {
		total += aSizes[i]
		if present[h] {
			shared += aSizes[i]
		}
	}
	if total == 0 {
		return 0
	}
	return float64(shared) / float64(total)
}

// Nearest suggests the recordings a history request most likely meant: those
// whose active request continues the same thread, then those sharing the most
// request bytes. Similarity is the share of the history request's bytes, by
// stored chunk, that the recording's active request also contains, so large
// bodies are never read or parsed. A small request (fineLimit) is also
// compared with fine chunks against recordings of the same route and model,
// keeping the higher share. A recording with exactly the row's key is excluded.
func (s *Store) Nearest(ctx context.Context, historyID int64) ([]model.NearestCandidate, error) {
	var cid int64
	var key, route string
	var body sql.NullInt64
	err := s.db.QueryRowContext(ctx, "SELECT collection_id,key,route,request_body FROM history WHERE id=?", historyID).Scan(&cid, &key, &route, &body)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	out := []model.NearestCandidate{}
	if !body.Valid {
		return out, nil
	}
	var manifest []byte
	var bodySize int64
	if err = s.db.QueryRowContext(ctx, "SELECT chunks,size FROM bodies WHERE id=?", body.Int64).Scan(&manifest, &bodySize); err != nil {
		return nil, err
	}
	chunkIDs, err := manifestIDs(manifest)
	if err != nil {
		return nil, err
	}
	sizes := make(map[int64]int64, len(chunkIDs))
	err = inBatches(slices.Compact(slices.Sorted(slices.Values(chunkIDs))), func(args []any, list string) error {
		rows, err := s.db.QueryContext(ctx, "SELECT id,size FROM chunks WHERE id IN ("+list+")", args...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var id, size int64
			if err = rows.Scan(&id, &size); err != nil {
				return err
			}
			sizes[id] = size
		}
		return rows.Err()
	})
	if err != nil {
		return nil, err
	}
	var total int64
	for _, id := range chunkIDs {
		total += sizes[id]
	}
	type candidate struct {
		model.NearestCandidate
		body, size int64
		route      string
	}
	rows, err := s.db.QueryContext(ctx, `SELECT r.id,r.key,r.route,v.request_body,b.chunks,b.size FROM recordings r
 JOIN revisions v ON v.id=r.active_revision_id JOIN bodies b ON b.id=v.request_body WHERE r.collection_id=? ORDER BY r.id DESC`, cid)
	if err != nil {
		return nil, err
	}
	var all []candidate
	need := map[int64]string{body.Int64: route}
	for rows.Next() {
		var c candidate
		var recKey string
		var recManifest []byte
		if err = rows.Scan(&c.RecordingID, &recKey, &c.route, &c.body, &recManifest, &c.size); err != nil {
			rows.Close()
			return nil, err
		}
		if recKey == key {
			continue
		}
		ids, e := manifestIDs(recManifest)
		if e != nil {
			rows.Close()
			return nil, e
		}
		present := make(map[int64]bool, len(ids))
		for _, id := range ids {
			present[id] = true
		}
		var shared int64
		for _, id := range chunkIDs {
			if present[id] {
				shared += sizes[id]
			}
		}
		if total > 0 {
			c.Similarity = float64(shared) / float64(total)
		}
		all = append(all, c)
		need[c.body] = c.route
	}
	if err = rows.Close(); err != nil {
		return nil, err
	}
	sums, err := s.requestSummaries(ctx, need)
	if err != nil {
		return nil, err
	}
	var own model.RequestSummary
	if v := sums[body.Int64]; v != nil {
		own = *v
	}
	if bodySize <= fineLimit {
		raw, err := loadBody(ctx, s.db, body.Int64)
		if err != nil {
			return nil, err
		}
		hashes, lengths := fineChunks(raw)
		loads := 0
		for i := range all {
			c := &all[i]
			v := sums[c.body]
			if loads == fineCandidates || c.route != route || v == nil || v.Model != own.Model || c.size > 4*fineLimit || c.Similarity == 1 {
				continue
			}
			loads++
			other, err := loadBody(ctx, s.db, c.body)
			if err != nil {
				return nil, err
			}
			c.Similarity = max(c.Similarity, fineSimilarity(hashes, lengths, other))
		}
	}
	kept := all[:0]
	for _, c := range all {
		v := sums[c.body]
		if v != nil {
			c.Preview, c.Model, c.Items = v.Preview, v.Model, v.Items
		}
		switch {
		case own.Thread != "" && v != nil && v.Thread == own.Thread:
			c.Reason = "same_thread"
		case c.Similarity >= nearestThreshold:
			c.Reason = "shared_content"
		default:
			continue
		}
		kept = append(kept, c)
	}
	slices.SortFunc(kept, func(a, b candidate) int {
		rank := func(c candidate) int {
			if c.Reason == "same_thread" {
				return 0
			}
			return 1
		}
		return cmp.Or(cmp.Compare(rank(a), rank(b)), cmp.Compare(b.Similarity, a.Similarity), cmp.Compare(b.RecordingID, a.RecordingID))
	})
	for _, c := range kept[:min(len(kept), nearestLimit)] {
		out = append(out, c.NearestCandidate)
	}
	return out, nil
}
