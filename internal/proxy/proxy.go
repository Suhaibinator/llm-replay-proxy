// Package proxy implements the inference-facing recording and replay HTTP proxy.
package proxy

import (
	"bytes"
	"compress/gzip"
	"compress/zlib"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"mime"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/local/llm-replay-proxy/internal/match"
	"github.com/local/llm-replay-proxy/internal/model"
	"github.com/local/llm-replay-proxy/internal/protocol"
	"github.com/local/llm-replay-proxy/internal/store"
)

const maxRequestBytes = 64 << 20
const maxRecordedResponseBytes = 256 << 20

type Upstream struct {
	URL      string
	APIKey   string
	Identity string
	Headers  map[string]string
}

// Clock exists so replay pacing can be tested without wall-clock sleeps.
type Clock interface {
	Now() time.Time
	Sleep(context.Context, time.Duration) error
}

type Config struct {
	Upstreams map[string]Upstream
	Client    *http.Client
	Clock     Clock
}

type handler struct {
	db        *store.Store
	upstreams map[string]Upstream
	client    *http.Client
	clock     Clock
}

type realClock struct{}

func (realClock) Now() time.Time { return time.Now() }
func (realClock) Sleep(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return nil
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func New(db *store.Store, cfg Config) http.Handler {
	c := cfg.Client
	if c == nil {
		c = http.DefaultClient
	}
	// Work with a copy: fixed upstream URLs must not turn into hidden re-POSTs via
	// redirects, and callers should not have their client mutated.
	clientCopy := *c
	clientCopy.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	clock := cfg.Clock
	if clock == nil {
		clock = realClock{}
	}
	upstreams := make(map[string]Upstream, len(cfg.Upstreams))
	for route, upstream := range cfg.Upstreams {
		upstreams[route] = normalizeUpstream(upstream)
	}
	return &handler{db: db, upstreams: upstreams, client: &clientCopy, clock: clock}
}

func (h *handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost || !validRoute(r.URL.Path) {
		writeError(w, http.StatusNotFound, "not_found", "only the configured inference endpoints are available")
		return
	}
	h.serveInference(w, r)
}

func validRoute(route string) bool {
	switch route {
	case "/v1/chat/completions", "/v1/responses", "/v1/messages":
		return true
	default:
		return false
	}
}

func (h *handler) serveInference(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, maxRequestBytes+1))
	if err != nil {
		writeError(w, 400, "invalid_request", err.Error())
		return
	}
	if len(body) > maxRequestBytes {
		writeError(w, 413, "request_too_large", "request body exceeds 64 MiB")
		return
	}
	var request map[string]any
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber()
	if err := dec.Decode(&request); err != nil || request == nil {
		writeError(w, 400, "invalid_json", "request body must be a JSON object")
		return
	}
	if err := ensureJSONEOF(dec); err != nil {
		writeError(w, 400, "invalid_json", "request body must contain exactly one JSON object")
		return
	}
	streaming := false
	// An explicit null means the default, as it does for the providers.
	if value, exists := request["stream"]; exists && value != nil {
		var ok bool
		streaming, ok = value.(bool)
		if !ok {
			writeError(w, 400, "invalid_stream", "stream must be a boolean")
			return
		}
	}

	settings, err := h.db.Settings(r.Context())
	if err != nil {
		writeError(w, 500, "storage_error", err.Error())
		return
	}
	collection, err := h.db.Collection(r.Context(), settings.ActiveCollectionID)
	if err != nil {
		writeError(w, 503, "no_active_collection", "configure an active recording collection")
		return
	}
	upstream, ok := h.upstreams[r.URL.Path]
	if !ok || upstream.URL == "" {
		writeError(w, 503, "upstream_not_configured", "no upstream is configured for this route")
		return
	}
	identity := upstreamIdentity(upstream)
	key, canonical, err := match.Key(r.URL.Path, identity, body, collection.Exclusions)
	if err != nil {
		writeError(w, 400, "matching_error", err.Error())
		return
	}

	if settings.Mode != "record" {
		entry, lookupErr := h.db.Lookup(r.Context(), collection.ID, key)
		if lookupErr == nil {
			started := h.clock.Now()
			first, replayErr := h.replay(w, r, entry, settings)
			duration := h.clock.Now().Sub(started).Milliseconds()
			item := model.History{CollectionID: collection.ID, Route: r.URL.Path, Key: key, Request: clone(body), Outcome: "hit", CacheStatus: "hit", RecordingID: entry.Recording.ID, Source: "replay", DurationMS: &duration, FirstEventMS: first}
			if replayErr != nil {
				item.Detail = replayErr.Error()
				if clientGone(r.Context(), replayErr) {
					item.Outcome = "interrupted"
				} else {
					item.Outcome = "error"
				}
			}
			h.addHistory(r.Context(), item)
			return
		}
		if !errors.Is(lookupErr, store.ErrNotFound) {
			writeError(w, 500, "storage_error", lookupErr.Error())
			return
		}
		if settings.Mode == "replay" {
			h.addHistory(r.Context(), model.History{CollectionID: collection.ID, Route: r.URL.Path, Key: key, Request: clone(body), Outcome: "miss", CacheStatus: "miss", Source: "proxy", Detail: "replay mode"})
			writeError(w, 404, "recording_not_found", "no exact recording exists in the active collection")
			return
		}
	}
	blocked, stateID, err := h.recordedStateReference(r.Context(), collection.ID, body)
	if err != nil {
		writeError(w, 500, "storage_error", err.Error())
		return
	}
	if blocked {
		detail := "request references recorded provider state " + stateID
		lookup := "bypass"
		if settings.Mode == "auto" {
			lookup = "miss"
		}
		h.addHistory(r.Context(), model.History{CollectionID: collection.ID, Route: r.URL.Path, Key: key, Request: clone(body), Outcome: "error", CacheStatus: lookup, Source: "proxy", Detail: detail})
		writeError(w, 409, "recorded_state_unavailable", detail+"; warm this exact request or reconstruct the conversation")
		return
	}

	h.forward(w, r, body, canonical, key, collection.ID, upstream, identity, streaming, settings.Mode == "auto")
}

