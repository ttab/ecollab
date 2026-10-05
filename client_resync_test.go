package ecollab_test

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ttab/ecollab"
	collabv1 "github.com/ttab/elephant-api/elephant/collab/v1"
	"github.com/ttab/goyjs"
)

// The offline half of the handshake: the lineage a subscribe
// declares, the step 2 a client answers the server's step 1 with,
// the publish soft-stop holding that step 2 back, and the two ways a
// returning client is refused without losing its local copy.

const (
	sessionLineage = "01K6HB00000000000000000000"
	staleLineage   = "01K6HA00000000000000000000"
)

// sessionServer is the server's half of a session the fake stream
// plays: the document it holds, what it answers a subscribe with,
// and what it does with a client's step 2.
type sessionServer struct {
	t   *testing.T
	doc *goyjs.Doc

	mu sync.Mutex

	// before is pushed ahead of the step 1 on every subscribe, such
	// as a soft-stop announcement.
	before []*collabv1.CollaborateResponse

	// readOnly withholds the step 1, as the service does.
	readOnly bool

	// refuse answers a step 2 with publish_in_progress, as the
	// service does during a soft-stop; drop takes no notice of it.
	// Otherwise it is applied to the server's document.
	refuse bool
	drop   bool

	// open says the server holds a subscription for the document,
	// so a subscribe is a repeat and gets no step 2.
	open bool

	subscribes atomic.Int32
}

func newSessionServer(t *testing.T) *sessionServer {
	t.Helper()

	doc := goyjs.New()
	t.Cleanup(doc.Close)

	if err := doc.ApplyUpdateV1(seedUpdate(t)); err != nil {
		t.Fatalf("seed the server's document: %v", err)
	}

	return &sessionServer{t: t, doc: doc}
}

func (srv *sessionServer) set(fn func(srv *sessionServer)) {
	srv.mu.Lock()
	defer srv.mu.Unlock()

	fn(srv)
}

// attach makes the server answer every Subscribe on f with the
// handshake the service sends: step 2 on one that opens the
// subscription, step 1 to a read-write subscription, then Synced
// with the lineage and the server's state vector.
func (srv *sessionServer) attach(f *fakeStream) {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.onSend = func(f *fakeStream, req *collabv1.CollaborateRequest) {
		srv.mu.Lock()
		defer srv.mu.Unlock()

		doc := req.GetDoc()

		switch p := req.GetPayload().(type) {
		case *collabv1.CollaborateRequest_Unsubscribe:
			srv.open = false
		case *collabv1.CollaborateRequest_SyncStep2:
			switch {
			case srv.refuse:
				f.push(publishEvent(ecollab.EventPublishInProgress))
			case srv.drop:
			default:
				if err := srv.doc.ApplyUpdateV1(p.SyncStep2.GetDiff()); err != nil {
					srv.t.Errorf("apply the client's step 2: %v", err)
				}
			}
		case *collabv1.CollaborateRequest_Subscribe:
			srv.subscribes.Add(1)

			if !srv.open {
				srv.open = true

				diff := srv.doc.EncodeDiffV1(p.Subscribe.GetStateVector())
				f.push(&collabv1.CollaborateResponse{
					Doc: doc,
					Payload: &collabv1.CollaborateResponse_SyncStep2{
						SyncStep2: &collabv1.SyncStep2{Diff: diff},
					},
				})
			}

			for _, msg := range srv.before {
				f.push(msg)
			}

			sv := srv.doc.StateVectorV1()
			mode := collabv1.SubscriptionMode_SUBSCRIPTION_MODE_READ_ONLY

			if !srv.readOnly {
				mode = collabv1.SubscriptionMode_SUBSCRIPTION_MODE_READ_WRITE

				f.push(&collabv1.CollaborateResponse{
					Doc: doc,
					Payload: &collabv1.CollaborateResponse_SyncStep1{
						SyncStep1: &collabv1.SyncStep1{StateVector: sv},
					},
				})
			}

			f.push(&collabv1.CollaborateResponse{
				Doc: doc,
				Payload: &collabv1.CollaborateResponse_Synced{
					Synced: &collabv1.Synced{
						Mode:        mode,
						Lineage:     sessionLineage,
						StateVector: sv,
					},
				},
			})
		}
	}
}

