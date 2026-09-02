package vmetrics

// tokenref_test.go — v0.10.273 (kuyruk 7 dilim 3): VictoriaMetrics tokenRef
// sözleşmesi (Tempo 271 / Thanos 272 ile aynı): ref > saklı token; çözülemezse
// BOŞ + snapshot rozeti; geçersiz ref SavePersisted'da reddedilir; istek yolu
// (request) tokenFor okur — canlı ref cache'ten, gönderilmiş ref (Test probe'u)
// anında çözülür.

import (
	"context"
	"errors"
	"net/url"
	"os"
	"strings"
	"testing"
)

type memVMStore struct{ raw []byte }

func (m *memVMStore) GetVMetricsSettingsRaw(context.Context) ([]byte, error) { return m.raw, nil }
func (m *memVMStore) PutVMetricsSettingsRaw(_ context.Context, raw []byte) error {
	m.raw = append([]byte(nil), raw...)
	return nil
}

func newRefTestService(env map[string]string, files map[string]string) *Service {
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

func TestVMEffectiveTokenPrecedence(t *testing.T) {
	if got := effectiveToken(Settings{Token: "stored", TokenRef: "env:X"}, "from-env"); got != "from-env" {
		t.Errorf("ref çözüldü → ref, %q", got)
	}
	if got := effectiveToken(Settings{Token: "stored", TokenRef: "env:X"}, ""); got != "" {
		t.Errorf("ref çözülemedi → BOŞ, %q", got)
	}
	if got := effectiveToken(Settings{Token: "stored"}, ""); got != "stored" {
		t.Errorf("ref yok → saklı, %q", got)
	}
}

func TestVMConfigureResolvesAndRequestUsesIt(t *testing.T) {
	s := newRefTestService(map[string]string{"COREMETRY_VM_TOKEN": "tok-env", "OTHER": "tok-other"}, map[string]string{"/run/secrets/vm": "tok-file\n"})
	s.Configure(Settings{Enabled: true, BaseURL: "http://vm", AuthType: "bearer", TokenRef: "env:COREMETRY_VM_TOKEN", Token: "stale"})
	live := s.CurrentSettings()
	if got := s.request("/api/v1/query", url.Values{}, live).Token; got != "tok-env" {
		t.Fatalf("canlı ref cache'ten okunmalı, %q", got)
	}
	snap := s.Snapshot()
	if !snap.TokenResolved || snap.TokenError != "" || snap.TokenRef != "env:COREMETRY_VM_TOKEN" {
		t.Fatalf("snapshot: %+v", snap)
	}
	// Test probe'u: gönderilmiş (kaydedilmemiş) ref anında çözülür.
	probe := live
	probe.TokenRef = "env:OTHER"
	if got := s.request("/api/v1/query", url.Values{}, probe).Token; got != "tok-other" {
		t.Fatalf("gönderilmiş ref anında çözülmeli, %q", got)
	}
	probe.TokenRef = "file:/run/secrets/vm"
	if got := s.request("/api/v1/query", url.Values{}, probe).Token; got != "tok-file" {
		t.Fatalf("file ref kırpılarak çözülmeli, %q", got)
	}
	s.Configure(Settings{TokenRef: "env:MISSING", Token: "stale"})
	if got := s.request("/api/v1/query", url.Values{}, s.CurrentSettings()).Token; got != "" {
		t.Fatalf("çözülemeyen ref saklı token'a düşmemeli, %q", got)
	}
	if snap := s.Snapshot(); snap.TokenResolved || !strings.Contains(snap.TokenError, "boş ya da tanımsız") {
		t.Fatalf("hata rozeti: %+v", snap)
	}
	s.Configure(Settings{Token: "plain"})
	if got := s.request("/api/v1/query", url.Values{}, s.CurrentSettings()).Token; got != "plain" {
		t.Fatalf("ref yokken saklı token, %q", got)
	}
}

func TestVMSavePersistedRejectsPlainTokenAsRef(t *testing.T) {
	s := newRefTestService(nil, nil)
	st := &memVMStore{}
	if err := s.SavePersisted(context.Background(), st, Settings{TokenRef: "my-plain-token"}); err == nil || !strings.Contains(err.Error(), "tokenRef") {
		t.Fatalf("düz token ref olarak reddedilmeli, err=%v", err)
	}
	if len(st.raw) != 0 {
		t.Fatal("reddedilen ayar blob'a yazıldı")
	}
	if err := s.SavePersisted(context.Background(), st, Settings{TokenRef: "env:OK"}); err != nil || !strings.Contains(string(st.raw), `"tokenRef":"env:OK"`) {
		t.Fatalf("geçerli ref kaydedilmeli: err=%v raw=%s", err, st.raw)
	}
}

func TestVMRequestUsesTokenFor(t *testing.T) {
	b, err := os.ReadFile("client.go")
	if err != nil {
		t.Fatal(err)
	}
	src := string(b)
	i := strings.Index(src, "func (s *Service) request(")
	if i < 0 {
		t.Fatal("request yok")
	}
	body := src[i:]
	if j := strings.Index(body[1:], "\nfunc "); j > 0 {
		body = body[:j+1]
	}
	if !strings.Contains(body, "s.tokenFor(cfg)") || strings.Contains(body, "cfg.Token,") {
		t.Errorf("request tokenFor okumalı, cfg.Token değil:\n%s", body)
	}
}
