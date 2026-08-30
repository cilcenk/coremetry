package api

// ai_settings_profiles.go — v0.10.175 (operatör: "birden fazla model
// eklenebilsin, ayrı endpoint'ler de olabilir; varsayılanı admin seçer").
//
// api.go BÜYÜMEYECEK kuralı (registerVMetricsRoutes emsali): profil uçları
// burada, kayıt ai_routes.go'da (AI ucu sicili). Hepsi admin + audit; anahtar
// hiçbir yanıtta dönmez (hasKey), boş anahtar = mevcut korunur (Secrets in
// Settings kuralı). Her yazım publishConfigReload("ai") ile öteki pod'lara
// duyurulur (StartConfigRefresh de 30 s'de okur).
//
//   PUT    /api/settings/ai/profiles/{id}          upsert
//   DELETE /api/settings/ai/profiles/{id}          (varsayılan silinemez → 400)
//   POST   /api/settings/ai/profiles/{id}/default  varsayılan yap
//   POST   /api/settings/ai/profiles/{id}/test     bağlantı yoklaması (200 + {ok,error})
//   PUT    /api/settings/ai/surface-map            {intent?, background?} → profil kimliği ("" = varsayılan)
//
// Test uçu: bir bağlantı denemesinin başarısızlığı operatörün sorusuna
// BAŞARILI cevaptır → 200 + {ok:false,error} (vmetrics_handlers emsali).

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/cilcenk/coremetry/internal/auth"
	"github.com/cilcenk/coremetry/internal/copilot"
)

func (s *Server) registerAISettingsProfileRoutes(mux *http.ServeMux) {
	mux.HandleFunc("PUT /api/settings/ai/profiles/{id}", auth.RequireRole(auth.RoleAdmin, s.putAIProfile))
	mux.HandleFunc("DELETE /api/settings/ai/profiles/{id}", auth.RequireRole(auth.RoleAdmin, s.deleteAIProfile))
	mux.HandleFunc("POST /api/settings/ai/profiles/{id}/default", auth.RequireRole(auth.RoleAdmin, s.defaultAIProfile))
	mux.HandleFunc("POST /api/settings/ai/profiles/{id}/test", auth.RequireRole(auth.RoleAdmin, s.testAIProfile))
	mux.HandleFunc("PUT /api/settings/ai/surface-map", auth.RequireRole(auth.RoleAdmin, s.putAISurfaceMap))
}

// aiProfileView — API görünümü: anahtar YOK, hasKey VAR.
type aiProfileView struct {
	ID          string   `json:"id"`
	Label       string   `json:"label,omitempty"`
	Provider    string   `json:"provider"`
	BaseURL     string   `json:"baseUrl,omitempty"`
	Model       string   `json:"model,omitempty"`
	SkipTLS     bool     `json:"skipTls,omitempty"`
	HasKey      bool     `json:"hasKey"`
	MaxTokens   int      `json:"maxTokens,omitempty"`
	Temperature *float64 `json:"temperature,omitempty"`
	TimeoutS    int      `json:"timeoutS,omitempty"`
	Default     bool     `json:"default,omitempty"`
}

func aiProfileViews(profiles []copilot.ModelProfile, defaultID string) []aiProfileView {
	out := make([]aiProfileView, 0, len(profiles))
	for _, p := range profiles {
		out = append(out, aiProfileView{
			ID: p.ID, Label: p.Label, Provider: p.Provider, BaseURL: p.BaseURL, Model: p.Model, SkipTLS: p.SkipTLS,
			HasKey: p.APIKey != "", MaxTokens: p.MaxTokens, Temperature: p.Temperature, TimeoutS: p.TimeoutS,
			Default: p.ID == defaultID,
		})
	}
	return out
}

// aiProfilesPayload — GET /api/settings/ai'ın profil bölümü ve yazım uçlarının
// cevabı; tek RLock'lık snapshot (yırtık okuma yok, #15).
func (s *Server) aiProfilesPayload() map[string]any {
	profiles, def, surface := s.copilot.ProfilesSnapshot()
	return map[string]any{
		"profiles":       aiProfileViews(profiles, def),
		"defaultProfile": def,
		"surfaceMap":     copilot.GroupsFromSurfaceMap(surface),
	}
}

// profileErr — ErrProfileNotFound → 404, diğer → 400 (#12).
func profileErr(w http.ResponseWriter, err error) {
	if errors.Is(err, copilot.ErrProfileNotFound) {
		writeJSONError(w, http.StatusNotFound, err.Error())
		return
	}
	writeJSONError(w, http.StatusBadRequest, err.Error())
}

// resolveLegacyAPIKey — eski düz form: boş anahtar = MEVCUT korunur (kutunun
// ipucu bunu söylüyordu ama sunucu "" yazıyordu — v0.8.13'ten beri çelişki,
// dilim B ile kolay ulaşılır oldu, #5); açık temizleme clearKey ile.
func resolveLegacyAPIKey(in, current string, clear bool) string {
	if clear {
		return ""
	}
	if in == "" {
		return current
	}
	return in
}

