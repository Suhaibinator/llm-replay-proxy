// Package devseed fills a database with realistic synthetic traffic so the
// console can be developed and reviewed against real endpoints: several
// models (reasoning ones included) on all three protocols, JSON and SSE,
// multi-turn conversations that grow, and hits, misses, recordings,
// interruptions and errors spread over weeks, with token usage, OpenRouter
// costs and latencies. It is a development aid; the proxy never runs it.
package devseed

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"math/rand/v2"
	"sort"
	"strings"
	"time"

	"github.com/local/llm-replay-proxy/internal/match"
	"github.com/local/llm-replay-proxy/internal/model"
	"github.com/local/llm-replay-proxy/internal/store"
)

const (
	chatRoute      = "/v1/chat/completions"
	responsesRoute = "/v1/responses"
	messagesRoute  = "/v1/messages"
)

type Options struct {
	// Collection names the collection to create; a numeric suffix is added
	// when the name is taken.
	Collection string
	// Rows is roughly how many history rows to write.
	Rows int
	// Days spreads the traffic over this many days before Now.
	Days int
	Seed uint64
	Now  time.Time
	// Activate makes the new collection the active one.
	Activate bool
}

type Result struct {
	Collection model.Collection
	Recordings int
	Revisions  int
	History    int
}

// profile describes one synthetic model.
type profile struct {
	route, model string
	reasoning    bool
	streamShare  float64
	toolShare    float64
	// priceIn/priceOut are USD per million tokens; set only for models
	// served through OpenRouter, which reports usage.cost.
	priceIn, priceOut float64
	latencyMS         float64 // median upstream duration
	weight            int
}

var profiles = []profile{
	{route: chatRoute, model: "gpt-4.1-mini", streamShare: .6, toolShare: .2, latencyMS: 900, weight: 5},
	{route: chatRoute, model: "openai/o4-mini", reasoning: true, streamShare: .7, toolShare: .15, priceIn: 1.1, priceOut: 4.4, latencyMS: 6000, weight: 2},
	{route: chatRoute, model: "deepseek/deepseek-r1", reasoning: true, streamShare: .9, priceIn: .55, priceOut: 2.19, latencyMS: 14000, weight: 1},
	{route: chatRoute, model: "anthropic/claude-sonnet-4.5", reasoning: true, streamShare: .8, toolShare: .2, priceIn: 3, priceOut: 15, latencyMS: 7000, weight: 1},
	{route: responsesRoute, model: "gpt-5", reasoning: true, streamShare: .7, toolShare: .25, latencyMS: 11000, weight: 3},
	{route: responsesRoute, model: "gpt-4.1", streamShare: .5, toolShare: .2, latencyMS: 1600, weight: 2},
	{route: messagesRoute, model: "claude-sonnet-4-5", reasoning: true, streamShare: .7, toolShare: .3, latencyMS: 5500, weight: 3},
	{route: messagesRoute, model: "claude-haiku-4-5", streamShare: .6, toolShare: .1, latencyMS: 1300, weight: 2},
}

var systems = []string{
	"You are a careful senior engineer. Answer concisely and show code when it helps.",
	"You are the support assistant for Acme Cloud. Use the tools to look up account data before answering.",
	"You summarize documents for busy executives. Prefer bullet points.",
	"You are a data analyst. Write SQL for PostgreSQL 16 and explain the result briefly.",
}

var openings = []string{
	"Why does my Go service leak goroutines when the client disconnects?",
	"Summarize the attached incident report for the leadership sync.",
	"Write a SQL query that finds customers whose spend dropped 30% month over month.",
	"My invoice for September looks wrong, can you check account 4411?",
	"Draft a migration plan from Postgres 12 to 16 with zero downtime.",
	"Explain the difference between optimistic and pessimistic locking with an example.",
	"What changed between v2.3 and v2.4 of our billing API?",
	"Review this React component for accessibility problems.",
	"Plan a three day offsite in Lisbon for twelve engineers.",
	"How should we shard the events table once it passes two billion rows?",
	"Turn these meeting notes into action items with owners.",
	"Why is the p99 latency of the search endpoint spiking every night at 2am?",
	"Compare Kafka and NATS JetStream for our order pipeline.",
	"Translate the onboarding email into German and French.",
	"Generate test cases for the password reset flow.",
}

