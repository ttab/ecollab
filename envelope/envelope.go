// Package envelope is the multiplexed WebSocket wire format the
// elephant collab service speaks with browser clients. It is the
// specification as much as an implementation: both ends encode and
// decode with this package, so a frame either round-trips through it
// or is not on the protocol.
//
// The frame layout mirrors the protocol used by Hocuspocus / the
// y-protocols family, so an in-browser Yjs client written against
// that protocol interoperates unchanged.
//
// Frame on the wire:
//
//	varstring doc           // document name; multiplexing key
//	varuint   message_type  // see MessageType
//	bytes     payload       // type-specific, opaque to this package
//
// The doc-name acts as the multiplexing key: a single WebSocket
// connection can carry frames for any number of documents
// concurrently. The service routes an incoming frame to the matching
// subscription by looking up the doc name.
//
// Payloads are not length-prefixed here because the WebSocket frame
// length is authoritative — readers receive a single frame's worth of
// bytes from the underlying transport and decode the envelope over
// the full slice.
//
// The other live transport, the Collaborate bidirectional stream,
// carries the same messages as protobuf and needs no envelope. This
// package is the browser half of that pair.
package envelope

import (
	"errors"
	"fmt"
	"time"

	"github.com/ttab/ecollab/lib0"
)

// MessageType enumerates the top-level frame types. Numeric values
// match the conventions used by Hocuspocus / y-protocols so clients
// written against that protocol interoperate unchanged.
//
// Slot 4 is intentionally reserved (see MessageReservedHocuspocus4)
// because the Hocuspocus number space leaves a gap there. Frames
// arriving with that type are rejected with ErrInvalidFrame rather
// than silently classified as "unknown" — see Decode.
type MessageType uint64

const (
	// MessageSync carries y-protocols/sync payloads (step1, step2 or
	// update). The first varuint of the payload is the sync subtype
	// (see SyncType).
	MessageSync MessageType = 0
	// MessageAwareness carries y-protocols/awareness update bytes
	// directly. The collab service does not interpret the payload —
	// it ships it to peers verbatim.
	MessageAwareness MessageType = 1
	// MessageAuth carries an authentication or token-refresh message
	// (the auth and auth_refresh envelope messages from the design).
	// The payload is a varuint subtype + opaque bytes.
	MessageAuth MessageType = 2
	// MessageQueryAwareness asks peers to broadcast their current
	// awareness state. Empty payload.
	MessageQueryAwareness MessageType = 3
	// MessageReservedHocuspocus4 is reserved by the Hocuspocus
	// numbering scheme. The collab service does not use it; if a
	// frame with this type arrives, Decode returns ErrInvalidFrame
	// with a "reserved" annotation so the connection is closed with
	// a clear error rather than the frame being silently classified
	// as "unknown type" by a downstream handler.
	MessageReservedHocuspocus4 MessageType = 4
	// MessageStateless carries a free-form server-issued event
	// (publish-in-progress soft-stop, session-reset notification,
	// freeze evict, ...). The payload is a varstring identifying the
	// event followed by event-specific data.
	MessageStateless MessageType = 5
	// MessageClose is sent by the server to terminate a single doc
	// subscription with a reason. The payload is a varstring reason
	// followed by an optional free-form message.
	MessageClose MessageType = 6
	// MessageSynced is sent by the server after the initial state
	// transfer for a subscription completes. The client uses it as a
	// signal that the local doc has reached server-known state.
	MessageSynced MessageType = 7
	// MessageServerPing is sent by the server on a steady cadence
	// carrying the server's current wall-clock time as RFC3339Nano.
	// Clients capture the most recent value and echo it back as
	// `client_last_server_ping` in any freezing SnapshotRequest so
	// the server can enforce a clock-skew bound (see design
	// §"Snapshot endpoint"). Doc is "" — this is a control frame
	// scoped to the whole conn, not a per-doc message.
	MessageServerPing MessageType = 8
	// MessageSubscribeOptions carries per-doc subscribe options sent
	// BEFORE the SyncStep1 frame that initiates the subscription.
	// The server attaches the decoded options to the upcoming
	// subscribe; once the SyncStep1 arrives the options drain into
	// sub.Request. Sending MessageSubscribeOptions after a
	// subscription is already open is a no-op (the orchestrator's
	// per-subscribe inputs are fixed at subscribe time).
	//
	// Slot 9 sits one past MessageServerPing; the Hocuspocus number
	// space ends at 8 in the current generation so we own slot 9
	// onward as collab-specific extensions.
	MessageSubscribeOptions MessageType = 9
)

