package mcpserver

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/local/llm-replay-proxy/internal/match"
	"github.com/local/llm-replay-proxy/internal/model"
	"github.com/local/llm-replay-proxy/internal/protocol"
	"github.com/local/llm-replay-proxy/internal/store"
)

// defaultMaxDifferences bounds the differences one comparison lists.
const defaultMaxDifferences = 50

// nearestShown is how many nearest recordings a diagnosis lists and diffs
// to find the closest.
const nearestShown = 5

// defaultDiffValueBytes is how much of each differing value a comparison
// shows unless the caller asks for more.
const defaultDiffValueBytes = 2000

// difference is a match.Difference as text, so a large value can be cut.
type difference struct {
	Path string `json:"path"`
	// RecordedPath is where the recorded side is, when not at Path.
	RecordedPath string `json:"recorded_path,omitempty"`
	// Kind is "reordered" or "moved" for array elements that only changed
	// position.
	Kind     string `json:"kind,omitempty"`
	Request  string `json:"request"`
	Recorded string `json:"recorded"`
	// ExcerptFrom is the byte offset in both values where a long pair's
	// excerpt starts, a little before their first difference.
	ExcerptFrom int `json:"excerpt_from,omitzero"`
	// Excluded marks a path the collection's exclusions remove from matching,
	// so it did not cause a miss.
	Excluded bool `json:"excluded,omitzero"`
}

type comparison struct {
	RecordingID int64 `json:"recording_id"`
	// MatchingDifferences changed the key; ExcludedDifferences did not.
	MatchingDifferences int          `json:"matching_differences"`
	ExcludedDifferences int          `json:"excluded_differences"`
	Differences         []difference `json:"differences"`
	DifferencesOmitted  int          `json:"differences_omitted,omitzero"`
}

// compare diffs a request against a recording's active request the way
// /api/compare does: a route mismatch is reported as the path /$route.
func compare(route string, request []byte, rec model.Recording, exclusions []string, most int, b *budget) (*comparison, error) {
	var a, r any
	if err := match.DecodeJSON(request, &a); err != nil {
		return nil, errors.New("request is not valid JSON")
	}
	if err := match.DecodeJSON(rec.Request, &r); err != nil {
		return nil, errors.New("recorded request is not valid JSON")
	}
	d := make([]match.Difference, 0)
	if route != rec.Route {
		d = append(d, match.NewDifference("/$route", route, rec.Route, true, true))
	}
	match.DiffAligned("", match.Normalize(route, a), match.Normalize(rec.Route, r), &d)
	match.MarkExcluded(d, exclusions)
	out := &comparison{RecordingID: rec.ID, Differences: []difference{}}
	// Differences that changed the key come first: they explain a miss.
	ordered := make([]match.Difference, 0, len(d))
	for _, excluded := range []bool{false, true} {
		for _, v := range d {
			if v.Excluded == excluded {
				ordered = append(ordered, v)
			}
		}
	}
	for i, v := range ordered {
		if v.Excluded {
			out.ExcludedDifferences++
		} else {
			out.MatchingDifferences++
		}
		if i >= most {
			out.DifferencesOmitted++
			continue
		}
		from := excerptStart(v.RequestDisplay, v.RecordedDisplay, b.perValue)
		out.Differences = append(out.Differences, difference{
			Path:         v.Path,
			RecordedPath: v.RecordedPath,
			Kind:         v.Kind,
			Request:      b.takeFrom(fmt.Sprintf("differences[%d].request", i), v.RequestDisplay, from),
			Recorded:     b.takeFrom(fmt.Sprintf("differences[%d].recorded", i), v.RecordedDisplay, from),
			ExcerptFrom:  from,
			Excluded:     v.Excluded,
		})
	}
	return out, nil
}

// excerptStart is where to start showing two values cut to n bytes, so the
// excerpt shows their first difference: a quarter of n before it, or 0 when
// both fit or the difference is near the start.
func excerptStart(a, b string, n int) int {
	if n <= 0 || (len(a) <= n && len(b) <= n) {
		return 0
	}
	p := 0
	for p < len(a) && p < len(b) && a[p] == b[p] {
		p++
	}
	return runeStart(a, max(p-n/4, 0))
}

