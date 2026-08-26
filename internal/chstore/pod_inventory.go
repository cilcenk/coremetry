package chstore

import (
	"context"
	"fmt"
	"time"
)

// pod_inventory.go — POD ENVANTERİ, okuma tarafı (v0.10.40, K8s entity
// katmanı Faz 1'in ingest'e dokunmayan yarısı).
//
// ── KİMLİK KARARI ───────────────────────────────────────────────────────
//
// `(k8s.namespace.name, k8s.pod.name)` — operatör kararı 2026-08-26.
// `k8s.pod.uid` prod'da gelmiyor ve beklenmiyor; ikisi de geliyor.
//
// ⚠ BU KİMLİĞİN TEK ZAYIF NOKTASI STATEFULSET. Deployment pod'ları
// rastgele sonek taşıyor (`svc-59df758cc-scdwq`), yani her yeniden
// yaratma YENİ bir ad → ömürler doğal olarak ayrışıyor. StatefulSet
// pod adı ise SABİT (`svc-0` hep `svc-0`): restart'tan sonra aynı ad
// döner ve iki ayrı pod ömrü TEK satırda birleşir.
//
// Sonuç: `firstSeen`/`lastSeen` iki hayatı kapsar ve "bu pod 40 gündür
// ayakta" gibi YANLIŞ bir cümle üretilir. Bunu çözen tek şey uid'di.
// Karar pod adı yönünde verildiği için sınır SESSİZ BIRAKILMIYOR:
// `NameStable` bayrağı ile taşınıyor ve arayüz ilan ediyor.
//
// ── NAMESPACE KANONİK ZİNCİRDEN ─────────────────────────────────────────
//
// Namespace elle çıkarılmıyor: `identity.go`nun `namespaceExpr()`i
// kullanılıyor. Elle yazmak ÜÇÜNCÜ bir sözlük demekti ve v0.9.1318'in
// kapattığı iki-sözlük arızasını geri getirirdi — depoda bunun kapısı
// var (identity_test.go) ve bu dosyayı ilk yazımda YAKALADI.
//
// Zincirin ilk basamağı `service.namespace`: prod'da o alan geliyor,
// yani kanonik ifade orada da çözüm üretiyor.
//
// Pod ve node için böyle bir zincir YOK (tek anahtar), o yüzden doğrudan
// okunuyorlar.
//
// ── NEDEN MV DEĞİL, OKUMA ───────────────────────────────────────────────
//
// Faz 1 aslında bir `pod_seen` MV'si öngörüyordu (service_seen emsali).
// Ama `service_seen` UCUZ bir promoted kolona (service_name) gruplanıyor;
// burada namespace ve pod promoted DEĞİL, `res_values[indexOf(res_keys,…)]`
// ile çıkarılıyor. Bir MV bunu HER eklenen span satırında yapardı —
// süzgeçsiz, tüm span'lerde. (Kıyas: db_summary_5m de dizi çıkarımı
// yapıyor ama yalnız `db_system != ''` alt kümesinde.)
//
// Milyar-span/gün ölçeğinde bu ingest yoluna ölçülmemiş bir maliyet
// eklemek demek. Okuma tarafı aynı değeri ÖRNEKLEMELİ ve SINIRLI olarak
// veriyor, ingest'e hiç dokunmadan. MV, maliyet ölçüldükten ve promoted
// kolon kararı verildikten sonra gelmeli.

// podInventorySampleRows — iç taramanın tavanı. Kapsama kartıyla aynı
// gerekçe: maliyet O(pencere) değil O(örneklem).
const podInventorySampleRows = 300_000

// podInventoryPerService — SERVİS BAŞINA örneklem tavanı (v0.10.56).
//
// Kapsama kartıyla AYNI kusur ve aynı çare: ORDER BY'sız bir LIMIT,
// (service_name, time) anahtarının ÖNEKİNİ döndürür — alfabetik ilk
// servisler. Envanterde bu, "z" ile başlayan servislerin pod'larının hiç
// görünmemesi demek.
//
// ⚠ İKİNCİ KUSUR DAHA AĞIRDI: `pod != ”` süzgeci DIŞ sorgudaydı, yani
// tavan pod adı TAŞIMAYAN satırlara da harcanıyordu. Filonun bir kısmı
// k8s bağlamı yaymıyorsa (ki bu kartın ölçtüğü şeyin ta kendisi) tavan
// onlarla dolup envanter BOŞ dönebiliyordu. Süzgeç artık LIMIT'ten ÖNCE.
//
// Bu, deponun "LIMIT'ten sonra süzme" ailesinin (v0.9.322→343) SQL
// biçimi. audit.sh CHECK 8 yalnız Go biçimini tarıyor — kapı kaçırmadı,
// kapsamı dışındaydı.
const podInventoryPerService = 500