// SyncType is the first varuint inside a MessageSync payload.
type SyncType uint64

const (
	// SyncStep1 carries a client-side state vector. The server
	// responds with SyncStep2 plus the diff it needs.
	SyncStep1 SyncType = 0
	// SyncStep2 carries the diff from the other peer's state vector
	// to the current state.
	SyncStep2 SyncType = 1
	// SyncUpdate carries a normal Yjs update produced from a local
	// transaction.
	SyncUpdate SyncType = 2
	// SyncUpdateSeed carries a Yjs update the client identifies as
	// structural seeding (e.g. a frontend plugin reflecting canonical
	// document data into a `_collab` shadow tree, not a user-authored
	// edit). The payload is byte-identical to SyncUpdate; the
	// distinction lives only in the sub-type so the server can tag the
	// resulting stream entry with EncodingV1Seed / EncodingV2Seed and
	// audit / replay tools can de-emphasise authorship attribution on
	// seed bursts without losing the originating subscription_id.
	SyncUpdateSeed SyncType = 3
)

// MaxDocNameLen guards against pathological doc names overflowing
// buffers. 256 bytes covers UUIDs, URI-derived UUIDs, and the
// __user:{sub}:{kind} named-doc IDs from the design.
const MaxDocNameLen = 256

// Frame is the decoded form of one wire frame.
type Frame struct {
	Doc     string
	Type    MessageType
	Payload []byte
}

// ErrInvalidFrame is returned when a frame cannot be decoded. Callers
// should treat it as a fatal connection-level error.
var ErrInvalidFrame = errors.New("envelope: invalid frame")

// Encode encodes a frame to wire bytes. Rejects frames carrying
// MessageReservedHocuspocus4 — callers must not construct frames
// with the reserved type.
func Encode(f Frame) ([]byte, error) {
	if len(f.Doc) > MaxDocNameLen {
		return nil, fmt.Errorf("%w: doc name exceeds %d bytes", ErrInvalidFrame, MaxDocNameLen)
	}

	if f.Type == MessageReservedHocuspocus4 {
		return nil, fmt.Errorf("%w: message type %d is reserved", ErrInvalidFrame, f.Type)
	}

	var enc lib0.Encoder
	enc.WriteVarString(f.Doc)
	enc.WriteVarUint(uint64(f.Type))
	enc.WriteBytes(f.Payload)

	return enc.Bytes(), nil
}

// Decode parses one frame from wire bytes. Rejects frames carrying
// MessageReservedHocuspocus4 with a clear error so the connection is
// closed rather than the frame being silently routed as "unknown".
func Decode(b []byte) (Frame, error) {
	d := lib0.NewDecoder(b)

	doc, err := d.ReadVarString()
	if err != nil {
		return Frame{}, fmt.Errorf("%w: doc: %w", ErrInvalidFrame, err)
	}

	if len(doc) > MaxDocNameLen {
		return Frame{}, fmt.Errorf("%w: doc name exceeds %d bytes", ErrInvalidFrame, MaxDocNameLen)
	}

	typ, err := d.ReadVarUint()
	if err != nil {
		return Frame{}, fmt.Errorf("%w: type: %w", ErrInvalidFrame, err)
	}

	if MessageType(typ) == MessageReservedHocuspocus4 {
		return Frame{}, fmt.Errorf("%w: message type %d is reserved", ErrInvalidFrame, typ)
	}

	return Frame{
		Doc:     doc,
		Type:    MessageType(typ),
		Payload: d.ReadAll(),
	}, nil
}

