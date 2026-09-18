package ecollab

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"time"

	"connectrpc.com/connect"
	collabv1 "github.com/ttab/elephant-api/elephant/collab/v1"
	"github.com/ttab/elephant-api/elephant/collab/v1/collabv1connect"
)

// Stateless event names. A server-issued event scoped to a document
// arrives as an Event with one of these names and a JSON payload the
// service documents; an event with a name that is not here is newer
// than this library and should be ignored rather than guessed at.
const (
	// EventPublishInProgress: a publish has soft-stopped the
	// session. Updates are paused until it clears, awareness is
	// not. The data carries "initiated_by" and "expires_at".
	EventPublishInProgress = "publish_in_progress"

	// EventPublishCleared: the soft-stop is over, either because the
	// publish finished or because it expired. Updates flow again.
	// The data carries "by" and "reason".
	EventPublishCleared = "publish_cleared"

	// EventSessionReset: an operator discarded the session's
	// collaborative state. The document is not gone, but everything
	// this subscription has locally is. The data carries "by",
	// "reason" and "session_id".
	EventSessionReset = "session_reset"
)

// DefaultRefreshLeeway is how long before a token's expiry the
// client asks its token source for the next one. A minute is
// comfortably more than a round trip to an OIDC provider and
// comfortably less than the shortest token life anything mints.
const DefaultRefreshLeeway = time.Minute

const (
	// refreshRetry is how long the refresh loop waits before asking
	// the token source again, after a failure or after a token that
	// did not move the expiry forward. The connection survives a few
	// of these before the server's own expiry guard ends it.
	refreshRetry = 15 * time.Second

	// refreshFloor keeps the loop off a hot spin when the token it
	// was handed is already inside its own leeway.
	refreshFloor = time.Second
)

// errorMetaTypeName is the Protobuf message the service attaches a
// refusal reason to. It is named rather than imported: reading the
// name off the wire is what lets this library stay free of the
// service's server-side dependencies. The shape it decodes is
// map<string, string> meta = 1.
const errorMetaTypeName = "elephantine.rpc.ErrorMeta"

// metaReason is the key carrying the structured refusal reason, on
// the terminal error's metadata and in the message above. It is part
// of the declaration — see the Collaborate comment in service.proto.
const metaReason = "reason"

// ErrClosed is returned by a call on a client or a subscription
// whose stream has already ended cleanly. A stream that ended
// because the server refused the connection reports the refusal
// instead; see StreamError.
var ErrClosed = errors.New("the stream has ended")

// ErrSubscribed is returned by Subscribe for a document the client
// already holds a subscription for. Resync re-syncs an open one;
// Unsubscribe gives up the slot.
var ErrSubscribed = errors.New("already subscribed to the document")

// ErrNoChange is returned by Update and Seed when the mutation
// produced no Yjs update, so there was nothing to forward. It is
// informational: a caller that mutates optimistically can ignore it.
var ErrNoChange = errors.New("the mutation produced no update")

// Token is a bearer and when it stops being one.
type Token struct {
	// Bearer is the token itself, without the "Bearer " prefix.
	Bearer string

	// Expires is when the token stops being accepted. The zero value
	// means the source does not say, and then nothing is scheduled
	// against it: the connection lives until the server's own expiry
	// guard ends it.
	Expires time.Time
}

// TokenSource hands out the bearers a connection lives on: the one
// the stream opens with, and the fresher ones the client sends as
// auth_refresh before the current one expires.
//
// Token is called from the client's own goroutine and may block, but
// a source that blocks past the expiry it last reported loses the
// connection.
type TokenSource interface {
	Token(ctx context.Context) (Token, error)
}

// StaticToken is a TokenSource that always answers with the same
// token. Useful for a short-lived program and for tests; a
// long-running one wants a source that can mint a new one, because
// the server ends a connection whose bearer expires.
func StaticToken(bearer string, expires time.Time) TokenSource {
	return staticToken{tok: Token{Bearer: bearer, Expires: expires}}
}

type staticToken struct {
	tok Token
}

func (s staticToken) Token(_ context.Context) (Token, error) {
	return s.tok, nil
}

// Event is a server-issued event scoped to one document.
type Event struct {
	// Doc is the document the event is about.
	Doc string

	// Name is one of the Event* constants.
	Name string

	// Data is the event's JSON payload, passed through as the
	// service wrote it.
	Data []byte
}

