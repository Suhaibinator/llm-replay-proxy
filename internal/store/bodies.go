package store

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/klauspost/compress/zstd"
	"github.com/local/llm-replay-proxy/internal/model"
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
	chunkEncoder  = mustEncoder(zstd.WithWindowSize(chunkMax))
	chunkDecoder  = mustDecoder(chunkMax)
	eventsEncoder = mustEncoder()
	eventsDecoder = mustDecoder(maxEventsBytes)
)

func mustEncoder(opts ...zstd.EOption) *zstd.Encoder {
	e, err := zstd.NewWriter(nil, append([]zstd.EOption{zstd.WithEncoderLevel(zstd.SpeedFastest), zstd.WithEncoderConcurrency(1)}, opts...)...)
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

type querier interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// internBody stores raw (if it is not already stored) and returns its body id.
// A body seen before costs one hash and one indexed lookup; a new one stores
// only the chunks no other body already holds.
func internBody(ctx context.Context, tx *sql.Tx, raw []byte) (int64, error) {
	sum := sha256.Sum256(raw)
	var id int64
	err := tx.QueryRowContext(ctx, "SELECT id FROM bodies WHERE hash=?", sum[:]).Scan(&id)
	if err == nil {
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
			// Keep compression only when it pays for the decode on read;
			// base64 media and encrypted reasoning barely shrink.
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
	res, err := tx.ExecContext(ctx, "INSERT INTO bodies(hash,size,chunks) VALUES(?,?,?)", sum[:], len(raw), manifest)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// loadBody reassembles a body and verifies it against its stored size and hash,
// so a damaged or forged chunk can never be returned as request bytes.
func loadBody(ctx context.Context, q querier, id int64) (json.RawMessage, error) {
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
	out := make([]byte, 0, size)
	var scratch []byte
	for len(manifest) > 0 {
		cid, n := binary.Uvarint(manifest)
		if n <= 0 {
			return nil, fmt.Errorf("%w: body %d has an unreadable chunk list", errCorruptBody, id)
		}
		manifest = manifest[n:]
		var codec int
		var chunkSize int64
		var data []byte
		err = q.QueryRowContext(ctx, "SELECT codec,size,data FROM chunks WHERE id=?", int64(cid)).Scan(&codec, &chunkSize, &data)
		if errors.Is(err, sql.ErrNoRows) {
			return nil, fmt.Errorf("%w: body %d references missing chunk %d", errCorruptBody, id, cid)
		}
		if err != nil {
			return nil, err
		}
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
type bodyCache map[int64]json.RawMessage

func (c bodyCache) load(ctx context.Context, q querier, id sql.NullInt64) (json.RawMessage, error) {
	if !id.Valid {
		return json.RawMessage(`null`), nil
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

// Events are kept frame for frame (replay timing, validation, text edits and
// provider-state lookups all work per frame) and stored as one zstd blob per
// revision: SSE JSON repeats itself heavily and compresses well.
func encodeEvents(events []model.Event) ([]byte, error) {
	j, err := json.Marshal(events)
	if err != nil {
		return nil, err
	}
	return eventsEncoder.EncodeAll(j, nil), nil
}

func decodeEvents(packed []byte) ([]model.Event, error) {
	j, err := eventsDecoder.DecodeAll(packed, nil)
	if err != nil {
		return nil, fmt.Errorf("decode events: %w", err)
	}
	var events []model.Event
	if err = json.Unmarshal(j, &events); err != nil {
		return nil, fmt.Errorf("decode events: %w", err)
	}
	return events, nil
}
