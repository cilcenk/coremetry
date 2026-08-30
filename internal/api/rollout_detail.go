package api

// rollout_detail.go — v0.10.203 (ROLLOUTS Faz 4b; audit §4.2, §9).
//
//	GET /api/rollout/detail?cluster=&namespace=&workload=&revision=&startedAt=<ms>
//
// Satır çekmecesi: rollout satırı + revizyonun servisleri (MV'nin
// service_name boyutu) + servis başına health verdict ve önce/sonra RED +
// deploy'dan beri açık problem / aktif anomali / yeni exception —
// deployment_report.go'nun KORUNAN çekirdeği (redComparisonWindow /
// redStatsFor / scoreHealth / filterOpenProblemsSince) tek rollout'a
// daraltılmış hâliyle (audit §4.2 "korunacak parça"). Rapor sayfasının
// "açık problemi olan servis" GİRİŞ KAPISI burada YOK: çekmece rollout'un
// TÜM servislerini gösterir (temiz servis "temiz" diye görünür — boş
// sayfa değil). Rol kapısı yok (rapor da açıktı; viewer okur).
// serveCached 30 s; pencereler now'a çapalı → anahtar 30 s zaman kovası
// taşır (yoksa aynı anahtar sonsuza dek ilk cevabı servis ederdi).

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/cilcenk/coremetry/internal/chstore"
)

// RolloutDetail — çekmece yükü. Since/Generated NANOSANİYE (rollout.startedAt
// ms'tir — RolloutRow.MarshalJSON); FE karşılaştırma penceresi için okur.
type RolloutDetail struct {
	Rollout   chstore.RolloutRow     `json:"rollout"`
	Services  []ServiceReportSection `json:"services"`
	Since     int64                  `json:"since"`       // ns (started_at)
	Generated int64                  `json:"generatedAt"` // ns
	// Note — servis çözümü/karşılaştırma sınırlamaları (registry kaydı yok,
	// pencere kelepçesi, liste kesildi) — çekmece boşken NEDENİ söyler.
	Note string `json:"note,omitempty"`
}

func (s *Server) getRolloutDetail(w http.ResponseWriter, r *http.Request) {
	if !s.rolloutEnabled(w) {
		return
	}
	q := r.URL.Query()
	id := chstore.RolloutID{
		ClusterID: strings.TrimSpace(q.Get("cluster")),
		Namespace: strings.TrimSpace(q.Get("namespace")),
		Workload:  strings.TrimSpace(q.Get("workload")),
		Revision:  strings.TrimSpace(q.Get("revision")),
	}
	ms, _ := strconv.ParseInt(q.Get("startedAt"), 10, 64)
	if id.ClusterID == "" || id.Workload == "" || id.Revision == "" || ms <= 0 {
		writeJSONError(w, http.StatusBadRequest, "cluster, workload, revision ve startedAt (ms) zorunlu")
		return
	}
	id.StartedAt = time.UnixMilli(ms).UTC()
	// Ad-formu ?cluster= da çalışsın ve anahtar tek biçime otursun (getRollout emsali).
	c, haveCluster := s.resolveCluster(id.ClusterID)
	if haveCluster {
		id.ClusterID = c.EffectiveID()
	}
	key := rolloutDetailKey(id, time.Now())
	s.serveCached(w, r, key, 30*time.Second, func(ctx context.Context) (any, error) {
		row, err := s.store.RolloutByID(ctx, id)
		if err != nil {
			return nil, err
		}
		if row == nil {
			return nil, errNotFound
		}
		if !haveCluster {
			// Satır registry kimliği taşır, MV span DEĞERİ taşır: kayıt yoksa
			// (silinmiş/kapalı cluster) çeviri imkânsız — MV'yi suçlamadan söyle.
			return &RolloutDetail{Rollout: *row, Services: []ServiceReportSection{},
				Since: row.StartedAt.UnixNano(), Generated: time.Now().UnixNano(),
				Note: "cluster kaydı registry'de yok ya da kapalı — servisler çözülemez (Settings → Remote Clusters)"}, nil
		}
		// -1 sa: önceki revizyonla örtüşen kovalar da servis boyutunu taşır.
		svcs, capped, err := s.store.RolloutServices(ctx, c.SpanClusterKeys(), id.Namespace, id.Workload, id.Revision, id.StartedAt.Add(-time.Hour))
		if err != nil {
			return nil, err
		}
		det, err := s.buildRolloutDetail(ctx, *row, svcs)
		if err != nil {
			return nil, err
		}
		if capped {
			det.Note = appendDetailNote(det.Note, "servis listesi kesildi (ilk 200)")
		}
		return det, nil
	})
}

func appendDetailNote(cur, add string) string {
	if cur == "" {
		return add
	}
	return cur + "; " + add
}

