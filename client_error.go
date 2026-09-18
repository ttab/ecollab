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
	// for a log line or a person, never for a branch.
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
