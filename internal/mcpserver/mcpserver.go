// Package mcpserver exposes the recordings and history as read-only Model
// Context Protocol tools, so an LLM client can inspect them with bodies
// reassembled and decompressed.
package mcpserver

import (
	"context"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"fmt"
	"net/http"
	"runtime/debug"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/local/llm-replay-proxy/internal/model"
	"github.com/local/llm-replay-proxy/internal/protocol"
	"github.com/local/llm-replay-proxy/internal/store"
)

// defaultMaxBytes bounds the large strings one tool result carries unless the
// caller asks for more.
const defaultMaxBytes = 200_000

const instructions = `Read-only access to an LLM inference record/replay proxy.
Collections hold recordings (a request's matching key plus immutable response revisions, one active).
History logs every inference call with its outcome (hit, miss, recorded, error, ...).
Start with list_collections, then list_recordings or list_history. Request and response bodies are
returned exactly as stored, decompressed; streaming responses are SSE frames with millisecond offsets.
To debug a replay failure, list_history with outcomes ["miss","error","incomplete","interrupted"],
then diagnose_replay on a call; compare_requests diffs any call against any recording.
Large strings are cut to max_bytes (default 200000; 0 = unlimited) and listed under "truncated";
max_value_bytes caps each string on its own, and include_request=false leaves the request out.`

// New returns the streamable HTTP handler serving the MCP tools. Requests are
// stateless; cross-origin browser requests are refused.
func New(db *store.Store) http.Handler {
	server := mcp.NewServer(&mcp.Implementation{Name: "llm-replay-proxy", Version: version()}, &mcp.ServerOptions{Instructions: instructions})
	t := tools{db: db}
	readOnly := &mcp.ToolAnnotations{ReadOnlyHint: true, IdempotentHint: true, OpenWorldHint: new(false)}
	add := func(name, description string) *mcp.Tool {
		return &mcp.Tool{Name: name, Description: description, Annotations: readOnly}
	}
	mcp.AddTool(server, add("list_collections", "List collections with their matching exclusions, and the proxy settings (mode, active collection, replay timing)."), t.listCollections)
	mcp.AddTool(server, add("list_recordings", "List a collection's recordings, newest first, with request and response summaries (model, preview, token usage) and replay hit counts."), t.listRecordings)
	mcp.AddTool(server, add("get_recording", "Get one recording: the exact request, the active (or chosen) revision's response body or SSE frames, the reconstructed assistant text, a response summary, and the revision list."), t.getRecording)
	mcp.AddTool(server, add("list_history", "List a collection's inference calls, newest first, with outcome (hit, miss, recorded, error, incomplete, interrupted), lookup outcome, provider, timing and request summary. Filter by outcomes to find replay failures; page back with before_id = next_before_id."), t.listHistory)
	mcp.AddTool(server, add("get_history_item", "Get one inference call: its exact request and, when a recording served or recorded it, that response."), t.getHistoryItem)
	mcp.AddTool(server, add("list_threads", "List a collection's conversation threads (calls sharing an opening), most recent first."), t.listThreads)
	mcp.AddTool(server, add("get_thread", "Get a thread's turns in order, with outcomes and response summaries."), t.getThread)
	mcp.AddTool(server, add("diagnose_replay", "Explain why one history call hit, missed or failed: findings in plain language, whether the request matches a recording now or in another collection, the nearest recordings, and the JSON differences from the closest one (key-changing first). Start here when debugging a replay failure."), t.diagnoseReplay)
	mcp.AddTool(server, add("compare_requests", "Diff a history call's request (or a given request body) against a recording's request by JSON Pointer path, marking which differences the collection's exclusions ignore."), t.compareRequests)
	mcp.AddTool(server, add("find_nearest_recordings", "Find the recordings closest to a history call's request, by shared content and thread."), t.findNearest)
	mcp.AddTool(server, add("get_insights", "Aggregate a collection's history over a time range: outcomes, hit rate, tokens, latency, and per-model and per-provider breakdowns."), t.getInsights)
	h := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return server }, &mcp.StreamableHTTPOptions{Stateless: true, JSONResponse: true})
	return http.NewCrossOriginProtection().Handler(h)
}

