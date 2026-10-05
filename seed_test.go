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

	update, err := ecollab.BuildSeedUpdate(original, ecollab.RootName, testLineage)
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

	update, err := ecollab.BuildSeedUpdate(original, ecollab.RootName, testLineage)
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
	_, err := ecollab.BuildSeedUpdate(nil, ecollab.RootName, testLineage)
	if err == nil {
		t.Fatal("BuildSeedUpdate(nil) succeeded; want error")
	}
}

func TestBuildSeedUpdateEmptyRootErrors(t *testing.T) {
	_, err := ecollab.BuildSeedUpdate(
		&newsdoc.Document{Uuid: "x"}, "", testLineage)
	if err == nil {
		t.Fatal("BuildSeedUpdate with empty root succeeded; want error")
	}
}

// testLineage is the lineage the tests seed with: a ULID, as the
// service mints one.
const testLineage = "01K6H9Z3QJ8M5V2X4N7P0R1S2T"

func TestBuildSeedUpdateEmptyLineageErrors(t *testing.T) {
	_, err := ecollab.BuildSeedUpdate(
		&newsdoc.Document{Uuid: "x"}, ecollab.RootName, "")
	if err == nil {
		t.Fatal("BuildSeedUpdate with empty lineage succeeded; want error")
	}
}

func TestBuildSeedUpdateLineageRootAsDocumentRootErrors(t *testing.T) {
	_, err := ecollab.BuildSeedUpdate(
		&newsdoc.Document{Uuid: "x"}, ecollab.LineageRootName, testLineage)
	if err == nil {
		t.Fatal("BuildSeedUpdate into the lineage root succeeded; want error")
	}
}

// TestBuildSeedUpdateWritesLineage holds the seed to writing the
// lineage where Lineage, and a browser's
// doc.getMap(LineageRootName).get(LineageKey), read it.
func TestBuildSeedUpdateWritesLineage(t *testing.T) {
	doc := seededDoc(t, sampleDocument(), testLineage)

	got, ok := ecollab.Lineage(doc)
	if !ok {
		t.Fatal("Lineage found no lineage in a seeded doc")
	}

	if got != testLineage {
		t.Errorf("lineage = %q, want %q", got, testLineage)
	}

	txn := doc.NewReadTxn()
	defer txn.Commit()

	v, ok := doc.Map(ecollab.LineageRootName).Get(txn, ecollab.LineageKey)
	if !ok || v.Kind() != goyjs.KindString || v.String() != testLineage {
		t.Errorf("lineage root holds %v (present %v), want the string %q",
			v, ok, testLineage)
	}
}

// TestLineageSurvivesTheEncodedState is the resume path in miniature:
// the state a session ends with, applied to a fresh Y.Doc, carries
// the lineage it was seeded with, because the lineage is content.
func TestLineageSurvivesTheEncodedState(t *testing.T) {
	seeded := seededDoc(t, sampleDocument(), testLineage)

	resumed := goyjs.New()
	defer resumed.Close()

	if err := resumed.ApplyUpdateV2(seeded.EncodeStateV2()); err != nil {
		t.Fatalf("ApplyUpdateV2: %v", err)
	}

	got, ok := ecollab.Lineage(resumed)
	if !ok || got != testLineage {
		t.Errorf("lineage = %q (present %v), want %q", got, ok, testLineage)
	}
}

func TestLineageAbsent(t *testing.T) {
	doc := goyjs.New()
	defer doc.Close()

	if got, ok := ecollab.Lineage(doc); ok {
		t.Errorf("Lineage of an empty doc = %q, want none", got)
	}

	if got, ok := ecollab.Lineage(nil); ok {
		t.Errorf("Lineage of nil = %q, want none", got)
	}
}

func TestLineageOfTheWrongKind(t *testing.T) {
	doc := goyjs.New()
	defer doc.Close()

	err := doc.MapSet(ecollab.LineageRootName, ecollab.LineageKey,
		goyjs.MapValue(map[string]goyjs.Input{"id": goyjs.String("x")}))
	if err != nil {
		t.Fatalf("MapSet: %v", err)
	}

	if got, ok := ecollab.Lineage(doc); ok {
		t.Errorf("Lineage of a map value = %q, want none", got)
	}
}

