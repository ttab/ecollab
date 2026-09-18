# ecollab

The data library for [elephant-collab][collab], the collaborative
editing service: everything a program needs to work with a
collaborative document without being the service.

[collab]: https://github.com/ttab/elephant-collab

It depends on [goyjs][goyjs] and [elephant-api][api] and nothing
else. goyjs runs yrs as WebAssembly under wazero, so this library is
pure Go to its consumers: no cgo, no C toolchain, `go build`
cross-compiles it like anything else.

[goyjs]: https://github.com/ttab/goyjs
[api]: https://github.com/ttab/elephant-api

## Status

Spike-quality, extracted from the service it names, and still
growing: the tree contract, the presence schema, the named-document
grammar, the protocol vocabulary and the two wire codecs are here,
and the `Collaborate` client follows. A `v0.x` tag is honest about
the surface still moving.

## The tree contract

A collaborative session for a repository document holds a Y.Doc whose
tree mirrors a `newsdoc.Document`. This package owns that
correspondence in both directions:

```go
update, err := ecollab.BuildSeedUpdate(doc, ecollab.RootName)
// ... apply update, and every update the session produces after it ...
doc, err := ecollab.Materialize(yDoc, ecollab.RootName)
```

`BuildSeedUpdate` turns a NewsDoc into the Yjs V1 update that seeds a
fresh session. `Materialize` turns a Y.Doc back into a NewsDoc. They
are one package because they are one contract: a change to either is
a change to both, and they are round-trip tested together.

The root YMap is named by `RootName` (`"document"`), and
`CollabKey` (`"_collab"`) names the application-private key the
translation skips.

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
means coalesce rather than reconnect.

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
make build   # go build -o /dev/null ./...
make vet
make lint    # golangci-lint run
make test
```

`goyjs` and `elephant-api` are pointed at working copies with
`replace` directives while their contracts are in flight. Both go
before the first tag.
