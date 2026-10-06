package ecollab

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"

	collabv1 "github.com/ttab/elephant-api/elephant/collab/v1"
	"github.com/ttab/elephant-api/newsdoc"
	"github.com/ttab/goyjs"
)

// MaxSyncStep2Bytes is the largest sync step 2 the service accepts
// from a client: the answer to its sync step 1, carrying everything
// the local document holds that the session lacks. It is larger than
// the cap on an ordinary update because a day of offline edits
// arrives in one, and a merged Yjs update cannot be split. A larger
// one is not sent; see ResyncTooLargeError.
const MaxSyncStep2Bytes = 1 << 20

// SubscribeOption configures one subscription.
type SubscribeOption func(*subscribeConfig)

type subscribeConfig struct {
	observer    bool
	advertise   *bool
	freezeOn    []string
	lineage     string
	doc         *goyjs.Doc
	onUpdate    func(update []byte, seed bool)
	onAwareness func(update []byte)
	onEvent     func(Event)
}

// Observer asks to watch a live session without joining it as an
// editor. An observer is granted read_only, is never counted as a
// participant, and can never open a session: a document with no live
// session answers CloseReasonNoActiveSession rather than starting
// one.
//
// It is the right mode for anything that reads rather than edits —
// an agent, a preview, a dashboard — and it is what keeps such a
// reader from taking a repository lock.
func Observer() SubscribeOption {
	return func(c *subscribeConfig) {
		c.observer = true
	}
}

// AdvertisePresence overrides whether this subscription appears in
// the document's presence entry. Unset leaves the doc-kind default:
// true for editors, false for observers.
func AdvertisePresence(advertise bool) SubscribeOption {
	return func(c *subscribeConfig) {
		c.advertise = &advertise
	}
}

// FreezeOnWorkflowStates asks the service to freeze the session when
// the document enters one of the named workflow states, rather than
// leaving it live across the transition.
func FreezeOnWorkflowStates(states ...string) SubscribeOption {
	return func(c *subscribeConfig) {
		c.freezeOn = append(c.freezeOn, states...)
	}
}

// WithDoc subscribes against a document the caller already has —
// state carried over from an earlier session, or built from a
// repository version — so the state vector the subscribe carries is
// not empty and the server's Step 2 is a diff rather than the whole
// document. Whatever the document holds that the session does not is
// sent back in the handshake; pair it with WithLineage for a copy
// persisted from an earlier subscription.
//
// The document is the caller's: Close leaves it open, and the caller
// must not touch it outside Read and Update while the subscription
// is live.
func WithDoc(doc *goyjs.Doc) SubscribeOption {
	return func(c *subscribeConfig) {
		c.doc = doc
	}
}

// WithLineage declares the lineage the local document belongs to:
// the value Subscription.Lineage reported when the document was last
// in sync, persisted alongside it. The server checks it against the
// session's own lineage before it computes any catch-up, and refuses
// a mismatch with a *LineageMismatchError rather than merging two
// histories into duplicate structure.
//
// It belongs with WithDoc: a client resuming from a persisted copy
// declares the lineage it persisted with that copy. Take it from
// Subscription.Lineage, never from the document's lineage root —
// see Lineage for why. A fresh client with an empty document
// declares nothing; so may a client that lost the value, in which
// case the server judges the state vector alone.
func WithLineage(lineage string) SubscribeOption {
	return func(c *subscribeConfig) {
		c.lineage = lineage
	}
}

// OnUpdate registers a callback for every document update the
// subscription receives, called after it has been applied to the
// local document. The seed flag says the emitter called it
// structural seeding rather than authorship; the bytes have already
// been applied either way.
//
// It runs on the client's read loop and must not block: hand the work
// to a goroutine or a buffered channel.
func OnUpdate(fn func(update []byte, seed bool)) SubscribeOption {
	return func(c *subscribeConfig) {
		c.onUpdate = fn
	}
}

// OnAwareness registers a callback for inbound awareness updates,
// which are y-protocols payloads this library passes through
// untouched — goyjs.Awareness is what interprets them. Same
// no-blocking rule as OnUpdate.
func OnAwareness(fn func(update []byte)) SubscribeOption {
	return func(c *subscribeConfig) {
		c.onAwareness = fn
	}
}

// OnEvent registers a callback for the server-issued events scoped to
// this document; see the Event* constants. Same no-blocking rule as
// OnUpdate.
func OnEvent(fn func(Event)) SubscribeOption {
	return func(c *subscribeConfig) {
		c.onEvent = fn
	}
}

