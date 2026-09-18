package ecollab

import (
	"errors"
	"fmt"

	"github.com/ttab/goyjs"
)

// HydratedKey names the marker map an authoring application keeps
// beside its fields: a YMap whose keys are field keys and whose
// values mark the field as created and filled. It is the signal that
// tells "the field does not exist yet" apart from "the field exists
// and is empty", which matters because the two call for opposite
// actions — the first for a create that races every other
// participant's create, the second for an ordinary edit that races
// nothing.
//
// The marker is the application's own: nothing in this package writes
// it, and Hydrated is the only thing that reads it. It is named here
// so a program that has to consult it does not have to guess the
// spelling.
const HydratedKey = "_hydrated"

// ErrCollabShape reports a value on the path to a field that is not
// the kind the layout calls for — a _collab key holding a string, an
// application key holding an array. It means some participant is
// writing a different structure under the same keys, and the caller
// must not paper over it by overwriting: whatever is there belongs to
// someone.
var ErrCollabShape = errors.New("ecollab: unexpected kind under " + CollabKey)

// Collab addresses the application-private state of one document or
// block YMap: the CollabKey map, the per-application maps inside it,
// and the rich values an application holds per editable field.
//
//	document                 the root YMap, named RootName
//	  _collab                CollabKey, a YMap keyed by application ID
//	    se.ecms.editor       one YMap per application
//	      _hydrated          HydratedKey, the application's markers
//	      title              one rich value per editable field
//	      body
//
// The layout below CollabKey is the authoring application's, not this
// package's: Materialize skips the key and has no opinion about what
// is under it. What this type offers is the reach — a program that
// edits the same values a human editor is editing should not have to
// spell the path out again, and should not have to invent the
// create-if-absent.
//
// Every read takes a goyjs.ReadTxn and every write a goyjs.WriteTxn,
// so the reach composes with whatever else the caller is doing in the
// same transaction.
type Collab struct {
	owner *goyjs.Map
}

// CollabOn returns a handle to the application-private state of owner,
// which is a document root YMap — doc.Map(RootName) — or a block YMap
// read out of one. Nothing is read or created; the handle is a path,
// and the maps it names may all be absent.
func CollabOn(owner *goyjs.Map) *Collab {
	return &Collab{owner: owner}
}

// Map returns the CollabKey map of the owner, and false when the owner
// does not have one yet. A value of another kind is absent as far as
// this reports: use EnsureField, which says so with ErrCollabShape,
// when the distinction matters.
func (c *Collab) Map(t *goyjs.ReadTxn) (*goyjs.Map, bool) {
	if c == nil || c.owner == nil {
		return nil, false
	}

	v, ok := c.owner.Get(t, CollabKey)
	if !ok || v.Kind() != goyjs.KindMap {
		return nil, false
	}

	return v.Map(), true
}

// App returns the map application appID keeps under CollabKey, and
// false when there is none. The map holds the application's fields and
// its HydratedKey markers side by side.
func (c *Collab) App(t *goyjs.ReadTxn, appID string) (*goyjs.Map, bool) {
	collab, ok := c.Map(t)
	if !ok {
		return nil, false
	}

	v, ok := collab.Get(t, appID)
	if !ok || v.Kind() != goyjs.KindMap {
		return nil, false
	}

	return v.Map(), true
}

// Field returns the value application appID holds for field, and false
// when there is none. The value is whatever the application put there;
// a program that means to edit it as rich text checks that the kind is
// goyjs.KindXMLText or goyjs.KindText before doing so, or uses
// FieldNode, which does that check.
func (c *Collab) Field(t *goyjs.ReadTxn, appID, field string) (goyjs.Value, bool) {
	app, ok := c.App(t, appID)
	if !ok {
		return goyjs.Value{}, false
	}

	v, ok := app.Get(t, field)
	if !ok {
		return goyjs.Value{}, false
	}

	return v, true
}

// FieldNode returns a handle to the rich value application appID holds
// for field, and false when there is none or it is not a rich type.
//
// The handle addresses the node by identity, so it keeps naming the
// same value while collaborators edit around it, and it is what the
// goyjs write methods take: Insert, Delete, Format, ApplyDelta,
// SetAttribute. Reading it again under a later transaction with
// Node.Value reports false once the value is gone — deleted, or
// replaced by a create that lost. That is the ordinary outcome of
// concurrent editing, and the answer to it is to read the field again.
func (c *Collab) FieldNode(t *goyjs.ReadTxn, appID, field string) (*goyjs.Node, bool) {
	v, ok := c.Field(t, appID, field)
	if !ok {
		return nil, false
	}

	n := v.Node()
	if n == nil {
		return nil, false
	}

	return n, true
}

