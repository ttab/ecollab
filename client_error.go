package ecollab

import (
	"errors"
	"fmt"
	"io"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/encoding/protowire"
)

// CloseError is the server closing one subscription, with a reason
// from the CloseReason* vocabulary. The stream and the client's other
// subscriptions are unaffected — a refusal that takes the whole
// connection is a StreamError instead.
type CloseError struct {
	// Doc is the document whose subscription was closed.
	Doc string

	// Reason is one of the CloseReason* constants. It is what says
	// what to do about it; see their documentation.
	Reason string

	// Message is the server's detail, where it had one to give. It is
	// for a log line or a person, never for a branch. The one
	// structured message is CloseReasonLineageMismatch's, a JSON
	// LineageMismatch, which LineageMismatchError carries decoded.
	Message string
}

func (e *CloseError) Error() string {
	if e.Message == "" {
		return fmt.Sprintf("the subscription for %q was closed: %s",
			e.Doc, e.Reason)
	}

	return fmt.Sprintf("the subscription for %q was closed: %s: %s",
		e.Doc, e.Reason, e.Message)
}

// LineageMismatchError is the server refusing a subscribe because
// the client's copy of the document belongs to a different CRDT
// lineage than the session: the lineage it declared, or the history
// its state vector implies, is gone. It is a CloseError with
// CloseReasonLineageMismatch, unwrapped into the parts a caller acts
// on, and errors.As finds the *CloseError underneath it, so Reason
// reports lineage_mismatch.
//
// Nothing was merged: the server sent no catch-up and accepted
// nothing from the client. The local document is as it was, and
// stays readable — the caller's own document when it subscribed
// with WithDoc, and Subscription.Read after Done when the refusal
// came on a Resync. Keep it, recover what is worth keeping (into a
// sketch, say), and subscribe again with an empty document and no
// lineage. Subscribing again with the same document is refused the
// same way.
type LineageMismatchError struct {
	// Doc is the document whose subscribe was refused.
	Doc string

	// Declared is the lineage the subscribe declared, or "" when it
	// declared none and the server judged the state vector instead.
	Declared string

	// Current is the session's lineage, as the server reported it.
	// Empty when there was no session to join: the subscribe would
	// have seeded a new lineage, which the local copy cannot belong
	// to, and the next subscribe from an empty document seeds afresh
	// and is told that lineage in its Synced.
	Current string

	// Reason is why the lineage the local copy belongs to ended, for
	// telling the person what happened. LineageEndReasonUnknown when
	// the server could not say.
	Reason LineageEndReason

	// Version is the repository version the local copy's lineage
	// ended at, the baseline to compare the copy against; zero when
	// there is none. See LineageMismatch.
	Version int64

	close *CloseError
}

func (e *LineageMismatchError) Error() string {
	msg := e.describe()

	if e.Reason == "" || e.Reason == LineageEndReasonUnknown {
		return msg
	}

	return fmt.Sprintf("%s (the local lineage ended: %s)", msg, e.Reason)
}

func (e *LineageMismatchError) describe() string {
	switch {
	case e.Current == "" && e.Declared == "":
		return fmt.Sprintf(
			"the local copy of %q belongs to a lineage, and there is no session to join",
			e.Doc)
	case e.Current == "":
		return fmt.Sprintf(
			"the local copy of %q belongs to lineage %q, and there is no session to join",
			e.Doc, e.Declared)
	case e.Declared == "":
		return fmt.Sprintf(
			"the local copy of %q belongs to another lineage than the session's %q",
			e.Doc, e.Current)
	}

	return fmt.Sprintf(
		"the local copy of %q belongs to lineage %q, the session to %q",
		e.Doc, e.Declared, e.Current)
}

// Unwrap returns the *CloseError the server sent.
func (e *LineageMismatchError) Unwrap() error {
	return e.close
}

// closeError is the error a server Close ends a subscription with:
// a *LineageMismatchError for lineage_mismatch, which carries the
// lineage the subscribe declared, and a plain *CloseError for
// everything else.
func closeError(doc, reason, message, declared string) error {
	closed := &CloseError{Doc: doc, Reason: reason, Message: message}

	if reason != CloseReasonLineageMismatch {
		return closed
	}

	mismatch, err := DecodeLineageMismatch(message)
	if err != nil {
		// Not the JSON the server sends; keep what there is rather
		// than lose the refusal over its detail.
		mismatch = LineageMismatch{Reason: LineageEndReasonUnknown}
	}

	return &LineageMismatchError{
		Doc:      doc,
		Declared: declared,
		Current:  mismatch.Lineage,
		Reason:   mismatch.Reason,
		Version:  mismatch.Version,
		close:    closed,
	}
}

// ResyncTooLargeError ends a subscription whose answer to the
// server's sync step 1 — everything the local document holds that
// the session lacks — is larger than MaxSyncStep2Bytes. The server
// would refuse the frame by ending the whole connection, and refuse
// it again after every reconnect, so the client does not send it: it
// gives up the subscription instead and leaves the local document
// as it was.
//
// It is the recovery path, as for a lineage mismatch: the local
// edits cannot reach the session as they are. Recover what is worth
// keeping from the document and subscribe again without it.
type ResyncTooLargeError struct {
	// Doc is the document whose subscription was given up.
	Doc string

	// Size is the encoded size of the step 2 that was not sent.
	Size int

	// Limit is MaxSyncStep2Bytes.
	Limit int
}

