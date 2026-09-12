package chstore

// incident_rootcause.go — v0.10.698 (Dynatrace paritesi #1, dilim B).
//
// Incident satırı bugüne dek yalnız "hangi servis" söylüyordu; kök neden
// bağlı problemlerin tek tek çekmecesinde saklıydı. Davis'in problem kartı
// gibi incident'ın kendisi bir kök neden taşımalı: bağlı problemlerin
// kalıcı hipotezleri (root_cause_hypotheses, anchor_kind='problem') içinden
// EN YÜKSEK GÜVENLİ TopSuspect. Okuma-anı zenginleştirme — Clusters /
// Problem.RootCause ile aynı duruş: incidents satırına yazılmaz, her listede
// TEK toplu okumayla eklenir (N+1 yok).
//
// Neden yalnız hipotezler, neden küme probleminin kaynak servisi DEĞİL:
// hipotez skoru ve küme SourceScore'u aynı ölçekte değil; ikisini karıştırmak
// operatöre tek sayı gibi görünen iki farklı güveni sunardı. Hipotez yalnız
// critical anchor'larda sentezlendiği için warning-only incident dürüstçe
// boş kalır ("—"), uydurulmaz (spec açık soru 3, operatör kararı 2026-09-12).

import (
	"context"
	"sort"
)

// incidentRootCauseMinConfidence — RootCauseRibbon'un çip eşiğiyle aynı
// (conf > 0.05): şeridin "belirsiz" saydığı bir hipotez incident satırında
// kök neden diye görünmesin. İki yüzey aynı problemi farklı yorumlamasın.
const incidentRootCauseMinConfidence = 0.05

// incidentProblemIDCap — bir listenin (≤200 incident) bağlı problem
// kümesi için tek sorgunun üst sınırı; OpenIncidentRollups'un 5000 tavanıyla
// aynı sınıf. Bağlı problem sayısı incident başına küçük (auto-attach
// 30 dk / 1-hop), tavan yalnız kaza sigortası.
const incidentProblemIDCap = 5000

// pickIncidentRootCause — SAF. Bağlı problemlerin özetlerinden incident'ın
// kök nedenini seçer: adı olan (TopSuspect != "") ve eşiği aşan adaylar
// içinden max Confidence; eşitlikte max TopScore; yine eşitse ad sırası
// (iki yazıcı / iki okuma aynı cevabı versin — determinizm). Hiç aday
// yoksa nil → JSON'da alan düşer, FE "—" çizer.
func pickIncidentRootCause(members []RootCauseSummary) *RootCauseSummary {
	var cands []RootCauseSummary
	for _, m := range members {
		if m.TopSuspect == "" || m.Confidence <= incidentRootCauseMinConfidence {
			continue
		}
		cands = append(cands, m)
	}
	if len(cands) == 0 {
		return nil
	}
	sort.SliceStable(cands, func(i, j int) bool {
		a, b := cands[i], cands[j]
		if a.Confidence != b.Confidence {
			return a.Confidence > b.Confidence
		}
		if a.TopScore != b.TopScore {
			return a.TopScore > b.TopScore
		}
		return a.TopSuspect < b.TopSuspect
	})
	best := cands[0]
	return &best
}

// IncidentProblemIDs — birden çok incident'ın bağlı problem id'leri TEK
// sorguda (incident_problems FINAL, IN-listesi bağlı literal). Anahtar
// problem_id olduğu için tarama tablo boyu; tablo küçük state tablosu,
// tavan + max_execution_time sigorta.
func (s *Store) IncidentProblemIDs(ctx context.Context, incidentIDs []string) (map[string][]string, error) {
	out := make(map[string][]string)
	ids := boundHypothesisIDs(incidentIDs)
	if len(ids) == 0 {
		return out, nil
	}
	args := make([]any, 0, len(ids))
	holders := ""
	for i, id := range ids {
		if i > 0 {
			holders += ", "
		}
		holders += "?"
		args = append(args, id)
	}
	rows, err := s.conn.Query(ctx,
		`SELECT incident_id, problem_id FROM incident_problems FINAL
		 WHERE incident_id IN (`+holders+`)
		 LIMIT `+itoa(int64(incidentProblemIDCap))+`
		 SETTINGS max_execution_time = 10`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var inc, pid string
		if err := rows.Scan(&inc, &pid); err != nil {
			return nil, err
		}
		out[inc] = append(out[inc], pid)
	}
	return out, rows.Err()
}

// EnrichIncidentsWithRootCause — liste/detay handler'ının çağırdığı toplu
// zenginleştirme: incident id'leri → bağlı problem id'leri (1 sorgu) →
// GetHypotheses("problem", …) (1 sorgu, hypothesesIDCap) → incident başına
// pickIncidentRootCause. Hata soft-fail: danışma amaçlı bir join sayfayı
// asla boşaltmaz (EnrichIncidentsWithClusters duruşu).
func (s *Store) EnrichIncidentsWithRootCause(ctx context.Context, incidents []Incident) []Incident {
	if len(incidents) == 0 {
		return incidents
	}
	incIDs := make([]string, 0, len(incidents))
	for i := range incidents {
		incIDs = append(incIDs, incidents[i].ID)
	}
	byInc, err := s.IncidentProblemIDs(ctx, incIDs)
	if err != nil || len(byInc) == 0 {
		return incidents
	}
	// Liste en-yeni-önce geldiği için problem id sırası da öyle:
	// hypothesesIDCap dolarsa kesilen en ESKİ incident'ın problemleri olur.
	var pids []string
	for i := range incidents {
		pids = append(pids, byInc[incidents[i].ID]...)
	}
	hyps, err := s.GetHypotheses(ctx, "problem", pids)
	if err != nil || len(hyps) == 0 {
		return incidents
	}
	for i := range incidents {
		var members []RootCauseSummary
		for _, pid := range byInc[incidents[i].ID] {
			if h, ok := hyps[pid]; ok {
				if sm := summaryOf(h); sm != nil {
					members = append(members, *sm)
				}
			}
		}
		incidents[i].RootCause = pickIncidentRootCause(members)
	}
	return incidents
}
