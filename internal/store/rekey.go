package store

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"sort"

	matching "github.com/local/llm-replay-proxy/internal/match"
)

// emptyArrayRekey names the one-time migration that recomputes stored matching
// keys after empty JSON arrays stopped canonicalizing as null.
const emptyArrayRekey = "rekey-empty-arrays-1"

// RekeyReport summarizes a matching-key migration.
type RekeyReport struct {
	Rekeyed int // existing recordings whose key or matching input changed
	Merged  int // recordings whose revisions joined another recording with the same new key
	Split   int // recordings created for revisions whose request now has its own key
}

func (r RekeyReport) changed() bool { return r.Rekeyed+r.Merged+r.Split > 0 }

// migrateMatchingKeys runs the one-time empty-array re-key and logs a summary
// when anything changed.
func (s *Store) migrateMatchingKeys(ctx context.Context) error {
	report, err := s.rekeyOnce(ctx, emptyArrayRekey)
	if err != nil {
		return err
	}
	if report.changed() {
		log.Printf("store: re-keyed recordings for empty-array matching (rekeyed=%d merged=%d split=%d)", report.Rekeyed, report.Merged, report.Split)
	}
	return nil
}

// rekeyOnce recomputes every recording's key and matching input from its stored
// request, route, upstream identity, and collection exclusions, in a single
// transaction recorded under name so it runs at most once per database.
//
// Each revision belongs to the recording for the key its own request now
// produces. When several recordings now share a key, the one with the newest
// active revision keeps serving; the others' revisions become its inactive
// history. When an inactive revision's request now has a key no recording
// holds, a recording is created for it with its newest such revision active.
// No revision is deleted, and afterwards every revision's provenance matches
// its recording's key again.
func (s *Store) rekeyOnce(ctx context.Context, name string) (RekeyReport, error) {
	var report RekeyReport
	if _, err := s.db.ExecContext(ctx, "CREATE TABLE IF NOT EXISTS store_migrations (name TEXT PRIMARY KEY, applied_at TEXT NOT NULL)"); err != nil {
		return report, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return report, err
	}
	defer tx.Rollback()
	var applied int
	if err = tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM store_migrations WHERE name=?", name).Scan(&applied); err != nil {
		return report, err
	}
	if applied > 0 {
		return report, nil
	}
	rows, err := tx.QueryContext(ctx, "SELECT id,exclusions FROM collections ORDER BY id")
	if err != nil {
		return report, err
	}
	exclusions := map[int64][]string{}
	var ids []int64
	for rows.Next() {
		var id int64
		var raw string
		if err = rows.Scan(&id, &raw); err != nil {
			rows.Close()
			return report, err
		}
		var ex []string
		if err = json.Unmarshal([]byte(raw), &ex); err != nil {
			rows.Close()
			return report, fmt.Errorf("collection %d exclusions: %w", id, err)
		}
		exclusions[id], ids = ex, append(ids, id)
	}
	if err = rows.Close(); err != nil {
		return report, err
	}
	for _, id := range ids {
		r, err := rekeyCollection(ctx, tx, id, exclusions[id])
		if err != nil {
			return report, fmt.Errorf("re-key collection %d: %w", id, err)
		}
		report.Rekeyed += r.Rekeyed
		report.Merged += r.Merged
		report.Split += r.Split
	}
	if _, err = tx.ExecContext(ctx, "INSERT INTO store_migrations(name,applied_at) VALUES(?,?)", name, now()); err != nil {
		return report, err
	}
	return report, tx.Commit()
}

type rekeyRecording struct {
	id, active        int64
	key, route, ident string
	input             []byte
	streaming         bool
	created           string
	newKey            string
	newInput          []byte
}

type rekeyRevision struct {
	id, recordingID int64
	input           []byte
	newKey          string
	newInput        []byte
}

