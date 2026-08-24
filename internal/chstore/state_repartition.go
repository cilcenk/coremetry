// v0.9.1335 — problems + anomaly_events'ten PARTITION BY'ın SÖKÜLMESİ.
//
// BUG (Kural P1 ihlali, denetim §7.2 A1). İki tablo da v0.9.1334'e dek:
//
//	ENGINE = ReplacingMergeTree(version)
//	PARTITION BY toDate(started_at)
//	ORDER BY id
//
// started_at PARTITION BY'da ama ORDER BY'da DEĞİL. ReplacingMergeTree
// yalnız partition İÇİNDE dedup eder; bir satırın started_at'i değişip
// başka bir güne düşerse eski satır ölümsüz bir kopya olarak kalır ve
// `OPTIMIZE … FINAL` bile onu toplayamaz. Doğruluk tek bir SUNUCU
// AYARINA asılıdır: `do_not_merge_across_partitions_select_final=1`
// (yaygın bir FINAL hızlandırma vidası) açıldığı an FINAL iki satırı da
// döndürür ve okuma tarafı sürüm karşılaştırmıyorsa BAYAT satır kazanır.
//
// ── v0.9.1306 TEŞHİSİ ARTIK EKSİK ────────────────────────────────────
// v0.9.1306 kaymayı TOPOLOJİYE bağlamıştı: state tabloları shard-yerel,
// uygulama iki host'a birden bağlanıyor, taşıma SELECT'i satırı taşımayan
// shard'a düşünce 0 satır dönüyor ve started_at tazeleniyordu. O mekanizma
// v0.9.1308 + migrations/0009 ile KAPANDI ve KAPANDIĞI ÖLÇÜLDÜ (lokal
// chc-0, 2026-08-24: birleştirmeden bu yana İKİ tabloda da 0 yeni bölünme;
// mevcut 21 + 32 bölünmüş id'nin tamamı birleştirme ÖNCESİ).
//
// Ama teşhis kapsayıcı değildi — topolojiden BAĞIMSIZ iki dal duruyor:
//
//	anomaly_events · UpsertAnomalyEvents'in taşıma SELECT'i HATA verirse
//	  `prev` boş kalır (bilinçli yumuşak düşüş) → o tikin TÜM olayları
//	  "ilk görülme" gibi yazılır, started_at tazelenir. Ayrıca 30 günlük
//	  TTL satırı düşürdükten sonra aynı parmak izi yeniden ateşlerse
//	  started_at yeni pencereden gelir.
//	problems · problem id'si birçok dedektörde DETERMİNİSTİK
//	  (fatalExcProblemID, capacityProblemID, runtimeProblemID,
//	  sharedBurstProblemID, `anomaly-auto:<fp>:<servis>`). Kapanan bir
//	  problem yeniden açıldığında AYNI id'yi geri alır ama açılış dalı
//	  started_at'i o anki now/FirstSeen ile yazar. Kapanışla yeniden
//	  açılış arasına bir gece girdiği an ikinci partition doğar.
//
// Yani severity DÜŞTÜ (yüksek frekanslı shard kayması bitti) ama defect
// KAPANMADI — ve started_at kozmetik değil: P1 açık-saat eşiğini ve
// effectiveSeverity'nin yaş yükseltmesini besliyor.
//
// ── NEDEN PARTITION BY tuple() (ve neden id-hash DEĞİL) ──────────────
// Üç aday vardı:
//
//  1. PARTITION BY'ı SÖK. Yapısal olarak doğru: partition yoksa
//     partition-arası kopya İMKÂNSIZ. Bedeli (a) zaman aralıklı
//     okumalarda partition budaması, (b) DROP PARTITION ile retention.
//     ÖLÇÜLDÜ: (b) SIFIR — EnforceRetention yalnız spans/logs/
//     metric_points/profiles/exemplars/span_links{,_reverse} yönetiyor,
//     bu iki tabloya DOKUNMUYOR (retention_enforce.go). (a) tek yerde:
//     GetNoisyRules'un `started_at >=/<` penceresi — zaten `FINAL` ile
//     TÜM tabloyu okuyan, max_execution_time=25'lik bir rapor sorgusu;
//     prod'da tablo 635.864 satır (lokalde 4.918 satır / 490 KiB).
//     Üstelik FINAL bugün ZATEN partition'lar arası birleştirme yapmak
//     zorunda — partition'ı sökmek FINAL'i UCUZLATIR.
//  2. id'den türetilmiş hash kovası (`cityHash64(id) % N`). Kural P1'i
//     mekanik olarak geçer (id ORDER BY'ın kendisi) ama HİÇBİR ŞEY
//     kazandırmaz: zaman aralıklı budama yine gider, tablo yine tek
//     seferde okunur, üstelik küçük bir tabloyu N parçaya bölerek merge
//     sayısını artırır. Aynı göç maliyeti, sıfır fayda.
//  3. toDate(started_at)'i tutup started_at'i DEĞİŞMEZ kıl. Yukarıdaki
//     iki dal kapatılabilirdi ama "hiçbir yazıcı bu kolonu bir daha
//     asla yeniden yazmayacak" iddiası KANITLANAMAZ — bu tam olarak
//     v0.9.1306'nın sicile yazdığı ve YANLIŞ çıkan iddiadır.
//
// (1) seçildi; emsal root_cause_hypotheses (v0.9.1304), ai_feedback ve
// rca_verdicts.
//
// ── NEDEN BOOT'TA DROP+RECREATE YOK ──────────────────────────────────
// v0.9.1304 tabloyu boot'ta düşürüp yeniden kurabiliyordu çünkü
// root_cause_hypotheses TÜRETİLMİŞ veri: telemetryPurgeTables üyesi,
// synthesizer 30 sn'de yeniden üretiyor. Bu iki tablo GERİ GETİRİLEMEZ
// operatör durumu taşıyor (ack, assignee, AI özeti, incident bağları,
// 30 günlük anomali geçmişi). Onlar için tek doğru yol veri KORUYAN bir
// göç — migrations/0010_state_repartition.sql, OPERATÖR uygular.
//
// Bu dosyanın boot'ta yaptığı tek şey SALT OKUNUR bir teşhis: eski şema
// hâlâ yerindeyse operatöre söyle. Hiçbir DDL göndermez.
package chstore