var followUps = []string{
	"Can you make that shorter?", "Now add unit tests.", "What about the error path?", "Use a table instead.",
	"That doesn't compile, the import is missing.", "Explain the second step in more detail.",
	"Can you check the logs for that account too?", "Rewrite it for a non-technical audience.",
	"What are the risks?", "Give me the final version.", "Use the staging data instead.", "Thanks, one more thing: add metrics.",
}

var words = strings.Fields(`the service request latency queue worker retry budget deploy rollout cache index query plan table
customer account invoice region cluster shard replica migration schema column metric alert threshold window
we should first then because so this means it is likely that you can a an and of to in for with on by
consider check update ensure measure compare reduce avoid handle return close open stream buffer context`)

var teams = []string{"OPS", "BILL", "PLAT", "DATA", "WEB", "SEC"}

var tools = []string{"lookup_account", "search_docs", "run_sql", "get_weather", "fetch_logs", "create_ticket"}

type seeder struct {
	r       *rand.Rand
	db      *store.Store
	opt     Options
	cid     int64
	rows    []model.History
	res     Result
	counter int
}

// Seed writes a new collection of synthetic recordings and history.
func Seed(ctx context.Context, db *store.Store, opt Options) (Result, error) {
	if opt.Rows <= 0 {
		opt.Rows = 4000
	}
	if opt.Days <= 0 {
		opt.Days = 30
	}
	if opt.Now.IsZero() {
		opt.Now = time.Now().UTC()
	}
	if opt.Collection == "" {
		opt.Collection = "Demo traffic"
	}
	s := &seeder{r: rand.New(rand.NewPCG(opt.Seed, opt.Seed^0x9e3779b97f4a7c15)), db: db, opt: opt}
	var err error
	name := opt.Collection
	for i := 2; ; i++ {
		s.res.Collection, err = db.CreateCollection(ctx, name, nil)
		if err == nil {
			break
		}
		if !errors.Is(err, store.ErrConflict) || i > 100 {
			return s.res, err
		}
		name = fmt.Sprintf("%s %d", opt.Collection, i)
	}
	s.cid = s.res.Collection.ID
	for len(s.rows) < opt.Rows {
		if err := s.conversation(ctx); err != nil {
			return s.res, err
		}
	}
	// Insert in time order so ids follow time, as live traffic does.
	sort.SliceStable(s.rows, func(i, j int) bool { return s.rows[i].CreatedAt < s.rows[j].CreatedAt })
	for _, h := range s.rows {
		if err := db.AddHistory(ctx, h); err != nil {
			return s.res, err
		}
	}
	s.res.History = len(s.rows)
	if opt.Activate {
		settings, err := db.Settings(ctx)
		if err != nil {
			return s.res, err
		}
		settings.ActiveCollectionID = s.cid
		if err = db.SetSettings(ctx, settings); err != nil {
			return s.res, err
		}
	}
	return s.res, nil
}

func (s *seeder) pick() profile {
	total := 0
	for _, p := range profiles {
		total += p.weight
	}
	n := s.r.IntN(total)
	for _, p := range profiles {
		if n < p.weight {
			return p
		}
		n -= p.weight
	}
	return profiles[0]
}

func (s *seeder) sentence(minWords, maxWords int) string {
	n := minWords + s.r.IntN(maxWords-minWords+1)
	parts := make([]string, n)
	for i := range parts {
		parts[i] = words[s.r.IntN(len(words))]
	}
	text := strings.Join(parts, " ")
	return strings.ToUpper(text[:1]) + text[1:] + "."
}

func (s *seeder) paragraph(sentences int) string {
	parts := make([]string, sentences)
	for i := range parts {
		parts[i] = s.sentence(6, 18)
	}
	return strings.Join(parts, " ")
}

