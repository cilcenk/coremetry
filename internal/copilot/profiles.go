package copilot

// profiles.go — v0.10.175 (operatör: "birden fazla model eklenebilsin — ayrı
// endpoint'ler de olabilir; varsayılanı admin seçer"; spec onayı 2026-08-30).
//
// Tek provider/baseURL/apiKey/model yerine MODEL PROFİLLERİ: her profil kendi
// sağlayıcısı, endpoint'i, anahtarı, model adı, TLS ayarı ve isteğe bağlı
// tuning'iyle (maxTokens/temperature/timeoutS; 0/nil = küresel) bağımsız bir
// uç. Admin bir profili VARSAYILAN seçer; çağrı profili şu sırayla çözülür:
//   WithProfile(ctx, id) > surfaceProfiles[CallMeta.Surface] > varsayılan.
// Yüzey eşlemesi iki grup için kullanılır (chat-intent → küçük yerel model,
// *-auto-explain → arka plan), ama harita ham yüzey adıyla saklanır.
//
// Geriye uyumluluk — iki yönde:
//   - Eski düz alanlar (s.provider/apiKey/model/baseURL/skipTLS/cli) VARSAYILAN
//     profilin aynasıdır: Snapshot/Configured/Active/ActiveModel ve eski
//     Configure/SavePersisted çağrıları aynen çalışır (Configure = varsayılan
//     profili düzenler, ötekilere dokunmaz).
//   - Blob (persisted) profiller listesini VE düz alanları birlikte yazar:
//     rolling deploy'da eski binary düz alanları okur, yeni binary listeyi
//     ([[feedback-distributed-column-safety]] sınıfı — prod'u iki kez kırdı).
//     Profilsiz eski blob → tek 'default' profile göç (LoadPersisted).
// Profil başına http.Client (skipTLS + zaman aşımı) ve GitHub oturum jetonu;
// JSON/stream yetenek önbellekleri zaten (provider, base, model) anahtarlı —
// profil ayrımı için yeterli, dokunulmadı. Değişmeyen profilin runtime'ı
// SetProfiles'ta korunur (30 s'lik config refresh istemciyi yeniden kurmaz).

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"time"
)

// DefaultProfileID — göçte ve New()'da üretilen tek profilin kimliği.
const DefaultProfileID = "default"

// MaxProfiles — tek system_settings blobu her AI yazımında yeniden yazılır (#13).
const MaxProfiles = 20

// ErrProfileNotFound — API 404'e çevirir (#12).
var ErrProfileNotFound = errors.New("profil yok")

type ModelProfile struct {
	ID       string `json:"id"`
	Label    string `json:"label,omitempty"`
	Provider string `json:"provider"`
	BaseURL  string `json:"baseUrl,omitempty"`
	// APIKey blob'da saklanır; API katmanı ASLA geri vermez (hasKey).
	APIKey  string `json:"apiKey,omitempty"`
	Model   string `json:"model,omitempty"`
	SkipTLS bool   `json:"skipTls,omitempty"`
	// Profil başına tuning; 0 / nil = küresel değer (ConfigureTuning).
	MaxTokens   int      `json:"maxTokens,omitempty"`
	Temperature *float64 `json:"temperature,omitempty"`
	TimeoutS    int      `json:"timeoutS,omitempty"`
}

// sameProfile — Temperature işaretçi olduğu için == kullanılamaz.
func sameProfile(a, b ModelProfile) bool {
	if a.ID != b.ID || a.Label != b.Label || a.Provider != b.Provider || a.BaseURL != b.BaseURL ||
		a.APIKey != b.APIKey || a.Model != b.Model || a.SkipTLS != b.SkipTLS ||
		a.MaxTokens != b.MaxTokens || a.TimeoutS != b.TimeoutS {
		return false
	}
	if (a.Temperature == nil) != (b.Temperature == nil) {
		return false
	}
	return a.Temperature == nil || *a.Temperature == *b.Temperature
}

type profileRuntime struct {
	cfg        ModelProfile
	cli        *http.Client
	cliSkipTLS bool
	ghSessTok  string
	ghSessExp  time.Time
}

