package ecollab_test

import (
	"context"
	"errors"
	"io"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/ttab/ecollab"
	collabv1 "github.com/ttab/elephant-api/elephant/collab/v1"
	"github.com/ttab/elephant-api/newsdoc"
	"github.com/ttab/goyjs"
	"google.golang.org/protobuf/encoding/protowire"
	"google.golang.org/protobuf/types/known/anypb"
)

// The client is exercised against a real server by the service's own
// integration tests. What is tested here is everything that does not
// need one: the handshake, the folding of Step 2 and updates into the
// local document, the two error shapes and the reason they carry, the
// ping echo, and the token refresh.

const (
	// budget bounds a wait for something the fake stream has already
	// been told to do.
	budget = 2 * time.Second

	testDoc = "3f2b8c1e-0000-4000-8000-000000000001"
)

// recvItem is one thing a fake stream hands its reader: a message,
// or the error the stream ends with.
type recvItem struct {
	msg *collabv1.CollaborateResponse
	err error
}

// fakeStream is an ecollab.Stream that a test scripts. onSend is the
// server's half: it sees every request and pushes whatever the
// server would have answered with.
type fakeStream struct {
	in chan recvItem

	mu     sync.Mutex
	sent   []*collabv1.CollaborateRequest
	onSend func(f *fakeStream, req *collabv1.CollaborateRequest)

	closeOnce sync.Once
}

func newFakeStream() *fakeStream {
	return &fakeStream{in: make(chan recvItem, 32)}
}

// syncedOnSubscribe answers every Subscribe with a Synced carrying
// mode, which is what the client's handshake waits for.
func (f *fakeStream) syncedOnSubscribe(mode collabv1.SubscriptionMode) {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.onSend = func(f *fakeStream, req *collabv1.CollaborateRequest) {
		if _, ok := req.GetPayload().(*collabv1.CollaborateRequest_Subscribe); !ok {
			return
		}

		f.push(&collabv1.CollaborateResponse{
			Doc: req.GetDoc(),
			Payload: &collabv1.CollaborateResponse_Synced{
				Synced: &collabv1.Synced{Mode: mode},
			},
		})
	}
}

func (f *fakeStream) Send(req *collabv1.CollaborateRequest) error {
	f.mu.Lock()
	f.sent = append(f.sent, req)
	onSend := f.onSend
	f.mu.Unlock()

	if onSend != nil {
		onSend(f, req)
	}

	return nil
}

func (f *fakeStream) Receive() (*collabv1.CollaborateResponse, error) {
	item, ok := <-f.in
	if !ok {
		return nil, io.EOF
	}

	if item.err != nil {
		return nil, item.err
	}

	return item.msg, nil
}

func (f *fakeStream) CloseRequest() error {
	f.closeOnce.Do(func() {
		close(f.in)
	})

	return nil
}

func (f *fakeStream) CloseResponse() error {
	return nil
}

func (f *fakeStream) push(msg *collabv1.CollaborateResponse) {
	f.in <- recvItem{msg: msg}
}

func (f *fakeStream) fail(err error) {
	f.in <- recvItem{err: err}
}

// requests is everything the client has sent so far.
func (f *fakeStream) requests() []*collabv1.CollaborateRequest {
	f.mu.Lock()
	defer f.mu.Unlock()

	return append([]*collabv1.CollaborateRequest(nil), f.sent...)
}

// lastOf returns the most recent request whose payload matches, and
// fails if there is none.
func lastOf[T any](t *testing.T, f *fakeStream) T {
	t.Helper()

	var zero T

	reqs := f.requests()

	for i := len(reqs) - 1; i >= 0; i-- {
		if p, ok := reqs[i].GetPayload().(T); ok {
			return p
		}
	}

	t.Fatalf("no request of type %T was sent (%d sent)", zero, len(reqs))

	return zero
}