// Hydrated reports whether application appID has marked field as
// created and filled, by holding the field key in its HydratedKey map
// with a value that is not false.
//
// It answers a question a program creating a field has to ask first:
// an unhydrated field is one the application is about to create, and
// creating it from two places at once is how the value gets lost. See
// EnsureField.
func (c *Collab) Hydrated(t *goyjs.ReadTxn, appID, field string) bool {
	app, ok := c.App(t, appID)
	if !ok {
		return false
	}

	markers, ok := app.Get(t, HydratedKey)
	if !ok || markers.Kind() != goyjs.KindMap {
		return false
	}

	marker, ok := markers.Map().Get(t, field)
	if !ok {
		return false
	}

	if marker.Kind() == goyjs.KindBool {
		return marker.Bool()
	}

	return true
}

// EnsureField gives application appID the value for field if it does
// not have one, creating the CollabKey map and the application's map
// on the way, and reports whether it created anything. An existing
// value of any kind is left alone: it belongs to whoever wrote it.
//
// The whole thing happens inside the caller's write scope — the reads
// go through w.ReadTxn — so the check and the create are one
// transaction and no other goroutine on this document can slip between
// them. The created subtree is written whole, so a peer never observes
// the map without the field or the field without its content.
//
// What that does not buy is safety against a peer, and the difference
// is worth stating plainly. Yjs resolves two participants setting the
// same map key to one winner; the loser's map is deleted with
// everything under it, including text typed into a rich value it
// held. A program that creates a field another participant is also
// about to create can therefore destroy that participant's work, and
// nothing on the wire prevents it. The same hazard applies to each of
// the three levels: two participants creating CollabKey concurrently
// lose one application's whole state.
//
// So creating is the exception, and this is the safe order of
// business:
//
//   - Edit a field that exists. Read it with FieldNode and write
//     through the node; two participants editing one rich value is
//     what Yjs is for, and it costs nothing.
//   - Create only what the authoring application has not. Hydrated
//     reports whether the application considers the field its own; a
//     field it has marked is one to edit, never one to replace.
//   - Where a create is unavoidable, do it once and early, before
//     anyone has typed into the field, so a lost create costs an empty
//     value rather than a paragraph.
//
// After a create that lost, the goyjs handles say so rather than
// failing silently: a write through the node returns an error matching
// goyjs.ErrStaleNode, and Node.Value reports false. The response to
// both is to read the field again and work on what is there.
//
// A value on the path that is not a map is refused with
// ErrCollabShape, and nothing is recorded in the scope.
func (c *Collab) EnsureField(
	w *goyjs.WriteTxn, appID, field string, value goyjs.Input,
) (bool, error) {
	if c == nil || c.owner == nil {
		return false, errors.New("ecollab: nil owner map")
	}

	if appID == "" || field == "" {
		return false, errors.New("ecollab: empty application ID or field key")
	}

	t := w.ReadTxn()

	collab, ok := c.owner.Get(t, CollabKey)
	if !ok {
		c.owner.Set(w, CollabKey, goyjs.MapValue(map[string]goyjs.Input{
			appID: goyjs.MapValue(map[string]goyjs.Input{
				field: value,
			}),
		}))

		return true, nil
	}

	if collab.Kind() != goyjs.KindMap {
		return false, fmt.Errorf("%w: %s holds a %s",
			ErrCollabShape, CollabKey, collab.Kind())
	}

	apps := collab.Map()

	app, ok := apps.Get(t, appID)
	if !ok {
		apps.Set(w, appID, goyjs.MapValue(map[string]goyjs.Input{
			field: value,
		}))

		return true, nil
	}

	if app.Kind() != goyjs.KindMap {
		return false, fmt.Errorf("%w: %s/%s holds a %s",
			ErrCollabShape, CollabKey, appID, app.Kind())
	}

	fields := app.Map()

	if _, exists := fields.Get(t, field); exists {
		return false, nil
	}

	fields.Set(w, field, value)

	return true, nil
}