// title reads the server's title under its lock.
func (srv *sessionServer) title(t *testing.T) string {
	t.Helper()

	srv.mu.Lock()
	defer srv.mu.Unlock()

	return titleOf(t, srv.doc)
}

// offlineCopy is a client's persisted copy of the session's
// document, edited while it was away. It is the server's state, not
// a fresh seed: two seeds are two lineages.
func (srv *sessionServer) offlineCopy(t *testing.T, title string) *goyjs.Doc {
	t.Helper()

	doc := goyjs.New()
	t.Cleanup(doc.Close)

	srv.mu.Lock()
	state := srv.doc.EncodeStateV1()
	srv.mu.Unlock()

	if err := doc.ApplyUpdateV1(state); err != nil {
		t.Fatalf("seed the local copy: %v", err)
	}

	if err := doc.MapSetString(ecollab.RootName, "title", title); err != nil {
		t.Fatalf("edit the local copy: %v", err)
	}

	return doc
}

func publishEvent(name string) *collabv1.CollaborateResponse {
	return &collabv1.CollaborateResponse{
		Doc: testDoc,
		Payload: &collabv1.CollaborateResponse_Event{
			Event: &collabv1.Event{Name: name, Data: []byte(`{}`)},
		},
	}
}

func step2s(f *fakeStream) []*collabv1.SyncStep2 {
	var out []*collabv1.SyncStep2

	for _, req := range f.requests() {
		if p, ok := req.GetPayload().(*collabv1.CollaborateRequest_SyncStep2); ok {
			out = append(out, p.SyncStep2)
		}
	}

	return out
}

func subscribesSent(f *fakeStream) []*collabv1.Subscribe {
	var out []*collabv1.Subscribe

	for _, req := range f.requests() {
		if p, ok := req.GetPayload().(*collabv1.CollaborateRequest_Subscribe); ok {
			out = append(out, p.Subscribe)
		}
	}

	return out
}

// eventually polls cond until it holds or the budget runs out.
func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()

	deadline := time.Now().Add(budget)

	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}

		time.Sleep(5 * time.Millisecond)
	}
}

func titleOf(t *testing.T, doc *goyjs.Doc) string {
	t.Helper()

	nd, err := ecollab.Materialize(doc, ecollab.RootName)
	if err != nil {
		t.Fatalf("Materialize: %v", err)
	}

	return nd.GetTitle()
}

// TestClientResumesWithOfflineEdits is the offline shape end to end:
// a persisted copy and its lineage go in, the subscribe declares the
// lineage, the step 2 answering the server's step 1 carries exactly
// the edit the session lacked, and a second handshake confirms it
// before Subscribe returns.
func TestClientResumesWithOfflineEdits(t *testing.T) {
	f := newFakeStream()
	srv := newSessionServer(t)
	srv.attach(f)

	local := srv.offlineCopy(t, "Written offline")

	c := ecollab.NewClient(t.Context(), f)

	sub, err := c.Subscribe(t.Context(), testDoc,
		ecollab.WithDoc(local), ecollab.WithLineage(sessionLineage))
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}

	subs := subscribesSent(f)
	if len(subs) != 2 {
		t.Fatalf("sent %d subscribes, want the handshake and its confirmation", len(subs))
	}

	for i, s := range subs {
		if got := s.GetLineage(); got != sessionLineage {
			t.Errorf("subscribe %d declared lineage %q, want %q", i, got, sessionLineage)
		}
	}

	if n := len(step2s(f)); n != 1 {
		t.Fatalf("sent %d sync step 2s, want 1", n)
	}

	if got := srv.title(t); got != "Written offline" {
		t.Errorf("server title after the step 2 = %q, want the offline edit", got)
	}

	if err := sub.WaitDelivered(waitCtx(t)); err != nil {
		t.Errorf("WaitDelivered after a confirmed step 2: %v", err)
	}

	if got := sub.Lineage(); got != sessionLineage {
		t.Errorf("Lineage = %q, want %q", got, sessionLineage)
	}

	if len(sub.ServerStateVector()) == 0 {
		t.Error("ServerStateVector is empty after Synced")
	}
}

