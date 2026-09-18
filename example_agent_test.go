package ecollab_test

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/ttab/ecollab"
	"github.com/ttab/elephant-api/newsdoc"
	"github.com/ttab/goyjs"
)

// An agent joining a live editing session.
//
// The workload: a document arrives over whatever transport the agent
// speaks; the editing application has hydrated the editable copy of
// each text block under _collab; a human is editing one of the three
// blocks, and their editor publishes the caret into awareness as a
// pair of relative positions against the value it is bound to. The
// agent reads the blocks nobody is in, finds something worth
// remarking on, and attaches a comment to the exact run of text it
// found.
//
// Between the reading and the writing, a colleague types at the start
// of one of those blocks. That is the interesting part. The agent
// does not hold an offset across its own thinking - an offset is true
// only for the document it was computed from, and the colleague just
// invalidated it - it holds a goyjs.Range, which names the run by the
// identity of the text in it. When the write lands, the comment is
// around the words the agent chose, several characters further along
// than where it first found them.
//
// The shape of the editable value matters to every step, so the file
// uses the one an editor built on @slate-yjs/core stores: a field is a
// Y.XmlText whose delta is a sequence of embedded paragraph blocks,
// each a Y.XmlText of its own carrying its properties as node
// attributes and its text in its own delta. The text a person types,
// and the caret they publish, are in the paragraph, not in the field -
// which is why the agent asks whether the field holds the caret rather
// than whether it is the caret's type, and why it takes its ranges and
// writes its formatting in the paragraph.
//
// The analysis here is a fixed-string search. A real agent would put
// a model behind that step and nothing else about the shape would
// change: what it produces is a range and a thread id, and the range
// is what this file shows how to hold and how to annotate.

const (
	// editorApp is the application that owns the editable state under
	// _collab. The agent writes into the editor's values, so it uses
	// the editor's ID rather than one of its own.
	editorApp = "se.ecms.article-editor"
	// bodyField is the key the editor hydrates per block.
	bodyField = "text"
	// threadID identifies the comment thread the agent is attaching to.
	threadID = "123"
	// phrase is what the analysis step looks for.
	phrase = "dricksvattnet"
	// typed is what the colleague inserts at the start of the first
	// block while the agent is thinking.
	typed = "Redan i dag: "

	humanClient = 1001
	agentClient = 2002

	// busyBlock is the block the human has the caret in, and
	// editedBlock the one the colleague types into.
	busyBlock   = 1
	editedBlock = 0
)

