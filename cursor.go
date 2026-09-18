package ecollab

import (
	"bytes"
	"encoding/json"
	"fmt"
	"slices"

	"github.com/ttab/goyjs"
)

// The awareness state fields an editor built on @slate-yjs/core
// publishes a caret under, which are that library's defaults:
// `cursor` holds the caret itself and `data` the descriptor drawn
// beside it — a display name, a colour. Both are the application's
// choice, so both are options rather than constants in the code
// path; these are the names to expect when nobody has chosen
// otherwise.
const (
	DefaultCursorField     = "cursor"
	DefaultCursorDataField = "data"
)

// Cursor is one participant's caret as their editor published it:
// the pair of relative positions that says where in a shared type
// the selection is, and whatever descriptor the editor publishes
// beside it.
//
// The positions are places, not offsets — they are still the same
// places after the peer types on. Resolve maps them onto a document
// to get the shared type and the offsets in it as they are now.
type Cursor struct {
	// ClientID is the Yjs client the caret belongs to. It is the key
	// of the awareness state it was read out of, and the only
	// identity the protocol itself carries; who that client is comes
	// from Data, or from the presence document.
	ClientID uint64

	// Range is the caret: an anchor and a focus, the same position
	// twice when the selection is collapsed. A selection made
	// backwards has its focus before its anchor, which
	// goyjs.ResolvedRange.Span accounts for.
	Range goyjs.Range

	// Data is the descriptor published beside the caret, as it was
	// on the wire. Its shape is the editor's, so it is left as JSON
	// for the caller to unmarshal into whatever the editor in use
	// writes. Nil when the state carries none.
	Data json.RawMessage
}

// Resolve maps the caret onto doc as the transaction sees it: the
// shared type each end points into, and the offset in it now. t may
// be nil, as for every goyjs read.
//
// The errors are goyjs.Doc.ResolveRange's, and the two a caller acts
// on differently are goyjs.ErrStaleNode — the value the peer was
// editing is gone, which is an ordinary outcome of concurrent
// editing — and goyjs.ErrPositionUnseen, which means the peer is
// ahead of this document and the answer is to sync and resolve
// again, not to discard the cursor.
func (c Cursor) Resolve(doc *goyjs.Doc, t *goyjs.ReadTxn) (ResolvedCursor, error) {
	r, err := doc.ResolveRange(t, c.Range)
	if err != nil {
		return ResolvedCursor{}, fmt.Errorf(
			"resolve the cursor of client %d: %w", c.ClientID, err)
	}

	return ResolvedCursor{
		ClientID: c.ClientID,
		Range:    r,
		Data:     c.Data,
	}, nil
}

// ResolvedCursor is a Cursor mapped onto a document: which shared
// type the peer is editing, and where in it. It is true for the
// transaction it was resolved under and no longer — the peer is
// still typing.
type ResolvedCursor struct {
	// ClientID is the Yjs client the caret belongs to.
	ClientID uint64

	// Range holds both ends, each with the kind of the type it
	// points into, a handle to it and the offset in it. Span answers
	// the ordered extent when both ends landed in the same type.
	Range goyjs.ResolvedRange

	// Data is Cursor.Data, carried through.
	Data json.RawMessage
}

// CursorReader reads carets out of awareness states. The envelope an
// editor wraps a position in is the application's — which state
// field carries the caret, which one carries the descriptor beside
// it — so a reader is configured with the names the editor in use
// writes, and a change there is a change in one place.
//
// The pair inside the envelope is `anchor` and `focus`, which is
// goyjs.Range's spelling and slate-yjs's. An editor that wraps its
// positions differently is decoded by unmarshalling the state into
// the caller's own type and letting goyjs.Position decode each end;
// that is the whole of what the library promises, and this type is
// only the common case.
type CursorReader struct {
	field     string
	dataField string
}

// CursorOption configures a CursorReader.
type CursorOption func(*CursorReader)

// WithCursorField sets the awareness state field the caret is
// published under. The default is DefaultCursorField.
func WithCursorField(field string) CursorOption {
	return func(r *CursorReader) {
		r.field = field
	}
}