// lognormal returns a positive duration around median ms with a long tail.
func (s *seeder) lognormal(median float64, spread float64) int64 {
	return int64(math.Max(1, median*math.Exp(s.r.NormFloat64()*spread)))
}

func (s *seeder) at(t time.Time) string { return t.UTC().Format(time.RFC3339Nano) }

// turn is one exchange already in the conversation.
type turn struct {
	user string
	r    reply
}

// request renders the conversation so far plus the new user message.
func request(p profile, system string, past []turn, user string, streaming bool) []byte {
	var body map[string]any
	toolResult := func(t turn) string {
		return mustJSON(map[string]any{"ok": true, "rows": len(t.r.tool.args)})
	}
	switch p.route {
	case chatRoute:
		messages := []any{map[string]any{"role": "system", "content": system}}
		for _, t := range past {
			messages = append(messages, map[string]any{"role": "user", "content": t.user})
			a := map[string]any{"role": "assistant", "content": t.r.text}
			if t.r.tool != nil {
				a["tool_calls"] = []any{map[string]any{"id": t.r.tool.id, "type": "function", "function": map[string]any{"name": t.r.tool.name, "arguments": t.r.tool.args}}}
				messages = append(messages, a, map[string]any{"role": "tool", "tool_call_id": t.r.tool.id, "content": toolResult(t)})
				continue
			}
			messages = append(messages, a)
		}
		messages = append(messages, map[string]any{"role": "user", "content": user})
		body = map[string]any{"model": p.model, "messages": messages}
		if streaming {
			body["stream_options"] = map[string]any{"include_usage": true}
		}
	case responsesRoute:
		input := []any{}
		for _, t := range past {
			input = append(input, map[string]any{"role": "user", "content": []any{map[string]any{"type": "input_text", "text": t.user}}})
			if t.r.text != "" {
				input = append(input, map[string]any{"role": "assistant", "content": []any{map[string]any{"type": "output_text", "text": t.r.text}}})
			}
			if t.r.tool != nil {
				input = append(input, map[string]any{"type": "function_call", "call_id": t.r.tool.id, "name": t.r.tool.name, "arguments": t.r.tool.args},
					map[string]any{"type": "function_call_output", "call_id": t.r.tool.id, "output": toolResult(t)})
			}
		}
		input = append(input, map[string]any{"role": "user", "content": []any{map[string]any{"type": "input_text", "text": user}}})
		body = map[string]any{"model": p.model, "instructions": system, "input": input, "store": false}
		if p.reasoning {
			body["reasoning"] = map[string]any{"effort": "medium", "summary": "auto"}
		}
	case messagesRoute:
		messages := []any{}
		for _, t := range past {
			messages = append(messages, map[string]any{"role": "user", "content": t.user})
			messages = append(messages, map[string]any{"role": "assistant", "content": messagesContent(t.r)})
			if t.r.tool != nil {
				messages = append(messages, map[string]any{"role": "user", "content": []any{map[string]any{"type": "tool_result", "tool_use_id": t.r.tool.id, "content": toolResult(t)}}})
			}
		}
		messages = append(messages, map[string]any{"role": "user", "content": user})
		body = map[string]any{"model": p.model, "system": system, "max_tokens": 8192, "messages": messages}
		if p.reasoning {
			body["thinking"] = map[string]any{"type": "enabled", "budget_tokens": 4096}
		}
	}
	if streaming {
		body["stream"] = true
	}
	b, _ := json.Marshal(body)
	return b
}

