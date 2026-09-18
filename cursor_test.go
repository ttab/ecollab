package ecollab_test

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/ttab/ecollab"
	"github.com/ttab/goyjs"
)

// hydratedDoc is a seeded article whose blocks the editor has
// hydrated: the document every test here reads carets against.
func hydratedDoc(t *testing.T) *goyjs.Doc {
	t.Helper()

	doc := goyjs.New()
	t.Cleanup(doc.Close)

	seed, err := ecollab.BuildSeedUpdate(article(), ecollab.RootName)
	if err != nil {
		t.Fatalf("build the seed update: %v", err)
	}

	if err := doc.ApplyUpdateV1(seed); err != nil {
		t.Fatalf("apply the seed update: %v", err)
	}

	hydrateAsTheEditorWould(t, doc)

	return doc
}

// caretIn returns a collapsed caret at index in the paragraph of a
// block's editable body, as the editor would take one: the caret is in
// the paragraph the text is in, not in the field that embeds it.
func caretIn(t *testing.T, doc *goyjs.Doc, block, index int) goyjs.Range {
	t.Helper()

	read := doc.NewReadTxn()
	defer read.Commit()

	pos, err := paragraphOf(t, doc, read, block).Node().Position(read, index, goyjs.AssocAfter)
	if err != nil {
		t.Fatalf("take a position in block %d: %v", block, err)
	}

	return goyjs.Range{Anchor: pos, Focus: pos}
}

// browserSpelling rewrites a position into the JSON a browser puts on
// the wire: JSON.stringify of a Y.RelativePosition instance, where the
// anchors that are not set are null rather than absent.
func browserSpelling(t *testing.T, p goyjs.Position) json.RawMessage {
	t.Helper()

	raw, err := json.Marshal(p)
	if err != nil {
		t.Fatalf("marshal the position: %v", err)
	}

	var fields map[string]json.RawMessage

	if err := json.Unmarshal(raw, &fields); err != nil {
		t.Fatalf("read the position back: %v", err)
	}

	for _, key := range []string{"type", "tname", "item"} {
		if _, ok := fields[key]; !ok {
			fields[key] = json.RawMessage("null")
		}
	}

	out, err := json.Marshal(fields)
	if err != nil {
		t.Fatalf("write the browser spelling: %v", err)
	}

	return out
}

// state builds one awareness state as JSON, which is what the
// awareness protocol carries per client.
func state(t *testing.T, fields map[string]any) json.RawMessage {
	t.Helper()

	raw, err := json.Marshal(fields)
	if err != nil {
		t.Fatalf("marshal the awareness state: %v", err)
	}

	return raw
}

// TestCursorReaderReadsTheBrowserSpelling is the case the package
// exists for: the state an editor publishes, decoded and resolved
// back to the value the caret is in and the offset in it.
func TestCursorReaderReadsTheBrowserSpelling(t *testing.T) {
	doc := hydratedDoc(t)
	caret := caretIn(t, doc, 1, 12)

	raw := state(t, map[string]any{
		"cursor": map[string]any{
			"anchor": browserSpelling(t, caret.Anchor),
			"focus":  browserSpelling(t, caret.Focus),
		},
		"data": map[string]any{"name": "Hanna"},
	})

	c, ok, err := ecollab.NewCursorReader().Cursor(1001, raw)
	if err != nil {
		t.Fatalf("read the caret: %v", err)
	}

	if !ok {
		t.Fatal("the state carries a caret, and the reader found none")
	}

	if c.ClientID != 1001 {
		t.Errorf("caret of client %d, want 1001", c.ClientID)
	}

	var descriptor struct {
		Name string `json:"name"`
	}

	if err := json.Unmarshal(c.Data, &descriptor); err != nil {
		t.Fatalf("read the descriptor: %v", err)
	}

	if descriptor.Name != "Hanna" {
		t.Errorf("descriptor name %q, want %q", descriptor.Name, "Hanna")
	}

	resolved, err := c.Resolve(doc, nil)
	if err != nil {
		t.Fatalf("resolve the caret: %v", err)
	}

	start, length, same := resolved.Range.Span()
	if !same {
		t.Fatal("the ends of a collapsed caret resolved into different values")
	}

	if start != 12 || length != 0 {
		t.Errorf("the caret resolved to %d+%d, want 12+0", start, length)
	}

	if resolved.Range.Anchor.Kind != goyjs.KindXMLText {
		t.Errorf("the caret resolved into a %s, want an XML text",
			resolved.Range.Anchor.Kind)
	}

	// Which value: the editable body of block 1, and no other.
	for block, want := range map[int]bool{0: false, 1: true, 2: false} {
		got := editingBlock(t, doc, block, resolved.Range.Anchor)
		if got != want {
			t.Errorf("editing block %d = %v, want %v", block, got, want)
		}
	}
}

// editingBlock asks the _collab reach whether a resolved position
// points into a block's editable body.
func editingBlock(
	t *testing.T, doc *goyjs.Doc, block int, end goyjs.Resolved,
) bool {
	t.Helper()

	read := doc.NewReadTxn()
	defer read.Commit()

	blocks, ok := doc.Map(ecollab.RootName).Get(read, "content")
	if !ok {
		t.Fatal("the document has no content blocks")
	}

	value, ok := blocks.Array().Get(read, block)
	if !ok {
		t.Fatalf("no block at index %d", block)
	}

	return ecollab.CollabOn(value.Map()).Editing(read, editorApp, bodyField, end)
}