// Subscription is one document on a Client's stream: the local Y.Doc
// the session's updates are folded into, and the means to contribute
// to it.
//
// It ends when the server closes it — a *CloseError carrying the
// reason — or when the stream ends under it, and a subscription that
// has ended does not come back: subscribe again for a fresh session.
type Subscription struct {
	client *Client
	docID  string

	// docMu guards doc against the read loop, which applies inbound
	// updates to it while the caller reads it.
	docMu  sync.Mutex
	doc    *goyjs.Doc
	ownDoc bool

	// hsMu serialises handshakes, so a Resync and the re-handshake
	// that follows a publish_cleared cannot take each other's
	// Synced.
	hsMu sync.Mutex

	// synced carries every Synced the server sends — one per
	// subscribe, including a repeat one. Buffered so the read loop
	// never waits for a caller that has stopped listening.
	synced chan syncedState

	// heldStep2 is the server's sync step 2, held back from the
	// document until the step 1 behind it has been answered, so that
	// a subscribe given up while answering leaves a WithDoc document
	// as the caller passed it. Guarded by docMu.
	heldStep2 []byte

	mu       sync.Mutex
	mode     SubscriptionMode
	lineage  string
	serverSV []byte
	err      error

	// declared is the lineage the latest handshake declared, kept
	// for the error a lineage_mismatch refusal of it becomes.
	declared string

	// unsent is the deletions in the local document the session may
	// not hold: those in a WithDoc document when it was passed in,
	// less those the server's step 2 showed it already had. A Yjs
	// diff carries the whole delete set whatever state vector it is
	// computed to, so this, and not the diff, is what says whether a
	// step 2 with no structs has anything to deliver. It is emptied
	// by a step 2 that landed, which carried them all.
	// unsentUnknown says the document's delete set could not be
	// read, and any non-empty diff is sent.
	unsent        deleteSet
	unsentUnknown bool

	// softStopped follows the publish soft-stop events: true from a
	// publish_in_progress to the next publish_cleared.
	softStopped bool

	// inFlight is a sync step 2 that has gone out and not yet been
	// confirmed. The step 1 of the next handshake confirms it, since
	// the server handles a connection's messages in order: a
	// refusal would have come first. Until then a
	// publish_in_progress is read as its refusal.
	inFlight *step2Flight

	// resyncOwed says a sync step 2 was refused, or held back, by the
	// soft-stop, and the subscription re-handshakes when it clears.
	resyncOwed    bool
	rehandshaking bool

	// dropped says a step 2 went out, was not refused, and the
	// confirming step 1 shows the session without it. The server
	// gave no reason, so it is not retried until the next Resync.
	dropped bool

	// delivery is closed and replaced whenever inFlight,
	// resyncOwed or dropped changes, which is what WaitDelivered
	// waits on.
	delivery chan struct{}

	// outstanding counts the subscribes sent and not yet answered by
	// a Synced. A subscription given up mid-handshake stays
	// registered until it reaches zero, so the Synced still on its
	// way cannot answer the next subscription for the document.
	outstanding int

	// abandoned says the client gave the subscription up itself;
	// see abandon. Set under mu.
	abandoned   atomic.Bool
	released    chan struct{}
	releaseOnce sync.Once

	done     chan struct{}
	doneOnce sync.Once

	onUpdate    func(update []byte, seed bool)
	onAwareness func(update []byte)
	onEvent     func(Event)
}

