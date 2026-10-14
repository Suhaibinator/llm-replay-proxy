package store

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/binary"
	"encoding/json/jsontext"
	"errors"
	"fmt"

	"github.com/klauspost/compress/zstd"
	"github.com/local/llm-replay-proxy/internal/protocol"
)

// Request bodies are content-addressed. Each distinct body is stored once, as
// an ordered list of content-defined chunks, and chunks are shared between
// bodies. A conversation that re-sends its whole thread on every turn therefore
// stores only each turn's new bytes, and a payload repeated across requests (an
// image, the tool list, the instructions) is stored once wherever it appears.
//
// Chunk boundaries depend only on the bytes around them (a gear rolling hash),
// so an insertion or appended turn shifts nothing outside the chunks it touches.
// Reads follow the stored chunk list, so changing these parameters later only
// affects deduplication, never the ability to read existing bodies.
const (
	chunkMin      = 2 << 10
	chunkMax      = 64 << 10
	chunkMaskBits = 13 // boundaries every ~8 KiB past chunkMin

	codecRaw  = 0
	codecZstd = 1

	// Bounds for reading untrusted snapshots; generous next to what the proxy
	// records (64 MiB requests, 256 MiB responses).
	maxBodyBytes   = 256 << 20
	maxEventsBytes = 1 << 30
)

// errCorruptBody wraps every inconsistency found while reassembling a body.
var errCorruptBody = errors.New("corrupt request body storage")

var gear = func() (t [256]uint64) {
	// splitmix64 from a fixed seed: the table must never change between builds.
	x := uint64(0x6a09e667f3bcc908)
	for i := range t {
		x += 0x9e3779b97f4a7c15
		z := x
		z = (z ^ z>>30) * 0xbf58476d1ce4e5b9
		z = (z ^ z>>27) * 0x94d049bb133111eb
		t[i] = z ^ z>>31
	}
	return
}()

// nextChunk returns the length of the chunk at the start of b.
func nextChunk(b []byte) int {
	n := len(b)
	if n <= chunkMin {
		return n
	}
	if n > chunkMax {
		n = chunkMax
	}
	var h uint64
	for i := chunkMin; i < n; i++ {
		h = h<<1 + gear[b[i]]
		if h>>(64-chunkMaskBits) == 0 {
			return i + 1
		}
	}
	return n
}

var (
	// A chunk's window never needs to exceed the chunk, which lets its decoder
	// refuse anything that would expand past chunkMax.
	chunkEncoder = mustEncoder(zstd.WithWindowSize(chunkMax))
	chunkDecoder = mustDecoder(chunkMax)
)

// mustEncoder uses SpeedBetterCompression because the faster levels store
// base64 uncompressed: it has no repeats to find, and only entropy coding
// recovers its 6 bits per byte. Base64 is most of what a request stores
// (inline images, files, audio, encrypted reasoning), and this level packs it
// to about three quarters, as storing the decoded bytes would. Text costs the
// same as at SpeedFastest, and only chunks not already stored are encoded.
func mustEncoder(opts ...zstd.EOption) *zstd.Encoder {
	e, err := zstd.NewWriter(nil, append([]zstd.EOption{zstd.WithEncoderLevel(zstd.SpeedBetterCompression), zstd.WithEncoderConcurrency(1)}, opts...)...)
	if err != nil {
		panic(err)
	}
	return e
}

func mustDecoder(limit uint64) *zstd.Decoder {
	d, err := zstd.NewReader(nil, zstd.WithDecoderConcurrency(0), zstd.WithDecoderMaxMemory(limit))
	if err != nil {
		panic(err)
	}
	return d
}

// querier is a connection pool, connection or transaction.
type querier interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// preparedBody is a request body ready to store: hashed, and summarized
// when the store does not yet hold a current summary of it.
type preparedBody struct {
	raw     []byte
	hash    [sha256.Size]byte
	summary *encodedSummary
}

// prepareBody does the CPU work of storing raw before the writer is
// involved: hashing it and, unless the store already holds a current
// summary of it, summarizing it for route.
func (s *Store) prepareBody(ctx context.Context, route string, raw []byte) (preparedBody, error) {
	b := preparedBody{raw: raw, hash: sha256.Sum256(raw)}
	var version int64
	err := s.rdb.QueryRowContext(ctx, "SELECT summary_v FROM bodies WHERE hash=?", b.hash[:]).Scan(&version)
	if err == nil && version == summaryVersion {
		return b, nil
	}
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return b, err
	}
	sum, err := encodeSummary(protocol.SummarizeRequest(route, raw))
	b.summary = &sum
	return b, err
}

