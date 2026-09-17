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
growing: the tree contract is here, and presence, the named-document
grammar, the protocol vocabulary and the `Collaborate` client follow.
A `v0.x` tag is honest about the surface still moving.

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

## Allowed Yjs types in named documents

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
types. The only place a named doc's content is walked by type is the
service's `InspectNamedDocument`. Rather than enforce the convention
on the hot write path (which would add a rebuild to every write
purely to guard a support endpoint), the walk renders any rich type
that does land in a named doc **best-effort**: YText/YXmlText become
their plain string, YXmlElement its serialized XML, binary base64,
and the non-stringable shared types (fragment, subdoc, weak) plus any
unknown kind a `{"_yjs": "..."}` marker. Inspection therefore never
fails on unexpected content.

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