func (h *handler) replay(w http.ResponseWriter, r *http.Request, entry model.Entry, s model.Settings) (*int64, error) {
	started := h.clock.Now()
	var first *int64
	copyResponseHeaders(w.Header(), entry.Revision.Headers)
	w.WriteHeader(entry.Revision.Status)
	if !entry.Recording.Streaming {
		if err := r.Context().Err(); err != nil {
			return nil, err
		}
		if _, err := io.WriteString(w, entry.Revision.Body); err != nil {
			return nil, clientWriteError(err)
		}
		return nil, nil
	}
	flusher, _ := w.(http.Flusher)
	previous := int64(0)
	for i, event := range entry.Revision.Events {
		if err := r.Context().Err(); err != nil {
			return first, err
		}
		delay := time.Duration(0)
		if i == 0 {
			delay = time.Duration(s.FirstEventDelayMS) * time.Millisecond
		} else {
			delta := event.OffsetMS - previous
			if delta < 0 {
				delta = 0
			}
			delay = scaledDelay(delta, s.DelayMultiplier)
		}
		if err := h.clock.Sleep(r.Context(), delay); err != nil {
			return first, err
		}
		if _, err := io.WriteString(w, event.Data); err != nil {
			return first, clientWriteError(err)
		}
		if flusher != nil {
			flusher.Flush()
		}
		previous = event.OffsetMS
		if first == nil && hasSSEData(event.Data) {
			v := h.clock.Now().Sub(started).Milliseconds()
			first = &v
		}
	}
	return first, nil
}