import (
	"context"
	"fmt"
	"log"
	"sort"
	"strings"
)

// repartitionedStateTables — v0.9.1335'te PARTITION BY'ı sökülen state
// tabloları. TEK KAYNAK: boot uyarısı, testler ve 0010'un kapsam
// doğrulaması aynı listeden okur.
var repartitionedStateTables = []string{"anomaly_events", "problems"}

// statePartitionDriftMsg — hangi tabloların HÂLÂ eski şemada olduğunu
// anlatan operatör mesajı. SAF (tablo testli).
//
// Boş dilim → boş dize, ve çağıran hiç log basmaz: "her şey yolunda"
// satırı, gerçekten bir şey söyleyen satırların arasında gürültüdür.
//
// Sıralı: aynı kurulum iki boot'ta aynı satırı basmalı, yoksa log
// karşılaştırması yalancı fark üretir.
func statePartitionDriftMsg(stale map[string]string) string {
	if len(stale) == 0 {
		return ""
	}
	names := make([]string, 0, len(stale))
	for n := range stale {
		names = append(names, n)
	}
	sort.Strings(names)
	parts := make([]string, 0, len(names))
	for _, n := range names {
		parts = append(parts, n+" (PARTITION BY "+stale[n]+")")
	}
	return "⚠ " + strings.Join(parts, ", ") + " hâlâ ESKİ şemada — " +
		"ReplacingMergeTree partition SINIRINI aşamaz, aynı id iki " +
		"gün-partition'ına düşerse FINAL bayat satır döndürebilir " +
		"(Kural P1, v0.9.1335). Veri koruyan göç OPERATÖRDE: " +
		"migrations/0010_state_repartition.sql. Boot bu tabloları " +
		"KENDİLİĞİNDEN yeniden kurmaz — operatör durumu taşıyorlar."
}

// warnStatePartitionDrift — SALT OKUNUR boot teşhisi.
//
// Hiçbir DDL göndermez, hiçbir hata boot'u düşürmez: probe başarısız
// olursa sessizce çekilir (v0.9.1304'ün probe gerekçesiyle aynı — bir
// teşhis, bir önkoşul değil).
//
// Küme kipinde clusterAllReplicas: replikalardan BİRİ hâlâ eski şemada
// olabilir (göç yarıda kalmışsa) ve operatörün bunu duyması gerekir.
func (s *Store) warnStatePartitionDrift(ctx context.Context) {
	stale, err := s.statePartitionKeys(ctx, repartitionedStateTables)
	if err != nil {
		return
	}
	if msg := statePartitionDriftMsg(stale); msg != "" {
		log.Printf("[chstore] %s", msg)
	}
}

// statePartitionKeys — verilen tabloların system.tables'taki DOLU
// partition anahtarları (boş olanlar = zaten düzeltilmiş ya da tablo
// yok → sonuçta yer almaz).
func (s *Store) statePartitionKeys(ctx context.Context, names []string) (map[string]string, error) {
	if len(names) == 0 {
		return nil, nil
	}
	src := "system.tables"
	extra := ""
	if s.clusterMode() {
		src = fmt.Sprintf("clusterAllReplicas(%s, system.tables)", quoteCHIdent(s.cfg.ClusterName))
		extra = " SETTINGS skip_unavailable_shards = 1, max_execution_time = 5"
	} else {
		extra = " SETTINGS max_execution_time = 5"
	}
	q := "SELECT name, max(partition_key) AS pk FROM " + src +
		" WHERE database = currentDatabase() AND name IN (" +
		chPlaceholders(len(names)) + ") GROUP BY name" + extra
	rows, err := s.conn.Query(ctx, q, toAnySlice(names)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var n, pk string
		if err := rows.Scan(&n, &pk); err != nil {
			return nil, err
		}
		if strings.TrimSpace(pk) == "" {
			continue // düzeltilmiş
		}
		out[n] = pk
	}
	return out, rows.Err()
}
