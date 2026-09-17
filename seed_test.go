package ecollab_test

import (
	"testing"

	"github.com/ttab/ecollab"
	"github.com/ttab/elephant-api/newsdoc"
	"github.com/ttab/goyjs"
	"google.golang.org/protobuf/proto"
)

// TestBuildSeedUpdateRoundTrip seeds a Y.Doc with a representative
// NewsDoc, applies the update to a fresh Y.Doc, materialises it
// back and asserts the recovered NewsDoc matches the input. This is
// the reason the two directions share a package: neither side of
// the tree contract can move without the other noticing.
func TestBuildSeedUpdateRoundTrip(t *testing.T) {
	original := &newsdoc.Document{
		Uuid:     "a06f9a4e-1234-4321-aaaa-aaaaaaaaaaaa",
		Type:     "core/article",
		Uri:      "core://article/foo",
		Title:    "An article",
		Language: "sv",
		Content: []*newsdoc.Block{
			{
				Id:   "para-1",
				Type: "core/text",
				Role: "body",
				Data: map[string]string{
					"text":   "Hello world.",
					"format": "html",
				},
			},
			{
				Id:   "para-2",
				Type: "core/text",
				Data: map[string]string{
					"text": "A second paragraph.",
				},
			},
		},
		Meta: []*newsdoc.Block{
			{Type: "core/byline", Data: map[string]string{"name": "Reporter"}},
		},
		Links: []*newsdoc.Block{
			{Type: "core/category", Rel: "subject", Uuid: "abc"},
		},
	}

	update, err := ecollab.BuildSeedUpdate(original, ecollab.RootName)
	if err != nil {
		t.Fatalf("BuildSeedUpdate: %v", err)
	}

	if len(update) == 0 {
		t.Fatal("BuildSeedUpdate returned an empty update")
	}

	doc := goyjs.New()
	defer doc.Close()

	if err := doc.ApplyUpdateV1(update); err != nil {
		t.Fatalf("ApplyUpdateV1: %v", err)
	}

	got, err := ecollab.Materialize(doc, ecollab.RootName)
	if err != nil {
		t.Fatalf("Materialize: %v", err)
	}

	if !proto.Equal(original, got) {
		t.Errorf("round-trip mismatch:\nwant: %+v\ngot:  %+v", original, got)
	}
}

// TestBuildSeedUpdateEmptyDoc covers the "brand-new doc" case: the
// repository returns a minimal NewsDoc and BuildSeedUpdate produces
// a valid update (not nil/empty) that round-trips to an equivalent
// empty NewsDoc on the other end.
func TestBuildSeedUpdateEmptyDoc(t *testing.T) {
	original := &newsdoc.Document{
		Uuid: "00000000-0000-0000-0000-000000000000",
		Type: "core/article",
	}

	update, err := ecollab.BuildSeedUpdate(original, ecollab.RootName)
	if err != nil {
		t.Fatalf("BuildSeedUpdate: %v", err)
	}

	doc := goyjs.New()
	defer doc.Close()

	if err := doc.ApplyUpdateV1(update); err != nil {
		t.Fatalf("ApplyUpdateV1: %v", err)
	}

	got, err := ecollab.Materialize(doc, ecollab.RootName)
	if err != nil {
		t.Fatalf("Materialize: %v", err)
	}

	if got.Uuid != original.Uuid {
		t.Errorf("uuid = %q, want %q", got.Uuid, original.Uuid)
	}

	if got.Type != original.Type {
		t.Errorf("type = %q, want %q", got.Type, original.Type)
	}
}

func TestBuildSeedUpdateNilDocErrors(t *testing.T) {
	_, err := ecollab.BuildSeedUpdate(nil, ecollab.RootName)
	if err == nil {
		t.Fatal("BuildSeedUpdate(nil) succeeded; want error")
	}
}

func TestBuildSeedUpdateEmptyRootErrors(t *testing.T) {
	_, err := ecollab.BuildSeedUpdate(&newsdoc.Document{Uuid: "x"}, "")
	if err == nil {
		t.Fatal("BuildSeedUpdate with empty root succeeded; want error")
	}
}
