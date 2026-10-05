# ecollab

The data library for the Elephant collaborative editing backend:
everything a program needs to work with a collaborative document
without being the service that hosts it.

It depends on [goyjs][goyjs] and [elephant-api][api], plus
[connect-go][connect] for the generated client the `Collaborate`
client drives, and nothing else. goyjs runs yrs as WebAssembly under
wazero, so this library is pure Go to its consumers: no cgo, no C
toolchain, `go build` cross-compiles it like anything else.

[connect]: https://connectrpc.com/docs/go/getting-started

[goyjs]: https://github.com/ttab/goyjs
[api]: https://github.com/ttab/elephant-api

## Status

Early. The version is `v0.x` and the Go API may still change. The
formats described here cannot change independently of the service that
speaks them, so they move at its pace rather than this library's.

What is here: the tree contract between a Y.Doc and a NewsDoc, the
reach into the application-private state a person's editor works in,
the decoding of the carets an editor publishes into awareness, the
presence schema, the named-document grammar, the protocol vocabulary,
the WebSocket and archive wire codecs, and the `Collaborate` client.
The client is exercised against a running server by the service's own
integration tests.

## The tree contract

A collaborative session for a repository document holds a Y.Doc whose
tree mirrors a `newsdoc.Document`. This package owns that
correspondence in both directions:

```go
update, err := ecollab.BuildSeedUpdate(doc, ecollab.RootName, lineage)
// ... apply update, and every update the session produces after it ...
doc, err := ecollab.Materialize(yDoc, ecollab.RootName)
```

`BuildSeedUpdate` turns a NewsDoc into the Yjs V1 update that seeds a
fresh session. `Materialize` turns a Y.Doc back into a NewsDoc. They
are one package because they are one contract: a change to either is
a change to both, and they are round-trip tested together.