// Subscribe opens a subscription for docID and blocks until the
// server has finished initial state transfer, which is what its
// Synced message marks. The granted mode and the session's lineage
// are on the returned subscription.
//
// State transfer runs both ways. The server sends what the local
// document lacks, then its own state vector; the client answers with
// what the session lacks — the edits a document passed with WithDoc
// gained while it was offline. So a client resuming from a persisted
// copy subscribes with WithDoc and WithLineage and lets the handshake
// deliver both backlogs. When the client did send something,
// Subscribe runs the handshake a second time before it returns: the
// server handles a connection's messages in order, so that second
// answer is what shows the edits were taken. WaitDelivered says
// whether they were — a publish soft-stop can hold them past the
// return — and is what to wait on before treating a persisted copy
// as handed over.
//
// A refusal is an error: a *CloseError carrying the reason when the
// server closed this document's subscription — CloseReasonReadOnly,
// CloseReasonNoActiveSession, CloseReasonSessionEnding (an eviction
// is finishing; subscribe again after a short wait),
// CloseReasonSubscribeFailed — a
// *LineageMismatchError when the local document belongs to a history
// the session no longer has, a *ResyncTooLargeError when its offline
// edits are too large to send, and the stream's terminal error when
// the refusal took the whole connection, as the subscription cap
// does. A document passed with WithDoc is untouched by any of them:
// the server's catch-up is held back from it until the client has
// answered, and a subscribe that fails before then never applies it.
//
// After a *ResyncTooLargeError, or any error the client raised
// itself, a new Subscribe for the same document waits until the
// server has finished answering the one that was given up, so the
// two cannot be confused.
//
// Editors need write permission on the document, not just read: a
// fresh session takes a repository lock with the service's own
// identity, and granting that on read alone would make the service a
// privilege-escalation channel. Observer subscriptions are exempt
// because they can never open a session.
func (c *Client) Subscribe(
	ctx context.Context, docID string, opts ...SubscribeOption,
) (*Subscription, error) {
	if docID == "" {
		return nil, errors.New("no document id given")
	}

	var cfg subscribeConfig

	for _, opt := range opts {
		opt(&cfg)
	}

	s := &Subscription{
		client:      c,
		docID:       docID,
		doc:         cfg.doc,
		ownDoc:      cfg.doc == nil,
		synced:      make(chan syncedState, 4),
		delivery:    make(chan struct{}),
		released:    make(chan struct{}),
		done:        make(chan struct{}),
		onUpdate:    cfg.onUpdate,
		onAwareness: cfg.onAwareness,
		onEvent:     cfg.onEvent,
	}

	if s.doc == nil {
		s.doc = goyjs.New()
	} else {
		// A diff to the document's own state vector carries no
		// structs, only the whole delete set.
		ds, err := encodedDeleteSet(s.doc.EncodeDiffV1(s.doc.StateVectorV1()))
		if err != nil {
			s.unsentUnknown = true
		} else {
			s.unsent = ds
		}
	}

	if err := c.claim(ctx, s); err != nil {
		s.closeDoc()

		return nil, err
	}

	mode, err := s.handshake(ctx, cfg, cfg.lineage)
	if err != nil {
		// A refused subscribe leaves nothing behind: no registration,
		// and no document for Client.Close to free twice. One the
		// client gave up itself unregisters when the server has
		// finished answering it.
		if !s.abandoned.Load() {
			s.release()
		}

		s.finish(err)
		s.closeDoc()

		return nil, err
	}

	s.setMode(mode)

	return s, nil
}

// request is the Subscribe payload the options resolve to.
func (c subscribeConfig) request(
	sv []byte, lineage string,
) *collabv1.CollaborateRequest_Subscribe {
	return &collabv1.CollaborateRequest_Subscribe{
		Subscribe: &collabv1.Subscribe{
			StateVector:            sv,
			AdvertisePresence:      c.advertise,
			Observer:               c.observer,
			FreezeOnWorkflowStates: c.freezeOn,
			Lineage:                lineage,
		},
	}
}

// DocID is the document this subscription is for.
func (s *Subscription) DocID() string {
	return s.docID
}

// Mode is the read/write capability the server granted.
func (s *Subscription) Mode() SubscriptionMode {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.mode
}

// Lineage is the session's lineage, as the latest Synced reported
// it, or "" before the first one and from a server too old to say.
//
// It is the value to persist alongside the local document and to
// declare with WithLineage when subscribing from that copy again:
// it names the CRDT history the document's items belong to, and the
// server uses it to tell a returning client of the same history from
// one whose history is gone.
func (s *Subscription) Lineage() string {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.lineage
}

// ServerStateVector is the server's state vector as the latest
// Synced reported it: the point the server's half of that handshake
// was computed at. Nil before the first Synced.
func (s *Subscription) ServerStateVector() []byte {
	s.mu.Lock()
	defer s.mu.Unlock()

	return bytes.Clone(s.serverSV)
}

// Done is closed when the subscription has ended. Err says how.
func (s *Subscription) Done() <-chan struct{} {
	return s.done
}

// Err is why the subscription ended: a *CloseError when the server
// closed it, the stream's own terminal error when the connection
// went with it, and nil while it is live.
func (s *Subscription) Err() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.err
}

// StateVector is the local document's Yjs state vector: what this
// subscription has seen, in the form a subscribe and a snapshot
// freshness check take it.
func (s *Subscription) StateVector() []byte {
	s.docMu.Lock()
	defer s.docMu.Unlock()

	if s.doc == nil {
		return nil
	}

	return s.doc.StateVectorV1()
}

// Read runs fn against the local document with the stream's writer
// held off. The document, and everything read out of it, belongs to
// the subscription: copy what the callback needs rather than letting
// a goyjs.Value escape.
func (s *Subscription) Read(fn func(doc *goyjs.Doc) error) error {
	s.docMu.Lock()
	defer s.docMu.Unlock()

	if s.doc == nil {
		return ErrClosed
	}

	if err := fn(s.doc); err != nil {
		return fmt.Errorf("read the document: %w", err)
	}

	return nil
}

