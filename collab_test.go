package ecollab_test

import (
	"errors"
	"fmt"
	"testing"

	"github.com/ttab/ecollab"
	"github.com/ttab/goyjs"
)

const testApp = "se.ecms.article-editor"

// seedDelta is the shape an editable field holds: one embedded
// Y.XmlText block carrying its properties as node attributes, with the
// text run inside it.
func seedDelta(id, text string) goyjs.Delta {
	block := goyjs.XMLTextValue(
		goyjs.Attrs{
			"type": goyjs.String("core/text"),
			"id":   goyjs.String(id),
		},
		goyjs.Delta{goyjs.Insert(text, nil)},
	)

	return goyjs.Delta{goyjs.Embed(block, nil)}
}

// ensureField runs one EnsureField as its own write scope, which is
// how a caller with nothing else to write in the same transaction
// uses it.
func ensureField(
	d *goyjs.Doc, c *ecollab.Collab, field string, value goyjs.Input,
) (bool, error) {
	var created bool

	err := d.Write(func(w *goyjs.WriteTxn) error {
		var err error

		created, err = c.EnsureField(w, testApp, field, value)
		if err != nil {
			return fmt.Errorf("ensure %q: %w", field, err)
		}

		return nil
	})
	if err != nil {
		return false, fmt.Errorf("write scope: %w", err)
	}

	return created, nil
}

// TestEnsureFieldCreatesEveryLevel: a document that has never seen
// _collab gets the key, the application map and the field in one
// write, and the field reads back as the rich value it was given.
func TestEnsureFieldCreatesEveryLevel(t *testing.T) {
	d := goyjs.New()
	defer d.Close()

	collab := ecollab.CollabOn(d.Map(ecollab.RootName))

	created, err := ensureField(d, collab, "body",
		goyjs.XMLTextValue(nil, seedDelta("b1", "Räksmörgås")))
	if err != nil {
		t.Fatalf("ensure body: %v", err)
	}

	if !created {
		t.Error("EnsureField reported no create on an empty document")
	}

	txn := d.NewReadTxn()
	defer txn.Commit()

	if _, ok := collab.Map(txn); !ok {
		t.Fatal("no _collab map after EnsureField")
	}

	if _, ok := collab.App(txn, testApp); !ok {
		t.Fatal("no application map after EnsureField")
	}

	v, ok := collab.Field(txn, testApp, "body")
	if !ok {
		t.Fatal("no body field after EnsureField")
	}

	if v.Kind() != goyjs.KindXMLText {
		t.Fatalf("body is a %s, want xml_text", v.Kind())
	}

	runs := v.Delta()
	if len(runs) != 1 || runs[0].Embed == nil {
		t.Fatalf("body delta is %d runs, want one embed", len(runs))
	}

	block := *runs[0].Embed

	id, ok := block.Attribute("id")
	if !ok || id.String() != "b1" {
		t.Errorf("block id is %q, want b1", id.String())
	}

	if got := block.Len(); got != goyjs.UTF16Len("Räksmörgås") {
		t.Errorf("block length is %d UTF-16 units, want %d",
			got, goyjs.UTF16Len("Räksmörgås"))
	}
}

// TestEnsureFieldLeavesAnExistingValue: a second EnsureField for the
// same field creates nothing and does not disturb what is there.
func TestEnsureFieldLeavesAnExistingValue(t *testing.T) {
	d := goyjs.New()
	defer d.Close()

	collab := ecollab.CollabOn(d.Map(ecollab.RootName))

	created, err := ensureField(d, collab, "title",
		goyjs.XMLTextValue(nil, seedDelta("t1", "First")))
	if err != nil {
		t.Fatalf("ensure title: %v", err)
	}

	if !created {
		t.Fatal("the first EnsureField reported no create")
	}

	created, err = ensureField(d, collab, "title",
		goyjs.XMLTextValue(nil, seedDelta("t1", "Second")))
	if err != nil {
		t.Fatalf("ensure title again: %v", err)
	}

	if created {
		t.Error("the second EnsureField created over an existing field")
	}

	txn := d.NewReadTxn()
	defer txn.Commit()

	v, ok := collab.Field(txn, testApp, "title")
	if !ok {
		t.Fatal("title is gone")
	}

	block := *v.Delta()[0].Embed
	if got, _ := block.RichString(); got != "First" {
		t.Errorf("title reads %q, want the value the first write put there", got)
	}
}

