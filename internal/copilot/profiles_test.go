package copilot

// profiles_test.go — v0.10.175 sözleşmesi (profiles.go başlığı): göç, çözüm
// sırası, profil başına tuning/istemci, kalıcılık (düz ayna dahil), anahtar
// koruma, varsayılan silinemez, refresh runtime'ı korur.

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

func f64(v float64) *float64 { return &v }

// captureRecorder — ai_calls satırını yakalar (RecordCall asenkron goroutine'den gelir).
type captureRecorder struct {
	mu  sync.Mutex
	rec CallRecord
}

func (c *captureRecorder) RecordCall(_ context.Context, r CallRecord) {
	c.mu.Lock()
	c.rec = r
	c.mu.Unlock()
}
func (c *captureRecorder) last() CallRecord {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.rec
}

func TestProfiles_LegacyBlobMigratesToSingleDefault(t *testing.T) {
	store := newMemStore()
	store.m[settingsKey] = []byte(`{"provider":"openai","apiKey":"k1","model":"gemma4","baseUrl":"http://vllm:8000/v1","enabled":true}`)
	s := New("anthropic", "", "")
	if err := s.LoadPersisted(context.Background(), store); err != nil {
		t.Fatal(err)
	}
	ps := s.Profiles()
	if len(ps) != 1 || ps[0].ID != DefaultProfileID || ps[0].Model != "gemma4" || ps[0].BaseURL != "http://vllm:8000/v1" || ps[0].APIKey != "k1" {
		t.Fatalf("göç yanlış: %+v", ps)
	}
	if s.DefaultProfileID() != DefaultProfileID {
		t.Fatalf("varsayılan %q", s.DefaultProfileID())
	}
	prov, model, base, hasKey, _, enabled := s.Snapshot()
	if prov != ProviderOpenAI || model != "gemma4" || base != "http://vllm:8000/v1" || !hasKey || !enabled {
		t.Fatalf("ayna yanlış: %s %s %s %v %v", prov, model, base, hasKey, enabled)
	}
}

func TestProfiles_ResolveOrder(t *testing.T) {
	s := New("openai", "kd", "default-model")
	s.SetProfiles([]ModelProfile{
		{ID: "big", Provider: ProviderOpenAI, BaseURL: "http://big/v1", APIKey: "kb", Model: "gemma4-31b"},
		{ID: "small", Provider: ProviderOpenAI, BaseURL: "http://small/v1", APIKey: "ks", Model: "qwen3-8b", MaxTokens: 512, Temperature: f64(0)},
	}, "big", map[string]string{"chat-intent": "small"})
	s.ConfigureTuning(2048, f64(0.7), 0)

	cfg, req, prov, base, model := s.callSnapshot(context.Background())
	if prov != ProviderOpenAI || base != "http://big/v1" || model != "gemma4-31b" || cfg.APIKey != "kb" || req.MaxTokens != 2048 || *req.Temperature != 0.7 {
		t.Fatalf("varsayılan çözüm yanlış: %s %s %s key=%s max=%d temp=%v", prov, base, model, cfg.APIKey, req.MaxTokens, *req.Temperature)
	}
	// yüzey eşlemesi
	ctx := WithMeta(context.Background(), CallMeta{Surface: "chat-intent"})
	cfg, req, _, base, model = s.callSnapshot(ctx)
	if base != "http://small/v1" || model != "qwen3-8b" || cfg.APIKey != "ks" || req.MaxTokens != 512 || *req.Temperature != 0 {
		t.Fatalf("yüzey eşlemesi yanlış: %s %s max=%d temp=%v", base, model, req.MaxTokens, *req.Temperature)
	}
	// ctx override yüzeyi ezer
	_, _, _, base, _ = s.callSnapshot(WithProfile(ctx, "big"))
	if base != "http://big/v1" {
		t.Fatalf("WithProfile ezmedi: %s", base)
	}
	// bilinmeyen override → varsayılan
	_, _, _, base, _ = s.callSnapshot(WithProfile(context.Background(), "yok"))
	if base != "http://big/v1" {
		t.Fatalf("bilinmeyen profil varsayılana düşmedi: %s", base)
	}
	if p, m, b := s.profileIdentity(ctx); p != ProviderOpenAI || m != "qwen3-8b" || b != "http://small/v1" {
		t.Fatalf("profileIdentity: %s %s %s", p, m, b)
	}
}

