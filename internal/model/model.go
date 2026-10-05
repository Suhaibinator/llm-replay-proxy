package model

import "encoding/json"

type Collection struct {
	ID         int64    `json:"id"`
	Name       string   `json:"name"`
	Exclusions []string `json:"exclusions"`
	CreatedAt  string   `json:"created_at"`
}
type Event struct {
	Data     string `json:"data"`
	OffsetMS int64  `json:"offset_ms"`
}
type Recording struct {
	ID           int64  `json:"id"`
	CollectionID int64  `json:"collection_id"`
	Key          string `json:"key"`
	Route        string `json:"route"`
	// Request and MatchingInput are never serialized: request bytes are served
	// once, as exact text, by the endpoints that need them.
	Request          json.RawMessage `json:"-"`
	MatchingInput    json.RawMessage `json:"-"`
	UpstreamIdentity string          `json:"upstream_identity"`
	Streaming        bool            `json:"streaming"`
	ActiveRevisionID int64           `json:"active_revision_id"`
	CreatedAt        string          `json:"created_at"`
}
type Revision struct {
	ID            int64             `json:"id"`
	RecordingID   int64             `json:"recording_id"`
	Status        int               `json:"status"`
	Headers       map[string]string `json:"headers"`
	Body          string            `json:"body"`
	Events        []Event           `json:"events"`
	Request       json.RawMessage   `json:"-"`
	MatchingInput json.RawMessage   `json:"-"`
	Source        string            `json:"source"`
	CreatedAt     string            `json:"created_at"`
}

// RequestSummary describes a request body for lists and search without
// sending the body itself.
type RequestSummary struct {
	Model string `json:"model"`
	// Items counts conversation entries: messages, or Responses input items.
	Items int `json:"items"`
	// Preview is the start of the last user-written text.
	Preview   string `json:"preview"`
	ToolCalls int    `json:"tool_calls"`
	Images    int    `json:"images"`
	Bytes     int64  `json:"bytes"`
	// Thread fingerprints the conversation's opening (route, instructions or
	// system prompt, and first user message), so a thread's turns share it.
	Thread string `json:"thread"`
}

// RecordingSummary is a recording as listed: its active request summarized,
// plus revision and replay counts.
type RecordingSummary struct {
	Recording
	Summary   *RequestSummary `json:"request"`
	UpdatedAt string          `json:"updated_at"`
	Source    string          `json:"source"`
	Revisions int             `json:"revisions"`
	Hits      int64           `json:"hits"`
	LastHitAt string          `json:"last_hit_at"`
}

type Entry struct {
	Recording Recording `json:"recording"`
	Revision  Revision  `json:"revision"`
}
type History struct {
	ID           int64           `json:"id"`
	CollectionID int64           `json:"collection_id"`
	Route        string          `json:"route"`
	Key          string          `json:"key"`
	Request      json.RawMessage `json:"-"`
	// Summary describes Request; nil when the call had no request body.
	Summary      *RequestSummary `json:"request"`
	Outcome      string          `json:"outcome"`
	Detail       string          `json:"detail"`
	RecordingID  int64           `json:"recording_id"`
	Source       string          `json:"source"`
	CacheStatus  string          `json:"lookup_outcome"`
	DurationMS   *int64          `json:"duration_ms"`
	FirstEventMS *int64          `json:"first_event_ms"`
	CreatedAt    string          `json:"created_at"`
}
type AnalyticsPoint struct {
	Start  string `json:"start"`
	Total  int64  `json:"total"`
	Hits   int64  `json:"hits"`
	Misses int64  `json:"misses"`
	Errors int64  `json:"errors"`
}
type Percentiles struct {
	P50     *int64 `json:"p50"`
	P95     *int64 `json:"p95"`
	P99     *int64 `json:"p99"`
	Samples int    `json:"samples"`
}
type SourceAnalytics struct {
	Total      int64       `json:"total"`
	Duration   Percentiles `json:"duration_ms"`
	FirstEvent Percentiles `json:"first_event_ms"`
}
type Analytics struct {
	Total         int64                      `json:"total"`
	LifetimeTotal int64                      `json:"lifetime_total"`
	Hits          int64                      `json:"hits"`
	Misses        int64                      `json:"misses"`
	Errors        int64                      `json:"errors"`
	Recorded      int64                      `json:"recorded"`
	HitRate       *float64                   `json:"hit_rate"`
	Sources       map[string]SourceAnalytics `json:"sources"`
	Series        []AnalyticsPoint           `json:"series"`
}
type Settings struct {
	Mode               string  `json:"mode"`
	ActiveCollectionID int64   `json:"active_collection_id"`
	FirstEventDelayMS  int64   `json:"first_event_delay_ms"`
	DelayMultiplier    float64 `json:"delay_multiplier"`
	// HistoryLimit is the number of history rows kept per collection; 0 keeps
	// every row.
	HistoryLimit int64 `json:"history_limit"`
}