func (h *handler) forward(w http.ResponseWriter, r *http.Request, original, canonical []byte, key string, collectionID int64, upstream Upstream, identity string, streaming bool, cacheMiss bool) {
	requestStarted := h.clock.Now()
	var firstEvent *int64
	history := func(outcome, detail string, recordingID int64) {
		duration := h.clock.Now().Sub(requestStarted).Milliseconds()
		cacheStatus := "bypass"
		if cacheMiss {
			cacheStatus = "miss"
		}
		h.addHistory(r.Context(), model.History{CollectionID: collectionID, Route: r.URL.Path, Key: key, Request: clone(original), Outcome: outcome, Detail: detail, RecordingID: recordingID, Source: "upstream", CacheStatus: cacheStatus, DurationMS: &duration, FirstEventMS: firstEvent})
	}
	req, err := http.NewRequestWithContext(r.Context(), http.MethodPost, upstream.URL, bytes.NewReader(original))
	if err != nil {
		writeError(w, 500, "upstream_request_error", err.Error())
		return
	}
	// A nil GetBody prevents net/http's transport from automatically replaying
	// this POST after a connection failure, including when idempotency headers
	// are configured upstream.
	req.GetBody = nil
	// The inference endpoints accept JSON and this fixed value is part of the
	// proxy's forwarding contract, so caller headers cannot silently change
	// matching semantics.
	req.Header.Set("Content-Type", "application/json")
	for k, v := range upstream.Headers {
		req.Header.Set(k, v)
	}
	// Content-Encoding is not forwarded, so the body must reach the caller and
	// the recording decoded. Leaving Accept-Encoding to the transport keeps its
	// transparent gzip decompression enabled; a configured value would disable it.
	req.Header.Del("Accept-Encoding")
	setCredential(req.Header, r.URL.Path, upstream.APIKey)
	started := h.clock.Now()
	resp, err := h.client.Do(req)
	if err != nil {
		if clientGone(r.Context(), err) {
			history("interrupted", err.Error(), 0)
			return
		}
		history("error", err.Error(), 0)
		writeError(w, 502, "upstream_error", err.Error())
		return
	}
	defer resp.Body.Close()
	body, err := decodedBody(resp)
	if err != nil {
		history("error", err.Error(), 0)
		writeError(w, 502, "upstream_error", err.Error())
		return
	}
	defer body.Close()
	headers := allowedHeaders(resp.Header)
	copyResponseHeaders(w.Header(), headers)
	w.WriteHeader(resp.StatusCode)
	flusher, _ := w.(http.Flusher)
	revision := model.Revision{Status: resp.StatusCode, Headers: headers, Source: "recorded"}
	success := resp.StatusCode >= 200 && resp.StatusCode < 300
	var readErr error
	hungUpAfterCompletion := false
	// incompleteAtHangup explains why a stream the caller abandoned was not
	// complete, since the cancellation error alone does not say.
	var incompleteAtHangup error
	if streaming {
		parser := newSSEParser()
		var captured int64
		captureOverflow := false
		buf := make([]byte, 32*1024)
		for {
			n, err := body.Read(buf)
			if n > 0 {
				chunk := buf[:n]
				written, writeErr := w.Write(chunk)
				if writeErr != nil {
					readErr = clientWriteError(writeErr)
					break
				}
				if written != len(chunk) {
					readErr = clientWriteError(io.ErrShortWrite)
					break
				}
				if flusher != nil {
					flusher.Flush()
				}
				for _, frame := range parser.Push(chunk) {
					if firstEvent == nil && hasSSEData(frame) {
						v := h.clock.Now().Sub(requestStarted).Milliseconds()
						firstEvent = &v
					}
					captured += int64(len(frame))
					if captured <= maxRecordedResponseBytes {
						revision.Events = append(revision.Events, model.Event{Data: frame, OffsetMS: h.clock.Now().Sub(started).Milliseconds()})
					} else {
						captureOverflow = true
						revision.Events = nil
					}
				}
			}
			if err != nil {
				if !errors.Is(err, io.EOF) {
					readErr = err
				}
				break
			}
		}
		// SDKs such as openai-go stop reading at the terminal event and close
		// the connection while the upstream is still ending its body. Every
		// captured frame already reached the caller, so a capture that is a
		// complete protocol response is recorded rather than reported as
		// interrupted. A stream cut short still fails validation.
		if success && readErr != nil && clientGone(r.Context(), readErr) && !parser.Overflow() && !captureOverflow {
			if parser.Pending() {
				incompleteAtHangup = errors.New("an SSE frame was partially received")
			} else if err := protocol.Validate(r.URL.Path, streaming, revision); err != nil {
				incompleteAtHangup = err
			} else {
				hungUpAfterCompletion = true
				readErr = nil
			}
		}
		// Error statuses carry provider error bodies (usually JSON), which are
		// forwarded as received; SSE framing only applies to successful streams.
		if success && readErr == nil {
			if parser.Pending() {
				readErr = errors.New("upstream ended with an incomplete SSE frame")
			}
			mediaType, _, mediaErr := mime.ParseMediaType(resp.Header.Get("Content-Type"))
			if mediaErr != nil || !strings.EqualFold(mediaType, "text/event-stream") {
				readErr = errors.New("streaming upstream response is not text/event-stream")
			}
		}
		if parser.Overflow() || captureOverflow {
			readErr = errors.New("upstream response exceeds recording limit")
		}
	} else {
		responseBody := &limitedCapture{limit: maxRecordedResponseBytes}
		writer := io.MultiWriter(w, responseBody)
		_, readErr = copyToClient(writer, body)
		if responseBody.overflow {
			readErr = errors.New("upstream response exceeds recording limit")
		}
		revision.Body = responseBody.buf.String()
	}
	if readErr != nil || !success {
		// The upstream status leads the detail so a failed stream still
		// explains which HTTP error the caller received.
		var details []string
		if !success {
			details = append(details, resp.Status)
		}
		if readErr != nil {
			details = append(details, readErr.Error())
		}
		if incompleteAtHangup != nil {
			details = append(details, "stream incomplete at disconnect", incompleteAtHangup.Error())
		}
		outcome := "error"
		if clientGone(r.Context(), readErr) {
			outcome = "interrupted"
		}
		history(outcome, strings.Join(details, ": "), 0)
		return
	}
	if err := protocol.Validate(r.URL.Path, streaming, revision); err != nil {
		history("incomplete", err.Error(), 0)
		return
	}
	publishCtx := r.Context()
	if streaming {
		// Callers hang up as soon as they read the terminal event, which races
		// both the upstream ending its body and this publication. A complete
		// stream is therefore published independently of the caller.
		var cancel context.CancelFunc
		publishCtx, cancel = context.WithTimeout(context.WithoutCancel(r.Context()), streamPublishTimeout)
		defer cancel()
	} else if err := r.Context().Err(); err != nil {
		history("interrupted", err.Error(), 0)
		return
	}
	recording := model.Recording{CollectionID: collectionID, Key: key, Route: r.URL.Path, Request: clone(original), MatchingInput: clone(canonical), UpstreamIdentity: identity, Streaming: streaming}
	entry, err := h.db.Publish(publishCtx, recording, revision)
	if err != nil {
		outcome := "error"
		if clientGone(publishCtx, err) {
			outcome = "interrupted"
		}
		history(outcome, err.Error(), 0)
		return
	}
	detail := ""
	if streaming && (hungUpAfterCompletion || r.Context().Err() != nil) {
		detail = "caller disconnected after the complete stream"
	}
	history("recorded", detail, entry.Recording.ID)
}