func (s *seeder) reply(p profile, req []byte, prevIn int64, created time.Time) reply {
	s.counter++
	r := reply{id: fmt.Sprintf("seed%06d", s.counter), model: p.model, created: created.Unix()}
	r.in = int64(len(req)/4) + 20
	if prevIn > 0 && s.r.Float64() < .75 {
		r.cached = prevIn / 64 * 64
	}
	if p.route == messagesRoute && s.r.Float64() < .5 {
		r.cacheWrite = min(r.in-r.cached, 200+int64(s.r.IntN(800)))
	}
	if s.r.Float64() < p.toolShare {
		name := tools[s.r.IntN(len(tools))]
		r.tool = &toolCall{id: "call_" + r.id, name: name, args: mustJSON(map[string]any{"query": s.sentence(2, 5), "limit": 1 + s.r.IntN(50)})}
		if s.r.Float64() < .4 {
			r.text = s.sentence(5, 12)
		}
	} else {
		r.text = s.paragraph(1 + s.r.IntN(6))
	}
	if p.reasoning {
		r.reasoningTk = 64 + int64(s.r.IntN(3000))
		if p.route == messagesRoute || s.r.Float64() < .6 {
			r.reasoning = s.paragraph(1 + s.r.IntN(3))
		}
	}
	r.out = int64(len(r.text)/4) + r.reasoningTk + 5
	if r.tool != nil {
		r.out += int64(len(r.tool.args) / 4)
	}
	if p.priceIn > 0 {
		cost := (float64(r.in)*p.priceIn + float64(r.out)*p.priceOut) / 1e6
		r.cost = &cost
	}
	return r
}

// conversation simulates one thread: each turn is first sent upstream (or
// missed, or fails), and recorded turns are replayed later.
func (s *seeder) conversation(ctx context.Context) error {
	p := s.pick()
	streaming := s.r.Float64() < p.streamShare
	system := systems[s.r.IntN(len(systems))]
	span := time.Duration(s.opt.Days) * 24 * time.Hour
	// Recent days are busier, so the series has a visible trend.
	start := s.opt.Now.Add(-time.Duration(math.Pow(s.r.Float64(), 1.6) * float64(span)))
	turns := 1 + int(math.Min(9, s.r.ExpFloat64()*2.2))
	identity := fmt.Sprintf(`{"version":1,"url":"https://upstream.example%s","identity":"","headers":[]}`, p.route)
	var past []turn
	var prevIn int64
	now := start
	for k := 0; k < turns && now.Before(s.opt.Now); k++ {
		// A ticket reference keeps each conversation's opening, and so its
		// thread, distinct.
		user := fmt.Sprintf("%s (ticket %s-%d)", openings[s.r.IntN(len(openings))], teams[s.r.IntN(len(teams))], 100+s.r.IntN(9900))
		if k > 0 {
			user = followUps[s.r.IntN(len(followUps))]
		}
		req := request(p, system, past, user, streaming)
		key, _, err := match.Key(p.route, identity, req, nil)
		if err != nil {
			return err
		}
		r := s.reply(p, req, prevIn, now)
		duration := s.lognormal(p.latencyMS*(1+float64(k)*.08), .55)
		var first *int64
		if streaming {
			v := duration / int64(3+s.r.IntN(6))
			if p.reasoning {
				v = duration * int64(4+s.r.IntN(5)) / 10
			}
			first = &v
		}
		row := model.History{CollectionID: s.cid, Route: p.route, Key: key, Request: req, Source: "upstream", CacheStatus: "miss", DurationMS: &duration, FirstEventMS: first, CreatedAt: s.at(now)}
		if s.r.Float64() < .2 {
			row.CacheStatus = "bypass" // recorded in record mode
		}
		roll := s.r.Float64()
		switch {
		case roll < .06:
			row.Outcome, row.Source, row.DurationMS, row.FirstEventMS, row.Detail = "miss", "proxy", nil, nil, "replay mode"
		case roll < .10:
			short := duration / 8
			row.Outcome, row.Detail, row.DurationMS, row.FirstEventMS = "error", "502 Bad Gateway: upstream connect error or disconnect/reset before headers", &short, nil
		case roll < .12:
			row.Outcome, row.Detail = "interrupted", "client connection closed: context canceled"
		case roll < .135 && streaming:
			row.Outcome, row.Detail = "incomplete", "stream ended before its terminal event"
		default:
			if err := s.record(ctx, p, streaming, identity, key, req, r, duration, first, row, now); err != nil {
				return err
			}
		}
		if row.Outcome != "" {
			s.rows = append(s.rows, row)
		}
		past = append(past, turn{user: user, r: r})
		prevIn = r.in
		now = now.Add(time.Duration(20+s.r.IntN(600)) * time.Second)
	}
	return nil
}