// TestClientSendsNoEmptyStep2: a client with nothing the session
// lacks answers the step 1 with nothing, and needs no confirmation.
func TestClientSendsNoEmptyStep2(t *testing.T) {
	f := newFakeStream()
	srv := newSessionServer(t)
	srv.attach(f)

	c := ecollab.NewClient(t.Context(), f)

	sub, err := c.Subscribe(t.Context(), testDoc)
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}

	if n := len(step2s(f)); n != 0 {
		t.Errorf("sent %d sync step 2s for a document with nothing new", n)
	}

	if n := len(subscribesSent(f)); n != 1 {
		t.Errorf("sent %d subscribes, want 1", n)
	}

	// A fresh client declares no lineage.
	if got := subscribesSent(f)[0].GetLineage(); got != "" {
		t.Errorf("a fresh client declared lineage %q", got)
	}

	if err := sub.WaitDelivered(waitCtx(t)); err != nil {
		t.Errorf("WaitDelivered with nothing owed: %v", err)
	}
}

// TestClientSendsNoStep2ForDeletionsTheSessionHas: a Yjs diff carries
// the sender's whole delete set whatever it is computed against, so
// once the session's history has a deletion in it — any retyped
// title — the diff to a caught-up server is not the empty update.
// It is still nothing the server lacks, and is not sent, whether the
// client came in empty or with a copy of the session.
func TestClientSendsNoStep2ForDeletionsTheSessionHas(t *testing.T) {
	f := newFakeStream()
	srv := newSessionServer(t)

	for _, title := range []string{"First", "Second"} {
		if err := srv.doc.MapSetString(ecollab.RootName, "title", title); err != nil {
			t.Fatal(err)
		}
	}

	srv.attach(f)

	c := ecollab.NewClient(t.Context(), f)

	sub, err := c.Subscribe(t.Context(), testDoc)
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}

	if _, err := sub.Resync(t.Context()); err != nil {
		t.Fatalf("Resync: %v", err)
	}

	if err := sub.Unsubscribe(t.Context()); err != nil {
		t.Fatalf("Unsubscribe: %v", err)
	}

	// A copy persisted from the session, with its history, and not
	// edited since.
	copied := goyjs.New()
	t.Cleanup(copied.Close)

	if err := copied.ApplyUpdateV1(srv.doc.EncodeStateV1()); err != nil {
		t.Fatal(err)
	}

	if _, err := c.Subscribe(t.Context(), testDoc,
		ecollab.WithDoc(copied), ecollab.WithLineage(sessionLineage)); err != nil {
		t.Fatalf("Subscribe with the copy: %v", err)
	}

	if n := len(step2s(f)); n != 0 {
		t.Errorf("sent %d sync step 2s carrying only deletions the session has", n)
	}

	if n := len(subscribesSent(f)); n != 3 {
		t.Errorf("sent %d subscribes, want 3 with no confirmations", n)
	}
}

// TestClientSendsAnOfflineDeletion: a deletion is news when the
// session lacks it, even with no structs to go with it.
func TestClientSendsAnOfflineDeletion(t *testing.T) {
	f := newFakeStream()
	srv := newSessionServer(t)
	srv.attach(f)

	local := srv.offlineCopy(t, "Seeded")

	if err := local.MapDelete(ecollab.RootName, "title"); err != nil {
		t.Fatalf("delete offline: %v", err)
	}

	c := ecollab.NewClient(t.Context(), f)

	if _, err := c.Subscribe(t.Context(), testDoc,
		ecollab.WithDoc(local), ecollab.WithLineage(sessionLineage)); err != nil {
		t.Fatalf("Subscribe: %v", err)
	}

	if n := len(step2s(f)); n != 1 {
		t.Fatalf("sent %d sync step 2s, want the deletion", n)
	}

	if got := srv.title(t); got != "" {
		t.Errorf("server title = %q, want it deleted", got)
	}
}