// streamPublishTimeout bounds publication of a complete stream, which no
// longer follows the caller's context.
const streamPublishTimeout = 30 * time.Second

// errClientWrite marks a failed write to the caller's connection, which only
// happens once the caller has gone away.
var errClientWrite = errors.New("client connection closed")

func clientWriteError(err error) error {
	return fmt.Errorf("%w: %w", errClientWrite, err)
}

// clientGone reports whether a failure was caused by the caller disconnecting
// (or the server shutting down) rather than by the upstream or storage.
func clientGone(ctx context.Context, err error) bool {
	return ctx.Err() != nil || errors.Is(err, context.Canceled) || errors.Is(err, errClientWrite)
}

// copyToClient copies a non-streaming body, tagging failed writes to the caller
// so they are not mistaken for upstream read failures.
func copyToClient(dst io.Writer, src io.Reader) (int64, error) {
	var written int64
	buf := make([]byte, 32*1024)
	for {
		n, err := src.Read(buf)
		if n > 0 {
			m, writeErr := dst.Write(buf[:n])
			written += int64(m)
			if writeErr != nil {
				return written, clientWriteError(writeErr)
			}
			if m != n {
				return written, clientWriteError(io.ErrShortWrite)
			}
		}
		if errors.Is(err, io.EOF) {
			return written, nil
		}
		if err != nil {
			return written, err
		}
	}
}