The root YMap is named by `RootName` (`"document"`), and
`CollabKey` (`"_collab"`) names the application-private key the
translation skips. The seed also writes a second root, the lineage,
which is not part of the NewsDoc; see
["The lineage root"](#the-lineage-root).

### Yjs structural conventions for NewsDoc

The Yjs representation of a NewsDoc document uses only three Yjs
primitives:

- **YMap** for `Document` and each `Block`, and for the `data` field
  on a Block.
- **YArray** for `content`, `meta`, and `links` (and the same fields
  nested inside Blocks).
- **String** for every value: scalar fields on Document and Block,
  and values inside `data` maps.

YText, YXmlFragment, YXmlElement, and other rich Yjs types **do not
appear** in the NewsDoc-translatable portion of the tree. Plain
strings carry NewsDoc text content. This keeps the materialization
mechanical, and keeps the Yjs binding's scope minimal — nothing in
this path ever needs to operate on rich Yjs types.

The rich values a person is actually typing into live under
`_collab`, beside this tree rather than in it. Reading and editing
them is a separate job with separate tools; see
["Editing what a person is editing"](#editing-what-a-person-is-editing).

### Translation rules

The translation is purely syntactic. It does not interpret document
content, does not run plugins, and does not extend its understanding
based on document type; the same code handles every document type the
platform supports.

For a Document YMap, copy each NewsDoc-defined field to the
corresponding `newsdoc.Document` field:

- Scalar string fields (`uuid`, `type`, `uri`, `url`, `title`,
  `language`) → corresponding string fields on the proto.
- `content`, `meta`, `links` YArrays → corresponding `repeated Block`
  fields, applying the Block translation rules to each element.

For a Block YMap, the same approach:

- Scalar string fields (`id`, `uuid`, `uri`, `url`, `type`, `title`,
  `rel`, `role`, `name`, `value`, `contenttype`, `sensitivity`) →
  corresponding string fields.
- `data` YMap (string-to-string) → `map<string,string>` field.
- `links`, `content`, `meta` YArrays → corresponding nested `repeated
  Block` fields, recursing.

Keys outside the table are dropped, and a value of the wrong Yjs kind
is skipped rather than fatal: materialization is best-effort over a
contract clients are expected to honour, and one misplaced value must
not cost a session its snapshot.

### The `_collab` key is skipped

Both Document and Block YMaps may carry an additional `_collab` key
whose value is a YMap. The materialization **skips this key
entirely** — it does not appear in the produced `newsdoc.Document`.
This is non-negotiable: `_collab` is not part of the NewsDoc schema,
so leaking it through would cause `Documents.Validate` (and
subsequently `Documents.Update`) to reject the snapshot.

The `_collab` map is keyed by application or component ID (e.g.
`se.ecms.article-editor`), and each value is a YMap whose internal
structure is the application's own concern — typically containing
YText, YXmlFragment, or other rich Yjs types used to support
collaborative editing of values that the application will eventually
commit to "real" NewsDoc fields. Nothing here traverses into
`_collab` and nothing here has an opinion about what is inside.
Frontends are responsible for keeping the NewsDoc-shaped portion of
the tree consistent with whatever collaborative state they are
maintaining inside `_collab`.

This split keeps the collab backend neutral: applications can extend
the data model with whatever supporting state they need, and the
backend stays unchanged. New plugin types, new editor features, new
collaborative widgets — all of these can live entirely inside
`_collab` without requiring backend awareness.

### Lifecycle of `_collab` data

Within an active collaborative session, `_collab` data is part of the
Y.Doc like any other state: it flows through Redis streams, is
archived to S3 with the rest of the session's update log, and is
visible to all subscribers. When a session evicts (graceful or
freeze), the next session for the same doc starts from the
repository's stored NewsDoc, which has no `_collab` content.
Frontends initialize whatever `_collab` state they need on connect.

Two consequences worth being explicit about:

- `_collab` is **session-scoped from the user's perspective**. It
  exists during a session and is gone after. Frontends that need
  persistence for UI state across sessions should write to a
  user-scoped named document (`__user:{sub}:{name}`), not stuff it in
  `_collab`.
- The audit archive in S3 still captures `_collab` updates, since the
  audit log records every update applied to the Y.Doc regardless of
  which keys it touches. Forensics asking "what did the
  article-editor plugin store in `_collab` during this session" can
  be answered by replaying the archive — though no normal use case
  needs this.

### The lineage root

A seed is a new CRDT lineage. `BuildSeedUpdate` writes into a fresh
Y.Doc with a client ID of its own, so two seeds built from the same
NewsDoc carry different item IDs, and an update made against one
cannot be merged into the other without duplicating the document's
structure. Nothing in a Yjs update identifies the lineage it belongs
to, so the lineage is written into the document as content.

`BuildSeedUpdate` takes the lineage, a ULID its caller mints, and
writes it as a string under `LineageKey` (`"id"`) in a root YMap
named `LineageRootName` (`"lineage"`). That root sits beside
`document`, not inside it: `Materialize` walks only the root it is
given, so the lineage never reaches a NewsDoc, and a test holds it to
that.

The lineage changes only when the service seeds a fresh session.
A join replays the session's existing log, and a resume carries the
previous session's state forward, lineage root included, so neither
changes it. A client that keeps a document across a disconnect keeps
its lineage with it, which is what lets the service tell a returning
client of the same history from one whose history is gone.

**The lineage a client persists and declares comes from the server,
not from the document.** The service records each session's lineage
itself, reports it in the `Synced` message that ends every
subscribe, and checks a client's declared lineage against its own
record. A client stores that value beside its local copy and
declares it on its next subscribe. The value in the lineage root is
document content: any writer to the document can overwrite it with
an ordinary update, and nothing in the update says it did — a set on
an existing map key names its neighbour rather than its parent, so
the write cannot be picked out without the document it applies to.
A client that took its lineage from the document would declare
whatever the last writer put there, and every client that did so
would be refused with `lineage_mismatch` on its next resume.

The root is there for a program holding a Y.Doc and no `Synced`
message — reading an archived or exported session, say — to learn
which history the document was seeded as:

```go
lineage, ok := ecollab.Lineage(yDoc)
```

```js
const seededAs = doc.getMap("lineage").get("id")
```

Treat what it returns as a description, not as an identity to
declare.

## Editing what a person is editing

The NewsDoc-shaped tree is a snapshot of committed values. The values
a person has open in an editor are the rich ones under `_collab`, and
a program that means to edit alongside that person edits those. This
is what `Collab` reaches.

```go
collab := ecollab.CollabOn(yDoc.Map(ecollab.RootName))
```

`CollabOn` takes the map that owns the state — a document root YMap,
or a block YMap read out of one — and reads nothing; the maps it
names may all be absent. Under it the layout is three levels deep:

```text
document                 the root YMap, named RootName
  _collab                CollabKey, a YMap keyed by application ID
    se.ecms.editor       one YMap per application
      _hydrated          HydratedKey, the application's markers
      title              one rich value per editable field
      body
```

The accessors follow it: `Map` for the `_collab` map, `App` for one
application's map, `Field` for the value it holds for a field key,
`FieldNode` for the same value as a `goyjs.Node` — a handle that
addresses the value by identity and is what the goyjs write methods
take — and `Hydrated` for the application's own marker. Every read
takes a `goyjs.ReadTxn` and every write a `goyjs.WriteTxn`, so the
reach composes with whatever else is happening in the same
transaction. Nothing here interprets the values; what is under the
application's key is still the application's own concern.

A field is a `Y.XmlText` whose delta is a sequence of embedded
`Y.XmlText` blocks, each carrying its properties as node attributes
and its text runs, with their marks as format attributes, in its own
delta. goyjs reads that whole and writes it back; its README
documents the delta, the node handles and the write scope.

### Offsets are UTF-16 code units

Every index and length that reaches a rich value — a retain, a
delete, an insertion point, a format range — counts UTF-16 code
units, because that is what Yjs counts and what the editor on the
other end derived its own numbers in. An embedded block is one unit
and a character outside the Basic Multilingual Plane is two.
`goyjs.UTF16Len` measures a Go string in them. An index computed from
`len(s)` over a Go string is wrong from the first non-ASCII
character, which in Swedish copy is the first word.

### Editing is cheap, creating is not

Two participants editing one rich value is what Yjs is for, and it
costs nothing: read the field, take its node, write through it.

```go
err := yDoc.Write(func(w *goyjs.WriteTxn) error {
    body, ok := collab.FieldNode(w.ReadTxn(), appID, "body")
    if !ok {
        return errors.New("no body to edit")
    }

    body.ApplyDelta(w, delta)

    return nil
})
if errors.Is(err, goyjs.ErrStaleNode) {
    // The value is gone: read the field again and work on what is there.
}
```

Creating a field is the dangerous half, and the hazard is structural
rather than a bug anyone can fix. Yjs resolves two participants
setting the same map key to one winner; the loser's map is deleted
with everything under it, including text already typed into a rich
value it held. That applies to each of the three levels — two
concurrent creates of `_collab` itself lose one application's whole
state.

`EnsureField` makes the local half safe and says so about the rest:

```go
err := yDoc.Write(func(w *goyjs.WriteTxn) error {
    _, err := collab.EnsureField(w, appID, "body",
        goyjs.XMLTextValue(nil, seed))
    if err != nil {
        return fmt.Errorf("ensure body: %w", err)
    }

    return nil
})
```

The check and the create are one write scope, so no other goroutine
on this document can slip between them, and the subtree is written
whole, so a peer never observes the map without the field. What it
cannot prevent is a concurrent create by a **peer**. So:

- **Edit a field that exists** rather than creating one.
- **Create only what the authoring application has not.** `Hydrated`
  reports whether the application considers the field its own; a
  marked field is one to edit, never one to replace.
- **Where a create is unavoidable, do it once and early**, before
  anyone has typed into the field, so a lost create costs an empty
  value rather than a paragraph.

After a create that lost, the goyjs handles say so rather than
failing silently: a write through the node returns an error matching
`goyjs.ErrStaleNode`, and reading the field reports it absent. The
response to both is to read again and work on what is there.

A value on the path that is not a map — a `_collab` key holding a
string — is refused with `ErrCollabShape` and nothing is recorded in
the scope. It means some participant is writing a different structure
under the same keys, and overwriting it would destroy whatever that
is.

### Reading where the other participants are

An editor built on `@slate-yjs/core` binds `withCursors` to the
`Y.XmlText` it is editing and publishes the caret into awareness as a
pair of relative positions against that value. `CursorReader` decodes
that, so a program can ask of a peer's awareness state which value
they are editing and where in it:

```go
cursors, err := ecollab.NewCursorReader().Cursors(awareness)
if err != nil {
    log.Printf("some carets could not be read: %v", err) // and go on with the rest
}

for _, c := range cursors {
    resolved, err := c.Resolve(yDoc, read)
    if errors.Is(err, goyjs.ErrStaleNode) {
        continue // both values that peer's selection was in are gone
    }
    if err != nil {
        return fmt.Errorf("resolve a caret: %w", err)
    }

    for _, end := range resolved.Ends() {
        if collab.Editing(read, appID, "body", end) {
            // This peer has the caret in the body of this block.
        }
    }
}
```

`Cursors` reads every participant but the local client, ordered by
client ID, and leaves out the ones with no selection — a `null`
selection field is a participant who is present and not in a text. It
returns every caret it could read together with an error joining the
states it could not — one that is not a JSON object, an envelope with
no anchor and focus pair — so one foreign or broken peer costs a
program that peer's caret, not the room: act on the carets, log the
error, which names each client it could not read.

`Resolve` resolves the two ends on their own. A selection can run from
a paragraph into one a peer has since deleted; the end that is still
there is returned, the end that is gone is the zero `goyjs.Resolved`
with `AnchorGone` or `FocusGone` set, and `Ends` is the ends that
resolved, which is what to ask `Editing` about. Both ends gone is
`goyjs.ErrStaleNode`; an end the document has not received yet is
`goyjs.ErrPositionUnseen`, whichever the other end did.

The envelope is the application's choice, so it is the reader's to be
told. `WithCursorField` and `WithCursorDataField` name the two state
fields; they default to `DefaultCursorField` (`selection`) and
`DefaultCursorDataField` (`data`), which are `withCursors`'
`cursorStateField` and `cursorDataField` defaults in `@slate-yjs/core`.
A reader told the wrong name does not fail — a state without the field
is a participant with no selection — so the defaults are held to the
library itself: `cursor_slate_test.go` drives a real `@slate-yjs/core`
editor, at the release pinned in `testdata/node/package-lock.json`,
and reads what it publishes with the default reader. `Cursor.Data` is
whatever the editor publishes beside the caret — a display name, a
colour — left as JSON because its shape is the editor's too. The pair
inside the envelope is `anchor` and `focus`, which is `goyjs.Range`'s
spelling; an editor that wraps its positions some other way is decoded
by unmarshalling the state into your own type and letting
`goyjs.Position` decode each end. The library stops at the position,
and this package is only the common envelope around it.

One detail of the library worth knowing when reading its carets: a
caret at the end of a text run is anchored `AssocBefore`, to the last
unit of the run rather than to what follows, so it resolves to the end
of the run and stays there while a peer appends after it. Everywhere
else the caret is `AssocAfter`, yjs's default.

`Editing` is the question asked per block: does this end of the caret
point into the value that application holds for that field here, or
into anything nested inside it. The nesting is the whole question. A
field is a `Y.XmlText` of embedded paragraph blocks, and a caret
resolves into the innermost type it lies in — the paragraph, or an
inline node inside the paragraph — so the resolved `Node` is never the
field's, and `Editing` asks `goyjs.Value.Holds` of the field's value
rather than `Node.Same` of its node. It is false for a value of
another application, for a field the owner does not have, and for a
position that landed anywhere outside the field.

An editor bound to one field publishes both ends of a selection
against that field, so a selection is inside one value whichever
paragraphs it spans, and either end marks the field as busy. A
program that works below the field — leaving alone the paragraphs a
selection covers rather than the field — finds them as the embeds
between the block holding one end and the block holding the other in
the field's delta; `Editing` does not do that walk.

### Holding a place while you think

An offset is true only for the document it was computed from. A
program that reads a block, spends a second deciding, and then writes
at the offset it found writes in the wrong place if anyone typed
before that point meanwhile — silently, and the more often the longer
it thinks. A `goyjs.Range` names a run by the identity of the text
in it, so it is still the same run afterwards:

```go
// While reading: turn the offset into a range immediately, against
// the document the offset was computed from.
held, err := body.Range(read, offset, goyjs.UTF16Len(phrase))

// Later, in the write scope, against whatever the document has become.
resolved, err := yDoc.ResolveRange(w.ReadTxn(), held)
start, length, ok := resolved.Span()
```

What holding a range does not promise:

- **It is a place, not a lock and not a claim on the words.** What
  lies between the ends may have been rewritten while both ends still
  resolve, so an annotation lands on the place the program chose, not
  necessarily on the text it read. A `length` of 0 means the run was
  deleted and there is nothing left to annotate.
- **The value can be gone.** A block deleted, a field replaced by a
  create that won: resolution reports `goyjs.ErrStaleNode`, which is
  an ordinary outcome of concurrent editing. Read the parent again and
  decide again.
- **`ok == false` from `Span` means the ends are in different
  values.** A range taken with `Node.Range` always has both ends in
  one value; a caret read out of awareness need not, because a person
  can select across blocks.
- **A peer can be ahead.** `goyjs.ErrPositionUnseen` says the position
  names content this document has not received yet; sync and resolve
  again rather than discarding it.
- **Awareness is a snapshot, a range is not.** Knowing which blocks
  the peers were in when you looked says nothing about where they will
  type next — which is exactly why the range, and not the reading of
  awareness, is what makes a deferred write land correctly.

`example_agent_test.go` is all of this in one file: an agent reads the
peers' carets, leaves the block a human is in alone, holds a range
over the run it decided to comment on, and attaches the comment after
a colleague has typed at the start of that block. It is meant to be
read and copied.

## The presence document

`__presence__` is a single service-managed named document carrying
who is subscribed to what, tenant-wide. The collab service is its
only writer; any authenticated caller may subscribe to it read-only.
`ecollab/presence` owns its schema:

```go
entries := presence.Read(yDoc)          // by doc id, then subscription id
who := presence.ReadDoc(yDoc, docID)    // one document's participants
```

- The document id is `presence.DocID` (`__presence__`).
- The state lives under the YMap root `presence.RootName`
  (`by_doc`) — deliberately not the `RootName` a repository document
  uses.
- The root is keyed by **doc id**. Each value is a nested YMap keyed
  by **subscription id**, and each of those is a flat YMap carrying
  one `presence.Entry`: `subject`, `joined_at` (RFC3339Nano, UTC),
  `doc_kind`, and `identity` (the JSON form of `presence.Identity`).

Keying the outer map by doc id is what makes the document usable as
a live index: a client rendering a document list binds reactively to
the value at one doc id and picks up that document's presence
changes without walking every participant in the tenant.

Reading is best-effort, as materialisation is: a field of the wrong
Yjs kind, an unparseable `joined_at` or an `identity` that is not the
JSON this package writes yields the zero value for that field. One
malformed entry does not cost a reader the document.

`Entry.Input()` is the other direction, and the service writes
through it — both on a join and when the presence reconciler rebuilds
an entry a lost write dropped, so a repaired entry reads exactly as
the join would have written it.

### The schema is a contract

Presence used to be an implementation detail of the service's
subscribe orchestrator. It is not one any more: with a reader here,
**changing an entry field is a change to this package first**, then
to the service, and only a version of each that agree will round-trip
an entry. Adding a field means adding it to `Entry` and to both its
encoder and its reader in one commit; renaming or removing one is a
breaking change to every consumer reading the document.

Two things are deliberately *not* here, because they are the
service's and not the schema's: which doc kinds write presence at all
(the service's `sub.DocKindWritesPresence` is the single answer), and
every mutation — replaying the presence stream, rewriting a per-doc
inner map whole, pruning against the subscription table. Presence
mutations replay the whole stream, so the service batches them; a
reader never needs to know that.

## Named documents

A named document is a Yjs document the collab service hosts itself,
not a mirror of a repository document, and it is addressed by a
reserved `__`-prefixed id rather than a UUID. `ecollab/named` is the
grammar:

```go
doc, err := named.Classify(docID)        // kind, and the parsed parts
id := named.FormatUserDocID(sub, "bookmarks")
state, err := named.WalkToJSON(yDoc, doc.RootName())
```

Two kinds exist:

- **`__presence__`** — the service-managed presence document above.
  `named.KindService`, any authenticated caller may subscribe,
  read-only, and its state lives under `presence.RootName`.
- **`__user:{url-encoded sub}:{name}`** — one person's own
  observable state: bookmarks, recents, UI preferences.
  `named.KindUser`, read-write to the owner and to nobody else, under
  the conventional `ecollab.RootName`.

Anything else `__`-prefixed is `ErrUnknownNamedDoc`, and a
recognized kind that will not parse is `ErrMalformedNamedDoc`: the
`__` prefix is reserved, so a client cannot conjure a named document
by picking a new name for one. An id without the prefix classifies
as `named.KindRepository` and is not parsed further, which is what
lets a caller run `Classify` on every id it handles and branch on
the kind.

`FormatUserDocID` is the only writer of the user-scoped form. The
owner's subject is URL-encoded, so a hand-built id that encodes it
differently is a different document — the round trip through
`Classify` is the definition.

`Doc.RootName()` answers which YMap root the document's state lives
under, and going through it is what keeps a writer, a reader and the
service's inspection on the same root.

### Allowed Yjs types in named documents

Named documents are *conventionally* restricted to **YMap, YArray,
and string** — the same type set as the NewsDoc-translatable portion
of repository docs. The use cases for named docs are observable state
(presence, bookmarks, recents, UI preferences), where rich
collaborative-text types aren't needed. If a future use case
genuinely requires collaborative text editing in a named-doc context,
it should go through the normal repository-doc + `_collab`
mechanism, not extend the named-doc type set.

This is a *convention*, not an enforced contract. The transport,
persistence, and convergence layers are all type-agnostic: the
service appends opaque update bytes, the snapshotter re-encodes via
`EncodeStateV2`, and clients apply opaque updates — none inspect Yjs
types. The only place a named doc's content is walked by type is
`WalkToJSON`, which the service's `InspectNamedDocument` serves from.
Rather than enforce the convention on the hot write path (which would
add a rebuild to every write purely to guard a support endpoint), the
walk renders any rich type that does land in a named doc
**best-effort**: YText/YXmlText become their plain string,
YXmlElement its serialized XML, binary base64, and the
non-stringable shared types (fragment, subdoc, weak) plus any unknown
kind a `{"_yjs": "..."}` marker. Inspection therefore never fails on
unexpected content.

## The protocol vocabulary

The strings a participant and the service exchange about a session,
as opposed to the document's contents. They are here because a
client has to act on them, and acting on them means agreeing with
the service about what each one is.

### Encoding tags

`ecollab.Encoding` tags what one entry in a document's update log
carries. The tag travels with the entry wherever the entry goes — a
live subscription, the S3 archive, and `SessionUpdateRecord.encoding`
on the archive-reading RPCs — so a program replaying a session sees
the same vocabulary the live pipeline used:

| Tag | Carries |
| --- | --- |
| `v1`, `v2` | Yjs document updates. Apply in stream order. |
| `v1-seed`, `v2-seed` | Yjs updates the emitter called structural seeding rather than authorship. Apply them exactly as `v1` and `v2` — the tag is for attribution, not for filtering. |
| `v1-resync` | A Yjs v1 update that arrived in a client's sync step 2 while its subscription was opening: edits the client made while it was away. Byte-identical to `v1`, applied and attributed like it. The service stamps it from where the update arrived; a client never claims it. |
| `aw` | An awareness update. Not document state; never apply it to a Y.Doc. |
| `evict` | The session ended. A live subscriber sees it as a `Close` with reason `session_terminated`. |
| `stateless` | A server-issued lifecycle event, `{"event": ..., "data": ...}`. |
| `step2` | The catch-up diff for one subscriber. Addressed to a subscriber rather than to the document, so it is never persisted and never appears in the archive. |

The set is closed and the values are durable: a record written years
ago still carries one of these strings, so a value can be added but
never repurposed.

### Subscription mode

`ecollab.SubscriptionMode` is the read/write capability the service
granted a subscription, decided once when it opens and carried on
the `Synced` message that ends initial state transfer. Believe it:
an update sent on a `read_only` subscription is refused and closes
that subscription rather than being ignored.

### Close reasons

A subscription the server closes on its own carries one of
`ecollab.CloseReason*` as the reason on the `Close` message, and a
connection it refuses carries one on the terminal error — in the
`reason` metadata on the Connect stream, or in the close frame on
the WebSocket transport. The reason, not the code, is what says what
to do about it: `no_active_session` means read the repository
version instead, `session_terminated` means subscribe again for a
fresh session, `token_expired` means re-authorize, `rate_limited`
means coalesce rather than reconnect, and `lineage_mismatch` means the
client's copy belongs to a history the session no longer has — keep
it, recover what is worth keeping, and subscribe again from empty.
`lineage_mismatch` is the one reason whose message is structured: it
is the session's current lineage, bare — or empty when no session was
open and the subscribe would have seeded a lineage the copy cannot
belong to.

The constants are untyped, so they compare directly against the
wire's plain string.

### State vectors

`ecollab.StateVector` decodes the lib0 state vector goyjs produces
and the service echoes back in the `server_state_vector` metadata
when it refuses a snapshot as stale. `Dominates` answers the
question that refusal was about — has this participant seen
everything the server had — and `FirstShortfall` names a client it
is behind on. `DecodeStateVector` refuses a vector with more than
`MaxStateVectorEntries` entries rather than honouring a
caller-controlled allocation hint.

`ecollab/lib0` is the varint and varstring layer underneath, shared
with the WebSocket envelope and the archive's chunk records below.

## The Collaborate client

`ecollab.Client` is a live editing session from the outside: one
bidirectional stream carrying any number of document subscriptions,
each folding the session's updates into a Y.Doc of its own.

```go
c, err := ecollab.Dial(ctx, endpoint, ecollab.HTTP2Client(),
	ecollab.WithTokenSource(tokens))
if err != nil {
	return err
}
defer c.Close()

sub, err := c.Subscribe(ctx, docID, ecollab.Observer())
if err != nil {
	return err
}

doc, err := sub.NewsDoc() // Materialize, on demand
```

`Subscribe` returns once the server has finished initial state
transfer, so the document is readable with no further waiting: the
Step 2 diff against the state vector the subscribe carried has been
applied, and the granted `Mode` and the session's `Lineage` are
known. The transfer runs both ways — see
["Working offline"](#working-offline). `Update` mutates the local
document and forwards what the mutation produced; `Seed` is the same
thing tagged as structural seeding rather than authorship. `Read`
and `NewsDoc` are how the document is read — the stream's writer is
holding the same lock, so the Y.Doc itself never escapes.

### Working offline

A client that keeps a document across a disconnect — an agent that
works through a network blip, or one that is only connected some of
the time — persists two things: the document's state, and the
lineage `Subscription.Lineage` reported for it. Coming back, it
restores the state into a Y.Doc and subscribes with both:

```go
doc := goyjs.New()
err := doc.ApplyUpdateV2(saved.State)
// ...
sub, err := c.Subscribe(ctx, docID,
	ecollab.WithDoc(doc), ecollab.WithLineage(saved.Lineage))
```

The handshake delivers both backlogs. The server sends a Step 2 with
what changed while the client was away, then a Step 1 with its own
state vector; the client answers that with a Step 2 carrying what the
session lacks — the offline edits — and `Synced` ends it. The server
sends `Synced` without waiting for the client's Step 2, so when the
client did send one, `Subscribe` runs the handshake a second time
before it returns: the server handles a connection's messages in
order, and the second Step 1 shows whether the edits were taken.

So the server's backlog is in the document when `Subscribe` returns,
and the client's usually is in the session — but not always, and
**`WaitDelivered` is what says so.** It returns nil once nothing is
owed, waits while a publish soft-stop holds the edits back, and
returns `ErrResyncDropped` if the server dropped them without a
refusal. A client that means to discard its persisted copy, or to
`Close`, after resuming waits on it first; until then, keep the copy.

An answer with nothing the session lacks is not sent. That is not
the same as an empty diff: a Yjs diff carries the sender's whole
delete set whatever state vector it is computed to, so the client
sends one only when it has items the server lacks, or deletions
beyond those the server's Step 2 showed it already holds. A
read-only subscription is sent no Step 1 and answers nothing.
`ExampleClient_Subscribe_resume` is the whole shape.

The lineage persisted is always the one `Synced` reported, never the
value in the document's lineage root; see
["The lineage root"](#the-lineage-root) for why. `Resync` declares
the subscription's current lineage unless told otherwise.

Three things can stop the edits from going through:

- **A publish soft-stop.** The server refuses an update while a
  publish is in progress, the client's Step 2 included. A Step 1 that
  arrives during a soft-stop is not answered, a `publish_in_progress`
  that arrives between a Step 2 and the Step 1 confirming it is taken
  as its refusal, and either way the subscription re-handshakes on
  `publish_cleared` and sends the Step 2 then. `Subscribe` has
  returned by that point, and `WaitDelivered` is what returns when
  the Step 2 has gone through. The local document is left as it is
  meanwhile. If the refused Step 2 had in fact landed, the
  re-handshake finds nothing to send. A confirmed Step 2 is done
  with: later publishes do not send it again.
- **A lineage mismatch.** The session the copy belonged to is gone
  and the document was seeded afresh, so the copy's items cannot be
  merged without duplicating its structure. The server refuses the
  subscribe before it sends anything, and the client returns a
  `*LineageMismatchError` carrying the lineage it declared and the
  session's current one (empty when no session was open: the copy is
  refused for the lineage the subscribe would have seeded). The copy
  is untouched — the caller's own
  document, or `Read` after `Done` when the refusal came on a
  `Resync` — so recover what is worth keeping from it, into a sketch
  for a person to copy across, and subscribe again from an empty
  document.
- **Too much to send.** A Step 2 is capped at `MaxSyncStep2Bytes`
  (1 MiB), and the server refuses a larger one by ending the whole
  connection, again after every reconnect. The client does not send
  it: it gives up the subscription with a `*ResyncTooLargeError` and
  leaves the copy alone — the server's catch-up is held back from it
  until the client has answered, so it is not merged either.
  Recovery is the same as for a mismatch. Subscribing again straight
  away is safe: the client has already told the server to close the
  subscription it gave up, and the new `Subscribe` waits until the
  server has finished answering the old one.

### It needs HTTP/2, and that is easy to get wrong

connect-go answers a bidirectional stream that arrived over HTTP/1.1
with a bare `505 HTTP Version Not Supported`, before any handler
runs, and Go only negotiates HTTP/2 through the TLS ALPN handshake.
A client talking to a plain-HTTP endpoint — a pod on the cluster
network, a local instance, a test server — therefore gets HTTP/1.1
and a 505 unless it asks for unencrypted HTTP/2 explicitly:

```go
var t http.Transport

t.Protocols = new(http.Protocols)
t.Protocols.SetHTTP2(true)
t.Protocols.SetUnencryptedHTTP2(true)
```

`ecollab.HTTP2Transport` and `ecollab.HTTP2Client` are exactly that,
and they leave HTTP/1.1 out rather than keeping it as a fallback:
falling back is what produces the 505, so a transport that cannot
reach the stream should fail at the connection instead. The same
requirement reaches the network in front of the pod — an ingress
that does not forward HTTP/2 makes the transport unusable, and
browsers cannot use it at all, which is why the WebSocket exists.

### Authorization

With a `TokenSource` the client owns the connection's authorization:
the stream opens with the source's current token, and a fresh one is
sent as an `auth_refresh` before the current one expires, which is
what keeps a long session from dropping mid-edit. The refreshed
token must be for the same subject — the service counts a refresh
for a different one and drops it. Without a source the HTTP client
carries the bearer and the connection ends when it expires;
`Client.Refresh` is then the caller's to call.

### Errors carry the reason

Two shapes, and `ecollab.Reason` reads both:

- `*CloseError` is the server closing **one** subscription, leaving
  the stream and its other documents alone. It is what a refused
  `Subscribe` returns, and what `Subscription.Err` holds after the
  session was frozen or evicted. A `lineage_mismatch` close arrives
  as a `*LineageMismatchError`, which carries both lineages and
  unwraps to the `*CloseError`.
- `*StreamError` is how the stream itself ended: a connection-wide
  refusal is the stream's status rather than a message on it,
  because a Connect stream has one. `Code` is shared between
  reasons — `rate_limited` and `subscription_limit` are both
  `resource_exhausted` — so `Reason` is what to branch on.

A clean half-close is not an error: `Close` and `Err` report `nil`.

### Freeze-snapshotting what you see

`Client.LastServerPing` is the service's most recent time witness,
kept verbatim. Echo it as `SnapshotRequest.client_last_server_ping`
and the service can tell whether this client had seen everything it
had; reformatting the string is how a client fails that check, so it
travels as it arrived. The state vector half of the same check is
`Subscription.StateVector`.

## The wire codecs

Two byte formats the service speaks that are not the document tree
and not the vocabulary. Each is the specification of a format a
program outside the service has to read or write, which is why they
are here rather than in it.

### The WebSocket envelope

`ecollab/envelope` is the multiplexed frame format a browser client
speaks over one WebSocket:

```
varstring doc           // document name; the multiplexing key
varuint   message_type
bytes     payload       // type-specific
```

The doc name is what lets one socket carry any number of documents,
and the layout follows Hocuspocus / y-protocols so an existing
in-browser Yjs client interoperates unchanged. The package encodes
and decodes every frame type in both directions — sync step 1 and 2,
updates and seed-tagged updates, awareness, the `Synced` handshake,
`Close`, stateless events, the server ping, the auth refresh and the
subscribe options — so the protocol is what round-trips through it.

The offline shape rides in the same frames. The server sends a sync
step 1 after its step 2, and the client answers it with a step 2 of
its own, as the stream does. The `Synced` frame's payload is the mode,
the session's lineage and the server's state vector
(`SyncedPayload`; `DecodeSynced` still reads the mode alone, and a
frame from an older server ends after it), and the subscribe-options
frame carries the lineage a client declares under the `lineage` key.

Server-side clients do not need it: the `Collaborate` bidirectional
stream carries the same messages as protobuf. The envelope is the
browser half of that pair, and the service converts between the two
in one file.

### The archive record codec

`ecollab/archive` decodes a session's archived updates straight out
of object storage, for an audit or forensic reader that would
otherwise go through the service's RPCs:

```go
records, err := archive.DecodeRecords(chunkBody)
```

A chunk body is the concatenation of records, one per archived stream
entry, carrying the Redis id, the receive timestamp, the originating
subscription, the `ecollab.Encoding` tag and the payload. Chunk
numbers increase monotonically per session, so a session reads in
order by walking the chunks in name order. A truncated body yields
the records that did decode plus `lib0.ErrTruncated`, so a chunk cut
short costs a reader the tail rather than the object.

The format is stable and a record is the only part of the archive
that is a contract: the prefix layout, the `manifest.json` and
`closed.json` markers, the purge tombstone and the drain policy that
decides when a chunk is written are the service's own.

## Development

```sh
make build       # go build -o /dev/null ./...
make vet
make lint        # golangci-lint run
make node-deps   # installs the @slate-yjs/core caret helper under testdata/node
make test
```

`cursor_slate_test.go` needs `node` (20 or later) and the helper's
dependencies, installed at the versions in the committed
`testdata/node/package-lock.json` — `@slate-yjs/core`, `slate`, `yjs`
and `y-protocols`. Moving them is a deliberate change to what the
reader is held to, not an install-time resolution. Without them those
tests skip with a hint and everything else runs; build, vet and lint
never touch node.