// internBody stores raw (if it is not already stored) and returns its body id.
func internBody(ctx context.Context, tx *sql.Tx, raw []byte) (int64, error) {
	return storeBody(ctx, tx, preparedBody{raw: raw, hash: sha256.Sum256(raw)})
}

// storeBody stores b (if it is not already stored) and returns its body id.
// A body seen before costs one indexed lookup; a new one stores only the
// chunks no other body already holds. b's summary, if any, is saved with a
// new body or replaces an outdated one.
func storeBody(ctx context.Context, tx *sql.Tx, b preparedBody) (int64, error) {
	raw, sum := b.raw, b.hash
	var id, version int64
	err := tx.QueryRowContext(ctx, "SELECT id,summary_v FROM bodies WHERE hash=?", sum[:]).Scan(&id, &version)
	if err == nil {
		if b.summary != nil && version != summaryVersion {
			if _, err = tx.ExecContext(ctx, "UPDATE bodies SET summary=?,summary_v=?,thread=? WHERE id=?", b.summary.json, summaryVersion, b.summary.thread, id); err != nil {
				return 0, err
			}
		}
		return id, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return 0, err
	}
	find, err := tx.PrepareContext(ctx, "SELECT id FROM chunks WHERE hash=?")
	if err != nil {
		return 0, err
	}
	defer find.Close()
	insert, err := tx.PrepareContext(ctx, "INSERT INTO chunks(hash,codec,size,data) VALUES(?,?,?,?)")
	if err != nil {
		return 0, err
	}
	defer insert.Close()
	manifest := make([]byte, 0, len(raw)/(8<<10)*3+8)
	for rest := raw; len(rest) > 0; {
		n := nextChunk(rest)
		chunk := rest[:n]
		rest = rest[n:]
		chunkSum := sha256.Sum256(chunk)
		var cid int64
		err = find.QueryRowContext(ctx, chunkSum[:]).Scan(&cid)
		if errors.Is(err, sql.ErrNoRows) {
			codec, data := codecRaw, chunk
			// Keep compression only when it pays for the decode on read.
			if packed := chunkEncoder.EncodeAll(chunk, nil); len(packed) < len(chunk)-len(chunk)/8 {
				codec, data = codecZstd, packed
			}
			res, e := insert.ExecContext(ctx, chunkSum[:], codec, len(chunk), data)
			if e != nil {
				return 0, e
			}
			cid, err = res.LastInsertId()
		}
		if err != nil {
			return 0, err
		}
		manifest = binary.AppendUvarint(manifest, uint64(cid))
	}
	var summary, thread sql.NullString
	version = 0
	if b.summary != nil {
		summary, thread, version = sql.NullString{String: b.summary.json, Valid: true}, b.summary.thread, summaryVersion
	}
	res, err := tx.ExecContext(ctx, "INSERT INTO bodies(hash,size,chunks,summary,summary_v,thread) VALUES(?,?,?,?,?,?)", sum[:], len(raw), manifest, summary, version, thread)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// loadBody reassembles a body and verifies it against its stored size and hash,
// so a damaged or forged chunk can never be returned as request bytes.
func loadBody(ctx context.Context, q querier, id int64) (jsontext.Value, error) {
	var hash, manifest []byte
	var size int64
	err := q.QueryRowContext(ctx, "SELECT hash,size,chunks FROM bodies WHERE id=?", id).Scan(&hash, &size, &manifest)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("%w: body %d is missing", errCorruptBody, id)
	}
	if err != nil {
		return nil, err
	}
	if size < 0 || size > maxBodyBytes {
		return nil, fmt.Errorf("%w: body %d has size %d", errCorruptBody, id, size)
	}
	ids, err := manifestIDs(manifest)
	if err != nil {
		return nil, fmt.Errorf("%w: body %d has an unreadable chunk list", errCorruptBody, id)
	}
	// Fetch the chunks in a few statements rather than one per chunk: each
	// statement outside a transaction is a read transaction of its own, with
	// its own round of locks.
	type chunk struct {
		codec int
		size  int64
		data  []byte
	}
	chunks := make(map[int64]*chunk, len(ids))
	unique := make([]int64, 0, len(ids))
	for _, cid := range ids {
		if _, seen := chunks[cid]; !seen {
			chunks[cid] = nil
			unique = append(unique, cid)
		}
	}
	err = inBatches(unique, func(args []any, list string) error {
		rows, err := q.QueryContext(ctx, "SELECT id,codec,size,data FROM chunks WHERE id IN ("+list+")", args...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var cid int64
			c := &chunk{}
			if err = rows.Scan(&cid, &c.codec, &c.size, &c.data); err != nil {
				return err
			}
			chunks[cid] = c
		}
		return rows.Err()
	})
	if err != nil {
		return nil, err
	}
	out := make([]byte, 0, size)
	var scratch []byte
	for _, cid := range ids {
		c := chunks[cid]
		if c == nil {
			return nil, fmt.Errorf("%w: body %d references missing chunk %d", errCorruptBody, id, cid)
		}
		codec, chunkSize, data := c.codec, c.size, c.data
		if chunkSize <= 0 || chunkSize > chunkMax || int64(len(out))+chunkSize > size {
			return nil, fmt.Errorf("%w: chunk %d has size %d", errCorruptBody, cid, chunkSize)
		}
		start := len(out)
		switch codec {
		case codecRaw:
			out = append(out, data...)
		case codecZstd:
			// The decoder's limit counts bytes already in dst, so decode
			// each chunk on its own and append.
			if scratch, err = chunkDecoder.DecodeAll(data, scratch[:0]); err != nil {
				return nil, fmt.Errorf("%w: chunk %d: %w", errCorruptBody, cid, err)
			}
			out = append(out, scratch...)
		default:
			return nil, fmt.Errorf("%w: chunk %d has unknown codec %d", errCorruptBody, cid, codec)
		}
		if int64(len(out)-start) != chunkSize {
			return nil, fmt.Errorf("%w: chunk %d decoded to the wrong size", errCorruptBody, cid)
		}
	}
	if sum := sha256.Sum256(out); int64(len(out)) != size || !bytes.Equal(sum[:], hash) {
		return nil, fmt.Errorf("%w: body %d does not match its hash", errCorruptBody, id)
	}
	return out, nil
}