func TestProfiles_UpsertKeepsKey_DeleteDefaultRefused_SurfaceValidated(t *testing.T) {
	store := newMemStore()
	s := New("openai", "kd", "m0")
	s.Configure("openai", "kd", "m0", "http://d/v1", false, true)
	if err := s.UpsertProfile(context.Background(), store, ModelProfile{ID: "small", Provider: ProviderOpenAI, BaseURL: "http://s/v1", APIKey: "ks", Model: "q"}); err != nil {
		t.Fatal(err)
	}
	// boş anahtar → mevcut korunur
	if err := s.UpsertProfile(context.Background(), store, ModelProfile{ID: "small", Label: "Küçük", Provider: ProviderOpenAI, BaseURL: "http://s2/v1", Model: "q2"}); err != nil {
		t.Fatal(err)
	}
	ps := s.Profiles()
	if len(ps) != 2 || ps[1].APIKey != "ks" || ps[1].BaseURL != "http://s2/v1" || ps[1].Label != "Küçük" {
		t.Fatalf("upsert: %+v", ps)
	}
	if s.DefaultProfileID() != DefaultProfileID {
		t.Fatalf("varsayılan değişmemeli: %s", s.DefaultProfileID())
	}
	if err := s.DeleteProfile(context.Background(), store, DefaultProfileID); err == nil {
		t.Fatal("varsayılan silinebildi")
	}
	if err := s.SetSurfaceProfiles(context.Background(), store, map[string]string{"chat-intent": "yok"}); err == nil {
		t.Fatal("bilinmeyen profil yüzeye atanabildi")
	}
	if err := s.SetSurfaceProfiles(context.Background(), store, map[string]string{"chat-intent": "small", "problem-auto-explain": ""}); err != nil {
		t.Fatal(err)
	}
	if got := s.SurfaceProfiles(); got["chat-intent"] != "small" || len(got) != 1 {
		t.Fatalf("yüzey haritası: %v", got)
	}
	if err := s.SetDefaultProfile(context.Background(), store, "small"); err != nil {
		t.Fatal(err)
	}
	if _, model, base, _, _, _ := s.Snapshot(); model != "q2" || base != "http://s2/v1" {
		t.Fatalf("varsayılan değişince ayna güncellenmedi: %s %s", model, base)
	}
	if err := s.DeleteProfile(context.Background(), store, DefaultProfileID); err != nil {
		t.Fatalf("eski varsayılan silinemedi: %v", err)
	}
	if ids := s.ProfileIDs(); len(ids) != 1 || ids[0] != "small" {
		t.Fatalf("kalan: %v", ids)
	}
	// silinen profil yüzey haritasından da düşer
	if err := s.UpsertProfile(context.Background(), store, ModelProfile{ID: "x", Provider: ProviderOpenAI, BaseURL: "http://x/v1", Model: "mx"}); err != nil {
		t.Fatal(err)
	}
	_ = s.SetSurfaceProfiles(context.Background(), store, map[string]string{"chat-intent": "x"})
	_ = s.DeleteProfile(context.Background(), store, "x")
	if got := s.SurfaceProfiles(); len(got) != 0 {
		t.Fatalf("silinen profil haritada kaldı: %v", got)
	}
}

