package ecollab_test

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/ttab/ecollab"
	"github.com/ttab/goyjs"
)

// persisted is what an offline-capable client keeps of a document
// between connections: the local state and the lineage that state
// belongs to. Both are needed; the state alone cannot say which
// history it is part of.
type persisted struct {
	State   []byte // goyjs.Doc.EncodeStateV2
	Lineage string // Subscription.Lineage
}

// An agent, or any client, that keeps working on a document while it
// is disconnected, and hands its edits over when it comes back.
//
// While connected it persists the document together with the lineage
// the last Synced reported. Coming back, it restores the document,
// subscribes with WithDoc and WithLineage, and lets the handshake do
// the rest: the server sends what changed while the client was away,
// and the client answers the server's state vector with the edits it
// made. The server's backlog is in the document when Subscribe
// returns; WaitDelivered is what says the client's edits are in the
// session, which a publish soft-stop can delay, so the copy is kept
// until it returns. If the session the copy belonged to is gone and
// the document was seeded afresh, the subscribe is refused with a
// *LineageMismatchError and the copy is left for the caller to
// recover from.
func ExampleClient_Subscribe_resume() {
	ctx := context.Background()

	c, err := ecollab.Dial(ctx, "https://collab.example", ecollab.HTTP2Client(),
		ecollab.WithTokenSource(ecollab.StaticToken("bearer", time.Time{})))
	if err != nil {
		fmt.Println(err)

		return
	}

	defer func() {
		_ = c.Close()
	}()

	const docID = "3f2b8c1e-0000-4000-8000-000000000001"

	saved, found := load(docID) // the application's own storage

	// The document is the caller's: the subscription folds the
	// session into it but never closes it.
	doc := goyjs.New()
	defer doc.Close()

	opts := []ecollab.SubscribeOption{ecollab.WithDoc(doc)}

	if found {
		if err := doc.ApplyUpdateV2(saved.State); err != nil {
			fmt.Println(err)

			return
		}

		opts = append(opts, ecollab.WithLineage(saved.Lineage))
	}

	sub, err := c.Subscribe(ctx, docID, opts...)

	var (
		mismatch *ecollab.LineageMismatchError
		tooLarge *ecollab.ResyncTooLargeError
	)

	switch {
	case errors.As(err, &mismatch), errors.As(err, &tooLarge):
		// The edits cannot be merged into the session as they are.
		// The copy is untouched: recover what is worth keeping from
		// it — into a sketch, for a person to copy across — then
		// forget it and subscribe again from an empty document.
		recoverFrom(doc)
		forget(docID)

		return
	case err != nil:
		fmt.Println(err)

		return
	}

	// The offline edits are in the session once this returns nil;
	// until then the persisted copy is the only place they are.
	// ErrResyncDropped means the server dropped them without saying
	// why: keep the copy, and Resync to try again.
	if err := sub.WaitDelivered(ctx); err != nil {
		fmt.Println(err)

		return
	}

	// Persist after every change worth keeping, always with the
	// lineage the subscription reports rather than one read out of
	// the document.
	err = sub.Read(func(doc *goyjs.Doc) error {
		return save(docID, persisted{
			State:   doc.EncodeStateV2(),
			Lineage: sub.Lineage(),
		})
	})
	if err != nil {
		fmt.Println(err)
	}
}

func load(string) (persisted, bool) { return persisted{}, false }
func save(string, persisted) error  { return nil }
func forget(string)                 {}
func recoverFrom(*goyjs.Doc)        {}
