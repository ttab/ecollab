package ecollab

import (
	"errors"
	"fmt"

	"github.com/ttab/elephant-api/newsdoc"
	"github.com/ttab/goyjs"
)

// Materialize walks doc's root YMap (named rootName, conventionally
// RootName) and produces a *newsdoc.Document mirroring the live
// Y.Doc state.
//
// The translation is purely syntactic: every key in the document or
// block YMap maps onto a same-named field on the proto, every item
// in the content, meta and links arrays is recursively translated as
// a block, and data YMaps become map<string,string> fields. The
// CollabKey key is skipped on both document and block maps.
//
// It returns an error if the root doesn't exist. Field-level type
// mismatches are silently skipped: materialization is best-effort
// over a contract clients are expected to honour, and a single
// misplaced value must not cost a session its snapshot.
//
// Materialize is a pure read — it opens a ReadTxn and walks the
// tree, writing nothing.
func Materialize(doc *goyjs.Doc, rootName string) (*newsdoc.Document, error) {
	if doc == nil {
		return nil, errors.New("nil Y.Doc")
	}

	root := doc.Map(rootName)
	if root == nil {
		return nil, fmt.Errorf("root map %q not found", rootName)
	}

	txn := doc.NewReadTxn()
	defer txn.Commit()

	out := &newsdoc.Document{}

	root.ForEach(txn, func(key string, v goyjs.Value) bool {
		if key == CollabKey {
			return true
		}

		switch key {
		case "uuid":
			out.Uuid = stringOf(v)
		case "type":
			out.Type = stringOf(v)
		case "uri":
			out.Uri = stringOf(v)
		case "url":
			out.Url = stringOf(v)
		case "title":
			out.Title = stringOf(v)
		case "language":
			out.Language = stringOf(v)
		case "content":
			out.Content = arrayOfBlocks(v, txn)
		case "meta":
			out.Meta = arrayOfBlocks(v, txn)
		case "links":
			out.Links = arrayOfBlocks(v, txn)
		default:
			// Unknown keys are ignored; the materialization is a
			// fixed translation table, not a passthrough of
			// arbitrary client state.
		}

		return true
	})

	return out, nil
}

// arrayOfBlocks decodes a YArray-of-YMap-blocks into a []*Block.
// Non-array inputs and non-map elements produce nil entries that
// are filtered out.
func arrayOfBlocks(v goyjs.Value, txn *goyjs.ReadTxn) []*newsdoc.Block {
	if v.Kind() != goyjs.KindArray {
		return nil
	}

	arr := v.Array()
	if arr == nil {
		return nil
	}

	out := make([]*newsdoc.Block, 0, arr.Len())
	arr.ForEach(txn, func(_ int, item goyjs.Value) bool {
		if item.Kind() == goyjs.KindMap {
			out = append(out, blockOf(item.Map(), txn))
		}

		return true
	})

	return out
}

// blockOf walks a YMap representing a Block and copies its keys
// into a *Block. Recurses for content / meta / links nested arrays
// and decodes the `data` YMap of strings into the proto's Data map.
func blockOf(m *goyjs.Map, txn *goyjs.ReadTxn) *newsdoc.Block {
	b := &newsdoc.Block{}
	if m == nil {
		return b
	}

	m.ForEach(txn, func(key string, v goyjs.Value) bool {
		if key == CollabKey {
			return true
		}

		switch key {
		case "id":
			b.Id = stringOf(v)
		case "uuid":
			b.Uuid = stringOf(v)
		case "uri":
			b.Uri = stringOf(v)
		case "url":
			b.Url = stringOf(v)
		case "type":
			b.Type = stringOf(v)
		case "title":
			b.Title = stringOf(v)
		case "rel":
			b.Rel = stringOf(v)
		case "role":
			b.Role = stringOf(v)
		case "name":
			b.Name = stringOf(v)
		case "value":
			b.Value = stringOf(v)
		case "contenttype":
			b.Contenttype = stringOf(v)
		case "sensitivity":
			b.Sensitivity = stringOf(v)
		case "data":
			b.Data = dataMapOf(v, txn)
		case "content":
			b.Content = arrayOfBlocks(v, txn)
		case "meta":
			b.Meta = arrayOfBlocks(v, txn)
		case "links":
			b.Links = arrayOfBlocks(v, txn)
		}

		return true
	})

	return b
}

// dataMapOf decodes the `data` YMap-of-strings into a Go map. Non-
// string values are silently skipped; the contract pins data to
// string-only.
func dataMapOf(v goyjs.Value, txn *goyjs.ReadTxn) map[string]string {
	if v.Kind() != goyjs.KindMap {
		return nil
	}

	m := v.Map()
	if m == nil {
		return nil
	}

	out := map[string]string{}

	m.ForEach(txn, func(key string, val goyjs.Value) bool {
		if val.Kind() == goyjs.KindString {
			out[key] = val.String()
		}

		return true
	})

	if len(out) == 0 {
		return nil
	}

	return out
}

// stringOf is a defensive accessor: returns the string payload for
// a KindString value, empty otherwise. The translation rules require
// scalar string values throughout — anything else is a client bug
// that materialization shouldn't crash on.
func stringOf(v goyjs.Value) string {
	if v.Kind() != goyjs.KindString {
		return ""
	}

	return v.String()
}
