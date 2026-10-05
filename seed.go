package ecollab

import (
	"errors"
	"fmt"

	"github.com/ttab/elephant-api/newsdoc"
	"github.com/ttab/goyjs"
)

// BuildSeedUpdate materialises a Yjs V1 update that hydrates an
// empty Y.Doc with the NewsDoc-shaped tree Materialize reads back:
// a root YMap named rootName (conventionally RootName), content,
// meta and links YArrays of block YMaps, and data YMaps of string
// scalars. CollabKey is not written — it is the authoring
// application's to create.
//
// The same update writes lineage, a ULID the caller mints for this
// seed, under LineageKey in a separate root YMap named
// LineageRootName. The lineage root sits outside rootName, so
// Materialize never sees it; Lineage reads it back. Every call is a
// new CRDT lineage whatever lineage it is given — the items carry a
// fresh client ID — so a lineage must be minted for each seed and
// never reused for another.
//
// The service appends the returned update to a fresh session's
// stream, so every subsequent subscriber replays the same seed and
// sees the structure as if the first collaborator had typed it in.
//
// BuildSeedUpdate makes no network calls; callers supply the
// document. A nil doc, an empty rootName, a rootName equal to
// LineageRootName or an empty lineage is a programmer error.
func BuildSeedUpdate(
	doc *newsdoc.Document, rootName string, lineage string,
) ([]byte, error) {
	if doc == nil {
		return nil, errors.New("nil document")
	}

	if rootName == "" {
		return nil, errors.New("empty root name")
	}

	if rootName == LineageRootName {
		return nil, fmt.Errorf(
			"root name %q is reserved for the lineage", rootName)
	}

	if lineage == "" {
		return nil, errors.New("empty lineage")
	}

	yDoc := goyjs.New()
	defer yDoc.Close()

	// Capture the empty state vector BEFORE writing so EncodeDiffV1
	// later produces an update that covers everything we just wrote.
	emptySV := yDoc.StateVectorV1()

	for k, v := range documentEntries(doc) {
		if err := yDoc.MapSet(rootName, k, v); err != nil {
			return nil, fmt.Errorf("set %s: %w", k, err)
		}
	}

	err := yDoc.MapSet(LineageRootName, LineageKey, goyjs.String(lineage))
	if err != nil {
		return nil, fmt.Errorf("set lineage: %w", err)
	}

	return yDoc.EncodeDiffV1(emptySV), nil
}

// documentEntries renders the Document into the top-level keys the
// tree contract enumerates (scalars plus the three block arrays).
// The map's iteration order is unspecified — yrs's operation
// ordering is deterministic across the resulting update, so any
// traversal order produces a valid (if reorderable-on-the-wire)
// seed.
func documentEntries(d *newsdoc.Document) map[string]goyjs.Input {
	out := map[string]goyjs.Input{
		"uuid":     goyjs.String(d.Uuid),
		"type":     goyjs.String(d.Type),
		"uri":      goyjs.String(d.Uri),
		"url":      goyjs.String(d.Url),
		"title":    goyjs.String(d.Title),
		"language": goyjs.String(d.Language),
		"content":  blockArray(d.Content),
		"meta":     blockArray(d.Meta),
		"links":    blockArray(d.Links),
	}

	return out
}

// blockArray converts a []*Block into a YArray-of-YMaps Input.
func blockArray(blocks []*newsdoc.Block) goyjs.Input {
	items := make([]goyjs.Input, 0, len(blocks))

	for _, b := range blocks {
		if b == nil {
			continue
		}

		items = append(items, blockValue(b))
	}

	return goyjs.ArrayValue(items...)
}

// blockValue renders a single Block as a YMap Input. Scalars map
// straight to strings; data is a YMap of strings; content / meta /
// links recurse into nested block arrays.
func blockValue(b *newsdoc.Block) goyjs.Input {
	entries := map[string]goyjs.Input{
		"id":          goyjs.String(b.Id),
		"uuid":        goyjs.String(b.Uuid),
		"uri":         goyjs.String(b.Uri),
		"url":         goyjs.String(b.Url),
		"type":        goyjs.String(b.Type),
		"title":       goyjs.String(b.Title),
		"rel":         goyjs.String(b.Rel),
		"role":        goyjs.String(b.Role),
		"name":        goyjs.String(b.Name),
		"value":       goyjs.String(b.Value),
		"contenttype": goyjs.String(b.Contenttype),
		"sensitivity": goyjs.String(b.Sensitivity),
		"data":        dataMap(b.Data),
		"content":     blockArray(b.Content),
		"meta":        blockArray(b.Meta),
		"links":       blockArray(b.Links),
	}

	return goyjs.MapValue(entries)
}

// dataMap converts a Block's data map into a YMap-of-strings Input.
// A nil or empty data field still produces an empty YMap so the
// materialisation walk finds the key with the right shape.
func dataMap(d map[string]string) goyjs.Input {
	entries := make(map[string]goyjs.Input, len(d))

	for k, v := range d {
		entries[k] = goyjs.String(v)
	}

	return goyjs.MapValue(entries)
}
