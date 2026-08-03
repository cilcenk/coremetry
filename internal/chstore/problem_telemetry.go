package chstore

// problem_telemetry.go — v0.9.507.
//
// problem.go'dan AYRILAN telemetri okumaları. Ayrılma sebebi mimari:
// okuma havuzunun (v0.9.496) güvenlik kapısı DOSYA düzeyinde çalışıyor —
// bir dosya RoundRobin havuzunu kullanıyorsa o dosya HİÇBİR state
// tablosu okumamalı, yoksa v0.9.486'nın /users tutarsızlığı geri gelir.
//
// problem.go karışıktı: 16 fonksiyonundan 13'ü state okuyor
// (problems, alert_rules), 3'ü saf telemetri. Karışık dosyayı beyaz
// listeye alamazdım; almasaydım da bu üç okuma node 1'de çakılı
// kalacaktı. Çözüm dosyayı ikiye ayırmak — burası %100 telemetri
// (spans + deploy türevleri), problem.go %100 state.
//
// Buraya bir state tablosu okuması EKLEME. Gerekiyorsa problem.go'ya
// koy; conn_strategy_test.go bu dosyayı tarıyor ve patlar.

import (
	"context"
	"fmt"
	"hash/fnv"
	"sort"
	"time"
)

// deployWindowFloor — deploy arama penceresinin en eski açılabileceği
// nokta (v0.9.552 ikinci kalkanı).
//
// Retention 30 gün, yani 32 günden eskisini sormanın hiçbir karşılığı
// yok: veri zaten yok, tarama bedavaya yanıyor. Bu taban bir ayar
// değil, `FROM spans` sorgusunun zaman sınırlı kalmasını garanti eden
// son savunma — pencereyi hesaplayan kod bir gün yine hata yaparsa
// sorgu tam tablo taramasına DÖNÜŞMESİN.
const deployWindowFloor = 32 * 24 * time.Hour

// spanDeploy is one observed (version, first_seen) pair for a service.
type spanDeploy struct {
	version string
	ns      int64
}

// deploysCacheKey builds the cache key for one deploys fetch: the FNV
// digest of the SORTED service set (cf. the v0.5.187 cache-key rule —
// never length-only) plus the exact query window. Pure — table-tested.
func deploysCacheKey(services map[string]struct{}, from, to time.Time) string {
	names := make([]string, 0, len(services))
	for svc := range services {
		names = append(names, svc)
	}
	sort.Strings(names)
	h := fnv.New64a()
	for _, n := range names {
		h.Write([]byte(n))
		h.Write([]byte{0})
	}
	return fmt.Sprintf("%x:%d:%d", h.Sum64(), from.UnixNano(), to.UnixNano())
}

