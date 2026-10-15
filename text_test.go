package ecollab_test

import (
	"testing"

	"github.com/ttab/ecollab"
	"github.com/ttab/goyjs"
)

// paragraph writes delta into a fresh document as a field's single
// paragraph embed, as an editor stores it, and returns the document
// and the paragraph's node.
func paragraph(t *testing.T, delta goyjs.Delta) (*goyjs.Doc, *goyjs.Node) {
	t.Helper()

	d := goyjs.New()
	t.Cleanup(d.Close)

	c := ecollab.CollabOn(d.Map(ecollab.RootName))

	_, err := ensureField(d, c, "body", goyjs.XMLTextValue(nil, goyjs.Delta{
		goyjs.Embed(goyjs.XMLTextValue(goyjs.Attrs{
			"id":   goyjs.String("para-1"),
			"type": goyjs.String("core/text"),
		}, delta), nil),
	}))
	if err != nil {
		t.Fatalf("stage the paragraph: %v", err)
	}

	return d, paragraphValue(t, d).Node()
}

// paragraphValue reads the paragraph paragraph() wrote.
func paragraphValue(t *testing.T, d *goyjs.Doc) goyjs.Value {
	t.Helper()

	read := d.NewReadTxn()
	defer read.Commit()

	field, ok := ecollab.CollabOn(d.Map(ecollab.RootName)).Field(read, testApp, "body")
	if !ok {
		t.Fatal("the body is gone")
	}

	runs := field.Delta()
	if len(runs) != 1 || runs[0].Embed == nil {
		t.Fatalf("the body holds %d runs, want one paragraph embed", len(runs))
	}

	return *runs[0].Embed
}

func bold() goyjs.Attrs {
	return goyjs.Attrs{"core/bold": goyjs.Bool(true)}
}

func link(text string) goyjs.Input {
	return goyjs.XMLTextValue(goyjs.Attrs{
		"type": goyjs.String("core/link"),
		"properties": goyjs.JSONMap(map[string]goyjs.Input{
			"url": goyjs.String("https://example.com"),
		}),
	}, goyjs.Delta{goyjs.Insert(text, nil)})
}

// TestFindTextCountsUTF16: the offsets are in the units yjs counts,
// so text before the match that is outside the Basic Multilingual
// Plane counts two, and every occurrence is reported.
func TestFindTextCountsUTF16(t *testing.T) {
	d, _ := paragraph(t, goyjs.Delta{goyjs.Insert("Öl 🍺 och ost, mer ost", nil)})

	runs := paragraphValue(t, d).Delta()

	got := ecollab.FindText(runs, "ost")

	want := []ecollab.TextMatch{{Index: 10, Length: 3}, {Index: 19, Length: 3}}

	if len(got) != len(want) {
		t.Fatalf("found %v, want %v", got, want)
	}

	for i := range want {
		if got[i] != want[i] {
			t.Errorf("match %d is %v, want %v", i, got[i], want[i])
		}
	}
}

// TestFindTextAcrossFormatting: a phrase with a mark starting inside
// it spans several runs and is found as one match.
func TestFindTextAcrossFormatting(t *testing.T) {
	d, _ := paragraph(t, goyjs.Delta{
		goyjs.Insert("The moon is ", nil),
		goyjs.Insert("made", bold()),
		goyjs.Insert(" of cheese.", nil),
	})

	runs := paragraphValue(t, d).Delta()
	if len(runs) != 3 {
		t.Fatalf("the paragraph holds %d runs, want the three the mark splits it into", len(runs))
	}

	got := ecollab.FindText(runs, "is made of")
	if len(got) != 1 || got[0] != (ecollab.TextMatch{Index: 9, Length: 10}) {
		t.Fatalf("found %v, want one match at 9 of length 10", got)
	}
}

