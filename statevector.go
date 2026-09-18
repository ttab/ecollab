package ecollab

import (
	"errors"
	"fmt"

	"github.com/ttab/ecollab/lib0"
)

// StateVector is a Yjs state vector: the highest clock each client
// in a document has contributed, keyed by client id. It is what a
// participant sends to say "this is everything I have seen", and
// what the service answers a freshness refusal with in the
// server_state_vector metadata, so a client that wanted to publish
// what it sees can tell how far behind it is.
type StateVector map[uint64]uint64

// MaxStateVectorEntries caps the size of an incoming state vector
// to bound the per-RPC allocation. Real Yjs state vectors carry one
// entry per editing client and stay in the hundreds; 10k is
// generous. Without the cap, a caller-controlled varuint count in
// `DecodeStateVector` becomes a `make(map, N)` allocation hint that
// Go honours aggressively, turning a small payload into hundreds of
// MB of resident memory.
const MaxStateVectorEntries = 10_000

// ErrStateVectorTooLarge surfaces an oversized incoming state
// vector. Callers translate it into an invalid_argument error with
// `error_code=state_vector_too_large`.
var ErrStateVectorTooLarge = errors.New("state vector: too many entries")

// DecodeStateVector parses the lib0 wire format goyjs produces
// from Doc.StateVectorV1 and the service echoes back on a refusal.
//
// Format:
//
//	varuint count
//	[ varuint clientID, varuint clock ] * count
func DecodeStateVector(b []byte) (StateVector, error) {
	d := lib0.NewDecoder(b)

	n, err := d.ReadVarUint()
	if err != nil {
		return nil, fmt.Errorf("state vector: count: %w", err)
	}

	if n > MaxStateVectorEntries {
		return nil, fmt.Errorf("%w: %d > %d",
			ErrStateVectorTooLarge, n, MaxStateVectorEntries)
	}

	sv := make(StateVector, n)
	for i := uint64(0); i < n; i++ {
		cid, err := d.ReadVarUint()
		if err != nil {
			return nil, fmt.Errorf("state vector: clientID at %d: %w", i, err)
		}

		clock, err := d.ReadVarUint()
		if err != nil {
			return nil, fmt.Errorf("state vector: clock for %d: %w", cid, err)
		}

		sv[cid] = clock
	}

	if !d.EOF() {
		return nil, errors.New("state vector: trailing bytes")
	}

	return sv, nil
}

// Dominates reports whether a >= b in the per-client sense the
// freshness check requires: for every client mentioned in b, a's
// clock is at least b's clock. Equivalent to "a includes every
// update b had seen."
//
// An empty b is dominated by every a.
func (a StateVector) Dominates(b StateVector) bool {
	for cid, clock := range b {
		if a[cid] < clock {
			return false
		}
	}

	return true
}

// FirstShortfall returns the first (clientID, expected, actual)
// triple where a falls short of b. Returned for diagnostic
// purposes; callers normally just check Dominates.
func (a StateVector) FirstShortfall(b StateVector) (clientID, expected, actual uint64, ok bool) {
	for cid, clock := range b {
		if a[cid] < clock {
			return cid, clock, a[cid], true
		}
	}

	return 0, 0, 0, false
}
