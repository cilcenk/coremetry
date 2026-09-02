package api

// preferences_routes.go — v0.10.247 (DataTable/ContextBar audit §11):
// kullanıcı-kapsamlı UI tercihi (ilk kullanım: kalıcı sütun modeli).
//
//   GET    /api/preferences/{key}   → {key, model|null, updatedAt}
//   PUT    /api/preferences/{key}   ← ColumnModel JSON (≤16 KiB)
//   DELETE /api/preferences/{key}   → tombstone (name='' — ListSavedViews atlar)
//
// Kayıt api.go'ya DEĞİL, route_registry.go defterine (init). Depolama
// invariant #5: yeni tablo YOK — saved_views satırı, page='table:<key>',
// owner_id=claims.UserID, name='columns', query_string=JSON, id
// deterministik 'pref:<uid>:<key>' → UpsertSavedView doğal upsert
// (ReplacingMergeTree; tam-satır replace: tüm alanlar taşınır),
// GetSavedView FINAL nokta okuma. `table:` öneki tercihleri SavedViewsBar
// listesinden (page='traces' tam eşitlik) uzak tutar. ai-chat emsali
// (ai_conversations.go) aynı tabloyu blob olarak kullanır.
//
// Rol kapısı YOK (her oturum açmış rol, viewer dahil — kişisel durum;
// "viewer salt-okur" paylaşılan durumu hedefler). s.audit YOK (admin
// yazımı değil, idempotent, kullanıcı-kapsamlı). serveCached YOK (kişisel
// FINAL nokta okuma; cache anahtarına uid girerdi). Claims yoksa 401 —
// ASLA owner_id='' paylaşımlı satıra düşme. Sunucu yalnız ŞEKLİ doğrular
// (id regex, ≤64 giriş), kolon kimliklerini yorumlamaz.

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/cilcenk/coremetry/internal/auth"
	"github.com/cilcenk/coremetry/internal/chstore"
)

func init() { registerRoutesExtra("preferences", (*Server).registerPreferencesRoutes) }

func (s *Server) registerPreferencesRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/preferences/{key}", s.getPreference)
	mux.HandleFunc("PUT /api/preferences/{key}", s.putPreference)
	mux.HandleFunc("DELETE /api/preferences/{key}", s.deletePreference)
}

const (
	preferenceBodyMax    = 16 << 10 // 16 KiB
	preferenceEntryMax   = 64
	preferencePagePrefix = "table:"
)

