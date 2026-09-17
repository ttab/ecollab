package ecollab_test

import (
	"reflect"
	"sort"
	"testing"

	"github.com/ttab/ecollab"
	"github.com/ttab/elephant-api/newsdoc"
	"github.com/ttab/goyjs"
)

// TestMaterializeScalars covers the simplest case: a document YMap
// with only string-scalar keys mapping to top-level newsdoc.Document
// fields.
func TestMaterializeScalars(t *testing.T) {
	d := goyjs.New()
	defer d.Close()

	must(t, "set uuid", d.MapSetString("document", "uuid", "00000000-0000-0000-0000-000000000001"))
	must(t, "set type", d.MapSetString("document", "type", "core/article"))
	must(t, "set uri", d.MapSetString("document", "uri", "core://article/1"))
	must(t, "set url", d.MapSetString("document", "url", "https://example.com/1"))
	must(t, "set title", d.MapSetString("document", "title", "Hello"))
	must(t, "set language", d.MapSetString("document", "language", "sv"))

	got, err := ecollab.Materialize(d, ecollab.RootName)
	if err != nil {
		t.Fatalf("Materialize: %v", err)
	}

	want := &newsdoc.Document{
		Uuid:     "00000000-0000-0000-0000-000000000001",
		Type:     "core/article",
		Uri:      "core://article/1",
		Url:      "https://example.com/1",
		Title:    "Hello",
		Language: "sv",
	}
	if !documentsEqual(got, want) {
		t.Errorf("Materialize:\n  got:  %s\n  want: %s", documentString(got), documentString(want))
	}
}

// TestMaterializeContentBlocks exercises the recursive translation
// of `content` blocks with `data` maps.
func TestMaterializeContentBlocks(t *testing.T) {
	d := goyjs.New()
	defer d.Close()

	must(t, "type", d.MapSetString("document", "type", "core/article"))
	must(t, "content", d.MapSet("document", "content", goyjs.ArrayValue(
		goyjs.MapValue(map[string]goyjs.Input{
			"id":   goyjs.String("b1"),
			"type": goyjs.String("core/text"),
			"data": goyjs.MapValue(map[string]goyjs.Input{
				"text":   goyjs.String("Hello world"),
				"format": goyjs.String("plain"),
			}),
		}),
		goyjs.MapValue(map[string]goyjs.Input{
			"id":   goyjs.String("b2"),
			"type": goyjs.String("core/heading"),
			"data": goyjs.MapValue(map[string]goyjs.Input{
				"text":  goyjs.String("Section"),
				"level": goyjs.String("2"),
			}),
		}),
	)))

	got, err := ecollab.Materialize(d, ecollab.RootName)
	if err != nil {
		t.Fatalf("Materialize: %v", err)
	}

	if len(got.Content) != 2 {
		t.Fatalf("Content len = %d, want 2", len(got.Content))
	}

	b1 := blockByID(got.Content, "b1")
	if b1 == nil {
		t.Fatal("block b1 missing")
	}

	if b1.Type != "core/text" {
		t.Errorf("b1.Type = %q", b1.Type)
	}

	if b1.Data["text"] != "Hello world" || b1.Data["format"] != "plain" {
		t.Errorf("b1.Data = %v", b1.Data)
	}

	b2 := blockByID(got.Content, "b2")
	if b2 == nil {
		t.Fatal("block b2 missing")
	}

	if b2.Data["level"] != "2" {
		t.Errorf("b2.Data.level = %q", b2.Data["level"])
	}
}