// TestClientSubscribeFoldsStep2 is the handshake and the state
// transfer: the Step 2 the server answers a subscribe with lands in
// the subscription's own document, and the mode from Synced is what
// the subscription reports.
func TestClientSubscribeFoldsStep2(t *testing.T) {
	f := newFakeStream()

	f.mu.Lock()
	f.onSend = func(f *fakeStream, req *collabv1.CollaborateRequest) {
		if _, ok := req.GetPayload().(*collabv1.CollaborateRequest_Subscribe); !ok {
			return
		}

		f.push(&collabv1.CollaborateResponse{
			Doc: req.GetDoc(),
			Payload: &collabv1.CollaborateResponse_SyncStep2{
				SyncStep2: &collabv1.SyncStep2{Diff: seedUpdate(t)},
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

	sub, err := c.Subscribe(t.Context(), testDoc)
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}

	if got := sub.Mode(); got != ecollab.SubscriptionModeReadWrite {
		t.Errorf("mode = %q, want %q", got, ecollab.SubscriptionModeReadWrite)
	}

	// The state transfer is complete when Synced arrives, so the
	// document is readable with no further waiting.
	doc, err := sub.NewsDoc()
	if err != nil {
		t.Fatalf("NewsDoc: %v", err)
	}

	if doc.GetTitle() != "Seeded" {
		t.Errorf("materialised title = %q, want Seeded", doc.GetTitle())
	}

	// The subscribe carried this client's state vector, which is
	// what makes the server's Step 2 a diff rather than the whole
	// document.
	sent := lastOf[*collabv1.CollaborateRequest_Subscribe](t, f)
	if len(sent.Subscribe.GetStateVector()) == 0 {
		t.Error("the subscribe carried no state vector")
	}
}

// TestClientSubscribeOptions: the subscribe-time options travel in
// the Subscribe message.
func TestClientSubscribeOptions(t *testing.T) {
	f := newFakeStream()
	f.syncedOnSubscribe(collabv1.SubscriptionMode_SUBSCRIPTION_MODE_READ_ONLY)

	c := ecollab.NewClient(t.Context(), f)

	_, err := c.Subscribe(t.Context(), testDoc,
		ecollab.Observer(),
		ecollab.AdvertisePresence(false),
		ecollab.FreezeOnWorkflowStates("done", "approved"))
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}

	got := lastOf[*collabv1.CollaborateRequest_Subscribe](t, f).Subscribe

	if !got.GetObserver() {
		t.Error("observer = false, want true")
	}

	if got.AdvertisePresence == nil || got.GetAdvertisePresence() {
		t.Errorf("advertise_presence = %v, want a set false",
			got.AdvertisePresence)
	}

	if len(got.GetFreezeOnWorkflowStates()) != 2 {
		t.Errorf("freeze_on_workflow_states = %v, want two states",
			got.GetFreezeOnWorkflowStates())
	}
}

// TestClientSubscribeTwiceRefused: one subscription per document on
// a stream. Resync re-syncs the open one.
func TestClientSubscribeTwiceRefused(t *testing.T) {
	f := newFakeStream()
	f.syncedOnSubscribe(collabv1.SubscriptionMode_SUBSCRIPTION_MODE_READ_WRITE)

	c := ecollab.NewClient(t.Context(), f)

	if _, err := c.Subscribe(t.Context(), testDoc); err != nil {
		t.Fatalf("Subscribe: %v", err)
	}

	_, err := c.Subscribe(t.Context(), testDoc)
	if !errors.Is(err, ecollab.ErrSubscribed) {
		t.Fatalf("second Subscribe = %v, want ErrSubscribed", err)
	}
}

// TestClientUpdateForwardsTheDiff: a mutation through Update is
// applied locally and forwarded as the update it produced, and Seed
// is the same thing tagged as structural seeding.
func TestClientUpdateForwardsTheDiff(t *testing.T) {
	f := newFakeStream()
	f.syncedOnSubscribe(collabv1.SubscriptionMode_SUBSCRIPTION_MODE_READ_WRITE)

	c := ecollab.NewClient(t.Context(), f)

	sub, err := c.Subscribe(t.Context(), testDoc)
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}

	err = sub.Update(t.Context(), func(doc *goyjs.Doc) error {
		return doc.MapSetString(ecollab.RootName, "title", "Typed")
	})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}

	sent := lastOf[*collabv1.CollaborateRequest_Update](t, f).Update

	if len(sent.GetUpdate()) == 0 {
		t.Error("the forwarded update was empty")
	}

	if sent.GetSeed() {
		t.Error("seed = true on an Update, want false")
	}

	err = sub.Seed(t.Context(), func(doc *goyjs.Doc) error {
		return doc.MapSetString(ecollab.RootName, "language", "sv")
	})
	if err != nil {
		t.Fatalf("Seed: %v", err)
	}

	if !lastOf[*collabv1.CollaborateRequest_Update](t, f).Update.GetSeed() {
		t.Error("seed = false on a Seed, want true")
	}

	// A mutation that changed nothing has nothing to forward.
	err = sub.Update(t.Context(), func(_ *goyjs.Doc) error {
		return nil
	})
	if !errors.Is(err, ecollab.ErrNoChange) {
		t.Errorf("a no-op Update = %v, want ErrNoChange", err)
	}
}