// bodyCache loads each body once per read, since many rows share a body.
type bodyCache map[int64]jsontext.Value

func (c bodyCache) load(ctx context.Context, q querier, id sql.NullInt64) (jsontext.Value, error) {
	if !id.Valid {
		return jsontext.Value(`null`), nil
	}
	if b, ok := c[id.Int64]; ok {
		return b, nil
	}
	b, err := loadBody(ctx, q, id.Int64)
	if err != nil {
		return nil, err
	}
	c[id.Int64] = b
	return b, nil
}

// sweepBodies deletes bodies no revision or history row references, then
// chunks no remaining body lists. Writes never maintain reference counts; this
// runs only where rows are deleted.
func sweepBodies(ctx context.Context, tx *sql.Tx) error {
	if _, err := tx.ExecContext(ctx, `DELETE FROM bodies WHERE id NOT IN (SELECT request_body FROM revisions)
AND id NOT IN (SELECT request_body FROM history WHERE request_body IS NOT NULL)`); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, "CREATE TEMP TABLE live_chunks(id INTEGER PRIMARY KEY)"); err != nil {
		return err
	}
	defer tx.ExecContext(ctx, "DROP TABLE temp.live_chunks")
	rows, err := tx.QueryContext(ctx, "SELECT chunks FROM bodies")
	if err != nil {
		return err
	}
	live := map[uint64]struct{}{}
	for rows.Next() {
		var manifest []byte
		if err = rows.Scan(&manifest); err != nil {
			rows.Close()
			return err
		}
		for len(manifest) > 0 {
			cid, n := binary.Uvarint(manifest)
			if n <= 0 {
				rows.Close()
				return errCorruptBody
			}
			live[cid] = struct{}{}
			manifest = manifest[n:]
		}
	}
	if err = rows.Close(); err != nil {
		return err
	}
	mark, err := tx.PrepareContext(ctx, "INSERT INTO temp.live_chunks(id) VALUES(?)")
	if err != nil {
		return err
	}
	defer mark.Close()
	for cid := range live {
		if _, err = mark.ExecContext(ctx, int64(cid)); err != nil {
			return err
		}
	}
	_, err = tx.ExecContext(ctx, "DELETE FROM chunks WHERE id NOT IN (SELECT id FROM temp.live_chunks)")
	return err
}