// NewsDoc materialises the local document as a newsdoc.Document,
// under the tree contract this package owns. It is a snapshot of what
// this subscription has seen, not a read of the repository, and it is
// computed on demand: a caller that wants one per keystroke should
// think about how often it asks.
func (s *Subscription) NewsDoc() (*newsdoc.Document, error) {
	s.docMu.Lock()
	defer s.docMu.Unlock()

	if s.doc == nil {
		return nil, ErrClosed
	}

	doc, err := Materialize(s.doc, RootName)
	if err != nil {
		return nil, fmt.Errorf("materialize the document: %w", err)
	}

	return doc, nil
}

// Update applies fn to the local document and forwards the resulting
// Yjs update to the session, which is how an edit reaches the other
// participants. A mutation that produced nothing is ErrNoChange and
// nothing is sent.
//
// A read-only subscription is refused: the server answers
// CloseReasonReadOnly and closes this subscription, rather than
// ignoring the update. Mode says so up front.
func (s *Subscription) Update(
	ctx context.Context, fn func(doc *goyjs.Doc) error,
) error {
	return s.mutate(ctx, false, fn)
}

// Seed is Update for structural seeding rather than authorship: the
// update is stored with a seed encoding so replay and attribution can
// tell the two apart. Reflecting canonical state into the document is
// seeding; what a person typed is not.
func (s *Subscription) Seed(
	ctx context.Context, fn func(doc *goyjs.Doc) error,
) error {
	return s.mutate(ctx, true, fn)
}

func (s *Subscription) mutate(
	ctx context.Context, seed bool, fn func(doc *goyjs.Doc) error,
) error {
	update, err := s.diff(fn)
	if err != nil {
		return err
	}

	if isEmptyUpdateV1(update) {
		return ErrNoChange
	}

	// Deliberately outside the document lock: the update is already
	// applied locally, and holding the lock across a write would
	// stall the read loop behind the network. Two concurrent updates
	// can reach the session in either order, which Yjs does not mind.
	return s.Forward(ctx, update, seed)
}

// emptyUpdateV1 is what a v1 update encodes to when there is
// nothing in it: no structs and no delete set, two varuint zeroes.
// Yjs produces it rather than an empty buffer for a diff against a
// state vector the document has nothing beyond, so it is what "the
// mutation changed nothing" looks like on the wire.
var emptyUpdateV1 = []byte{0, 0}

// isEmptyUpdateV1 reports whether an update carries nothing.
func isEmptyUpdateV1(update []byte) bool {
	return len(update) == 0 || bytes.Equal(update, emptyUpdateV1)
}

// diff applies fn and returns the update it produced.
func (s *Subscription) diff(fn func(doc *goyjs.Doc) error) ([]byte, error) {
	s.docMu.Lock()
	defer s.docMu.Unlock()

	if s.doc == nil {
		return nil, ErrClosed
	}

	before := s.doc.StateVectorV1()

	if err := fn(s.doc); err != nil {
		return nil, fmt.Errorf("mutate the document: %w", err)
	}

	return s.doc.EncodeDiffV1(before), nil
}

// Forward ships a Yjs update the caller produced itself, without
// touching the local document — for a caller that keeps the Y.Doc on
// its own side, such as a proxy between this stream and a browser.
// Update is the ordinary way to make an edit.
func (s *Subscription) Forward(
	ctx context.Context, update []byte, seed bool,
) error {
	return s.client.send(ctx, &collabv1.CollaborateRequest{
		Doc: s.docID,
		Payload: &collabv1.CollaborateRequest_Update{
			Update: &collabv1.Update{Update: update, Seed: seed},
		},
	})
}

// SendAwareness ships a y-protocols awareness update — who is here,
// where their cursor is — to the other participants. It is not
// document state and the service does not interpret it.
//
// Awareness is never paused by a publish soft-stop: collaborators
// keep seeing each other while updates are held.
func (s *Subscription) SendAwareness(
	ctx context.Context, update []byte,
) error {
	return s.client.send(ctx, &collabv1.CollaborateRequest{
		Doc: s.docID,
		Payload: &collabv1.CollaborateRequest_Awareness{
			Awareness: &collabv1.Awareness{Update: update},
		},
	})
}