// takeFrom takes s from byte offset from, recording a cut when it skips any.
func (b *budget) takeFrom(name, s string, from int) string {
	if from <= 0 {
		return b.take(name, s)
	}
	from = min(from, len(s))
	out := b.take(name, s[from:])
	b.cut[name] = len(s)
	return out
}

type compareRequestsInput struct {
	RecordingID    int64  `json:"recording_id"`
	HistoryID      int64  `json:"history_id,omitempty" jsonschema:"compare this history call's request (give this or request)"`
	Request        string `json:"request,omitempty" jsonschema:"a request body as JSON text, compared on the recording's route (give this or history_id)"`
	MaxDifferences int    `json:"max_differences,omitempty" jsonschema:"differences to list, key-changing ones first (default 50)"`
	MaxBytes       *int   `json:"max_bytes,omitempty" jsonschema:"byte allowance for the listed values (default 200000; 0 = unlimited)"`
	MaxValueBytes  *int   `json:"max_value_bytes,omitempty" jsonschema:"bytes shown of each differing value, an excerpt around where the two first differ (default 2000; 0 = whole values)"`
}

func (t tools) compareRequests(ctx context.Context, _ *mcp.CallToolRequest, in compareRequestsInput) (*mcp.CallToolResult, any, error) {
	if (in.HistoryID == 0) == (in.Request == "") {
		return nil, nil, errors.New("give exactly one of history_id or request")
	}
	most, err := limitOf(in.MaxDifferences, defaultMaxDifferences, 10_000)
	if err != nil {
		return nil, nil, err
	}
	e, err := t.db.Get(ctx, in.RecordingID)
	if err != nil {
		return nil, nil, storeError(fmt.Sprintf("recording %d", in.RecordingID), err)
	}
	c, err := t.db.Collection(ctx, e.Recording.CollectionID)
	if err != nil {
		return nil, nil, err
	}
	route, request := e.Recording.Route, []byte(in.Request)
	if in.HistoryID != 0 {
		h, err := t.db.HistoryItem(ctx, in.HistoryID)
		if err != nil {
			return nil, nil, storeError(fmt.Sprintf("history item %d", in.HistoryID), err)
		}
		if h.CollectionID != e.Recording.CollectionID {
			return nil, nil, errors.New("the history call and recording belong to different collections")
		}
		route, request = h.Route, h.Request
	}
	b, err := newBudget(in.MaxBytes).withValueCap(in.MaxValueBytes, defaultDiffValueBytes)
	if err != nil {
		return nil, nil, err
	}
	v, err := compare(route, request, e.Recording, c.Exclusions, most, b)
	if err != nil {
		return nil, nil, err
	}
	return result(struct {
		*comparison
		Exclusions []string       `json:"collection_exclusions"`
		Truncated  map[string]int `json:"truncated,omitempty"`
	}{v, c.Exclusions, b.truncated()})
}

type diagnoseInput struct {
	HistoryID      int64 `json:"history_id"`
	MaxDifferences int   `json:"max_differences,omitempty" jsonschema:"differences from the closest recording to list (default 50)"`
	MaxBytes       *int  `json:"max_bytes,omitempty" jsonschema:"byte allowance for the listed values (default 200000; 0 = unlimited)"`
	MaxValueBytes  *int  `json:"max_value_bytes,omitempty" jsonschema:"bytes shown of each differing value, an excerpt around where the two first differ (default 2000; 0 = whole values)"`
}

type collectionMatch struct {
	CollectionID   int64  `json:"collection_id"`
	CollectionName string `json:"collection_name"`
	RecordingID    int64  `json:"recording_id"`
}

