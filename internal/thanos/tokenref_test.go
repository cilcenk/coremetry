package thanos

// tokenref_test.go — v0.10.272 (kuyruk 7 dilim 2): Remote Clusters tokenRef
// sözleşmesi, Tempo (v0.10.271) ile aynı: küme başına ref > saklı token;
// çözülemezse BOŞ (bayat token'a düşme yok) + snapshot rozeti; geçersiz ref
// SavePersisted'da reddedilir; doQueryWith çözülmüş token'ı okur (kaynak pini).

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
)

type memThanosStore struct{ raw []byte }

func (m *memThanosStore) GetThanosSettingsRaw(context.Context) ([]byte, error) { return m.raw, nil }
func (m *memThanosStore) PutThanosSettingsRaw(_ context.Context, raw []byte) error {
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

func TestThanosEffectiveTokenPrecedence(t *testing.T) {
	c := ClusterConfig{Name: "prod", Token: "stored", TokenRef: "env:X"}
	if got := effectiveToken(c, "from-env"); got != "from-env" {
		t.Errorf("ref çözüldü → ref, %q", got)
	}
	if got := effectiveToken(c, ""); got != "" {
		t.Errorf("ref çözülemedi → BOŞ, saklıya düşme yok, %q", got)
	}
	if got := effectiveToken(ClusterConfig{Token: "stored"}, ""); got != "stored" {
		t.Errorf("ref yok → saklı, %q", got)
	}
}

func TestThanosConfigureResolvesPerCluster(t *testing.T) {
	s := newRefTestService(map[string]string{"COREMETRY_THANOS_TOKEN_PROD": "tok-prod"}, map[string]string{"/run/secrets/dr": "tok-dr\n"})
	s.Configure(Settings{Clusters: []ClusterConfig{
		{ID: "c-1", Name: "prod", URL: "http://p", AuthType: "bearer", TokenRef: "env:COREMETRY_THANOS_TOKEN_PROD", Token: "stale", Enabled: true},
		{ID: "c-2", Name: "dr", URL: "http://d", AuthType: "bearer", TokenRef: "file:/run/secrets/dr", Enabled: true},
		{ID: "c-3", Name: "lab", URL: "http://l", AuthType: "bearer", TokenRef: "env:MISSING", Token: "stale", Enabled: true},
		{ID: "c-4", Name: "plain", URL: "http://x", AuthType: "bearer", Token: "plain-tok", Enabled: true},
	}})
	cs := s.CurrentSettings().Clusters
	want := map[string]string{"prod": "tok-prod", "dr": "tok-dr", "lab": "", "plain": "plain-tok"}
	for _, c := range cs {
		if got := s.effectiveTokenFor(c); got != want[c.Name] {
			t.Errorf("%s: etkin token %q, istenen %q", c.Name, got, want[c.Name])
		}
	}
	snap := s.Snapshot()
	byName := map[string]ClusterSnapshot{}
	for _, c := range snap.Clusters {
		byName[c.Name] = c
	}
	if !byName["prod"].TokenResolved || byName["prod"].TokenError != "" || byName["prod"].TokenRef != "env:COREMETRY_THANOS_TOKEN_PROD" {
		t.Errorf("prod rozeti: %+v", byName["prod"])
	}
	if byName["lab"].TokenResolved || !strings.Contains(byName["lab"].TokenError, "boş ya da tanımsız") {
		t.Errorf("lab hata rozeti: %+v", byName["lab"])
	}
	if byName["plain"].TokenResolved || byName["plain"].TokenError != "" || !byName["plain"].HasToken {
		t.Errorf("plain: ref yok, rozet temiz, saklı var: %+v", byName["plain"])
	}
}

func TestThanosSavePersistedRejectsPlainTokenAsRef(t *testing.T) {
	s := newRefTestService(nil, nil)
	st := &memThanosStore{}
	err := s.SavePersisted(context.Background(), st, Settings{Clusters: []ClusterConfig{{Name: "prod", TokenRef: "my-plain-token"}}})
	if err == nil || !strings.Contains(err.Error(), "tokenRef") || !strings.Contains(err.Error(), "prod") {
		t.Fatalf("düz token ref olarak reddedilmeli (küme adıyla), err=%v", err)
	}
	if len(st.raw) != 0 {
		t.Fatal("reddedilen ayar blob'a yazıldı")
	}
	if err := s.SavePersisted(context.Background(), st, Settings{Clusters: []ClusterConfig{{Name: "prod", TokenRef: "env:OK"}}}); err != nil {
		t.Fatalf("geçerli ref kaydedilmeli: %v", err)
	}
	if !strings.Contains(string(st.raw), `"tokenRef":"env:OK"`) {
		t.Fatalf("blob referansı taşımalı: %s", st.raw)
	}
}

func TestDoQueryUsesEffectiveToken(t *testing.T) {
	b, err := os.ReadFile("client.go")
	if err != nil {
		t.Fatal(err)
	}
	src := string(b)
	i := strings.Index(src, "func (s *Service) doQueryWith(")
	if i < 0 {
		t.Fatal("doQueryWith yok")
	}
	body := src[i:]
	if j := strings.Index(body[1:], "\nfunc "); j > 0 {
		body = body[:j+1]
	}
	if !strings.Contains(body, "s.effectiveTokenFor(c)") {
		t.Error("doQueryWith effectiveTokenFor okumuyor")
	}
	if strings.Contains(body, "c.Token") {
		t.Error("doQueryWith hâlâ c.Token okuyor (ref'i atlar)")
	}
}