// Stream is the client half of the Collaborate bidirectional stream.
// The generated
// *connect.BidiStreamForClient[collabv1.CollaborateRequest,
// collabv1.CollaborateResponse] implements it, and Dial builds one;
// the interface is what lets a caller that already has a
// CollaborationService client of its own hand the stream over to
// NewClient instead.
type Stream interface {
	Send(*collabv1.CollaborateRequest) error
	Receive() (*collabv1.CollaborateResponse, error)
	CloseRequest() error
	CloseResponse() error
}

// Option configures a Client.
type Option func(*clientConfig)

type clientConfig struct {
	tokens         TokenSource
	leeway         time.Duration
	connectOptions []connect.ClientOption
	onMessage      func(*collabv1.CollaborateResponse)
}

// WithTokenSource makes the client responsible for the connection's
// authorization: the stream opens with the source's current token,
// and the client sends an auth_refresh with a fresh one before that
// token expires. Without a source the HTTP client has to carry the
// bearer itself, and the connection ends when it expires.
//
// The refreshed token must be for the same subject. The server
// counts a refresh for a different one and drops it, which is worth
// knowing about a source that can switch identities under you.
func WithTokenSource(src TokenSource) Option {
	return func(c *clientConfig) {
		c.tokens = src
	}
}

// WithRefreshLeeway overrides DefaultRefreshLeeway, how long before
// expiry the next token is fetched. Zero or negative keeps the
// default.
func WithRefreshLeeway(d time.Duration) Option {
	return func(c *clientConfig) {
		if d > 0 {
			c.leeway = d
		}
	}
}

// WithConnectOptions passes options through to the generated Connect
// client Dial builds — an interceptor, a compression choice. Ignored
// by NewClient, which is handed a stream that has already been
// opened.
func WithConnectOptions(opts ...connect.ClientOption) Option {
	return func(c *clientConfig) {
		c.connectOptions = append(c.connectOptions, opts...)
	}
}

// OnMessage registers a tap on every response the stream delivers,
// called before the client acts on it and from the read loop, so it
// must not block. It is the whole conversation rather than the parts
// a subscription reacts to: for logging, for tracing, and for a test
// that asserts on the order messages arrived in.
func OnMessage(fn func(*collabv1.CollaborateResponse)) Option {
	return func(c *clientConfig) {
		c.onMessage = fn
	}
}

// Client is one Collaborate connection: a single bidirectional
// stream carrying any number of document subscriptions, keyed on the
// document id every message travels with.
//
// It is safe for concurrent use. The document state each subscription
// folds its stream into is guarded, and a caller reaches it through
// Subscription.Read rather than by holding the Y.Doc.
type Client struct {
	stream    Stream
	onMessage func(*collabv1.CollaborateResponse)

	// sendMu serialises writes: connect allows Send and Receive from
	// different goroutines but not two concurrent Sends.
	sendMu sync.Mutex

	mu   sync.Mutex
	subs map[string]*Subscription

	// opened is every subscription this client has made, including
	// the ones that have since been closed or given up. It is what
	// Close frees the documents of; subs only holds the live ones.
	opened   []*Subscription
	lastPing string
	err      error

	done     chan struct{}
	doneOnce sync.Once

	closeOnce sync.Once
	closeErr  error

	stopBackground context.CancelFunc
}

// HTTP2Transport returns a transport that can carry the Collaborate
// stream, which is the one thing about this client that is easy to
// get wrong.
//
// connect-go answers a bidirectional stream that arrived over
// HTTP/1.1 with a bare 505, before any handler runs, and Go only
// negotiates HTTP/2 through the TLS ALPN handshake. So a client
// talking to a plain-HTTP endpoint — a pod on the cluster network, a
// local instance, a test server — gets HTTP/1.1 and a 505 unless it
// asks for unencrypted HTTP/2 explicitly. This transport does:
//
//	http.Transport{Protocols: HTTP2 + UnencryptedHTTP2}
//
// HTTP/1.1 is deliberately left out rather than kept as a fallback,
// because falling back is what produces the 505: a transport that
// cannot reach the stream should fail at the connection rather than
// at the RPC. It also means an ingress that does not forward HTTP/2
// to the pod is a connection failure, which is the honest report.
func HTTP2Transport() *http.Transport {
	var t http.Transport

	t.Protocols = new(http.Protocols)
	t.Protocols.SetHTTP2(true)
	t.Protocols.SetUnencryptedHTTP2(true)

	return &t
}

// HTTP2Client returns an http.Client on HTTP2Transport. It carries no
// authorization of its own: pass WithTokenSource to Dial, or wrap the
// client's transport.
func HTTP2Client() *http.Client {
	return &http.Client{Transport: HTTP2Transport()}
}