// TestCursorReaderAbsentCaret covers the two ways a state says the
// participant is present without a selection.
func TestCursorReaderAbsentCaret(t *testing.T) {
	cases := map[string]json.RawMessage{
		"null":    json.RawMessage(`{"cursor":null,"data":{"name":"Hanna"}}`),
		"missing": json.RawMessage(`{"data":{"name":"Hanna"}}`),
		"empty":   json.RawMessage(`{}`),
	}

	for name, raw := range cases {
		t.Run(name, func(t *testing.T) {
			_, ok, err := ecollab.NewCursorReader().Cursor(1001, raw)
			if err != nil {
				t.Fatalf("read the state: %v", err)
			}

			if ok {
				t.Error("the reader found a caret in a state that carries none")
			}
		})
	}
}

// TestCursorReaderRefusesAHalfCaret: an envelope that is there but
// cannot be read means the editor and the reader disagree, which is
// worth an error rather than a silent absence.
func TestCursorReaderRefusesAHalfCaret(t *testing.T) {
	doc := hydratedDoc(t)
	caret := caretIn(t, doc, 0, 3)

	raw := state(t, map[string]any{
		"cursor": map[string]any{"anchor": caret.Anchor},
	})

	if _, _, err := ecollab.NewCursorReader().Cursor(1001, raw); err == nil {
		t.Error("a caret with no focus was read as a caret")
	}

	if _, _, err := ecollab.NewCursorReader().Cursor(1001, json.RawMessage(`{"cursor":7}`)); err == nil {
		t.Error("a number was read as a caret")
	}
}

// TestCursorReaderFieldNames: the envelope is the application's, so
// the names are the reader's to be told.
func TestCursorReaderFieldNames(t *testing.T) {
	doc := hydratedDoc(t)
	caret := caretIn(t, doc, 2, 4)

	raw := state(t, map[string]any{
		"selection": caret,
		"who":       map[string]any{"name": "Hanna"},
	})

	reader := ecollab.NewCursorReader(
		ecollab.WithCursorField("selection"),
		ecollab.WithCursorDataField("who"),
	)

	c, ok, err := reader.Cursor(1001, raw)
	if err != nil {
		t.Fatalf("read the caret: %v", err)
	}

	if !ok {
		t.Fatal("the reader found no caret under the configured field")
	}

	if string(c.Data) != `{"name":"Hanna"}` {
		t.Errorf("descriptor %s, want the who field", c.Data)
	}

	// The default reader looks under other names and finds nothing.
	if _, ok, err := ecollab.NewCursorReader().Cursor(1001, raw); err != nil || ok {
		t.Errorf("the default reader found (%v, %v) under a renamed envelope", ok, err)
	}
}

// TestCursorsSkipTheLocalClient: what an agent wants is the peers,
// ordered so that a decision made over them is reproducible.
func TestCursorsSkipTheLocalClient(t *testing.T) {
	doc := hydratedDoc(t)

	agent := goyjs.NewAwareness(2002)

	if err := agent.SetLocalStateField("cursor", caretIn(t, doc, 0, 1)); err != nil {
		t.Fatalf("publish the agent's own caret: %v", err)
	}

	for _, client := range []uint64{3003, 1001} {
		peer := goyjs.NewAwareness(client)

		if err := peer.SetLocalStateField("cursor", caretIn(t, doc, 1, 2)); err != nil {
			t.Fatalf("publish the caret of client %d: %v", client, err)
		}

		if err := agent.ApplyUpdate(peer.Encode(), nil); err != nil {
			t.Fatalf("apply the awareness of client %d: %v", client, err)
		}
	}

	cursors, err := ecollab.NewCursorReader().Cursors(agent)
	if err != nil {
		t.Fatalf("read the peers' carets: %v", err)
	}

	if len(cursors) != 2 {
		t.Fatalf("read %d carets, want the two peers'", len(cursors))
	}

	if cursors[0].ClientID != 1001 || cursors[1].ClientID != 3003 {
		t.Errorf("carets of %d and %d, want 1001 then 3003",
			cursors[0].ClientID, cursors[1].ClientID)
	}
}

// TestResolveAValueThatIsGone: a peer's caret in a value another
// participant has replaced is ErrStaleNode, which is an ordinary
// outcome of concurrent editing and not a decoding failure.
func TestResolveAValueThatIsGone(t *testing.T) {
	doc := hydratedDoc(t)
	caret := caretIn(t, doc, 0, 5)

	c := ecollab.Cursor{ClientID: 1001, Range: caret}

	err := doc.Write(func(w *goyjs.WriteTxn) error {
		read := w.ReadTxn()

		blocks, ok := doc.Map(ecollab.RootName).Get(read, "content")
		if !ok {
			t.Fatal("the document has no content blocks")
		}

		block, ok := blocks.Array().Get(read, 0)
		if !ok {
			t.Fatal("no block at index 0")
		}

		app, ok := ecollab.CollabOn(block.Map()).App(read, editorApp)
		if !ok {
			t.Fatal("the block has no editor state")
		}

		app.Set(w, bodyField, goyjs.XMLTextValue(nil, goyjs.Delta{
			goyjs.Insert("Skrivet på nytt.", nil),
		}))

		return nil
	})
	if err != nil {
		t.Fatalf("replace the value: %v", err)
	}

	if _, err := c.Resolve(doc, nil); !errors.Is(err, goyjs.ErrStaleNode) {
		t.Errorf("resolving a caret into a replaced value gave %v, want ErrStaleNode", err)
	}
}
