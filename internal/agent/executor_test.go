package agent

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// v0.7.4 — agent executors run operator-authored automated runbook steps in
// an isolated pod. These pin the security-relevant behaviors: JS is a no-I/O
// sandbox with a wall-clock timeout, bash captures output + times out, HTTP
// surfaces status, unknown kinds error rather than panic. A regression here
// could hang the agent (no timeout) or leak host access from the JS sandbox.

func TestExecuteJavaScript(t *testing.T) {
	if r := executeJavaScript("1 + 2", time.Second); r.Error != "" || r.Output != "3" {
		t.Fatalf("js eval = %+v, want output 3", r)
	}
	if r := executeJavaScript("throw new Error('boom')", time.Second); r.Error == "" {
		t.Fatal("js throw should surface as Error, not panic")
	}
	// No host bindings — require/process must be undefined (the sandbox).
	if r := executeJavaScript("typeof require + ',' + typeof process", time.Second); r.Output != "undefined,undefined" {
		t.Fatalf("sandbox leak: %q (want undefined,undefined)", r.Output)
	}
	// Infinite loop must be interrupted by the timeout, not hang.
	if r := executeJavaScript("while(true){}", 200*time.Millisecond); r.Error == "" {
		t.Fatal("infinite loop should be interrupted with an error")
	}
}

func TestExecuteBash(t *testing.T) {
	if r := executeBash(context.Background(), "echo hello", time.Second); r.Error != "" || strings.TrimSpace(r.Output) != "hello" {
		t.Fatalf("bash echo = %+v", r)
	}
	if r := executeBash(context.Background(), "sleep 5", 150*time.Millisecond); r.Error == "" {
		t.Fatal("bash past timeout should error")
	}
}

func TestExecuteHTTP(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Test") != "v" {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		_, _ = w.Write([]byte("ok"))
	}))
	defer srv.Close()
	if r := Execute(context.Background(), AutomatedStep{Kind: "http", URL: srv.URL, Headers: map[string]string{"X-Test": "v"}}); r.Error != "" || !strings.Contains(r.Output, "ok") {
		t.Fatalf("http = %+v", r)
	}
	if r := Execute(context.Background(), AutomatedStep{Kind: "http", URL: srv.URL}); r.Error == "" {
		t.Fatal("http 400 should surface as error")
	}
}

func TestExecuteUnknownKind(t *testing.T) {
	if r := Execute(context.Background(), AutomatedStep{Kind: "python"}); r.Error == "" {
		t.Fatal("unknown kind should return an error result, not panic")
	}
}