func version() string {
	if info, ok := debug.ReadBuildInfo(); ok && info.Main.Version != "" {
		return info.Main.Version
	}
	return "devel"
}

type tools struct{ db *store.Store }

// result returns v as indented JSON text.
func result(v any) (*mcp.CallToolResult, any, error) {
	b, err := json.Marshal(v, jsontext.WithIndent("  "))
	if err != nil {
		return nil, nil, err
	}
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: string(b)}}}, nil, nil
}

// storeError makes store errors readable tool errors.
func storeError(what string, err error) error {
	if errors.Is(err, store.ErrNotFound) {
		return fmt.Errorf("%s not found", what)
	}
	return err
}

func (t tools) collection(ctx context.Context, id int64) error {
	_, err := t.db.Collection(ctx, id)
	return storeError(fmt.Sprintf("collection %d", id), err)
}

// budget cuts large strings to a per-value cap and to what is left of a
// result's byte allowance, and records what it cut, with the full length.
type budget struct {
	left      int
	unlimited bool
	// perValue caps each string on its own; 0 means no cap.
	perValue int
	cut      map[string]int
}

func newBudget(maxBytes *int) *budget {
	b := &budget{left: defaultMaxBytes, cut: map[string]int{}}
	if maxBytes != nil {
		b.left = max(*maxBytes, 0)
		b.unlimited = *maxBytes == 0
	}
	return b
}

// withValueCap sets the per-value cap, def when maxValueBytes is nil.
func (b *budget) withValueCap(maxValueBytes *int, def int) (*budget, error) {
	v := orDefault(maxValueBytes, def)
	if v < 0 {
		return nil, errors.New("max_value_bytes must not be negative")
	}
	b.perValue = v
	return b, nil
}

func (b *budget) take(name, s string) string {
	n := len(s)
	if b.perValue > 0 {
		n = min(n, b.perValue)
	}
	if !b.unlimited {
		n = min(n, b.left)
		b.left -= n
	}
	if n == len(s) {
		return s
	}
	b.cut[name] = len(s)
	return s[:runeStart(s, n)]
}

// runeStart moves n back to the start of the rune it falls in.
func runeStart(s string, n int) int {
	for n > 0 && n < len(s) && !utf8.RuneStart(s[n]) {
		n--
	}
	return n
}

func (b *budget) truncated() map[string]int {
	if len(b.cut) == 0 {
		return nil
	}
	return b.cut
}

type event struct {
	OffsetMS int64  `json:"offset_ms"`
	Data     string `json:"data"`
}

// response is a revision's decoded response and what it reconstructs to.
type response struct {
	RevisionID int64             `json:"revision_id"`
	Status     int               `json:"status"`
	Source     string            `json:"source"`
	CreatedAt  string            `json:"created_at"`
	Headers    map[string]string `json:"headers"`
	// Text is the reconstructed assistant output; TextUnavailable says why
	// there is none.
	Text            string                `json:"text,omitempty"`
	TextUnavailable string                `json:"text_unavailable_reason,omitempty"`
	Summary         model.ResponseSummary `json:"summary"`
	Body            string                `json:"body,omitempty"`
	EventCount      int                   `json:"event_count,omitzero"`
	Events          []event               `json:"events,omitempty"`
	// EventsOmitted counts frames left out by include_events or max_bytes.
	EventsOmitted int `json:"events_omitted,omitzero"`
}

