package archive_test

import (
	"bytes"
	"errors"
	"testing"

	"github.com/ttab/ecollab"
	"github.com/ttab/ecollab/archive"
	"github.com/ttab/ecollab/lib0"
)

func chunk() []archive.Record {
	return []archive.Record{
		{
			RedisID:      "1700000000000-0",
			TSMillis:     1700000000000,
			Subscription: "sub-a",
			Encoding:     ecollab.EncodingV1,
			Data:         []byte{0x01, 0x02, 0x03},
		},
		{
			RedisID:      "1700000000001-0",
			TSMillis:     1700000000001,
			Subscription: "sub-b",
			Encoding:     ecollab.EncodingAwareness,
			Data:         []byte{},
		},
		{
			// A server-issued marker: no originating subscription.
			RedisID:      "1700000000002-3",
			TSMillis:     1700000000002,
			Subscription: "",
			Encoding:     ecollab.EncodingStateless,
			Data:         []byte(`{"event":"publish_in_progress","data":{}}`),
		},
	}
}

func requireRecord(t *testing.T, want, got archive.Record) {
	t.Helper()

	if got.RedisID != want.RedisID {
		t.Errorf("redis_id: got %q, want %q", got.RedisID, want.RedisID)
	}

	if got.TSMillis != want.TSMillis {
		t.Errorf("%s: ts: got %d, want %d",
			want.RedisID, got.TSMillis, want.TSMillis)
	}

	if got.Subscription != want.Subscription {
		t.Errorf("%s: subscription: got %q, want %q",
			want.RedisID, got.Subscription, want.Subscription)
	}

	if got.Encoding != want.Encoding {
		t.Errorf("%s: encoding: got %q, want %q",
			want.RedisID, got.Encoding, want.Encoding)
	}

	if !bytes.Equal(got.Data, want.Data) {
		t.Errorf("%s: data: got %x, want %x",
			want.RedisID, got.Data, want.Data)
	}
}

func TestChunkRoundTrip(t *testing.T) {
	want := chunk()

	got, err := archive.DecodeRecords(archive.EncodeRecords(want))
	if err != nil {
		t.Fatalf("decode chunk: %v", err)
	}

	if len(got) != len(want) {
		t.Fatalf("got %d records, want %d", len(got), len(want))
	}

	for i := range want {
		requireRecord(t, want[i], got[i])
	}
}

// A chunk body is the concatenation of records, so decoding the
// concatenation of two encoded chunks must yield both in order. That
// is what lets the archiver append to a chunk it already wrote.
func TestConcatenatedChunksDecode(t *testing.T) {
	records := chunk()

	body := append(
		archive.EncodeRecords(records[:1]),
		archive.EncodeRecords(records[1:])...)

	got, err := archive.DecodeRecords(body)
	if err != nil {
		t.Fatalf("decode concatenation: %v", err)
	}

	if len(got) != len(records) {
		t.Fatalf("got %d records, want %d", len(got), len(records))
	}

	for i := range records {
		requireRecord(t, records[i], got[i])
	}
}

func TestEmptyChunkDecodesToNothing(t *testing.T) {
	got, err := archive.DecodeRecords(nil)
	if err != nil {
		t.Fatalf("decode empty: %v", err)
	}

	if len(got) != 0 {
		t.Fatalf("got %d records, want none", len(got))
	}
}

// A truncated chunk — an object cut short in transit, or a drain
// that died mid-write — keeps the records that did decode and says
// what went wrong, rather than costing a reader the whole chunk.
func TestTruncatedChunkKeepsWhatDecoded(t *testing.T) {
	records := chunk()
	body := archive.EncodeRecords(records)

	for _, cut := range []int{len(body) - 1, len(body) / 2} {
		got, err := archive.DecodeRecords(body[:cut])
		if !errors.Is(err, lib0.ErrTruncated) {
			t.Fatalf("cut at %d: got error %v, want ErrTruncated",
				cut, err)
		}

		if len(got) >= len(records) {
			t.Fatalf("cut at %d: got %d records, want fewer than %d",
				cut, len(got), len(records))
		}

		for i := range got {
			requireRecord(t, records[i], got[i])
		}
	}
}

// DecodeRecord copies the payload out of the buffer, so a caller can
// hold a record after the decoder has moved on — or after the body
// it came from is reused.
func TestDecodedDataIsCopied(t *testing.T) {
	want := []byte{0xde, 0xad, 0xbe, 0xef}

	body := archive.EncodeRecords([]archive.Record{{
		RedisID:  "1-0",
		Encoding: ecollab.EncodingV2,
		Data:     want,
	}})

	got, err := archive.DecodeRecords(body)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}

	for i := range body {
		body[i] = 0
	}

	if !bytes.Equal(got[0].Data, want) {
		t.Fatalf("data after clobbering the body: got %x, want %x",
			got[0].Data, want)
	}
}

// EncodeRecord is the single-record half of the API, for a writer
// streaming into an encoder it owns. Its output must be exactly what
// EncodeRecords produces for the same record.
func TestEncodeRecordMatchesEncodeRecords(t *testing.T) {
	r := chunk()[0]

	var enc lib0.Encoder

	archive.EncodeRecord(&enc, r)

	if !bytes.Equal(enc.Bytes(), archive.EncodeRecords([]archive.Record{r})) {
		t.Fatal("EncodeRecord and EncodeRecords disagree")
	}
}
