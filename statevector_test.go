package ecollab_test

import (
	"errors"
	"testing"

	"github.com/ttab/ecollab"
	"github.com/ttab/ecollab/lib0"
	"github.com/ttab/goyjs"
)

// encodeStateVector writes the lib0 form DecodeStateVector reads,
// so the table cases below say what they mean rather than carrying
// hand-counted bytes.
func encodeStateVector(sv map[uint64]uint64, order []uint64) []byte {
	var e lib0.Encoder

	e.WriteVarUint(uint64(len(order)))

	for _, cid := range order {
		e.WriteVarUint(cid)
		e.WriteVarUint(sv[cid])
	}

	return e.Bytes()
}

// TestDecodeStateVectorFromGoyjs is the conformance anchor: the
// bytes come from a real Y.Doc, not from this package's own
// encoder, so a change to the lib0 primitives that broke the format
// would show up here rather than cancelling itself out.
func TestDecodeStateVectorFromGoyjs(t *testing.T) {
	a := goyjs.NewWithClientID(7)
	defer a.Close()

	if err := a.MapSetString("document", "title", "one"); err != nil {
		t.Fatalf("set title: %v", err)
	}

	if err := a.MapSetString("document", "uri", "two"); err != nil {
		t.Fatalf("set uri: %v", err)
	}

	sv, err := ecollab.DecodeStateVector(a.StateVectorV1())
	if err != nil {
		t.Fatalf("decode state vector: %v", err)
	}

	clock, ok := sv[7]
	if !ok {
		t.Fatalf("state vector %v has no entry for client 7", sv)
	}

	if clock == 0 {
		t.Error("clock for an edited client is 0")
	}

	// A second document that has seen everything the first produced
	// must dominate it, and the first must not dominate the second
	// once it has moved on.
	b := goyjs.NewWithClientID(9)
	defer b.Close()

	if err := b.ApplyUpdateV1(a.EncodeStateV1()); err != nil {
		t.Fatalf("apply state: %v", err)
	}

	bsv, err := ecollab.DecodeStateVector(b.StateVectorV1())
	if err != nil {
		t.Fatalf("decode peer state vector: %v", err)
	}

	if !bsv.Dominates(sv) {
		t.Errorf("peer %v does not dominate %v after applying its state", bsv, sv)
	}

	if err := a.MapSetString("document", "title", "three"); err != nil {
		t.Fatalf("set title again: %v", err)
	}

	moved, err := ecollab.DecodeStateVector(a.StateVectorV1())
	if err != nil {
		t.Fatalf("decode moved state vector: %v", err)
	}

	if bsv.Dominates(moved) {
		t.Errorf("peer %v still dominates %v after a further edit", bsv, moved)
	}
}

// TestDecodeStateVectorRoundTrip covers the shapes the wire produces
// and the three ways a payload can be rejected.
func TestDecodeStateVectorRoundTrip(t *testing.T) {
	cases := []struct {
		name string
		in   []byte
		want ecollab.StateVector
	}{
		{
			name: "empty",
			in:   encodeStateVector(nil, nil),
			want: ecollab.StateVector{},
		},
		{
			name: "single client",
			in:   encodeStateVector(map[uint64]uint64{42: 17}, []uint64{42}),
			want: ecollab.StateVector{42: 17},
		},
		{
			name: "multi-byte varuints",
			in: encodeStateVector(map[uint64]uint64{
				3_456_789_012: 128,
				1:             0,
			}, []uint64{3_456_789_012, 1}),
			want: ecollab.StateVector{3_456_789_012: 128, 1: 0},
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := ecollab.DecodeStateVector(c.in)
			if err != nil {
				t.Fatalf("decode: %v", err)
			}

			if len(got) != len(c.want) {
				t.Fatalf("decoded %v, want %v", got, c.want)
			}

			for cid, clock := range c.want {
				if got[cid] != clock {
					t.Errorf("client %d clock = %d, want %d", cid, got[cid], clock)
				}
			}
		})
	}
}

// TestDecodeStateVectorTooLarge: the count is caller-controlled and
// feeds a make hint, so the cap has to bite before the allocation.
func TestDecodeStateVectorTooLarge(t *testing.T) {
	var e lib0.Encoder

	e.WriteVarUint(ecollab.MaxStateVectorEntries + 1)

	_, err := ecollab.DecodeStateVector(e.Bytes())
	if !errors.Is(err, ecollab.ErrStateVectorTooLarge) {
		t.Fatalf("decode error = %v, want ErrStateVectorTooLarge", err)
	}
}

// TestDecodeStateVectorMalformed: a truncated or over-long payload
// is refused rather than decoded to a plausible-looking prefix.
func TestDecodeStateVectorMalformed(t *testing.T) {
	cases := []struct {
		name string
		in   []byte
	}{
		{name: "no count", in: nil},
		{name: "count without entries", in: []byte{0x02}},
		{name: "client without clock", in: []byte{0x01, 0x05}},
		{
			name: "trailing bytes",
			in: append(
				encodeStateVector(map[uint64]uint64{1: 1}, []uint64{1}),
				0x00),
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if _, err := ecollab.DecodeStateVector(c.in); err == nil {
				t.Fatal("decoded a malformed state vector without error")
			}
		})
	}
}

// TestDominates covers the asymmetry the freshness check rests on:
// a client is fresh when it has seen everything the server has, and
// an unmentioned client counts as clock 0.
func TestDominates(t *testing.T) {
	cases := []struct {
		name string
		a, b ecollab.StateVector
		want bool
	}{
		{
			name: "empty is dominated by everything",
			a:    ecollab.StateVector{1: 5},
			b:    ecollab.StateVector{},
			want: true,
		},
		{
			name: "equal",
			a:    ecollab.StateVector{1: 5, 2: 3},
			b:    ecollab.StateVector{1: 5, 2: 3},
			want: true,
		},
		{
			name: "ahead on one client",
			a:    ecollab.StateVector{1: 6, 2: 3},
			b:    ecollab.StateVector{1: 5, 2: 3},
			want: true,
		},
		{
			name: "behind on one client",
			a:    ecollab.StateVector{1: 4, 2: 3},
			b:    ecollab.StateVector{1: 5, 2: 3},
			want: false,
		},
		{
			name: "missing a client the other has",
			a:    ecollab.StateVector{1: 5},
			b:    ecollab.StateVector{1: 5, 2: 1},
			want: false,
		},
		{
			name: "extra client is not a shortfall",
			a:    ecollab.StateVector{1: 5, 9: 9},
			b:    ecollab.StateVector{1: 5},
			want: true,
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := c.a.Dominates(c.b); got != c.want {
				t.Errorf("%v.Dominates(%v) = %v, want %v", c.a, c.b, got, c.want)
			}
		})
	}
}

// TestFirstShortfall: the diagnostic names a client the caller is
// actually behind on, and reports nothing when it is not behind.
func TestFirstShortfall(t *testing.T) {
	a := ecollab.StateVector{1: 5}
	b := ecollab.StateVector{1: 5, 2: 4}

	cid, expected, actual, ok := a.FirstShortfall(b)
	if !ok {
		t.Fatal("no shortfall reported against a state vector that dominates")
	}

	if cid != 2 || expected != 4 || actual != 0 {
		t.Errorf("shortfall = (%d, %d, %d), want (2, 4, 0)", cid, expected, actual)
	}

	if _, _, _, ok := b.FirstShortfall(a); ok {
		t.Error("shortfall reported for a dominating state vector")
	}
}
