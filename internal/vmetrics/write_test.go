package vmetrics

import (
	"compress/gzip"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// write_test.go — v0.10.292 (audit §7.5 madde 1): httptest tablo testi.

type capture struct {
	path, ctype, cenc, auth string
	body                    []byte
	rawLen                  int
}

func writeServer(t *testing.T, status int, resp string, got *capture) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		got.path, got.ctype, got.cenc, got.auth = r.URL.Path, r.Header.Get("Content-Type"), r.Header.Get("Content-Encoding"), r.Header.Get("Authorization")
		got.rawLen = len(raw)
		if r.Header.Get("Content-Encoding") == "gzip" {
			zr, err := gzip.NewReader(strings.NewReader(string(raw)))
			if err == nil {
				got.body, _ = io.ReadAll(zr)
			}
		} else {
			got.body = raw
		}
		w.WriteHeader(status)
		io.WriteString(w, resp)
	}))
}

func TestWriteOTLPTable(t *testing.T) {
	for _, tc := range []struct {
		name    string
		status  int
		resp    string
		wantErr error // nil = başarı
		wantMsg string
	}{
		{"200", 200, "", nil, ""},
		{"204 (VM'in normal cevabı)", 204, "", nil, ""},
		{"400 gövde VERBATİM", 400, "cannot parse OTLP: bad field", ErrRejected, "cannot parse OTLP: bad field"},
		{"401 upstream", 401, "unauthorized", ErrUpstream, "401"},
		{"403 upstream", 403, "", ErrUpstream, "403"},
		{"429 retryable", 429, "", ErrRetryable, "429"},
		{"503 retryable", 503, "", ErrRetryable, "503"},
		{"418 diğer → upstream", 418, "teapot", ErrUpstream, "418"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var got capture
			srv := writeServer(t, tc.status, tc.resp, &got)
			defer srv.Close()
			s := New()
			s.Configure(Settings{BaseURL: srv.URL + "/", WriteEnabled: true, AuthType: "bearer", Token: "tok"})
			err := s.WriteOTLP(context.Background(), []byte("proto-bytes"), false)
			if tc.wantErr == nil {
				if err != nil {
					t.Fatalf("hata beklenmiyordu: %v", err)
				}
			} else {
				if !errors.Is(err, tc.wantErr) {
					t.Fatalf("hata sınıfı %v; got %v", tc.wantErr, err)
				}
				if !strings.Contains(err.Error(), tc.wantMsg) {
					t.Errorf("mesaj %q; %q içermeliydi", err.Error(), tc.wantMsg)
				}
			}
			if got.path != "/opentelemetry/v1/metrics" {
				t.Errorf("path %q", got.path)
			}
			if got.ctype != "application/x-protobuf" || got.cenc != "gzip" {
				t.Errorf("başlıklar ctype=%q cenc=%q", got.ctype, got.cenc)
			}
			if got.auth != "Bearer tok" {
				t.Errorf("auth %q", got.auth)
			}
			if string(got.body) != "proto-bytes" {
				t.Errorf("gövde gzip açılınca %q; ham baytlar bekleniyordu", got.body)
			}
		})
	}
}

func TestWriteOTLPPassesGzippedBodyVerbatim(t *testing.T) {
	var got capture
	srv := writeServer(t, 204, "", &got)
	defer srv.Close()
	s := New()
	s.Configure(Settings{BaseURL: srv.URL, WriteURL: srv.URL + "/write-here", WriteEnabled: true})
	var pre strings.Builder
	zw := gzip.NewWriter(&pre)
	zw.Write([]byte("already"))
	zw.Close()
	if err := s.WriteOTLP(context.Background(), []byte(pre.String()), true); err != nil {
		t.Fatal(err)
	}
	if got.rawLen != len(pre.String()) || string(got.body) != "already" {
		t.Errorf("gzip'li gövde değişti: rawLen=%d body=%q", got.rawLen, got.body)
	}
	if got.path != "/write-here/opentelemetry/v1/metrics" {
		t.Errorf("WriteURL BaseURL'e tercih edilmeli: %q", got.path)
	}
	if got.auth != "" {
		t.Errorf("authType none iken Authorization gönderilmemeli: %q", got.auth)
	}
}

func TestWriteOTLPTokenRefResolved(t *testing.T) {
	var got capture
	srv := writeServer(t, 200, "", &got)
	defer srv.Close()
	s := &Service{getenv: func(k string) string {
		if k == "VM_TOK" {
			return "from-env"
		}
		return ""
	}, readFile: func(string) ([]byte, error) { return nil, errors.New("no") }}
	s.Configure(Settings{BaseURL: srv.URL, WriteEnabled: true, AuthType: "bearer", Token: "stale", TokenRef: "env:VM_TOK"})
	if err := s.WriteOTLP(context.Background(), []byte("x"), false); err != nil {
		t.Fatal(err)
	}
	if got.auth != "Bearer from-env" {
		t.Errorf("çözülmüş ref kullanılmalı: %q", got.auth)
	}
}

func TestWriteOTLPNotConfiguredAndConnRefused(t *testing.T) {
	s := New()
	s.Configure(Settings{BaseURL: "http://127.0.0.1:9"})
	if err := s.WriteOTLP(context.Background(), []byte("x"), false); !errors.Is(err, ErrNoWrite) {
		t.Errorf("yazım kapalıyken ErrNoWrite bekleniyordu: %v", err)
	}
	var nilSvc *Service
	if err := nilSvc.WriteOTLP(context.Background(), nil, false); !errors.Is(err, ErrNoWrite) {
		t.Errorf("nil servis ErrNoWrite: %v", err)
	}
	s.Configure(Settings{BaseURL: "http://127.0.0.1:9", WriteEnabled: true})
	if err := s.WriteOTLP(context.Background(), []byte("x"), false); !errors.Is(err, ErrRetryable) {
		t.Errorf("bağlantı reddi ErrRetryable olmalı: %v", err)
	}
}

func TestWriteOTLPContextCancel(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-time.After(3 * time.Second):
		}
	}))
	defer srv.Close()
	s := New()
	s.Configure(Settings{BaseURL: srv.URL, WriteEnabled: true})
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	start := time.Now()
	err := s.WriteOTLP(ctx, []byte("x"), false)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("ctx iptali ctx.Err() olmalı: %v", err)
	}
	if time.Since(start) > 2*time.Second {
		t.Error("iptal beklemedi — çağrı asılı kaldı")
	}
}

func TestWriteTargetAndReady(t *testing.T) {
	if WriteTarget(Settings{BaseURL: "http://a/", WriteURL: ""}) != "http://a" {
		t.Error("WriteURL boş → BaseURL, sondaki / kırpılır")
	}
	if WriteTarget(Settings{BaseURL: "http://a", WriteURL: " http://w/ "}) != "http://w" {
		t.Error("WriteURL tercih edilir")
	}
	s := New()
	s.Configure(Settings{WriteEnabled: true})
	if s.WriteReady() {
		t.Error("hedefsiz yazım hazır olamaz")
	}
	s.Configure(Settings{WriteEnabled: true, BaseURL: "http://a"})
	if !s.WriteReady() {
		t.Error("BaseURL yeterli hedef")
	}
}