// TestClientNeverDeclaresTheDocumentsLineage: the lineage root is
// content, so a document carrying one is not a reason to declare it.
// Only WithLineage, or the lineage a Synced reported, is declared.
func TestClientNeverDeclaresTheDocumentsLineage(t *testing.T) {
	f := newFakeStream()
	srv := newSessionServer(t)
	srv.attach(f)

	local := srv.offlineCopy(t, "Seeded")

	if l, ok := ecollab.Lineage(local); !ok || l == "" {
		t.Fatal("the test copy carries no lineage root")
	}

	c := ecollab.NewClient(t.Context(), f)

	sub, err := c.Subscribe(t.Context(), testDoc, ecollab.WithDoc(local))
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}

	if got := subscribesSent(f)[0].GetLineage(); got != "" {
		t.Errorf("declared %q, read from the document", got)
	}

	// A Resync declares what Synced reported, unless told otherwise.
	if _, err := sub.Resync(t.Context()); err != nil {
		t.Fatalf("Resync: %v", err)
	}

	subs := subscribesSent(f)
	if got := subs[len(subs)-1].GetLineage(); got != sessionLineage {
		t.Errorf("Resync declared %q, want the Synced lineage %q",
			got, sessionLineage)
	}

	if _, err := sub.Resync(t.Context(), ecollab.WithLineage(staleLineage)); err != nil {
		t.Fatalf("Resync: %v", err)
	}

	subs = subscribesSent(f)
	if got := subs[len(subs)-1].GetLineage(); got != staleLineage {
		t.Errorf("Resync with WithLineage declared %q, want %q",
			got, staleLineage)
	}
}

// TestClientReadOnlyGetsNoStep1: a read-only subscription is sent no
// step 1 and answers nothing, whatever its document holds, and its
// catch-up still lands.
func TestClientReadOnlyGetsNoStep1(t *testing.T) {
	f := newFakeStream()
	srv := newSessionServer(t)
	srv.readOnly = true
	srv.attach(f)

	empty := goyjs.New()
	t.Cleanup(empty.Close)

	c := ecollab.NewClient(t.Context(), f)

	_, err := c.Subscribe(t.Context(), testDoc,
		ecollab.Observer(), ecollab.WithDoc(empty))
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}

	if n := len(step2s(f)); n != 0 {
		t.Errorf("a read-only subscription sent %d sync step 2s", n)
	}

	if got := titleOf(t, empty); got != "Seeded" {
		t.Errorf("read-only catch-up title = %q, want Seeded", got)
	}
}

// TestClientLineageMismatch: a refused subscribe is a typed error
// carrying both lineages, Reason reads it, and the caller's copy is
// untouched and readable for recovery.
func TestClientLineageMismatch(t *testing.T) {
	f := newFakeStream()

	f.mu.Lock()
	f.onSend = func(f *fakeStream, req *collabv1.CollaborateRequest) {
		if _, ok := req.GetPayload().(*collabv1.CollaborateRequest_Subscribe); !ok {
			return
		}

		f.push(&collabv1.CollaborateResponse{
			Doc: req.GetDoc(),
			Payload: &collabv1.CollaborateResponse_Close{
				Close: &collabv1.Close{
					Reason: ecollab.CloseReasonLineageMismatch,
					Message: ecollab.EncodeLineageMismatch(ecollab.LineageMismatch{
						Lineage: sessionLineage,
						Reason:  ecollab.LineageEndReasonFrozen,
						Version: 12,
					}),
				},
			},
		})
	}
	f.mu.Unlock()

	// The copy comes from a session that has since been evicted and
	// seeded afresh: the server is not attached to the stream.
	local := newSessionServer(t).offlineCopy(t, "Orphaned")

	c := ecollab.NewClient(t.Context(), f)

	_, err := c.Subscribe(t.Context(), testDoc,
		ecollab.WithDoc(local), ecollab.WithLineage(staleLineage))

	var mismatch *ecollab.LineageMismatchError
	if !errors.As(err, &mismatch) {
		t.Fatalf("Subscribe error = %v, want a *LineageMismatchError", err)
	}

	if mismatch.Declared != staleLineage || mismatch.Current != sessionLineage {
		t.Errorf("mismatch = declared %q, current %q; want %q, %q",
			mismatch.Declared, mismatch.Current, staleLineage, sessionLineage)
	}

	if mismatch.Reason != ecollab.LineageEndReasonFrozen {
		t.Errorf("mismatch reason = %q, want %q",
			mismatch.Reason, ecollab.LineageEndReasonFrozen)
	}

	if mismatch.Version != 12 {
		t.Errorf("mismatch version = %d, want 12", mismatch.Version)
	}

	if got := ecollab.Reason(err); got != ecollab.CloseReasonLineageMismatch {
		t.Errorf("Reason = %q, want %q", got, ecollab.CloseReasonLineageMismatch)
	}

	var closed *ecollab.CloseError
	if !errors.As(err, &closed) || closed.Doc != testDoc {
		t.Errorf("no *CloseError for %q under %v", testDoc, err)
	}

	if got := titleOf(t, local); got != "Orphaned" {
		t.Errorf("local copy title = %q after the refusal, want it kept", got)
	}

	if n := len(step2s(f)); n != 0 {
		t.Errorf("sent %d sync step 2s to a refused subscribe", n)
	}
}

