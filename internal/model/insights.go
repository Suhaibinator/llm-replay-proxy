package model

// The dashboard insight types. docs/dashboard-api.md is the contract and
// web/lib/api.ts mirrors these field for field.

// TokenTotals sums token counts over many responses; each field sums only
// the values providers reported.
type TokenTotals struct {
	Input       int64 `json:"input"`
	CachedInput int64 `json:"cached_input"`
	Output      int64 `json:"output"`
	Reasoning   int64 `json:"reasoning"`
}

// Add accumulates the reported counts of u.
func (t *TokenTotals) Add(u TokenUsage) {
	add := func(dst *int64, v *int64) {
		if v != nil {
			*dst += *v
		}
	}
	add(&t.Input, u.Input)
	add(&t.CachedInput, u.CachedInput)
	add(&t.Output, u.Output)
	add(&t.Reasoning, u.Reasoning)
}

// OutcomeCounts counts history rows by outcome. Errors counts both error and
// incomplete rows.
type OutcomeCounts struct {
	Requests    int64 `json:"requests"`
	Hits        int64 `json:"hits"`
	Misses      int64 `json:"misses"`
	Recorded    int64 `json:"recorded"`
	Interrupted int64 `json:"interrupted"`
	Errors      int64 `json:"errors"`
}

// Count adds one row with the given outcome.
func (c *OutcomeCounts) Count(outcome string) {
	c.Requests++
	switch outcome {
	case "hit":
		c.Hits++
	case "miss":
		c.Misses++
	case "recorded":
		c.Recorded++
	case "interrupted":
		c.Interrupted++
	case "error", "incomplete":
		c.Errors++
	}
}

// HistogramBucket counts samples in (previous bucket's Le, Le]; the last
// bucket has a nil Le and counts every larger sample.
type HistogramBucket struct {
	Le    *int64 `json:"le"`
	Count int64  `json:"count"`
}

type LatencyStats struct {
	Duration            Percentiles       `json:"duration_ms"`
	FirstEvent          Percentiles       `json:"first_event_ms"`
	Histogram           []HistogramBucket `json:"histogram"`
	FirstEventHistogram []HistogramBucket `json:"first_event_histogram"`
}

type InsightBucket struct {
	OutcomeCounts
	Start          string      `json:"start"`
	HitRate        *float64    `json:"hit_rate"`
	UpstreamTokens TokenTotals `json:"upstream_tokens"`
	ReplayedTokens TokenTotals `json:"replayed_tokens"`
}

type ModelInsight struct {
	OutcomeCounts
	Model string `json:"model"`
	// HitRate is defined as InsightTotals.HitRate, over this model's rows.
	HitRate        *float64     `json:"hit_rate"`
	UpstreamTokens TokenTotals  `json:"upstream_tokens"`
	ReplayedTokens TokenTotals  `json:"replayed_tokens"`
	UpstreamCost   *float64     `json:"upstream_cost"`
	SavedCost      *float64     `json:"saved_cost"`
	Upstream       LatencyStats `json:"upstream"`
	Replay         LatencyStats `json:"replay"`
}

// ProviderInsight counts the rows that selected one upstream provider; rows
// recorded without one are attributed to "unknown".
type ProviderInsight struct {
	OutcomeCounts
	Provider string `json:"provider"`
	// HitRate is defined as InsightTotals.HitRate, over this provider's rows.
	HitRate *float64 `json:"hit_rate"`
}

type RouteInsight struct {
	OutcomeCounts
	Route string `json:"route"`
	// HitRate is defined as InsightTotals.HitRate, over this route's rows.
	HitRate *float64 `json:"hit_rate"`
}

type InsightTotals struct {
	OutcomeCounts
	// HitRate is LookupHits / Lookups: calls whose recording lookup found a
	// recording, over all calls that looked one up (Record mode does not).
	HitRate        *float64    `json:"hit_rate"`
	Lookups        int64       `json:"lookups"`
	LookupHits     int64       `json:"lookup_hits"`
	UpstreamTokens TokenTotals `json:"upstream_tokens"`
	ReplayedTokens TokenTotals `json:"replayed_tokens"`
	UpstreamCost   *float64    `json:"upstream_cost"`
	SavedCost      *float64    `json:"saved_cost"`
	// Threads counts distinct conversation threads seen in the range.
	Threads int64 `json:"threads"`
}

type TopRecording struct {
	RecordingID    int64       `json:"recording_id"`
	Preview        string      `json:"preview"`
	Model          string      `json:"model"`
	Hits           int64       `json:"hits"`
	ReplayedTokens TokenTotals `json:"replayed_tokens"`
	SavedCost      *float64    `json:"saved_cost"`
}

type Insights struct {
	From      string            `json:"from"`
	To        string            `json:"to"`
	Bucket    string            `json:"bucket"`
	Totals    InsightTotals     `json:"totals"`
	Series    []InsightBucket   `json:"series"`
	Models    []ModelInsight    `json:"models"`
	Routes    []RouteInsight    `json:"routes"`
	Providers []ProviderInsight `json:"providers"`
	Latency   struct {
		Upstream LatencyStats `json:"upstream"`
		Replay   LatencyStats `json:"replay"`
	} `json:"latency"`
	TopRecordings []TopRecording  `json:"top_recordings"`
	TopThreads    []ThreadSummary `json:"top_threads"`
}

// ThreadSummary aggregates the history rows of one conversation thread.
type ThreadSummary struct {
	OutcomeCounts
	Thread string `json:"thread"`
	Route  string `json:"route"`
	Model  string `json:"model"`
	// Opening is the start of the first user message; Latest of the latest.
	Opening        string      `json:"opening"`
	Latest         string      `json:"latest"`
	MaxItems       int         `json:"max_items"`
	FirstAt        string      `json:"first_at"`
	LastAt         string      `json:"last_at"`
	LastOutcome    string      `json:"last_outcome"`
	UpstreamTokens TokenTotals `json:"upstream_tokens"`
}

type ThreadTurn struct {
	HistoryID     int64            `json:"history_id"`
	CreatedAt     string           `json:"created_at"`
	Outcome       string           `json:"outcome"`
	LookupOutcome string           `json:"lookup_outcome"`
	Detail        string           `json:"detail"`
	RecordingID   int64            `json:"recording_id"`
	Route         string           `json:"route"`
	Source        string           `json:"source"`
	Items         int              `json:"items"`
	Preview       string           `json:"preview"`
	DurationMS    *int64           `json:"duration_ms"`
	FirstEventMS  *int64           `json:"first_event_ms"`
	Response      *ResponseSummary `json:"response"`
}

type ThreadDetail struct {
	ThreadSummary
	Turns []ThreadTurn `json:"turns"`
}

type NearestCandidate struct {
	RecordingID int64   `json:"recording_id"`
	Similarity  float64 `json:"similarity"`
	Reason      string  `json:"reason"`
	Preview     string  `json:"preview"`
	Model       string  `json:"model"`
	Items       int     `json:"items"`
	// Thread is the recording's request thread; "" if none.
	Thread string `json:"thread"`
	// CreatedAt is when the recording's active revision was created.
	CreatedAt string `json:"created_at"`
}
