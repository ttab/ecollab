package ecollab

// Encoding tags what one entry in a document's update log carries.
// It is how a reader of the log decides what to do with the bytes
// next to the tag: feed them to Yjs, hand them to the awareness
// protocol, or read them as a lifecycle marker the service issued
// on its own behalf.
//
// The tag travels with the entry wherever the entry goes — the
// service's per-document stream, the S3 archive, and
// SessionUpdateRecord.encoding on the archive-reading RPCs — so a
// program replaying a session sees the same vocabulary the live
// pipeline used.
//
// The set is closed and the values are durable: an archived record
// written years ago still carries one of these strings, so a value
// can be added but never repurposed.
type Encoding string

const (
	// EncodingV1 and EncodingV2 are Yjs document updates in the
	// lib0 v1 and v2 formats. Apply them to a Y.Doc in stream order.
	EncodingV1 Encoding = "v1"
	EncodingV2 Encoding = "v2"

	// EncodingV1Seed and EncodingV2Seed are Yjs updates the emitter
	// identified as structural seeding rather than authorship: the
	// service's own seed of a fresh session from the repository
	// version, or a client reflecting canonical state into a
	// _collab shadow tree. The bytes apply exactly as EncodingV1 and
	// EncodingV2 do — a replay must not skip them — but an audit or
	// replay visualiser can use the tag to leave the burst
	// unattributed. The originating subscription is still recorded
	// on the entry, so the emitter is still named.
	EncodingV1Seed Encoding = "v1-seed"
	EncodingV2Seed Encoding = "v2-seed"

	// EncodingV1Resync is a Yjs v1 update that arrived in a client's
	// sync step 2 during the handshake that opens a subscription:
	// what the client had and the session lacked, which is to say
	// edits made while the client was away. The payload is
	// byte-identical to EncodingV1 and applies exactly as it does;
	// the update is persisted, archived and counted as authored by
	// the subscription that sent it like any other. The tag is the
	// service's, stamped from where the update arrived rather than
	// claimed by the client, and it is what lets a reader of the
	// log tell offline edits from live ones. An update the client
	// forwards after Synced is plain EncodingV1.
	EncodingV1Resync Encoding = "v1-resync"

	// EncodingAwareness is a y-protocols/awareness update: who is
	// present, where their cursor is. It is not document state and
	// must not be applied to a Y.Doc. The service ships the payload
	// verbatim without interpreting it.
	EncodingAwareness Encoding = "aw"

	// EncodingEvict is a server-issued marker saying the session
	// this entry belongs to has been terminated — the last thing in
	// the log before the stream is dropped. A live subscriber sees
	// it as a Close with reason CloseReasonSessionTerminated; a
	// reader of the archive sees where the session ended.
	EncodingEvict Encoding = "evict"

	// EncodingStateless is a server-issued lifecycle event:
	// publish_in_progress, publish_cleared, session_reset and the
	// like. The payload is JSON of the shape
	// {"event": ..., "data": ...}, and a live subscriber sees it as
	// the named event rather than as document state.
	EncodingStateless Encoding = "stateless"

	// EncodingSyncStep2 tags the catch-up diff the service computes
	// for one subscriber against the state vector it subscribed
	// with. It is addressed to a single subscriber rather than to
	// the document, so it is never persisted and never appears in
	// the archive or on SessionUpdateRecord.encoding — only inside
	// the service, on the path that delivers a subscribe.
	EncodingSyncStep2 Encoding = "step2"
)
