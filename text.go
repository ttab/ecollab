package ecollab

import (
	"strings"

	"github.com/ttab/goyjs"
)

// TextMatch is a run of text found in a rich value's delta. Both
// numbers count UTF-16 code units from the start of the value, with
// an embedded node counting as one, so they are what the goyjs write
// methods take: Node.Format, Node.Delete, Node.Range.
type TextMatch struct {
	Index  int
	Length int
}

// FindText returns every occurrence of needle in the text of a rich
// value's delta, in document order and without overlaps — what a
// paragraph's Value.Delta returns.
//
// The search is over the text a person reads, not over the delta's
// runs: formatting splits a delta into runs wherever a mark starts or
// ends, so a phrase with one bold word in it is three runs, and it is
// found all the same. An embedded node is not text and a match never
// spans one. In an editor's paragraph an embed is an inline element
// such as a link, which is one unit in the paragraph and carries its
// words in its own delta, so a phrase that runs into a link cannot be
// formatted or replaced as one span of this value; it is not found
// rather than found at offsets that would land on the wrong text.
//
// The comparison is exact: no case folding, and no whitespace or
// Unicode normalisation. An empty needle matches nothing.
func FindText(runs []goyjs.Run, needle string) []TextMatch {
	if needle == "" {
		return nil
	}

	var (
		matches []TextMatch
		segment strings.Builder
		start   int
		offset  int
	)

	flush := func() {
		matches = append(matches, findInSegment(segment.String(), start, needle)...)
		segment.Reset()
	}

	for _, run := range runs {
		if run.Embed != nil {
			flush()

			offset++
			start = offset

			continue
		}

		segment.WriteString(run.Text)
		offset += goyjs.UTF16Len(run.Text)
	}

	flush()

	return matches
}

// findInSegment finds needle in a stretch of text with no embeds in
// it, which starts at the UTF-16 offset start.
func findInSegment(text string, start int, needle string) []TextMatch {
	var (
		matches []TextMatch
		// consumed is how far into text the search has got, in
		// bytes, and units the same distance in UTF-16 code units.
		consumed int
		units    = start
		length   = goyjs.UTF16Len(needle)
	)

	for {
		i := strings.Index(text[consumed:], needle)
		if i < 0 {
			return matches
		}

		units += goyjs.UTF16Len(text[consumed : consumed+i])

		matches = append(matches, TextMatch{Index: units, Length: length})

		consumed += i + len(needle)
		units += length
	}
}

// ReplaceText returns the delta that replaces the text m covers with
// replacement, for Node.ApplyDelta on the value runs was read from.
// It is meant for a match FindText returned from the same runs; a
// match the value no longer holds is ErrOutOfRange from the write,
// and one that lands on different text replaces that text, so read
// and replace in one write scope.
//
// The replacement takes the formatting of the first unit it
// replaces, so a bold word replaced stays bold, and a phrase that
// starts plain and ends bold becomes plain throughout. It is written
// with exactly those format attributes, rather than inheriting
// whatever is in force to the left of the match, which is what an
// insert without attributes would do. An empty replacement deletes.
func ReplaceText(runs []goyjs.Run, m TextMatch, replacement string) goyjs.Delta {
	delta := goyjs.Delta{
		goyjs.Retain(m.Index, nil),
		goyjs.Delete(m.Length),
	}

	if replacement != "" {
		delta = append(delta, goyjs.Insert(replacement, formatAt(runs, m.Index)))
	}

	return delta
}

// formatAt returns the format attributes in force over the unit at
// index, as write attributes.
func formatAt(runs []goyjs.Run, index int) goyjs.Attrs {
	offset := 0

	for _, run := range runs {
		width := 1
		if run.Embed == nil {
			width = goyjs.UTF16Len(run.Text)
		}

		if index < offset+width {
			if len(run.Attributes) == 0 {
				return nil
			}

			attrs := make(goyjs.Attrs, len(run.Attributes))

			for k, v := range run.Attributes {
				attrs[k] = jsonInput(v)
			}

			return attrs
		}

		offset += width
	}

	return nil
}

// jsonInput turns a JSON-kinded value read out of a document back
// into the input that writes it. Format attributes only ever hold
// JSON kinds; anything else becomes null, which a write would read as
// removing the attribute, so it cannot occur for a value read from a
// format position.
func jsonInput(v goyjs.Value) goyjs.Input {
	switch v.Kind() {
	case goyjs.KindBool:
		return goyjs.Bool(v.Bool())
	case goyjs.KindInt:
		return goyjs.Int(v.Int())
	case goyjs.KindFloat:
		return goyjs.Float(v.Float())
	case goyjs.KindString:
		return goyjs.String(v.String())
	case goyjs.KindBytes:
		return goyjs.Bytes(v.Bytes())
	case goyjs.KindJSONArray:
		items := make([]goyjs.Input, 0, len(v.JSONArray()))

		for _, item := range v.JSONArray() {
			items = append(items, jsonInput(item))
		}

		return goyjs.JSONArray(items...)
	case goyjs.KindJSONMap:
		entries := make(map[string]goyjs.Input, len(v.JSONMap()))

		for k, item := range v.JSONMap() {
			entries[k] = jsonInput(item)
		}

		return goyjs.JSONMap(entries)
	case goyjs.KindUndefined, goyjs.KindNull, goyjs.KindArray,
		goyjs.KindMap, goyjs.KindText, goyjs.KindXMLElem,
		goyjs.KindXMLText, goyjs.KindXMLFrag, goyjs.KindDoc,
		goyjs.KindWeak:
		return goyjs.Null()
	}

	return goyjs.Null()
}
