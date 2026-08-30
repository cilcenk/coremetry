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

// k8sCoveragePerService — SERVİS BAŞINA örneklem tavanı (v0.10.56).
//
// ⚠ ÖNEK ÖRNEKLEMESİ ÖRNEKLEM DEĞİLDİR. `spans` birincil anahtarı
// (service_name, time); ORDER BY'sız bir LIMIT o anahtarın ÖNEKİNİ döndürür,
// yani ALFABETİK OLARAK İLK servisleri. Bu kartın bütün amacı "filonun
// hangi kısmı k8s bağlamı yayıyor" sorusuna cevap vermek — önek
// örneklemesiyle cevap, alfabetik ilk birkaç servisten üretilmiş bir FİLO
// İDDİASI oluyor.
//
// Lokalde ölçüldü (100 servislik pencere, 5.000 satırlık tavan):
//
//	önek örneklemesi   →   5 / 100 servis   (%5)
//	servis başına      → 100 / 100 servis
//
// `LIMIT n BY service_name` her servise kendi kotasını veriyor: seyrek
// yayan bir servis artık gürültülü bir komşunun arkasında kaybolmuyor.
// Toplam tavan (k8sCoverageSampleRows) DIŞ LIMIT olarak duruyor, yani
// maliyet hâlâ O(örneklem).
//
// 400: 100 servislik bir filoda 40k satır, 5.000 servislikte dış tavan
// ısırır. Alan-var-yok sorusu için 400 satır fazlasıyla yeterli — soru
// "geliyor mu", "ne kadar geliyor" değil.
const k8sCoveragePerService = 400

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
	// Cluster — k8s.cluster.name VEYA openshift.cluster.name (geriye uyum).
	Cluster uint64 `json:"cluster"`
	// v0.10.192 (rollouts audit ön koşul) — rollout dedektörünün girdileri:
	// ReplicaSet adı + imaj; ve cluster'ın HANGİ anahtarla geldiği (tasarım
	// statik k8s.cluster.name ister; OR sayacı ikisini birleştiriyordu).
	ReplicaSet       uint64 `json:"replicaset"`
	Image            uint64 `json:"image"`
	ClusterK8s       uint64 `json:"clusterK8s"`
	ClusterOpenshift uint64 `json:"clusterOpenshift"`
}

// K8sCoverage — servis × k8s alanı kapsama tablosu.
type K8sCoverage struct {
	Rows []K8sCoverageRow `json:"rows"`
	// SampleRows — iç taramanın tavanı. Zarfta çünkü "0 gördüm" ile
	// "örneklem yetmedi" ayrımı operatörün elinde olmalı.
	SampleRows int `json:"sampleRows"`
	// WindowSec — GERÇEKTEN sorulan pencere.
	WindowSec int64 `json:"windowSec"`
	// Capped (v0.10.62) — DIŞ tavan ısırdı mı.
	//
	// ⚠ Kartın kendi "en önemli sözleşmesi" ölçülemiyordu. `fieldState`ın
	// `unknown` dalı ("örneklemde o servisten hiç satır yok → 'alan yok'
	// DEMEK YANLIŞ") YAPISAL OLARAK ULAŞILAMAZDI: GROUP BY sıfır satırlı
	// bir grup için HİÇ SATIR üretmez, yani `sampled` asla 0 olmaz.
	// Örnekleme girmeyen servis "unknown" olarak DEĞİL, HİÇ görünmüyordu.
	//
	// v0.10.56 servis-başına örneklemeyle bunu büyük ölçüde kapattı: artık
	// her servisin kendi kotası var. Kalan tek delik DIŞ tavanın ısırması —
	// o zaman bazı servisler örnekleme hiç girmemiş olabilir ve kart eksik
	// bir filo üzerinden konuşur.
	//
	// Ölçülebilir olduğu için ilan ediliyor: satır sayılarının toplamı
	// tavana dayandıysa Capped=true. "Gördüğüm bu kadar" ile "olan bu
	// kadar" ayrımı yine operatörün elinde.
	Capped bool `json:"capped,omitempty"`
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
	// v0.10.195 — kota ZAMAN DİLİMİNE bölünür (sample_slices.go): salt
	// `LIMIT n BY service_name` birincil anahtar önekini, yani pencerenin
	// ilk saniyelerini örnekliyordu.
	bucketSec, perBucket := sampleSlices(int64(to.Sub(from).Seconds()), k8sCoveragePerService)
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
		               OR has(res_keys, 'openshift.cluster.name')) AS clus,
		       countIf(has(res_keys, 'k8s.replicaset.name'))    AS rs,
		       countIf(has(res_keys, 'container.image.name'))    AS img,
		       countIf(has(res_keys, 'k8s.cluster.name'))        AS clus_k8s,
		       countIf(has(res_keys, 'openshift.cluster.name'))  AS clus_ocp
		FROM (
			SELECT service_name, res_keys FROM spans
			WHERE time >= ? AND time <= ?
			LIMIT %d BY service_name, toStartOfInterval(time, INTERVAL %d SECOND)
			LIMIT %d
		)
		GROUP BY service_name
		ORDER BY sampled DESC
		LIMIT ?
		SETTINGS max_execution_time = 25`, perBucket, bucketSec, k8sCoverageSampleRows)

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
	total := 0
	for rows.Next() {
		var r K8sCoverageRow
		if err := rows.Scan(&r.Service, &r.Sampled, &r.Namespace, &r.Deployment,
			&r.Pod, &r.PodUID, &r.Node, &r.Container, &r.Cluster,
			&r.ReplicaSet, &r.Image, &r.ClusterK8s, &r.ClusterOpenshift); err != nil {
			continue
		}
		out.Rows = append(out.Rows, r)
		total += int(r.Sampled)
	}
	// v0.10.62 — dış tavan ısırdıysa filo EKSİK olabilir; ilan ediliyor.
	out.Capped = total >= k8sCoverageSampleRows
	return out, rows.Err()
}
