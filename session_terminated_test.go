package ecollab_test

import (
	"bytes"
	"errors"
	"fmt"
	"testing"

	"github.com/ttab/ecollab"
	"github.com/ttab/goyjs"
)

func TestSessionTerminatedRoundTrip(t *testing.T) {
	in := ecollab.SessionTerminated{
		Reason:      ecollab.TerminateReasonFrozen,
		Version:     12,
		SessionID:   "01K6HA2B7C9D3E5F8G0H1J4K6M",
		StateVector: []byte{0x01, 0x8d, 0x88, 0xab, 0xba, 0x04, 0x03},
	}

	msg := ecollab.EncodeSessionTerminated(in)

	const want = `{"reason":"frozen","version":12,` +
		`"session_id":"01K6HA2B7C9D3E5F8G0H1J4K6M","state_vector":"AY2Iq7oEAw=="}`

	if msg != want {
		t.Errorf("encoded = %s, want %s", msg, want)
	}

	out, err := ecollab.DecodeSessionTerminated(msg)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}

	assertSessionTerminated(t, out, in)
}

func TestSessionTerminatedWithoutVector(t *testing.T) {
	in := ecollab.SessionTerminated{
		Reason:    ecollab.TerminateReasonEvicted,
		SessionID: "01K6HA2B7C9D3E5F8G0H1J4K6M",
	}

	msg := ecollab.EncodeSessionTerminated(in)

	if want := `{"reason":"evicted","session_id":"01K6HA2B7C9D3E5F8G0H1J4K6M"}`; msg != want {
		t.Errorf("encoded = %s, want %s", msg, want)
	}

	out, err := ecollab.DecodeSessionTerminated(msg)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}

	assertSessionTerminated(t, out, in)
}

// TestSessionTerminatedLegacyShapes: the messages the service writes
// before it carries a state vector still decode, with no vector.
func TestSessionTerminatedLegacyShapes(t *testing.T) {
	cases := map[string]ecollab.SessionTerminated{
		`{"reason":"frozen","version":12,"session_id":"s1"}`: {
			Reason:    ecollab.TerminateReasonFrozen,
			Version:   12,
			SessionID: "s1",
		},
		`{"reason":"evicted","session_id":"s2"}`: {
			Reason:    ecollab.TerminateReasonEvicted,
			SessionID: "s2",
		},
		`{"reason":"sketch_promoted","session_id":"s3"}`: {
			Reason:    ecollab.TerminateReasonSketchPromoted,
			SessionID: "s3",
		},
	}

	for msg, want := range cases {
		out, err := ecollab.DecodeSessionTerminated(msg)
		if err != nil {
			t.Errorf("decode %s: %v", msg, err)

			continue
		}

		assertSessionTerminated(t, out, want)
	}
}

func TestSessionTerminatedDecodeErrors(t *testing.T) {
	for _, msg := range []string{
		"",
		"subscription was reaped",
		`{"session_id":"s1"}`,
		`{"reason":"","version":3}`,
		`{"reason":"frozen","state_vector":"not base64!"}`,
	} {
		if out, err := ecollab.DecodeSessionTerminated(msg); err == nil {
			t.Errorf("decode %q = %+v, want an error", msg, out)
		}
	}
}

func TestTerminated(t *testing.T) {
	want := ecollab.SessionTerminated{
		Reason:      ecollab.TerminateReasonFrozen,
		Version:     4,
		SessionID:   "s1",
		StateVector: []byte{0x01, 0x05, 0x02},
	}

	closed := &ecollab.CloseError{
		Doc:     "doc",
		Reason:  ecollab.CloseReasonSessionTerminated,
		Message: ecollab.EncodeSessionTerminated(want),
	}

	// A subscribe refused because the document is frozen carries the
	// freeze's message under subscribe_failed.
	refused := &ecollab.CloseError{
		Doc:     "doc",
		Reason:  ecollab.CloseReasonSubscribeFailed,
		Message: ecollab.EncodeSessionTerminated(want),
	}

	for name, err := range map[string]error{
		"bare":             closed,
		"wrapped":          fmt.Errorf("subscription ended: %w", closed),
		"subscribe failed": refused,
	} {
		got, ok := ecollab.Terminated(err)
		if !ok {
			t.Errorf("%s: Terminated reported no session_terminated close", name)

			continue
		}

		assertSessionTerminated(t, got, want)
	}

	for name, err := range map[string]error{
		"nil":       nil,
		"unrelated": errors.New("connection reset"),
		"other close": &ecollab.CloseError{
			Doc:     "doc",
			Reason:  ecollab.CloseReasonReadOnly,
			Message: ecollab.EncodeSessionTerminated(want),
		},
		"prose message": &ecollab.CloseError{
			Doc:     "doc",
			Reason:  ecollab.CloseReasonSessionTerminated,
			Message: "subscription was reaped",
		},
		"prose subscribe failure": &ecollab.CloseError{
			Doc:     "doc",
			Reason:  ecollab.CloseReasonSubscribeFailed,
			Message: "subscribe: connection refused",
		},
	} {
		if got, ok := ecollab.Terminated(err); ok {
			t.Errorf("%s: Terminated = %+v, want not ok", name, got)
		}
	}
}