type diagnosis struct {
	Call       model.History    `json:"call"`
	RevisionID int64            `json:"revision_id,omitzero"`
	Collection model.Collection `json:"collection"`
	// Settings are the current ones, which may have changed since the call.
	Settings model.Settings `json:"current_settings"`
	Findings []string       `json:"findings"`
	// ExactMatchNow is a recording in the call's collection with its key,
	// as of now.
	ExactMatchNow *int64 `json:"exact_match_recording_id_now,omitzero"`
	// OtherCollections hold a recording this request would replay from.
	OtherCollections []collectionMatch        `json:"matches_in_other_collections,omitempty"`
	Nearest          []model.NearestCandidate `json:"nearest_recordings,omitempty"`
	Closest          *comparison              `json:"closest_recording_comparison,omitempty"`
	Truncated        map[string]int           `json:"truncated,omitempty"`
}

// diagnoseReplay explains one history call: why it hit, missed or failed,
// where its request would have matched, and how it differs from the closest
// recording.
func (t tools) diagnoseReplay(ctx context.Context, _ *mcp.CallToolRequest, in diagnoseInput) (*mcp.CallToolResult, any, error) {
	most, err := limitOf(in.MaxDifferences, defaultMaxDifferences, 10_000)
	if err != nil {
		return nil, nil, err
	}
	b, err := newBudget(in.MaxBytes).withValueCap(in.MaxValueBytes, defaultDiffValueBytes)
	if err != nil {
		return nil, nil, err
	}
	h, err := t.db.HistoryItem(ctx, in.HistoryID)
	if err != nil {
		return nil, nil, storeError(fmt.Sprintf("history item %d", in.HistoryID), err)
	}
	c, err := t.db.Collection(ctx, h.CollectionID)
	if err != nil {
		return nil, nil, storeError(fmt.Sprintf("collection %d", h.CollectionID), err)
	}
	settings, err := t.db.Settings(ctx)
	if err != nil {
		return nil, nil, err
	}
	d := diagnosis{Call: h, RevisionID: h.RevisionID, Collection: c, Settings: settings}
	note := func(format string, args ...any) { d.Findings = append(d.Findings, fmt.Sprintf(format, args...)) }

	switch {
	case h.Outcome == "hit":
		note("Replayed recording %d (revision %d).", h.RecordingID, h.RevisionID)
	case h.Source == "replay":
		note("Recording %d matched, but replaying it ended with %q: %s.", h.RecordingID, h.Outcome, h.Detail)
		if h.Outcome == "interrupted" {
			note("The caller disconnected (or the server shut down) during playback; the recording itself may be fine.")
		}
		if e, err := t.db.Get(ctx, h.RecordingID); err == nil {
			if verr := protocol.Validate(e.Recording.Route, e.Recording.Streaming, e.Revision); verr != nil {
				note("Recording %d's active revision %d fails validation: %v.", e.Recording.ID, e.Revision.ID, verr)
			} else {
				note("Recording %d's active revision %d passes validation.", e.Recording.ID, e.Revision.ID)
			}
		}
	case strings.HasPrefix(h.Detail, "request references recorded provider state"):
		note("No recording had this request's key, and it was not forwarded: %s.", h.Detail)
		note("The request continues a conversation by referring to provider-side state (such as previous_response_id) that only exists in a recording. Record this exact follow-up request while the upstream is reachable, or send the conversation self-contained.")
	case h.CacheStatus == "miss" && h.Outcome == "miss":
		note("Replay miss: no recording in collection %q had this request's key (%s), so the proxy returned 404 recording_not_found.", c.Name, h.Detail)
	case h.CacheStatus == "miss":
		note("Replay miss in auto mode: no recording had this key, so the request was forwarded upstream; outcome %q.", h.Outcome)
	case h.CacheStatus == "bypass":
		note("Record mode: the request was forwarded upstream without a replay lookup; outcome %q.", h.Outcome)
	}
	if h.Source == "upstream" {
		switch h.Outcome {
		case "recorded":
			note("The upstream response was published as recording %d revision %d.", h.RecordingID, h.RevisionID)
		case "error", "incomplete", "interrupted":
			note("The upstream response was not recorded (%s): %s.", h.Outcome, h.Detail)
		}
	}
	if h.CacheStatus != "miss" && h.Outcome != "miss" {
		return result(d)
	}
	t.locateMatches(ctx, h, c, &d, note)
	if len(h.Request) > 0 {
		if err := t.closest(ctx, h, c, most, b, &d, note); err != nil {
			return nil, nil, err
		}
		d.Truncated = b.truncated()
	}
	return result(d)
}

