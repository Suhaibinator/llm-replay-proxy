package store

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/klauspost/compress/zstd"
	"github.com/local/llm-replay-proxy/internal/model"
)

// Format 6 response blobs have a codec byte, an unsigned-varint decoded size,
// and the raw or zstd-compressed payload. Compression never parses provider
// JSON. Small and incompressible payloads stay raw.
const responseWindow = 8 << 20

var responseEncoder = mustEncoder(zstd.WithWindowSize(responseWindow))
var responseDecoder = func() *zstd.Decoder {
	d, err := zstd.NewReader(nil, zstd.WithDecoderConcurrency(1),
		zstd.WithDecoderMaxMemory(maxEventsBytes), zstd.WithDecoderMaxWindow(responseWindow), zstd.WithDecodeAllCapLimit(true))
	if err != nil {
		panic(err)
	}
	return d
}()

var errCorruptResponse = errors.New("corrupt response storage")

func packResponse(raw []byte, limit int) ([]byte, error) {
	if len(raw) > limit {
		return nil, fmt.Errorf("response exceeds storage limit %d", limit)
	}
	codec, data := byte(codecRaw), raw
	if len(raw) >= 128 {
		if packed := responseEncoder.EncodeAll(raw, nil); len(packed) < len(raw)-len(raw)/8 {
			codec, data = codecZstd, packed
		}
	}
	out := binary.AppendUvarint([]byte{codec}, uint64(len(raw)))
	return append(out, data...), nil
}

func unpackResponse(packed []byte, limit int) ([]byte, error) {
	if len(packed) < 2 {
		return nil, fmt.Errorf("%w: missing payload header", errCorruptResponse)
	}
	size, n := binary.Uvarint(packed[1:])
	if n <= 0 || size > uint64(limit) {
		return nil, fmt.Errorf("%w: invalid decoded size", errCorruptResponse)
	}
	data := packed[1+n:]
	var raw []byte
	switch packed[0] {
	case codecRaw:
		raw = data
	case codecZstd:
		var header zstd.Header
		// Zstd omits the content-size field for some small frames. Their
		// envelope still bounds eager allocation and decoder output. Large
		// frames produced by this encoder always carry a content size.
		if err := header.Decode(data); err != nil || header.Skippable ||
			(header.HasFCS && header.FrameContentSize != size) || (!header.HasFCS && size > responseWindow) {
			return nil, fmt.Errorf("%w: invalid compressed frame header", errCorruptResponse)
		}
		var err error
		if size <= responseWindow {
			// Small frames use the fast stateless decoder. Validate their frame
			// header before allocating, and cap output to the declared size.
			raw, err = responseDecoder.DecodeAll(data, make([]byte, 0, int(size)))
		} else {
			// A snapshot's size field is untrusted. Grow large outputs as bytes
			// actually decode rather than allocating up to 1 GiB from a header.
			var decoder *zstd.Decoder
			decoder, err = zstd.NewReader(bytes.NewReader(data), zstd.WithDecoderConcurrency(1),
				zstd.WithDecoderMaxMemory(uint64(limit)), zstd.WithDecoderMaxWindow(responseWindow), zstd.WithDecoderLowmem(true))
			if err == nil {
				raw, err = io.ReadAll(io.LimitReader(decoder, int64(size)+1))
				decoder.Close()
			}
		}
		if err != nil {
			return nil, fmt.Errorf("%w: %w", errCorruptResponse, err)
		}
	default:
		return nil, fmt.Errorf("%w: unknown codec %d", errCorruptResponse, packed[0])
	}
	if uint64(len(raw)) != size {
		return nil, fmt.Errorf("%w: decoded size mismatch", errCorruptResponse)
	}
	return raw, nil
}

func encodeResponseBody(body string) ([]byte, error) {
	return packResponse([]byte(body), maxBodyBytes)
}

func decodeResponseBody(packed []byte) (string, error) {
	raw, err := unpackResponse(packed, maxBodyBytes)
	return string(raw), err
}

const maxEventOffset = int64((1<<63 - 1) / time.Millisecond)

// Events are a count followed by (offset delta, byte length, raw frame) tuples.
// The first delta is the original first offset. Zero deltas preserve equal
// timestamps. Raw bytes retain delimiters, BOMs, comments, and invalid UTF-8;
// JSON string encoding would replace invalid UTF-8 with U+FFFD.
func encodeEvents(events []model.Event) ([]byte, error) {
	out := binary.AppendUvarint(nil, uint64(len(events)))
	var previous int64
	for _, event := range events {
		if event.OffsetMS < previous || event.OffsetMS > maxEventOffset {
			return nil, errors.New("invalid event offset")
		}
		if len(event.Data) < 2 {
			return nil, errors.New("event is missing its frame delimiter")
		}
		overhead := binary.MaxVarintLen64 * 2
		if len(event.Data) > maxEventsBytes-overhead-len(out) {
			return nil, errors.New("events exceed storage limit")
		}
		out = binary.AppendUvarint(out, uint64(event.OffsetMS-previous))
		out = binary.AppendUvarint(out, uint64(len(event.Data)))
		out = append(out, event.Data...)
		previous = event.OffsetMS
	}
	return packResponse(out, maxEventsBytes)
}

func decodeEvents(packed []byte) ([]model.Event, error) {
	raw, err := unpackResponse(packed, maxEventsBytes)
	if err != nil {
		return nil, err
	}
	read := func() (uint64, error) {
		v, n := binary.Uvarint(raw)
		if n <= 0 {
			return 0, fmt.Errorf("%w: unreadable event varint", errCorruptResponse)
		}
		raw = raw[n:]
		return v, nil
	}
	count, err := read()
	if err != nil {
		return nil, err
	}
	// Each frame needs two delimiter bytes and at least two varints. Do not
	// preallocate based on an untrusted count: a truncated snapshot must not
	// cause a huge allocation.
	if count > uint64(len(raw)/4) {
		return nil, fmt.Errorf("%w: impossible event count", errCorruptResponse)
	}
	events := make([]model.Event, 0)
	var offset uint64
	for range count {
		delta, err := read()
		if err != nil {
			return nil, err
		}
		if delta > uint64(maxEventOffset)-offset {
			return nil, fmt.Errorf("%w: event offset overflow", errCorruptResponse)
		}
		offset += delta
		size, err := read()
		if err != nil {
			return nil, err
		}
		if size < 2 || size > uint64(len(raw)) {
			return nil, fmt.Errorf("%w: truncated event", errCorruptResponse)
		}
		events = append(events, model.Event{Data: string(raw[:int(size)]), OffsetMS: int64(offset)})
		raw = raw[int(size):]
	}
	if len(raw) != 0 {
		return nil, fmt.Errorf("%w: trailing event bytes", errCorruptResponse)
	}
	return events, nil
}