// PodRow — bir pod'un envanter satırı.
type PodRow struct {
	Namespace string `json:"namespace"`
	Pod       string `json:"pod"`
	// Service — pod'u yayan servis(ler)den biri. Pod↔servis köprüsü
	// bugün yalnız burada kuruluyor.
	Service string `json:"service"`
	Node    string `json:"node,omitempty"`
	Spans   uint64 `json:"spans"`
	// FirstSeen / LastSeen — ÖRNEKLEMDEKİ ilk/son span. Gerçek pod
	// ömrü değil: örneklem penceresi içindeki görülme aralığı.
	FirstSeen int64 `json:"firstSeen"`
	LastSeen  int64 `json:"lastSeen"`
	// NameStable — pod adı SABİT görünüyor mu (StatefulSet deseni).
	// true ise firstSeen/lastSeen İKİ ayrı pod ömrünü kapsıyor
	// olabilir; arayüz bunu ilan etmek zorunda.
	NameStable bool `json:"nameStable"`
}

// PodInventory — pod envanteri zarfı.
type PodInventory struct {
	Rows       []PodRow `json:"rows"`
	SampleRows int      `json:"sampleRows"`
	WindowSec  int64    `json:"windowSec"`
}

// looksStatefulSetName — pod adı SABİT desende mi.
//
// Deployment: `<ad>-<replicaset-hash>-<5 rastgele>` → son parça 5 karakter
// ve alfanümerik karışık.
// StatefulSet: `<ad>-<ordinal>` → son parça saf RAKAM.
//
// Sezgi, kesinlik değil — ve öyle olduğu ADIYLA söyleniyor. Yanlış
// pozitif zararsız (fazladan uyarı); yanlış negatif ise sessizce
// birleşmiş bir ömür demek, o yüzden eşik GEVŞEK tutuluyor.
func looksStatefulSetName(pod string) bool {
	i := len(pod) - 1
	digits := 0
	for i >= 0 && pod[i] >= '0' && pod[i] <= '9' {
		digits++
		i--
	}
	// En az bir rakamla bitmeli ve öncesinde '-' olmalı: `svc-0`, `svc-12`.
	return digits > 0 && i >= 0 && pod[i] == '-'
}

// GetPodInventory — örneklem penceresindeki pod'lar.
func (s *Store) GetPodInventory(ctx context.Context, from, to time.Time, limit int) (*PodInventory, error) {
	if limit <= 0 || limit > 1000 {
		limit = 300
	}
	q := fmt.Sprintf(`
		SELECT ns, pod,
		       any(service_name)      AS svc,
		       any(node)              AS node,
		       count()                AS spans,
		       toUnixTimestamp64Nano(min(time)) AS first_ns,
		       toUnixTimestamp64Nano(max(time)) AS last_ns
		FROM (
			SELECT service_name, time,
			       %s AS ns,
			       res_values[indexOf(res_keys, 'k8s.pod.name')] AS pod,
			       res_values[indexOf(res_keys, 'k8s.node.name')] AS node
			FROM spans
			WHERE time >= ? AND time <= ?
			  AND has(res_keys, 'k8s.pod.name')
			LIMIT %d BY service_name
			LIMIT %d
		)
		WHERE pod != ''
		GROUP BY ns, pod
		ORDER BY spans DESC
		LIMIT ?
		SETTINGS max_execution_time = 25`, namespaceExpr(), podInventoryPerService, podInventorySampleRows)

	rows, err := s.telemetryReadConn().Query(ctx, q, from, to, limit)
	if err != nil {
		return nil, fmt.Errorf("pod inventory: %w", err)
	}
	defer rows.Close()

	out := &PodInventory{
		Rows:       []PodRow{},
		SampleRows: podInventorySampleRows,
		WindowSec:  int64(to.Sub(from).Seconds()),
	}
	for rows.Next() {
		var r PodRow
		if err := rows.Scan(&r.Namespace, &r.Pod, &r.Service, &r.Node,
			&r.Spans, &r.FirstSeen, &r.LastSeen); err != nil {
			continue
		}
		r.NameStable = looksStatefulSetName(r.Pod)
		out.Rows = append(out.Rows, r)
	}
	return out, rows.Err()
}