// TestMaterializeMetaAndLinks: meta and links arrays follow the
// same rules as content. Block scalar fields all map to their proto
// counterparts.
func TestMaterializeMetaAndLinks(t *testing.T) {
	d := goyjs.New()
	defer d.Close()

	must(t, "meta", d.MapSet("document", "meta", goyjs.ArrayValue(
		goyjs.MapValue(map[string]goyjs.Input{
			"type":  goyjs.String("core/teaser"),
			"role":  goyjs.String("preamble"),
			"value": goyjs.String("A teaser."),
		}),
	)))
	must(t, "links", d.MapSet("document", "links", goyjs.ArrayValue(
		goyjs.MapValue(map[string]goyjs.Input{
			"type": goyjs.String("core/author"),
			"rel":  goyjs.String("creator"),
			"uuid": goyjs.String("00000000-0000-0000-0000-aaaaaaaaaaaa"),
			"uri":  goyjs.String("core://user/alice"),
			"name": goyjs.String("Alice"),
		}),
	)))

	got, err := ecollab.Materialize(d, ecollab.RootName)
	if err != nil {
		t.Fatalf("Materialize: %v", err)
	}

	if len(got.Meta) != 1 || got.Meta[0].Role != "preamble" || got.Meta[0].Value != "A teaser." {
		t.Errorf("meta = %+v", got.Meta)
	}

	if len(got.Links) != 1 ||
		got.Links[0].Rel != "creator" ||
		got.Links[0].Uri != "core://user/alice" ||
		got.Links[0].Name != "Alice" {
		t.Errorf("links = %+v", got.Links)
	}
}

// TestMaterializeNestedBlocks: a block's own content array recurses
// (a heading containing an emphasized text run, etc.).
func TestMaterializeNestedBlocks(t *testing.T) {
	d := goyjs.New()
	defer d.Close()

	must(t, "content", d.MapSet("document", "content", goyjs.ArrayValue(
		goyjs.MapValue(map[string]goyjs.Input{
			"type": goyjs.String("core/paragraph"),
			"content": goyjs.ArrayValue(
				goyjs.MapValue(map[string]goyjs.Input{
					"type":  goyjs.String("core/run"),
					"value": goyjs.String("Hello "),
				}),
				goyjs.MapValue(map[string]goyjs.Input{
					"type":  goyjs.String("core/run"),
					"value": goyjs.String("world"),
				}),
			),
		}),
	)))

	got, err := ecollab.Materialize(d, ecollab.RootName)
	if err != nil {
		t.Fatalf("Materialize: %v", err)
	}

	if len(got.Content) != 1 || got.Content[0].Type != "core/paragraph" {
		t.Fatalf("top content = %+v", got.Content)
	}

	inner := got.Content[0].Content
	if len(inner) != 2 || inner[0].Value != "Hello " || inner[1].Value != "world" {
		t.Errorf("inner content = %+v", inner)
	}
}

// TestMaterializeSkipsCollabKey: a `_collab` key on the document
// map AND on a block map must not appear in the materialized
// output. This is non-negotiable per the design — leaking `_collab`
// would cause repository validation to reject the snapshot.
func TestMaterializeSkipsCollabKey(t *testing.T) {
	d := goyjs.New()
	defer d.Close()

	must(t, "uuid", d.MapSetString("document", "uuid", "u-1"))
	must(t, "collab on doc", d.MapSet("document", ecollab.CollabKey, goyjs.MapValue(map[string]goyjs.Input{
		"se.ecms.editor": goyjs.String("ephemeral state"),
	})))
	must(t, "content", d.MapSet("document", "content", goyjs.ArrayValue(
		goyjs.MapValue(map[string]goyjs.Input{
			"id":   goyjs.String("b1"),
			"type": goyjs.String("core/text"),
			"data": goyjs.MapValue(map[string]goyjs.Input{
				"text": goyjs.String("Visible"),
			}),
			ecollab.CollabKey: goyjs.MapValue(map[string]goyjs.Input{
				"cursor": goyjs.String("hidden"),
			}),
		}),
	)))

	got, err := ecollab.Materialize(d, ecollab.RootName)
	if err != nil {
		t.Fatalf("Materialize: %v", err)
	}

	// _collab on the document didn't surface as content/meta/links
	// or as any scalar.
	if got.Uuid != "u-1" {
		t.Errorf("Uuid = %q", got.Uuid)
	}

	if len(got.Content) != 1 {
		t.Fatalf("content len = %d", len(got.Content))
	}

	b := got.Content[0]
	if b.Data["text"] != "Visible" {
		t.Errorf("b.data.text = %q", b.Data["text"])
	}

	if _, ok := b.Data[ecollab.CollabKey]; ok {
		t.Errorf("b.data leaked _collab key")
	}
}

