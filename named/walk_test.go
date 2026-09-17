package named_test

import (
	"encoding/json"
	"os"
	"testing"

	"github.com/ttab/ecollab"
	"github.com/ttab/ecollab/named"
	"github.com/ttab/goyjs"
)

// TestWalkToJSONRichTypeTolerant verifies that a named doc containing a
// rich Yjs type (a nested YText) materializes best-effort rather than
// erroring: the YText renders as its plain string and ordinary content is
// unaffected. The fixture is a v2 update produced by yjs (regenerate via
// goyjs's node helper with a Y.Text under the "document" root); the Go
// write API can't create rich types.
func TestWalkToJSONRichTypeTolerant(t *testing.T) {
	raw, err := os.ReadFile("testdata/rich_document_v2.bin")
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}

	doc := goyjs.New()
	defer doc.Close()

	if err := doc.ApplyUpdateV2(raw); err != nil {
		t.Fatalf("apply fixture: %v", err)
	}

	out, err := named.WalkToJSON(doc, ecollab.RootName)
	if err != nil {
		t.Fatalf("WalkToJSON: %v", err)
	}

	var got map[string]any
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if got["title"] != "a title" {
		t.Errorf("title = %v, want %q", got["title"], "a title")
	}

	if got["body"] != "hello rich" {
		t.Errorf("body (YText) = %v, want %q", got["body"], "hello rich")
	}

	meta, ok := got["meta"].(map[string]any)
	if !ok {
		t.Fatalf("meta = %v, want map", got["meta"])
	}

	if meta["lang"] != "en" {
		t.Errorf("meta.lang = %v, want %q", meta["lang"], "en")
	}
}

// TestWalkToJSONPlainDoc confirms an ordinary YMap/YArray/string doc — the
// conventional named-doc shape, built with the Go write API — walks to the
// expected JSON and is unaffected by the rich-type handling.
func TestWalkToJSONPlainDoc(t *testing.T) {
	doc := goyjs.New()
	defer doc.Close()

	if err := doc.MapSetString(ecollab.RootName, "name", "bookmarks"); err != nil {
		t.Fatalf("MapSetString: %v", err)
	}

	if err := doc.MapSet(ecollab.RootName, "tags", goyjs.ArrayValue(
		goyjs.String("a"), goyjs.String("b"),
	)); err != nil {
		t.Fatalf("MapSet array: %v", err)
	}

	out, err := named.WalkToJSON(doc, ecollab.RootName)
	if err != nil {
		t.Fatalf("WalkToJSON: %v", err)
	}

	var got map[string]any
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if got["name"] != "bookmarks" {
		t.Errorf("name = %v, want %q", got["name"], "bookmarks")
	}

	tags, ok := got["tags"].([]any)
	if !ok || len(tags) != 2 || tags[0] != "a" || tags[1] != "b" {
		t.Errorf("tags = %v, want [a b]", got["tags"])
	}
}