// Dial opens a Collaborate stream against endpoint — the base URL of
// the collab service, not a procedure path — and starts reading it.
//
// The httpClient must be able to speak HTTP/2 to the endpoint; see
// HTTP2Client. The context governs the whole connection: cancelling
// it ends the stream, so it must outlive the session and must not be
// a per-request context unless the session is the request.
//
// Authorization comes from a TokenSource if one is given, and from
// the HTTP client otherwise.
func Dial(
	ctx context.Context,
	endpoint string,
	httpClient connect.HTTPClient,
	opts ...Option,
) (*Client, error) {
	if endpoint == "" {
		return nil, errors.New("no endpoint given")
	}

	if httpClient == nil {
		return nil, errors.New("no HTTP client given")
	}

	cfg := resolveConfig(opts)

	var (
		initial  Token
		copts    = cfg.connectOptions
		withAuth bool
	)

	if cfg.tokens != nil {
		tok, err := cfg.tokens.Token(ctx)
		if err != nil {
			return nil, fmt.Errorf("get an access token: %w", err)
		}

		if tok.Bearer == "" {
			return nil, errors.New("the token source returned an empty token")
		}

		initial = tok
		withAuth = true

		copts = append(copts, connect.WithInterceptors(
			bearerInterceptor{bearer: tok.Bearer}))
	}

	svc := collabv1connect.NewCollaborationServiceClient(
		httpClient, endpoint, copts...)

	c, bg := newClient(ctx, svc.Collaborate(ctx), cfg)

	if withAuth {
		go c.refreshLoop(bg, cfg.tokens, cfg.leeway, initial)
	}

	return c, nil
}

// NewClient drives a stream the caller opened itself, for a program
// that already has a generated CollaborationService client. The
// context governs the client's background work; the stream's own
// lifetime is the one it was opened with.
//
// Dial is the ordinary way in.
func NewClient(ctx context.Context, stream Stream, opts ...Option) *Client {
	cfg := resolveConfig(opts)

	c, bg := newClient(ctx, stream, cfg)

	if cfg.tokens != nil {
		tok, err := cfg.tokens.Token(ctx)
		if err == nil && tok.Bearer != "" {
			go c.refreshLoop(bg, cfg.tokens, cfg.leeway, tok)
		}
	}

	return c
}

func resolveConfig(opts []Option) clientConfig {
	cfg := clientConfig{leeway: DefaultRefreshLeeway}

	for _, opt := range opts {
		opt(&cfg)
	}

	return cfg
}

// newClient starts the read loop and returns the client together
// with the context its background work runs under, which is the
// caller's cancelled by the client's own teardown.
func newClient(
	ctx context.Context, stream Stream, cfg clientConfig,
) (*Client, context.Context) {
	bg, cancel := context.WithCancel(ctx)

	c := &Client{
		stream:         stream,
		onMessage:      cfg.onMessage,
		subs:           make(map[string]*Subscription),
		done:           make(chan struct{}),
		stopBackground: cancel,
	}

	// The stream's own failure is how a cancelled context reaches
	// the read loop, so nothing else has to watch for it.
	go c.readLoop()

	return c, bg
}

// Done is closed when the stream has ended, whichever side ended it.
// Err says how.
func (c *Client) Done() <-chan struct{} {
	return c.done
}

// Err is how the stream ended: nil while it is open and nil when it
// ended cleanly, and a *StreamError carrying the code and the
// structured reason when the server refused the connection.
func (c *Client) Err() error {
	c.mu.Lock()
	defer c.mu.Unlock()

	return c.err
}

// Wait blocks until the stream has ended and reports how, or until
// the context is done.
func (c *Client) Wait(ctx context.Context) error {
	select {
	case <-c.done:
		return c.Err()
	case <-ctx.Done():
		return fmt.Errorf("wait for the stream to end: %w", ctx.Err())
	}
}

// LastServerPing is the most recent server time witness, verbatim as
// the service sent it, or "" if none has arrived yet.
//
// It is what makes a freeze-snapshot publish-what-you-see: echo it as
// SnapshotRequest.client_last_server_ping and the service can tell
// whether this client had seen everything it had when the snapshot
// was asked for. Reformatting it is how a client fails that check, so
// it travels as the string it arrived as.
func (c *Client) LastServerPing() string {
	c.mu.Lock()
	defer c.mu.Unlock()

	return c.lastPing
}

// Subscription is the open subscription for docID, or nil.
func (c *Client) Subscription(docID string) *Subscription {
	c.mu.Lock()
	defer c.mu.Unlock()

	return c.subs[docID]
}