// TestFindTextStopsAtEmbeds: an inline element is one unit and not
// text, so it shifts the offsets after it, and a phrase that would
// run into it is not found.
func TestFindTextStopsAtEmbeds(t *testing.T) {
	d, _ := paragraph(t, goyjs.Delta{
		goyjs.Insert("Read ", nil),
		goyjs.Embed(link("the report"), nil),
		goyjs.Insert(" about the report", nil),
	})

	runs := paragraphValue(t, d).Delta()

	got := ecollab.FindText(runs, "the report")
	if len(got) != 1 || got[0] != (ecollab.TextMatch{Index: 13, Length: 10}) {
		t.Fatalf("found %v, want only the plain-text occurrence at 13", got)
	}

	if got := ecollab.FindText(runs, "Read the"); len(got) != 0 {
		t.Errorf("found %v across the link, want nothing", got)
	}

	if got := ecollab.FindText(runs, ""); got != nil {
		t.Errorf("found %v for an empty needle, want nothing", got)
	}
}

// TestReplaceTextKeepsTheMarks: the replacement takes the formatting
// of the first unit it replaces, through a real write, and the text
// around it is untouched.
func TestReplaceTextKeepsTheMarks(t *testing.T) {
	d, node := paragraph(t, goyjs.Delta{
		goyjs.Insert("Kaffe och ", nil),
		goyjs.Insert("kanelbullar", bold()),
		goyjs.Insert(" på fikat.", nil),
	})

	runs := paragraphValue(t, d).Delta()

	matches := ecollab.FindText(runs, "kanelbullar")
	if len(matches) != 1 {
		t.Fatalf("found %v, want one match", matches)
	}

	err := d.Write(func(w *goyjs.WriteTxn) error {
		node.ApplyDelta(w, ecollab.ReplaceText(runs, matches[0], "semlor"))

		return nil
	})
	if err != nil {
		t.Fatalf("replace: %v", err)
	}

	got, _ := paragraphValue(t, d).RichString()

	want := "Kaffe och <core/bold>semlor</core/bold> på fikat."
	if got != want {
		t.Errorf("the paragraph reads %q, want %q", got, want)
	}
}

// TestReplaceTextAfterEmbed: offsets past an inline element land on
// the text the match named, and the unformatted replacement does not
// inherit a mark from the left.
func TestReplaceTextAfterEmbed(t *testing.T) {
	d, node := paragraph(t, goyjs.Delta{
		goyjs.Insert("Läs ", bold()),
		goyjs.Embed(link("rapporten"), nil),
		goyjs.Insert(" om ost i dag", nil),
	})

	runs := paragraphValue(t, d).Delta()

	matches := ecollab.FindText(runs, "ost")
	if len(matches) != 1 {
		t.Fatalf("found %v, want one match", matches)
	}

	err := d.Write(func(w *goyjs.WriteTxn) error {
		node.ApplyDelta(w, ecollab.ReplaceText(runs, matches[0], "mjölk"))

		return nil
	})
	if err != nil {
		t.Fatalf("replace: %v", err)
	}

	after := paragraphValue(t, d).Delta()

	if got := ecollab.FindText(after, " om mjölk i dag"); len(got) != 1 {
		t.Fatalf("the replacement is not where the match was: %v", after)
	}

	for _, run := range after {
		if run.Embed == nil && run.Text == " om mjölk i dag" && len(run.Attributes) != 0 {
			t.Errorf("the replacement took formatting %v, want none", run.Attributes)
		}
	}
}

// TestReplaceTextEmptyDeletes: an empty replacement removes the run.
func TestReplaceTextEmptyDeletes(t *testing.T) {
	d, node := paragraph(t, goyjs.Delta{goyjs.Insert("Inte alls ost.", nil)})

	runs := paragraphValue(t, d).Delta()

	matches := ecollab.FindText(runs, " alls")

	err := d.Write(func(w *goyjs.WriteTxn) error {
		node.ApplyDelta(w, ecollab.ReplaceText(runs, matches[0], ""))

		return nil
	})
	if err != nil {
		t.Fatalf("replace: %v", err)
	}

	if got, _ := paragraphValue(t, d).RichString(); got != "Inte ost." {
		t.Errorf("the paragraph reads %q, want %q", got, "Inte ost.")
	}
}