// TestClientLineageMismatchOnResync: the subscription ends with the
// typed error, and Read after Done still reaches its document.
func TestClientLineageMismatchOnResync(t *testing.T) {
	f := newFakeStream()
	srv := newSessionServer(t)
	srv.attach(f)

	c := ecollab.NewClient(t.Context(), f)

	sub, err := c.Subscribe(t.Context(), testDoc)
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}

	f.mu.Lock()
	f.onSend = func(f *fakeStream, req *collabv1.CollaborateRequest) {
		if _, ok := req.GetPayload().(*collabv1.CollaborateRequest_Subscribe); !ok {
			return
		}

		f.push(&collabv1.CollaborateResponse{
			Doc: req.GetDoc(),
			Payload: &collabv1.CollaborateResponse_Close{
				Close: &collabv1.Close{
					Reason: ecollab.CloseReasonLineageMismatch,
					Message: ecollab.EncodeLineageMismatch(ecollab.LineageMismatch{
						Lineage: staleLineage,
					}),
				},
			},
		})
	}
	f.mu.Unlock()

	_, err = sub.Resync(t.Context())

	var mismatch *ecollab.LineageMismatchError
	if !errors.As(err, &mismatch) || mismatch.Declared != sessionLineage {
		t.Fatalf("Resync error = %v, want a mismatch declaring %q",
			err, sessionLineage)
	}

	if mismatch.Reason != ecollab.LineageEndReasonUnknown {
		t.Errorf("mismatch reason = %q, want %q for a message without one",
			mismatch.Reason, ecollab.LineageEndReasonUnknown)
	}

	<-sub.Done()

	err = sub.Read(func(doc *goyjs.Doc) error {
		if got := titleOf(t, doc); got != "Seeded" {
			t.Errorf("title read after Done = %q, want Seeded", got)
		}

		return nil
	})
	if err != nil {
		t.Errorf("Read after a lineage mismatch: %v", err)
	}
}

// TestClientHoldsStep2DuringSoftStop: a step 1 that arrives while a
// publish soft-stop is announced is not answered, Subscribe returns
// with the edits still owed, and the subscription re-handshakes when
// the soft-stop clears, which is when the step 2 goes out and
// WaitDelivered returns.
func TestClientHoldsStep2DuringSoftStop(t *testing.T) {
	f := newFakeStream()
	srv := newSessionServer(t)
	srv.before = []*collabv1.CollaborateResponse{
		publishEvent(ecollab.EventPublishInProgress),
	}
	srv.attach(f)

	local := srv.offlineCopy(t, "Held back")

	c := ecollab.NewClient(t.Context(), f)

	sub, err := c.Subscribe(t.Context(), testDoc,
		ecollab.WithDoc(local), ecollab.WithLineage(sessionLineage))
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}

	if n := len(step2s(f)); n != 0 {
		t.Fatalf("sent %d sync step 2s during a soft-stop", n)
	}

	short, cancel := context.WithTimeout(t.Context(), 20*time.Millisecond)
	defer cancel()

	if err := sub.WaitDelivered(short); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("WaitDelivered during the soft-stop = %v, want it still waiting", err)
	}

	if titleOf(t, local) != "Held back" {
		t.Error("the local copy changed while the step 2 was held")
	}

	srv.set(func(srv *sessionServer) { srv.before = nil })

	f.push(publishEvent(ecollab.EventPublishCleared))

	if err := sub.WaitDelivered(waitCtx(t)); err != nil {
		t.Fatalf("WaitDelivered after the soft-stop cleared: %v", err)
	}

	if n := len(step2s(f)); n != 1 {
		t.Errorf("sent %d sync step 2s, want 1", n)
	}

	if got := srv.title(t); got != "Held back" {
		t.Errorf("server title = %q, want the held edit", got)
	}

	// The handshake, the re-handshake, and its confirmation.
	subs := subscribesSent(f)
	if len(subs) != 3 || subs[1].GetLineage() != sessionLineage {
		t.Errorf("re-handshake = %d subscribes, last declaring %q",
			len(subs), subs[len(subs)-1].GetLineage())
	}
}