// record publishes a turn's response, logs the upstream call and its later
// replays, and occasionally an edit or a near-miss variant of the request.
func (s *seeder) record(ctx context.Context, p profile, streaming bool, identity, key string, req []byte, r reply, duration int64, first *int64, row model.History, at time.Time) error {
	firstMS := int64(0)
	if first != nil {
		firstMS = *first
	}
	rec := model.Recording{CollectionID: s.cid, Key: key, Route: p.route, Request: req, UpstreamIdentity: identity, Streaming: streaming}
	// Recordings date from the call that recorded them, not from seeding.
	entry, err := s.db.PublishAt(ctx, rec, revision(p.route, streaming, r, firstMS, duration), at)
	if err != nil {
		return fmt.Errorf("publish %s %s: %w", p.route, p.model, err)
	}
	s.res.Recordings++
	s.res.Revisions++
	row.Outcome, row.RecordingID, row.RevisionID = "recorded", entry.Recording.ID, entry.Revision.ID
	s.rows = append(s.rows, row)

	revisionID := entry.Revision.ID
	editAt := time.Time{}
	if s.r.Float64() < .05 {
		edited := r
		edited.text = r.text + " " + s.sentence(4, 8)
		if r.text == "" {
			edited.text = s.sentence(4, 8)
		}
		rev := revision(p.route, streaming, edited, firstMS, duration)
		rev.Source = "edit"
		editAt = at.Add(time.Duration(s.r.IntN(72)) * time.Hour)
		if editAt.After(s.opt.Now) {
			editAt = at.Add(s.opt.Now.Sub(at) / 2)
		}
		e, err := s.db.PublishAt(ctx, rec, rev, editAt)
		if err != nil {
			return fmt.Errorf("edit %s %s: %w", p.route, p.model, err)
		}
		s.res.Revisions++
		revisionID = e.Revision.ID
	}
	hits := int(s.r.ExpFloat64() * 4)
	if s.r.Float64() < .05 {
		hits += 20 + s.r.IntN(40) // a few hot recordings
	}
	remaining := s.opt.Now.Sub(at)
	for range hits {
		when := at.Add(time.Duration(s.r.Float64() * float64(remaining)))
		served := entry.Revision.ID
		if !editAt.IsZero() && when.After(editAt) {
			served = revisionID
		}
		d := s.lognormal(12, .9)
		var fe *int64
		if streaming {
			d = s.lognormal(180, .6)
			v := s.lognormal(8, .7)
			fe = &v
		}
		hit := model.History{CollectionID: s.cid, Route: p.route, Key: key, Request: req, Outcome: "hit", CacheStatus: "hit", Source: "replay",
			RecordingID: entry.Recording.ID, RevisionID: served, DurationMS: &d, FirstEventMS: fe, CreatedAt: s.at(when)}
		if s.r.Float64() < .03 {
			hit.Outcome, hit.RevisionID, hit.Detail = "interrupted", 0, "client connection closed: write: broken pipe"
		}
		s.rows = append(s.rows, hit)
	}
	// A client that changed one parameter misses the recording: the near
	// misses the console's nearest-recording view is for.
	if s.r.Float64() < .08 {
		var body map[string]any
		_ = json.Unmarshal(req, &body)
		body["temperature"] = math.Round(s.r.Float64()*10) / 10
		variant, _ := json.Marshal(body)
		vkey, _, err := match.Key(p.route, identity, variant, nil)
		if err != nil {
			return err
		}
		when := at.Add(time.Duration(s.r.Float64() * float64(remaining)))
		s.rows = append(s.rows, model.History{CollectionID: s.cid, Route: p.route, Key: vkey, Request: variant, Outcome: "miss", CacheStatus: "miss",
			Source: "proxy", Detail: "replay mode", CreatedAt: s.at(when)})
	}
	return nil
}