// newResponse describes rev with its reconstructed text; addPayload then adds
// the body and frames. Callers spend the budget on the request in between, so
// the text and request survive a long stream.
func newResponse(route string, streaming bool, rev model.Revision, b *budget) *response {
	text, reason := protocol.Text(route, streaming, rev)
	return &response{
		RevisionID: rev.ID, Status: rev.Status, Source: rev.Source, CreatedAt: rev.CreatedAt, Headers: rev.Headers,
		Text: b.take("text", text), TextUnavailable: reason,
		Summary:    protocol.SummarizeResponse(route, streaming, rev),
		EventCount: len(rev.Events),
	}
}

func (r *response) addPayload(rev model.Revision, includeEvents bool, b *budget) {
	r.Body = b.take("body", rev.Body)
	if !includeEvents {
		r.EventsOmitted = len(rev.Events)
		return
	}
	for i, e := range rev.Events {
		if !b.unlimited && b.left == 0 {
			r.EventsOmitted = len(rev.Events) - i
			b.cut["events"] = len(rev.Events)
			return
		}
		r.Events = append(r.Events, event{OffsetMS: e.OffsetMS, Data: b.take(fmt.Sprintf("events[%d]", i), e.Data)})
	}
}

func orDefault[T any](p *T, v T) T {
	if p == nil {
		return v
	}
	return *p
}

type noInput struct{}

func (t tools) listCollections(ctx context.Context, _ *mcp.CallToolRequest, _ noInput) (*mcp.CallToolResult, any, error) {
	collections, err := t.db.Collections(ctx)
	if err != nil {
		return nil, nil, err
	}
	settings, err := t.db.Settings(ctx)
	if err != nil {
		return nil, nil, err
	}
	return result(map[string]any{"collections": collections, "settings": settings})
}

type listRecordingsInput struct {
	CollectionID int64  `json:"collection_id"`
	Query        string `json:"query,omitempty" jsonschema:"case-insensitive substring of the route, key, model, or the first or last user text"`
	Limit        int    `json:"limit,omitempty" jsonschema:"recordings to return, 1-500 (default 50)"`
	Offset       int    `json:"offset,omitempty" jsonschema:"recordings to skip"`
}

func (t tools) listRecordings(ctx context.Context, _ *mcp.CallToolRequest, in listRecordingsInput) (*mcp.CallToolResult, any, error) {
	if err := t.collection(ctx, in.CollectionID); err != nil {
		return nil, nil, err
	}
	limit, err := limitOf(in.Limit, 50, 500)
	if err != nil {
		return nil, nil, err
	}
	if in.Offset < 0 {
		return nil, nil, errors.New("offset must not be negative")
	}
	all, err := t.db.Recordings(ctx, in.CollectionID)
	if err != nil {
		return nil, nil, err
	}
	matched := all[:0]
	q := strings.ToLower(in.Query)
	for _, r := range all {
		if q == "" || matchesRecording(r, q) {
			matched = append(matched, r)
		}
	}
	page := matched[min(in.Offset, len(matched)):min(in.Offset+limit, len(matched))]
	return result(map[string]any{"total": len(matched), "offset": in.Offset, "recordings": page})
}

func matchesRecording(r model.RecordingSummary, q string) bool {
	fields := []string{r.Route, r.Key}
	if s := r.Summary; s != nil {
		fields = append(fields, s.Model, s.Preview, s.Opening)
	}
	for _, f := range fields {
		if strings.Contains(strings.ToLower(f), q) {
			return true
		}
	}
	return false
}

func limitOf(v, def, most int) (int, error) {
	if v == 0 {
		return def, nil
	}
	if v < 1 || v > most {
		return 0, fmt.Errorf("limit must be between 1 and %d", most)
	}
	return v, nil
}