// TestClientInboundUpdateIsApplied: a live update from another
// participant reaches the local document, and the callback sees it
// with its seed tag.
func TestClientInboundUpdateIsApplied(t *testing.T) {
	f := newFakeStream()
	f.syncedOnSubscribe(collabv1.SubscriptionMode_SUBSCRIPTION_MODE_READ_WRITE)

	c := ecollab.NewClient(t.Context(), f)

	seen := make(chan bool, 4)

	sub, err := c.Subscribe(t.Context(), testDoc,
		ecollab.OnUpdate(func(_ []byte, seed bool) {
			seen <- seed
		}))
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}

	f.push(&collabv1.CollaborateResponse{
		Doc: testDoc,
		Payload: &collabv1.CollaborateResponse_Update{
			Update: &collabv1.Update{Update: seedUpdate(t), Seed: true},
		},
	})

	select {
	case seed := <-seen:
		if !seed {
			t.Error("OnUpdate saw seed = false, want true")
		}
	case <-time.After(budget):
		t.Fatal("OnUpdate was not called")
	}

	doc, err := sub.NewsDoc()
	if err != nil {
		t.Fatalf("NewsDoc: %v", err)
	}

	if doc.GetTitle() != "Seeded" {
		t.Errorf("materialised title = %q, want Seeded", doc.GetTitle())
	}
}

// TestClientCloseEndsOneSubscription: a per-document Close ends that
// subscription with a *CloseError carrying the reason, and leaves
// the stream open.
func TestClientCloseEndsOneSubscription(t *testing.T) {
	f := newFakeStream()
	f.syncedOnSubscribe(collabv1.SubscriptionMode_SUBSCRIPTION_MODE_READ_WRITE)

	c := ecollab.NewClient(t.Context(), f)

	sub, err := c.Subscribe(t.Context(), testDoc)
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}

	f.push(&collabv1.CollaborateResponse{
		Doc: testDoc,
		Payload: &collabv1.CollaborateResponse_Close{
			Close: &collabv1.Close{
				Reason:  ecollab.CloseReasonSessionTerminated,
				Message: "frozen for a publish",
			},
		},
	})

	select {
	case <-sub.Done():
	case <-time.After(budget):
		t.Fatal("the subscription did not end")
	}

	var closed *ecollab.CloseError
	if !errors.As(sub.Err(), &closed) {
		t.Fatalf("subscription error = %v, want a *CloseError", sub.Err())
	}

	if closed.Doc != testDoc {
		t.Errorf("close doc = %q, want %q", closed.Doc, testDoc)
	}

	if got := ecollab.Reason(sub.Err()); got != ecollab.CloseReasonSessionTerminated {
		t.Errorf("Reason = %q, want %q",
			got, ecollab.CloseReasonSessionTerminated)
	}

	select {
	case <-c.Done():
		t.Fatalf("the stream ended with the subscription: %v", c.Err())
	default:
	}
}

// TestClientSubscribeRefusedIsAnError: a subscribe the server
// answers with a Close instead of a Synced is the Subscribe call's
// error, so a caller never holds a subscription that was refused.
func TestClientSubscribeRefusedIsAnError(t *testing.T) {
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
					Reason: ecollab.CloseReasonNoActiveSession,
				},
			},
		})
	}
	f.mu.Unlock()

	c := ecollab.NewClient(t.Context(), f)

	_, err := c.Subscribe(t.Context(), testDoc)
	if got := ecollab.Reason(err); got != ecollab.CloseReasonNoActiveSession {
		t.Fatalf("Subscribe error = %v (reason %q), want no_active_session",
			err, got)
	}

	if c.Subscription(testDoc) != nil {
		t.Error("a refused subscribe left a subscription behind")
	}
}