// Refresh hands the connection a fresh bearer for the same subject,
// extending its expiry. A client with a TokenSource has this done for
// it; one without it must call this before its own token expires or
// the server ends the stream.
//
// The server validates the token and counts the outcome. A refusal —
// an invalid token, or one for a different subject — is not reported
// back: the refresh is dropped and the original bearer keeps
// governing the connection, which then ends at its expiry.
func (c *Client) Refresh(ctx context.Context, token string) error {
	if token == "" {
		return errors.New("no token given")
	}

	return c.send(ctx, &collabv1.CollaborateRequest{
		Payload: &collabv1.CollaborateRequest_AuthRefresh{
			AuthRefresh: &collabv1.AuthRefresh{Token: token},
		},
	})
}

// Close half-closes the stream — "done sending", which is how a
// client says it is leaving — and waits for the server to finish
// tearing the connection down. The service closes the subscriptions
// on its side, which is what releases their presence entries; the
// repository lock is the evictor's to release, not a disconnect's.
//
// It returns how the stream ended: nil when it ended cleanly. The
// documents the subscriptions own are closed, so nothing may read
// them afterwards. Close is idempotent, and the context Dial was
// given bounds the wait.
func (c *Client) Close() error {
	c.closeOnce.Do(func() {
		if err := c.stream.CloseRequest(); err != nil {
			c.closeErr = fmt.Errorf("half-close the stream: %w", err)
		}

		<-c.done

		_ = c.stream.CloseResponse()

		c.stopBackground()

		for _, s := range c.allSubscriptions() {
			s.closeDoc()
		}
	})

	if c.closeErr != nil {
		return c.closeErr
	}

	return c.Err()
}

// send ships one request. A stream that has already ended reports
// how it ended rather than the write's own failure, because the two
// race and only one of them says anything useful.
func (c *Client) send(
	ctx context.Context, msg *collabv1.CollaborateRequest,
) error {
	if err := c.ended(); err != nil {
		return err
	}

	if err := ctx.Err(); err != nil {
		return fmt.Errorf("send on the stream: %w", err)
	}

	c.sendMu.Lock()
	defer c.sendMu.Unlock()

	if err := c.stream.Send(msg); err != nil {
		if ended := c.ended(); ended != nil {
			return ended
		}

		return fmt.Errorf("send on the stream: %w", err)
	}

	return nil
}

// ended reports how the stream ended, or nil while it is open. A
// clean end is ErrClosed rather than nil, because a caller reaching
// this has something to be told.
func (c *Client) ended() error {
	select {
	case <-c.done:
	default:
		return nil
	}

	if err := c.Err(); err != nil {
		return err
	}

	return ErrClosed
}

func (c *Client) readLoop() {
	for {
		msg, err := c.stream.Receive()
		if err != nil {
			c.finish(err)

			return
		}

		c.dispatch(msg)
	}
}

// dispatch routes one response. A message for a document this client
// holds no subscription for is dropped: it is either the tail of a
// subscription that has just been given up, or a document another
// participant on a shared stream owns.
func (c *Client) dispatch(msg *collabv1.CollaborateResponse) {
	if c.onMessage != nil {
		c.onMessage(msg)
	}

	if p, ok := msg.GetPayload().(*collabv1.CollaborateResponse_ServerPing); ok {
		c.mu.Lock()
		c.lastPing = p.ServerPing.GetServerTime()
		c.mu.Unlock()

		return
	}

	s := c.Subscription(msg.GetDoc())
	if s == nil {
		return
	}

	switch p := msg.GetPayload().(type) {
	case *collabv1.CollaborateResponse_SyncStep2:
		s.apply(p.SyncStep2.GetDiff())
	case *collabv1.CollaborateResponse_Update:
		s.apply(p.Update.GetUpdate())
		s.deliverUpdate(p.Update.GetUpdate(), p.Update.GetSeed())
	case *collabv1.CollaborateResponse_Synced:
		s.setSynced(modeOf(p.Synced.GetMode()))
	case *collabv1.CollaborateResponse_Close:
		c.drop(s)
		s.finish(&CloseError{
			Doc:     msg.GetDoc(),
			Reason:  p.Close.GetReason(),
			Message: p.Close.GetMessage(),
		})
	case *collabv1.CollaborateResponse_Event:
		s.deliverEvent(Event{
			Doc:  msg.GetDoc(),
			Name: p.Event.GetName(),
			Data: p.Event.GetData(),
		})
	case *collabv1.CollaborateResponse_Awareness:
		s.deliverAwareness(p.Awareness.GetUpdate())
	}
}

