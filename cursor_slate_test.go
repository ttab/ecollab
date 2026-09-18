package ecollab_test

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/ttab/ecollab"
	"github.com/ttab/goyjs"
)

// The caret an editor built on @slate-yjs/core publishes, read by this
// package's defaults. Everything the default CursorReader assumes about
// the envelope - which state field the caret is under, which the
// descriptor, the anchor/focus pair inside, the spelling of a position
// on the wire and the association the library picks at each place -
// is held here to the library itself, at the release pinned in
// testdata/node/package-lock.json, rather than to a literal a test
// wrote and then read back. A reader looking under the wrong name sees
// an empty room and no error, so nothing weaker would catch a drift.
//
// The helper is a one-shot Node process. It needs the dependencies in
// testdata/node installed - `make node-deps` - and the tests skip with
// that hint when they are not, so `go test` without them still runs
// everything that needs no editor.

// slatePoint is a Slate point into a field: a paragraph of the field
// and an offset into its text, in UTF-16 code units as Slate counts.
type slatePoint struct {
	Paragraph int `json:"paragraph"`
	Offset    int `json:"offset"`
}

// caretRequest is what the helper is asked to publish.
type caretRequest struct {
	ClientID uint64         `json:"clientId"`
	Update   string         `json:"update"`
	Block    int            `json:"block"`
	AppID    string         `json:"appId"`
	Field    string         `json:"field"`
	Anchor   slatePoint     `json:"anchor"`
	Focus    slatePoint     `json:"focus"`
	Data     map[string]any `json:"data"`
}

// caretReply is what it publishes: the awareness update carrying its
// state, the state as the JSON on the wire, the Slate tree it built
// from the field, and whether binding the editor wrote anything back
// to the document.
type caretReply struct {
	ClientID   uint64          `json:"clientId"`
	Update     string          `json:"update"`
	State      json.RawMessage `json:"state"`
	Children   json.RawMessage `json:"children"`
	DocChanged bool            `json:"docChanged"`
}

// publishWithSlateYjs runs the helper against doc and returns what the
// editor published. It skips the test when the helper's dependencies
// are not installed.
func publishWithSlateYjs(
	t *testing.T, doc *goyjs.Doc, block int, anchor, focus slatePoint,
) caretReply {
	t.Helper()

	dir, err := filepath.Abs("testdata/node")
	if err != nil {
		t.Fatalf("locate testdata/node: %v", err)
	}

	if _, err := os.Stat(filepath.Join(dir, "node_modules")); err != nil {
		t.Skip("testdata/node/node_modules missing; run `make node-deps` to enable")
	}

	req := caretRequest{
		ClientID: humanClient,
		Update:   base64.StdEncoding.EncodeToString(doc.EncodeStateV1()),
		Block:    block,
		AppID:    editorApp,
		Field:    bodyField,
		Anchor:   anchor,
		Focus:    focus,
		Data:     map[string]any{"name": "Hanna"},
	}

	in, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("marshal the request: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, "node", "caret.mjs")
	cmd.Dir = dir
	cmd.Stdin = bytes.NewReader(in)

	var stderr bytes.Buffer

	cmd.Stderr = &stderr

	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("run the caret helper: %v\n%s", err, stderr.String())
	}

	var reply caretReply
	if err := json.Unmarshal(out, &reply); err != nil {
		t.Fatalf("decode the helper's reply %q: %v", out, err)
	}

	return reply
}

// readTheOnlyCaret applies the helper's awareness update to an agent's
// awareness and reads the one caret in it with the default reader.
func readTheOnlyCaret(t *testing.T, reply caretReply) ecollab.Cursor {
	t.Helper()

	update, err := base64.StdEncoding.DecodeString(reply.Update)
	if err != nil {
		t.Fatalf("decode the awareness update: %v", err)
	}

	agent := goyjs.NewAwareness(agentClient)
	if err := agent.ApplyUpdate(update, nil); err != nil {
		t.Fatalf("apply the editor's awareness: %v", err)
	}

	cursors, err := ecollab.NewCursorReader().Cursors(agent)
	if err != nil {
		t.Fatalf("read the carets: %v", err)
	}

	if len(cursors) != 1 {
		t.Fatalf("the default reader found %d carets in the editor's state %s, want 1",
			len(cursors), reply.State)
	}

	return cursors[0]
}