// fetchDeploysByService runs the bulk deploys GROUP BY for the given
// service set + window, behind a 15s TTL cache (v0.8.359, perf P2-C:
// the problems list / buckets / inbox / sidebar all re-run this every
// 5s poll with an unchanged problem set — the ~80-130ms scan collapsed
// to one query per window per TTL). Keys are exact (service set digest
// + ns-precise window), so a changed problem set is always a miss.
func (s *Store) fetchDeploysByService(ctx context.Context, services map[string]struct{}, from, to time.Time) (map[string][]spanDeploy, error) {
	key := deploysCacheKey(services, from, to)
	now := time.Now()
	s.deploysMu.Lock()
	if e, ok := s.deploysCache[key]; ok && now.Sub(e.at) < deploysCacheTTL {
		s.deploysMu.Unlock()
		return e.byService, nil
	}
	s.deploysMu.Unlock()

	svcList := make([]any, 0, len(services))
	for svc := range services {
		svcList = append(svcList, svc)
	}
	holders := ""
	for i := range svcList {
		if i > 0 {
			holders += ","
		}
		holders += "?"
	}
	// v0.9.66 (operator-reported) — bu okuma effectiveVersionExpr
	// zincirini BYPASS edip yalnız service.version okuyordu; filoda
	// service.version sabit olduğundan "fresh deploy" sinyali (P1
	// triage) hiç ateşlemiyordu. Artık merkez zincir (image-tag önde).
	sql := `
		SELECT service_name,
		       ` + effectiveVersionExpr + ` AS version,
		       toUnixTimestamp64Nano(min(time))                 AS first_seen_ns
		FROM spans
		WHERE service_name IN (` + holders + `)
		  AND time >= ? AND time <= ?
		  AND (has(res_keys, 'service.version')
		    OR has(res_keys, 'container.image.tag')
		    OR has(res_keys, 'k8s.container.image.tag')
		    OR has(res_keys, 'k8s.deployment.labels.app_kubernetes_io_version')
		    OR has(res_keys, 'k8s.pod.labels.app_kubernetes_io_version')
		    OR has(res_keys, 'k8s.deployment.labels.version')
		    OR has(res_keys, 'helm.chart.version'))
		GROUP BY service_name, version
		HAVING version != ''
		ORDER BY service_name, first_seen_ns ASC
		SETTINGS max_execution_time = 10`
	args := append([]any{}, svcList...)
	args = append(args, from, to)
	rows, err := s.telemetryReadConn().Query(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	byService := map[string][]spanDeploy{}
	for rows.Next() {
		var svc, ver string
		var ns int64
		if err := rows.Scan(&svc, &ver, &ns); err != nil {
			return nil, err
		}
		byService[svc] = append(byService[svc], spanDeploy{ver, ns})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	s.storeDeploysCacheEntry(key, deploysCacheEntry{at: now, byService: byService}, now)
	return byService, nil
}

// storeDeploysCacheEntry inserts one fetch result, keeping the cache
// bounded: expired entries are swept on every store, and if a burst of
// distinct problem sets still overflows the cap, the oldest entry is
// dropped. Split out so the bound is table-testable without a CH conn.
func (s *Store) storeDeploysCacheEntry(key string, e deploysCacheEntry, now time.Time) {
	s.deploysMu.Lock()
	defer s.deploysMu.Unlock()
	if s.deploysCache == nil {
		s.deploysCache = map[string]deploysCacheEntry{}
	}
	for k, old := range s.deploysCache {
		if now.Sub(old.at) >= deploysCacheTTL {
			delete(s.deploysCache, k)
		}
	}
	if len(s.deploysCache) >= deploysCacheMax {
		var oldestK string
		var oldestAt time.Time
		for k, old := range s.deploysCache {
			if oldestK == "" || old.at.Before(oldestAt) {
				oldestK, oldestAt = k, old.at
			}
		}
		delete(s.deploysCache, oldestK)
	}
	s.deploysCache[key] = e
}

// EnrichProblemsWithDeploys attaches the most recent
// observed service.version deploy that happened up to
// `lookback` before each problem's started_at. Single bulk
// CH query covers every service across every problem in the
// slice — N+1 free regardless of problem count. Soft-fails:
// CH error returns the slice unchanged rather than blocking
// the page render.
//
// Mechanism: one GROUP BY over spans for the union of
// involved services in [min(started)-lookback, max(started)]
// (cached ~15s, v0.8.359), then per-problem in-memory match
// against the highest first_seen time ≤ that problem's
// started_at.
func (s *Store) EnrichProblemsWithDeploys(ctx context.Context, problems []Problem, lookback time.Duration) []Problem {
	if len(problems) == 0 {
		return problems
	}
	// Distinct services + global time window across the page.
	services, from, to, ok := deployEnrichWindow(problems, lookback, time.Now())
	if !ok {
		return problems
	}

	byService, err := s.fetchDeploysByService(ctx, services, from, to)
	if err != nil {
		return problems
	}
	lookbackNs := int64(lookback)
	for i := range problems {
		list := byService[problems[i].Service]
		if len(list) == 0 {
			continue
		}
		// Find latest deploy with ns ≤ problem.StartedAt and
		// ns ≥ problem.StartedAt-lookback. List is asc, so
		// walk from the end.
		var pick *spanDeploy
		for j := len(list) - 1; j >= 0; j-- {
			if list[j].ns > problems[i].StartedAt {
				continue
			}
			if list[j].ns < problems[i].StartedAt-lookbackNs {
				break
			}
			pick = &list[j]
			break
		}
		if pick != nil {
			problems[i].RecentDeploy = &RecentDeploy{
				Version:    pick.version,
				TimeUnixNs: pick.ns,
				AgeSeconds: (problems[i].StartedAt - pick.ns) / 1e9,
			}
		}
	}
	return problems
}

// EnrichAnomaliesWithDeploys is the AnomalyEvent twin of
// EnrichProblemsWithDeploys. v0.5.286 — same one-shot
// bulk-query pattern (one round-trip regardless of how many
// services / events are in the slice). Each event's
// RecentDeploy points at the most recent deploy of that
// service whose first_seen falls in
// [event.startedAt-lookback, event.startedAt]. Uses the
// effectiveVersionExpr chain (v0.5.283) so Helm-only
// installs (app.kubernetes.io/version label) and image-tag
// fallbacks correlate too, not just bare service.version.
func (s *Store) EnrichAnomaliesWithDeploys(ctx context.Context, events []AnomalyEvent, lookback time.Duration) []AnomalyEvent {
	if len(events) == 0 {
		return events
	}
	// v0.9.552 — İKİZDE DE AYNI BUG VARDI. Problem tarafındaki hatayı
	// düzeltirken bu fonksiyon "AnomalyEvent twin" olarak anılıyordu;
	// açıp bakınca pencere hesabı satır satır aynıydı — aynı `i == 0`
	// tuzağı, aynı sonuç (from = 1970 ⇒ spans tam tablo taraması).
	// Tek kaynağa indirildi: deployEnrichWindow artık ikisini de
	// besliyor, böylece bir daha ayrı ayrı bozulamazlar.
	services, from, to, ok := deployEnrichWindow(anomalyDeployRows(events), lookback, time.Now())
	if !ok {
		return events
	}
	svcList := make([]any, 0, len(services))
	for s := range services {
		svcList = append(svcList, s)
	}

	holders := ""
	for i := range svcList {
		if i > 0 {
			holders += ","
		}
		holders += "?"
	}
	// v0.5.286 — uses effectiveVersionExpr (the same Helm /
	// image-tag / placeholder-filtered chain GetRecentDeploys
	// uses) so the correlation finds deploys even when
	// service.version stays at "0.0.1-SNAPSHOT" or the
	// pipeline only ships labels via Helm.
	sql := `
		SELECT service_name,
		       ` + effectiveVersionExpr + ` AS version,
		       toUnixTimestamp64Nano(min(time))                 AS first_seen_ns
		FROM spans
		WHERE service_name IN (` + holders + `)
		  AND time >= ? AND time <= ?
		  AND (has(res_keys, 'service.version')
		    OR has(res_keys, 'container.image.tag')
		    OR has(res_keys, 'k8s.container.image.tag')
		    OR has(res_keys, 'k8s.deployment.labels.app_kubernetes_io_version')
		    OR has(res_keys, 'k8s.pod.labels.app_kubernetes_io_version')
		    OR has(res_keys, 'k8s.deployment.labels.version')
		    OR has(res_keys, 'helm.chart.version'))
		GROUP BY service_name, version
		HAVING version != ''
		ORDER BY service_name, first_seen_ns ASC
		SETTINGS max_execution_time = 10`
	args := append([]any{}, svcList...)
	args = append(args, from, to)
	rows, err := s.telemetryReadConn().Query(ctx, sql, args...)
	if err != nil {
		return events
	}
	defer rows.Close()
	type d struct {
		version string
		ns      int64
	}
	byService := map[string][]d{}
	for rows.Next() {
		var svc, ver string
		var ns int64
		if err := rows.Scan(&svc, &ver, &ns); err != nil {
			return events
		}
		byService[svc] = append(byService[svc], d{ver, ns})
	}
	if err := rows.Err(); err != nil {
		return events
	}
	lookbackNs := int64(lookback)
	for i := range events {
		list := byService[events[i].Service]
		if len(list) == 0 {
			continue
		}
		var pick *d
		for j := len(list) - 1; j >= 0; j-- {
			if list[j].ns > events[i].StartedAt {
				continue
			}
			if list[j].ns < events[i].StartedAt-lookbackNs {
				break
			}
			pick = &list[j]
			break
		}
		if pick != nil {
			events[i].RecentDeploy = &RecentDeploy{
				Version:    pick.version,
				TimeUnixNs: pick.ns,
				AgeSeconds: (events[i].StartedAt - pick.ns) / 1e9,
			}
		}
	}
	return events
}
// CalleesOf returns services that `service` calls (outgoing dependency view).
func (s *Store) CalleesOf(ctx context.Context, service string, since time.Duration) ([]ServiceEdgeStats, error) {
	cutoff := time.Now().Add(-since)
	rows, err := s.telemetryReadConn().Query(ctx, `
		SELECT peer_service AS callee,
		       count() AS calls,
		       countIf(status_code = 'error') / count() * 100 AS error_rate,
		       avg(duration) / 1e6 AS avg_ms,
		       quantile(0.99)(duration) / 1e6 AS p99_ms
		FROM spans
		WHERE time >= ?
		  AND service_name = ?
		  AND peer_service != ''
		  AND kind IN ('client', 'producer')
		GROUP BY callee
		ORDER BY calls DESC
		-- v0.9.231 (scale-audit) — had neither, so it inherited the 60s
		-- connection default while the only caller keeps just the top 5
		-- (api.go, root-cause callees). Matches the sibling query in that
		-- same handler, which already carries LIMIT + a 5s ceiling.
		LIMIT 20
		SETTINGS max_execution_time = 5`, cutoff, service)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ServiceEdgeStats
	for rows.Next() {
		var e ServiceEdgeStats
		if err := rows.Scan(&e.Service, &e.Calls, &e.ErrorRate, &e.AvgMs, &e.P99Ms); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// deployEnrichWindow — deploy zenginleştirmesinin servis kümesini ve
// zaman penceresini hesaplar. SAF: test edilebilmesi için Store'dan
// ayrı duruyor (v0.9.552 — buradaki hata sessizce tam tablo taraması
// üretmişti, bir daha testsiz kalmasın).
//
// ok=false ⇒ servisli problem yok, zenginleştirme atlanır.
func deployEnrichWindow(problems []Problem, lookback time.Duration, now time.Time) (services map[string]struct{}, from, to time.Time, ok bool) {
	services = map[string]struct{}{}
	// Pencere SAYAÇLA kurulur, indeksle DEĞİL.
	//
	// Öncesi: `if i == 0 || p.StartedAt < minStarted`. i==0 dalı, ilk
	// problemin Service'i boşsa yukarıdaki `continue` yüzünden HİÇ
	// çalışmıyordu; sonraki turlarda i!=0 olduğu için karşılaştırma
	// minStarted=0 ile yapılıyor ve pozitif bir unix-ns hiçbir zaman
	// 0'dan küçük olmadığı için minStarted 0'da KALIYORDU. Sonuç:
	// from = 1970 ⇒ `FROM spans` sorgusunun tek zaman sınırı yok olur
	// ve TÜM tablo taranır.
	//
	// Tetikleyici kenar durum değil: ES watcher kuralları servissiz
	// problem üretir (evaluator/watcher_eval.go) ve ListProblems
	// started_at DESC sıralar — en yeni açık problem bir watcher
	// alarmıysa problems[0].Service boştur.
	//
	// Belirti sessizdi ve YANLIŞ yöne bakıyordu: sorgu 10sn timeout'una
	// takılıyor, çağıran zenginleştirmeyi atlıyor, RecentDeploy nil
	// kalıyor ve computePriority'nin postDeploy dalı hiç ateşlemiyor —
	// taze deploy sonrası kritik problemler P1 yerine P2 etiketleniyordu.
	var minStarted, maxStarted int64
	seen := 0
	for _, p := range problems {
		if p.Service == "" {
			continue
		}
		services[p.Service] = struct{}{}
		if seen == 0 || p.StartedAt < minStarted {
			minStarted = p.StartedAt
		}
		if seen == 0 || p.StartedAt > maxStarted {
			maxStarted = p.StartedAt
		}
		seen++
	}
	if len(services) == 0 {
		return nil, time.Time{}, time.Time{}, false
	}
	from = time.Unix(0, minStarted).Add(-lookback)
	to = time.Unix(0, maxStarted)
	// İkinci kalkan: StartedAt bozuk/sıfır gelirse pencere yine
	// sınırsız açılmasın. `FROM spans` sorgularının zaman sınırlı
	// olması CLAUDE.md'de sert kısıt; tek bir hesap hatasının onu
	// delebilmesi kabul edilemez.
	if floor := now.Add(-deployWindowFloor); from.Before(floor) {
		from = floor
	}
	return services, from, to, true
}

// anomalyDeployRows — AnomalyEvent'leri deployEnrichWindow'un anladığı
// asgari şekle indirir (v0.9.552).
//
// Neden dönüştürme: pencere hesabı yalnız iki alana bakıyor (Service,
// StartedAt) ve iki çağıranın tipleri farklı. Alternatif bir arayüz
// tanımlamaktı; bu, iki alan için fazla tören. Asıl kazanç pencere
// mantığının TEK yerde kalması — ikisi ayrı ayrı yazıldığı için aynı
// hata iki yere birden kopyalanmıştı.
func anomalyDeployRows(events []AnomalyEvent) []Problem {
	out := make([]Problem, len(events))
	for i, e := range events {
		out[i] = Problem{Service: e.Service, StartedAt: e.StartedAt}
	}
	return out
}

// EnrichProblemsForRead — bir problem listesini OKUMA için hazırlar:
// önce deploy, sonra öncelik (v0.9.554).
//
// Sıra ZORUNLU. computePriority'nin kritik kolu RecentDeploy'a bakar:
//
//	postDeploy := p.RecentDeploy != nil && AgeSeconds <= 5*60
//	case postDeploy: return "P1", "critical + deploy Ns before"
//	default:         return "P2", "critical"
//
// Deploy adımı koşmazsa RecentDeploy nil kalır ve AYNI SATIR P2 olur.
// v0.9.553'te sohbet yüzeyleri tam bu yüzden Problems sayfasıyla
// çelişiyordu.
//
// Zincir chstore'a TAŞINDI çünkü ikinci bir tüketici çıktı: MCP
// list_problems aracı (internal/mcptools) — o da api.Server'a
// erişemiyor. İki paketin ayrı ayrı "deploy sonra öncelik" yazması,
// düzeltilen ayrışmanın yeni bir kopyası olurdu.
func (s *Store) EnrichProblemsForRead(ctx context.Context, probs []Problem, lookback time.Duration) []Problem {
	if len(probs) == 0 {
		return probs
	}
	probs = s.EnrichProblemsWithDeploys(ctx, probs, lookback)
	return EnrichProblemsWithPriority(probs)
}

// FilterProblemsByPriority — P1/P2/P3 daraltması (v0.9.554).
//
// ProblemFilter.Priority SQL'de UYGULANMAZ: öncelik okuma anında
// hesaplanır, CH satırında yoktur (problem.go:594-605). Filtre alanını
// set edip bu daraltmayı ÇAĞIRMAMAK, argümanın sessizce yok sayılması
// demek — MCP list_problems aracının v0.9.554 öncesi hatası buydu.
//
// Boş bucket "P3" sayılır: frontend'in bucket'sız satırlar için
// kullandığı geri düşüş değeriyle aynı, böylece chip davranışı okuma
// ile render arasında tutarlı kalır.
func FilterProblemsByPriority(probs []Problem, want []string) []Problem {
	if len(want) == 0 {
		return probs
	}
	m := make(map[string]bool, len(want))
	for _, w := range want {
		m[w] = true
	}
	keep := make([]Problem, 0, len(probs))
	for _, p := range probs {
		bucket := p.Priority
		if bucket == "" {
			bucket = "P3"
		}
		if m[bucket] {
			keep = append(keep, p)
		}
	}
	return keep
}

// SortProblemsByPriority — P1 → P2 → P3, eşitlikte en yeni önce
// (v0.9.554). Yerinde sıralar ve aynı dilimi döner.
//
// MCP "Open problems" kaynağının açıklaması "Sorted by priority then
// recency" DİYORDU ama öncelik hiç hesaplanmadığı için ona göre
// sıralanması imkânsızdı — liste yalnız started_at DESC geliyordu.
// Açıklamanın doğru olabilmesi için hem zenginleştirme hem bu sıralama
// gerekiyor.
//
// Bilinmeyen/boş bucket P3 sayılır: FilterProblemsByPriority ile aynı
// geri düşüş, iki yerin ayrışmaması için.
func SortProblemsByPriority(probs []Problem) []Problem {
	rank := func(p Problem) int {
		switch p.Priority {
		case "P1":
			return 1
		case "P2":
			return 2
		default:
			return 3
		}
	}
	sort.SliceStable(probs, func(i, j int) bool {
		ri, rj := rank(probs[i]), rank(probs[j])
		if ri != rj {
			return ri < rj
		}
		return probs[i].StartedAt > probs[j].StartedAt
	})
	return probs
}

// ProblemScanCeiling / ProblemScanLimit — öncelik daraltmasında CH
// taramasının genişletilmesi (v0.9.576).
//
// ProblemFilter.Priority SQL'de UYGULANMAZ: öncelik okuma anında
// hesaplanır, CH satırında yoktur. Daraltma Go'da, LIMIT'ten SONRA
// olur — yani sayfa boyutu kadar satır taranıp içinden P1'ler
// süzülürse, filoda yüzlerce P1 varken SIFIR sonuç dönebilir.
//
// Bu, make audit CHECK 8'in kovaladığı "LIMIT'ten sonra filtrele"
// sınıfı. Sayfa yolu (internal/api) bunu zaten doğru yapıyordu; MCP
// list_problems aracı v0.9.554'te aynı tuzağa düştü.
//
// Kural chstore'a TAŞINDI çünkü iki tüketici var ve mcptools
// internal/api'yi import edemez (döngü). İkinci bir kopya yazmak,
// bu oturumda altı kez çıkan ayrışma sınıfının yenisi olurdu.
const ProblemScanCeiling = 2000

// ProblemScanLimit — daraltma varsa taramayı 5× açar, tavanla kırpar.
// Saf, tablo-testli.
func ProblemScanLimit(pageLimit int, narrowed bool) int {
	if pageLimit <= 0 {
		pageLimit = 100
	}
	if !narrowed {
		return pageLimit
	}
	n := pageLimit * 5
	if n > ProblemScanCeiling {
		n = ProblemScanCeiling
	}
	return n
}