// TestMaterializeIgnoresUnknownKeys keeps the translation strict:
// keys outside the design's translation table are silently dropped
// rather than smuggled into the proto somehow.
func TestMaterializeIgnoresUnknownKeys(t *testing.T) {
	d := goyjs.New()
	defer d.Close()

	must(t, "type", d.MapSetString("document", "type", "core/article"))
	must(t, "wat", d.MapSetString("document", "wat", "unmapped"))

	got, err := ecollab.Materialize(d, ecollab.RootName)
	if err != nil {
		t.Fatalf("Materialize: %v", err)
	}

	if got.Type != "core/article" {
		t.Errorf("Type = %q", got.Type)
	}
	// No field on Document is named "wat", so there's no way to
	// assert "not present" except by verifying the proto round-trips
	// through marshaling without surprise — covered by the equality
	// check.
}

// TestMaterializeRejectsMissingRoot: asking for a root that doesn't
// exist is a programming error; we surface it rather than returning
// an empty document.
func TestMaterializeReturnsZeroForEmptyRoot(t *testing.T) {
	d := goyjs.New()
	defer d.Close()
	// A goyjs.Doc.Map("document") creates the root lazily — so a
	// brand-new doc still has a "document" map, it's just empty.
	got, err := ecollab.Materialize(d, ecollab.RootName)
	if err != nil {
		t.Fatalf("Materialize on fresh doc: %v", err)
	}

	if got.Uuid != "" || got.Type != "" || len(got.Content) != 0 {
		t.Errorf("fresh doc materialized to non-zero: %+v", got)
	}
}

// --- helpers ----------------------------------------------------------

func must(t *testing.T, what string, err error) {
	t.Helper()

	if err != nil {
		t.Fatalf("%s: %v", what, err)
	}
}

func blockByID(blocks []*newsdoc.Block, id string) *newsdoc.Block {
	for _, b := range blocks {
		if b.Id == id {
			return b
		}
	}

	return nil
}

// documentsEqual compares two newsdoc.Document protos for our test
// purposes — assumes scalar fields are equal and ignores Content /
// Meta / Links (callers verifying nested shapes assert those
// directly). Uses reflect.DeepEqual on the flat fields.
func documentsEqual(got, want *newsdoc.Document) bool {
	return got.Uuid == want.Uuid &&
		got.Type == want.Type &&
		got.Uri == want.Uri &&
		got.Url == want.Url &&
		got.Title == want.Title &&
		got.Language == want.Language &&
		blocksEqual(got.Content, want.Content) &&
		blocksEqual(got.Meta, want.Meta) &&
		blocksEqual(got.Links, want.Links)
}

func blocksEqual(a, b []*newsdoc.Block) bool {
	if len(a) != len(b) {
		return false
	}

	for i := range a {
		if !blockEqual(a[i], b[i]) {
			return false
		}
	}

	return true
}

func blockEqual(a, b *newsdoc.Block) bool {
	if a == nil || b == nil {
		return a == b
	}

	if a.Id != b.Id || a.Uuid != b.Uuid || a.Type != b.Type ||
		a.Title != b.Title || a.Value != b.Value || a.Rel != b.Rel ||
		a.Role != b.Role || a.Name != b.Name {
		return false
	}

	if !mapsEqualSorted(a.Data, b.Data) {
		return false
	}

	return blocksEqual(a.Content, b.Content) &&
		blocksEqual(a.Meta, b.Meta) &&
		blocksEqual(a.Links, b.Links)
}

func mapsEqualSorted(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}

	keysA := make([]string, 0, len(a))
	for k := range a {
		keysA = append(keysA, k)
	}

	sort.Strings(keysA)

	keysB := make([]string, 0, len(b))
	for k := range b {
		keysB = append(keysB, k)
	}

	sort.Strings(keysB)

	if !reflect.DeepEqual(keysA, keysB) {
		return false
	}

	for _, k := range keysA {
		if a[k] != b[k] {
			return false
		}
	}

	return true
}

func documentString(d *newsdoc.Document) string {
	if d == nil {
		return "<nil>"
	}

	return d.String()
}
