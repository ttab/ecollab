package ecollab_test

import (
	"testing"

	"github.com/ttab/ecollab"
)

func TestLineageMismatchRoundTrip(t *testing.T) {
	in := ecollab.LineageMismatch{
		Lineage: "01K6H9Z3QJ8M5V2X4N7P0R1S2T",
		Cause:   ecollab.LineageEndCauseAnchorMoved,
	}

	msg := ecollab.EncodeLineageMismatch(in)

	if want := `{"lineage":"01K6H9Z3QJ8M5V2X4N7P0R1S2T","cause":"anchor_moved"}`; msg != want {
		t.Errorf("encoded = %s, want %s", msg, want)
	}

	out, err := ecollab.DecodeLineageMismatch(msg)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}

	if out != in {
		t.Errorf("round trip = %+v, want %+v", out, in)
	}
}

func TestLineageMismatchDefaultsToUnknown(t *testing.T) {
	if got := ecollab.EncodeLineageMismatch(ecollab.LineageMismatch{}); got != `{"lineage":"","cause":"unknown"}` {
		t.Errorf("encoded zero value = %s", got)
	}

	out, err := ecollab.DecodeLineageMismatch(`{"lineage":""}`)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}

	if out.Cause != ecollab.LineageEndCauseUnknown {
		t.Errorf("decoded cause = %q, want unknown", out.Cause)
	}
}