// TestCursorReaderReadsWhatSlateYjsPublishes is the pin: a caret the
// real editor publishes into the paragraph of a block is found by the
// default reader, carries the descriptor, resolves to the same place
// in the same paragraph, and marks that block and no other as being
// edited. The editor also accepts the field shape the tests hydrate
// without rewriting it, which is what makes that shape the right
// stand-in everywhere else.
func TestCursorReaderReadsWhatSlateYjsPublishes(t *testing.T) {
	doc := hydratedDoc(t)

	at := slatePoint{Paragraph: 0, Offset: 12}
	reply := publishWithSlateYjs(t, doc, busyBlock, at, at)

	t.Logf("the editor published %s", reply.State)

	if reply.DocChanged {
		t.Fatalf("binding the editor rewrote the hydrated field; it read it as %s", reply.Children)
	}

	c := readTheOnlyCaret(t, reply)

	if c.ClientID != humanClient {
		t.Errorf("caret of client %d, want %d", c.ClientID, humanClient)
	}

	var descriptor struct {
		Name string `json:"name"`
	}

	if err := json.Unmarshal(c.Data, &descriptor); err != nil || descriptor.Name != "Hanna" {
		t.Errorf("descriptor %s (%v), want the name published beside the caret", c.Data, err)
	}

	resolved, err := c.Resolve(doc, nil)
	if err != nil {
		t.Fatalf("resolve the editor's caret: %v", err)
	}

	start, length, same := resolved.Range.Span()
	if !same || start != 12 || length != 0 {
		t.Errorf("the caret resolved to %d+%d (same type %v), want 12+0", start, length, same)
	}

	if resolved.Range.Anchor.Kind != goyjs.KindXMLText {
		t.Errorf("the caret resolved into a %s, want the paragraph's XML text",
			resolved.Range.Anchor.Kind)
	}

	for block, want := range map[int]bool{0: false, 1: true, 2: false} {
		if got := editingBlock(t, doc, block, resolved.Range.Anchor); got != want {
			t.Errorf("editing block %d = %v, want %v", block, got, want)
		}
	}

	// The stand-in the other tests publish with is the position the
	// editor publishes: the same anchor, the same hint, the same
	// association.
	standIn := caretIn(t, doc, busyBlock, 12)
	if !c.Range.Anchor.Equal(standIn.Anchor) || !c.Range.Focus.Equal(standIn.Focus) {
		t.Errorf("the editor's caret %s is not the stand-in caretIn takes", reply.State)
	}
}

// TestSlateYjsAnchorsTheEndOfARunBefore: at the end of a text run the
// editor sticks the caret to the unit before the gap - the last
// character of the run - rather than to what follows, so that a point
// on the boundary between two runs keeps to the run it came from. This
// is the one place the library departs from yjs's default association,
// and it is visible to a reader: such a caret resolves to the end of
// the run now, and stays before whatever a peer appends there rather
// than moving past it.
func TestSlateYjsAnchorsTheEndOfARunBefore(t *testing.T) {
	doc := hydratedDoc(t)

	read := doc.NewReadTxn()
	end := paragraphOf(t, doc, read, busyBlock).Len()
	read.Commit()

	at := slatePoint{Paragraph: 0, Offset: end}
	reply := publishWithSlateYjs(t, doc, busyBlock, at, at)

	c := readTheOnlyCaret(t, reply)

	if c.Range.Anchor.Assoc() != goyjs.AssocBefore || c.Range.Focus.Assoc() != goyjs.AssocBefore {
		t.Errorf("a caret at the end of a run is anchored %v/%v, want before",
			c.Range.Anchor.Assoc(), c.Range.Focus.Assoc())
	}

	resolved, err := c.Resolve(doc, nil)
	if err != nil {
		t.Fatalf("resolve the caret: %v", err)
	}

	if start, _, _ := resolved.Range.Span(); start != end {
		t.Errorf("the caret resolved to %d, want the end of the paragraph, %d", start, end)
	}

	// A peer appends at the end: the caret keeps to the run it was in
	// and the appended text lands after it.
	err = doc.Write(func(w *goyjs.WriteTxn) error {
		paragraphOf(t, doc, w.ReadTxn(), busyBlock).Node().Insert(w, end, " Mer.")

		return nil
	})
	if err != nil {
		t.Fatalf("append at the end: %v", err)
	}

	resolved, err = c.Resolve(doc, nil)
	if err != nil {
		t.Fatalf("resolve the caret after the append: %v", err)
	}

	if start, _, _ := resolved.Range.Span(); start != end {
		t.Errorf("the caret resolved to %d after a peer appended at the end, want it to stay at %d",
			start, end)
	}
}

// TestSlateYjsSelectionSpansItsWords: a selection is the anchor and
// focus the editor publishes, and Span reads it back as the run of
// text between them.
func TestSlateYjsSelectionSpansItsWords(t *testing.T) {
	doc := hydratedDoc(t)

	reply := publishWithSlateYjs(t, doc, busyBlock,
		slatePoint{Paragraph: 0, Offset: 8},
		slatePoint{Paragraph: 0, Offset: 14})

	c := readTheOnlyCaret(t, reply)

	resolved, err := c.Resolve(doc, nil)
	if err != nil {
		t.Fatalf("resolve the selection: %v", err)
	}

	start, length, same := resolved.Range.Span()
	if !same || start != 8 || length != 6 {
		t.Errorf("the selection resolved to %d+%d (same type %v), want 8+6", start, length, same)
	}
}

// TestSlateYjsCaretWhoseParagraphIsGone: the editor's caret into a
// field another participant has replaced resolves as ErrStaleNode, so
// the reader and the library agree about what a vanished value looks
// like.
func TestSlateYjsCaretWhoseParagraphIsGone(t *testing.T) {
	doc := hydratedDoc(t)

	at := slatePoint{Paragraph: 0, Offset: 3}
	reply := publishWithSlateYjs(t, doc, editedBlock, at, at)

	c := readTheOnlyCaret(t, reply)

	replaceField(t, doc, editedBlock)

	if _, err := c.Resolve(doc, nil); !errors.Is(err, goyjs.ErrStaleNode) {
		t.Errorf("resolving the editor's caret into a replaced field gave %v, want ErrStaleNode", err)
	}
}