// TestClientResendsARefusedStep2: a publish_in_progress answering a
// step 2 is its refusal; Subscribe returns with the edits owed, the
// step 2 is sent again after the soft-stop clears, and once that one
// is confirmed a later publish sends nothing.
func TestClientResendsARefusedStep2(t *testing.T) {
	f := newFakeStream()
	srv := newSessionServer(t)
	srv.refuse = true
	srv.attach(f)

	c := ecollab.NewClient(t.Context(), f)

	sub, err := c.Subscribe(t.Context(), testDoc,
		ecollab.WithDoc(srv.offlineCopy(t, "Refused once")),
		ecollab.WithLineage(sessionLineage))
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}

	if n := len(step2s(f)); n != 1 {
		t.Fatalf("sent %d sync step 2s, want 1", n)
	}

	if got := srv.title(t); got == "Refused once" {
		t.Fatal("the refused step 2 landed")
	}

	srv.set(func(srv *sessionServer) { srv.refuse = false })

	f.push(publishEvent(ecollab.EventPublishCleared))

	if err := sub.WaitDelivered(waitCtx(t)); err != nil {
		t.Fatalf("WaitDelivered: %v", err)
	}

	if n := len(step2s(f)); n != 2 {
		t.Errorf("sent %d sync step 2s, want 2", n)
	}

	if got := srv.title(t); got != "Refused once" {
		t.Errorf("server title = %q, want the resent edit", got)
	}

	// The second one was confirmed; the next publish, however long
	// after, finds nothing owed.
	before := len(subscribesSent(f))

	f.push(publishEvent(ecollab.EventPublishInProgress))
	f.push(publishEvent(ecollab.EventPublishCleared))

	time.Sleep(50 * time.Millisecond)

	if n := len(subscribesSent(f)); n != before {
		t.Errorf("%d subscribes after a publish with nothing owed, want %d",
			n, before)
	}
}

// TestClientReportsADroppedStep2: a step 2 the server neither took
// nor refused shows up in the confirming step 1, and WaitDelivered
// says so rather than waiting for ever. A Resync tries again.
func TestClientReportsADroppedStep2(t *testing.T) {
	f := newFakeStream()
	srv := newSessionServer(t)
	srv.drop = true
	srv.attach(f)

	c := ecollab.NewClient(t.Context(), f)

	sub, err := c.Subscribe(t.Context(), testDoc,
		ecollab.WithDoc(srv.offlineCopy(t, "Dropped")),
		ecollab.WithLineage(sessionLineage))
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}

	if err := sub.WaitDelivered(waitCtx(t)); !errors.Is(err, ecollab.ErrResyncDropped) {
		t.Fatalf("WaitDelivered = %v, want ErrResyncDropped", err)
	}

	if n := len(step2s(f)); n != 1 {
		t.Errorf("sent %d sync step 2s, want 1 and no resend loop", n)
	}

	srv.set(func(srv *sessionServer) { srv.drop = false })

	if _, err := sub.Resync(t.Context()); err != nil {
		t.Fatalf("Resync: %v", err)
	}

	if err := sub.WaitDelivered(waitCtx(t)); err != nil {
		t.Errorf("WaitDelivered after the Resync: %v", err)
	}

	if got := srv.title(t); got != "Dropped" {
		t.Errorf("server title = %q, want the edit", got)
	}
}

