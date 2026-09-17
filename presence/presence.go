// Package presence describes the `__presence__` document: the
// service-managed named document that carries who is subscribed to
// what, tenant-wide.
//
// The collab service is the document's only writer, so what lives
// here is the schema rather than the mechanism. The constants name
// the document and its root, Entry is one participant entry in both
// directions, Identity is the auth-layer descriptor an entry
// carries, and Read walks a materialised presence doc back into
// entries. Replaying the stream, rewriting a per-doc inner map and
// reconciling against the subscription table stay in the service.
//
// The schema is a contract between the writer and every reader, so
// changing a field is a change to this package first. The README
// says the same thing at more length, under "The presence
// document".
package presence

import (
	"encoding/json"
	"time"

	"github.com/ttab/goyjs"
)

// DocID is the doc id of the single tenant-wide presence document.
// It is a service-managed named document: any authenticated caller
// may subscribe, read-only.
const DocID = "__presence__"

// RootName is the YMap root the presence document's state lives
// under — deliberately not the RootName a repository document uses.
// The outer map is keyed by doc id and each value is a nested YMap
// keyed by subscription id, so a client rendering a document list
// binds reactively to one doc id's value and picks up that doc's
// presence changes without walking the whole participant universe.
const RootName = "by_doc"

// The keys of one participant entry. Unexported because the entry is
// reached through Entry in both directions; a reader that wants the
// raw field names is reading the wrong layer.
const (
	fieldSubject  = "subject"
	fieldJoinedAt = "joined_at"
	fieldDocKind  = "doc_kind"
	fieldIdentity = "identity"
)

// Identity is the auth-layer-supplied descriptor of who a subscriber
// is. It travels in a participant entry, and the service also
// persists the JSON form on its subscription and audit rows.
//
// All fields are optional. A transport populates Subject from the
// validated JWT subject; downstream auth integrations fill in the
// rest as they become available. A zero Identity encodes as `{}` so
// a column or an entry field always holds a well-formed object —
// easier to query than mixing object and null.
//
// Identity is broadcast to every authenticated reader of the
// presence document, which is why Custom must not carry PII. The
// service's design document spells the policy out under "Identity
// visibility and the tenancy boundary".
type Identity struct {
	// Subject is the canonical JWT subject URI.
	Subject string `json:"sub,omitempty"`

	// Units lists the tenant-unit memberships of the caller.
	Units []string `json:"units,omitempty"`

	// Org is the caller's tenant org URI.
	Org string `json:"org,omitempty"`

	// Name is the caller's human display name (best-effort; the auth
	// layer may not populate it).
	Name string `json:"name,omitempty"`

	// Custom carries IdP-specific key/value pairs the auth layer
	// surfaces. Kept as a typed map so the surface does not leak
	// `any` / `json.RawMessage`.
	Custom map[string]string `json:"custom,omitempty"`
}

// JSONBytes returns the JSON encoding an entry's identity field
// carries, and that the service's identity JSONB columns store. A
// marshalling error (impossible for the current shape — every field
// marshals cleanly) falls back to `{}` so the value is always a
// valid object.
func (i Identity) JSONBytes() []byte {
	b, err := json.Marshal(i)
	if err != nil {
		return []byte("{}")
	}

	return b
}

// Entry is one participant entry: the flat YMap stored under a
// subscription id in the presence document's per-doc inner map.
//
// Every field encodes as a string, because the presence document
// holds only the three Yjs primitives a named document is restricted
// to. JoinedAt is RFC3339Nano in UTC and Identity is its JSON form.
type Entry struct {
	// Subject is the JWT subject of the participant.
	Subject string

	// JoinedAt is when the join was published — not when the
	// subscription row was written. A repaired entry carries the
	// row's creation instant instead, which is the one field a
	// repair cannot reproduce exactly.
	JoinedAt time.Time

	// DocKind is the doc-class tag of the subscription, as the
	// service spells it: "repository" or "sketch" today, since those
	// are the kinds that participate in presence. The vocabulary and
	// the answer to whether a kind writes presence at all are the
	// service's, not this package's.
	DocKind string

	// Identity is the participant's auth-layer descriptor.
	Identity Identity
}

