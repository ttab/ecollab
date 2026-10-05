// Package ecollab is the data library for the elephant collaborative
// editing service: everything a program needs to work with a
// collaborative document without being the service.
//
// The tree contract lives here, in both directions. BuildSeedUpdate
// turns a NewsDoc into the Yjs update that seeds a fresh session,
// and Materialize turns the resulting Y.Doc back into a NewsDoc.
// They are one package because they are one contract — a change to
// either is a change to both, and they are round-trip tested
// together.
//
// The seed also carries its lineage, in a root of its own beside the
// document root, where Materialize never looks and Lineage reads it
// back.
//
// The rules the two implement are written down in the README, under
// "The tree contract". The service's design document points at it
// rather than restating it.
package ecollab

// RootName is the YMap root a repository document's tree lives
// under. The name is a convention shared by every participant in a
// session: the service seeds it, clients write into it, and
// Materialize walks it.
const RootName = "document"

// CollabKey is the YMap key holding application-private state, on
// the document map and on every block map. Its contents are the
// authoring application's own concern, and Materialize skips it
// entirely — leaking it into a NewsDoc would fail repository
// validation.
const CollabKey = "_collab"
