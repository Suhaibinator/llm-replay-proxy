package store

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"

	"github.com/local/llm-replay-proxy/internal/model"
)

func TestResponseBodyCodecExactBytes(t *testing.T) {
	random := make([]byte, 64<<10)
	rand.New(rand.NewSource(1)).Read(random)
	for _, raw := range [][]byte{
		{}, []byte(`{ "n":900719925474099312345.00, "text":"\u00e9" }`),
		[]byte(strings.Repeat("x", 200)), // zstd can omit content size on small frames
		[]byte(strings.Repeat("output text ", 10000)), random,
		{0, 0xff, 0xfe, '\r', '\n'},
	} {
		packed, err := encodeResponseBody(string(raw))
		if err != nil {
			t.Fatal(err)
		}
		got, err := decodeResponseBody(packed)
		if err != nil || !bytes.Equal([]byte(got), raw) {
			t.Fatalf("round trip %d bytes: %v", len(raw), err)
		}
	}
	packed, _ := encodeResponseBody(strings.Repeat("output text ", 10000))
	if packed[0] != codecZstd || len(packed) > 1000 {
		t.Fatalf("compressible response stored in %d bytes, codec %d", len(packed), packed[0])
	}
	packed, _ = encodeResponseBody(string(random))
	if packed[0] != codecRaw {
		t.Fatal("incompressible response should stay raw")
	}
}

func TestEventsCodecExactFramesAndOffsets(t *testing.T) {
	events := []model.Event{
		{Data: "\xef\xbb\xbf: keepalive\r\n\r\n", OffsetMS: 17},
		{Data: ": invalid utf8 " + string([]byte{0xff}) + "\n\n", OffsetMS: 17},
		{Data: "event: unknown\nid: abc\nretry: 500\ndata: { \"n\":900719925474099312345,\ndata: \"s\":\"\\u00e9\" }\n\n", OffsetMS: 99},
		{Data: "data: [DONE]\n\n", OffsetMS: maxEventOffset},
	}
	packed, err := encodeEvents(events)
	if err != nil {
		t.Fatal(err)
	}
	got, err := decodeEvents(packed)
	if err != nil || !reflect.DeepEqual(got, events) {
		t.Fatalf("events changed: %+v (%v)", got, err)
	}
	for _, events := range [][]model.Event{nil, {}} {
		packed, err := encodeEvents(events)
		if err != nil {
			t.Fatal(err)
		}
		got, err := decodeEvents(packed)
		if err != nil || len(got) != 0 {
			t.Fatalf("empty stream: %v", err)
		}
	}
	for _, events := range [][]model.Event{
		{{OffsetMS: -1}}, {{OffsetMS: maxEventOffset + 1}}, {{OffsetMS: 2}, {OffsetMS: 1}},
	} {
		if _, err := encodeEvents(events); err == nil {
			t.Fatal("invalid offsets accepted")
		}
	}
}

func TestResponseCodecRejectsCorruption(t *testing.T) {
	compressed, _ := encodeResponseBody(strings.Repeat("test ", 1000))
	for _, packed := range [][]byte{
		nil, {codecRaw}, {42, 0}, {codecRaw, 0x80},
		{codecRaw, 3, 'x'}, {codecRaw, 0, 'x'},
		binary.AppendUvarint([]byte{codecRaw}, maxBodyBytes+1),
		compressed[:len(compressed)-1],
		append([]byte{codecZstd, 1}, compressed[3:]...),
	} {
		if _, err := decodeResponseBody(packed); !errors.Is(err, errCorruptResponse) {
			t.Fatalf("bad payload %x: %v", packed, err)
		}
	}
	// Corruption inside the compressed stream must fail its checksum.
	compressed[len(compressed)-1] ^= 1
	if _, err := decodeResponseBody(compressed); err == nil {
		t.Fatal("bad checksum accepted")
	}
	for _, raw := range [][]byte{
		{}, {0x80}, {2, 0, 0}, {1, 0, 4, 'x'}, {0, 'x'},
		binary.AppendUvarint([]byte{1}, uint64(maxEventOffset)+1),
		{1, 0, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 2},
	} {
		packed, err := packResponse(raw, maxEventsBytes)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := decodeEvents(packed); !errors.Is(err, errCorruptResponse) {
			t.Fatalf("bad event payload %x: %v", raw, err)
		}
	}
}

