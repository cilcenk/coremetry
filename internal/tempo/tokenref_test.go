package tempo

// tokenref_test.go — v0.10.271 (kuyruk 7 dilim 1): Tempo tokenRef sözleşmesi.
//   · ref doluysa çözülmüş değer > saklı token; ref çözülemezse BOŞ (eski
//     token'a sessizce düşülmez) ve Snapshot TokenResolved=false + TokenError
//   · geçersiz ref SavePersisted'da reddedilir, blob'a yazılmaz
//   · LookupTrace gövdesi EffectiveToken'ı okur, cfg.Token'ı değil (kaynak pini)

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
)

type memStore struct{ raw []byte }

func (m *memStore) GetTempoSettingsRaw(context.Context) ([]byte, error) { return m.raw, nil }
func (m *memStore) PutTempoSettingsRaw(_ context.Context, raw []byte) error {
	m.raw = append([]byte(nil), raw...)
	return nil
}

func newTestService(env map[string]string, files map[string]string) *Service {
	s := New()
	s.getenv = func(k string) string { return env[k] }
	s.readFile = func(p string) ([]byte, error) {
		if v, ok := files[p]; ok {
			return []byte(v), nil
		}
		return nil, errors.New("open: no such file")
	}
	return s
}

func TestEffectiveTokenPrecedence(t *testing.T) {
	cases := []struct {
		name     string
		cfg      Settings
		resolved string
		want     string
	}{
		{"ref çözüldü → ref", Settings{Token: "stored", TokenRef: "env:X"}, "from-env", "from-env"},
		{"ref çözülemedi → BOŞ, saklıya düşme yok", Settings{Token: "stored", TokenRef: "env:X"}, "", ""},
		{"ref yok → saklı", Settings{Token: "stored"}, "", "stored"},
		{"ikisi de yok → boş", Settings{}, "", ""},
	}
	for _, c := range cases {
		if got := effectiveToken(c.cfg, c.resolved); got != c.want {
			t.Errorf("%s: %q, istenen %q", c.name, got, c.want)
		}
	}
}

func TestConfigureResolvesTokenRef(t *testing.T) {
	s := newTestService(map[string]string{"COREMETRY_TEMPO_TOKEN": "tok-env"}, map[string]string{"/run/secrets/tempo": "tok-file\n"})

	s.Configure(Settings{Enabled: true, BaseURL: "http://t", AuthType: "bearer", TokenRef: "env:COREMETRY_TEMPO_TOKEN", Token: "stale"})
	if s.EffectiveToken() != "tok-env" {
		t.Fatalf("env ref çözülmeli, %q", s.EffectiveToken())
	}
	snap := s.Snapshot()
	if !snap.TokenResolved || snap.TokenError != "" || snap.TokenRef != "env:COREMETRY_TEMPO_TOKEN" || !snap.HasToken {
		t.Fatalf("snapshot: %+v", snap)
	}

	s.Configure(Settings{TokenRef: "file:/run/secrets/tempo"})
	if s.EffectiveToken() != "tok-file" {
		t.Fatalf("file ref kırpılarak çözülmeli, %q", s.EffectiveToken())
	}

	s.Configure(Settings{TokenRef: "env:MISSING", Token: "stale"})
	if s.EffectiveToken() != "" {
		t.Fatalf("çözülemeyen ref saklı token'a düşmemeli, %q", s.EffectiveToken())
	}
	snap = s.Snapshot()
	if snap.TokenResolved || !strings.Contains(snap.TokenError, "boş ya da tanımsız") {
		t.Fatalf("hata rozeti yok: %+v", snap)
	}

	s.Configure(Settings{Token: "plain"})
	if s.EffectiveToken() != "plain" || s.Snapshot().TokenResolved || s.Snapshot().TokenError != "" {
		t.Fatalf("ref silinince saklı token + temiz rozet: %q %+v", s.EffectiveToken(), s.Snapshot())
	}
}

func TestSavePersistedRejectsPlainTokenAsRef(t *testing.T) {
	s := newTestService(nil, nil)
	st := &memStore{}
	err := s.SavePersisted(context.Background(), st, Settings{TokenRef: "my-plain-token"})
	if err == nil || !strings.Contains(err.Error(), "tokenRef") {
		t.Fatalf("düz token ref olarak reddedilmeli, err=%v", err)
	}
	if len(st.raw) != 0 {
		t.Fatal("reddedilen ayar blob'a yazıldı")
	}
	if err := s.SavePersisted(context.Background(), st, Settings{TokenRef: "env:OK"}); err != nil {
		t.Fatalf("geçerli ref kaydedilmeli: %v", err)
	}
	if !strings.Contains(string(st.raw), `"tokenRef":"env:OK"`) {
		t.Fatalf("blob referansı taşımalı: %s", st.raw)
	}
}

// Kaynak pini: LookupTrace çözülmüş token'ı kullanır; cfg.Token'a dönüş
// (ref varken bayat/saklı token'la istek) sessiz güvenlik gerilemesi olur.
func TestLookupTraceUsesEffectiveToken(t *testing.T) {
	b, err := os.ReadFile("client.go")
	if err != nil {
		t.Fatal(err)
	}
	src := string(b)
	i := strings.Index(src, "func (s *Service) LookupTrace(")
	if i < 0 {
		t.Fatal("LookupTrace yok")
	}
	body := src[i:]
	if j := strings.Index(body[1:], "\nfunc "); j > 0 {
		body = body[:j+1]
	}
	if !strings.Contains(body, "s.EffectiveToken()") {
		t.Error("LookupTrace EffectiveToken() okumuyor")
	}
	if strings.Contains(body, "cfg.Token") {
		t.Error("LookupTrace hâlâ cfg.Token okuyor (ref'i atlar)")
	}
}
