package chstore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"testing"
)

// scan_test.go — v0.8.564. The exemplar readers used to swallow EVERY
// scan error; at prod scale a 10s max_execution_time timeout's only
// symptom was a drawer with no exemplar link. isNoRows is the seam that
// separates "legitimately nothing" (silent) from "read failed" (logged);
// misclassifying either direction re-hides timeouts or spams logs on
// every empty window.
func TestIsNoRows(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"std sentinel", sql.ErrNoRows, true},
		{"wrapped sentinel", fmt.Errorf("read: %w", sql.ErrNoRows), true},
		{"driver message analogue (the user.go precedent)",
			errors.New("sql: no rows in result set"), true},
		{"timeout is a REAL failure", context.DeadlineExceeded, false},
		{"ch max_execution_time is a REAL failure",
			errors.New("code: 159, message: Timeout exceeded"), false},
		{"nil", nil, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := isNoRows(c.err); got != c.want {
				t.Fatalf("isNoRows(%v) = %v, want %v", c.err, got, c.want)
			}
		})
	}
}