type getRecordingInput struct {
	RecordingID          int64 `json:"recording_id"`
	RevisionID           int64 `json:"revision_id,omitempty" jsonschema:"a revision of this recording (default: the active one)"`
	IncludeEvents        *bool `json:"include_events,omitempty" jsonschema:"include SSE frames of streaming responses (default true)"`
	IncludeRequest       *bool `json:"include_request,omitempty" jsonschema:"include the request (default true)"`
	IncludeMatchingInput bool  `json:"include_matching_input,omitempty" jsonschema:"include the canonical matching input hashed into the key"`
	MaxBytes             *int  `json:"max_bytes,omitempty" jsonschema:"byte allowance for request, text, body and frames (default 200000; 0 = unlimited)"`
	MaxValueBytes        *int  `json:"max_value_bytes,omitempty" jsonschema:"bytes kept of each string (request, text, body, each frame) so one large value cannot use up max_bytes (default 0 = no per-value cap)"`
}

func (t tools) getRecording(ctx context.Context, _ *mcp.CallToolRequest, in getRecordingInput) (*mcp.CallToolResult, any, error) {
	what := fmt.Sprintf("recording %d", in.RecordingID)
	e, err := t.db.Get(ctx, in.RecordingID)
	if err != nil {
		return nil, nil, storeError(what, err)
	}
	rev := e.Revision
	if in.RevisionID != 0 && in.RevisionID != rev.ID {
		if rev, err = t.db.RevisionDetail(ctx, in.RecordingID, in.RevisionID); err != nil {
			return nil, nil, storeError(fmt.Sprintf("revision %d of %s", in.RevisionID, what), err)
		}
	}
	revisions, err := t.db.RevisionSummaries(ctx, in.RecordingID)
	if err != nil {
		return nil, nil, err
	}
	b, err := newBudget(in.MaxBytes).withValueCap(in.MaxValueBytes, 0)
	if err != nil {
		return nil, nil, err
	}
	out := struct {
		Recording model.Recording `json:"recording"`
		Active    bool            `json:"revision_is_active"`
		// ValidationError says why the response would not replay cleanly
		// (failed, malformed or incomplete); empty when it is valid.
		ValidationError string                  `json:"validation_error,omitempty"`
		Request         string                  `json:"request,omitempty"`
		MatchingInput   string                  `json:"matching_input,omitempty"`
		Response        *response               `json:"response"`
		Revisions       []model.RevisionSummary `json:"revisions"`
		Truncated       map[string]int          `json:"truncated,omitempty"`
	}{Recording: e.Recording, Active: rev.ID == e.Recording.ActiveRevisionID, Revisions: revisions}
	r := e.Recording
	if err := protocol.Validate(r.Route, r.Streaming, rev); err != nil {
		out.ValidationError = err.Error()
	}
	out.Response = newResponse(r.Route, r.Streaming, rev, b)
	if orDefault(in.IncludeRequest, true) {
		out.Request = b.take("request", string(rev.Request))
	}
	out.Response.addPayload(rev, orDefault(in.IncludeEvents, true), b)
	if in.IncludeMatchingInput {
		out.MatchingInput = b.take("matching_input", string(rev.MatchingInput))
	}
	out.Truncated = b.truncated()
	return result(out)
}

type listHistoryInput struct {
	CollectionID int64    `json:"collection_id"`
	BeforeID     int64    `json:"before_id,omitempty" jsonschema:"only calls older than this history id (for paging back)"`
	AfterID      int64    `json:"after_id,omitempty" jsonschema:"only calls newer than this history id"`
	Provider     string   `json:"provider,omitempty" jsonschema:"only calls that selected this provider (\"unknown\" for none)"`
	Outcomes     []string `json:"outcomes,omitempty" jsonschema:"only calls with one of these outcomes: hit, miss, recorded, error, incomplete, interrupted (not with after_id)"`
	Limit        int      `json:"limit,omitempty" jsonschema:"calls to return, 1-1000 (default 50)"`
}