func TestAgentHoldsItsRangeWhileTheHumansType(t *testing.T) {
	doc := goyjs.New()
	t.Cleanup(doc.Close)

	// 1. The document, as any transport delivers it: an update.
	seed, err := ecollab.BuildSeedUpdate(article(), ecollab.RootName)
	if err != nil {
		t.Fatalf("build the seed update: %v", err)
	}

	if err := doc.ApplyUpdateV1(seed); err != nil {
		t.Fatalf("apply the seed update: %v", err)
	}

	// 2. The editing application hydrates an editable YXmlText per
	//    block under _collab, one embedded paragraph block per line of
	//    text. The agent does not do this - it is the authoring
	//    application's protocol - but a test has to stand in for the
	//    editor that would have done it.
	hydrateAsTheEditorWould(t, doc)

	// 3. Two other participants, each with their own document: the
	//    human whose caret the agent is about to read, and a colleague
	//    who has not said anything yet.
	humanDoc := replicaOf(t, doc)
	colleagueDoc := replicaOf(t, doc)

	// 4. The human's editor publishes the caret. It is a pair of
	//    relative positions against the paragraph the caret is in,
	//    wrapped in the envelope the editor chooses - here
	//    @slate-yjs/core's, which is what CursorReader reads by
	//    default.
	human := goyjs.NewAwareness(humanClient)

	publishCaret(t, human, humanDoc, busyBlock, 12)

	agent := goyjs.NewAwareness(agentClient)
	if err := agent.ApplyUpdate(human.Encode(), nil); err != nil {
		t.Fatalf("apply the human's awareness: %v", err)
	}

	// 5. The agent asks of every peer's awareness state: which value
	//    are you editing, and where. What it needs here is the first
	//    half - which blocks to leave alone.
	busy := blocksBeingEdited(t, doc, agent)

	if _, ok := busy[busyBlock]; !ok || len(busy) != 1 {
		t.Fatalf("the agent read the peers' carets as %v, want only block %d",
			busy, busyBlock)
	}

	// 6. The agent reads the rest of the blocks and decides what to
	//    annotate. What it comes away with is a range per block, not
	//    an offset: the offset it found is recorded only so the test
	//    can show it has gone stale.
	plans := decideWhatToAnnotate(t, doc, busy)

	if len(plans) != 2 {
		t.Fatalf("the agent planned %d annotations, want 2", len(plans))
	}

	// 7. While the agent is thinking, a colleague types at the start
	//    of a block it decided to annotate, and the update reaches the
	//    agent. Awareness said nothing about this: the colleague had
	//    no caret to publish when the agent looked, which is why
	//    reading awareness is not a substitute for holding a range.
	theColleagueTypes(t, colleagueDoc, editedBlock)
	deliver(t, colleagueDoc, doc)

	// 8. The agent attaches the comments through the ranges it held.
	starts := attachComments(t, doc, plans)

	// The comment is around the words the agent chose, in both
	// blocks, and in the edited one it is further along than the
	// offset the agent first computed. An agent that had held that
	// offset would have commented on the wrong words.
	for _, p := range plans {
		got := renderParagraph(t, doc, p.block)

		t.Logf("block %d -> %s", p.block, got)

		want := `<comment thread="` + threadID + `">` + phrase + `</comment>`
		if !strings.Contains(got, want) {
			t.Errorf("block %d rendered %q, want it to contain %q",
				p.block, got, want)
		}

		switch p.block {
		case editedBlock:
			if starts[p.block] != p.staleOffset+goyjs.UTF16Len(typed) {
				t.Errorf("block %d: the held range resolved to %d, want the stale offset %d moved by %d",
					p.block, starts[p.block], p.staleOffset, goyjs.UTF16Len(typed))
			}

			if !strings.HasPrefix(got, typed) {
				t.Errorf("block %d lost what the colleague typed: %q", p.block, got)
			}
		default:
			if starts[p.block] != p.staleOffset {
				t.Errorf("block %d: the held range resolved to %d, want %d - nothing moved in it",
					p.block, starts[p.block], p.staleOffset)
			}
		}
	}

	// The block the human is editing is untouched: no comment, and the
	// text is exactly as it was.
	busyText := renderParagraph(t, doc, busyBlock)

	if strings.Contains(busyText, "<comment") {
		t.Errorf("the agent annotated the block the human is editing: %q", busyText)
	}

	if !strings.Contains(busyText, phrase) {
		t.Errorf("the busy block lost its text: %q", busyText)
	}
}

// plan is one annotation the agent has decided on: the block, the
// paragraph it lives in, and the run of text to comment on - held as a
// range, so that it survives whatever the humans do next.
type plan struct {
	block int
	node  *goyjs.Node
	span  goyjs.Range

	// staleOffset is where the run was when the agent found it. An
	// agent has no use for it; the test keeps it to show that the
	// place moved.
	staleOffset int
}

// blocksBeingEdited resolves every peer's caret against the document
// and reports which blocks they are in. A caret that resolves into a
// value the agent cannot place - another application's field, a value
// that has since been deleted - simply does not mark a block.
//
// The caret resolves into the paragraph the person is typing in, so
// the question per block is whether the block's field holds that
// paragraph, which is what Editing asks. It is asked of every end
// that resolved: an editor publishes both against the field it is
// bound to, so for a selection inside one field either would do, and
// a selection whose other end is in a paragraph a peer has deleted
// still has this one.
//
// Cursors returns the carets it could read alongside an error for the
// states it could not; a real agent logs the error and works with the
// carets, and a test fails, because a state it cannot read is a bug
// in the test.
func blocksBeingEdited(
	t *testing.T, doc *goyjs.Doc, a *goyjs.Awareness,
) map[int]struct{} {
	t.Helper()

	cursors, err := ecollab.NewCursorReader().Cursors(a)
	if err != nil {
		t.Fatalf("read the peers' carets: %v", err)
	}

	read := doc.NewReadTxn()
	defer read.Commit()

	busy := map[int]struct{}{}

	for _, c := range cursors {
		resolved, err := c.Resolve(doc, read)

		switch {
		case errors.Is(err, goyjs.ErrStaleNode):
			// Both values this peer's selection was in are gone. That
			// is an ordinary outcome of concurrent editing and not a
			// reason to stop reading the others.
			continue
		case err != nil:
			t.Fatalf("resolve the caret of client %d: %v", c.ClientID, err)
		}

		forEachBlock(t, doc, read, func(i int, block goyjs.Value) {
			collab := ecollab.CollabOn(block.Map())

			for _, end := range resolved.Ends() {
				if collab.Editing(read, editorApp, bodyField, end) {
					busy[i] = struct{}{}
				}
			}
		})
	}

	return busy
}

