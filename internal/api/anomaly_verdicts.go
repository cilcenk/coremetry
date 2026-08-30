package api

// anomaly_verdicts.go — v0.10.181 (etüt «anomali işaretleri» dilim 2, geri
// bildirim akışı: «anomali / değil»).
//
// api.go BÜYÜMEYECEK kuralı: uçlar burada, api.go tek satır register.
//
//   GET /api/anomalies/verdicts?ids=a,b,c   — viewer görür (karar = görünüm)
//   PUT /api/anomalies/{id}/verdict         — editor+; {verdict, note?, kind,
//                                             pattern, service}
//
// Parmak izi silenceFingerprint ile kanonik sha1 (susturmayla aynı anahtar
// uzayı); events ucu kararı okuma zamanı ekler (EnrichAnomaliesWithVerdicts)
// ve "anomaly:" önbelleği yazımda düşer. «Değil» susturma DEĞİLDİR — UI
// isterse ikisini zincirler (Overview muteAnomaly).

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/cilcenk/coremetry/internal/auth"
	"github.com/cilcenk/coremetry/internal/chstore"
)

func (s *Server) registerAnomalyVerdictRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/anomalies/verdicts", s.listAnomalyVerdicts)
	mux.HandleFunc("PUT /api/anomalies/{id}/verdict", auth.RequireAnyRole(editorRoles, s.putAnomalyVerdict))
}

// splitIDs — `ids=a,b,c` → boş/kopya elenmiş, en çok 200.
func splitIDs(raw string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, 16)
	for _, p := range strings.Split(raw, ",") {
		p = strings.TrimSpace(p)
		if p == "" || seen[p] {
			continue
		}
		seen[p] = true
		out = append(out, p)
		if len(out) >= 200 {
			break
		}
	}
	return out
}

func (s *Server) listAnomalyVerdicts(w http.ResponseWriter, r *http.Request) {
	ids := splitIDs(r.URL.Query().Get("ids"))
	if len(ids) == 0 {
		writeJSON(w, map[string]any{"items": map[string]chstore.AnomalyVerdict{}})
		return
	}
	vs, err := s.store.ListAnomalyVerdicts(r.Context(), ids)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, map[string]any{"items": vs})
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
	if len(body.Note) > 500 {
		body.Note = body.Note[:500]
	}
	now := time.Now()
	v := chstore.AnomalyVerdict{
		EventID: id, Fingerprint: silenceFingerprint(id, body.Kind, body.Pattern, body.Service),
		Kind: body.Kind, Pattern: body.Pattern, Service: body.Service,
		Verdict: body.Verdict, Note: strings.TrimSpace(body.Note),
		CreatedBy: claimEmail(auth.FromContext(r.Context())), CreatedAt: now.UnixNano(),
	}
	if err := s.store.UpsertAnomalyVerdict(r.Context(), v); err != nil {
		writeErr(w, err)
		return
	}
	s.cacheInvalidatePrefix(r.Context(), "anomaly:")
	s.audit(r, "anomaly.verdict", "anomaly", id,
		fmt.Sprintf(`{"verdict":%q,"kind":%q,"service":%q,"note":%q}`, body.Verdict, body.Kind, body.Service, v.Note))
	writeJSON(w, v)
}