// EncodeSyncStep1 builds a SyncStep1 frame carrying the client's
// state vector.
func EncodeSyncStep1(doc string, stateVector []byte) ([]byte, error) {
	var pl lib0.Encoder
	pl.WriteVarUint(uint64(SyncStep1))
	pl.WriteVarUint(uint64(len(stateVector)))
	pl.WriteBytes(stateVector)

	return Encode(Frame{Doc: doc, Type: MessageSync, Payload: pl.Bytes()})
}

// EncodeSyncStep2 builds a SyncStep2 frame carrying the diff from the
// peer's state vector to the local state.
func EncodeSyncStep2(doc string, diff []byte) ([]byte, error) {
	var pl lib0.Encoder
	pl.WriteVarUint(uint64(SyncStep2))
	pl.WriteVarUint(uint64(len(diff)))
	pl.WriteBytes(diff)

	return Encode(Frame{Doc: doc, Type: MessageSync, Payload: pl.Bytes()})
}

// EncodeSyncUpdate builds a SyncUpdate frame for a regular Yjs
// update.
func EncodeSyncUpdate(doc string, update []byte) ([]byte, error) {
	var pl lib0.Encoder
	pl.WriteVarUint(uint64(SyncUpdate))
	pl.WriteVarUint(uint64(len(update)))
	pl.WriteBytes(update)

	return Encode(Frame{Doc: doc, Type: MessageSync, Payload: pl.Bytes()})
}

// EncodeSyncUpdateSeed builds a SyncUpdateSeed frame — same payload
// shape as SyncUpdate, but tagged so the server writes the resulting
// stream entry with the seed encoding (EncodingV1Seed / EncodingV2Seed).
func EncodeSyncUpdateSeed(doc string, update []byte) ([]byte, error) {
	var pl lib0.Encoder
	pl.WriteVarUint(uint64(SyncUpdateSeed))
	pl.WriteVarUint(uint64(len(update)))
	pl.WriteBytes(update)

	return Encode(Frame{Doc: doc, Type: MessageSync, Payload: pl.Bytes()})
}

// EncodeAwareness builds an awareness frame carrying a
// y-protocols/awareness update.
func EncodeAwareness(doc string, awarenessUpdate []byte) ([]byte, error) {
	return Encode(Frame{Doc: doc, Type: MessageAwareness, Payload: awarenessUpdate})
}

// EncodeQueryAwareness builds a query-awareness frame (empty
// payload).
func EncodeQueryAwareness(doc string) ([]byte, error) {
	return Encode(Frame{Doc: doc, Type: MessageQueryAwareness})
}

// EncodeAuthRefresh builds an auth-refresh frame carrying a new
// bearer token for the connection.
func EncodeAuthRefresh(token string) ([]byte, error) {
	var pl lib0.Encoder
	pl.WriteVarUint(uint64(AuthSubtypeRefresh))
	pl.WriteVarString(token)
	// Doc is empty for connection-scoped messages — the auth refresh
	// applies to the connection, not a specific subscription.
	return Encode(Frame{Doc: "", Type: MessageAuth, Payload: pl.Bytes()})
}

// EncodeClose builds a close frame terminating one doc subscription.
func EncodeClose(doc, reason, message string) ([]byte, error) {
	var pl lib0.Encoder
	pl.WriteVarString(reason)
	pl.WriteVarString(message)

	return Encode(Frame{Doc: doc, Type: MessageClose, Payload: pl.Bytes()})
}

