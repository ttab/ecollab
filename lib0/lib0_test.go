package lib0_test

import (
	"bytes"
	"errors"
	"testing"

	"github.com/ttab/ecollab/lib0"
)

// TestVarUintVectors checks canonical encoding boundaries that match
// the lib0 reference test vectors.
func TestVarUintVectors(t *testing.T) {
	cases := []struct {
		v   uint64
		enc []byte
	}{
		{0, []byte{0x00}},
		{1, []byte{0x01}},
		{127, []byte{0x7F}},
		{128, []byte{0x80, 0x01}},
		{129, []byte{0x81, 0x01}},
		{255, []byte{0xFF, 0x01}},
		{256, []byte{0x80, 0x02}},
		{16383, []byte{0xFF, 0x7F}},
		{16384, []byte{0x80, 0x80, 0x01}},
		{1<<32 - 1, []byte{0xFF, 0xFF, 0xFF, 0xFF, 0x0F}},
	}
	for _, c := range cases {
		var e lib0.Encoder
		e.WriteVarUint(c.v)

		if !bytes.Equal(e.Bytes(), c.enc) {
			t.Errorf("encode %d: got % x, want % x", c.v, e.Bytes(), c.enc)
		}

		d := lib0.NewDecoder(e.Bytes())

		got, err := d.ReadVarUint()
		if err != nil {
			t.Errorf("decode %d: %v", c.v, err)

			continue
		}

		if got != c.v {
			t.Errorf("decode %d: got %d", c.v, got)
		}

		if !d.EOF() {
			t.Errorf("decode %d: %d bytes remaining", c.v, len(d.Remaining()))
		}
	}
}

func TestVarStringRoundTrip(t *testing.T) {
	cases := []string{
		"",
		"hello",
		"hello world",
		"å räksmörgås",
		"🚀 emoji 🎉",
		string(make([]byte, 200)),
	}
	for _, s := range cases {
		var e lib0.Encoder
		e.WriteVarString(s)
		d := lib0.NewDecoder(e.Bytes())

		got, err := d.ReadVarString()
		if err != nil {
			t.Errorf("decode %q: %v", s, err)

			continue
		}

		if got != s {
			t.Errorf("round-trip %q: got %q", s, got)
		}
	}
}

func TestDecodeTruncated(t *testing.T) {
	d := lib0.NewDecoder([]byte{0x80})
	if _, err := d.ReadVarUint(); !errors.Is(err, lib0.ErrTruncated) {
		t.Errorf("readVarUint on truncated: %v, want lib0.ErrTruncated", err)
	}

	var e lib0.Encoder
	e.WriteVarUint(10)
	e.WriteBytes([]byte("abcd"))

	d2 := lib0.NewDecoder(e.Bytes())
	if _, err := d2.ReadVarString(); !errors.Is(err, lib0.ErrTruncated) {
		t.Errorf("readVarString on truncated: %v, want lib0.ErrTruncated", err)
	}
}

func TestDecodeOverflow(t *testing.T) {
	// 11 bytes of 0xFF: shift reaches 70 before we'd terminate.
	d := lib0.NewDecoder(bytes.Repeat([]byte{0xFF}, 11))
	if _, err := d.ReadVarUint(); !errors.Is(err, lib0.ErrOverflow) {
		t.Errorf("readVarUint on overflow: %v, want lib0.ErrOverflow", err)
	}
}

func TestReadAll(t *testing.T) {
	var e lib0.Encoder
	e.WriteVarUint(7)
	e.WriteBytes([]byte("payload"))
	d := lib0.NewDecoder(e.Bytes())

	n, err := d.ReadVarUint()
	if err != nil {
		t.Fatal(err)
	}

	if n != 7 {
		t.Fatalf("varuint = %d, want 7", n)
	}

	got := d.ReadAll()
	if string(got) != "payload" {
		t.Fatalf("ReadAll = %q, want payload", got)
	}

	if !d.EOF() {
		t.Errorf("not EOF after ReadAll")
	}
}