// decideWhatToAnnotate is the agent's own work: for every block no
// peer is in, find the phrase in one of its paragraphs and take a
// range over it. The ranges are taken under one read transaction, so
// they all describe the same document state, and they stay true for
// every state after it.
func decideWhatToAnnotate(
	t *testing.T, doc *goyjs.Doc, busy map[int]struct{},
) []plan {
	t.Helper()

	read := doc.NewReadTxn()
	defer read.Commit()

	var plans []plan

	forEachBlock(t, doc, read, func(i int, block goyjs.Value) {
		if _, editing := busy[i]; editing {
			return
		}

		field, ok := ecollab.CollabOn(block.Map()).Field(read, editorApp, bodyField)
		if !ok {
			return
		}

		// The text is in the paragraphs the field embeds, one per run
		// of its delta, and so is the range: a paragraph is the value
		// the phrase is in, and the value the comment is written
		// through.
		for _, run := range field.Delta() {
			if run.Embed == nil || run.Embed.Kind() != goyjs.KindXMLText {
				continue
			}

			paragraph := *run.Embed

			// The analysis step. A real agent reasons over the text
			// here; what it has to come back with is an offset and a
			// length, and those are in UTF-16 code units because that
			// is how yjs counts - which is why the offset is
			// accumulated with UTF16Len rather than with len().
			offset, found := findRun(paragraph, phrase)
			if !found {
				continue
			}

			// The offset is turned into a range immediately, while the
			// document it was computed from is still the document.
			// From here on the agent carries the range and nothing
			// else.
			span, err := paragraph.Node().Range(read, offset, goyjs.UTF16Len(phrase))
			if err != nil {
				t.Fatalf("hold the run in block %d: %v", i, err)
			}

			plans = append(plans, plan{
				block:       i,
				node:        paragraph.Node(),
				span:        span,
				staleOffset: offset,
			})

			return
		}
	})

	return plans
}

// attachComments writes every planned annotation in one write scope,
// so a peer sees all of the agent's comments or none of them, and
// returns the offset each range resolved to.
//
// Each range is resolved against the document as the scope opened -
// which is the document the colleague's update has already reached -
// and the resolution says both where the run is now and which value
// it is in. A range whose ends have met is a run that was deleted
// while the agent thought, and there is nothing left to comment on.
func attachComments(t *testing.T, doc *goyjs.Doc, plans []plan) map[int]int {
	t.Helper()

	starts := map[int]int{}

	err := doc.Write(func(w *goyjs.WriteTxn) error {
		read := w.ReadTxn()

		for _, p := range plans {
			resolved, err := doc.ResolveRange(read, p.span)
			if err != nil {
				return fmt.Errorf("resolve the held run in block %d: %w", p.block, err)
			}

			start, length, ok := resolved.Span()
			if !ok {
				return fmt.Errorf("block %d: the run's ends are in different values", p.block)
			}

			if length == 0 {
				continue
			}

			// The value the range resolved into is the value to write
			// through. It is the same paragraph the agent read, and
			// saying so is how an agent that kept a handle checks that
			// it is still the right one.
			node := resolved.Anchor.Node
			if !p.node.Same(node) {
				return fmt.Errorf("block %d: the run moved to another value", p.block)
			}

			node.Format(w, start, length, goyjs.Attrs{
				"comment": goyjs.JSONMap(map[string]goyjs.Input{
					"thread": goyjs.String(threadID),
				}),
			})

			starts[p.block] = start
		}

		return nil
	})
	if err != nil {
		t.Fatalf("attach the comments: %v", err)
	}

	return starts
}

