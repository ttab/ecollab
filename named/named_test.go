package named_test

import (
	"errors"
	"testing"

	"github.com/ttab/ecollab"
	"github.com/ttab/ecollab/named"
	"github.com/ttab/ecollab/presence"
)

func TestClassifyRepository(t *testing.T) {
	d, err := named.Classify("11111111-2222-3333-4444-555555555555")
	if err != nil {
		t.Fatalf("err: %v", err)
	}

	if d.Kind != named.KindRepository {
		t.Errorf("kind = %q, want repository", d.Kind)
	}
}

func TestClassifyPresence(t *testing.T) {
	d, err := named.Classify("__presence__")
	if err != nil {
		t.Fatalf("err: %v", err)
	}

	if d.Kind != named.KindService || d.Service != "presence" {
		t.Errorf("got %+v", d)
	}
}

func TestClassifyUser(t *testing.T) {
	encoded := named.FormatUserDocID("core://user/alice", "bookmarks")

	d, err := named.Classify(encoded)
	if err != nil {
		t.Fatalf("err: %v", err)
	}

	if d.Kind != named.KindUser {
		t.Errorf("kind = %q, want named_user", d.Kind)
	}

	if d.OwnerSub != "core://user/alice" {
		t.Errorf("owner = %q", d.OwnerSub)
	}

	if d.Name != "bookmarks" {
		t.Errorf("name = %q", d.Name)
	}
}

func TestClassifyUserWithHTTPSSub(t *testing.T) {
	encoded := named.FormatUserDocID(
		"https://accounts.example.com/users/alice", "recent")

	d, err := named.Classify(encoded)
	if err != nil {
		t.Fatalf("err: %v", err)
	}

	if d.OwnerSub != "https://accounts.example.com/users/alice" {
		t.Errorf("owner = %q", d.OwnerSub)
	}

	if d.Name != "recent" {
		t.Errorf("name = %q", d.Name)
	}
}

func TestClassifyUnknown(t *testing.T) {
	_, err := named.Classify("__future-thing__")
	if !errors.Is(err, named.ErrUnknownNamedDoc) {
		t.Errorf("err = %v, want ErrUnknownNamedDoc", err)
	}
}

func TestClassifyMalformedUser(t *testing.T) {
	cases := []string{
		"__user:",
		"__user:alice",
		"__user::name",
		"__user:alice:",
		"__user:%ZZ:name",
	}
	for _, c := range cases {
		t.Run(c, func(t *testing.T) {
			_, err := named.Classify(c)
			if !errors.Is(err, named.ErrMalformedNamedDoc) {
				t.Errorf("%q: err = %v, want ErrMalformedNamedDoc", c, err)
			}
		})
	}
}

// TestRootName pins the root each kind resolves to: the presence
// document's reactive by_doc root, and the conventional document
// root for everything else. The service's inspection walk reads a
// named document through this, so a wrong answer here is an empty
// inspection of a populated document.
func TestRootName(t *testing.T) {
	cases := []struct {
		docID string
		want  string
	}{
		{presence.DocID, presence.RootName},
		{named.FormatUserDocID("core://user/alice", "bookmarks"), ecollab.RootName},
		{"11111111-2222-3333-4444-555555555555", ecollab.RootName},
	}

	for _, c := range cases {
		t.Run(c.docID, func(t *testing.T) {
			d, err := named.Classify(c.docID)
			if err != nil {
				t.Fatalf("err: %v", err)
			}

			if got := d.RootName(); got != c.want {
				t.Errorf("RootName() = %q, want %q", got, c.want)
			}
		})
	}
}