// TestClientStreamRefusalCarriesTheReason: a connection-wide refusal
// is the stream's terminal status, and the structured reason travels
// in the error metadata the service attaches. Reading it is what
// tells rate_limited from subscription_limit, which share a code.
func TestClientStreamRefusalCarriesTheReason(t *testing.T) {
	f := newFakeStream()
	f.syncedOnSubscribe(collabv1.SubscriptionMode_SUBSCRIPTION_MODE_READ_WRITE)

	c := ecollab.NewClient(t.Context(), f)

	sub, err := c.Subscribe(t.Context(), testDoc)
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}

	f.fail(refusal(t,
		connect.CodeResourceExhausted, ecollab.CloseReasonRateLimited))

	if err := c.Wait(waitCtx(t)); err == nil {
		t.Fatal("the stream ended cleanly, want a refusal")
	}

	var streamErr *ecollab.StreamError
	if !errors.As(c.Err(), &streamErr) {
		t.Fatalf("stream error = %v, want a *StreamError", c.Err())
	}

	if streamErr.Code != connect.CodeResourceExhausted {
		t.Errorf("code = %v, want resource_exhausted", streamErr.Code)
	}

	if got := ecollab.Reason(c.Err()); got != ecollab.CloseReasonRateLimited {
		t.Errorf("Reason = %q, want %q", got, ecollab.CloseReasonRateLimited)
	}

	// The refusal took the subscription with it, and it reports the
	// same thing rather than a close of its own.
	select {
	case <-sub.Done():
	case <-time.After(budget):
		t.Fatal("the subscription outlived the stream")
	}

	if got := ecollab.Reason(sub.Err()); got != ecollab.CloseReasonRateLimited {
		t.Errorf("subscription Reason = %q, want %q",
			got, ecollab.CloseReasonRateLimited)
	}

	// Sending on a stream that has ended reports how it ended, not
	// the write's own failure.
	if got := ecollab.Reason(sub.Forward(t.Context(), []byte{1}, false)); got !=
		ecollab.CloseReasonRateLimited {
		t.Errorf("Forward after the refusal reported %q", got)
	}
}

// TestClientCleanEndIsNotAnError: a half-close is how a client says
// it is leaving, and the stream ending after one is not a failure.
func TestClientCleanEndIsNotAnError(t *testing.T) {
	f := newFakeStream()
	f.syncedOnSubscribe(collabv1.SubscriptionMode_SUBSCRIPTION_MODE_READ_WRITE)

	c := ecollab.NewClient(t.Context(), f)

	if _, err := c.Subscribe(t.Context(), testDoc); err != nil {
		t.Fatalf("Subscribe: %v", err)
	}

	if err := c.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	if err := c.Err(); err != nil {
		t.Errorf("Err after a clean end = %v, want nil", err)
	}
}

// TestClientServerPingIsEchoable: the ping is kept verbatim, because
// the freshness check a freeze-snapshot makes compares the exact
// string.
func TestClientServerPingIsEchoable(t *testing.T) {
	f := newFakeStream()

	c := ecollab.NewClient(t.Context(), f)

	const witness = "2026-09-18T09:41:12.123456789Z"

	f.push(&collabv1.CollaborateResponse{
		Payload: &collabv1.CollaborateResponse_ServerPing{
			ServerPing: &collabv1.ServerPing{ServerTime: witness},
		},
	})

	deadline := time.Now().Add(budget)
	for c.LastServerPing() == "" && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}

	if got := c.LastServerPing(); got != witness {
		t.Errorf("LastServerPing = %q, want %q", got, witness)
	}
}

// TestClientEventReachesTheCallback: a stateless event is delivered
// with its JSON payload as the service wrote it.
func TestClientEventReachesTheCallback(t *testing.T) {
	f := newFakeStream()
	f.syncedOnSubscribe(collabv1.SubscriptionMode_SUBSCRIPTION_MODE_READ_WRITE)

	c := ecollab.NewClient(t.Context(), f)

	events := make(chan ecollab.Event, 4)

	_, err := c.Subscribe(t.Context(), testDoc,
		ecollab.OnEvent(func(ev ecollab.Event) {
			events <- ev
		}))
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}

	const data = `{"initiated_by":"core://application/bob"}`

	f.push(&collabv1.CollaborateResponse{
		Doc: testDoc,
		Payload: &collabv1.CollaborateResponse_Event{
			Event: &collabv1.Event{
				Name: ecollab.EventPublishInProgress,
				Data: []byte(data),
			},
		},
	})

	select {
	case ev := <-events:
		if ev.Name != ecollab.EventPublishInProgress {
			t.Errorf("event name = %q, want %q",
				ev.Name, ecollab.EventPublishInProgress)
		}

		if string(ev.Data) != data {
			t.Errorf("event data = %q, want %q", ev.Data, data)
		}
	case <-time.After(budget):
		t.Fatal("OnEvent was not called")
	}
}

