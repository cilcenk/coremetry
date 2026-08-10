// Davranış motoru AŞAMA 1 — TARAMA TİKİ (v0.9.936). Saf çekirdek
// behavior.go'da; burası onu canlı CH'ye ve anomaly_events'e bağlayan
// ince katman.
//
// NEREDE KOŞUYOR: mevcut metrik dedektörünün (anomaly.Detector) tikinde,
// AYNI leader-lock altında, AYNI `now` ile. Yeni worker, yeni lock, yeni
// zamanlayıcı YOK. Gerekçe üç katlı:
//
//   - Lider kilidi zaten orada; ikinci bir kilit ikinci bir devir/HA
//     hikâyesi demek olurdu.
//   - Tek `now` tik boyunca sabit kalıyor (v0.8.507'nin dersi): aksi
//     halde taramanın ilk ve son servisi farklı kovalara düşebilir.
//   - Dedektörün varsayılan tiki 2 dakika; bu motorun penceresi 5-dk
//     bucket'ları, yani daha sık koşmanın hiçbir karşılığı yok.
package anomaly

import (
	"context"
	"fmt"
	"log"
	"sync/atomic"
	"time"

	"github.com/cilcenk/coremetry/internal/chstore"
)

// behaviorObs — motorun kendi ölçümü (self-observability). Süreç-içi
// atomik sayaçlar; /admin/stats bunları okur.
//
// SÜREÇ-İÇİ olması ingest sayaçlarıyla aynı duruş: COREMETRY_MODE=api
// bir pod'da bu motor koşmadığı için sayaçlar sıfırdır ve bu DOĞRU
// cevaptır — o pod gerçekten tarama yapmıyor. Sayaçları CH'ye yazmak
// bir yazma yolu daha açardı.
var behaviorObs struct {
	ticks        atomic.Int64
	candidates   atomic.Int64
	lastUnix     atomic.Int64
	lastMs       atomic.Int64
	lastCands    atomic.Int64
	lastServices atomic.Int64
	lastErr      atomic.Value // string
}

// BehaviorStats — /admin/stats'ın gördüğü anlık görüntü.
type BehaviorStats struct {
	// Ticks — süreç başından beri koşan davranış taraması sayısı.
	Ticks int64 `json:"ticks"`
	// Candidates — süreç başından beri YAZILAN aday sayısı (tavandan
	// SONRA; kesilenler burada sayılmaz).
	Candidates int64 `json:"candidates"`
	// LastUnix — son taramanın bitiş anı (unix sn). 0 = hiç koşmadı
	// (bu pod lider değil, ya da motor kapalı).
	LastUnix int64 `json:"lastUnix"`
	// LastDurationMs — son taramanın TOPLAM süresi (sorgu + karar +
	// yazım). Bütçe göstergesi: 10 sn'yi aşarsa vidalar sıkılmalı.
	LastDurationMs int64 `json:"lastDurationMs"`
	// LastCandidates / LastServices — son tikin çıktısı ve kapsamı.
	LastCandidates int64 `json:"lastCandidates"`
	LastServices   int64 `json:"lastServices"`
	// LastError — son taramanın hatası ("" = temiz). Sessiz kapanma bu
	// depoda tekrarlayan hata sınıfı: motor bir CH hatasıyla hiç aday
	// üretmiyor olabilir ve hiçbir ekran bunu söylemezdi.
	LastError string `json:"lastError,omitempty"`
}

// BehaviorObservability — sayaçların anlık görüntüsü. API sunucusu
// çağırır (aynı süreçte koşuyorsa dolu, koşmuyorsa sıfır).
func BehaviorObservability() BehaviorStats {
	s := BehaviorStats{
		Ticks:          behaviorObs.ticks.Load(),
		Candidates:     behaviorObs.candidates.Load(),
		LastUnix:       behaviorObs.lastUnix.Load(),
		LastDurationMs: behaviorObs.lastMs.Load(),
		LastCandidates: behaviorObs.lastCands.Load(),
		LastServices:   behaviorObs.lastServices.Load(),
	}
	if v, ok := behaviorObs.lastErr.Load().(string); ok {
		s.LastError = v
	}
	return s
}