// QueryAwareness asks for the session's awareness state. The service
// answers with an empty awareness update — it forwards awareness
// rather than keeping it — so what actually fills the local picture
// is the other participants answering the query with their own
// state.
func (s *Subscription) QueryAwareness(ctx context.Context) error {
	return s.client.send(ctx, &collabv1.CollaborateRequest{
		Doc: s.docID,
		Payload: &collabv1.CollaborateRequest_QueryAwareness{
			QueryAwareness: &collabv1.QueryAwareness{},
		},
	})
}

// Resync re-runs the handshake on an open subscription: the server
// sends its sync step 1 (to a read-write subscription), the client
// answers it with whatever the local document holds that the
// session lacks, and a fresh Synced ends it. The server sends no
// step 2, because the subscription has been on the live tail and
// already holds the session's state. When the client did send a
// step 2, the handshake runs once more to confirm it, as
// Subscribe's does. It does not re-open the session, re-seed it, or
// re-apply the subscribe options — those decisions were made when
// the subscription opened.
//
// The subscription does this itself after a publish soft-stop held
// its step 2 back, so a caller rarely needs to. A Resync is also how
// to try again after WaitDelivered reported ErrResyncDropped.
//
// It takes the same options as Subscribe because the message on the
// wire carries them either way, and the server ignoring them on an
// open subscription is the contract. Only the options that shape the
// message are read: the callbacks and the document stay as
// Subscribe left them. The lineage declared is WithLineage's when
// given and Lineage otherwise.
func (s *Subscription) Resync(
	ctx context.Context, opts ...SubscribeOption,
) (SubscriptionMode, error) {
	var cfg subscribeConfig

	for _, opt := range opts {
		opt(&cfg)
	}

	lineage := cfg.lineage
	if lineage == "" {
		lineage = s.Lineage()
	}

	mode, err := s.handshake(ctx, cfg, lineage)
	if err != nil {
		return "", err
	}

	s.setMode(mode)

	return mode, nil
}

// Unsubscribe gives up the subscription, leaving the stream open for
// the client's other documents. The local document is left as it is,
// so what the subscription saw can still be read; Client.Close frees
// it.
func (s *Subscription) Unsubscribe(ctx context.Context) error {
	// Dropped first, so nothing the stream has already queued for
	// this document reaches it after the caller has taken it back.
	s.client.drop(s)

	s.finish(ErrClosed)

	return s.client.send(ctx, &collabv1.CollaborateRequest{
		Doc: s.docID,
		Payload: &collabv1.CollaborateRequest_Unsubscribe{
			Unsubscribe: &collabv1.Unsubscribe{},
		},
	})
}

// ErrResyncDropped is WaitDelivered's answer when the client sent the
// session its local changes, the server did not refuse them, and the
// next handshake showed the session without them: the server dropped
// them and gave no reason. They are not sent again on their own;
// Resync tries once more.
var ErrResyncDropped = errors.New("the session did not take the local changes")

// WaitDelivered blocks until everything the handshakes so far owed
// the session has reached it: the edits a WithDoc document carried
// in, and those made while a step 2 was held. It returns nil when
// nothing is owed, which is the usual case when Subscribe returns:
// a step 2 that went out has been confirmed by then.
//
// What it waits through is a publish soft-stop. The server refuses
// a step 2 while one is in force, and the subscription holds it and
// sends it again when the soft-stop clears, so the edits are only
// delivered then. A client that means to discard its persisted copy,
// or to Close, after resuming waits here first.
//
// It returns ErrResyncDropped when the server dropped the step 2
// without refusing it, the subscription's error when the
// subscription ends with something still owed, and the context's
// error when that ends first. Updates made with Update are not
// tracked here: they are forwarded as they are made.
func (s *Subscription) WaitDelivered(ctx context.Context) error {
	for {
		s.mu.Lock()

		var (
			dropped = s.dropped
			owed    = s.inFlight != nil || s.resyncOwed
			changed = s.delivery
		)

		s.mu.Unlock()

		switch {
		case dropped:
			return ErrResyncDropped
		case !owed:
			return nil
		}

		select {
		case <-changed:
		case <-s.done:
			if err := s.Err(); err != nil {
				return err
			}

			return ErrClosed
		case <-ctx.Done():
			return fmt.Errorf("wait for the local changes to be delivered: %w",
				ctx.Err())
		}
	}
}