func (t tools) listHistory(ctx context.Context, _ *mcp.CallToolRequest, in listHistoryInput) (*mcp.CallToolResult, any, error) {
	if err := t.collection(ctx, in.CollectionID); err != nil {
		return nil, nil, err
	}
	limit, err := limitOf(in.Limit, 50, 1000)
	if err != nil {
		return nil, nil, err
	}
	var rows []model.History
	switch {
	case in.BeforeID != 0 && in.AfterID != 0:
		return nil, nil, errors.New("use before_id or after_id, not both")
	case in.AfterID != 0 && len(in.Outcomes) > 0:
		return nil, nil, errors.New("outcomes cannot be combined with after_id; page with before_id")
	case in.AfterID != 0:
		rows, err = t.db.HistoryAfter(ctx, in.CollectionID, in.AfterID, limit, in.Provider)
	default:
		rows, err = t.db.HistoryBefore(ctx, in.CollectionID, in.BeforeID, limit, in.Provider, in.Outcomes)
	}
	if err != nil {
		return nil, nil, err
	}
	out := map[string]any{"history": rows}
	if len(rows) == limit {
		out["next_before_id"] = rows[len(rows)-1].ID
	}
	return result(out)
}

type getHistoryItemInput struct {
	HistoryID       int64 `json:"history_id"`
	IncludeRequest  *bool `json:"include_request,omitempty" jsonschema:"include the request text (default true)"`
	IncludeResponse *bool `json:"include_response,omitempty" jsonschema:"include the response served or recorded (default true)"`
	IncludeEvents   *bool `json:"include_events,omitempty" jsonschema:"include SSE frames of a streaming response (default true)"`
	MaxBytes        *int  `json:"max_bytes,omitempty" jsonschema:"byte allowance for request, text, body and frames (default 200000; 0 = unlimited)"`
	MaxValueBytes   *int  `json:"max_value_bytes,omitempty" jsonschema:"bytes kept of each string (request, text, body, each frame) so one large value cannot use up max_bytes (default 0 = no per-value cap)"`
}

func (t tools) getHistoryItem(ctx context.Context, _ *mcp.CallToolRequest, in getHistoryItemInput) (*mcp.CallToolResult, any, error) {
	h, err := t.db.HistoryItem(ctx, in.HistoryID)
	if err != nil {
		return nil, nil, storeError(fmt.Sprintf("history item %d", in.HistoryID), err)
	}
	b, err := newBudget(in.MaxBytes).withValueCap(in.MaxValueBytes, 0)
	if err != nil {
		return nil, nil, err
	}
	out := struct {
		model.History
		RevisionID          int64          `json:"revision_id"`
		Response            *response      `json:"response,omitempty"`
		ResponseUnavailable string         `json:"response_unavailable_reason,omitempty"`
		RequestText         string         `json:"request_text,omitempty"`
		Truncated           map[string]int `json:"truncated,omitempty"`
	}{History: h, RevisionID: h.RevisionID}
	var rev model.Revision
	switch {
	case !orDefault(in.IncludeResponse, true):
	case h.RevisionID == 0:
		out.ResponseUnavailable = "no recorded response was served or recorded for this call"
	default:
		e, err := t.db.Get(ctx, h.RecordingID)
		if err == nil {
			rev, err = t.db.Revision(ctx, h.RecordingID, h.RevisionID)
		}
		switch {
		case errors.Is(err, store.ErrNotFound):
			out.ResponseUnavailable = "the recording or revision has since been deleted"
		case err != nil:
			return nil, nil, err
		default:
			out.Response = newResponse(e.Recording.Route, e.Recording.Streaming, rev, b)
		}
	}
	if orDefault(in.IncludeRequest, true) {
		out.RequestText = b.take("request_text", string(h.Request))
	}
	if out.Response != nil {
		out.Response.addPayload(rev, orDefault(in.IncludeEvents, true), b)
	}
	out.Truncated = b.truncated()
	return result(out)
}

type listThreadsInput struct {
	CollectionID int64  `json:"collection_id"`
	Limit        int    `json:"limit,omitempty" jsonschema:"threads to return, 1-500 (default 50)"`
	From         string `json:"from,omitempty" jsonschema:"RFC 3339 start time"`
	To           string `json:"to,omitempty" jsonschema:"RFC 3339 end time"`
}