// TestClientRefreshesFromTheTokenSource: the client asks the source
// for the next token before the current one expires and sends it as
// an auth_refresh, which is what keeps a long session from dropping
// mid-edit.
func TestClientRefreshesFromTheTokenSource(t *testing.T) {
	f := newFakeStream()

	src := &scriptedTokens{
		tokens: []ecollab.Token{
			{Bearer: "first", Expires: time.Now().Add(300 * time.Millisecond)},
			{Bearer: "second", Expires: time.Now().Add(time.Hour)},
		},
	}

	c := ecollab.NewClient(t.Context(), f,
		ecollab.WithTokenSource(src),
		ecollab.WithRefreshLeeway(100*time.Millisecond))

	defer func() {
		_ = c.Close()
	}()

	deadline := time.Now().Add(budget)

	for time.Now().Before(deadline) {
		if hasRefresh(f, "second") {
			return
		}

		time.Sleep(10 * time.Millisecond)
	}

	t.Fatalf("no auth_refresh carrying the second token within %s", budget)
}

func hasRefresh(f *fakeStream, token string) bool {
	for _, req := range f.requests() {
		p, ok := req.GetPayload().(*collabv1.CollaborateRequest_AuthRefresh)
		if ok && p.AuthRefresh.GetToken() == token {
			return true
		}
	}

	return false
}

// scriptedTokens hands out a scripted sequence, repeating the last
// one.
type scriptedTokens struct {
	mu     sync.Mutex
	tokens []ecollab.Token
	calls  int
}

func (s *scriptedTokens) Token(_ context.Context) (ecollab.Token, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	tok := s.tokens[min(s.calls, len(s.tokens)-1)]
	s.calls++

	return tok, nil
}

// TestClientUnsubscribeKeepsTheStream: giving up one subscription
// leaves the connection and its other documents alone.
func TestClientUnsubscribeKeepsTheStream(t *testing.T) {
	f := newFakeStream()
	f.syncedOnSubscribe(collabv1.SubscriptionMode_SUBSCRIPTION_MODE_READ_WRITE)

	c := ecollab.NewClient(t.Context(), f)

	sub, err := c.Subscribe(t.Context(), testDoc)
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}

	if err := sub.Unsubscribe(t.Context()); err != nil {
		t.Fatalf("Unsubscribe: %v", err)
	}

	lastOf[*collabv1.CollaborateRequest_Unsubscribe](t, f)

	if c.Subscription(testDoc) != nil {
		t.Error("the subscription is still registered")
	}

	select {
	case <-c.Done():
		t.Fatalf("the stream ended with the unsubscribe: %v", c.Err())
	default:
	}
}

// TestDialRejectsAnEmptyEndpoint covers the two arguments Dial can
// answer for without opening anything.
func TestDialRejectsAnEmptyEndpoint(t *testing.T) {
	if _, err := ecollab.Dial(t.Context(), "", ecollab.HTTP2Client()); err == nil {
		t.Error("Dial with no endpoint returned no error")
	}

	if _, err := ecollab.Dial(t.Context(), "http://localhost:1", nil); err == nil {
		t.Error("Dial with no HTTP client returned no error")
	}
}

// seedUpdate is a Yjs update carrying a small document under the
// tree contract, built through the library's own seeding.
func seedUpdate(t *testing.T) []byte {
	t.Helper()

	update, err := ecollab.BuildSeedUpdate(&newsdoc.Document{
		Uuid:  testDoc,
		Type:  "core/article",
		Title: "Seeded",
	}, ecollab.RootName)
	if err != nil {
		t.Fatalf("BuildSeedUpdate: %v", err)
	}

	return update
}

// refusal is the terminal error the service ends a refused stream
// with: a coded Connect error carrying the reason in the key/value
// detail its RPC layer attaches.
func refusal(t *testing.T, code connect.Code, reason string) error {
	t.Helper()

	detail, err := connect.NewErrorDetail(&anypb.Any{
		TypeUrl: "type.googleapis.com/elephantine.rpc.ErrorMeta",
		Value:   errorMetaBytes("reason", reason),
	})
	if err != nil {
		t.Fatalf("build the error detail: %v", err)
	}

	cErr := connect.NewError(code, errors.New(reason))
	cErr.AddDetail(detail)

	return cErr
}

// errorMetaBytes encodes elephantine.rpc.ErrorMeta's
// map<string, string> meta = 1 with one entry.
func errorMetaBytes(key, value string) []byte {
	entry := protowire.AppendTag(nil, 1, protowire.BytesType)
	entry = protowire.AppendString(entry, key)
	entry = protowire.AppendTag(entry, 2, protowire.BytesType)
	entry = protowire.AppendString(entry, value)

	out := protowire.AppendTag(nil, 1, protowire.BytesType)

	return protowire.AppendBytes(out, entry)
}

func waitCtx(t *testing.T) context.Context {
	t.Helper()

	ctx, cancel := context.WithTimeout(t.Context(), budget)
	t.Cleanup(cancel)

	return ctx
}