// handshake sends a Subscribe declaring lineage and waits for the
// Synced that answers it. The server's sync step 1, which arrives
// before the Synced, is answered by the read loop; see answerStep1.
//
// When that answer sent a step 2, the handshake runs once more. The
// server handles a connection's messages in order, so the second
// step 1 is computed after the step 2 was taken or refused, and
// answerStep1 reads the outcome off it. That bounds the window in
// which a publish_in_progress is taken for a refusal to the
// handshake itself.
func (s *Subscription) handshake(
	ctx context.Context, cfg subscribeConfig, lineage string,
) (SubscriptionMode, error) {
	s.hsMu.Lock()
	defer s.hsMu.Unlock()

	// An explicit handshake is a retry of anything dropped.
	s.mu.Lock()
	s.dropped = false
	s.signalDeliveryLocked()
	s.mu.Unlock()

	mode, err := s.subscribeOnce(ctx, cfg, lineage)
	if err != nil {
		return "", err
	}

	s.mu.Lock()
	confirm := s.inFlight != nil
	s.mu.Unlock()

	if !confirm {
		return mode, nil
	}

	// The first Synced has reported the session's lineage, which a
	// fresh client did not have to declare.
	if lineage == "" {
		lineage = s.Lineage()
	}

	return s.subscribeOnce(ctx, cfg, lineage)
}

// subscribeOnce is one Subscribe and the Synced that answers it.
func (s *Subscription) subscribeOnce(
	ctx context.Context, cfg subscribeConfig, lineage string,
) (SubscriptionMode, error) {
	// A subscribe for a subscription that has ended would open a new
	// one on the server that nothing here routes to.
	select {
	case <-s.done:
		if err := s.Err(); err != nil {
			return "", err
		}

		return "", ErrClosed
	default:
	}

	// A Synced left over from an earlier subscribe would answer this
	// one.
	drain(s.synced)

	s.mu.Lock()

	if s.abandoned.Load() {
		s.mu.Unlock()

		return "", s.Err()
	}

	s.declared = lineage
	s.outstanding++

	s.mu.Unlock()

	err := s.client.send(ctx, &collabv1.CollaborateRequest{
		Doc:     s.docID,
		Payload: cfg.request(s.StateVector(), lineage),
	})
	if err != nil {
		s.mu.Lock()
		s.outstanding--
		s.mu.Unlock()

		return "", err
	}

	select {
	case st := <-s.synced:
		return st.mode, nil
	case <-s.done:
		return "", s.Err()
	case <-s.client.done:
		return "", s.client.ended()
	case <-ctx.Done():
		return "", fmt.Errorf("wait for the subscription to sync: %w",
			ctx.Err())
	}
}

func drain(ch chan syncedState) {
	for {
		select {
		case <-ch:
		default:
			return
		}
	}
}

// apply folds one inbound update into the local document. A document
// that cannot take an update has diverged from the session, and the
// subscription is given up rather than carrying on with a local
// state nobody else has.
func (s *Subscription) apply(update []byte) {
	if isEmptyUpdateV1(update) {
		return
	}

	s.docMu.Lock()

	var err error
	if s.doc != nil {
		err = s.doc.ApplyUpdateV1(update)
	}

	s.docMu.Unlock()

	if err != nil {
		s.abandon(fmt.Errorf(
			"apply an inbound update to the local document: %w", err))
	}
}

// holdStep2 takes the server's sync step 2. Its delete set is the
// server's whole one, so whatever deletions the local document
// shares with it are not news to the session. The diff itself waits
// in heldStep2 for flushStep2.
func (s *Subscription) holdStep2(diff []byte) {
	s.flushStep2()

	if isEmptyUpdateV1(diff) {
		return
	}

	if _, ds, err := updateV1DeleteSet(diff); err == nil {
		s.mu.Lock()
		s.unsent = s.unsent.subtract(ds)
		s.mu.Unlock()
	}

	s.docMu.Lock()
	s.heldStep2 = diff
	s.docMu.Unlock()
}

// flushStep2 applies a held server step 2, if there is one.
func (s *Subscription) flushStep2() {
	s.docMu.Lock()
	held := s.heldStep2
	s.heldStep2 = nil
	s.docMu.Unlock()

	if held != nil {
		s.apply(held)
	}
}

// syncedState is what one Synced reports.
type syncedState struct {
	mode     SubscriptionMode
	lineage  string
	serverSV []byte
}

func (s *Subscription) setSynced(st syncedState) {
	// A read-only subscription is sent no step 1, so this is where
	// its catch-up lands.
	s.flushStep2()

	s.mu.Lock()

	s.outstanding = max(s.outstanding-1, 0)
	s.mode = st.mode
	s.serverSV = st.serverSV

	// A server that predates lineage says nothing, which is not a
	// reason to forget what an earlier Synced said.
	if st.lineage != "" {
		s.lineage = st.lineage
	}

	s.mu.Unlock()

	select {
	case s.synced <- st:
	default:
	}
}