// publishCaret stands in for the editor's withCursors binding: it
// takes a relative position in the paragraph the caret is in and
// publishes it into awareness, under the field names @slate-yjs/core
// uses. A collapsed caret is the same position twice, and slate-yjs
// anchors a caret inside a text run AssocAfter, as this does.
func publishCaret(
	t *testing.T, a *goyjs.Awareness, doc *goyjs.Doc, block, index int,
) {
	t.Helper()

	read := doc.NewReadTxn()
	defer read.Commit()

	pos, err := paragraphOf(t, doc, read, block).Node().Position(read, index, goyjs.AssocAfter)
	if err != nil {
		t.Fatalf("take the caret position in block %d: %v", block, err)
	}

	err = a.SetLocalStateField(ecollab.DefaultCursorField, goyjs.Range{
		Anchor: pos,
		Focus:  pos,
	})
	if err != nil {
		t.Fatalf("publish the caret: %v", err)
	}

	err = a.SetLocalStateField(ecollab.DefaultCursorDataField, map[string]any{
		"name": "Hanna",
	})
	if err != nil {
		t.Fatalf("publish the caret descriptor: %v", err)
	}
}

// theColleagueTypes inserts at the start of a block's paragraph on the
// colleague's own document, which is where the concurrent edit comes
// from: their client, not the agent's.
func theColleagueTypes(t *testing.T, doc *goyjs.Doc, block int) {
	t.Helper()

	err := doc.Write(func(w *goyjs.WriteTxn) error {
		paragraphOf(t, doc, w.ReadTxn(), block).Node().Insert(w, 0, typed)

		return nil
	})
	if err != nil {
		t.Fatalf("type into block %d: %v", block, err)
	}
}

// findRun walks the value's delta and returns the UTF-16 offset of the
// first occurrence of needle. Walking the delta rather than the
// rendered string is what keeps the offset right: the rendering carries
// formatting as XML tags, and an offset counted over that would include
// them.
func findRun(v goyjs.Value, needle string) (int, bool) {
	offset := 0

	for _, run := range v.Delta() {
		if run.Embed != nil {
			// An embedded node is one unit wide whatever it holds.
			offset++

			continue
		}

		if i := strings.Index(run.Text, needle); i >= 0 {
			return offset + goyjs.UTF16Len(run.Text[:i]), true
		}

		offset += goyjs.UTF16Len(run.Text)
	}

	return 0, false
}

// hydrateAsTheEditorWould stands in for the editing application, which
// creates the editable YXmlText for each block under _collab and copies
// the NewsDoc text into it in the shape it edits. EnsureField creates
// every level that is missing and leaves an existing value alone, so
// the editor and the agent racing to hydrate the same field cannot
// produce two of them on this document.
func hydrateAsTheEditorWould(t *testing.T, doc *goyjs.Doc) {
	t.Helper()

	err := doc.Write(func(w *goyjs.WriteTxn) error {
		read := w.ReadTxn()

		blocks, ok := doc.Map(ecollab.RootName).Get(read, "content")
		if !ok {
			t.Fatal("the seeded document has no content blocks")
		}

		var ferr error

		blocks.Array().ForEach(read, func(_ int, block goyjs.Value) bool {
			m := block.Map()

			data, ok := m.Get(read, "data")
			if !ok {
				return true
			}

			text, ok := data.Map().Get(read, bodyField)
			if !ok {
				return true
			}

			_, err := ecollab.CollabOn(m).EnsureField(w, editorApp, bodyField,
				richText(text.String()))
			if err != nil {
				ferr = err

				return false
			}

			return true
		})

		return ferr
	})
	if err != nil {
		t.Fatalf("hydrate the editable state: %v", err)
	}
}

// richText is a field as an editor built on @slate-yjs/core stores it:
// a Y.XmlText whose delta is one embedded core/text block per
// paragraph, each carrying its properties as node attributes and the
// paragraph's text in its own delta. A bare text run at the root of
// the field is not a shape such an editor accepts, so nothing here
// produces one.
func richText(text string) goyjs.Input {
	lines := strings.Split(text, "\n")
	delta := make(goyjs.Delta, 0, len(lines))

	for _, line := range lines {
		var runs goyjs.Delta
		if line != "" {
			runs = goyjs.Delta{goyjs.Insert(line, nil)}
		}

		delta = append(delta, goyjs.Embed(goyjs.XMLTextValue(goyjs.Attrs{
			"type":       goyjs.String("core/text"),
			"class":      goyjs.String("text"),
			"properties": goyjs.JSONMap(map[string]goyjs.Input{}),
		}, runs), nil))
	}

	return goyjs.XMLTextValue(nil, delta)
}