// EncodeSynced builds a synced frame for a doc, signalling that
// initial state transfer is complete and carrying the granted
// subscription mode ("read_only" / "read_write"). Clients use the
// mode to gate their write UI immediately on subscribe instead of
// discovering by attempting Forward and getting closed on
// read_only. An empty mode is permitted for legacy reasons but
// production callers should always populate it.
//
// The frame carries the mode and nothing else; EncodeSyncedPayload
// is the full form, which the service sends.
func EncodeSynced(doc, mode string) ([]byte, error) {
	var pl lib0.Encoder
	pl.WriteVarString(mode)

	return Encode(Frame{Doc: doc, Type: MessageSynced, Payload: pl.Bytes()})
}

// SyncedPayload is the decoded inner shape of a MessageSynced frame:
//
//	varstring mode          // "read_only" / "read_write"
//	varstring lineage       // the session's lineage; "" when unknown
//	varuint   sv_length
//	bytes     state_vector  // the server's lib0 state vector
//
// Lineage and StateVector are what the Collaborate stream carries in
// its Synced message: the lineage the client persists beside its
// local document and declares on its next subscribe, and the state
// vector the server's half of the handshake was computed at. A frame
// from a server that predates them ends after the mode, and
// DecodeSyncedPayload leaves both empty; a reader that only wants the
// mode can keep using DecodeSynced, which stops after the first
// field and ignores the rest.
type SyncedPayload struct {
	Mode        string
	Lineage     string
	StateVector []byte
}

// EncodeSyncedPayload builds a synced frame carrying the mode, the
// session's lineage and the server's state vector.
func EncodeSyncedPayload(doc string, p SyncedPayload) ([]byte, error) {
	var pl lib0.Encoder
	pl.WriteVarString(p.Mode)
	pl.WriteVarString(p.Lineage)
	pl.WriteVarUint(uint64(len(p.StateVector)))
	pl.WriteBytes(p.StateVector)

	return Encode(Frame{Doc: doc, Type: MessageSynced, Payload: pl.Bytes()})
}

// DecodeSyncedPayload parses a MessageSynced payload. A payload that
// ends after the mode — an empty one, or one from EncodeSynced —
// decodes with an empty lineage and a nil state vector.
func DecodeSyncedPayload(payload []byte) (SyncedPayload, error) {
	if len(payload) == 0 {
		return SyncedPayload{}, nil
	}

	d := lib0.NewDecoder(payload)

	mode, err := d.ReadVarString()
	if err != nil {
		return SyncedPayload{}, fmt.Errorf("synced: read mode: %w", err)
	}

	p := SyncedPayload{Mode: mode}

	if d.EOF() {
		return p, nil
	}

	p.Lineage, err = d.ReadVarString()
	if err != nil {
		return SyncedPayload{}, fmt.Errorf("synced: read lineage: %w", err)
	}

	n, err := d.ReadVarUint()
	if err != nil {
		return SyncedPayload{}, fmt.Errorf("synced: read state vector length: %w", err)
	}

	//nolint:gosec // bounded by the frame length; ReadN re-checks.
	sv, err := d.ReadN(int(n))
	if err != nil {
		return SyncedPayload{}, fmt.Errorf("synced: read state vector: %w", err)
	}

	if len(sv) > 0 {
		p.StateVector = sv
	}

	return p, nil
}

// DecodeSynced extracts the granted mode from a synced frame.
// Returns an empty string for legacy frames that lack the payload.
// Whatever follows the mode is left unread; DecodeSyncedPayload
// reads the whole frame.
func DecodeSynced(payload []byte) (string, error) {
	if len(payload) == 0 {
		return "", nil
	}

	d := lib0.NewDecoder(payload)

	mode, err := d.ReadVarString()
	if err != nil {
		return "", fmt.Errorf("synced: read mode: %w", err)
	}

	return mode, nil
}

// EncodeStateless builds a server-issued stateless event.
func EncodeStateless(doc, event string, eventPayload []byte) ([]byte, error) {
	var pl lib0.Encoder
	pl.WriteVarString(event)
	pl.WriteBytes(eventPayload)

	return Encode(Frame{Doc: doc, Type: MessageStateless, Payload: pl.Bytes()})
}