// abandonedMessage handles a message for a subscription the client
// has given up: only the end of the handshake it was given up in
// matters, which is what lets it be unregistered.
func (s *Subscription) abandonedMessage(msg *collabv1.CollaborateResponse) {
	s.mu.Lock()

	switch msg.GetPayload().(type) {
	case *collabv1.CollaborateResponse_Synced:
		s.outstanding = max(s.outstanding-1, 0)
	case *collabv1.CollaborateResponse_Close:
		s.outstanding = 0
	}

	settled := s.outstanding == 0

	s.mu.Unlock()

	if settled {
		s.release()
	}
}

func (s *Subscription) declaredLineage() string {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.declared
}

// step2Flight is a sync step 2 that has gone out, awaiting the step
// 1 that confirms it.
type step2Flight struct {
	// sv is the local state vector the step 2 was computed at, and
	// hasStructs whether it carried any items. A session that took
	// it holds everything up to sv.
	sv         StateVector
	hasStructs bool
}

// answerStep1 is the client's half of the handshake: the server's
// sync step 1 carries its state vector, and the answer is a sync
// step 2 with everything the local document holds beyond it — edits
// made offline, or while the subscription was down. It runs on the
// read loop, before the server's own step 2 has been applied: that
// adds only items the server has, so the diff is the same either
// way, and a subscribe given up here leaves the document as it was.
//
// A diff is sent when it carries structs, or deletions the session
// may lack (see unsent). Nothing else is news, however many bytes
// the delete set takes.
//
// A step 1 that follows a step 2 first settles it: taken if the
// session's state vector now covers what the step 2 carried, dropped
// if not. While a publish soft-stop is in force the step 2 is held
// back rather than sent to be refused, and the subscription
// re-handshakes when the soft-stop clears. A step 2 over
// MaxSyncStep2Bytes is not sent at all: the subscription is given up
// with a *ResyncTooLargeError and the local document left as it is.
func (s *Subscription) answerStep1(serverSV []byte) {
	// goyjs panics on a state vector it cannot decode, and a Doc
	// that trapped is dead, so the vector is checked first.
	sv, err := DecodeStateVector(serverSV)
	if err != nil {
		s.abandon(fmt.Errorf(
			"the server's sync step 1 for %q carried an unreadable state vector: %w",
			s.docID, err))

		return
	}

	defer s.flushStep2()

	// Everything here runs on the read loop, as do the events that
	// change the same state, so the state is read once and written
	// once: WaitDelivered must not see an in-between.
	s.mu.Lock()
	flight := s.inFlight
	s.mu.Unlock()

	if flight != nil && flight.hasStructs && !sv.Dominates(flight.sv) {
		s.mu.Lock()
		s.inFlight = nil
		s.resyncOwed = false
		s.dropped = true
		s.signalDeliveryLocked()
		s.mu.Unlock()

		return
	}

	s.docMu.Lock()

	var diff, localSV []byte
	if s.doc != nil {
		diff = s.doc.EncodeDiffV1(serverSV)
		localSV = s.doc.StateVectorV1()
	}

	s.docMu.Unlock()

	hasStructs, _, parseErr := updateV1DeleteSet(diff)

	s.mu.Lock()

	if flight != nil {
		// A diff carries the whole delete set, so the session now
		// holds every deletion the document had when it was sent.
		s.unsent = nil
		s.unsentUnknown = false
	}

	news := !isEmptyUpdateV1(diff) &&
		(parseErr != nil || hasStructs || s.unsentUnknown || !s.unsent.empty())

	switch {
	case !news:
		s.inFlight = nil
		s.resyncOwed = false
	case len(diff) > MaxSyncStep2Bytes:
		// Given up below; the subscription's end is what
		// WaitDelivered reports.
	case s.softStopped:
		s.inFlight = nil
		s.resyncOwed = true
	default:
		// The vector came from goyjs, so it decodes; an empty one
		// would only weaken the dropped check.
		local, _ := DecodeStateVector(localSV)

		s.inFlight = &step2Flight{
			sv:         local,
			hasStructs: parseErr != nil || hasStructs,
		}
		s.resyncOwed = false
	}

	send := news && !s.softStopped && len(diff) <= MaxSyncStep2Bytes

	s.signalDeliveryLocked()
	s.mu.Unlock()

	if news && len(diff) > MaxSyncStep2Bytes {
		s.abandon(&ResyncTooLargeError{
			Doc:   s.docID,
			Size:  len(diff),
			Limit: MaxSyncStep2Bytes,
		})

		return
	}

	if !send {
		return
	}

	// Sent from the read loop, so it goes out ahead of anything the
	// caller does once Synced has released the handshake. A failure
	// is the stream ending, which the read loop reports.
	_ = s.client.send(s.client.bg, &collabv1.CollaborateRequest{
		Doc: s.docID,
		Payload: &collabv1.CollaborateRequest_SyncStep2{
			SyncStep2: &collabv1.SyncStep2{Diff: diff},
		},
	})
}