var (
	preferenceKeyRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,63}$`)
	columnIDRe      = regexp.MustCompile(`^[A-Za-z0-9_.:-]{1,64}$`)
)

// preferenceID — deterministik satır kimliği (SAF).
func preferenceID(uid, key string) string { return "pref:" + uid + ":" + key }

// validPreferenceKey — SAF; regex + boş değil.
func validPreferenceKey(key string) bool { return preferenceKeyRe.MatchString(key) }

// columnModelBody — istemcinin ColumnModel'i (lib/columnModel.ts aynası).
// widths kabul edilir ama tarayıcı-yerel kalır (dilim 1) — sunucu yazmaz.
type columnModelBody struct {
	V      int                `json:"v"`
	Order  []string           `json:"order"`
	Hidden []string           `json:"hidden"`
	Widths map[string]float64 `json:"widths,omitempty"`
	Sig    string             `json:"sig"`
}

// validateColumnModel — SAF şekil doğrulaması: v=1, ≤64 giriş, her id
// regex'e uyar, sig ≤ 128. Anlam (kolon var mı) istemcinin işi.
func validateColumnModel(m columnModelBody) error {
	if m.V != 1 {
		return errors.New("v=1 bekleniyor")
	}
	if len(m.Order) == 0 || len(m.Order) > preferenceEntryMax || len(m.Hidden) > preferenceEntryMax {
		return fmt.Errorf("order 1-%d, hidden 0-%d giriş olmalı", preferenceEntryMax, preferenceEntryMax)
	}
	for _, id := range append(append([]string{}, m.Order...), m.Hidden...) {
		if !columnIDRe.MatchString(id) {
			return fmt.Errorf("geçersiz kolon kimliği %q", id)
		}
	}
	if len(m.Sig) > 128 {
		return errors.New("sig çok uzun")
	}
	return nil
}

// canonicalColumnModelJSON — saklanan biçim: widths DÜŞER (tarayıcı-yerel),
// alan sırası sabit → aynı model aynı bayt (idempotent upsert).
func canonicalColumnModelJSON(m columnModelBody) string {
	b, _ := json.Marshal(struct {
		V      int      `json:"v"`
		Order  []string `json:"order"`
		Hidden []string `json:"hidden"`
		Sig    string   `json:"sig"`
	}{1, m.Order, nonNil(m.Hidden), m.Sig})
	return string(b)
}

func nonNil(a []string) []string {
	if a == nil {
		return []string{}
	}
	return a
}

func (s *Server) preferenceClaims(w http.ResponseWriter, r *http.Request) (*auth.Claims, string, bool) {
	claims := auth.FromContext(r.Context())
	if claims == nil || claims.UserID == "" {
		writeJSONError(w, http.StatusUnauthorized, "kimlik gerekli")
		return nil, "", false
	}
	key := strings.TrimSpace(r.PathValue("key"))
	if !validPreferenceKey(key) {
		writeJSONError(w, http.StatusBadRequest, "key: ^[a-z0-9][a-z0-9-]{0,63}$")
		return nil, "", false
	}
	return claims, key, true
}

func (s *Server) getPreference(w http.ResponseWriter, r *http.Request) {
	claims, key, ok := s.preferenceClaims(w, r)
	if !ok {
		return
	}
	v, err := s.store.GetSavedView(r.Context(), preferenceID(claims.UserID, key))
	if err != nil {
		writeErr(w, err)
		return
	}
	resp := map[string]any{"key": key, "model": nil}
	// Sahip izolasyonu: id zaten uid taşır; yine de owner eşleşmezse yok say.
	if v != nil && v.Name != "" && v.OwnerID == claims.UserID {
		resp["model"] = json.RawMessage(v.QueryString)
		resp["updatedAt"] = v.CreatedAt
	}
	writeJSON(w, resp)
}

func (s *Server) putPreference(w http.ResponseWriter, r *http.Request) {
	claims, key, ok := s.preferenceClaims(w, r)
	if !ok {
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, preferenceBodyMax))
	if err != nil {
		writeJSONError(w, http.StatusRequestEntityTooLarge, "gövde 16 KiB'ı aşamaz")
		return
	}
	var m columnModelBody
	if err := json.Unmarshal(body, &m); err != nil {
		writeJSONError(w, http.StatusBadRequest, "geçersiz JSON: "+err.Error())
		return
	}
	if err := validateColumnModel(m); err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	now := time.Now().UnixNano()
	if err := s.store.UpsertSavedView(r.Context(), chstore.SavedView{
		ID: preferenceID(claims.UserID, key), OwnerID: claims.UserID, Name: "columns",
		Page: preferencePagePrefix + key, QueryString: canonicalColumnModelJSON(m), CreatedAt: now,
	}); err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, map[string]any{"ok": true, "key": key, "updatedAt": now})
}

func (s *Server) deletePreference(w http.ResponseWriter, r *http.Request) {
	claims, key, ok := s.preferenceClaims(w, r)
	if !ok {
		return
	}
	// Tombstone: name='' (ListSavedViews atlar; GET model:null döner).
	if err := s.store.UpsertSavedView(r.Context(), chstore.SavedView{
		ID: preferenceID(claims.UserID, key), OwnerID: claims.UserID, Name: "",
		Page: preferencePagePrefix + key, QueryString: "", CreatedAt: time.Now().UnixNano(),
	}); err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, map[string]any{"ok": true, "key": key})
}