// EncodeServerPing builds a server-ping frame. The payload is a
// single varstring with the server's wall-clock time as
// RFC3339Nano. Clients echo this verbatim back to the server in
// SnapshotRequest.client_last_server_ping so the server can bound
// clock skew during freeze.
func EncodeServerPing(serverTime time.Time) ([]byte, error) {
	return EncodeServerPingString(serverTime.UTC().Format(time.RFC3339Nano))
}

// EncodeServerPingString builds a server-ping frame from an
// already-formatted RFC3339Nano timestamp. The transport-agnostic
// connection core formats the witness once and hands the same string
// to every wire it serves, so a parse-and-reformat on the way out
// cannot introduce skew between what one client echoes back and what
// another does.
func EncodeServerPingString(serverTime string) ([]byte, error) {
	var pl lib0.Encoder
	pl.WriteVarString(serverTime)

	return Encode(Frame{Doc: "", Type: MessageServerPing, Payload: pl.Bytes()})
}

// DecodeServerPing extracts the server-time string from a server-ping
// frame's payload. The returned value is the RFC3339Nano timestamp
// the server wrote when the frame was emitted.
func DecodeServerPing(payload []byte) (string, error) {
	d := lib0.NewDecoder(payload)

	ts, err := d.ReadVarString()
	if err != nil {
		return "", fmt.Errorf("server ping: read timestamp: %w", err)
	}

	return ts, nil
}

// SyncPayload is the decoded inner shape of a MessageSync frame.
type SyncPayload struct {
	Type SyncType
	Data []byte
}

// DecodeSync parses a MessageSync payload into its subtype and
// length-prefixed bytes.
func DecodeSync(payload []byte) (SyncPayload, error) {
	d := lib0.NewDecoder(payload)

	st, err := d.ReadVarUint()
	if err != nil {
		return SyncPayload{}, fmt.Errorf("%w: sync type: %w", ErrInvalidFrame, err)
	}

	n, err := d.ReadVarUint()
	if err != nil {
		return SyncPayload{}, fmt.Errorf("%w: sync length: %w", ErrInvalidFrame, err)
	}

	rest := d.Remaining()
	if uint64(len(rest)) != n {
		return SyncPayload{}, fmt.Errorf("%w: sync payload length mismatch: header=%d remaining=%d",
			ErrInvalidFrame, n, len(rest))
	}

	return SyncPayload{Type: SyncType(st), Data: rest}, nil
}

// AuthSubtype is the first varuint inside a MessageAuth payload.
type AuthSubtype uint64

const (
	// AuthSubtypeRefresh carries a new bearer token for the
	// connection. The body is a varstring containing the token.
	AuthSubtypeRefresh AuthSubtype = 0
)

// AuthPayload is the decoded inner shape of a MessageAuth frame.
type AuthPayload struct {
	Subtype AuthSubtype
	Token   string
}

// DecodeAuth parses a MessageAuth payload.
func DecodeAuth(payload []byte) (AuthPayload, error) {
	d := lib0.NewDecoder(payload)

	st, err := d.ReadVarUint()
	if err != nil {
		return AuthPayload{}, fmt.Errorf("%w: auth subtype: %w", ErrInvalidFrame, err)
	}

	tok, err := d.ReadVarString()
	if err != nil {
		return AuthPayload{}, fmt.Errorf("%w: auth token: %w", ErrInvalidFrame, err)
	}

	return AuthPayload{Subtype: AuthSubtype(st), Token: tok}, nil
}

// ClosePayload is the decoded inner shape of a MessageClose frame.
type ClosePayload struct {
	Reason  string
	Message string
}