func rekeyCollection(ctx context.Context, tx *sql.Tx, cid int64, exclusions []string) (RekeyReport, error) {
	var report RekeyReport
	recordings := map[int64]*rekeyRecording{}
	var order []int64
	rows, err := tx.QueryContext(ctx, "SELECT id,key,route,matching_input,upstream_identity,streaming,active_revision_id,created_at FROM recordings WHERE collection_id=? ORDER BY id", cid)
	if err != nil {
		return report, err
	}
	for rows.Next() {
		r := &rekeyRecording{}
		var active sql.NullInt64
		var stream int
		if err = rows.Scan(&r.id, &r.key, &r.route, &r.input, &r.ident, &stream, &active, &r.created); err != nil {
			rows.Close()
			return report, err
		}
		r.streaming, r.active = stream != 0, active.Int64
		recordings[r.id] = r
		order = append(order, r.id)
	}
	if err = rows.Close(); err != nil {
		return report, err
	}
	if len(order) == 0 {
		return report, nil
	}

	var revisions []*rekeyRevision
	byID := map[int64]*rekeyRevision{}
	rows, err = tx.QueryContext(ctx, "SELECT v.id,v.recording_id,v.request,v.matching_input FROM revisions v JOIN recordings r ON r.id=v.recording_id WHERE r.collection_id=? ORDER BY v.id", cid)
	if err != nil {
		return report, err
	}
	for rows.Next() {
		v := &rekeyRevision{}
		var request []byte
		if err = rows.Scan(&v.id, &v.recordingID, &request, &v.input); err != nil {
			rows.Close()
			return report, err
		}
		owner := recordings[v.recordingID]
		v.newKey, v.newInput, err = matching.Key(owner.route, owner.ident, request, exclusions)
		if err != nil {
			rows.Close()
			return report, fmt.Errorf("revision %d: %w", v.id, err)
		}
		revisions = append(revisions, v)
		byID[v.id] = v
	}
	if err = rows.Close(); err != nil {
		return report, err
	}

	// A recording without an active revision is never served and has no
	// authoritative request; it and its revisions are left untouched.
	participating := func(r *rekeyRecording) bool { return r.active != 0 && byID[r.active] != nil }
	for _, id := range order {
		if r := recordings[id]; participating(r) {
			r.newKey, r.newInput = byID[r.active].newKey, byID[r.active].newInput
		}
	}

	// Fast path: nothing in this collection changes.
	unchanged := true
	for _, v := range revisions {
		r := recordings[v.recordingID]
		if participating(r) && (v.newKey != r.newKey || v.newKey != r.key || !bytes.Equal(v.newInput, v.input) || !bytes.Equal(r.newInput, r.input)) {
			unchanged = false
			break
		}
	}
	if unchanged {
		return report, nil
	}

	// Choose one recording per new key: the owner with the newest active
	// revision. Keys with revisions but no owner get a new recording.
	owners := map[string]*rekeyRecording{}
	for _, id := range order {
		r := recordings[id]
		if !participating(r) {
			continue
		}
		if current := owners[r.newKey]; current == nil || r.active > current.active {
			owners[r.newKey] = r
		}
	}
	groups := map[string][]*rekeyRevision{}
	var groupKeys []string
	for _, v := range revisions {
		if !participating(recordings[v.recordingID]) {
			continue
		}
		if groups[v.newKey] == nil {
			groupKeys = append(groupKeys, v.newKey)
		}
		groups[v.newKey] = append(groups[v.newKey], v)
	}
	sort.Strings(groupKeys)

	// Free every key this migration touches before assigning final keys, so
	// swaps and merges cannot trip UNIQUE(collection_id,key).
	for _, id := range order {
		r := recordings[id]
		if participating(r) && (owners[r.newKey] != r || r.newKey != r.key) {
			if _, err = tx.ExecContext(ctx, "UPDATE recordings SET key=? WHERE id=?", fmt.Sprintf("rekey-pending:%d", r.id), r.id); err != nil {
				return report, err
			}
		}
	}

	for _, key := range groupKeys {
		group := groups[key]
		owner := owners[key]
		var targetID int64
		if owner != nil {
			targetID = owner.id
			if owner.newKey != owner.key || !bytes.Equal(owner.newInput, owner.input) {
				report.Rekeyed++
			}
			if _, err = tx.ExecContext(ctx, "UPDATE recordings SET key=?,matching_input=? WHERE id=?", key, owner.newInput, owner.id); err != nil {
				return report, err
			}
		} else {
			// Revisions are ordered by id, so the last one is the newest.
			newest := group[len(group)-1]
			source := recordings[newest.recordingID]
			res, err := tx.ExecContext(ctx, `INSERT INTO recordings(collection_id,key,route,request,matching_input,upstream_identity,streaming,active_revision_id,created_at)
SELECT ?,?,?,request,?,?,?,id,? FROM revisions WHERE id=?`, cid, key, source.route, newest.newInput, source.ident, source.streaming, source.created, newest.id)
			if err != nil {
				return report, err
			}
			if targetID, err = res.LastInsertId(); err != nil {
				return report, err
			}
			report.Split++
		}
		for _, v := range group {
			if v.recordingID == targetID && bytes.Equal(v.input, v.newInput) {
				continue
			}
			if _, err = tx.ExecContext(ctx, "UPDATE revisions SET recording_id=?,matching_input=? WHERE id=?", targetID, v.newInput, v.id); err != nil {
				return report, err
			}
		}
	}

	// Owners that lost their key to a newer recording now hold no revisions.
	for _, id := range order {
		r := recordings[id]
		if participating(r) && owners[r.newKey] != r {
			if _, err = tx.ExecContext(ctx, "DELETE FROM recordings WHERE id=?", r.id); err != nil {
				return report, err
			}
			report.Merged++
		}
	}
	return report, nil
}
