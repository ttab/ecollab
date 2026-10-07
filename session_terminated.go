package ecollab

import (
	"encoding/json"
	"errors"
	"fmt"
)

// TerminateReason is why a session ended, as a session_terminated
// close reports it to the subscribers it was closed under.
//
// The set may grow: a client must treat a value it does not know as
// a session that ended for a reason it cannot act on, and subscribe
// again as for any other.
type TerminateReason string

const (
	// TerminateReasonFrozen: the document was frozen, which is what
	// a publish does. Version is the frozen version and StateVector,
	// when present, its state: what the publish took.
	TerminateReasonFrozen TerminateReason = "frozen"

	// TerminateReasonEvicted: the session was evicted, the ordinary
	// end of a session nobody is editing any more. Its final version
	// and resume state have been written; the eviction is still
	// finishing, so a subscribe right away may be refused with
	// session_ending. Subscribing again within the resume window
	// continues the same lineage. StateVector, when present, is the
	// state the final version was written from.
	TerminateReasonEvicted TerminateReason = "evicted"

	// TerminateReasonSketchPromoted: the sketch was promoted to a
	// repository document, whose sessions begin a lineage of their
	// own. StateVector, when present, is the promoted sketch's.
	TerminateReasonSketchPromoted TerminateReason = "sketch_promoted"
)

// SessionTerminated is the message of a CloseReasonSessionTerminated
// close, encoded as JSON. It describes the state the session ended
// with, so a client can tell whether everything it sent made it in
// without reasoning about which of its updates the server saw.
type SessionTerminated struct {
	// Reason is why the session ended.
	Reason TerminateReason `json:"reason"`

	// Version is the repository version a freeze published. Zero
	// for an eviction or a sketch promotion, and for a freeze the
	// server has no version for.
	Version int64 `json:"version,omitempty"`

	// SessionID is the session that ended. Empty when the server did
	// not name it.
	SessionID string `json:"session_id,omitempty"`

	// StateVector is the document's Yjs lib0 state vector as the
	// session ended with it: the frozen version's on a freeze, the
	// promoted sketch's on a promotion. Absent when the server did
	// not materialise the state, which a client reads as "unknown".
	// Compare the local document's vector against it. The structs in
	// Y.encodeStateAsUpdate(doc, StateVector) are exactly the inserts
	// the ended state lacks; its delete set is the local document's
	// whole delete set, so the update is never empty, and
	// Y.decodeUpdate(update).structs.length === 0 is the test for
	// "nothing missing". The comparison covers inserts, not
	// deletions; DeleteSet covers those. On the wire it is base64,
	// as encoding/json writes a []byte.
	StateVector []byte `json:"state_vector,omitempty"`

	// DeleteSet is a Yjs v1 update with no structs carrying the
	// document's whole delete set as the session ended with it, so
	// a client can tell a deletion the ended state lacks, which no
	// state vector shows. A client that deleted text the ended state
	// still holds finds a range of its own delete set outside this
	// one; LacksDeletions is the comparison. Absent whenever
	// StateVector is. Base64 on the wire, like StateVector.
	DeleteSet []byte `json:"delete_set,omitempty"`
}

// ErrNoDeleteSet is LacksDeletions' answer for a message that carries
// no delete set: the server did not materialise the state, and
// whether a deletion made it in is unknown.
var ErrNoDeleteSet = errors.New("the session terminated message carries no delete set")

// LacksDeletions reports whether local deletes anything m's delete
// set does not: a deletion the client made that the ended state —
// the frozen version, on a freeze — does not hold. local is any Yjs
// v1 update carrying the client's whole delete set; the cheapest is
// one with no structs, doc.EncodeDiffV1(doc.StateVectorV1()) in
// goyjs, Y.encodeStateAsUpdate(doc, Y.encodeStateVector(doc)) in
// yjs. A message without a delete set answers ErrNoDeleteSet.
func (m SessionTerminated) LacksDeletions(local []byte) (bool, error) {
	if len(m.DeleteSet) == 0 {
		return false, ErrNoDeleteSet
	}

	_, ended, err := updateV1DeleteSet(m.DeleteSet)
	if err != nil {
		return false, fmt.Errorf("read the message's delete set: %w", err)
	}

	_, own, err := updateV1DeleteSet(local)
	if err != nil {
		return false, fmt.Errorf("read the local delete set: %w", err)
	}

	return !own.subtract(ended).empty(), nil
}

// EncodeSessionTerminated encodes the message of a
// session_terminated close.
func EncodeSessionTerminated(m SessionTerminated) string {
	data, err := json.Marshal(m)
	if err != nil {
		// Strings, a number and a byte slice cannot fail to marshal.
		panic(fmt.Sprintf("encode session terminated: %v", err))
	}

	return string(data)
}

// DecodeSessionTerminated decodes the message of a session_terminated
// close. A message that is not JSON is an error — the close of a
// subscription the server reaped carries prose — and so is one
// without a reason.
func DecodeSessionTerminated(message string) (SessionTerminated, error) {
	var m SessionTerminated

	if err := json.Unmarshal([]byte(message), &m); err != nil {
		return SessionTerminated{}, fmt.Errorf("decode session terminated: %w", err)
	}

	if m.Reason == "" {
		return SessionTerminated{}, errors.New(
			"decode session terminated: no reason")
	}

	return m, nil
}

// Terminated reports the state a session ended with when err is, or
// wraps, a session_terminated close whose message decodes, or a
// subscribe_failed close whose message decodes: a subscribe refused
// because the document is frozen carries the freeze's
// SessionTerminated as its message. It is false for any other error,
// and for a close whose message is not a SessionTerminated, as the
// close of a reaped subscription and an ordinary subscribe failure
// are not:
//
//	if ended, ok := ecollab.Terminated(sub.Err()); ok {
//	    // Compare the local state vector against ended.StateVector,
//	    // and the local delete set with ended.LacksDeletions.
//	}
func Terminated(err error) (SessionTerminated, bool) {
	var closed *CloseError
	if !errors.As(err, &closed) {
		return SessionTerminated{}, false
	}

	if closed.Reason != CloseReasonSessionTerminated &&
		closed.Reason != CloseReasonSubscribeFailed {
		return SessionTerminated{}, false
	}

	m, decodeErr := DecodeSessionTerminated(closed.Message)
	if decodeErr != nil {
		return SessionTerminated{}, false
	}

	return m, true
}
