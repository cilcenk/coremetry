package logstore

import (
	"strings"
	"testing"
)

// v0.8.166 — a least-privilege bank apikey (index `read` on logs-* but no
// cluster `monitor`) returns 403 on _cat/indices while /logs Search keeps
// working. Indices() used to return the raw ES JSON, which the handler
// turned into an HTTP 500 that BLANKED the whole /admin/elastic panel.
// catIndicesError must turn 401/403 into a clear permission-typed message
// (so the frontend shows WHY, not a blank), and must NOT degrade to an
// empty list (that would falsely render "No indices match the pattern").
func TestCatIndicesError_PermissionMessage(t *testing.T) {
	for _, code := range []int{401, 403} {
		err := catIndicesError(code, nil, "logs-*")
		if err == nil {
			t.Fatalf("status %d must produce an error, not nil (nil → blank panel)", code)
		}
		msg := err.Error()
		if !strings.Contains(msg, "monitor") || !strings.Contains(msg, "privilege") {
			t.Fatalf("status %d message must name the cluster monitor privilege, got %q", code, msg)
		}
		if strings.Contains(strings.ToLower(msg), "no indices") {
			t.Fatalf("status %d must NOT read as an empty cluster, got %q", code, msg)
		}
	}
}

func TestIsESPermissionStatus(t *testing.T) {
	cases := map[int]bool{401: true, 403: true, 200: false, 404: false, 500: false}
	for code, want := range cases {
		if got := isESPermissionStatus(code); got != want {
			t.Fatalf("isESPermissionStatus(%d) = %v, want %v", code, got, want)
		}
	}
}