// locateMatches checks whether the request now has an exact recording in its
// own collection or would have matched in another collection.
func (t tools) locateMatches(ctx context.Context, h model.History, c model.Collection, d *diagnosis, note func(string, ...any)) {
	if e, err := t.db.Lookup(ctx, c.ID, h.Key); err == nil && e.Recording.ID != h.RecordingID {
		d.ExactMatchNow = &e.Recording.ID
		note("Recording %d in this collection now has exactly this key (created %s), so this request would replay today.", e.Recording.ID, e.Recording.CreatedAt)
	}
	if len(h.Request) == 0 {
		return
	}
	collections, err := t.db.Collections(ctx)
	if err != nil {
		return
	}
	for _, other := range collections {
		if other.ID == c.ID {
			continue
		}
		key, _, err := match.Key(h.Route, h.Request, other.Exclusions)
		if err != nil {
			continue
		}
		if e, err := t.db.Lookup(ctx, other.ID, key); err == nil {
			d.OtherCollections = append(d.OtherCollections, collectionMatch{CollectionID: other.ID, CollectionName: other.Name, RecordingID: e.Recording.ID})
			note("Collection %q (id %d) has a matching recording %d; the call went to collection %q. Check which collection is active.", other.Name, other.ID, e.Recording.ID, c.Name)
		}
	}
}

// closest lists the nearest recordings and diffs the request against the
// closest one: of the nearest few, the one with the fewest differences that
// affect matching. When a recording now has the request's exact key there is
// nothing to diff.
func (t tools) closest(ctx context.Context, h model.History, c model.Collection, most int, b *budget, d *diagnosis, note func(string, ...any)) error {
	nearest, err := t.db.Nearest(ctx, h.ID)
	if err != nil {
		return err
	}
	if len(nearest) == 0 {
		note("No recording in this collection resembles this request.")
		return nil
	}
	d.Nearest = nearest[:min(len(nearest), nearestShown)]
	if d.ExactMatchNow != nil {
		return nil
	}
	var best *model.NearestCandidate
	var bestRec model.Recording
	fewest := -1
	for i, n := range d.Nearest {
		e, err := t.db.Get(ctx, n.RecordingID)
		if errors.Is(err, store.ErrNotFound) {
			continue
		}
		if err != nil {
			return err
		}
		// Diff without a budget first; only the chosen one is shown.
		v, err := compare(h.Route, h.Request, e.Recording, c.Exclusions, 0, &budget{unlimited: true, cut: map[string]int{}})
		if err != nil {
			note("Could not compare with recording %d: %v.", n.RecordingID, err)
			continue
		}
		if fewest < 0 || v.MatchingDifferences < fewest {
			best, bestRec, fewest = &d.Nearest[i], e.Recording, v.MatchingDifferences
		}
	}
	if best == nil {
		return nil
	}
	cmp, err := compare(h.Route, h.Request, bestRec, c.Exclusions, most, b)
	if err != nil {
		return err
	}
	d.Closest = cmp
	note("Closest recording is %d (%.0f%% similar, %s): %d difference(s) affect matching, %d are excluded.", best.RecordingID, best.Similarity*100, best.Reason, cmp.MatchingDifferences, cmp.ExcludedDifferences)
	if best.RecordingID != d.Nearest[0].RecordingID {
		note("Recording %d ranks first by shared content but differs in more places, so the comparison uses %d.", d.Nearest[0].RecordingID, best.RecordingID)
	}
	if cmp.MatchingDifferences > 0 {
		note("If a differing path is volatile (an id, timestamp or run identifier), a JSON Pointer exclusion would ignore it; exclusions are fixed per collection, so that means a new collection and re-recording.")
	}
	return nil
}
