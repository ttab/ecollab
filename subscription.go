package ecollab

// SubscriptionMode is the read/write capability the service granted
// a subscription. It is decided once, when the subscription opens,
// from the caller's permissions on the document and from whether
// they asked to observe; it does not change for the life of the
// subscription.
//
// A subscriber learns its mode from the Synced message that ends
// initial state transfer, and should believe it: sending an update
// on a read-only subscription is refused with
// CloseReasonReadOnly rather than ignored, and the refusal closes
// that subscription.
//
// The zero value is not a mode. It is what an unset wire enum
// decodes to, and a client seeing it has been answered by a server
// that did not say.
type SubscriptionMode string

const (
	// SubscriptionModeReadOnly grants reads only: state transfer,
	// live updates and awareness, with no way to contribute an
	// update. Observers always get it, and so does an editor whose
	// permission on the document falls short of write.
	SubscriptionModeReadOnly SubscriptionMode = "read_only"

	// SubscriptionModeReadWrite grants the full editing capability:
	// the subscriber may forward updates into the session and they
	// are attributed to it.
	SubscriptionModeReadWrite SubscriptionMode = "read_write"
)

// Close reasons. A subscription the server closes on its own
// carries one of these as the reason on the Close message, and a
// connection the server refuses carries one as the reason on the
// terminal error — in its "reason" metadata on the Connect stream,
// or in the close frame on the WebSocket transport.
//
// They are the vocabulary a client acts on: the code a refusal
// travels in is shared between several of them, and the reason is
// what says whether to re-authorize, back off, resubscribe, or stop.
// The constants are untyped so they compare directly against the
// wire's plain string.
//
// The set is closed; a refusal with no reason of its own is not
// given an invented one.
const (
	// CloseReasonNoActiveSession: an observer asked to join a
	// document that has no live editing session. There is nothing
	// to observe, and nothing will arrive if the subscription is
	// retried; read the document's current version from the
	// repository instead, and subscribe again when an editor opens
	// one.
	CloseReasonNoActiveSession = "no_active_session"

	// CloseReasonSubscribeFailed: the subscription could not be
	// opened, for a reason with no vocabulary of its own. The
	// message carries the detail. Retrying may work. A subscribe
	// refused because the document is frozen is the one structured
	// case: its message is the freeze's SessionTerminated, which
	// Terminated decodes, and retrying will not work until the
	// document is unfrozen.
	CloseReasonSubscribeFailed = "subscribe_failed"

	// CloseReasonReadOnly: an update was sent on a read-only
	// subscription. The mode was on the Synced message; a client
	// that respects it never sees this.
	CloseReasonReadOnly = "read_only"

	// CloseReasonSessionTerminated: the editing session this
	// subscription belonged to has ended — it was frozen for a
	// publish, evicted, or the sketch was promoted. The document is
	// not gone; a fresh subscribe opens a new session from the
	// repository version. The message is a JSON object,
	// SessionTerminated, carrying why it ended and the state vector
	// and delete set it ended with; Terminated decodes it from the
	// error. A subscription the server reaped is closed with this
	// reason and a prose message instead, unless the document was
	// frozen under it.
	CloseReasonSessionTerminated = "session_terminated"

	// CloseReasonSessionEnding: an editor asked to subscribe while
	// the document's session was being evicted — the session has
	// ended, but the service is still writing its final version and
	// archive and has not yet released the repository lock, so there
	// is neither a session to join nor room to open one. Doc-scoped,
	// and no subscription is opened. The eviction finishes on its
	// own, so keep the document and its lineage and subscribe again
	// after a short wait; within the resume bounds the new session
	// continues the same lineage. A client that reconnects on
	// hearing session_terminated lands here as a matter of course,
	// and should back off rather than retry in a tight loop.
	CloseReasonSessionEnding = "session_ending"

	// CloseReasonLineageMismatch: the client's copy of the document
	// belongs to a different CRDT lineage than the session — the
	// lineage it declared on subscribe, or the seed client ID its
	// state vector implies, is not the session's. Its updates cannot
	// be merged without duplicating the document's structure, so the
	// server sends no catch-up and accepts nothing from it. This
	// happens to a client returning after its session ended and the
	// document was seeded afresh — or is about to be: with no session
	// open the copy is refused the same way, since the subscribe
	// would seed a lineage it cannot belong to. The message is a
	// JSON object, LineageMismatch, carrying the session's current
	// lineage (empty when there was no session), why the client's
	// lineage ended and the repository version it ended at, so the
	// client can tell the person what happened. Resubscribing with the same local state is refused
	// the same way: keep the orphaned copy, recover what is worth
	// keeping from it (into a sketch, say), and subscribe again with
	// an empty document.
	CloseReasonLineageMismatch = "lineage_mismatch"

	// CloseReasonSubscriptionLimit: the connection already holds as
	// many subscriptions as it may. Connection-wide: the whole
	// connection ends, not just the subscription that asked.
	// Unsubscribe from documents that are no longer on screen, or
	// open a second connection.
	CloseReasonSubscriptionLimit = "subscription_limit"

	// CloseReasonPayloadTooLarge: a single update or awareness
	// payload exceeded the per-message limit. Connection-wide,
	// because a client sending one is not producing edits a person
	// made. Batching less per update is the fix.
	CloseReasonPayloadTooLarge = "payload_too_large"

	// CloseReasonRateLimited: the connection sent updates faster
	// than the server accepts them for longer than the burst
	// allows. Connection-wide. Coalesce local edits before
	// forwarding them rather than reconnecting in a loop.
	CloseReasonRateLimited = "rate_limited"

	// CloseReasonTokenExpired: the bearer the connection opened with
	// has passed its expiry and was not refreshed in time.
	// Connection-wide. Get a fresh token and reconnect — and refresh
	// on the live connection before the deadline next time, which is
	// what keeps a session from dropping mid-edit.
	CloseReasonTokenExpired = "token_expired"
)