// scanBehavior — bir davranış taraması. Dedektörün scan()'i çağırır;
// `now` ve `cfg` tik boyunca sabit olanların ta kendisi.
//
// HATA DURUŞU: her dal SOFT-FAIL. Sorgu hata verirse tik biter, sayaç
// hatayı taşır, ani-sapma dedektörü etkilenmez. Bu motor bir EK sinyal;
// çökmesi mevcut anomali hattını sessizleştirmemeli.
func (d *Detector) scanBehavior(ctx context.Context, now time.Time, cfg chstore.AnomalySensitivityConfig) {
	b := cfg.Behavior
	if !b.IsEnabled() {
		return
	}
	start := time.Now()
	rowsByService, err := d.fetchBehaviorBuckets(ctx, now, b)
	if err != nil {
		behaviorObs.lastErr.Store(err.Error())
		log.Printf("[behavior] bucket okuması: %v — bu tik aday YOK", err)
		return
	}

	// Deploy korelasyonu — TEK okuma, tüm filo için. Best-effort: hata
	// hâlinde adaylar deploy ilişkisi OLMADAN yazılır (deploy'suz kalıcı
	// kaymalar zaten motorun kapsamında, yani boş harita meşru bir girdi).
	deploysByService := map[string][]chstore.RecentDeployEntry{}
	if deps, derr := d.store.GetRecentDeploys(ctx, behaviorDeployWindow, behaviorDeployLimit); derr == nil {
		for _, dep := range deps {
			deploysByService[dep.Service] = append(deploysByService[dep.Service], dep)
		}
	} else {
		log.Printf("[behavior] deploy okuması: %v — adaylar deploy ilişkisi olmadan yazılır", derr)
	}

	recentCutoff := lastCompleteBucketStart(now).Add(-behaviorRecentHours * time.Hour).Unix()
	var cands []behaviorCandidate
	for service, rows := range rowsByService {
		for _, metric := range behaviorMetrics {
			pol := policyFor(metric, cfg)
			baseline, recent := splitBehaviorSeries(rows, metric, recentCutoff)
			c, ok := evalBehavior(service, metric, baseline, recent, pol, b)
			if !ok {
				continue
			}
			c.Deploy = pickBehaviorDeploy(deploysByService[service], c.OnsetUnix)
			cands = append(cands, c)
		}
	}
	cands = capBehaviorCandidates(cands, b.MaxCandidatesPerTick)

	written := 0
	for _, c := range cands {
		if err := d.store.UpsertAnomalyEvent(ctx, behaviorEvent(c, now)); err != nil {
			log.Printf("[behavior] upsert %s/%s: %v", c.Service, c.Metric, err)
			continue
		}
		written++
		log.Printf("[behavior] %s · %s %s %s — %.2f%s → %.2f%s (%.2f×, %.1fσ, %d dilim)%s",
			c.Service, displayMetric(c.Metric), behaviorSignalTR(c.Signal), behaviorDirectionTR(c.Direction),
			c.Baseline, unitOf(c.Metric), c.Current, unitOf(c.Metric),
			c.Ratio, c.Z, c.Dwell, behaviorDeploySuffix(c))
	}

	behaviorObs.ticks.Add(1)
	behaviorObs.candidates.Add(int64(written))
	behaviorObs.lastUnix.Store(time.Now().Unix())
	behaviorObs.lastMs.Store(time.Since(start).Milliseconds())
	behaviorObs.lastCands.Store(int64(written))
	behaviorObs.lastServices.Store(int64(len(rowsByService)))
	behaviorObs.lastErr.Store("")
}

const (
	// behaviorDeployWindow / behaviorDeployLimit — korelasyon için
	// çekilen deploy penceresi. Adayın onset'i en fazla
	// behaviorRecentHours kadar geride olabilir, artı
	// behaviorDeployLookback; 6 saat rahat pay bırakıyor.
	behaviorDeployWindow = 6 * time.Hour
	behaviorDeployLimit  = 2000
)

