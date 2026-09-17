package presence_test

import (
	"encoding/json"
	"maps"
	"testing"
	"time"

	"github.com/ttab/ecollab/presence"
	"github.com/ttab/goyjs"
)

// writeEntries writes one per-doc inner map, the way the service's
// writer does: whole-value, because goyjs only exposes root-level
// map writes.
func writeEntries(t *testing.T, doc *goyjs.Doc, docID string, entries map[string]presence.Entry) {
	t.Helper()

	inner := map[string]goyjs.Input{}
	for subID, e := range entries {
		inner[subID] = e.Input()
	}

	if err := doc.MapSet(presence.RootName, docID, goyjs.MapValue(inner)); err != nil {
		t.Fatalf("map set %q: %v", docID, err)
	}
}

// TestEntryRoundTrip is the reason Entry and Read live in one
// package: the encoder the writer uses and the reader every consumer
// uses cannot drift without this failing.
func TestEntryRoundTrip(t *testing.T) {
	doc := goyjs.New()
	defer doc.Close()

	joined := time.Date(2026, 9, 18, 10, 30, 0, 123456789, time.UTC)

	want := presence.Entry{
		Subject:  "core://user/alice",
		JoinedAt: joined,
		DocKind:  "repository",
		Identity: presence.Identity{
			Subject: "core://user/alice",
			Units:   []string{"core://unit/desk"},
			Org:     "core://org/tt",
			Name:    "Alice",
			Custom:  map[string]string{"tenant_role": "editor"},
		},
	}

	writeEntries(t, doc, "doc-1", map[string]presence.Entry{"sub-1": want})

	all := presence.Read(doc)

	if len(all) != 1 || all["doc-1"] == nil {
		t.Fatalf("Read = %v, want one entry under doc-1", all)
	}

	got := all["doc-1"]["sub-1"]

	if got.Subject != want.Subject {
		t.Errorf("Subject = %q, want %q", got.Subject, want.Subject)
	}

	if !got.JoinedAt.Equal(want.JoinedAt) {
		t.Errorf("JoinedAt = %s, want %s", got.JoinedAt, want.JoinedAt)
	}

	if got.DocKind != want.DocKind {
		t.Errorf("DocKind = %q, want %q", got.DocKind, want.DocKind)
	}

	if got.Identity.Subject != want.Identity.Subject ||
		got.Identity.Org != want.Identity.Org ||
		got.Identity.Name != want.Identity.Name {
		t.Errorf("Identity = %+v, want %+v", got.Identity, want.Identity)
	}

	if len(got.Identity.Units) != 1 || got.Identity.Units[0] != "core://unit/desk" {
		t.Errorf("Identity.Units = %v, want [core://unit/desk]", got.Identity.Units)
	}

	if !maps.Equal(got.Identity.Custom, want.Identity.Custom) {
		t.Errorf("Identity.Custom = %v, want %v", got.Identity.Custom, want.Identity.Custom)
	}
}

// TestReadPerDocLayout pins the doc_id-keyed outer map the schema
// specifies: a flat by-subscription layout would still round-trip
// individual entries but would surface the wrong shape to a client
// binding one doc id.
func TestReadPerDocLayout(t *testing.T) {
	doc := goyjs.New()
	defer doc.Close()

	now := time.Now()

	writeEntries(t, doc, "doc-1", map[string]presence.Entry{
		"sub-1": {Subject: "alice", JoinedAt: now, DocKind: "repository"},
		"sub-2": {Subject: "bob", JoinedAt: now, DocKind: "repository"},
	})
	writeEntries(t, doc, "doc-2", map[string]presence.Entry{
		"sub-3": {Subject: "alice", JoinedAt: now, DocKind: "sketch"},
	})

	all := presence.Read(doc)

	if len(all) != 2 {
		t.Fatalf("Read covered %d docs, want 2: %v", len(all), all)
	}

	if len(all["doc-1"]) != 2 || len(all["doc-2"]) != 1 {
		t.Errorf("participant counts = %d/%d, want 2/1",
			len(all["doc-1"]), len(all["doc-2"]))
	}

	one := presence.ReadDoc(doc, "doc-2")

	if len(one) != 1 || one["sub-3"].DocKind != "sketch" {
		t.Errorf("ReadDoc(doc-2) = %v, want sub-3 as a sketch", one)
	}
}

// TestReadEmpty covers the two absences a reader meets before
// anybody has joined: a document with no presence root at all, and a
// doc id with no entry.
func TestReadEmpty(t *testing.T) {
	doc := goyjs.New()
	defer doc.Close()

	if all := presence.Read(doc); len(all) != 0 {
		t.Errorf("Read on a fresh doc = %v, want empty", all)
	}

	if one := presence.ReadDoc(doc, "doc-1"); len(one) != 0 {
		t.Errorf("ReadDoc on a fresh doc = %v, want empty", one)
	}

	writeEntries(t, doc, "doc-1", map[string]presence.Entry{
		"sub-1": {Subject: "alice", JoinedAt: time.Now(), DocKind: "repository"},
	})

	if one := presence.ReadDoc(doc, "other"); len(one) != 0 {
		t.Errorf("ReadDoc for an absent doc id = %v, want empty", one)
	}
}

// TestReadBestEffort asserts that a malformed entry yields zero
// values for the fields it broke rather than costing the reader the
// participant, or the rest of the document.
func TestReadBestEffort(t *testing.T) {
	doc := goyjs.New()
	defer doc.Close()

	err := doc.MapSet(presence.RootName, "doc-1", goyjs.MapValue(map[string]goyjs.Input{
		"sub-broken": goyjs.MapValue(map[string]goyjs.Input{
			"subject":   goyjs.String("core://user/alice"),
			"joined_at": goyjs.String("yesterday"),
			"doc_kind":  goyjs.String("repository"),
			"identity":  goyjs.String("{not json"),
			"future":    goyjs.String("a field this reader does not know"),
		}),
	}))
	if err != nil {
		t.Fatalf("map set: %v", err)
	}

	got := presence.ReadDoc(doc, "doc-1")["sub-broken"]

	if got.Subject != "core://user/alice" || got.DocKind != "repository" {
		t.Errorf("readable fields lost: %+v", got)
	}

	if !got.JoinedAt.IsZero() {
		t.Errorf("JoinedAt = %s, want the zero time for an unparseable value", got.JoinedAt)
	}

	if string(got.Identity.JSONBytes()) != "{}" {
		t.Errorf("Identity = %+v, want zero for an undecodable value", got.Identity)
	}
}

// TestIdentityJSONBytes pins the `{}` encoding of a zero Identity.
// The service's identity columns are JSONB and are queried as
// objects, so a null there would be a schema change.
func TestIdentityJSONBytes(t *testing.T) {
	if got := string(presence.Identity{}.JSONBytes()); got != "{}" {
		t.Errorf("zero Identity encodes as %q, want {}", got)
	}

	var decoded map[string]any

	if err := json.Unmarshal(presence.Identity{Name: "Alice"}.JSONBytes(), &decoded); err != nil {
		t.Fatalf("identity JSON does not decode: %v", err)
	}

	if decoded["name"] != "Alice" || len(decoded) != 1 {
		t.Errorf("identity JSON = %v, want only a name", decoded)
	}
}