func (t tools) listThreads(ctx context.Context, _ *mcp.CallToolRequest, in listThreadsInput) (*mcp.CallToolResult, any, error) {
	if err := t.collection(ctx, in.CollectionID); err != nil {
		return nil, nil, err
	}
	limit, err := limitOf(in.Limit, 50, 500)
	if err != nil {
		return nil, nil, err
	}
	var from, to time.Time
	if err := parseTime("from", in.From, &from); err != nil {
		return nil, nil, err
	}
	if err := parseTime("to", in.To, &to); err != nil {
		return nil, nil, err
	}
	if !from.IsZero() && !to.IsZero() && !from.Before(to) {
		return nil, nil, errors.New("from must be before to")
	}
	v, err := t.db.Threads(ctx, in.CollectionID, limit, from, to)
	if err != nil {
		return nil, nil, err
	}
	return result(map[string]any{"threads": v})
}

type getThreadInput struct {
	CollectionID int64  `json:"collection_id"`
	Thread       string `json:"thread" jsonschema:"the thread fingerprint: 16 lowercase hex digits"`
}

func (t tools) getThread(ctx context.Context, _ *mcp.CallToolRequest, in getThreadInput) (*mcp.CallToolResult, any, error) {
	if !validThread(in.Thread) {
		return nil, nil, errors.New("thread must be 16 lowercase hex digits")
	}
	v, err := t.db.Thread(ctx, in.CollectionID, in.Thread)
	if err != nil {
		return nil, nil, storeError("thread "+in.Thread, err)
	}
	return result(v)
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

type findNearestInput struct {
	HistoryID int64 `json:"history_id"`
}

func (t tools) findNearest(ctx context.Context, _ *mcp.CallToolRequest, in findNearestInput) (*mcp.CallToolResult, any, error) {
	v, err := t.db.Nearest(ctx, in.HistoryID)
	if err != nil {
		return nil, nil, storeError(fmt.Sprintf("history item %d", in.HistoryID), err)
	}
	return result(map[string]any{"candidates": v})
}

type getInsightsInput struct {
	CollectionID int64  `json:"collection_id"`
	From         string `json:"from,omitempty" jsonschema:"RFC 3339 start time (default: 7 days before to)"`
	To           string `json:"to,omitempty" jsonschema:"RFC 3339 end time (default: now)"`
	Model        string `json:"model,omitempty" jsonschema:"only calls for this model"`
	Provider     string `json:"provider,omitempty" jsonschema:"only calls that selected this provider"`
}

func (t tools) getInsights(ctx context.Context, _ *mcp.CallToolRequest, in getInsightsInput) (*mcp.CallToolResult, any, error) {
	if err := t.collection(ctx, in.CollectionID); err != nil {
		return nil, nil, err
	}
	to := time.Now().UTC()
	if err := parseTime("to", in.To, &to); err != nil {
		return nil, nil, err
	}
	from := to.Add(-7 * 24 * time.Hour)
	if err := parseTime("from", in.From, &from); err != nil {
		return nil, nil, err
	}
	if !from.Before(to) {
		return nil, nil, errors.New("from must be before to")
	}
	if to.Sub(from) > 366*24*time.Hour {
		return nil, nil, errors.New("the range cannot exceed 366 days")
	}
	v, err := t.db.Insights(ctx, in.CollectionID, from, to, store.InsightFilter{Model: in.Model, Provider: in.Provider})
	if err != nil {
		return nil, nil, err
	}
	return result(v)
}

func parseTime(name, raw string, dst *time.Time) error {
	if raw == "" {
		return nil
	}
	v, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		return fmt.Errorf("%s must be RFC 3339, like 2026-01-02T15:04:05Z", name)
	}
	*dst = v.UTC()
	return nil
}
