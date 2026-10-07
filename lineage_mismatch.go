package ecollab

import (
	"encoding/json"
	"fmt"
)

// LineageEndReason is why a lineage stopped being resumable, as a
// lineage_mismatch refusal reports it for the lineage the client's
// copy belongs to. It is what lets a client tell the person more
// than "your copy is out of date", and decide whether the work can
// go back into the document: a copy orphaned by a publish calls for
// a different message than one orphaned by someone resetting the
// document.
//
// The set may grow: a client must treat a value it does not know as
// LineageEndReasonUnknown.
type LineageEndReason string

const (
	// LineageEndReasonFrozen: the document was frozen, which is
	// what a publish does. The common case for a client that was
	// offline while someone else published. Version is the frozen
	// version; the document can be unfrozen and edited again, so the
	// work can go back in once it has been compared against it.
	LineageEndReasonFrozen LineageEndReason = "frozen"

	// LineageEndReasonReset: someone reset the document's
	// collaborative state on purpose, discarding what the session
	// held, and the repository version is what collaboration starts
	// from again. The work was deliberately set aside; offer it,
	// don't put it back on the person's behalf.
	LineageEndReasonReset LineageEndReason = "reset"

	// LineageEndReasonPurged: a session of the lineage was purged —
	// an audit-trail erasure, normally on a legal request. Keep the
	// copy apart; it holds content that was meant to go.
	LineageEndReasonPurged LineageEndReason = "purged"

	// LineageEndReasonDiscarded: the sketch was deleted with
	// DiscardSketch. There is no document left to put the work back
	// into; a new sketch is the only home for it.
	LineageEndReasonDiscarded LineageEndReason = "discarded"

	// LineageEndReasonPromoted: the sketch was promoted to a
	// repository document, whose sessions begin a lineage of their
	// own. Version is the repository version the promotion created,
	// which holds the sketch as it was then.
	LineageEndReasonPromoted LineageEndReason = "promoted"

	// LineageEndReasonExpired: the lineage's state was kept for its
	// 24 hour resume window after the session was evicted, and
	// nobody came back within it. Nothing happened to the document;
	// Version is the one the copy was last in step with.
	LineageEndReasonExpired LineageEndReason = "expired"

	// LineageEndReasonAnchorMoved: the document changed outside
	// collaboration after the session was evicted — a version
	// written by another client of the repository, or the document
	// deleted and recreated — so the stored state no longer
	// described it. Version is the one the copy was last in step
	// with; the repository has moved on from it.
	LineageEndReasonAnchorMoved LineageEndReason = "anchor_moved"

	// LineageEndReasonUnknown: the server has no record of how the
	// lineage ended, or of the lineage at all.
	LineageEndReasonUnknown LineageEndReason = "unknown"
)

// LineageMismatch is the message of a CloseReasonLineageMismatch
// close, encoded as JSON. Its reason and version follow the
// session_terminated message's, SessionTerminated.
type LineageMismatch struct {
	// Lineage is the session's current lineage, or the lineage the
	// next subscribe would resume. Empty when there was no session
	// and nothing to resume: the subscribe would have seeded a new
	// lineage, which the next subscribe from an empty document is
	// told in its Synced.
	Lineage string `json:"lineage"`

	// Reason is why the lineage the client's copy belongs to ended.
	Reason LineageEndReason `json:"reason"`

	// Version is the repository version the client's lineage ended
	// at: the newest version a session of the lineage wrote or
	// started from. It is the baseline to compare the copy's content
	// against, and the version the copy's unsaved work postdates.
	// Zero when the lineage knew no repository version, as a sketch's
	// does not, or when the server has no record of it.
	Version int64 `json:"version"`
}

// EncodeLineageMismatch encodes the message of a lineage_mismatch
// close. An empty reason is sent as LineageEndReasonUnknown.
func EncodeLineageMismatch(m LineageMismatch) string {
	if m.Reason == "" {
		m.Reason = LineageEndReasonUnknown
	}

	data, err := json.Marshal(m)
	if err != nil {
		// Two strings and a number cannot fail to marshal.
		panic(fmt.Sprintf("encode lineage mismatch: %v", err))
	}

	return string(data)
}

// DecodeLineageMismatch decodes the message of a lineage_mismatch
// close. A missing or empty reason decodes as LineageEndReasonUnknown.
func DecodeLineageMismatch(message string) (LineageMismatch, error) {
	var m LineageMismatch

	if err := json.Unmarshal([]byte(message), &m); err != nil {
		return LineageMismatch{}, fmt.Errorf("decode lineage mismatch: %w", err)
	}

	if m.Reason == "" {
		m.Reason = LineageEndReasonUnknown
	}

	return m, nil
}