// signalDeliveryLocked wakes WaitDelivered. Called with mu held.
func (s *Subscription) signalDeliveryLocked() {
	close(s.delivery)
	s.delivery = make(chan struct{})
}

// observeEvent follows the publish soft-stop. The service refuses a
// step 2 during one by sending publish_in_progress, which is the
// same event that announces the soft-stop, so a publish_in_progress
// while a step 2 is in flight is taken as its refusal. The window is
// short: the handshake that sent the step 2 runs again to confirm
// it. When the soft-stop clears, a subscription owed a resync
// re-handshakes; if the step 2 had in fact landed, the server's
// step 1 shows it and there is nothing to send.
func (s *Subscription) observeEvent(name string) {
	switch name {
	case EventPublishInProgress:
		s.mu.Lock()

		s.softStopped = true

		if s.inFlight != nil {
			s.inFlight = nil
			s.resyncOwed = true
			s.signalDeliveryLocked()
		}

		s.mu.Unlock()
	case EventPublishCleared:
		s.mu.Lock()

		s.softStopped = false

		spawn := s.resyncOwed && !s.rehandshaking
		if spawn {
			s.rehandshaking = true
		}

		s.mu.Unlock()

		if spawn {
			// Not on the read loop: the handshake waits for a Synced
			// that only the read loop can deliver.
			go s.rehandshake()
		}
	}
}

// rehandshake re-runs the handshake on the subscription's own
// behalf. A refusal ends the subscription and is reported through
// Err, and a stream that has ended reports itself, so there is
// nothing to do with the error here.
func (s *Subscription) rehandshake() {
	_, _ = s.Resync(s.client.bg)

	s.mu.Lock()
	s.rehandshaking = false
	s.mu.Unlock()
}

// abandon gives up a subscription from the client's side: the server
// is told to close its half, then the subscription ends with err.
// The local document is left as it is, and a held server step 2 is
// never applied to it.
//
// The order is what makes subscribing again safe. The Unsubscribe is
// on the stream before the caller is woken, so a new Subscribe
// cannot overtake it. And the subscription stays registered, eating
// whatever the server still sends for it, until the Synced of the
// handshake it was given up in has arrived; Client.Subscribe waits
// for that before it claims the document again.
func (s *Subscription) abandon(err error) {
	s.mu.Lock()

	if s.abandoned.Load() {
		s.mu.Unlock()

		return
	}

	s.abandoned.Store(true)

	s.mu.Unlock()

	s.docMu.Lock()
	s.heldStep2 = nil
	s.docMu.Unlock()

	_ = s.client.send(s.client.bg, &collabv1.CollaborateRequest{
		Doc: s.docID,
		Payload: &collabv1.CollaborateRequest_Unsubscribe{
			Unsubscribe: &collabv1.Unsubscribe{},
		},
	})

	s.finish(err)

	s.mu.Lock()
	settled := s.outstanding == 0
	s.mu.Unlock()

	if settled {
		s.release()
	}
}

// release unregisters the subscription and lets a Subscribe waiting
// for its document go ahead.
func (s *Subscription) release() {
	s.releaseOnce.Do(func() {
		s.client.drop(s)
		close(s.released)
	})
}

func (s *Subscription) setMode(mode SubscriptionMode) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.mode = mode
}

func (s *Subscription) deliverUpdate(update []byte, seed bool) {
	if s.onUpdate != nil {
		s.onUpdate(update, seed)
	}
}

func (s *Subscription) deliverAwareness(update []byte) {
	if s.onAwareness != nil {
		s.onAwareness(update)
	}
}

func (s *Subscription) deliverEvent(ev Event) {
	if s.onEvent != nil {
		s.onEvent(ev)
	}
}

// finish records why the subscription ended, once.
func (s *Subscription) finish(err error) {
	s.doneOnce.Do(func() {
		s.mu.Lock()
		s.err = err
		s.mu.Unlock()

		close(s.done)
	})
}

// closeDoc frees the local document, if the subscription owns it.
func (s *Subscription) closeDoc() {
	s.docMu.Lock()
	defer s.docMu.Unlock()

	if s.ownDoc && s.doc != nil {
		s.doc.Close()
		s.doc = nil
	}
}