// finish records the stream's terminal status and fails every open
// subscription with it.
func (c *Client) finish(cause error) {
	err := terminalError(cause)

	c.mu.Lock()

	if c.err == nil {
		c.err = err
	}

	c.mu.Unlock()

	c.doneOnce.Do(func() {
		close(c.done)
	})

	c.stopBackground()

	reported := err
	if reported == nil {
		reported = ErrClosed
	}

	for _, s := range c.subscriptions() {
		s.finish(reported)
	}
}

// allSubscriptions is every subscription the client has made.
func (c *Client) allSubscriptions() []*Subscription {
	c.mu.Lock()
	defer c.mu.Unlock()

	return append([]*Subscription(nil), c.opened...)
}

// subscriptions is the live ones.
func (c *Client) subscriptions() []*Subscription {
	c.mu.Lock()
	defer c.mu.Unlock()

	out := make([]*Subscription, 0, len(c.subs))

	for _, s := range c.subs {
		out = append(out, s)
	}

	return out
}

// claim registers a subscription for docID, or reports that one is
// already open.
func (c *Client) claim(s *Subscription) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if _, ok := c.subs[s.docID]; ok {
		return ErrSubscribed
	}

	c.subs[s.docID] = s
	c.opened = append(c.opened, s)

	return nil
}

// drop unregisters a subscription, so nothing the stream delivers for
// it afterwards reaches a document the caller has taken back.
func (c *Client) drop(s *Subscription) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.subs[s.docID] == s {
		delete(c.subs, s.docID)
	}
}

// refreshLoop keeps the connection's bearer fresh from the token
// source. It exits when the stream ends, when the context is
// cancelled, or when the source stops saying anything new — at which
// point the server's expiry guard is what ends the connection, which
// is the same outcome a client without a source gets.
func (c *Client) refreshLoop(
	ctx context.Context, src TokenSource, leeway time.Duration, tok Token,
) {
	if tok.Expires.IsZero() {
		return
	}

	bearer, expires := tok.Bearer, tok.Expires
	wait := max(time.Until(expires.Add(-leeway)), refreshFloor)

	for {
		if !c.sleep(ctx, wait) {
			return
		}

		next, err := src.Token(ctx)

		switch {
		case err != nil, next.Bearer == "", next.Bearer == bearer,
			!next.Expires.After(expires):
			// Nothing newer to send yet. The bearer is still valid
			// for the leeway, so there is time to ask again.
			wait = refreshRetry
		default:
			if sErr := c.Refresh(ctx, next.Bearer); sErr != nil {
				return
			}

			bearer, expires = next.Bearer, next.Expires
			wait = max(time.Until(expires.Add(-leeway)), refreshFloor)
		}
	}
}

// sleep waits for d, and reports whether it got there rather than
// being interrupted.
func (c *Client) sleep(ctx context.Context, d time.Duration) bool {
	timer := time.NewTimer(d)
	defer timer.Stop()

	select {
	case <-ctx.Done():
		return false
	case <-c.done:
		return false
	case <-timer.C:
		return true
	}
}

// bearerInterceptor stamps the connection's opening bearer on the
// stream's request headers. A stream is opened once, so the token it
// carries is the one Dial fetched; every later one travels as an
// auth_refresh on the stream itself.
type bearerInterceptor struct {
	bearer string
}

func (i bearerInterceptor) WrapUnary(
	next connect.UnaryFunc,
) connect.UnaryFunc {
	return func(
		ctx context.Context, req connect.AnyRequest,
	) (connect.AnyResponse, error) {
		req.Header().Set("Authorization", "Bearer "+i.bearer)

		return next(ctx, req)
	}
}

func (i bearerInterceptor) WrapStreamingClient(
	next connect.StreamingClientFunc,
) connect.StreamingClientFunc {
	return func(
		ctx context.Context, spec connect.Spec,
	) connect.StreamingClientConn {
		conn := next(ctx, spec)

		conn.RequestHeader().Set("Authorization", "Bearer "+i.bearer)

		return conn
	}
}

func (i bearerInterceptor) WrapStreamingHandler(
	next connect.StreamingHandlerFunc,
) connect.StreamingHandlerFunc {
	return next
}

// modeOf translates the wire enum into the vocabulary's string. An
// unset enum stays the zero SubscriptionMode, which is what a server
// that did not say resolves to.
func modeOf(mode collabv1.SubscriptionMode) SubscriptionMode {
	switch mode {
	case collabv1.SubscriptionMode_SUBSCRIPTION_MODE_READ_ONLY:
		return SubscriptionModeReadOnly
	case collabv1.SubscriptionMode_SUBSCRIPTION_MODE_READ_WRITE:
		return SubscriptionModeReadWrite
	case collabv1.SubscriptionMode_SUBSCRIPTION_MODE_UNSPECIFIED:
		return ""
	}

	return ""
}