func assertSessionTerminated(t *testing.T, got, want ecollab.SessionTerminated) {
	t.Helper()

	if got.Reason != want.Reason || got.Version != want.Version ||
		got.SessionID != want.SessionID ||
		!bytes.Equal(got.StateVector, want.StateVector) ||
		!bytes.Equal(got.DeleteSet, want.DeleteSet) {
		t.Errorf("session terminated = %+v, want %+v", got, want)
	}
}

func TestSessionTerminatedDeleteSetRoundTrip(t *testing.T) {
	in := ecollab.SessionTerminated{
		Reason:      ecollab.TerminateReasonFrozen,
		Version:     3,
		SessionID:   "s1",
		StateVector: []byte{0x01, 0x05, 0x02},
		DeleteSet:   []byte{0x00, 0x01, 0x05, 0x01, 0x00, 0x02},
	}

	msg := ecollab.EncodeSessionTerminated(in)

	const want = `{"reason":"frozen","version":3,"session_id":"s1",` +
		`"state_vector":"AQUC","delete_set":"AAEFAQAC"}`

	if msg != want {
		t.Errorf("encoded = %s, want %s", msg, want)
	}

	out, err := ecollab.DecodeSessionTerminated(msg)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}

	assertSessionTerminated(t, out, in)
}

// TestLacksDeletions: a client that deleted text after the state the
// session ended with was taken finds a deletion the delete set lacks;
// one whose deletions were all taken finds none. Neither shows in a
// state vector comparison, since a deletion adds no struct.
func TestLacksDeletions(t *testing.T) {
	server := goyjs.New()
	t.Cleanup(server.Close)

	editText(t, server, func(w *goyjs.WriteTxn, txt *goyjs.Node) {
		txt.Insert(w, 0, "Hello, world")
	})
	editText(t, server, func(w *goyjs.WriteTxn, txt *goyjs.Node) {
		txt.Delete(w, 5, 7)
	})

	client := goyjs.New()
	t.Cleanup(client.Close)

	if err := client.ApplyUpdateV1(server.EncodeStateV1()); err != nil {
		t.Fatalf("apply the server state: %v", err)
	}

	ended := endedState(server)

	lacks, err := ended.LacksDeletions(ownDeleteSet(client))
	if err != nil {
		t.Fatalf("compare: %v", err)
	}

	if lacks {
		t.Error("a client holding the ended state lacks deletions, want none")
	}

	// A pure deletion the ended state did not take.
	editText(t, client, func(w *goyjs.WriteTxn, txt *goyjs.Node) {
		txt.Delete(w, 0, 1)
	})

	lacks, err = ended.LacksDeletions(ownDeleteSet(client))
	if err != nil {
		t.Fatalf("compare: %v", err)
	}

	if !lacks {
		t.Error("a deletion the ended state did not take went unseen")
	}

	if _, err := (ecollab.SessionTerminated{
		Reason: ecollab.TerminateReasonFrozen,
	}).LacksDeletions(ownDeleteSet(client)); !errors.Is(err, ecollab.ErrNoDeleteSet) {
		t.Errorf("no delete set: err = %v, want ErrNoDeleteSet", err)
	}
}

func editText(t *testing.T, doc *goyjs.Doc, fn func(w *goyjs.WriteTxn, txt *goyjs.Node)) {
	t.Helper()

	err := doc.Write(func(w *goyjs.WriteTxn) error {
		fn(w, doc.Text("t"))

		return nil
	})
	if err != nil {
		t.Fatalf("edit: %v", err)
	}
}

func ownDeleteSet(doc *goyjs.Doc) []byte {
	return doc.EncodeDiffV1(doc.StateVectorV1())
}

func endedState(doc *goyjs.Doc) ecollab.SessionTerminated {
	return ecollab.SessionTerminated{
		Reason:      ecollab.TerminateReasonFrozen,
		StateVector: doc.StateVectorV1(),
		DeleteSet:   ownDeleteSet(doc),
	}
}