func TestMalformedResponseDoesNotAllocateDeclaredSize(t *testing.T) {
	// Both a junk stream and a syntactically valid frame header declaring a
	// huge single segment must fail without allocating the declared 1 GiB.
	for _, data := range [][]byte{{0xff}, {0x28, 0xb5, 0x2f, 0xfd, 0xa0, 0, 0, 0, 0x40}} {
		packed := append(binary.AppendUvarint([]byte{codecZstd}, maxEventsBytes), data...)
		var before, after runtime.MemStats
		runtime.ReadMemStats(&before)
		_, err := decodeEvents(packed)
		runtime.ReadMemStats(&after)
		if !errors.Is(err, errCorruptResponse) {
			t.Fatalf("malformed frame accepted: %v", err)
		}
		if allocated := after.TotalAlloc - before.TotalAlloc; allocated > 16<<20 {
			t.Fatalf("malformed frame allocated %d bytes", allocated)
		}
	}
}

func TestLargeResponseProgressiveDecode(t *testing.T) {
	raw := strings.Repeat("response bytes ", responseWindow/15+100)
	packed, err := encodeResponseBody(raw)
	if err != nil {
		t.Fatal(err)
	}
	got, err := decodeResponseBody(packed)
	if err != nil || got != raw {
		t.Fatalf("large response changed: %v", err)
	}
	packed[len(packed)-1] ^= 1
	if _, err := decodeResponseBody(packed); !errors.Is(err, errCorruptResponse) {
		t.Fatalf("large checksum failure ignored: %v", err)
	}
}

func TestResponseStorageLifecycleExact(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "responses.sqlite")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	settings, _ := s.Settings(ctx)
	chat := chatRevision()
	chat.Body = "{ \"id\":\"x\", \"choices\":[{\"message\":{\"role\":\"assistant\",\"content\":\"\\u00e9 <&>\"},\"finish_reason\":\"stop\"}],\"unknown\":900719925474099312345.00 }\n"
	first := publishOne(t, s, chatRecording(settings.ActiveCollectionID), chat)
	changed := chat
	changed.Body = strings.Replace(chat.Body, "\\u00e9 <&>", "edited", 1)
	second := publishOne(t, s, first.Recording, changed)
	if _, err = s.Restore(ctx, first.Recording.ID, first.Revision.ID); err != nil {
		t.Fatal(err)
	}
	stream := testRevision("resp_exact", "recorded")
	stream.Events = append([]model.Event{{Data: "\xef\xbb\xbf: keepalive " + string([]byte{0xff}) + "\r\n\r\n", OffsetMS: 0}}, stream.Events...)
	stream.Events[2].OffsetMS = stream.Events[1].OffsetMS
	streamed := publishOne(t, s, testRecording(settings.ActiveCollectionID, "stream"), stream)
	check := func(db *Store, cid int64) {
		t.Helper()
		for _, original := range []model.Entry{first, streamed} {
			got, err := db.Lookup(ctx, cid, original.Recording.Key)
			if err != nil || got.Revision.Body != original.Revision.Body || !reflect.DeepEqual(got.Revision.Events, original.Revision.Events) || !reflect.DeepEqual(got.Revision.Headers, original.Revision.Headers) {
				t.Fatalf("response changed: %+v, %v", got.Revision, err)
			}
			if got.Revision.Status != original.Revision.Status || got.Revision.Source != original.Revision.Source || got.Revision.CreatedAt != original.Revision.CreatedAt {
				t.Fatal("metadata changed")
			}
			full, err := db.Get(ctx, got.Recording.ID)
			if err != nil || !bytes.Equal(full.Revision.Request, original.Revision.Request) {
				t.Fatalf("provenance changed: %v", err)
			}
		}
		entry, _ := db.Lookup(ctx, cid, first.Recording.Key)
		revisions, err := db.Revisions(ctx, entry.Recording.ID)
		if err != nil || len(revisions) != 2 || revisions[0].Body != second.Revision.Body || revisions[1].Body != first.Revision.Body {
			t.Fatalf("history changed: %v", err)
		}
	}
	check(s, settings.ActiveCollectionID)
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	check(s, settings.ActiveCollectionID)
	snapshot := filepath.Join(t.TempDir(), "snapshot.sqlite")
	if err = s.Export(ctx, settings.ActiveCollectionID, snapshot); err != nil {
		t.Fatal(err)
	}
	receiver := openTest(t)
	collections, err := receiver.Import(ctx, snapshot)
	if err != nil {
		t.Fatal(err)
	}
	check(receiver, collections[0].ID)
}