// decodedBody returns the upstream body without content coding. The default
// transport already removes gzip it requested; this covers custom transports
// and upstreams that compress without being asked.
func decodedBody(resp *http.Response) (io.ReadCloser, error) {
	encoding := strings.ToLower(strings.TrimSpace(resp.Header.Get("Content-Encoding")))
	switch encoding {
	case "", "identity":
		return io.NopCloser(resp.Body), nil
	case "gzip", "x-gzip":
		reader, err := gzip.NewReader(resp.Body)
		if err != nil {
			return nil, fmt.Errorf("decode gzip upstream response: %w", err)
		}
		return reader, nil
	case "deflate":
		reader, err := zlib.NewReader(resp.Body)
		if err != nil {
			return nil, fmt.Errorf("decode deflate upstream response: %w", err)
		}
		return reader, nil
	default:
		return nil, fmt.Errorf("unsupported upstream Content-Encoding %q", encoding)
	}
}

func hasSSEData(frame string) bool {
	for _, line := range strings.Split(strings.ReplaceAll(frame, "\r\n", "\n"), "\n") {
		if line == "data" || strings.HasPrefix(line, "data:") {
			return true
		}
	}
	return false
}

func (h *handler) recordedStateReference(ctx context.Context, collectionID int64, body []byte) (bool, string, error) {
	refs, err := match.StateReferences(body)
	if err != nil {
		return false, "", err
	}
	if len(refs) == 0 {
		return false, "", nil
	}
	collections, err := h.db.Collections(ctx)
	if err != nil {
		return false, "", err
	}
	// Check the active collection first, then every other collection. Provider
	// state recorded during an earlier collection remains synthetic even after
	// the operator switches collections.
	ids := make([]int64, 0, len(collections))
	ids = append(ids, collectionID)
	for _, collection := range collections {
		if collection.ID != collectionID {
			ids = append(ids, collection.ID)
		}
	}
	for _, ref := range refs {
		for _, id := range ids {
			yes, err := h.db.HasProviderState(ctx, id, ref)
			if err != nil {
				return false, "", err
			}
			if yes {
				return true, ref, nil
			}
		}
	}
	return false, "", nil
}

func (h *handler) addHistory(ctx context.Context, item model.History) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
	defer cancel()
	_ = h.db.AddHistory(ctx, item)
}

var responseHeaderAllowlist = map[string]bool{
	"content-type": true, "cache-control": true, "request-id": true, "x-request-id": true,
	"openai-request-id": true, "anthropic-request-id": true,
}

func allowedHeaders(src http.Header) map[string]string {
	out := make(map[string]string)
	for k, values := range src {
		if responseHeaderAllowlist[strings.ToLower(k)] {
			out[http.CanonicalHeaderKey(k)] = strings.Join(values, ", ")
		}
	}
	return out
}
func copyResponseHeaders(dst http.Header, src map[string]string) {
	for k, v := range src {
		dst.Set(k, v)
	}
}
func clone(value []byte) json.RawMessage { return append(json.RawMessage(nil), value...) }
func ensureJSONEOF(dec *json.Decoder) error {
	var extra any
	err := dec.Decode(&extra)
	if errors.Is(err, io.EOF) {
		return nil
	}
	if err == nil {
		return errors.New("extra JSON value")
	}
	return err
}