// DecodeClose parses a MessageClose payload.
func DecodeClose(payload []byte) (ClosePayload, error) {
	d := lib0.NewDecoder(payload)

	reason, err := d.ReadVarString()
	if err != nil {
		return ClosePayload{}, fmt.Errorf("%w: close reason: %w", ErrInvalidFrame, err)
	}

	msg, err := d.ReadVarString()
	if err != nil {
		return ClosePayload{}, fmt.Errorf("%w: close message: %w", ErrInvalidFrame, err)
	}

	return ClosePayload{Reason: reason, Message: msg}, nil
}

// SubscribeOptions is the decoded payload of a
// MessageSubscribeOptions frame. Fields use pointer / slice
// indirection so absent-vs-empty is meaningful on the wire:
// AdvertisePresence==nil means "client did not set this option"
// (server default applies); a nil/empty FreezeOnWorkflowStates
// means "no migration freeze list".
type SubscribeOptions struct {
	// AdvertisePresence controls whether the subscription appears
	// in the presence document. nil → server default (true for
	// editors; false when Observer is true).
	AdvertisePresence *bool

	// FreezeOnWorkflowStates is the migration auto-freeze list.
	// See sub.Request.FreezeOnWorkflowStates and design §"Unfreeze /
	// Migration".
	FreezeOnWorkflowStates []string

	// Observer requests read-only subscription semantics: the
	// subscription joins an existing session without acquiring the
	// repo lock, cannot Forward updates or awareness, and (by
	// default) is silent in presence. Subscribes that arrive when
	// no session is live are rejected — observers join, they
	// don't open. nil means "client did not set this option"
	// (server default is false).
	Observer *bool

	// Lineage declares the lineage the client's local document
	// belongs to: the value the Synced frame reported when the
	// document was last in sync, persisted beside it. Empty for a
	// fresh client. The server refuses a lineage that is not the
	// session's with a Close carrying lineage_mismatch, before it
	// computes any diff against the Step 1 that follows.
	Lineage string
}

// SubscribeOption keys recognised in the on-wire encoding. The
// encoder/decoder ignores unknown keys (forward-compatibility) but
// readers can use these constants to assemble payloads correctly.
const (
	SubscribeOptionAdvertisePresence      = "advertise_presence"
	SubscribeOptionFreezeOnWorkflowStates = "freeze_on_workflow_states"
	SubscribeOptionObserver               = "observer"
	SubscribeOptionLineage                = "lineage"
)

// MaxLineageLen caps the byte length of a declared lineage. A
// lineage is a ULID, 26 bytes; the cap leaves room for another
// identifier shape without letting a hostile client pin a frame's
// worth of bytes on every pending subscribe.
const MaxLineageLen = 128

// MaxSubscribeOptionsEntries caps the per-frame option count to
// guard against pathological clients sending huge option maps. The
// design currently spells out two keys; the cap is well above any
// reasonable extension.
const MaxSubscribeOptionsEntries = 32

// MaxFreezeOnWorkflowStates caps the per-call workflow-state count.
// Deployments using this list typically pass a handful of states
// (e.g. ["usable"]); a four-figure list is almost certainly a bug
// or attack, and accepting it would let a hostile client force the
// orchestrator to compare against a vast scan list per subscribe.
const MaxFreezeOnWorkflowStates = 64

// MaxFreezeWorkflowStateLen caps the per-entry byte length of a
// workflow-state label. Real labels are short identifiers ("usable",
// "approved", etc.); without the cap a hostile client could pack
// 64 ~32 KiB strings into one frame and force the linear scan in
// the subscribe-time auto-freeze gate to do MB-class memcmp work
// per call.
const MaxFreezeWorkflowStateLen = 64

