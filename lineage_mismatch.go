package ecollab

import (
	"encoding/json"
	"fmt"
)

// LineageEndCause is why a lineage stopped being resumable, as a
// lineage_mismatch refusal reports it for the lineage the client's
// copy belongs to. It is what lets a client tell the person more
// than "your copy is out of date": a copy orphaned by a publish
// calls for a different message than one orphaned by someone
// resetting the document.
//
// The set is open-ended in one direction: a client must treat a
// value it does not know as LineageEndCauseUnknown.
type LineageEndCause string

const (
	// LineageEndCauseFrozen: the document was frozen, which is what
	// a publish does. The common case for a client that was offline
	// while someone else published.
	LineageEndCauseFrozen LineageEndCause = "frozen"

	// LineageEndCauseReset: someone reset the document's
	// collaborative state, discarding what the session held.
	LineageEndCauseReset LineageEndCause = "reset"

	// LineageEndCausePurged: a session of the lineage was purged,
	// and its content with it.
	LineageEndCausePurged LineageEndCause = "purged"

	// LineageEndCauseDiscarded: the sketch was discarded.
	LineageEndCauseDiscarded LineageEndCause = "discarded"

	// LineageEndCausePromoted: the sketch was promoted to a
	// repository document, whose sessions begin a lineage of their
	// own.
	LineageEndCausePromoted LineageEndCause = "promoted"

	// LineageEndCauseExpired: the lineage's state was kept for its
	// resume window after the session was evicted, and the window
	// passed before anyone came back to it.
	LineageEndCauseExpired LineageEndCause = "expired"

	// LineageEndCauseAnchorMoved: the document changed outside the
	// session after it was evicted — a version written by something
	// other than the collaboration service, or the document deleted
	// and recreated — so the stored state no longer described it.
	LineageEndCauseAnchorMoved LineageEndCause = "anchor_moved"

	// LineageEndCauseUnknown: the server has no record of how the
	// lineage ended, or of the lineage at all.
	LineageEndCauseUnknown LineageEndCause = "unknown"
)

// LineageMismatch is the message of a CloseReasonLineageMismatch
// close, encoded as JSON.
type LineageMismatch struct {
	// Lineage is the session's current lineage, or the lineage the
	// next subscribe would resume. Empty when there was no session
	// and nothing to resume: the subscribe would have seeded a new
	// lineage, which the next subscribe from an empty document is
	// told in its Synced.
	Lineage string `json:"lineage"`

	// Cause is why the lineage the client's copy belongs to ended.
	Cause LineageEndCause `json:"cause"`
}

// EncodeLineageMismatch encodes the message of a lineage_mismatch
// close.
func EncodeLineageMismatch(m LineageMismatch) string {
	if m.Cause == "" {
		m.Cause = LineageEndCauseUnknown
	}

	data, err := json.Marshal(m)
	if err != nil {
		// Two strings cannot fail to marshal.
		panic(fmt.Sprintf("encode lineage mismatch: %v", err))
	}

	return string(data)
}

// DecodeLineageMismatch decodes the message of a lineage_mismatch
// close. A missing or empty cause decodes as LineageEndCauseUnknown.
func DecodeLineageMismatch(message string) (LineageMismatch, error) {
	var m LineageMismatch

	if err := json.Unmarshal([]byte(message), &m); err != nil {
		return LineageMismatch{}, fmt.Errorf("decode lineage mismatch: %w", err)
	}

	if m.Cause == "" {
		m.Cause = LineageEndCauseUnknown
	}

	return m, nil
}
