package api

// anomaly_verdicts.go — v0.10.181 (etüt «anomali işaretleri» dilim 2, geri
// bildirim akışı: «anomali / değil»).
//
// api.go BÜYÜMEYECEK kuralı: uçlar burada, api.go tek satır register.
//
//   PUT /api/anomalies/{id}/verdict         — editor+; {verdict, note?, kind,
//                                             pattern, service}
//
// Okuma ucu YOK (v0.10.184, inceleme #2): kararı events ucu satıra ekler
// (EnrichAnomaliesWithVerdicts) — çağıransız GET yüzeyi kaldırıldı.
// Parmak izi kanonik sha1 (FingerprintAnomaly) — pattern/service boşsa ""
// (ham id yazılmaz; desen istatistiği yanlış anahtar görmesin, #4).
// "anomaly:" önbelleği yazımda düşer. «Değil» susturma DEĞİLDİR — UI
// isterse ikisini zincirler (Overview muteAnomaly).

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/cilcenk/coremetry/internal/auth"
	"github.com/cilcenk/coremetry/internal/chstore"
)

func (s *Server) registerAnomalyVerdictRoutes(mux *http.ServeMux) {
	mux.HandleFunc("PUT /api/anomalies/{id}/verdict", auth.RequireAnyRole(editorRoles, s.putAnomalyVerdict))
}

// verdictFingerprint — kanonik sha1 yalnız pattern+service doluyken; aksi "" (#4).
func verdictFingerprint(kind, pattern, service string) string {
	if strings.TrimSpace(pattern) == "" || strings.TrimSpace(service) == "" {
		return ""
	}
	return chstore.FingerprintAnomaly(kind, pattern, service)
}

// truncRunes — rune sınırında kes (bayt kesimi UTF-8'i bölüp audit JSON'unu
// bozuyordu, #3).
func truncRunes(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n])
	}
	return s
}

func (s *Server) putAnomalyVerdict(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(r.PathValue("id"))
	var body struct {
		Verdict string `json:"verdict"`
		Note    string `json:"note"`
		Kind    string `json:"kind"`
		Pattern string `json:"pattern"`
		Service string `json:"service"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSONError(w, http.StatusBadRequest, "geçersiz JSON: "+err.Error())
		return
	}
	if id == "" || !chstore.ValidVerdict(body.Verdict) {
		writeJSONError(w, http.StatusBadRequest, "verdict 'anomaly' ya da 'not_anomaly' olmalı")
		return
	}
	body.Note = truncRunes(strings.TrimSpace(body.Note), 500)
	now := time.Now()
	v := chstore.AnomalyVerdict{
		EventID: id, Fingerprint: verdictFingerprint(body.Kind, body.Pattern, body.Service),
		Kind: body.Kind, Pattern: body.Pattern, Service: body.Service,
		Verdict: body.Verdict, Note: strings.TrimSpace(body.Note),
		CreatedBy: claimEmail(auth.FromContext(r.Context())), CreatedAt: now.UnixNano(),
	}
	if err := s.store.UpsertAnomalyVerdict(r.Context(), v); err != nil {
		writeErr(w, err)
		return
	}
	s.cacheInvalidatePrefix(r.Context(), "anomaly:")
	details, _ := json.Marshal(map[string]string{"verdict": body.Verdict, "kind": body.Kind, "service": body.Service, "note": v.Note}) // geçerli JSON (#3)
	s.audit(r, "anomaly.verdict", "anomaly", id, string(details))
	writeJSON(w, v)
}
