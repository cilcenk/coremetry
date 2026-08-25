package chstore

import (
	"context"
	"fmt"
	"time"
)

// k8s_coverage.go — K8s BAĞLAM KAPSAMA KARTI (v0.10.36, K8s entity
// katmanı Faz 0).
//
// ── NEDEN BU İLK DİLİM ──────────────────────────────────────────────────
//
// Entity katmanının asıl adımı (k8sattributes processor + RBAC) prod'da
// collector restart'ı gerektiriyor ve collector pod bounce'ta wedge oluyor
// — bilinen bir risk. O riski ÖLÇÜLMEMİŞ gerekçeyle almak yanlış sıra.
//
// Bugün elimizde yalnız TEK bir prod span'inin resource seti var; filo
// genelinde hangi servisin hangi k8s alanını yaydığı BİLİNMİYOR. Bu okuma
// tam olarak onu ölçüyor ve sonraki fazın KABUL TESTİ oluyor: collector
// değişikliğinden önce ve sonra aynı tablo.
//
// ── NEDEN ÖRNEKLEME ─────────────────────────────────────────────────────
//
// Milyar-span ölçeğinde pencere genelinde `arrayJoin(res_keys)` zaman
// aşımına uğrar ve HATA döner — v0.7.30'da "Add column" seçicisi tam bu
// yüzden boş kalmıştı. Tarama iç sorguda LIMIT'le sınırlanıyor: maliyet
// O(pencere) değil O(örneklem).
//
// ⚠ Bunun bedeli DÜRÜSTÇE ilan edilmeli: sonuç bir ÖRNEKLEM. Çok seyrek
// yayan bir servis örnekleme düşebilir ve "alan yok" gibi görünür. Zarf
// bu yüzden örneklem boyunu ve servis başına görülen satır sayısını
// taşıyor — operatör "0 satır görüldü" ile "alan gerçekten yok"u
// ayırabilsin.

// k8sCoverageSampleRows — servis eksenli taramanın iç LIMIT'i.
// attributeKeysSQL'in attrKeysSampleRows'uyla aynı gerekçe.
const k8sCoverageSampleRows = 200_000

// K8sCoverageRow — bir servisin k8s bağlam kapsaması.
type K8sCoverageRow struct {
	Service string `json:"service"`
	// Sampled — bu servis için örneklemde kaç span görüldü. 0 ise satır
	// zaten dönmez; küçük bir sayı "alan yok" yargısını ZAYIFLATIR.
	Sampled uint64 `json:"sampled"`
	// Alanların her biri: örneklemde KAÇ span'de vardı.
	Namespace  uint64 `json:"namespace"`
	Deployment uint64 `json:"deployment"`
	Pod        uint64 `json:"pod"`
	PodUID     uint64 `json:"podUid"`
	Node       uint64 `json:"node"`
	Container  uint64 `json:"container"`
	Cluster    uint64 `json:"cluster"`
}

// K8sCoverage — servis × k8s alanı kapsama tablosu.
type K8sCoverage struct {
	Rows []K8sCoverageRow `json:"rows"`
	// SampleRows — iç taramanın tavanı. Zarfta çünkü "0 gördüm" ile
	// "örneklem yetmedi" ayrımı operatörün elinde olmalı.
	SampleRows int `json:"sampleRows"`
	// WindowSec — GERÇEKTEN sorulan pencere.
	WindowSec int64 `json:"windowSec"`
}

// GetK8sCoverage — hangi servis hangi k8s resource alanını yayıyor.
//
// Kimlik alanları `res_keys` üzerinden okunuyor: bu depoda k8s bağlamı
// span ATTRIBUTE'unda değil, RESOURCE ekseninde yaşıyor (ölçüldü —
// attr_keys tarafında tek bir k8s.* anahtarı yok).
func (s *Store) GetK8sCoverage(ctx context.Context, from, to time.Time, limit int) (*K8sCoverage, error) {
	if limit <= 0 || limit > 500 {
		limit = 200
	}
	// has() ile sayım: anahtar dizide var mı. indexOf+değer okumaya gerek
	// yok — soru "alan GELİYOR MU", değeri ne değil.
	q := fmt.Sprintf(`
		SELECT service_name,
		       count()                                  AS sampled,
		       countIf(has(res_keys, 'k8s.namespace.name'))  AS ns,
		       countIf(has(res_keys, 'k8s.deployment.name')) AS depl,
		       countIf(has(res_keys, 'k8s.pod.name'))        AS pod,
		       countIf(has(res_keys, 'k8s.pod.uid'))         AS uid,
		       countIf(has(res_keys, 'k8s.node.name'))       AS node,
		       countIf(has(res_keys, 'k8s.container.name'))  AS cont,
		       countIf(has(res_keys, 'k8s.cluster.name')
		               OR has(res_keys, 'openshift.cluster.name')) AS clus
		FROM (
			SELECT service_name, res_keys FROM spans
			WHERE time >= ? AND time <= ?
			LIMIT %d
		)
		GROUP BY service_name
		ORDER BY sampled DESC
		LIMIT ?
		SETTINGS max_execution_time = 25`, k8sCoverageSampleRows)

	rows, err := s.telemetryReadConn().Query(ctx, q, from, to, limit)
	if err != nil {
		return nil, fmt.Errorf("k8s coverage: %w", err)
	}
	defer rows.Close()

	out := &K8sCoverage{
		Rows:       []K8sCoverageRow{},
		SampleRows: k8sCoverageSampleRows,
		WindowSec:  int64(to.Sub(from).Seconds()),
	}
	for rows.Next() {
		var r K8sCoverageRow
		if err := rows.Scan(&r.Service, &r.Sampled, &r.Namespace, &r.Deployment,
			&r.Pod, &r.PodUID, &r.Node, &r.Container, &r.Cluster); err != nil {
			continue
		}
		out.Rows = append(out.Rows, r)
	}
	return out, rows.Err()
}