func TestProfiles_PersistRoundTripWithFlatMirror(t *testing.T) {
	store := newMemStore()
	s := New("openai", "kd", "m0")
	if err := s.SavePersisted(context.Background(), store, "openai", "kd", "m0", "http://d/v1", false, true, 0, nil, 0, nil, ""); err != nil {
		t.Fatal(err)
	}
	if err := s.UpsertProfile(context.Background(), store, ModelProfile{ID: "small", Provider: ProviderOpenAI, BaseURL: "http://s/v1", APIKey: "ks", Model: "q", TimeoutS: 30}); err != nil {
		t.Fatal(err)
	}
	if err := s.SetDefaultProfile(context.Background(), store, "small"); err != nil {
		t.Fatal(err)
	}
	var disk map[string]any
	if err := json.Unmarshal(store.m[settingsKey], &disk); err != nil {
		t.Fatal(err)
	}
	// düz ayna = varsayılan (eski binary bunu okur)
	if disk["model"] != "q" || disk["baseUrl"] != "http://s/v1" || disk["apiKey"] != "ks" || disk["defaultProfile"] != "small" {
		t.Fatalf("düz ayna yanlış: %v", disk)
	}
	if _, ok := disk["profiles"].([]any); !ok {
		t.Fatalf("profiles listesi yok: %v", disk)
	}
	// başka pod: sıfırdan yükle
	s2 := New("anthropic", "", "")
	if err := s2.LoadPersisted(context.Background(), store); err != nil {
		t.Fatal(err)
	}
	if s2.DefaultProfileID() != "small" || len(s2.Profiles()) != 2 {
		t.Fatalf("yükleme: def=%s n=%d", s2.DefaultProfileID(), len(s2.Profiles()))
	}
	if got := s2.ProfileTimeout("small"); got != 30*time.Second {
		t.Fatalf("profil zaman aşımı: %v", got)
	}
	if got := s2.ProfileTimeout("default"); got != defaultTimeout {
		t.Fatalf("küresel zaman aşımı: %v", got)
	}
	// düz SavePersisted (eski form) varsayılanı düzenler, öteki profil ve harita kalır
	_ = s2.SetSurfaceProfiles(context.Background(), store, map[string]string{"chat-intent": "default"})
	if err := s2.SavePersisted(context.Background(), store, "openai", "", "q-new", "http://s/v1", false, true, 0, nil, 0, nil, ""); err != nil {
		t.Fatal(err)
	}
	ps := s2.Profiles()
	if len(ps) != 2 || ps[1].ID != "small" || ps[1].Model != "q-new" || ps[0].APIKey != "kd" || s2.SurfaceProfiles()["chat-intent"] != "default" {
		t.Fatalf("düz kayıt öteki profili/haritayı bozdu: %+v %v", ps, s2.SurfaceProfiles())
	}
}

func TestProfiles_RefreshKeepsRuntime(t *testing.T) {
	s := New("openai", "k", "m")
	list := []ModelProfile{{ID: "a", Provider: ProviderOpenAI, BaseURL: "http://a/v1", APIKey: "ka", Model: "ma"}}
	s.SetProfiles(list, "a", nil)
	s.mu.RLock()
	before := s.profiles["a"]
	s.mu.RUnlock()
	s.SetProfiles(list, "a", nil) // 30 s config refresh: aynı şekil
	s.mu.RLock()
	after := s.profiles["a"]
	s.mu.RUnlock()
	if before != after || before.cli != after.cli {
		t.Fatal("değişmeyen profilin runtime'ı yeniden kuruldu")
	}
	s.SetProfiles([]ModelProfile{{ID: "a", Provider: ProviderOpenAI, BaseURL: "http://a/v1", APIKey: "ka", Model: "ma", TimeoutS: 20}}, "a", nil)
	s.mu.RLock()
	changed := s.profiles["a"]
	s.mu.RUnlock()
	if changed == after || changed.cli.Timeout != 20*time.Second {
		t.Fatalf("değişen profil yeniden kurulmadı: %v", changed.cli.Timeout)
	}
}

func TestValidateProfileAndSurfaceGroups(t *testing.T) {
	if err := ValidateProfile(ModelProfile{ID: "Büyük", Provider: ProviderOpenAI}); err == nil {
		t.Fatal("kimlik biçimi kabul edildi")
	}
	if err := ValidateProfile(ModelProfile{ID: "ok-1", Provider: "azure"}); err == nil {
		t.Fatal("bilinmeyen sağlayıcı kabul edildi")
	}
	if err := ValidateProfile(ModelProfile{ID: "ok-1", Provider: ProviderOpenAI, MaxTokens: 5}); err == nil || !strings.Contains(err.Error(), "maxTokens") {
		t.Fatalf("tuning doğrulaması: %v", err)
	}
	m, err := SurfaceMapFromGroups(map[string]string{SurfaceGroupIntent: "small", SurfaceGroupBackground: ""})
	if err != nil || m["chat-intent"] != "small" || m["problem-auto-explain"] != "" || m["exception-auto-explain"] != "" {
		t.Fatalf("grup → harita: %v %v", m, err)
	}
	if _, err := SurfaceMapFromGroups(map[string]string{"x": "small"}); err == nil {
		t.Fatal("bilinmeyen grup kabul edildi")
	}
	g := GroupsFromSurfaceMap(map[string]string{"chat-intent": "small"})
	if g[SurfaceGroupIntent] != "small" || g[SurfaceGroupBackground] != "" {
		t.Fatalf("harita → grup: %v", g)
	}
}

