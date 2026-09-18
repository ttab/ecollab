package ecollab

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"slices"

	"github.com/ttab/goyjs"
)

// The awareness state fields an editor built on @slate-yjs/core
// publishes a caret under, which are that library's defaults:
// `selection` holds the caret itself — withCursors' cursorStateField —
// and `data` the descriptor drawn beside it — its cursorDataField — a
// display name, a colour. Both are the application's choice, so both
// are options rather than constants in the code path; these are the
// names to expect when nobody has chosen otherwise, and they are held
// to the library's release in cursor_slate_test.go rather than to
// this file, because a reader looking under the wrong name sees an
// empty room and no error.
const (
	DefaultCursorField     = "selection"
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
// be nil, as for every goyjs read; each end is then resolved under a
// hold of its own, and a write can land between the two, so a caller
// reading the document under a transaction passes it.
//
// The two ends are resolved on their own, because a selection can
// have one end in a value a peer has since deleted and the other in a
// value that is still there - a selection from one paragraph into the
// next, and the next paragraph gone. The end that resolved is
// returned, the end whose value is gone is the zero goyjs.Resolved with
// AnchorGone or FocusGone set, and the caller still learns which value
// the peer is in.
//
// The errors are goyjs.Doc.Resolve's, and the two a caller acts on
// differently are goyjs.ErrStaleNode - both ends' values are gone,
// which is an ordinary outcome of concurrent editing - and
// goyjs.ErrPositionUnseen, which means the peer is ahead of this
// document and the answer is to sync and resolve again, not to
// discard the cursor. An unseen end takes precedence: the sync it asks
// for is what settles the other end too.
func (c Cursor) Resolve(doc *goyjs.Doc, t *goyjs.ReadTxn) (ResolvedCursor, error) {
	r := ResolvedCursor{ClientID: c.ClientID, Data: c.Data}

	var anchorErr, focusErr error

	r.Range.Anchor, anchorErr = doc.Resolve(t, c.Range.Anchor)
	r.Range.Focus, focusErr = doc.Resolve(t, c.Range.Focus)

	r.AnchorGone = anchorErr != nil && errors.Is(anchorErr, goyjs.ErrStaleNode)
	r.FocusGone = focusErr != nil && errors.Is(focusErr, goyjs.ErrStaleNode)

	// One end gone and the other resolved is an answer: the end that
	// is there says where the peer is.
	if r.AnchorGone && focusErr == nil {
		r.Range.Anchor = goyjs.Resolved{}

		return r, nil
	}

	if r.FocusGone && anchorErr == nil {
		r.Range.Focus = goyjs.Resolved{}

		return r, nil
	}

	err := cursorError(anchorErr, focusErr)
	if err != nil {
		return ResolvedCursor{}, fmt.Errorf(
			"resolve the cursor of client %d: %w", c.ClientID, err)
	}

	return r, nil
}

// cursorError picks the error a cursor with a failed end reports:
// nil when both resolved, an unseen end before anything else, then
// the anchor's before the focus's.
func cursorError(anchorErr, focusErr error) error {
	switch {
	case anchorErr == nil && focusErr == nil:
		return nil
	case anchorErr != nil && errors.Is(anchorErr, goyjs.ErrPositionUnseen):
		return anchorErr
	case focusErr != nil && errors.Is(focusErr, goyjs.ErrPositionUnseen):
		return focusErr
	case anchorErr != nil:
		return anchorErr
	default:
		return focusErr
	}
}

// ResolvedCursor is a Cursor mapped onto a document: which shared
// type the peer is editing, and where in it. It is true for the
// transaction it was resolved under and no longer - the peer is
// still typing.
type ResolvedCursor struct {
	// ClientID is the Yjs client the caret belongs to.
	ClientID uint64

	// Range holds both ends, each with the kind of the type it
	// points into, a handle to it and the offset in it. Span answers
	// the ordered extent when both ends landed in the same type. An
	// end whose value is gone is the zero goyjs.Resolved - no Kind,
	// no handle - and AnchorGone or FocusGone says so; Span then
	// reports ok == false, and Collab.Editing is false for that end.
	Range goyjs.ResolvedRange

	// AnchorGone and FocusGone report an end whose value has been
	// deleted or replaced while the other end resolved. Both gone is
	// goyjs.ErrStaleNode from Resolve, never a ResolvedCursor.
	AnchorGone bool
	FocusGone  bool

	// Data is Cursor.Data, carried through.
	Data json.RawMessage
}

// Ends returns the ends that resolved, anchor first: both for a
// selection whose values are still there, one when the other's is
// gone. It is what a caller that asks per value "is this peer here"
// iterates over.
func (r ResolvedCursor) Ends() []goyjs.Resolved {
	ends := make([]goyjs.Resolved, 0, 2)

	if !r.AnchorGone {
		ends = append(ends, r.Range.Anchor)
	}

	if !r.FocusGone {
		ends = append(ends, r.Range.Focus)
	}

	return ends
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
// client, ordered by client ID, and returns every caret it could read
// together with an error joining every state it could not - a state
// that is not a JSON object, an envelope with no anchor and focus
// pair, a position neither Yjs encoding produces. One foreign or
// broken peer therefore costs a caller that peer's caret and not the
// room: act on the carets returned, and log the error, which names
// each client it could not read.
//
// A participant whose state carries no caret field, or a null one, is
// not an error and not a caret: it is a participant who is present and
// has no selection. That is also what a reader told the wrong field
// name sees of everybody, silently, which is why the default names are
// held to the editor library that writes them rather than assumed.
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

	var errs []error

	for _, clientID := range clients {
		c, ok, err := r.Cursor(clientID, states[clientID])
		if err != nil {
			errs = append(errs, err)

			continue
		}

		if !ok {
			continue
		}

		out = append(out, c)
	}

	return out, errors.Join(errs...)
}

// Editing reports whether end — one end of a resolved cursor, or any
// other resolved position — points into the value application appID
// holds for field on this owner, or into anything nested inside it.
// It is the answer to "is this peer in the block I am about to edit",
// asked per block against the caret read out of awareness.
//
// The nesting is the point. An editor stores a field as a Y.XmlText
// whose delta is a sequence of embedded paragraph blocks, and a caret
// resolves into the innermost type it lies in — the paragraph, or an
// inline node inside the paragraph — never into the field itself. So
// the question is containment, goyjs.Value.Holds over the field's
// value, not identity against the field's node.
//
// It is false when the owner has no such field, when the field holds
// no rich value, and when the position landed anywhere outside the
// field. An editor bound to one field publishes both ends of a
// selection against that field, so a selection is inside one value
// whichever paragraphs it spans, and either end answers for it; a
// caller that must leave the whole selection alone at the paragraph
// level walks the field's delta from the block holding one end to the
// block holding the other.
func (c *Collab) Editing(
	t *goyjs.ReadTxn, appID, field string, end goyjs.Resolved,
) bool {
	if end.Node == nil {
		return false
	}

	v, ok := c.Field(t, appID, field)
	if !ok {
		return false
	}

	return v.Holds(end.Node)
}

// isNull reports whether raw is the JSON null literal, which is what
// an editor publishes for a participant with no selection.
func isNull(raw json.RawMessage) bool {
	return bytes.Equal(bytes.TrimSpace(raw), []byte("null"))
}
