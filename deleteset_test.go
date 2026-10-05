package ecollab_test

import (
	"bytes"
	"reflect"
	"testing"

	"github.com/ttab/ecollab"
	"github.com/ttab/goyjs"
)

// richDoc writes every kind of item content goyjs can produce, then
// deletes some of it, so its encoded state exercises the struct
// walker over all of them and ends in a non-trivial delete set.
func richDoc(t *testing.T) *goyjs.Doc {
	t.Helper()

	doc := goyjs.New()
	t.Cleanup(doc.Close)

	err := doc.Write(func(w *goyjs.WriteTxn) error {
		m := doc.Map("m")
		m.Set(w, "s", goyjs.String("a string"))
		m.Set(w, "n", goyjs.Null())
		m.Set(w, "b", goyjs.Bool(true))
		m.Set(w, "i", goyjs.Int(-1234567))
		m.Set(w, "f", goyjs.Float(3.25))
		m.Set(w, "bytes", goyjs.Bytes([]byte{1, 2, 3}))
		m.Set(w, "json", goyjs.JSONMap(map[string]goyjs.Input{
			"nested": goyjs.JSONArray(goyjs.Float(1), goyjs.String("x"),
				goyjs.JSONMap(map[string]goyjs.Input{"deep": goyjs.Bool(false)})),
		}))
		m.Set(w, "map", goyjs.MapValue(map[string]goyjs.Input{
			"inner": goyjs.String("v"),
		}))
		m.Set(w, "arr", goyjs.ArrayValue(goyjs.String("one"), goyjs.Float(2)))

		a := doc.Array("a")
		a.Push(w, goyjs.String("first"))
		a.Push(w, goyjs.Bytes([]byte("bin")))
		a.Push(w, goyjs.TextValue(goyjs.Delta{goyjs.Insert("in text", nil)}))

		txt := doc.Text("t")
		txt.InsertWithAttributes(w, 0, "Hello, world",
			goyjs.Attrs{"bold": goyjs.Bool(true)})
		txt.InsertEmbed(w, 5, goyjs.JSONMap(map[string]goyjs.Input{
			"image": goyjs.String("x.png"),
		}), nil)
		txt.Format(w, 0, 3, goyjs.Attrs{"italic": goyjs.Bool(true)})

		frag := doc.XMLFragment("x")
		frag.PushChild(w, goyjs.XMLElementValue("p",
			goyjs.Attrs{"class": goyjs.String("lead")},
			goyjs.XMLTextValue(nil, goyjs.Delta{goyjs.Insert("para", nil)})))

		return nil
	})
	if err != nil {
		t.Fatalf("write the document: %v", err)
	}

	err = doc.Write(func(w *goyjs.WriteTxn) error {
		doc.Map("m").Set(w, "s", goyjs.String("overwritten"))
		doc.Map("m").Delete(w, "b")
		doc.Text("t").Delete(w, 1, 4)
		doc.Array("a").Delete(w, 0, 1)

		return nil
	})
	if err != nil {
		t.Fatalf("delete from the document: %v", err)
	}

	return doc
}

// TestUpdateV1DeleteSetWalksEveryContentKind: the delete set read off
// the end of a full state, after walking every struct in it, is the
// one the structs-free encoding carries.
func TestUpdateV1DeleteSetWalksEveryContentKind(t *testing.T) {
	doc := richDoc(t)

	hasStructs, full, err := ecollab.UpdateV1DeleteSet(doc.EncodeStateV1())
	if err != nil {
		t.Fatalf("read the full state: %v", err)
	}

	if !hasStructs {
		t.Error("the full state reads as carrying no structs")
	}

	bareStructs, bare, err := ecollab.UpdateV1DeleteSet(doc.EncodeDiffV1(doc.StateVectorV1()))
	if err != nil || bareStructs {
		t.Fatalf("read the structs-free encoding: structs %v, %v", bareStructs, err)
	}

	if len(full) == 0 {
		t.Fatal("the document's delete set is empty")
	}

	if !reflect.DeepEqual(full, bare) {
		t.Errorf("delete set from the full state %v, from the bare encoding %v",
			full, bare)
	}
}

// TestDiffAgainstACaughtUpPeerCarriesItsDeleteSet is the reason the
// delete set is tracked at all: a peer that holds everything still
// gets a diff that is not the empty update, and what it carries is
// deletions the peer already has.
func TestDiffAgainstACaughtUpPeerCarriesItsDeleteSet(t *testing.T) {
	a := goyjs.New()
	t.Cleanup(a.Close)

	if err := a.MapSetString("root", "title", "one"); err != nil {
		t.Fatal(err)
	}

	if err := a.MapSetString("root", "title", "two"); err != nil {
		t.Fatal(err)
	}

	b := goyjs.New()
	t.Cleanup(b.Close)

	if err := b.ApplyUpdateV1(a.EncodeStateV1()); err != nil {
		t.Fatal(err)
	}

	diff := a.EncodeDiffV1(b.StateVectorV1())
	if bytes.Equal(diff, []byte{0, 0}) {
		t.Fatal("the diff is the empty update; the premise no longer holds")
	}

	hasStructs, ds, err := ecollab.UpdateV1DeleteSet(diff)
	if err != nil {
		t.Fatal(err)
	}

	if hasStructs {
		t.Error("the diff to a caught-up peer carries structs")
	}

	if len(ds) == 0 {
		t.Error("the diff carries no deletions")
	}

	_, held, err := ecollab.UpdateV1DeleteSet(b.EncodeDiffV1(b.StateVectorV1()))
	if err != nil {
		t.Fatal(err)
	}

	if extra := ecollab.SubtractDeleteSets(ds, held); len(extra) != 0 {
		t.Errorf("the diff deletes %v beyond what the peer holds", extra)
	}
}

func TestDeleteSetSubtract(t *testing.T) {
	a := ecollab.TestDeleteSet{1: {{0, 10}, {20, 5}}, 2: {{0, 3}}}
	b := ecollab.TestDeleteSet{1: {{2, 3}, {8, 14}}, 2: {{0, 3}}}

	want := ecollab.TestDeleteSet{1: {{0, 2}, {5, 3}, {22, 3}}}

	if got := ecollab.SubtractDeleteSets(a, b); !reflect.DeepEqual(got, want) {
		t.Errorf("subtract = %v, want %v", got, want)
	}

	merged := ecollab.NormalizeDeleteSet(ecollab.TestDeleteSet{7: {{5, 5}, {0, 5}, {8, 4}}})

	if want := (ecollab.TestDeleteSet{7: {{0, 12}}}); !reflect.DeepEqual(merged, want) {
		t.Errorf("merged = %v, want %v", merged, want)
	}
}

func TestUpdateV1DeleteSetRefusesGarbage(t *testing.T) {
	update := richDoc(t).EncodeStateV1()

	for _, bad := range [][]byte{
		{},
		update[:len(update)/2],
		append(append([]byte(nil), update...), 0),
	} {
		if _, _, err := ecollab.UpdateV1DeleteSet(bad); err == nil {
			t.Errorf("read %d bytes of garbage without an error", len(bad))
		}
	}
}
