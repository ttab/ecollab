// Package named is the grammar of named documents: the collab
// service's Yjs documents that are not backed by a repository
// document, addressed by a reserved `__`-prefixed id rather than a
// UUID.
//
// Two kinds exist. `__presence__` is the service-managed presence
// document, described by ecollab/presence; `__user:{sub}:{name}` is
// one person's own observable state — bookmarks, recents, UI
// preferences — writable only by its owner. Classify is the parser
// both a client and the service run before taking a doc id
// anywhere, and FormatUserDocID is the writer for the user-scoped
// form, so the URL-encoding of the owner's subject has exactly one
// spelling.
//
// WalkToJSON is the other half: a named document's tree read back as
// JSON. Named docs are conventionally restricted to the YMap /
// YArray / string trinity, and the walk renders anything else
// best-effort rather than failing — the README says what to, under
// "Allowed Yjs types in named documents".
//
// The package is a recognizer and a reader, nothing more. Hosting a
// named document — its auth, its persistence, its lifecycle — is the
// service's, and stays there.
package named

import (
	"errors"
	"fmt"
	"net/url"
	"strings"

	"github.com/ttab/ecollab"
	"github.com/ttab/ecollab/presence"
)

// Kind identifies the sub-class of a named document.
type Kind string

const (
	// KindRepository is not a named doc; the input was a regular
	// repository UUID. Returned as the zero classification so callers
	// can fall through without a separate "not named" boolean.
	KindRepository Kind = "repository"

	// KindService is a service-managed named doc. The collab service
	// is the sole writer; clients can subscribe read-only. The only
	// instance today is __presence__.
	KindService Kind = "named_service"

	// KindUser is a user-scoped named doc, owned by exactly one
	// subject identified by the sub URL in the doc ID.
	KindUser Kind = "named_user"
)

// Doc is the parsed view of a named-doc ID. For KindRepository the
// only meaningful field is the original DocID. For KindService and
// KindUser the rest of the fields are populated.
type Doc struct {
	DocID string
	Kind  Kind

	// Service is the service-managed doc's name (e.g. "presence").
	// Empty for non-service docs.
	Service string

	// OwnerSub is the JWT subject of the user-scoped doc's owner.
	// Empty for non-user docs.
	OwnerSub string

	// Name is the user-scoped doc's name (e.g. "bookmarks"). Empty
	// for non-user docs.
	Name string
}

// RootName returns the YMap root the document's state lives under.
// The presence document uses presence.RootName ("by_doc") for its
// reactive per-doc layout; every other named document uses the
// conventional ecollab.RootName ("document") a repository document's
// tree lives under.
//
// Going through here is what keeps a writer, a reader and the
// service's inspection walk on the same root — a mismatch is how an
// inspection of a populated presence document comes back empty.
func (d Doc) RootName() string {
	if d.Kind == KindService && d.Service == "presence" {
		return presence.RootName
	}

	return ecollab.RootName
}

// ErrUnknownNamedDoc is returned by Classify when the doc_id starts
// with `__` but does not match a recognized named-doc kind.
var ErrUnknownNamedDoc = errors.New("named: unknown named-doc kind")

// ErrMalformedNamedDoc is returned by Classify when a `__`-prefixed
// id is recognized as a kind but cannot be parsed (e.g.
// __user:something — missing name segment).
var ErrMalformedNamedDoc = errors.New("named: malformed named-doc id")

// Classify inspects a doc_id and returns its kind plus parsed
// components.
//
// Recognized forms:
//   - __presence__                       → KindService("presence")
//   - __user:{url-encoded sub}:{name}    → KindUser(sub, name)
//
// Any other `__`-prefixed input returns ErrUnknownNamedDoc. Input
// without a `__` prefix is classified as KindRepository with no
// parsing.
func Classify(docID string) (Doc, error) {
	if !strings.HasPrefix(docID, "__") {
		return Doc{DocID: docID, Kind: KindRepository}, nil
	}

	if docID == presence.DocID {
		return Doc{DocID: docID, Kind: KindService, Service: "presence"}, nil
	}

	if strings.HasPrefix(docID, "__user:") {
		// __user:{url-encoded sub}:{name}
		// Sub URLs contain ':' (e.g. https%3A%2F%2F...), but they
		// arrive URL-encoded so ':' in the wire format is always a
		// segment separator. We split on the second colon (`:`)
		// because the literal `:` in the sub is always %3A.
		rest := strings.TrimPrefix(docID, "__user:")

		idx := strings.IndexByte(rest, ':')
		if idx <= 0 || idx == len(rest)-1 {
			return Doc{}, fmt.Errorf("%w: %q", ErrMalformedNamedDoc, docID)
		}

		encodedSub := rest[:idx]
		name := rest[idx+1:]

		sub, err := url.QueryUnescape(encodedSub)
		if err != nil {
			return Doc{}, fmt.Errorf("%w: decode sub: %w", ErrMalformedNamedDoc, err)
		}

		if sub == "" || name == "" {
			return Doc{}, fmt.Errorf("%w: %q", ErrMalformedNamedDoc, docID)
		}

		return Doc{
			DocID:    docID,
			Kind:     KindUser,
			OwnerSub: sub,
			Name:     name,
		}, nil
	}

	return Doc{}, fmt.Errorf("%w: %q", ErrUnknownNamedDoc, docID)
}

// FormatUserDocID renders a user-scoped doc ID for the given owner
// subject and name. The sub is URL-encoded so that any embedded
// colons or other reserved characters survive the round-trip.
//
// It is the one writer of the form: a hand-built id that encodes the
// subject differently classifies as a different document.
func FormatUserDocID(ownerSub, name string) string {
	return "__user:" + url.QueryEscape(ownerSub) + ":" + name
}
