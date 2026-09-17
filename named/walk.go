package named

import (
	"encoding/base64"
	"encoding/json"
	"fmt"

	"github.com/ttab/goyjs"
)

// WalkToJSON materializes a named-doc Y.Doc into JSON. Named docs are
// conventionally restricted to the YMap / YArray / string trinity (see
// the README, under "Allowed Yjs types in named documents"), but the
// transport itself is type-agnostic, so a client can land a rich Yjs type
// in a named doc. The walk renders any such type best-effort rather than
// failing: text types become their plain string, YXmlElement its
// serialized XML, and the non-stringable shared types a structured
// {"_yjs":...} marker. The only error a caller sees is a json.Marshal
// failure.
//
// The Doc's root YMap with the given name is the entry point. If
// the root is empty or absent the function returns "{}".
//
// The caller must NOT hold a ReadTxn on doc when calling: a ReadTxn
// holds the Doc's mutex until it commits, so opening a second one
// blocks forever. Resolving the root before the transaction is the
// idiomatic order, not a requirement.
func WalkToJSON(doc *goyjs.Doc, rootName string) ([]byte, error) {
	root := doc.Map(rootName)

	txn := doc.NewReadTxn()
	defer txn.Commit()

	if root == nil || root.Len(txn) == 0 {
		return []byte("{}"), nil
	}

	b, err := json.Marshal(walkMap(root, txn))
	if err != nil {
		return nil, fmt.Errorf("marshal named document: %w", err)
	}

	return b, nil
}

func walkMap(m *goyjs.Map, txn *goyjs.ReadTxn) map[string]any {
	out := make(map[string]any, m.Len(txn))

	m.ForEach(txn, func(key string, v goyjs.Value) bool {
		out[key] = convert(v, txn)

		return true
	})

	return out
}

func walkArray(a *goyjs.Array, txn *goyjs.ReadTxn) []any {
	out := make([]any, 0, a.Len())

	a.ForEach(txn, func(_ int, v goyjs.Value) bool {
		out = append(out, convert(v, txn))

		return true
	})

	return out
}

// yjsTypeKey is the key the marker objects for non-stringable shared
// types carry, so an inspecting client can tell "this is an opaque Yjs
// type" from an ordinary object.
const yjsTypeKey = "_yjs"

// convert maps a goyjs Value into the corresponding JSON-shaped Go value.
// Named docs are conventionally constrained to YMap / YArray / string plus
// JSON scalars, but rich Yjs types are rendered best-effort so inspection
// never fails: text types as their plain string, YXmlElement as serialized
// XML, binary as base64, and the non-stringable shared types (fragment,
// subdoc, weak) plus any unknown future kind as a {"_yjs":...} marker.
func convert(v goyjs.Value, txn *goyjs.ReadTxn) any {
	switch v.Kind() {
	case goyjs.KindMap:
		return walkMap(v.Map(), txn)
	case goyjs.KindArray:
		return walkArray(v.Array(), txn)
	case goyjs.KindString:
		return v.String()
	case goyjs.KindBool:
		return v.Bool()
	case goyjs.KindInt:
		return v.Int()
	case goyjs.KindFloat:
		return v.Float()
	case goyjs.KindNull, goyjs.KindUndefined:
		return nil
	case goyjs.KindJSONArray:
		out := make([]any, 0, len(v.JSONArray()))
		for _, el := range v.JSONArray() {
			out = append(out, convert(el, txn))
		}

		return out
	case goyjs.KindJSONMap:
		out := make(map[string]any, len(v.JSONMap()))
		for k, el := range v.JSONMap() {
			out[k] = convert(el, txn)
		}

		return out
	case goyjs.KindText, goyjs.KindXMLText, goyjs.KindXMLElem:
		// Rich text/XML shared types. Render their string content
		// (plain text, or serialized XML for elements) rather than
		// failing the walk.
		s, _ := v.RichString()

		return s
	case goyjs.KindBytes:
		return base64.StdEncoding.EncodeToString(v.Bytes())
	case goyjs.KindXMLFrag:
		return map[string]any{yjsTypeKey: "fragment"}
	case goyjs.KindDoc:
		return map[string]any{yjsTypeKey: "subdoc"}
	case goyjs.KindWeak:
		return map[string]any{yjsTypeKey: "weak"}
	default:
		return map[string]any{yjsTypeKey: "unknown"}
	}
}