// EncodeSubscribeOptions builds a subscribe-options frame for doc.
// Option layout:
//
//	varuint  option_count
//	  for each option:
//	    varstring key
//	    varbytes  value (varuint length + bytes)
//
// Value encoding per key:
//
//   - "advertise_presence":          1 byte (0 or 1)
//   - "freeze_on_workflow_states":   varuint(N) + N varstring entries
//   - "observer":                    1 byte (0 or 1)
//   - "lineage":                     the lineage's bytes, as they are
//
// The double length prefix on the value side (outer varbytes plus
// inner key-specific encoding) is deliberate: it lets future
// readers skip over unknown option keys without parsing their
// payloads, keeping the format forward-compatible.
func EncodeSubscribeOptions(doc string, opts SubscribeOptions) ([]byte, error) {
	type kv struct {
		key   string
		value []byte
	}

	var entries []kv

	if opts.AdvertisePresence != nil {
		val := byte(0)
		if *opts.AdvertisePresence {
			val = 1
		}

		entries = append(entries, kv{
			key:   SubscribeOptionAdvertisePresence,
			value: []byte{val},
		})
	}

	if opts.Observer != nil {
		val := byte(0)
		if *opts.Observer {
			val = 1
		}

		entries = append(entries, kv{
			key:   SubscribeOptionObserver,
			value: []byte{val},
		})
	}

	if opts.Lineage != "" {
		if len(opts.Lineage) > MaxLineageLen {
			return nil, fmt.Errorf("%w: lineage is %d bytes, max %d",
				ErrInvalidFrame, len(opts.Lineage), MaxLineageLen)
		}

		entries = append(entries, kv{
			key:   SubscribeOptionLineage,
			value: []byte(opts.Lineage),
		})
	}

	if len(opts.FreezeOnWorkflowStates) > 0 {
		if len(opts.FreezeOnWorkflowStates) > MaxFreezeOnWorkflowStates {
			return nil, fmt.Errorf("%w: freeze_on_workflow_states count %d exceeds %d",
				ErrInvalidFrame, len(opts.FreezeOnWorkflowStates), MaxFreezeOnWorkflowStates)
		}

		var inner lib0.Encoder
		inner.WriteVarUint(uint64(len(opts.FreezeOnWorkflowStates)))

		for i, st := range opts.FreezeOnWorkflowStates {
			if len(st) > MaxFreezeWorkflowStateLen {
				return nil, fmt.Errorf(
					"%w: freeze_on_workflow_states entry %d is %d bytes, max %d",
					ErrInvalidFrame, i, len(st), MaxFreezeWorkflowStateLen)
			}

			inner.WriteVarString(st)
		}

		entries = append(entries, kv{
			key:   SubscribeOptionFreezeOnWorkflowStates,
			value: inner.Bytes(),
		})
	}

	if len(entries) > MaxSubscribeOptionsEntries {
		return nil, fmt.Errorf("%w: option count %d exceeds %d",
			ErrInvalidFrame, len(entries), MaxSubscribeOptionsEntries)
	}

	var pl lib0.Encoder
	pl.WriteVarUint(uint64(len(entries)))

	for _, e := range entries {
		pl.WriteVarString(e.key)
		pl.WriteVarUint(uint64(len(e.value)))
		pl.WriteBytes(e.value)
	}

	return Encode(Frame{Doc: doc, Type: MessageSubscribeOptions, Payload: pl.Bytes()})
}