func TestRevisionSummariesDoNotReadPayloads(t *testing.T) {
	s := openTest(t)
	settings, _ := s.Settings(context.Background())
	e := publishOne(t, s, testRecording(settings.ActiveCollectionID, "summary"), testRevision("response", "recorded"))
	if _, err := s.db.Exec("UPDATE revisions SET body=X'ff',events=X'ff' WHERE id=?", e.Revision.ID); err != nil {
		t.Fatal(err)
	}
	rows, err := s.RevisionSummaries(context.Background(), e.Recording.ID)
	if err != nil || len(rows) != 1 || rows[0].ID != e.Revision.ID {
		t.Fatalf("summary touched corrupt payload: %v", err)
	}
	if _, err = s.Revision(context.Background(), e.Recording.ID, e.Revision.ID); !errors.Is(err, errCorruptResponse) {
		t.Fatalf("detail corruption ignored: %v", err)
	}
}

func FuzzEventsExactRoundTrip(f *testing.F) {
	f.Add([]byte("\xef\xbb\xbf: keepalive\r\n\r\n"), uint64(17), uint64(0))
	f.Add([]byte{0xff, 0, '\n', '\n'}, uint64(0), uint64(99))
	f.Fuzz(func(t *testing.T, data []byte, first, delta uint64) {
		if len(data) > 1<<20 {
			t.Skip()
		}
		first %= uint64(maxEventOffset) + 1
		delta %= uint64(maxEventOffset) - first + 1
		frame := string(data) + "\n\n"
		events := []model.Event{{Data: frame, OffsetMS: int64(first)}, {Data: frame, OffsetMS: int64(first + delta)}}
		packed, err := encodeEvents(events)
		if err != nil {
			t.Fatal(err)
		}
		got, err := decodeEvents(packed)
		if err != nil || !reflect.DeepEqual(got, events) {
			t.Fatalf("round trip: %v", err)
		}
	})
}

func BenchmarkEventStorageDecode(b *testing.B) {
	events := make([]model.Event, 10000)
	for i := range events {
		events[i] = model.Event{Data: fmt.Sprintf("data: {\"id\":\"example\",\"choices\":[{\"delta\":{\"content\":\" word%d\"}}]}\n\n", i), OffsetMS: int64(i * 20)}
	}
	binaryPacked, _ := encodeEvents(events)
	j, _ := json.Marshal(events)
	jsonPacked := responseEncoder.EncodeAll(j, nil)
	b.Run("binary", func(b *testing.B) {
		b.ReportAllocs()
		b.ReportMetric(float64(len(binaryPacked)), "stored-bytes")
		for range b.N {
			if _, err := decodeEvents(binaryPacked); err != nil {
				b.Fatal(err)
			}
		}
	})
	b.Run("previous-json", func(b *testing.B) {
		decoder := mustDecoder(maxEventsBytes)
		defer decoder.Close()
		b.ReportAllocs()
		b.ReportMetric(float64(len(jsonPacked)), "stored-bytes")
		for range b.N {
			raw, err := decoder.DecodeAll(jsonPacked, nil)
			if err != nil {
				b.Fatal(err)
			}
			var out []model.Event
			if err = json.Unmarshal(raw, &out); err != nil {
				b.Fatal(err)
			}
		}
	})
}