// buildRolloutDetail — deployment_report çekirdeğinin tek-rollout daraltması.
func (s *Server) buildRolloutDetail(ctx context.Context, row chstore.RolloutRow, svcs []string) (*RolloutDetail, error) {
	sinceNs := row.StartedAt.UnixNano()
	nowNs := time.Now().UnixNano()
	inSet := map[string]bool{}
	for _, v := range svcs {
		inSet[v] = true
	}
	det := &RolloutDetail{Rollout: row, Services: []ServiceReportSection{}, Since: sinceNs, Generated: nowNs}
	if len(svcs) == 0 {
		return det, nil
	}
	snapshot, err := s.store.OpenProblemsSnapshot(ctx)
	if err != nil {
		return nil, err
	}
	// Önce DARALT sonra zenginleştir: enrich CH işi problem sayısıyla orantılı
	// (filonun tamamını zenginleştirip atmak israftı — inceleme).
	all := filterOpenProblemsSince(snapshot.All(), sinceNs)
	mine := make([]chstore.Problem, 0, 8)
	for _, p := range all {
		if inSet[p.Service] {
			mine = append(mine, p)
		}
	}
	probs := s.enrichProblemsForRead(ctx, mine)
	probsBySvc := map[string][]chstore.Problem{}
	for _, p := range probs {
		probsBySvc[p.Service] = append(probsBySvc[p.Service], p)
	}
	// Services SQL'de daraltır (filter-after-LIMIT sınıfı — v0.9.353 dersi).
	allAnomalies, err := s.store.ListAnomalyEvents(ctx, chstore.ListAnomalyEventsFilter{SinceNs: sinceNs, Services: svcs, ActiveOnly: true, Limit: 2000})
	if err != nil {
		return nil, err
	}
	anomBySvc := map[string][]chstore.AnomalyEvent{}
	for _, a := range allAnomalies {
		if a.Status == "active" && a.StartedAt >= sinceNs && inSet[a.Service] {
			anomBySvc[a.Service] = append(anomBySvc[a.Service], a)
		}
	}
	allErrors, err := s.store.ListExceptionGroups(ctx, chstore.ExceptionGroupFilter{State: "open", Services: svcs, Limit: 500})
	if err != nil {
		return nil, err
	}
	errsBySvc := map[string][]chstore.ExceptionGroup{}
	for _, e := range allErrors {
		if e.FirstSeen >= sinceNs && inSet[e.Service] {
			errsBySvc[e.Service] = append(errsBySvc[e.Service], e)
		}
	}
	// Karşılaştırma penceresi kelepçesi: 30 günlük eski satıra tıklamak 60 günlük
	// tDigest taraması olmasın; deploy etkisi ilk saatlerde görünür.
	const maxCmp = 6 * time.Hour
	endNs := nowNs
	if time.Duration(nowNs-sinceNs) > maxCmp {
		endNs = sinceNs + int64(maxCmp)
		det.Note = appendDetailNote(det.Note, "karşılaştırma penceresi: deploy sonrası ilk 6 sa")
	}
	beforeFrom, beforeTo, afterFrom, afterTo := redComparisonWindow(sinceNs, endNs)
	beforeRows, err := s.store.GetServicesAggFilteredIn(ctx, beforeFrom, beforeTo, "", svcs, "", "", 0, 0)
	if err != nil {
		return nil, err
	}
	afterRows, err := s.store.GetServicesAggFilteredIn(ctx, afterFrom, afterTo, "", svcs, "", "", 0, 0)
	if err != nil {
		return nil, err
	}
	beforeBySvc := map[string]chstore.ServiceSummary{}
	for _, sv := range beforeRows {
		beforeBySvc[sv.Name] = sv
	}
	afterBySvc := map[string]chstore.ServiceSummary{}
	for _, sv := range afterRows {
		afterBySvc[sv.Name] = sv
	}
	beforeSec := beforeTo.Sub(beforeFrom).Seconds()
	afterSec := afterTo.Sub(afterFrom).Seconds()
	openCounts, err := s.openProblemCountsCached(ctx)
	if err != nil {
		return nil, err
	}
	for _, svc := range svcs {
		afterSv, hasAfter := afterBySvc[svc]
		health, _ := scoreHealth(&afterSv, openCounts[svc])
		if !hasAfter || afterSv.SpanCount == 0 {
			// Deploy'dan sonra hiç span YOK: en alarme edici sonuç "green"
			// görünmesin — '' → FE 'n/a' + '—' değerler.
			health = ""
		}
		det.Services = append(det.Services, ServiceReportSection{
			Service:   svc,
			Health:    health,
			Before:    redStatsFor(beforeBySvc[svc], beforeSec),
			After:     redStatsFor(afterSv, afterSec),
			Problems:  nonNilSlice(probsBySvc[svc]),
			Anomalies: nonNilSlice(anomBySvc[svc]),
			NewErrors: nonNilSlice(errsBySvc[svc]),
		})
	}
	return det, nil
}