// İnceleme #17c/d: eski düz kayıt varsayılanın etiketi + profil tuning'ini korur;
// profil skipTls taşıyıcıya ulaşır.
func TestProfiles_LegacySaveKeepsLabelAndTuning_SkipTLSReachesTransport(t *testing.T) {
	store := newMemStore()
	s := New("openai", "k", "m")
	if err := s.UpsertProfile(context.Background(), store, ModelProfile{ID: DefaultProfileID, Label: "Ana", Provider: ProviderOpenAI, BaseURL: "http://d/v1", APIKey: "k", Model: "m", MaxTokens: 1024, TimeoutS: 45, SkipTLS: true}); err != nil {
		t.Fatal(err)
	}
	if err := s.SavePersisted(context.Background(), store, "openai", "k2", "m2", "http://d2/v1", false, true, 0, nil, 0, nil, ""); err != nil {
		t.Fatal(err)
	}
	p := s.Profiles()[0]
	if p.Label != "Ana" || p.MaxTokens != 1024 || p.TimeoutS != 45 || p.APIKey != "k2" || p.Model != "m2" || p.SkipTLS {
		t.Fatalf("düz kayıt etiket/tuning'i bozdu ya da alanları yazmadı: %+v", p)
	}
	s.mu.RLock()
	rt := s.profiles[DefaultProfileID]
	tr, _ := rt.cli.Transport.(*http.Transport)
	s.mu.RUnlock()
	if rt.cli.Timeout != 45*time.Second || tr == nil || tr.TLSClientConfig == nil || tr.TLSClientConfig.InsecureSkipVerify {
		t.Fatalf("istemci profil ayarlarını taşımıyor: timeout=%v tls=%v", rt.cli.Timeout, tr != nil && tr.TLSClientConfig != nil && tr.TLSClientConfig.InsecureSkipVerify)
	}
	_ = s.UpsertProfile(context.Background(), store, ModelProfile{ID: "x", Provider: ProviderOpenAI, BaseURL: "https://x/v1", Model: "mx", SkipTLS: true})
	s.mu.RLock()
	xr := s.profiles["x"]
	xtr, _ := xr.cli.Transport.(*http.Transport)
	s.mu.RUnlock()
	if xtr == nil || xtr.TLSClientConfig == nil || !xtr.TLSClientConfig.InsecureSkipVerify {
		t.Fatal("profil skipTls taşıyıcıya ulaşmadı")
	}
}

// İnceleme #1/#2: probe ana anahtar KAPALIYKEN ve varsayılan anahtarsızken
// hedef profile gider; ai_calls yüzeyi ctx'ten korunur (Explain ise reddeder).
func TestProbeProfileBypassesMasterSwitchAndDefaultCreds(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"pong"}}],"usage":{"prompt_tokens":1,"completion_tokens":1}}`))
	}))
	defer srv.Close()
	s := New("anthropic", "", "") // varsayılan anahtarsız → Active() false
	s.SetProfiles([]ModelProfile{
		{ID: DefaultProfileID, Provider: ProviderAnthropic},
		{ID: "local", Provider: ProviderOpenAI, BaseURL: srv.URL, Model: "m"},
	}, DefaultProfileID, nil)
	s.SetEnabled(false)
	rec := &captureRecorder{}
	s.SetRecorder(rec)
	if _, err := s.Explain(context.Background(), "s", "u"); err == nil {
		t.Fatal("Explain kapalıyken/anahtarsızken geçti")
	}
	ctx := WithMeta(context.Background(), CallMeta{Surface: "settings-probe"})
	out, err := s.ProbeProfile(ctx, "local", "s", "ping")
	if err != nil || out != "pong" || !strings.HasSuffix(gotPath, "/chat/completions") {
		t.Fatalf("probe: out=%q err=%v path=%s", out, err, gotPath)
	}
	if _, err := s.ProbeProfile(ctx, "yok", "s", "u"); err == nil || !strings.Contains(err.Error(), "profil yok") {
		t.Fatalf("bilinmeyen profil: %v", err)
	}
	// Kayıt asenkron (5 s'lik goroutine) — kısa bekleme
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && rec.last().Surface == "" {
		time.Sleep(10 * time.Millisecond)
	}
	if r := rec.last(); r.Surface != "settings-probe" || r.Model != "m" || r.BaseURL != srv.URL {
		t.Fatalf("ai_calls satırı: surface=%q model=%q base=%q", r.Surface, r.Model, r.BaseURL)
	}
}