// replicaOf returns a second document holding what doc holds: another
// participant's client, after its own sync.
func replicaOf(t *testing.T, doc *goyjs.Doc) *goyjs.Doc {
	t.Helper()

	replica := goyjs.New()
	t.Cleanup(replica.Close)

	if err := replica.ApplyUpdateV1(doc.EncodeStateV1()); err != nil {
		t.Fatalf("sync the replica: %v", err)
	}

	return replica
}

// deliver sends what from has and to lacks, which is what a client's
// connection to the service carries.
func deliver(t *testing.T, from, to *goyjs.Doc) {
	t.Helper()

	if err := to.ApplyUpdateV1(from.EncodeDiffV1(to.StateVectorV1())); err != nil {
		t.Fatalf("apply the update: %v", err)
	}
}

// forEachBlock walks the content blocks of the document under the
// caller's transaction.
func forEachBlock(
	t *testing.T, doc *goyjs.Doc, read *goyjs.ReadTxn,
	fn func(i int, block goyjs.Value),
) {
	t.Helper()

	blocks, ok := doc.Map(ecollab.RootName).Get(read, "content")
	if !ok {
		t.Fatal("the document has no content blocks")
	}

	blocks.Array().ForEach(read, func(i int, block goyjs.Value) bool {
		fn(i, block)

		return true
	})
}

// fieldOf returns the editable field of a block under the caller's
// transaction.
func fieldOf(t *testing.T, doc *goyjs.Doc, read *goyjs.ReadTxn, index int) goyjs.Value {
	t.Helper()

	blocks, ok := doc.Map(ecollab.RootName).Get(read, "content")
	if !ok {
		t.Fatal("the document has no content blocks")
	}

	block, ok := blocks.Array().Get(read, index)
	if !ok {
		t.Fatalf("no block at index %d", index)
	}

	field, ok := ecollab.CollabOn(block.Map()).Field(read, editorApp, bodyField)
	if !ok {
		t.Fatalf("block %d has no editable body", index)
	}

	return field
}

// paragraphOf returns the first paragraph of a block's editable field:
// the value the text is in, and the one a caret in the block resolves
// into.
func paragraphOf(t *testing.T, doc *goyjs.Doc, read *goyjs.ReadTxn, index int) goyjs.Value {
	t.Helper()

	runs := fieldOf(t, doc, read, index).Delta()
	if len(runs) == 0 || runs[0].Embed == nil || runs[0].Embed.Kind() != goyjs.KindXMLText {
		t.Fatalf("block %d has no paragraph", index)
	}

	return *runs[0].Embed
}

// renderParagraph returns the rendered XML of a block's first
// paragraph.
func renderParagraph(t *testing.T, doc *goyjs.Doc, index int) string {
	t.Helper()

	read := doc.NewReadTxn()
	defer read.Commit()

	s, _ := paragraphOf(t, doc, read, index).RichString()

	return s
}

func article() *newsdoc.Document {
	return &newsdoc.Document{
		Uuid:     "6c0c1cf2-9a7b-4f0e-9c6a-1f2b3c4d5e6f",
		Type:     "core/article",
		Uri:      "core://article/vattenverket",
		Title:    "Vattenverket byggs ut",
		Language: "sv",
		Content: []*newsdoc.Block{
			{
				Id:   "para-1",
				Type: "core/text",
				Role: "body",
				Data: map[string]string{
					"text":   "Kommunen bygger ut vattenverket för att säkra dricksvattnet i framtiden.",
					"format": "html",
				},
			},
			{
				Id:   "para-2",
				Type: "core/text",
				Role: "body",
				Data: map[string]string{
					"text":   "Arbetet inleds i höst och beräknas pågå i två år, enligt dricksvattnet-utredningen.",
					"format": "html",
				},
			},
			{
				Id:   "para-3",
				Type: "core/text",
				Role: "body",
				Data: map[string]string{
					"text":   "Kostnaden för dricksvattnet delas mellan kommunen och regionen.",
					"format": "html",
				},
			},
		},
	}
}