type limitedCapture struct {
	buf      bytes.Buffer
	limit    int64
	written  int64
	overflow bool
}

func (w *limitedCapture) Write(p []byte) (int, error) {
	remaining := w.limit - w.written
	if remaining > 0 {
		n := int64(len(p))
		if n > remaining {
			n = remaining
		}
		_, _ = w.buf.Write(p[:n])
		w.written += n
	}
	if int64(len(p)) > remaining {
		w.overflow = true
	}
	return len(p), nil
}

func setCredential(header http.Header, route, key string) {
	if key == "" {
		return
	}
	if route == "/v1/messages" {
		header.Set("X-Api-Key", key)
	} else {
		header.Set("Authorization", "Bearer "+key)
	}
}

func normalizeUpstream(u Upstream) Upstream {
	keys := make([]string, 0, len(u.Headers))
	for key := range u.Headers {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	normal := make(map[string]string, len(keys))
	for _, key := range keys {
		normal[http.CanonicalHeaderKey(key)] = u.Headers[key]
	}
	u.Headers = normal
	return u
}

func scaledDelay(deltaMS int64, multiplier float64) time.Duration {
	if deltaMS <= 0 || multiplier <= 0 {
		return 0
	}
	nanos := float64(deltaMS) * float64(time.Millisecond) * multiplier
	if math.IsInf(nanos, 0) || nanos >= float64(math.MaxInt64) {
		return time.Duration(math.MaxInt64)
	}
	return time.Duration(nanos)
}

func upstreamIdentity(u Upstream) string {
	type pair struct {
		Name  string `json:"name"`
		Value string `json:"value"`
	}
	pairs := make([]pair, 0, len(u.Headers))
	for key, value := range u.Headers {
		lower := strings.ToLower(key)
		if !secretHeader(lower) {
			pairs = append(pairs, pair{lower, value})
		}
	}
	sort.Slice(pairs, func(i, j int) bool { return pairs[i].Name < pairs[j].Name })
	encoded, _ := json.Marshal(struct {
		Version  int    `json:"version"`
		URL      string `json:"url"`
		Identity string `json:"identity"`
		Headers  []pair `json:"headers"`
	}{1, u.URL, u.Identity, pairs})
	return string(encoded)
}

func secretHeader(lower string) bool {
	switch strings.ToLower(lower) {
	case "authorization", "proxy-authorization", "x-api-key", "api-key", "x-goog-api-key", "cookie", "set-cookie":
		return true
	default:
		return false
	}
}

func writeError(w http.ResponseWriter, status int, code, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]string{"code": code, "message": message}})
}

// sseParser recognizes both LF and CRLF event delimiters while retaining bytes.
type sseParser struct {
	mu       sync.Mutex
	buf      []byte
	overflow bool
}

func newSSEParser() *sseParser { return &sseParser{} }
func (p *sseParser) Push(chunk []byte) []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.overflow {
		return nil
	}
	p.buf = append(p.buf, chunk...)
	if len(p.buf) > maxRecordedResponseBytes {
		p.buf = nil
		p.overflow = true
		return nil
	}
	var frames []string
	for {
		index, size := nextDelimiter(p.buf)
		if index < 0 {
			break
		}
		end := index + size
		frames = append(frames, string(p.buf[:end]))
		p.buf = append(p.buf[:0], p.buf[end:]...)
	}
	return frames
}
func (p *sseParser) Pending() bool  { p.mu.Lock(); defer p.mu.Unlock(); return len(p.buf) != 0 }
func (p *sseParser) Overflow() bool { p.mu.Lock(); defer p.mu.Unlock(); return p.overflow }
func nextDelimiter(data []byte) (int, int) {
	lf := bytes.Index(data, []byte("\n\n"))
	crlf := bytes.Index(data, []byte("\r\n\r\n"))
	if lf < 0 {
		if crlf < 0 {
			return -1, 0
		}
		return crlf, 4
	}
	if crlf < 0 || lf < crlf {
		return lf, 2
	}
	return crlf, 4
}