// behaviorEvent — adayın anomaly_events satırı.
//
// PeakRatio'yu SET ETMİYORUZ: UpsertAnomalyEvent zaten
// max(prev, CurrentRatio) uyguluyor. DÜŞÜŞ adaylarında (ratio < 1) bu
// "peak" ilk görülen orana takılır ve terfi kapısının oran eşiğini
// geçmez — bilinçli: bir düşüşü "5× peak" diye raporlamak yalan olurdu.
// Düşüşler /anomalies akışında görünür, otomatik Problem açmaz.
func behaviorEvent(c behaviorCandidate, now time.Time) chstore.AnomalyEvent {
	return chstore.AnomalyEvent{
		ID:      behaviorEventID(c.Service, c.Metric),
		Kind:    behaviorKind,
		Pattern: behaviorPattern(c.Metric),
		Service: c.Service,
		// StartedAt = kaymanın BAŞLANGICI, tespit anı değil. Upsert
		// mevcut satırın started_at'ını korur, yani süregelen bir
		// kayma tek bir sürekli olay olarak kalır.
		StartedAt:    c.OnsetUnix * int64(time.Second),
		LastSeen:     now.UnixNano(),
		CurrentRatio: c.Ratio,
		CurrentCount: c.Spans,
		Sample:       encodeBehaviorDetails(c),
	}
}

// fetchBehaviorBuckets — TEK toplu sorgu, servis başına satır dizisi.
// Satırlar zamana göre ARTAN gelir (SQL ORDER BY service_name, t);
// splitBehaviorSeries ve dwell penceresi buna dayanıyor.
func (d *Detector) fetchBehaviorBuckets(ctx context.Context, now time.Time, b chstore.AnomalyBehaviorConfig) (map[string][]behaviorRow, error) {
	upper := lastCompleteBucketStart(now)
	cutoff := upper.Add(-behaviorBaselineDays * 24 * time.Hour)
	recentFrom := upper.Add(-behaviorRecentHours * time.Hour)
	target := hourOfWeek(now)

	rows, err := d.store.TelemetryReadConn().Query(ctx, buildBehaviorBucketsQuery(),
		cutoff, upper, target, target, behaviorNeighborHours, recentFrom)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make(map[string][]behaviorRow)
	for rows.Next() {
		var svc string
		var t uint32
		var how int32
		var spans, errs uint64
		var p99 float64
		if err := rows.Scan(&svc, &t, &how, &spans, &errs, &p99); err != nil {
			return nil, err
		}
		out[svc] = append(out[svc], behaviorRow{
			Unix: int64(t), HOW: int(how),
			Spans: spans, Errs: errs, P99Ms: p99,
		})
	}
	return out, rows.Err()
}

// ── Log yardımcıları (Türkçe, operatörün okuduğu dil) ────────────────

func behaviorSignalTR(signal string) string {
	if signal == "regime" {
		return "rejim kayması"
	}
	return "mevsimsel sapma"
}

func behaviorDirectionTR(dir string) string {
	if dir == "down" {
		return "↓"
	}
	return "↑"
}

// behaviorDeploySuffix — log satırının deploy eki. Korelasyon yoksa boş
// (yokluk da bilgidir: "deploy'suz kalıcı kayma").
func behaviorDeploySuffix(c behaviorCandidate) string {
	if c.Deploy == nil {
		return ""
	}
	age := (c.OnsetUnix*int64(time.Second) - c.Deploy.FirstSeenNs) / int64(time.Minute)
	return fmt.Sprintf(" · deploy %s, %d dk önce", c.Deploy.Version, age)
}

// behaviorLogLine — hassasiyet log satırının davranış eki. Ani-sapma
// özetiyle AYNI sözleşme: DEĞİŞİNCE bir satır, her tikte değil.
func behaviorLogLine(b chstore.AnomalyBehaviorConfig) string {
	if !b.IsEnabled() {
		return " | davranış: KAPALI"
	}
	return fmt.Sprintf(" | davranış: seasonalZ=%.1f regimeRatio=%.2f dwell=%d/%d tavan=%d",
		b.SeasonalZ, b.RegimeRatio, b.DwellSeasonal, b.DwellRegime, b.MaxCandidatesPerTick)
}