// TestEnsureFieldFillsInBelowAnExistingLevel: _collab that exists
// without the application, and an application that exists without the
// field, are both filled in rather than replaced.
func TestEnsureFieldFillsInBelowAnExistingLevel(t *testing.T) {
	d := goyjs.New()
	defer d.Close()

	collab := ecollab.CollabOn(d.Map(ecollab.RootName))

	// Another application is already there, so _collab exists and this
	// application's map does not.
	err := d.MapSet(ecollab.RootName, ecollab.CollabKey,
		goyjs.MapValue(map[string]goyjs.Input{
			"se.ecms.other": goyjs.MapValue(map[string]goyjs.Input{
				"note": goyjs.String("keep me"),
			}),
		}))
	if err != nil {
		t.Fatalf("seed another application: %v", err)
	}

	for _, field := range []string{"body", "title"} {
		created, err := ensureField(d, collab, field,
			goyjs.XMLTextValue(nil, seedDelta("x", field)))
		if err != nil {
			t.Fatalf("ensure %s: %v", field, err)
		}

		if !created {
			t.Errorf("EnsureField reported no create for %q", field)
		}
	}

	txn := d.NewReadTxn()
	defer txn.Commit()

	for _, field := range []string{"body", "title"} {
		if _, ok := collab.FieldNode(txn, testApp, field); !ok {
			t.Errorf("no node for %q", field)
		}
	}

	other, ok := collab.App(txn, "se.ecms.other")
	if !ok {
		t.Fatal("the other application's map is gone")
	}

	note, ok := other.Get(txn, "note")
	if !ok || note.String() != "keep me" {
		t.Error("the other application's state did not survive")
	}
}

// TestEnsureFieldRefusesAForeignShape: a _collab or application key
// holding something that is not a map is reported rather than
// overwritten.
func TestEnsureFieldRefusesAForeignShape(t *testing.T) {
	cases := []struct {
		name  string
		value goyjs.Input
	}{
		{
			name:  "collab key holds a string",
			value: goyjs.String("not a map"),
		},
		{
			name: "application key holds an array",
			value: goyjs.MapValue(map[string]goyjs.Input{
				testApp: goyjs.ArrayValue(goyjs.String("nope")),
			}),
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			d := goyjs.New()
			defer d.Close()

			collab := ecollab.CollabOn(d.Map(ecollab.RootName))

			if err := d.MapSet(ecollab.RootName, ecollab.CollabKey, c.value); err != nil {
				t.Fatalf("seed: %v", err)
			}

			_, err := ensureField(d, collab, "body",
				goyjs.XMLTextValue(nil, nil))
			if !errors.Is(err, ecollab.ErrCollabShape) {
				t.Fatalf("got %v, want ErrCollabShape", err)
			}
		})
	}
}

// TestCollabReadsReportAbsence: every accessor answers false on a
// document that holds nothing, and on one whose owner map is nil.
func TestCollabReadsReportAbsence(t *testing.T) {
	d := goyjs.New()
	defer d.Close()

	collab := ecollab.CollabOn(d.Map(ecollab.RootName))

	txn := d.NewReadTxn()
	defer txn.Commit()

	if _, ok := collab.Map(txn); ok {
		t.Error("Map found a _collab map on an empty document")
	}

	if _, ok := collab.App(txn, testApp); ok {
		t.Error("App found an application map on an empty document")
	}

	if _, ok := collab.Field(txn, testApp, "body"); ok {
		t.Error("Field found a field on an empty document")
	}

	if _, ok := collab.FieldNode(txn, testApp, "body"); ok {
		t.Error("FieldNode found a node on an empty document")
	}

	if collab.Hydrated(txn, testApp, "body") {
		t.Error("Hydrated reported a marker on an empty document")
	}

	nilOwner := ecollab.CollabOn(nil)
	if _, ok := nilOwner.Map(txn); ok {
		t.Error("Map answered for a nil owner")
	}
}

// TestFieldNodeRefusesAScalar: a field holding a plain string has no
// node to edit, and FieldNode says so rather than handing back a
// handle that cannot be written.
func TestFieldNodeRefusesAScalar(t *testing.T) {
	d := goyjs.New()
	defer d.Close()

	collab := ecollab.CollabOn(d.Map(ecollab.RootName))

	err := d.MapSet(ecollab.RootName, ecollab.CollabKey,
		goyjs.MapValue(map[string]goyjs.Input{
			testApp: goyjs.MapValue(map[string]goyjs.Input{
				"body": goyjs.String("plain"),
			}),
		}))
	if err != nil {
		t.Fatalf("seed: %v", err)
	}

	txn := d.NewReadTxn()
	defer txn.Commit()

	if _, ok := collab.Field(txn, testApp, "body"); !ok {
		t.Fatal("Field did not find the string value")
	}

	if _, ok := collab.FieldNode(txn, testApp, "body"); ok {
		t.Error("FieldNode handed back a node for a string value")
	}
}

