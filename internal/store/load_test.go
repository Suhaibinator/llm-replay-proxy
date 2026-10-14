package store

import (
	"context"
	"encoding/base64"
	"encoding/json/v2"
	"fmt"
	"math/rand/v2"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	matching "github.com/local/llm-replay-proxy/internal/match"
	"github.com/local/llm-replay-proxy/internal/model"
	"github.com/local/llm-replay-proxy/internal/protocol"
)

// TestLoadConcurrentProxyAndAdmin replays the database traffic of a busy
// proxy: recorders run the record path (settings, collection, lookup miss,
// publish, history) and hitters the replay path (settings, collection,
// lookup hit, history) with megabyte requests carrying inline images and
// thousand-event streams, while admin readers time the history and insights
// endpoints. It is a benchmark, not a check, so it runs only when
// REPLAY_LOADTEST=1. Knobs (environment):
//
//	LOAD_DIR       directory for the database; use a real disk, not tmpfs
//	LOAD_DURATION  how long traffic runs (default 20s)
//	LOAD_RECORDERS concurrent record-path callers (default 20)
//	LOAD_HITTERS   concurrent replay-path callers (default 20)
//
// To model a slow-fsync disk, run the test binary under
// strace -f --seccomp-bpf -e trace=fsync,fdatasync -e inject=fsync,fdatasync:delay_exit=10000
func TestLoadConcurrentProxyAndAdmin(t *testing.T) {
	if os.Getenv("REPLAY_LOADTEST") != "1" {
		t.Skip("set REPLAY_LOADTEST=1 to run the load test")
	}
	envInt := func(name string, def int) int {
		if v, err := strconv.Atoi(os.Getenv(name)); err == nil {
			return v
		}
		return def
	}
	duration := 20 * time.Second
	if v, err := time.ParseDuration(os.Getenv("LOAD_DURATION")); err == nil {
		duration = v
	}
	recorders, hitters := envInt("LOAD_RECORDERS", 20), envInt("LOAD_HITTERS", 20)
	// LOAD_MAX_BATCH=1 turns group commit off; LOAD_RELAXED_PUBLISH=1
	// commits publications without their own fsync.
	defer func(batch int, durable bool) { maxBatch, durablePublish = batch, durable }(maxBatch, durablePublish)
	maxBatch = envInt("LOAD_MAX_BATCH", maxBatch)
	durablePublish = os.Getenv("LOAD_RELAXED_PUBLISH") != "1"
	t.Logf("max batch %d, durable publish %v", maxBatch, durablePublish)
	dir := os.Getenv("LOAD_DIR")
	if dir == "" {
		dir = t.TempDir()
	} else {
		var err error
		if dir, err = os.MkdirTemp(dir, "replay-load-"); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { os.RemoveAll(dir) })
	}
	s, err := Open(filepath.Join(dir, "load.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	settings, err := s.Settings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	settings.Mode = "auto"
	if err = s.SetSettings(ctx, settings); err != nil {
		t.Fatal(err)
	}
	cid := settings.ActiveCollectionID

	stream := loadStream(1000)
	if err = protocol.Validate(chatRoute, true, stream); err != nil {
		t.Fatal(err)
	}

	// Seed recordings for the hitters and some older history to page through.
	seedStart := time.Now()
	var seeded [][]byte
	for i := range 40 {
		c := newLoadConversation(uint64(1000 + i))
		for range 3 {
			c.turn()
		}
		body := c.body()
		seeded = append(seeded, body)
		if err = loadPublish(ctx, s, cid, body, stream); err != nil {
			t.Fatal(err)
		}
	}
	for i := range 2000 {
		body := seeded[i%len(seeded)]
		key, _, _ := matching.Key(chatRoute, body, nil)
		if err = s.AddHistory(ctx, model.History{CollectionID: cid, Route: chatRoute, Key: key, Request: body, Outcome: "hit", CacheStatus: "hit", Source: "replay"}); err != nil {
			t.Fatal(err)
		}
	}
	t.Logf("seeded 40 recordings and 2000 history rows in %v", time.Since(seedStart).Round(time.Millisecond))

	var stats loadStats
	run, stop := context.WithTimeout(ctx, duration)
	defer stop()
	var wg sync.WaitGroup
	for w := range recorders {
		wg.Go(func() {
			c := newLoadConversation(uint64(w))
			for run.Err() == nil {
				if len(c.messages) > 40 || c.size() > 2<<20 {
					c = newLoadConversation(rand.Uint64())
				}
				c.turn()
				body := c.body()
				started := time.Now()
				entry, found, lookup := loadLookup(ctx, s, body)
				if found {
					continue
				}
				published := time.Now()
				e, err := s.Publish(ctx, entry.Recording, stream)
				if err != nil {
					stats.errors.Add(1)
					t.Logf("publish: %v", err)
					continue
				}
				historyStart := time.Now()
				err = s.AddHistory(ctx, model.History{CollectionID: cid, Route: chatRoute, Key: entry.Recording.Key, Request: body, Outcome: "recorded", CacheStatus: "miss", Source: "upstream", RecordingID: e.Recording.ID, RevisionID: e.Revision.ID})
				if err != nil {
					stats.errors.Add(1)
					t.Logf("history: %v", err)
				}
				done := time.Now()
				stats.add(&stats.recordLookup, lookup)
				stats.add(&stats.publish, historyStart.Sub(published))
				stats.add(&stats.recordHistory, done.Sub(historyStart))
				stats.add(&stats.recordTotal, done.Sub(started))
				stats.records.Add(1)
				stats.commits.Add(2)
			}
		})
	}
	for w := range hitters {
		wg.Go(func() {
			r := rand.New(rand.NewPCG(uint64(w), 7))
			for run.Err() == nil {
				body := seeded[r.IntN(len(seeded))]
				started := time.Now()
				entry, found, lookup := loadLookup(ctx, s, body)
				if !found {
					stats.errors.Add(1)
					continue
				}
				historyStart := time.Now()
				if err := s.AddHistory(ctx, model.History{CollectionID: cid, Route: chatRoute, Key: entry.Recording.Key, Request: body, Outcome: "hit", CacheStatus: "hit", Source: "replay", RecordingID: entry.Recording.ID, RevisionID: entry.Revision.ID}); err != nil {
					stats.errors.Add(1)
				}
				done := time.Now()
				stats.add(&stats.hitLookup, lookup)
				stats.add(&stats.hitHistory, done.Sub(historyStart))
				stats.add(&stats.hitTotal, done.Sub(started))
				stats.hits.Add(1)
				stats.commits.Add(1)
				// A replayed stream takes time to send; keep hitters from
				// becoming a pure history-insert loop.
				time.Sleep(20 * time.Millisecond)
			}
		})
	}
	admin := []struct {
		name string
		call func(context.Context) error
		lat  *[]time.Duration
	}{
		{"history limit=5", func(c context.Context) error { _, err := s.HistoryBefore(c, cid, 0, 5, "", nil); return err }, &stats.list5},
		{"history limit=200", func(c context.Context) error { _, err := s.HistoryBefore(c, cid, 0, 200, "", nil); return err }, &stats.list200},
		{"history after", func(c context.Context) error { _, err := s.HistoryAfter(c, cid, 0, 100, ""); return err }, &stats.after},
		{"insights 24h", func(c context.Context) error {
			_, err := s.Insights(c, cid, time.Now().Add(-24*time.Hour), time.Now().Add(time.Minute), InsightFilter{})
			return err
		}, &stats.insights},
	}
	for _, a := range admin {
		wg.Go(func() {
			for run.Err() == nil {
				c, cancel := context.WithTimeout(ctx, 2*time.Minute)
				started := time.Now()
				err := a.call(c)
				cancel()
				if err != nil {
					stats.errors.Add(1)
					t.Logf("%s: %v", a.name, err)
				}
				stats.add(a.lat, time.Since(started))
				time.Sleep(250 * time.Millisecond)
			}
		})
	}
	commits0, writes0, busy0 := s.w.commits.Load(), s.w.writes.Load(), s.w.busy.Load()
	wg.Wait()
	elapsed := duration.Seconds()
	if commits := s.w.commits.Load() - commits0; commits > 0 {
		t.Logf("writer: %.1f transactions/s holding %.1f writes each, %v per transaction, busy %.0f%% of the time",
			float64(commits)/elapsed, float64(s.w.writes.Load()-writes0)/float64(commits),
			(time.Duration(s.w.busy.Load()-busy0) / time.Duration(commits)).Round(100*time.Microsecond),
			100*float64(s.w.busy.Load()-busy0)/float64(duration))
	}
	t.Logf("duration %v, %d recorders, %d hitters, errors %d", duration, recorders, hitters, stats.errors.Load())
	t.Logf("throughput: records %.1f/s, hits %.1f/s, write commits %.1f/s", float64(stats.records.Load())/elapsed, float64(stats.hits.Load())/elapsed, float64(stats.commits.Load())/elapsed)
	for _, row := range []struct {
		name string
		v    []time.Duration
	}{
		{"record lookup (settings+collection+key+lookup)", stats.recordLookup},
		{"record publish", stats.publish},
		{"record history insert", stats.recordHistory},
		{"record total", stats.recordTotal},
		{"hit lookup (settings+collection+key+lookup)", stats.hitLookup},
		{"hit history insert", stats.hitHistory},
		{"hit total", stats.hitTotal},
		{"admin " + admin[0].name, stats.list5},
		{"admin " + admin[1].name, stats.list200},
		{"admin " + admin[2].name, stats.after},
		{"admin " + admin[3].name, stats.insights},
	} {
		t.Logf("%-50s %s", row.name, loadPercentiles(row.v))
	}
}

type loadStats struct {
	mu                                                               sync.Mutex
	recordLookup, publish, recordHistory, recordTotal                []time.Duration
	hitLookup, hitHistory, hitTotal, list5, list200, after, insights []time.Duration
	records, hits, commits, errors                                   atomic.Int64
}

func (s *loadStats) add(to *[]time.Duration, d time.Duration) {
	s.mu.Lock()
	*to = append(*to, d)
	s.mu.Unlock()
}

func loadPercentiles(v []time.Duration) string {
	if len(v) == 0 {
		return "n=0"
	}
	v = slices.Clone(v)
	slices.Sort(v)
	at := func(q float64) time.Duration {
		return v[min(len(v)-1, int(q*float64(len(v))))].Round(100 * time.Microsecond)
	}
	return fmt.Sprintf("n=%-6d p50=%-10v p99=%-10v max=%v", len(v), at(.5), at(.99), v[len(v)-1].Round(time.Millisecond))
}

// loadLookup does what the proxy does before answering: read the settings
// and active collection, key the request and look it up.
func loadLookup(ctx context.Context, s *Store, body []byte) (model.Entry, bool, time.Duration) {
	started := time.Now()
	settings, err := s.Settings(ctx)
	if err != nil {
		panic(err)
	}
	c, err := s.Collection(ctx, settings.ActiveCollectionID)
	if err != nil {
		panic(err)
	}
	key, input, err := matching.Key(chatRoute, body, c.Exclusions)
	if err != nil {
		panic(err)
	}
	e, err := s.Lookup(ctx, c.ID, key)
	if err == nil {
		return e, true, time.Since(started)
	}
	return model.Entry{Recording: model.Recording{CollectionID: c.ID, Key: key, Route: chatRoute, Request: body, MatchingInput: input, Streaming: true}}, false, time.Since(started)
}

func loadPublish(ctx context.Context, s *Store, cid int64, body []byte, rev model.Revision) error {
	key, input, err := matching.Key(chatRoute, body, nil)
	if err != nil {
		return err
	}
	_, err = s.Publish(ctx, model.Recording{CollectionID: cid, Key: key, Route: chatRoute, Request: body, MatchingInput: input, Streaming: true}, rev)
	return err
}

// loadStream is a chat completion streamed as n chunks, like a long answer.
func loadStream(n int) model.Revision {
	events := make([]model.Event, 0, n+2)
	for i := range n {
		events = append(events, model.Event{OffsetMS: int64(i * 15), Data: fmt.Sprintf(`data: {"id":"chatcmpl-1","object":"chat.completion.chunk","created":1,"model":"m","choices":[{"index":0,"delta":{"content":"token %d "},"finish_reason":null}]}`+"\n\n", i)})
	}
	events = append(events,
		model.Event{OffsetMS: int64(n * 15), Data: `data: {"id":"chatcmpl-1","object":"chat.completion.chunk","created":1,"model":"m","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":50000,"completion_tokens":1000}}` + "\n\n"},
		model.Event{OffsetMS: int64(n * 15), Data: "data: [DONE]\n\n"})
	return model.Revision{Status: 200, Headers: map[string]string{"Content-Type": "text/event-stream"}, Events: events, Source: "recorded"}
}

// loadConversation is an agentic chat that re-sends its whole thread each
// turn, with a large system prompt, tool output and inline screenshots.
type loadConversation struct {
	r        *rand.Rand
	messages []any
}

func newLoadConversation(seed uint64) *loadConversation {
	c := &loadConversation{r: rand.New(rand.NewPCG(seed, seed^0x9e37))}
	c.messages = []any{map[string]any{"role": "system", "content": c.text(16 << 10)}}
	c.messages = append(c.messages, map[string]any{"role": "user", "content": []any{
		map[string]any{"type": "text", "text": c.text(2 << 10)},
		c.image(900 << 10),
	}})
	return c
}

func (c *loadConversation) text(n int) string {
	const words = "the of and to in is was for on that with as by tool result file line error build test "
	var b strings.Builder
	for b.Len() < n {
		start := c.r.IntN(len(words) - 8)
		b.WriteString(words[start : start+8])
	}
	return b.String()
}

// image is a base64 PNG-sized payload of incompressible bytes.
func (c *loadConversation) image(n int) map[string]any {
	raw := make([]byte, n*3/4)
	for i := range raw {
		raw[i] = byte(c.r.Uint32())
	}
	return map[string]any{"type": "image_url", "image_url": map[string]any{"url": "data:image/png;base64," + base64.StdEncoding.EncodeToString(raw)}}
}

func (c *loadConversation) turn() {
	c.messages = append(c.messages, map[string]any{"role": "assistant", "content": c.text(1 << 10)})
	content := []any{map[string]any{"type": "text", "text": c.text(3 << 10)}}
	if c.r.IntN(4) == 0 {
		content = append(content, c.image(200<<10))
	}
	c.messages = append(c.messages, map[string]any{"role": "user", "content": content})
}

func (c *loadConversation) body() []byte {
	b, err := json.Marshal(map[string]any{"model": "m", "stream": true, "messages": c.messages}, json.Deterministic(true))
	if err != nil {
		panic(err)
	}
	return b
}

func (c *loadConversation) size() int { return len(c.body()) }
