package ecollab

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"sync"

	collabv1 "github.com/ttab/elephant-api/elephant/collab/v1"
	"github.com/ttab/elephant-api/newsdoc"
	"github.com/ttab/goyjs"
)

// SubscribeOption configures one subscription.
type SubscribeOption func(*subscribeConfig)

type subscribeConfig struct {
	observer    bool
	advertise   *bool
	freezeOn    []string
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
// document.
//
// The document is the caller's: Close leaves it open, and the caller
// must not touch it outside Read and Update while the subscription
// is live.
func WithDoc(doc *goyjs.Doc) SubscribeOption {
	return func(c *subscribeConfig) {
		c.doc = doc
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

	// synced carries the mode from every Synced the server sends —
	// one per subscribe, including a repeat one. Buffered so the
	// read loop never waits for a caller that has stopped listening.
	synced chan SubscriptionMode

	mu   sync.Mutex
	mode SubscriptionMode
	err  error

	done     chan struct{}
	doneOnce sync.Once

	onUpdate    func(update []byte, seed bool)
	onAwareness func(update []byte)
	onEvent     func(Event)
}

// Subscribe opens a subscription for docID and blocks until the
// server has finished initial state transfer, which is what its
// Synced message marks. The granted mode is on the returned
// subscription.
//
// A refusal is an error: a *CloseError carrying the reason when the
// server closed this document's subscription — CloseReasonReadOnly,
// CloseReasonNoActiveSession, CloseReasonSubscribeFailed — and the
// stream's terminal error when the refusal took the whole connection,
// as the subscription cap does.
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
		synced:      make(chan SubscriptionMode, 4),
		done:        make(chan struct{}),
		onUpdate:    cfg.onUpdate,
		onAwareness: cfg.onAwareness,
		onEvent:     cfg.onEvent,
	}

	if s.doc == nil {
		s.doc = goyjs.New()
	}

	if err := c.claim(s); err != nil {
		s.closeDoc()

		return nil, err
	}

	mode, err := s.handshake(ctx, cfg.request(s.StateVector()))
	if err != nil {
		// A refused subscribe leaves nothing behind: no registration,
		// and no document for Client.Close to free twice.
		c.drop(s)
		s.finish(err)
		s.closeDoc()

		return nil, err
	}

	s.setMode(mode)

	return s, nil
}

// request is the Subscribe payload the options resolve to.
func (c subscribeConfig) request(sv []byte) *collabv1.CollaborateRequest_Subscribe {
	return &collabv1.CollaborateRequest_Subscribe{
		Subscribe: &collabv1.Subscribe{
			StateVector:            sv,
			AdvertisePresence:      c.advertise,
			Observer:               c.observer,
			FreezeOnWorkflowStates: c.freezeOn,
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

// Resync re-syncs an open subscription: the server answers the
// current state vector with a Step 2 for anything missing and a
// fresh Synced. It does not re-open the session, re-seed it, or
// re-apply the subscribe options — those decisions were made when
// the subscription opened.
//
// It takes the same options as Subscribe because the message on the
// wire carries them either way, and the server ignoring them on an
// open subscription is the contract. Only the options that shape the
// message are read: the callbacks and the document stay as
// Subscribe left them.
func (s *Subscription) Resync(
	ctx context.Context, opts ...SubscribeOption,
) (SubscriptionMode, error) {
	var cfg subscribeConfig

	for _, opt := range opts {
		opt(&cfg)
	}

	mode, err := s.handshake(ctx, cfg.request(s.StateVector()))
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

// handshake sends a Subscribe and waits for the Synced that answers
// it.
func (s *Subscription) handshake(
	ctx context.Context, payload *collabv1.CollaborateRequest_Subscribe,
) (SubscriptionMode, error) {
	// A Synced left over from an earlier subscribe would answer this
	// one.
	drain(s.synced)

	err := s.client.send(ctx, &collabv1.CollaborateRequest{
		Doc:     s.docID,
		Payload: payload,
	})
	if err != nil {
		return "", err
	}

	select {
	case mode := <-s.synced:
		return mode, nil
	case <-s.done:
		return "", s.Err()
	case <-s.client.done:
		return "", s.client.ended()
	case <-ctx.Done():
		return "", fmt.Errorf("wait for the subscription to sync: %w",
			ctx.Err())
	}
}

func drain(ch chan SubscriptionMode) {
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
// subscription ends rather than carrying on with a local state
// nobody else has.
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
		s.client.drop(s)
		s.finish(fmt.Errorf(
			"apply an inbound update to the local document: %w", err))
	}
}

func (s *Subscription) setSynced(mode SubscriptionMode) {
	s.setMode(mode)

	select {
	case s.synced <- mode:
	default:
	}
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