// TestMaterializeNeverReadsTheLineageRoot proves the lineage root is
// outside the tree contract. The lineage root is filled with every
// key Materialize translates, holding values that would change the
// NewsDoc if Materialize read them, and the NewsDoc does not change.
// A doc holding only a populated lineage root materializes as empty.
func TestMaterializeNeverReadsTheLineageRoot(t *testing.T) {
	original := sampleDocument()
	doc := seededDoc(t, original, testLineage)

	block := goyjs.MapValue(map[string]goyjs.Input{
		"id":   goyjs.String("from-the-lineage-root"),
		"type": goyjs.String("core/text"),
		"data": goyjs.MapValue(map[string]goyjs.Input{
			"text": goyjs.String("must not appear"),
		}),
	})

	decoys := map[string]goyjs.Input{
		"uuid":     goyjs.String("decoy-uuid"),
		"type":     goyjs.String("decoy/type"),
		"uri":      goyjs.String("decoy://uri"),
		"url":      goyjs.String("https://decoy.example"),
		"title":    goyjs.String("decoy title"),
		"language": goyjs.String("xx"),
		"content":  goyjs.ArrayValue(block),
		"meta":     goyjs.ArrayValue(block),
		"links":    goyjs.ArrayValue(block),
	}

	for k, v := range decoys {
		if err := doc.MapSet(ecollab.LineageRootName, k, v); err != nil {
			t.Fatalf("MapSet %s: %v", k, err)
		}
	}

	got, err := ecollab.Materialize(doc, ecollab.RootName)
	if err != nil {
		t.Fatalf("Materialize: %v", err)
	}

	if !proto.Equal(original, got) {
		t.Errorf("the lineage root reached the NewsDoc:\nwant: %+v\ngot:  %+v",
			original, got)
	}

	// The same decoys with no document root beside them: Materialize
	// finds an empty document root and nothing else.
	lone := goyjs.New()
	defer lone.Close()

	for k, v := range decoys {
		if err := lone.MapSet(ecollab.LineageRootName, k, v); err != nil {
			t.Fatalf("MapSet %s: %v", k, err)
		}
	}

	empty, err := ecollab.Materialize(lone, ecollab.RootName)
	if err != nil {
		t.Fatalf("Materialize: %v", err)
	}

	if !proto.Equal(&newsdoc.Document{}, empty) {
		t.Errorf("a doc with only a lineage root materialized as %+v", empty)
	}
}

// TestSeedLineagesDoNotChangeTheNewsDoc seeds the same NewsDoc under
// two lineages and gets the same NewsDoc back from each.
func TestSeedLineagesDoNotChangeTheNewsDoc(t *testing.T) {
	original := sampleDocument()

	a, err := ecollab.Materialize(
		seededDoc(t, original, testLineage), ecollab.RootName)
	if err != nil {
		t.Fatalf("Materialize: %v", err)
	}

	b, err := ecollab.Materialize(
		seededDoc(t, original, "01K6HA0000000000000000000Z"), ecollab.RootName)
	if err != nil {
		t.Fatalf("Materialize: %v", err)
	}

	if !proto.Equal(a, b) || !proto.Equal(original, a) {
		t.Errorf("lineage changed the NewsDoc:\nwant: %+v\na:    %+v\nb:    %+v",
			original, a, b)
	}
}

func sampleDocument() *newsdoc.Document {
	return &newsdoc.Document{
		Uuid:     "a06f9a4e-1234-4321-aaaa-aaaaaaaaaaaa",
		Type:     "core/article",
		Title:    "An article",
		Language: "sv",
		Content: []*newsdoc.Block{
			{
				Id:   "para-1",
				Type: "core/text",
				Data: map[string]string{"text": "Hello world."},
			},
		},
		Meta: []*newsdoc.Block{
			{Type: "core/byline", Data: map[string]string{"name": "Reporter"}},
		},
		Links: []*newsdoc.Block{
			{Type: "core/category", Rel: "subject", Uuid: "abc"},
		},
	}
}

func seededDoc(
	t *testing.T, original *newsdoc.Document, lineage string,
) *goyjs.Doc {
	t.Helper()

	update, err := ecollab.BuildSeedUpdate(original, ecollab.RootName, lineage)
	if err != nil {
		t.Fatalf("BuildSeedUpdate: %v", err)
	}

	doc := goyjs.New()
	t.Cleanup(doc.Close)

	if err := doc.ApplyUpdateV1(update); err != nil {
		t.Fatalf("ApplyUpdateV1: %v", err)
	}

	return doc
}