func (rt *profileRuntime) effectiveTimeout(global time.Duration) time.Duration {
	if rt.cfg.TimeoutS > 0 {
		return time.Duration(rt.cfg.TimeoutS) * time.Second
	}
	return global
}

// ensureClient — zaman aşımı / TLS değişmediyse mevcut istemci kalır.
func (rt *profileRuntime) ensureClient(global time.Duration) {
	want := rt.effectiveTimeout(global)
	if rt.cli != nil && rt.cli.Timeout == want && rt.cliSkipTLS == rt.cfg.SkipTLS {
		return
	}
	rt.cli = buildCopilotHTTPClient(rt.cfg.SkipTLS, want)
	rt.cliSkipTLS = rt.cfg.SkipTLS
}

type profileKey struct{}

// WithProfile — bu çağrı için profil override'ı (bağlantı testi, ileride sohbet seçici).
func WithProfile(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, profileKey{}, id)
}

func profileFromContext(ctx context.Context) string {
	v, _ := ctx.Value(profileKey{}).(string)
	return v
}

var profileIDRe = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,39}$`)

// ValidateProfile — kimlik biçimi, sağlayıcı, tuning sınırları (ValidateTuning).
func ValidateProfile(p ModelProfile) error {
	if !profileIDRe.MatchString(p.ID) {
		return errors.New("profil kimliği [a-z0-9][a-z0-9_-]{0,39} olmalı")
	}
	switch p.Provider {
	case ProviderAnthropic, ProviderGitHub, ProviderOpenAI:
	default:
		return errors.New("provider 'anthropic', 'github' ya da 'openai' olmalı")
	}
	if len(p.Label) > 60 {
		return errors.New("label ≤ 60 karakter")
	}
	return ValidateTuning(p.MaxTokens, p.Temperature, p.TimeoutS)
}

// ── küme yönetimi (s.mu altında) ───────────────────────────────────────

func (s *Service) setProfilesLocked(profiles []ModelProfile, defaultID string, surface map[string]string) {
	next := make(map[string]*profileRuntime, len(profiles))
	order := make([]string, 0, len(profiles))
	rebuilt := false
	for _, p := range profiles {
		if p.ID == "" {
			continue
		}
		if _, dup := next[p.ID]; dup { // elle düzenlenmiş blobda kopya kimlik (#14)
			continue
		}
		if p.Provider == "" {
			p.Provider = ProviderAnthropic
		}
		rt := &profileRuntime{cfg: p}
		if old := s.profiles[p.ID]; old != nil {
			if sameProfile(old.cfg, p) {
				rt = old
			} else {
				rebuilt = true
				if old.cfg.Provider == p.Provider && old.cfg.APIKey == p.APIKey {
					rt.ghSessTok, rt.ghSessExp = old.ghSessTok, old.ghSessExp
				}
			}
		} else {
			rebuilt = true
		}
		rt.ensureClient(s.clientTimeoutLocked())
		next[p.ID] = rt
		order = append(order, p.ID)
	}
	if rebuilt || len(next) != len(s.profiles) {
		// Bir profil değişti/eklendi/silindi → (provider, base, model) anahtarlı
		// yetenek önbellekleri sıfırlanır (aynı üçlü, farklı TLS/anahtar — #10).
		// Değişmeyen küme (30 s refresh) DOKUNMAZ.
		s.streamUnsupported = nil
		s.jsonModeUnsupported = nil
		s.jsonSchemaUnsupported = nil
	}
	s.profiles = next
	s.profileOrder = order
	if _, ok := next[defaultID]; !ok {
		defaultID = ""
		if len(order) > 0 {
			defaultID = order[0]
		}
	}
	s.defaultID = defaultID
	s.surfaceProfiles = map[string]string{}
	for k, v := range surface {
		if _, ok := next[v]; ok && k != "" {
			s.surfaceProfiles[k] = v
		}
	}
	s.mirrorDefaultLocked()
}

// mirrorDefaultLocked — düz alanlar = varsayılan profil (bkz. dosya başlığı).
func (s *Service) mirrorDefaultLocked() {
	d := s.profiles[s.defaultID]
	if d == nil {
		return
	}
	s.provider, s.apiKey, s.model, s.baseURL, s.skipTLS = d.cfg.Provider, d.cfg.APIKey, d.cfg.Model, d.cfg.BaseURL, d.cfg.SkipTLS
	s.cli, s.cliSkipTLS = d.cli, d.cliSkipTLS
}

// profilesLocked — kayıt sırasıyla kopya (anahtarlar dahil; API katmanı soyar).
func (s *Service) profilesLocked() []ModelProfile {
	out := make([]ModelProfile, 0, len(s.profileOrder))
	for _, id := range s.profileOrder {
		if rt := s.profiles[id]; rt != nil {
			out = append(out, rt.cfg)
		}
	}
	return out
}

func (s *Service) resolveProfileLocked(ctx context.Context) *profileRuntime {
	if id := profileFromContext(ctx); id != "" {
		if rt := s.profiles[id]; rt != nil {
			return rt
		}
	}
	if id := s.surfaceProfiles[MetaFromContext(ctx).Surface]; id != "" {
		if rt := s.profiles[id]; rt != nil {
			return rt
		}
	}
	if rt := s.profiles[s.defaultID]; rt != nil {
		return rt
	}
	// Profil kümesi boş (yalnız eski düz alanlar) → geçici aynadan.
	return &profileRuntime{cfg: ModelProfile{ID: DefaultProfileID, Provider: s.provider, APIKey: s.apiKey, Model: s.model, BaseURL: s.baseURL, SkipTLS: s.skipTLS}, cli: s.cli, cliSkipTLS: s.cliSkipTLS}
}

// clientForLocked — varsayılan profil için AYNA (s.cli) yetkilidir: eski
// yol ve testler s.cli'yi doğrudan değiştirir (captureRT); öteki profiller
// kendi istemcisini kullanır.
func (s *Service) clientForLocked(rt *profileRuntime) *http.Client {
	if rt.cfg.ID == s.defaultID && s.cli != nil {
		return s.cli
	}
	if rt.cli == nil {
		return s.cli
	}
	return rt.cli
}

// profileIdentity — çağrının gideceği profilin (provider, model, baseURL) üçlüsü.
func (s *Service) profileIdentity(ctx context.Context) (provider, model, baseURL string) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	rt := s.resolveProfileLocked(ctx)
	return rt.cfg.Provider, rt.cfg.Model, rt.cfg.BaseURL
}

// ── dışa açık okuma ────────────────────────────────────────────────────

// Profiles — kayıt sırasıyla profiller (anahtarlar DAHİL — çağıran soyar).
func (s *Service) Profiles() []ModelProfile {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.profilesLocked()
}

func (s *Service) DefaultProfileID() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.defaultID
}

// SurfaceProfiles — yüzey → profil kimliği (kopya).
func (s *Service) SurfaceProfiles() map[string]string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make(map[string]string, len(s.surfaceProfiles))
	for k, v := range s.surfaceProfiles {
		out[k] = v
	}
	return out
}

// ProfileTimeout — profilin etkin istemci zaman aşımı ("" = varsayılan).
func (s *Service) ProfileTimeout(id string) time.Duration {
	s.mu.RLock()
	defer s.mu.RUnlock()
	rt := s.profiles[id]
	if rt == nil {
		rt = s.profiles[s.defaultID]
	}
	if rt == nil {
		return s.clientTimeoutLocked()
	}
	return rt.effectiveTimeout(s.clientTimeoutLocked())
}

// ProfilesSnapshot — tek RLock altında tutarlı üçlü (API yükü; #15).
func (s *Service) ProfilesSnapshot() (profiles []ModelProfile, defaultID string, surface map[string]string) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	surface = make(map[string]string, len(s.surfaceProfiles))
	for k, v := range s.surfaceProfiles {
		surface[k] = v
	}
	return s.profilesLocked(), s.defaultID, surface
}

// activeFor — çağrının ÇÖZÜLEN profili kimlik taşıyor mu (anahtar ya da
// openai + base URL); ana anahtar ayrı. Active()'in profil-farkında hâli:
// varsayılan anahtarsızken bile anahtarlı profil çalışır (#1).
func (s *Service) activeFor(ctx context.Context, requireEnabled bool) bool {
	if s == nil {
		return false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if requireEnabled && !s.enabled {
		return false
	}
	p := s.resolveProfileLocked(ctx).cfg
	return p.APIKey != "" || (p.Provider == ProviderOpenAI && p.BaseURL != "")
}

// SetProfiles — kümeyi değiştirir (yükleme/göç). Kalıcılık için Save* kullan.
func (s *Service) SetProfiles(profiles []ModelProfile, defaultID string, surface map[string]string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.setProfilesLocked(profiles, defaultID, surface)
}

// ── kalıcı düzenlemeler (blob + bellek) ────────────────────────────────

// UpsertProfile — ekler/günceller; boş APIKey mevcut anahtarı KORUR (UI boş
// kutuyu "değişmedi" diye gönderir — Secrets in Settings kuralı). İlk profil
// otomatik varsayılan olur.
func (s *Service) UpsertProfile(ctx context.Context, store SettingsStore, p ModelProfile) error {
	if err := ValidateProfile(p); err != nil {
		return err
	}
	s.saveMu.Lock() // oku-değiştir-yaz tek sırada (#8)
	defer s.saveMu.Unlock()
	s.mu.Lock()
	list := s.profilesLocked()
	found := false
	for i := range list {
		if list[i].ID == p.ID {
			if p.APIKey == "" {
				p.APIKey = list[i].APIKey
			}
			list[i] = p
			found = true
		}
	}
	if !found {
		if len(list) >= MaxProfiles {
			s.mu.Unlock()
			return fmt.Errorf("en fazla %d profil", MaxProfiles)
		}
		list = append(list, p)
	}
	def := s.defaultID
	if def == "" {
		def = p.ID
	}
	surface := s.surfaceProfiles
	s.mu.Unlock()
	return s.saveProfiles(ctx, store, list, def, surface)
}

// DeleteProfile — varsayılan silinemez (önce başka profili varsayılan yap).
func (s *Service) DeleteProfile(ctx context.Context, store SettingsStore, id string) error {
	s.saveMu.Lock()
	defer s.saveMu.Unlock()
	s.mu.Lock()
	if s.profiles[id] == nil {
		s.mu.Unlock()
		return fmt.Errorf("%w: %s", ErrProfileNotFound, id)
	}
	if id == s.defaultID {
		s.mu.Unlock()
		return errors.New("varsayılan profil silinemez — önce başka bir profili varsayılan yap")
	}
	list := make([]ModelProfile, 0, len(s.profiles))
	for _, p := range s.profilesLocked() {
		if p.ID != id {
			list = append(list, p)
		}
	}
	surface := map[string]string{}
	for k, v := range s.surfaceProfiles {
		if v != id {
			surface[k] = v
		}
	}
	def := s.defaultID
	s.mu.Unlock()
	return s.saveProfiles(ctx, store, list, def, surface)
}

func (s *Service) SetDefaultProfile(ctx context.Context, store SettingsStore, id string) error {
	s.saveMu.Lock()
	defer s.saveMu.Unlock()
	s.mu.Lock()
	if s.profiles[id] == nil {
		s.mu.Unlock()
		return fmt.Errorf("%w: %s", ErrProfileNotFound, id)
	}
	list, surface := s.profilesLocked(), s.surfaceProfiles
	s.mu.Unlock()
	return s.saveProfiles(ctx, store, list, id, surface)
}

// SetSurfaceProfiles — yüzey → profil haritasını değiştirir ("" = sil).
func (s *Service) SetSurfaceProfiles(ctx context.Context, store SettingsStore, surface map[string]string) error {
	s.saveMu.Lock()
	defer s.saveMu.Unlock()
	s.mu.Lock()
	for k, v := range surface {
		if v != "" && s.profiles[v] == nil {
			s.mu.Unlock()
			return fmt.Errorf("%w: %s (%s)", ErrProfileNotFound, v, k)
		}
	}
	list, def := s.profilesLocked(), s.defaultID
	s.mu.Unlock()
	clean := map[string]string{}
	for k, v := range surface {
		if v != "" {
			clean[k] = v
		}
	}
	return s.saveProfiles(ctx, store, list, def, clean)
}

// saveProfiles — blobu (profiller + varsayılanın düz aynası + küresel vidalar)
// yazar, sonra belleği günceller. Sıra: önce disk (çok-pod refresh aynı
// şekli okur), sonra bellek.
func (s *Service) saveProfiles(ctx context.Context, store SettingsStore, list []ModelProfile, defaultID string, surface map[string]string) error {
	if len(list) == 0 {
		return errors.New("en az bir profil gerekli")
	}
	if _, ok := indexProfile(list, defaultID); !ok {
		defaultID = list[0].ID
	}
	d, _ := indexProfile(list, defaultID)
	s.mu.RLock()
	p := persisted{
		Provider: d.Provider, APIKey: d.APIKey, Model: d.Model, BaseURL: d.BaseURL, SkipTLS: d.SkipTLS,
		Enabled:   boolPtr(s.enabled),
		MaxTokens: s.maxTokens, Temperature: s.temperature, TimeoutS: s.timeoutS,
		AutoExplain: s.autoExplain, IntentClassify: s.intentClassify,
		Profiles: list, DefaultProfile: defaultID, SurfaceProfiles: surface,
	}
	s.mu.RUnlock()
	if store != nil {
		raw, err := marshalPersisted(p)
		if err != nil {
			return err
		}
		if err := store.PutSetting(ctx, settingsKey, raw); err != nil {
			return err
		}
	}
	s.SetProfiles(list, defaultID, surface)
	return nil
}

func indexProfile(list []ModelProfile, id string) (ModelProfile, bool) {
	for _, p := range list {
		if p.ID == id {
			return p, true
		}
	}
	return ModelProfile{}, false
}

func boolPtr(b bool) *bool { return &b }

// ProfileIDs — sıralı kimlikler (hata mesajları / testler).
func (s *Service) ProfileIDs() []string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	ids := append([]string(nil), s.profileOrder...)
	sort.Strings(ids)
	return ids
}

// Surface grupları — UI iki seçici gösterir, harita ham yüzeylerle saklanır.
const (
	SurfaceGroupIntent     = "intent"
	SurfaceGroupBackground = "background"
)

var surfaceGroups = map[string][]string{
	SurfaceGroupIntent:     {"chat-intent", "chat-intent-none"}, // none satırı da aynı profille yazılır (#6)
	SurfaceGroupBackground: {"problem-auto-explain", "exception-auto-explain"},
}

// ProbeProfile — bağlantı yoklaması: ana anahtar KAPALIYKEN ve varsayılan
// anahtarsızken de çalışır (onboarding sırası: ekle → dene → varsayılan yap,
// #1); ai_calls satırı ctx'teki CallMeta yüzeyiyle yazılır (#2).
func (s *Service) ProbeProfile(ctx context.Context, id, systemPrompt, userPrompt string) (string, error) {
	s.mu.RLock()
	_, ok := s.profiles[id]
	s.mu.RUnlock()
	if !ok {
		return "", fmt.Errorf("%w: %s", ErrProfileNotFound, id)
	}
	return s.explain(WithProfile(ctx, id), systemPrompt, userPrompt, false)
}

// SurfaceMapFromGroups — {intent: id, background: id} → ham yüzey haritası.
func SurfaceMapFromGroups(groups map[string]string) (map[string]string, error) {
	out := map[string]string{}
	for g, id := range groups {
		surfaces, ok := surfaceGroups[g]
		if !ok {
			return nil, fmt.Errorf("bilinmeyen yüzey grubu: %s", g)
		}
		for _, sf := range surfaces {
			if strings.TrimSpace(id) != "" {
				out[sf] = id
			} else {
				out[sf] = ""
			}
		}
	}
	return out, nil
}

// GroupsFromSurfaceMap — ham harita → {intent, background} (grubun ilk yüzeyi temsil eder).
func GroupsFromSurfaceMap(m map[string]string) map[string]string {
	out := map[string]string{}
	for g, surfaces := range surfaceGroups {
		out[g] = m[surfaces[0]]
	}
	return out
}
