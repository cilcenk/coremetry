package secretref

// secretref_test.go — v0.10.271: influx/secret_test.go sözleşmesinin ortak
// paket kopyası (Tempo/Thanos/VM aynı çözücüyü kullanır).

import (
	"errors"
	"strings"
	"testing"
)

func TestResolveWith(t *testing.T) {
	getenv := func(k string) string { return map[string]string{"TOK": "tok-env", "EMPTY": "  "}[k] }
	readFile := func(p string) ([]byte, error) {
		switch p {
		case "/run/secrets/t":
			return []byte("tok-file\n"), nil
		case "/run/secrets/empty":
			return []byte("\n"), nil
		}
		return nil, errors.New("open: no such file")
	}
	cases := []struct {
		ref, want, errPart string
	}{
		{"env:TOK", "tok-env", ""},
		{"env:MISSING", "", "boş ya da tanımsız"},
		{"env:EMPTY", "", "boş ya da tanımsız"},
		{"file:/run/secrets/t", "tok-file", ""},
		{"file:/run/secrets/empty", "", "dosya boş"},
		{"file:/nope", "", "no such file"},
		{"my-plain-token", "", "şema"},
		{"env:", "", "şema"},
		{"file:relative/path", "", "şema"},
		{"", "", "şema"},
	}
	for _, c := range cases {
		got, err := ResolveWith(c.ref, getenv, readFile)
		if c.errPart == "" {
			if err != nil || got != c.want {
				t.Errorf("%q: got %q err %v", c.ref, got, err)
			}
			continue
		}
		if err == nil || !strings.Contains(err.Error(), c.errPart) {
			t.Errorf("%q: hata %q bekleniyordu, %v", c.ref, c.errPart, err)
		}
	}
}

func TestValid(t *testing.T) {
	for ref, want := range map[string]bool{"env:A_1": true, "file:/x/y": true, "env:1bad": false, "file:x": false, "plain": false, "": false} {
		if Valid(ref) != want {
			t.Errorf("Valid(%q) = %v", ref, !want)
		}
	}
}