// WithCursorDataField sets the awareness state field the descriptor
// beside the caret is published under. The default is
// DefaultCursorDataField; an empty name reads no descriptor at all.
func WithCursorDataField(field string) CursorOption {
	return func(r *CursorReader) {
		r.dataField = field
	}
}

// NewCursorReader returns a reader for the default field names,
// changed by the options given.
func NewCursorReader(opts ...CursorOption) *CursorReader {
	r := CursorReader{
		field:     DefaultCursorField,
		dataField: DefaultCursorDataField,
	}

	for _, opt := range opts {
		opt(&r)
	}

	return &r
}

// Cursor reads the caret out of one participant's awareness state,
// and reports false when the state carries none: the field is
// absent, or null, which is what an editor publishes for a
// participant who is present but has no selection.
//
// An envelope that is there but cannot be read — a caret with no
// anchor, a position neither Yjs encoding produces — is an error
// rather than a silent absence: it means the editor and this reader
// disagree about the envelope, which is worth seeing.
func (r *CursorReader) Cursor(
	clientID uint64, state json.RawMessage,
) (Cursor, bool, error) {
	var fields map[string]json.RawMessage

	if err := json.Unmarshal(state, &fields); err != nil {
		return Cursor{}, false, fmt.Errorf(
			"decode the awareness state of client %d: %w", clientID, err)
	}

	raw, ok := fields[r.field]
	if !ok || isNull(raw) {
		return Cursor{}, false, nil
	}

	var envelope struct {
		Anchor *goyjs.Position `json:"anchor"`
		Focus  *goyjs.Position `json:"focus"`
	}

	if err := json.Unmarshal(raw, &envelope); err != nil {
		return Cursor{}, false, fmt.Errorf(
			"decode the %s field of client %d: %w", r.field, clientID, err)
	}

	if envelope.Anchor == nil || envelope.Focus == nil {
		return Cursor{}, false, fmt.Errorf(
			"the %s field of client %d has no anchor and focus pair",
			r.field, clientID)
	}

	c := Cursor{
		ClientID: clientID,
		Range: goyjs.Range{
			Anchor: *envelope.Anchor,
			Focus:  *envelope.Focus,
		},
	}

	if r.dataField != "" {
		data, ok := fields[r.dataField]
		if ok && !isNull(data) {
			c.Data = data
		}
	}

	return c, true, nil
}

// Cursors reads the carets of every participant in a but the local
// client, ordered by client ID. A participant with no caret is left
// out; a state that cannot be read is an error, so a reader that has
// drifted from the editor is not mistaken for an empty room. The
// states are read in client order, so which error that is does not
// depend on map iteration.
func (r *CursorReader) Cursors(a *goyjs.Awareness) ([]Cursor, error) {
	states := a.States()

	clients := make([]uint64, 0, len(states))

	for clientID := range states {
		if clientID == a.ClientID() {
			continue
		}

		clients = append(clients, clientID)
	}

	slices.Sort(clients)

	out := make([]Cursor, 0, len(clients))

	for _, clientID := range clients {
		c, ok, err := r.Cursor(clientID, states[clientID])
		if err != nil {
			return nil, err
		}

		if !ok {
			continue
		}

		out = append(out, c)
	}

	return out, nil
}

// Editing reports whether end — one end of a resolved cursor, or any
// other resolved position — points into the value application appID
// holds for field on this owner. It is the answer to "is this peer in
// the block I am about to edit", asked per block against the caret
// read out of awareness.
//
// It is false when the owner has no such field, when the field holds
// something that is not a rich value, and when the position landed in
// a different type. A peer whose selection spans two blocks has its
// two ends in different values, so a caller that must not touch
// either asks about both.
func (c *Collab) Editing(
	t *goyjs.ReadTxn, appID, field string, end goyjs.Resolved,
) bool {
	if end.Node == nil {
		return false
	}

	node, ok := c.FieldNode(t, appID, field)
	if !ok {
		return false
	}

	return node.Same(end.Node)
}

// isNull reports whether raw is the JSON null literal, which is what
// an editor publishes for a participant with no selection.
func isNull(raw json.RawMessage) bool {
	return bytes.Equal(bytes.TrimSpace(raw), []byte("null"))
}
