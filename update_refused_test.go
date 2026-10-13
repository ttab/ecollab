package ecollab_test

import (
	"testing"

	"github.com/ttab/ecollab"
)

func TestDecodeUpdateRefused(t *testing.T) {
	cases := []struct {
		data string
		want ecollab.UpdateRefusedReason
	}{
		{`{"reason":"unavailable"}`, ecollab.UpdateRefusedUnavailable},
		{`{"reason":"not_subscribed"}`, ecollab.UpdateRefusedNotSubscribed},
		{`{"reason":"publish_in_progress"}`, ecollab.UpdateRefusedPublishInProgress},
		// A reason newer than the library is passed through, for
		// the caller to treat as unavailable.
		{`{"reason":"something_new","extra":1}`, "something_new"},
	}

	for _, tc := range cases {
		got, err := ecollab.DecodeUpdateRefused([]byte(tc.data))
		if err != nil {
			t.Errorf("decode %s: %v", tc.data, err)

			continue
		}

		if got.Reason != tc.want {
			t.Errorf("decode %s = %q, want %q", tc.data, got.Reason, tc.want)
		}
	}
}

func TestDecodeUpdateRefusedRefusesMalformed(t *testing.T) {
	for _, data := range []string{``, `not json`, `{}`, `{"reason":""}`} {
		if got, err := ecollab.DecodeUpdateRefused([]byte(data)); err == nil {
			t.Errorf("decode %q = %+v, want an error", data, got)
		}
	}
}
