package ecollab_test

import (
	"encoding/json"
	"strconv"
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
// each text block under _collab; a human is focused on one of the three
// blocks and says so through awareness. The agent reads the remaining
// blocks, finds something worth remarking on, and attaches a comment to
// the exact run of text it found - without touching the block the human
// is in, and without disturbing anything the human typed.
//
// The analysis here is a fixed-string search. A real agent would put a
// model behind that step and nothing else about the shape would change:
// what it produces is a range and a thread id, and the range is what
// this file shows how to annotate.

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
)

func TestAgentCommentsOnTheBlocksNobodyIsEditing(t *testing.T) {
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
	//    block under _collab. The agent does not do this - it is the
	//    authoring application's protocol - but a test has to stand in
	//    for the editor that would have done it.
	hydrateAsTheEditorWould(t, doc)

	// 3. The human says where they are. Awareness carries a "focus"
	//    field holding the yjs path of the value being edited, which is
	//    what the editor publishes and what other participants match
	//    on.
	human := goyjs.NewAwareness(1001)

	err = human.SetLocalStateField("focus", map[string]any{
		"key":  "content[1].data.text",
		"path": "content[1].data.text",
	})
	if err != nil {
		t.Fatalf("set the human's focus: %v", err)
	}

	agent := goyjs.NewAwareness(2002)
	if err := agent.ApplyUpdate(human.Encode(), nil); err != nil {
		t.Fatalf("apply the human's awareness: %v", err)
	}

	busy := focusedPaths(t, agent)

	if _, ok := busy["content[1].data.text"]; !ok {
		t.Fatalf("the agent did not see the human's focus, saw %v", busy)
	}

	// 4. and 5. Read every block the human is not in, find the phrase,
	//    and attach the comment to exactly that run.
	commented := annotate(t, doc, busy)

	if want := []string{"content[0].data.text", "content[2].data.text"}; !equal(commented, want) {
		t.Errorf("commented on %v, want %v", commented, want)
	}

	// The annotated runs render as the comment element wrapping the
	// matched text, which is what a peer reading the document sees.
	for _, path := range commented {
		got := renderBlock(t, doc, blockIndexOf(t, path))

		t.Logf("%s -> %s", path, got)

		want := `<comment thread="` + threadID + `">` + phrase + `</comment>`
		if !strings.Contains(got, want) {
			t.Errorf("block %s rendered %q, want it to contain %q", path, got, want)
		}
	}

	// The block the human is editing is untouched: no comment, and the
	// text is exactly as it was.
	busyText := renderBlock(t, doc, 1)

	if strings.Contains(busyText, "<comment") {
		t.Errorf("the agent annotated the block the human is editing: %q", busyText)
	}

	if !strings.Contains(busyText, phrase) {
		t.Errorf("the busy block lost its text: %q", busyText)
	}
}

// annotate is the agent's own work: for every block the human is not
// focused on, find the phrase and attach a comment thread to that run.
// Every block is annotated in one write scope, so a peer sees all of
// the agent's comments or none of them.
func annotate(t *testing.T, doc *goyjs.Doc, busy map[string]struct{}) []string {
	t.Helper()

	var commented []string

	err := doc.Write(func(w *goyjs.WriteTxn) error {
		read := w.ReadTxn()

		blocks, ok := doc.Map(ecollab.RootName).Get(read, "content")
		if !ok {
			t.Fatal("the document has no content blocks")
		}

		blocks.Array().ForEach(read, func(i int, block goyjs.Value) bool {
			path := blockPath(i)

			if _, editing := busy[path]; editing {
				return true
			}

			body, ok := ecollab.CollabOn(block.Map()).FieldNode(read, editorApp, bodyField)
			if !ok {
				return true
			}

			value, ok := body.Value(read)
			if !ok {
				return true
			}

			// The analysis step. A real agent reasons over the text
			// here; what it has to come back with is an offset and a
			// length, and those are in UTF-16 code units because that
			// is how yjs counts - which is why the offset is
			// accumulated with UTF16Len rather than with len().
			offset, found := findRun(value, phrase)
			if !found {
				return true
			}

			body.Format(w, offset, goyjs.UTF16Len(phrase), goyjs.Attrs{
				"comment": goyjs.JSONMap(map[string]goyjs.Input{
					"thread": goyjs.String(threadID),
				}),
			})

			commented = append(commented, path)

			return true
		})

		return nil
	})
	if err != nil {
		t.Fatalf("attach the comments: %v", err)
	}

	return commented
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

// focusedPaths reads the awareness states and returns the yjs paths
// other participants are focused on. A state with a null focus is a
// participant who is present but not in a field.
func focusedPaths(t *testing.T, a *goyjs.Awareness) map[string]struct{} {
	t.Helper()

	out := map[string]struct{}{}

	for clientID, raw := range a.States() {
		if clientID == a.ClientID() {
			continue
		}

		var state struct {
			Focus *struct {
				Key  string `json:"key"`
				Path string `json:"path"`
			} `json:"focus"`
		}

		if err := json.Unmarshal(raw, &state); err != nil {
			t.Fatalf("decode the awareness state of %d: %v", clientID, err)
		}

		if state.Focus == nil || state.Focus.Path == "" {
			continue
		}

		out[state.Focus.Path] = struct{}{}
	}

	return out
}

// hydrateAsTheEditorWould stands in for the editing application, which
// creates the editable YXmlText for each block under _collab and copies
// the NewsDoc text into it. EnsureField creates every level that is
// missing and leaves an existing value alone, so the editor and the
// agent racing to hydrate the same field cannot produce two of them on
// this document.
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
				goyjs.XMLTextValue(nil, goyjs.Delta{
					goyjs.Insert(text.String(), nil),
				}))
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

// renderBlock returns the rendered XML of a block's editable body.
func renderBlock(t *testing.T, doc *goyjs.Doc, index int) string {
	t.Helper()

	read := doc.NewReadTxn()
	defer read.Commit()

	blocks, ok := doc.Map(ecollab.RootName).Get(read, "content")
	if !ok {
		t.Fatal("the document has no content blocks")
	}

	block, ok := blocks.Array().Get(read, index)
	if !ok {
		t.Fatalf("no block at index %d", index)
	}

	value, ok := ecollab.CollabOn(block.Map()).Field(read, editorApp, bodyField)
	if !ok {
		t.Fatalf("block %d has no editable body", index)
	}

	s, _ := value.RichString()

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

func blockPath(i int) string {
	return "content[" + strconv.Itoa(i) + "].data.text"
}

func blockIndexOf(t *testing.T, path string) int {
	t.Helper()

	for i := range 3 {
		if blockPath(i) == path {
			return i
		}
	}

	t.Fatalf("no block index for path %q", path)

	return -1
}

func equal(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}

	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}

	return true
}