// Input encodes the entry for writing into the presence document.
//
// Everything that writes an entry goes through here: the subscribe
// path that publishes a join and the reconciler that rebuilds an
// entry a lost write dropped. A repaired entry has to read exactly
// as the join would have written it, and one encoder is what makes
// that true.
func (e Entry) Input() goyjs.Input {
	return goyjs.MapValue(map[string]goyjs.Input{
		fieldSubject:  goyjs.String(e.Subject),
		fieldJoinedAt: goyjs.String(e.JoinedAt.UTC().Format(time.RFC3339Nano)),
		fieldDocKind:  goyjs.String(e.DocKind),
		fieldIdentity: goyjs.String(string(e.Identity.JSONBytes())),
	})
}

// Read returns every participant entry in doc, keyed by doc id and
// then by subscription id. The doc must already be materialised —
// how the caller got there, a stream replay or a snapshot, is none
// of this package's business.
//
// Reading is best-effort in the same way the tree contract's
// materialisation is: a field of the wrong Yjs kind, an unparseable
// joined_at or an identity that is not the JSON this package writes
// yields the zero value for that field rather than an error. One
// malformed entry must not cost a reader the rest of the document.
//
// A doc id carrying no entries is omitted rather than returned with
// an empty map: the writer deletes a per-doc map as it empties, so
// "present with nobody in it" is not a state the schema has.
func Read(doc *goyjs.Doc) map[string]map[string]Entry {
	out := map[string]map[string]Entry{}

	if doc == nil {
		return out
	}

	outer := doc.Map(RootName)

	txn := doc.NewReadTxn()
	defer txn.Commit()

	outer.ForEach(txn, func(docID string, value goyjs.Value) bool {
		participants := entriesOf(txn, value)
		if len(participants) > 0 {
			out[docID] = participants
		}

		return true
	})

	return out
}

// ReadDoc returns the participant entries for a single doc id, keyed
// by subscription id. Empty when the document carries none.
func ReadDoc(doc *goyjs.Doc, docID string) map[string]Entry {
	if doc == nil {
		return map[string]Entry{}
	}

	outer := doc.Map(RootName)

	txn := doc.NewReadTxn()
	defer txn.Commit()

	value, ok := outer.Get(txn, docID)
	if !ok {
		return map[string]Entry{}
	}

	return entriesOf(txn, value)
}

// entriesOf decodes one per-doc inner map into entries.
func entriesOf(txn *goyjs.ReadTxn, value goyjs.Value) map[string]Entry {
	out := map[string]Entry{}

	inner := value.Map()
	if inner == nil {
		return out
	}

	inner.ForEach(txn, func(subscriptionID string, entry goyjs.Value) bool {
		out[subscriptionID] = entryOf(txn, entry)

		return true
	})

	return out
}

// entryOf decodes one participant entry.
func entryOf(txn *goyjs.ReadTxn, value goyjs.Value) Entry {
	var e Entry

	fields := value.Map()
	if fields == nil {
		return e
	}

	fields.ForEach(txn, func(key string, field goyjs.Value) bool {
		if field.Kind() != goyjs.KindString {
			return true
		}

		switch key {
		case fieldSubject:
			e.Subject = field.String()
		case fieldJoinedAt:
			if t, err := time.Parse(time.RFC3339Nano, field.String()); err == nil {
				e.JoinedAt = t
			}
		case fieldDocKind:
			e.DocKind = field.String()
		case fieldIdentity:
			// A stored identity that will not decode leaves the
			// field zero: a participant without a descriptor beats
			// dropping the participant.
			_ = json.Unmarshal([]byte(field.String()), &e.Identity)
		default:
			// An entry field this version does not know about is
			// ignored. The writer and the reader are versioned
			// together, so this is forward compatibility for a
			// reader running behind a newer service.
		}

		return true
	})

	return e
}
