package ecollab

import (
	"encoding/json"
	"errors"
	"fmt"
)

// UpdateRefusedReason is why the service did not forward an update,
// as an EventUpdateRefused event reports it.
//
// The set may grow: a client must treat a value it does not know as
// UpdateRefusedUnavailable, work that did not reach the session and
// is worth sending again.
type UpdateRefusedReason string

const (
	// UpdateRefusedUnavailable: the service could not take the
	// update — a read it gates updates on, or the append to the
	// session's stream, failed. The work is still the client's to
	// deliver, and a later handshake is what sends it again.
	UpdateRefusedUnavailable UpdateRefusedReason = "unavailable"

	// UpdateRefusedNotSubscribed: the connection holds no
	// subscription for the document, either because the client sent
	// the update after its own unsubscribe, or because the service
	// had already ended the subscription and its Close is on the way.
	// It is informational: the Close that follows, or the client's
	// own next subscribe, decides what happens to the document. Keep
	// the work, and do not re-handshake on this alone.
	UpdateRefusedNotSubscribed UpdateRefusedReason = "not_subscribed"

	// UpdateRefusedPublishInProgress: the update arrived while a
	// publish had soft-stopped the session. Send it again once
	// EventPublishCleared says updates flow.
	UpdateRefusedPublishInProgress UpdateRefusedReason = "publish_in_progress"
)

// UpdateRefused is the data of an EventUpdateRefused event.
type UpdateRefused struct {
	// Reason is why the update was not forwarded.
	Reason UpdateRefusedReason `json:"reason"`
}

// DecodeUpdateRefused decodes the data of an EventUpdateRefused
// event. Data that is not JSON is an error, and so is data without a
// reason.
func DecodeUpdateRefused(data []byte) (UpdateRefused, error) {
	var m UpdateRefused

	if err := json.Unmarshal(data, &m); err != nil {
		return UpdateRefused{}, fmt.Errorf("decode update refused: %w", err)
	}

	if m.Reason == "" {
		return UpdateRefused{}, errors.New("decode update refused: no reason")
	}

	return m, nil
}

// UpdateRefusedError is WaitDelivered's answer when the service
// refused work this subscription sent — a sync step 2, or an update
// made with Update — for a reason other than a publish soft-stop,
// which the subscription waits out on its own. The local document
// still holds the work: a Resync sends whatever the session lacks
// and clears the error.
type UpdateRefusedError struct {
	// Doc is the document the work was for.
	Doc string

	// Reason is the service's, one of the UpdateRefused* constants,
	// or UpdateRefusedUnavailable when the event's data could not be
	// read.
	Reason UpdateRefusedReason
}

func (e *UpdateRefusedError) Error() string {
	return fmt.Sprintf("the session refused the local changes to %q: %s",
		e.Doc, e.Reason)
}
