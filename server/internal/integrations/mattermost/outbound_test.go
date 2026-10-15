package mattermost

import (
	"errors"
	"net"
	"testing"
)

func TestSendRejectionDoesNotGuessAfterAnAmbiguousFailure(t *testing.T) {
	for _, tc := range []struct {
		name     string
		err      error
		rejected bool
	}{
		{"rate limit", &apiError{StatusCode: 429}, true},
		{"revoked token", &apiError{StatusCode: 401}, true},
		{"connection refused", &requestError{cause: &net.OpError{Op: "dial", Err: errors.New("refused")}}, true},
		{"lost response", &requestError{cause: &net.OpError{Op: "read", Err: errors.New("timeout")}}, false},
		{"provider failed after write", &apiError{StatusCode: 500}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := definiteSendRejection(tc.err); got != tc.rejected {
				t.Fatalf("definite rejection = %v, want %v", got, tc.rejected)
			}
		})
	}
}
