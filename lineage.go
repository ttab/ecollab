package ecollab

import (
	"github.com/ttab/goyjs"
)

// LineageRootName is the root YMap the lineage of a session's Y.Doc
// lives under. It is a root of its own, beside RootName rather than
// inside it, so Materialize never walks it and the lineage never
// reaches a NewsDoc.
//
// A browser can read it with
//
//	doc.getMap("lineage").get("id")
//
// but a client must not persist or declare what it reads there; see
// Lineage for why, and for where the lineage to declare comes from.
const LineageRootName = "lineage"

// LineageKey is the key under LineageRootName holding the lineage, a
// ULID string.
const LineageKey = "id"

// Lineage returns the lineage a Y.Doc was seeded with: the string
// BuildSeedUpdate wrote under LineageRootName and LineageKey. It
// reports false when the document has no lineage, or holds something
// other than a string where it should be.
//
// A lineage names one CRDT history. Two seeds built from the same
// NewsDoc are different lineages — their items have different IDs —
// and an update made against one cannot be merged into the other
// without duplicating the document's structure. Nothing else in a
// Yjs document identifies its lineage on the wire, so the lineage is
// content: the service mints one when it seeds a fresh session, and
// it changes only then, never on a join or a resume.
//
// The value is what the document says, and any writer to the
// document can overwrite it with an ordinary update — one the
// service cannot pick out from the update alone, since a set on an
// existing map key names its neighbouring item rather than its
// parent. So the value describes which history a document was
// seeded as; it is not an identity to act on. The service keeps its
// own record of each session's lineage and checks against that. A
// client takes the lineage it persists and declares on subscribe
// from the Synced message, where the service reports that record,
// never from this: a client that declared the document's value
// would declare whatever the last writer put there, and be refused
// with CloseReasonLineageMismatch on its next resume.
//
// Lineage is for a program holding a Y.Doc and no Synced message,
// such as one reading an archived session.
func Lineage(doc *goyjs.Doc) (string, bool) {
	if doc == nil {
		return "", false
	}

	txn := doc.NewReadTxn()
	defer txn.Commit()

	v, ok := doc.Map(LineageRootName).Get(txn, LineageKey)
	if !ok || v.Kind() != goyjs.KindString {
		return "", false
	}

	return v.String(), true
}