// TestClientStep2TooLarge: offline edits over MaxSyncStep2Bytes are
// not sent; the subscribe fails with the size, and the caller's copy
// is left alone even though the server had a catch-up for it.
func TestClientStep2TooLarge(t *testing.T) {
	f := newFakeStream()
	srv := newSessionServer(t)
	srv.attach(f)

	huge := strings.Repeat("x", ecollab.MaxSyncStep2Bytes+1)
	local := srv.offlineCopy(t, huge)

	// Somebody else edited the session meanwhile, so its step 2
	// has something for the copy.
	if err := srv.doc.MapSetString(ecollab.RootName, "language", "sv"); err != nil {
		t.Fatal(err)
	}

	before := local.EncodeStateV1()

	c := ecollab.NewClient(t.Context(), f)

	_, err := c.Subscribe(t.Context(), testDoc,
		ecollab.WithDoc(local), ecollab.WithLineage(sessionLineage))

	var tooLarge *ecollab.ResyncTooLargeError
	if !errors.As(err, &tooLarge) {
		t.Fatalf("Subscribe error = %v, want a *ResyncTooLargeError", err)
	}

	if tooLarge.Size <= tooLarge.Limit || tooLarge.Limit != ecollab.MaxSyncStep2Bytes {
		t.Errorf("size %d, limit %d", tooLarge.Size, tooLarge.Limit)
	}

	if n := len(step2s(f)); n != 0 {
		t.Errorf("sent %d sync step 2s over the limit", n)
	}

	lastOf[*collabv1.CollaborateRequest_Unsubscribe](t, f)

	eventually(t, "the given-up subscription to unregister", func() bool {
		return c.Subscription(testDoc) == nil
	})

	if !bytes.Equal(local.EncodeStateV1(), before) {
		t.Error("the local copy changed: the server's catch-up was applied to it")
	}
}

// TestClientResubscribesAfterGivingUp is the documented recovery for
// a step 2 too large to send: subscribe again straight away with an
// empty document. The Unsubscribe is on the stream before the new
// Subscribe, and the Synced still on its way for the subscribe that
// was given up does not end the new handshake early.
func TestClientResubscribesAfterGivingUp(t *testing.T) {
	f := newFakeStream()
	srv := newSessionServer(t)
	srv.attach(f)

	local := srv.offlineCopy(t, strings.Repeat("x", ecollab.MaxSyncStep2Bytes+1))

	c := ecollab.NewClient(t.Context(), f)

	_, err := c.Subscribe(t.Context(), testDoc,
		ecollab.WithDoc(local), ecollab.WithLineage(sessionLineage))

	var tooLarge *ecollab.ResyncTooLargeError
	if !errors.As(err, &tooLarge) {
		t.Fatalf("Subscribe error = %v, want a *ResyncTooLargeError", err)
	}

	sub, err := c.Subscribe(t.Context(), testDoc)
	if err != nil {
		t.Fatalf("Subscribe again: %v", err)
	}

	if got, err := sub.NewsDoc(); err != nil || got.GetTitle() != "Seeded" {
		t.Errorf("the new subscription's document = %v, %v; want the session's catch-up",
			got, err)
	}

	var order []string

	for _, req := range f.requests() {
		switch req.GetPayload().(type) {
		case *collabv1.CollaborateRequest_Subscribe:
			order = append(order, "subscribe")
		case *collabv1.CollaborateRequest_Unsubscribe:
			order = append(order, "unsubscribe")
		}
	}

	if strings.Join(order, ",") != "subscribe,unsubscribe,subscribe" {
		t.Errorf("requests went out as %v", order)
	}
}

// TestClientUnreadableStep1: a step 1 whose state vector does not
// decode ends the subscription instead of reaching goyjs, which
// would trap on it.
func TestClientUnreadableStep1(t *testing.T) {
	f := newFakeStream()

	f.mu.Lock()
	f.onSend = func(f *fakeStream, req *collabv1.CollaborateRequest) {
		if _, ok := req.GetPayload().(*collabv1.CollaborateRequest_Subscribe); !ok {
			return
		}

		f.push(&collabv1.CollaborateResponse{
			Doc: req.GetDoc(),
			Payload: &collabv1.CollaborateResponse_SyncStep1{
				SyncStep1: &collabv1.SyncStep1{StateVector: []byte{0xff}},
			},
		})
		f.push(&collabv1.CollaborateResponse{
			Doc: req.GetDoc(),
			Payload: &collabv1.CollaborateResponse_Synced{
				Synced: &collabv1.Synced{
					Mode: collabv1.SubscriptionMode_SUBSCRIPTION_MODE_READ_WRITE,
				},
			},
		})
	}
	f.mu.Unlock()

	c := ecollab.NewClient(t.Context(), f)

	if _, err := c.Subscribe(t.Context(), testDoc); err == nil {
		t.Fatal("Subscribe succeeded over an unreadable step 1")
	}

	if n := len(step2s(f)); n != 0 {
		t.Errorf("sent %d sync step 2s", n)
	}
}
