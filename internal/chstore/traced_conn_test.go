package chstore

import (
	"context"
	"errors"
	"fmt"
	"testing"
)

// v0.7.12 regression — the CH-span wrapper marked EVERY error as codes.Error,
// so a client-cancelled request (context.Canceled, browser navigated away /
// React Query superseded a poll) produced 0ms "context canceled" error spans
// on coremetry-api — noise in the self-obs trace view + error_rate. Operator
// reported it. chErrorIsBenignCancel must treat context.Canceled (incl.
// wrapped) as benign while keeping context.DeadlineExceeded and every real
// error as a failure, so genuine slow queries still surface.
func TestCHErrorIsBenignCancel(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"nil is not benign-cancel", nil, false},
		{"context.Canceled is benign", context.Canceled, true},
		{"wrapped context.Canceled is benign", fmt.Errorf("query failed: %w", context.Canceled), true},
		{"context.DeadlineExceeded is a real error", context.DeadlineExceeded, false},
		{"wrapped DeadlineExceeded is a real error", fmt.Errorf("ch timeout: %w", context.DeadlineExceeded), false},
		{"generic CH error is a real error", errors.New("code: 159, DB::Exception: timeout"), false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := chErrorIsBenignCancel(c.err); got != c.want {
				t.Errorf("chErrorIsBenignCancel(%v) = %v, want %v", c.err, got, c.want)
			}
		})
	}
}