func (s *Server) putAIProfile(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(r.PathValue("id"))
	var in struct {
		Label       string   `json:"label"`
		Provider    string   `json:"provider"`
		BaseURL     string   `json:"baseUrl"`
		APIKey      string   `json:"apiKey"`
		Model       string   `json:"model"`
		SkipTLS     bool     `json:"skipTls"`
		MaxTokens   int      `json:"maxTokens"`
		Temperature *float64 `json:"temperature"`
		TimeoutS    int      `json:"timeoutS"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeJSONError(w, http.StatusBadRequest, "geçersiz JSON: "+err.Error())
		return
	}
	p := copilot.ModelProfile{
		ID: id, Label: strings.TrimSpace(in.Label), Provider: strings.TrimSpace(in.Provider), BaseURL: strings.TrimSpace(in.BaseURL),
		APIKey: in.APIKey, Model: strings.TrimSpace(in.Model), SkipTLS: in.SkipTLS,
		MaxTokens: in.MaxTokens, Temperature: in.Temperature, TimeoutS: in.TimeoutS,
	}
	if err := copilot.ValidateProfile(p); err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := s.copilot.UpsertProfile(r.Context(), s.store, p); err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	s.publishConfigReload(r.Context(), "ai")
	s.audit(r, "settings.ai.profile.upsert", "settings", id,
		fmt.Sprintf("provider=%s model=%s baseUrl=%s hasKey=%v", p.Provider, p.Model, p.BaseURL, in.APIKey != ""))
	writeJSON(w, s.aiProfilesPayload())
}

func (s *Server) deleteAIProfile(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(r.PathValue("id"))
	if err := s.copilot.DeleteProfile(r.Context(), s.store, id); err != nil {
		profileErr(w, err)
		return
	}
	s.publishConfigReload(r.Context(), "ai")
	s.audit(r, "settings.ai.profile.delete", "settings", id, "")
	writeJSON(w, s.aiProfilesPayload())
}

func (s *Server) defaultAIProfile(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(r.PathValue("id"))
	if err := s.copilot.SetDefaultProfile(r.Context(), s.store, id); err != nil {
		profileErr(w, err)
		return
	}
	s.publishConfigReload(r.Context(), "ai")
	s.audit(r, "settings.ai.profile.default", "settings", id, "")
	writeJSON(w, s.aiProfilesPayload())
}

func (s *Server) putAISurfaceMap(w http.ResponseWriter, r *http.Request) {
	var groups map[string]string
	if err := json.NewDecoder(r.Body).Decode(&groups); err != nil {
		writeJSONError(w, http.StatusBadRequest, "geçersiz JSON: "+err.Error())
		return
	}
	// Kısmi gövde: verilmeyen grup MEVCUT değerini korur (#7).
	_, _, cur := s.copilot.ProfilesSnapshot()
	merged := copilot.GroupsFromSurfaceMap(cur)
	for k, v := range groups {
		merged[k] = v
	}
	m, err := copilot.SurfaceMapFromGroups(merged)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := s.copilot.SetSurfaceProfiles(r.Context(), s.store, m); err != nil {
		profileErr(w, err)
		return
	}
	s.publishConfigReload(r.Context(), "ai")
	b, _ := json.Marshal(groups)
	s.audit(r, "settings.ai.surface_map", "settings", "ai", string(b))
	writeJSON(w, s.aiProfilesPayload())
}

// testAIProfile — profil endpoint'ine kısa bir "ping": süre, model, hata.
// ProbeProfile: ana anahtar kapalıyken/varsayılan anahtarsızken de dener
// (#1); ai_calls satırı surface=settings-probe (ctx meta korunur, #2).
// copilotExplain sarmalayıcısı bilinçli KULLANILMIYOR: yüzeyi yoldan
// "other"a çevirip ana anahtara takılıyordu; atıf (kimlik + yüzey) burada.
func (s *Server) testAIProfile(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(r.PathValue("id"))
	c := auth.FromContext(r.Context())
	meta := copilot.CallMeta{Surface: "settings-probe"}
	if c != nil {
		meta.UserID, meta.UserEmail = c.UserID, c.Email
	}
	timeout := s.copilot.ProfileTimeout(id)
	if timeout > 20*time.Second {
		timeout = 20 * time.Second
	}
	ctx, cancel := context.WithTimeout(copilot.WithMeta(r.Context(), meta), timeout)
	defer cancel()
	t0 := time.Now()
	out, err := s.copilot.ProbeProfile(ctx, id, "Sen bir bağlantı testisin. Yalnız 'pong' yaz.", "ping")
	if errors.Is(err, copilot.ErrProfileNotFound) {
		profileErr(w, err)
		return
	}
	res := map[string]any{"ok": err == nil, "ms": time.Since(t0).Milliseconds(), "profile": id}
	if err != nil {
		res["error"] = err.Error()
	} else {
		if r := []rune(strings.TrimSpace(out)); len(r) > 80 {
			out = string(r[:80]) + "…"
		}
		res["sample"] = strings.TrimSpace(out)
	}
	s.audit(r, "settings.ai.profile.test", "settings", id, fmt.Sprintf("ok=%v", err == nil))
	writeJSON(w, res)
}