// TestHydratedReadsTheApplicationsMarkers: the marker map is the
// application's, and Hydrated reports exactly what is in it.
func TestHydratedReadsTheApplicationsMarkers(t *testing.T) {
	d := goyjs.New()
	defer d.Close()

	collab := ecollab.CollabOn(d.Map(ecollab.RootName))

	err := d.MapSet(ecollab.RootName, ecollab.CollabKey,
		goyjs.MapValue(map[string]goyjs.Input{
			testApp: goyjs.MapValue(map[string]goyjs.Input{
				ecollab.HydratedKey: goyjs.MapValue(map[string]goyjs.Input{
					"body":  goyjs.Bool(true),
					"title": goyjs.Bool(false),
				}),
			}),
		}))
	if err != nil {
		t.Fatalf("seed: %v", err)
	}

	txn := d.NewReadTxn()
	defer txn.Commit()

	if !collab.Hydrated(txn, testApp, "body") {
		t.Error("body is marked and Hydrated says otherwise")
	}

	if collab.Hydrated(txn, testApp, "title") {
		t.Error("title is marked false and Hydrated says otherwise")
	}

	if collab.Hydrated(txn, testApp, "lead") {
		t.Error("lead has no marker and Hydrated says otherwise")
	}
}

// TestEditAFieldThroughItsNode: the ordinary path — read the field,
// address a block by identity, apply a delta to it as one
// transaction, and read the result back.
func TestEditAFieldThroughItsNode(t *testing.T) {
	d := goyjs.New()
	defer d.Close()

	collab := ecollab.CollabOn(d.Map(ecollab.RootName))

	_, err := ensureField(d, collab, "body",
		goyjs.XMLTextValue(nil, seedDelta("b1", "Räksmörgås är gott")))
	if err != nil {
		t.Fatalf("ensure body: %v", err)
	}

	err = d.Write(func(w *goyjs.WriteTxn) error {
		field, ok := collab.Field(w.ReadTxn(), testApp, "body")
		if !ok {
			return errors.New("body is gone")
		}

		block := field.Delta()[0].Embed.Node()

		block.ApplyDelta(w, goyjs.Delta{
			goyjs.Retain(goyjs.UTF16Len("Räksmörgås "), nil),
			goyjs.Insert("verkligen ", goyjs.Attrs{"bold": goyjs.Bool(true)}),
		})
		block.SetAttribute(w, "id", goyjs.String("b2"))

		return nil
	})
	if err != nil {
		t.Fatalf("edit body: %v", err)
	}

	txn := d.NewReadTxn()
	defer txn.Commit()

	field, ok := collab.Field(txn, testApp, "body")
	if !ok {
		t.Fatal("body is gone after the edit")
	}

	block := *field.Delta()[0].Embed

	id, _ := block.Attribute("id")
	if id.String() != "b2" {
		t.Errorf("block id is %q, want b2", id.String())
	}

	runs := block.Delta()
	if len(runs) != 3 {
		t.Fatalf("block has %d runs, want three", len(runs))
	}

	if runs[0].Text != "Räksmörgås " {
		t.Errorf("first run is %q", runs[0].Text)
	}

	if runs[1].Text != "verkligen " {
		t.Errorf("second run is %q", runs[1].Text)
	}

	if bold, ok := runs[1].Attributes["bold"]; !ok || !bold.Bool() {
		t.Error("the inserted run is not bold")
	}

	if runs[2].Text != "är gott" {
		t.Errorf("third run is %q", runs[2].Text)
	}
}

// TestMaterializeSkipsRichCollabState: rich values under _collab stay
// out of the NewsDoc, which is the reason the key is skipped rather
// than translated.
func TestMaterializeSkipsRichCollabState(t *testing.T) {
	d := goyjs.New()
	defer d.Close()

	must(t, "title", d.MapSetString(ecollab.RootName, "title", "A headline"))

	collab := ecollab.CollabOn(d.Map(ecollab.RootName))

	_, err := ensureField(d, collab, "title",
		goyjs.XMLTextValue(nil, seedDelta("t1", "A headline being edited")))
	if err != nil {
		t.Fatalf("ensure title: %v", err)
	}

	doc, err := ecollab.Materialize(d, ecollab.RootName)
	if err != nil {
		t.Fatalf("materialize: %v", err)
	}

	if doc.Title != "A headline" {
		t.Errorf("title is %q, want the NewsDoc-shaped string", doc.Title)
	}
}