func (e *ResyncTooLargeError) Error() string {
	return fmt.Sprintf(
		"the local changes to %q are %d bytes, over the %d byte sync step 2 limit",
		e.Doc, e.Size, e.Limit)
}

// StreamError is how the stream ended when it did not end cleanly:
// the server refusing the connection, a transport failure, or a
// cancelled context.
//
// A connection-wide refusal is the stream's status rather than a
// message on it, because a Connect stream has one. The code is
// shared between several reasons — rate_limited and
// subscription_limit are both resource_exhausted — so Reason is what
// to branch on.
type StreamError struct {
	// Reason is the structured refusal reason, one of the
	// CloseReason* constants, or "" for a failure that is not a
	// refusal.
	Reason string

	// Code is the Connect code the stream ended with.
	Code connect.Code

	// Err is the underlying error, the *connect.Error included.
	Err error
}

func (e *StreamError) Error() string {
	if e.Reason == "" {
		return fmt.Sprintf("the stream ended: %s: %v", e.Code, e.Err)
	}

	return fmt.Sprintf("the stream ended: %s: %s: %v",
		e.Code, e.Reason, e.Err)
}

func (e *StreamError) Unwrap() error {
	return e.Err
}

// Reason is the structured reason an error carries, or "" if it
// carries none. It reads both the per-subscription close and the
// stream's terminal status, so a caller that only wants to know what
// to do next has one thing to call:
//
//	switch ecollab.Reason(err) {
//	case ecollab.CloseReasonNoActiveSession:
//	    // Read the repository version instead.
//	case ecollab.CloseReasonSessionTerminated:
//	    // Subscribe again for a fresh session.
//	}
func Reason(err error) string {
	var closed *CloseError
	if errors.As(err, &closed) {
		return closed.Reason
	}

	var stream *StreamError
	if errors.As(err, &stream) {
		return stream.Reason
	}

	return reasonOf(err)
}

// terminalError is how the stream ended, as the client reports it. A
// clean half-close is not an error.
func terminalError(cause error) error {
	if cause == nil || errors.Is(cause, io.EOF) {
		return nil
	}

	return &StreamError{
		Reason: reasonOf(cause),
		Code:   connect.CodeOf(cause),
		Err:    cause,
	}
}

// reasonOf digs the structured refusal reason out of a Connect
// error.
//
// The service puts it in the error's metadata, which reaches the
// client two ways: as trailer metadata, and as the key/value detail
// message the service's RPC layer attaches. The detail is the one in
// use today, and decoding it off the wire rather than through its
// generated type is what keeps this library clear of the service's
// server-side dependencies — the message is a single
// map<string, string>, so there is little to get wrong and the field
// numbers are the contract.
func reasonOf(err error) string {
	var cErr *connect.Error
	if !errors.As(err, &cErr) {
		return ""
	}

	if reason := cErr.Meta().Get(metaReason); reason != "" {
		return reason
	}

	for _, detail := range cErr.Details() {
		if detail.Type() != errorMetaTypeName {
			continue
		}

		if value, ok := metaValue(detail.Bytes(), metaReason); ok {
			return value
		}
	}

	return ""
}

// metaValue reads one key out of an encoded map<string, string>
// field 1. A message it cannot parse yields nothing rather than a
// guess.
func metaValue(b []byte, key string) (string, bool) {
	for len(b) > 0 {
		num, typ, n := protowire.ConsumeTag(b)
		if n < 0 {
			return "", false
		}

		b = b[n:]

		if num != 1 || typ != protowire.BytesType {
			n = protowire.ConsumeFieldValue(num, typ, b)
			if n < 0 {
				return "", false
			}

			b = b[n:]

			continue
		}

		entry, n := protowire.ConsumeBytes(b)
		if n < 0 {
			return "", false
		}

		b = b[n:]

		k, v, ok := mapEntry(entry)
		if ok && k == key {
			return v, true
		}
	}

	return "", false
}

// mapEntry reads one protobuf map entry: key in field 1, value in
// field 2, both strings.
func mapEntry(b []byte) (key string, value string, ok bool) {
	for len(b) > 0 {
		num, typ, n := protowire.ConsumeTag(b)
		if n < 0 {
			return "", "", false
		}

		b = b[n:]

		if typ != protowire.BytesType {
			n = protowire.ConsumeFieldValue(num, typ, b)
			if n < 0 {
				return "", "", false
			}

			b = b[n:]

			continue
		}

		s, n := protowire.ConsumeBytes(b)
		if n < 0 {
			return "", "", false
		}

		b = b[n:]

		// Field 1 is the key and field 2 the value; a map entry
		// has no other fields. The switch is over field numbers, of
		// which protowire's named constants are not a set.
		switch num { //nolint:exhaustive // Field numbers, not an enum.
		case 1:
			key = string(s)
		case 2:
			value = string(s)
		}
	}

	return key, value, true
}
