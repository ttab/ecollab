// Package lib0 provides the minimal lib0 encoder/decoder primitives
// the Yjs wire formats around a collaborative session are built
// from: a Yjs state vector (varuint client id, varuint clock), the
// WebSocket envelope (varstring doc name, varuint type, payload) and
// the S3 archive's chunk records.
//
// Only varuint and varstring are implemented, because that is all
// those formats need. Adding a primitive is mechanical when one
// does.
//
// The encoding is lib0's, so it interoperates with the JavaScript
// reference implementation: a varuint is little-endian 7 bits per
// byte with the high bit as the continuation flag, and a varstring
// is a varuint byte length followed by UTF-8.
package lib0

import "errors"

// ErrOverflow is returned when a varuint payload exceeds 64 bits.
var ErrOverflow = errors.New("lib0: varuint overflow")

// ErrTruncated is returned when input ends before a full encoding
// has been read.
var ErrTruncated = errors.New("lib0: input truncated")

// Encoder appends varuints and varstrings to a growing byte buffer.
// Zero value is ready to use; call Bytes to retrieve the encoded
// payload.
type Encoder struct {
	buf []byte
}

// Bytes returns the encoded payload. The slice aliases the encoder's
// internal buffer; copy if you need to retain it past further writes.
func (e *Encoder) Bytes() []byte {
	return e.buf
}

// Reset clears the buffer so the encoder can be reused.
func (e *Encoder) Reset() {
	e.buf = e.buf[:0]
}

// WriteVarUint appends a lib0 unsigned varint encoding of v.
func (e *Encoder) WriteVarUint(v uint64) {
	for v >= 0x80 {
		e.buf = append(e.buf, byte(v)|0x80)
		v >>= 7
	}

	e.buf = append(e.buf, byte(v))
}

// WriteVarString appends a varstring (varuint length + UTF-8 bytes).
func (e *Encoder) WriteVarString(s string) {
	e.WriteVarUint(uint64(len(s)))
	e.buf = append(e.buf, s...)
}

// WriteBytes appends raw bytes without a length prefix. Used after a
// final varuint where the rest of the frame is opaque payload.
func (e *Encoder) WriteBytes(b []byte) {
	e.buf = append(e.buf, b...)
}

// Decoder reads varuints and varstrings from a fixed byte slice.
type Decoder struct {
	buf []byte
	pos int
}

// NewDecoder constructs a decoder over the supplied bytes. The slice
// is not copied; callers should not mutate it during decoding.
func NewDecoder(buf []byte) *Decoder {
	return &Decoder{buf: buf}
}

// Pos returns the current read offset.
func (d *Decoder) Pos() int {
	return d.pos
}

// Remaining returns the unread bytes. The slice aliases the
// decoder's buffer.
func (d *Decoder) Remaining() []byte {
	return d.buf[d.pos:]
}

// EOF reports whether the decoder has consumed every byte.
func (d *Decoder) EOF() bool {
	return d.pos >= len(d.buf)
}

// ReadVarUint decodes a lib0 unsigned varint.
func (d *Decoder) ReadVarUint() (uint64, error) {
	var (
		v     uint64
		shift uint
	)

	for d.pos < len(d.buf) {
		b := d.buf[d.pos]
		d.pos++

		v |= uint64(b&0x7F) << shift
		if b < 0x80 {
			return v, nil
		}

		shift += 7
		if shift >= 64 {
			return 0, ErrOverflow
		}
	}

	return 0, ErrTruncated
}

// ReadVarString decodes a varstring (varuint length + UTF-8 bytes).
func (d *Decoder) ReadVarString() (string, error) {
	n, err := d.ReadVarUint()
	if err != nil {
		return "", err
	}

	//nolint:gosec // overflow protected by the explicit end>len/end<pos check below.
	end := d.pos + int(n)
	if end > len(d.buf) || end < d.pos {
		return "", ErrTruncated
	}

	s := string(d.buf[d.pos:end])
	d.pos = end

	return s, nil
}

// ReadAll returns the unread bytes and advances the cursor past them.
// Used to consume the opaque payload at the end of a frame.
func (d *Decoder) ReadAll() []byte {
	out := d.buf[d.pos:]
	d.pos = len(d.buf)

	return out
}

// ReadN returns the next n bytes and advances the cursor. Returns
// ErrTruncated if fewer than n bytes remain.
func (d *Decoder) ReadN(n int) ([]byte, error) {
	if n < 0 {
		return nil, ErrTruncated
	}

	if d.pos+n > len(d.buf) {
		return nil, ErrTruncated
	}

	out := d.buf[d.pos : d.pos+n]
	d.pos += n

	return out, nil
}