// DecodeSubscribeOptions parses a MessageSubscribeOptions payload.
// Unknown option keys are skipped (the outer varbytes length lets
// us advance the cursor without interpreting the value). Returns
// ErrInvalidFrame for malformed payloads.
func DecodeSubscribeOptions(payload []byte) (SubscribeOptions, error) {
	d := lib0.NewDecoder(payload)

	count, err := d.ReadVarUint()
	if err != nil {
		return SubscribeOptions{}, fmt.Errorf("%w: option count: %w", ErrInvalidFrame, err)
	}

	if count > MaxSubscribeOptionsEntries {
		return SubscribeOptions{}, fmt.Errorf("%w: option count %d exceeds %d",
			ErrInvalidFrame, count, MaxSubscribeOptionsEntries)
	}

	var opts SubscribeOptions

	for i := uint64(0); i < count; i++ {
		key, err := d.ReadVarString()
		if err != nil {
			return SubscribeOptions{}, fmt.Errorf("%w: option key: %w", ErrInvalidFrame, err)
		}

		valLen, err := d.ReadVarUint()
		if err != nil {
			return SubscribeOptions{}, fmt.Errorf("%w: option value length: %w", ErrInvalidFrame, err)
		}

		//nolint:gosec // bounded above by frame length; ReadN re-checks.
		valBytes, err := d.ReadN(int(valLen))
		if err != nil {
			return SubscribeOptions{}, fmt.Errorf("%w: option value bytes: %w", ErrInvalidFrame, err)
		}

		switch key {
		case SubscribeOptionAdvertisePresence:
			if len(valBytes) != 1 {
				return SubscribeOptions{}, fmt.Errorf(
					"%w: advertise_presence value must be 1 byte, got %d",
					ErrInvalidFrame, len(valBytes))
			}

			b := valBytes[0] != 0
			opts.AdvertisePresence = &b
		case SubscribeOptionObserver:
			if len(valBytes) != 1 {
				return SubscribeOptions{}, fmt.Errorf(
					"%w: observer value must be 1 byte, got %d",
					ErrInvalidFrame, len(valBytes))
			}

			b := valBytes[0] != 0
			opts.Observer = &b
		case SubscribeOptionFreezeOnWorkflowStates:
			states, err := decodeSubscribeFreezeStates(valBytes)
			if err != nil {
				return SubscribeOptions{}, err
			}

			opts.FreezeOnWorkflowStates = states
		case SubscribeOptionLineage:
			if len(valBytes) > MaxLineageLen {
				return SubscribeOptions{}, fmt.Errorf(
					"%w: lineage is %d bytes, max %d",
					ErrInvalidFrame, len(valBytes), MaxLineageLen)
			}

			opts.Lineage = string(valBytes)
		default:
			// Unknown key — skip silently for forward compatibility.
			// The outer length prefix already advanced the cursor.
		}
	}

	return opts, nil
}

// decodeSubscribeFreezeStates decodes the inner varuint(N) + N
// varstring representation of FreezeOnWorkflowStates.
func decodeSubscribeFreezeStates(b []byte) ([]string, error) {
	d := lib0.NewDecoder(b)

	n, err := d.ReadVarUint()
	if err != nil {
		return nil, fmt.Errorf("%w: freeze_on_workflow_states count: %w", ErrInvalidFrame, err)
	}

	if n > MaxFreezeOnWorkflowStates {
		return nil, fmt.Errorf("%w: freeze_on_workflow_states count %d exceeds %d",
			ErrInvalidFrame, n, MaxFreezeOnWorkflowStates)
	}

	out := make([]string, 0, n)

	for i := uint64(0); i < n; i++ {
		s, err := d.ReadVarString()
		if err != nil {
			return nil, fmt.Errorf("%w: freeze_on_workflow_states entry %d: %w", ErrInvalidFrame, i, err)
		}

		if len(s) > MaxFreezeWorkflowStateLen {
			return nil, fmt.Errorf(
				"%w: freeze_on_workflow_states entry %d is %d bytes, max %d",
				ErrInvalidFrame, i, len(s), MaxFreezeWorkflowStateLen)
		}

		out = append(out, s)
	}

	return out, nil
}

// StatelessPayload is the decoded inner shape of a MessageStateless
// frame.
type StatelessPayload struct {
	Event string
	Data  []byte
}

// DecodeStateless parses a MessageStateless payload.
func DecodeStateless(payload []byte) (StatelessPayload, error) {
	d := lib0.NewDecoder(payload)

	event, err := d.ReadVarString()
	if err != nil {
		return StatelessPayload{}, fmt.Errorf("%w: stateless event: %w", ErrInvalidFrame, err)
	}

	return StatelessPayload{Event: event, Data: d.ReadAll()}, nil
}
