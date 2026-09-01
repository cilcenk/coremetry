package influx

import (
	"errors"
	"strings"
	"testing"
)

// secret_test.go — v0.10.222 (Influx D1, audit K5).
//
// Sözleşme: token system_settings'e DÜZ METİN yazılmaz; blob yalnız
// referansı taşır ve referans KULLANIM ANINDA çözülür.
//   env:NAME     → ortam değişkeni (Helm extraEnv + existingSecret)
//   file:/path   → dosya içeriği (mounted Secret), sondaki boşluk/newline
//                  kırpılır (kubectl create secret --from-file newline bırakır)
// Başka şema / boş çözüm → hata: sessiz 401 yerine Settings rozeti.

func TestResolveTokenRef(t *testing.T) {
	getenv := func(k string) string {
		return map[string]string{"COREMETRY_INFLUX_TOKEN_GG": "tok-env"}[k]
	}
	readFile := func(p string) ([]byte, error) {
		switch p {
		case "/var/run/secrets/influx/token":
			return []byte("tok-file\n"), nil
		case "/var/run/secrets/influx/empty":
			return []byte("  \n"), nil
		}
		return nil, errors.New("open: no such file")
	}
	cases := []struct {
		ref, want, wantErr string
	}{
		{"env:COREMETRY_INFLUX_TOKEN_GG", "tok-env", ""},
		{"file:/var/run/secrets/influx/token", "tok-file", ""},
		{"env:MISSING", "", "boş"},
		{"file:/var/run/secrets/influx/empty", "", "boş"},
		{"file:/nope", "", "no such file"},
		{"tok-plain", "", "şema"},
		{"", "", "şema"},
		{"env:", "", "şema"},
		{"file:relative/path", "", "şema"},
	}
	for _, c := range cases {
		t.Run(c.ref, func(t *testing.T) {
			got, err := resolveTokenRef(c.ref, getenv, readFile)
			if c.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), c.wantErr) {
					t.Fatalf("want error containing %q, got %q / %v", c.wantErr, got, err)
				}
				return
			}
			if err != nil || got != c.want {
				t.Fatalf("want %q, got %q / %v", c.want, got, err)
			}
		})
	}
}

func TestValidTokenRef(t *testing.T) {
	for _, ok := range []string{"env:A", "env:COREMETRY_INFLUX_TOKEN_X1", "file:/a", "file:/var/run/secrets/x"} {
		if !ValidTokenRef(ok) {
			t.Errorf("%q should be valid", ok)
		}
	}
	for _, bad := range []string{"", "env:", "env:1abc", "env:a-b", "file:", "file:rel", "token", "ENV:A", "file:/a b"} {
		if ValidTokenRef(bad) {
			t.Errorf("%q should be rejected", bad)
		}
	}
}
