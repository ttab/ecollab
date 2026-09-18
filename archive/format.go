// Package archive is the record codec for the elephant collab
// service's S3 audit archive, for a program reading an archived
// session straight out of object storage rather than through the
// service's RPCs.
//
// A session's updates are archived as numbered chunk objects under
// the session's prefix, `{prefix}/updates/NNNNNNNN.bin`. A chunk
// body is the concatenation of records, each one archived Redis
// stream entry encoded with lib0 varuint/varstring primitives:
//
//	varstring  redis_id     // the Redis stream entry ID
//	varuint    ts_millis    // server-side timestamp at receive
//	varstring  subscription // originator's subscription_id
//	varstring  encoding     // the entry's ecollab.Encoding tag
//	varuint    data_len
//	bytes      data         // the Yjs / awareness / stateless payload
//
// Readers walk a chunk by decoding records until the input is
// consumed, which DecodeRecords does. The chunk number in the S3 key
// increases monotonically per session, so chronological order across
// a session falls out of the numbering plus the redis_id ordering
// within a chunk.
//
// The format is stable: a chunk written years ago must still decode,
// so a field can be appended behind a new chunk-format version but
// never repurposed. What is not here is the service's side of the
// archive — the prefix layout, the manifest.json and closed.json
// terminal markers, the purge tombstone and the drain policy that
// produces chunks. Those are the service's, and only a record is a
// contract with a reader.
package archive

import (
	"fmt"

	"github.com/ttab/ecollab"
	"github.com/ttab/ecollab/lib0"
)

// Record is one decoded entry from an archive chunk.
type Record struct {
	RedisID      string
	TSMillis     uint64
	Subscription string
	Encoding     ecollab.Encoding
	Data         []byte
}

// EncodeRecord appends a single record to enc.
func EncodeRecord(enc *lib0.Encoder, r Record) {
	enc.WriteVarString(r.RedisID)
	enc.WriteVarUint(r.TSMillis)
	enc.WriteVarString(r.Subscription)
	enc.WriteVarString(string(r.Encoding))
	enc.WriteVarUint(uint64(len(r.Data)))
	enc.WriteBytes(r.Data)
}

// EncodeRecords builds a complete chunk body from a slice of
// records.
func EncodeRecords(records []Record) []byte {
	var e lib0.Encoder
	for _, r := range records {
		EncodeRecord(&e, r)
	}

	return e.Bytes()
}

// DecodeRecords walks a chunk body and returns every record in
// order. Returns an error wrapping ErrTruncated if the input
// ends mid-record.
func DecodeRecords(b []byte) ([]Record, error) {
	d := lib0.NewDecoder(b)

	var out []Record

	for !d.EOF() {
		r, err := DecodeRecord(d)
		if err != nil {
			return out, err
		}

		out = append(out, r)
	}

	return out, nil
}

// DecodeRecord reads one record from d.
func DecodeRecord(d *lib0.Decoder) (Record, error) {
	var r Record

	id, err := d.ReadVarString()
	if err != nil {
		return r, fmt.Errorf("record: redis_id: %w", err)
	}

	r.RedisID = id

	ts, err := d.ReadVarUint()
	if err != nil {
		return r, fmt.Errorf("record %s: ts: %w", id, err)
	}

	r.TSMillis = ts

	sub, err := d.ReadVarString()
	if err != nil {
		return r, fmt.Errorf("record %s: subscription: %w", id, err)
	}

	r.Subscription = sub

	enc, err := d.ReadVarString()
	if err != nil {
		return r, fmt.Errorf("record %s: encoding: %w", id, err)
	}

	r.Encoding = ecollab.Encoding(enc)

	n, err := d.ReadVarUint()
	if err != nil {
		return r, fmt.Errorf("record %s: data length: %w", id, err)
	}

	//nolint:gosec // ReadN returns ErrTruncated when n exceeds the buffer.
	raw, err := d.ReadN(int(n))
	if err != nil {
		return r, fmt.Errorf("record %s: data bytes: %w", id, err)
	}
	// Copy so callers can hold the data past further decoder use.
	r.Data = make([]byte, len(raw))
	copy(r.Data, raw)

	return r, nil
}
