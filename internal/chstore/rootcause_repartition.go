// v0.9.1304 — root_cause_hypotheses'ten PARTITION BY'ın SÖKÜLMESİ.
//
// BUG (CH şema taraması, Kural P1 ihlali). Tablo v0.9.1303'e kadar şöyleydi:
//
//	ENGINE = ReplacingMergeTree(version)
//	PARTITION BY toYYYYMM(computed_at)
//	ORDER BY (anchor_kind, anchor_id)
//
// `computed_at` PARTITION BY'da ama ORDER BY'da DEĞİL — ve synthesizer onu
// HER tikte `now` ile yeniden yazıyor (30 sn, tik başına ≤64 anchor; açık
// critical problemler + son 30 dk'nın anomalileri). Yani ay sınırını geçen
// her AÇIK anchor ikinci bir satır kazanıyor.
//
// ReplacingMergeTree'nin arka plan birleştirmesi partition SINIRINI AŞMAZ:
// `OPTIMIZE TABLE … FINAL` bile o iki satırı birleştiremez (ölçüldü,
// aşağıda). Kopya fiziksel olarak ÖLÜMSÜZ — 30 günlük TTL onu düşürene dek.
//
// NEDEN OKUMALAR BUGÜN YİNE DE DOĞRU — ve neden bu bir teselli değil:
// `SELECT … FINAL` varsayılan `do_not_merge_across_partitions_select_final=0`
// altında sorgu anında partition'lar arası birleştirme yapar. Yani doğruluk
// tamamen bir SUNUCU AYARINA yaslanmış durumda. CH 24.8.14'te iki kol da
// doğrulandı (chc-0, iki partition'da aynı anchor):
//
//	OPTIMIZE … FINAL sonrası fiziksel satır : 2  (birleşmedi)
//	SELECT … FINAL                          : 1  → FRESH  (doğru)
//	SELECT … FINAL SETTINGS do_not_…=1      : 2  → STALE + FRESH
//
// Üçüncü satır asıl tehlike: o vida (yaygın bir FINAL hızlandırma ayarı)
// kurulduğu anda GetHypotheses'in `out[h.AnchorID] = h` yazımı SON satırı
// kazandırır — sürüm karşılaştırması yok — ve /problems + /anomalies
// ribbon'ı SESSİZCE bayat bir kök-neden şüphelisi gösterir.
//
// DÜZELTME: PARTITION BY'ı düşür (emsal: ai_feedback ve rca_verdicts, ikisi
// de tam bu gerekçeyle bilinçli partition'sız — store.go'daki yorumlarına
// bak). ORDER BY'a `computed_at` EKLEMEK yanlış olurdu: dedup anahtarını
// bozar, her yeniden hesap yeni satır olurdu.
//
// PARTITION BY ALTER edilemez → tablo yeniden kurulmalı.
//
// NEDEN DROP+RECREATE SAVUNULABİLİR (veri kopyalamak yerine):
//  1. Reponun KENDİ sınıflandırması: root_cause_hypotheses
//     telemetryPurgeTables üyesi — "purely-mechanical generated analysis,
//     no operator content, regenerates from new telemetry". Operatör bugün
//     zaten /admin yüzeyinden bu tabloyu TRUNCATE edebiliyor. Tek seferlik
//     bir drop, ürünün hâlihazırda sunduğu purge'den kesinlikle daha zayıf
//     bir işlem.
//  2. Veri türetilmiş ve kendini onarır: synthesizer 30 sn'lik tikte açık
//     anchor'ları yeniden sentezler; okumalar (EnrichProblems/Anomalies…)
//     hata durumunda zaten soft-fail edip dürüst "no clear cause yet"
//     boş durumunu gösteriyor, sayfa boşalmıyor.
//  3. Risk altındaki azami veri 30 günlük TÜRETİLMİŞ sıralama + deep_evidence
//     denetim izi. Önemli olan P1 anchor'ları hâlâ AÇIK, dolayısıyla ilk
//     tikte geri geliyorlar.
//  4. EXCHANGE TABLES ile veri korumak DAHA KÖTÜ bir takas: (a) ZK yolu
//     tablo ADINDAN türetiliyor (adaptDDL) — takas sonrası tablo
//     `…_new` znode'unda yaşardı ve reset.go'nun ada göre znode temizliği
//     sessizce ıskalardı; (b) rolling deploy'da N pod'un yarıştığı 4
//     ifadelik ON CLUSTER zinciri tam olarak v0.9.613'ün kuyruk tıkanma
//     sınıfı. Sınırlı ve kendini onaran bir veri kaybını yeni bir dağıtık
//     DDL arıza kipiyle takas etmek kötü bir alışveriş.
//
// İDEMPOTENT + DAĞITIK-GÜVENLİ: müdahale YALNIZ system.tables'ta hâlâ bir
// partition anahtarı görünürse koşar. Taze kurulumda tablo yok (probe boş
// dize döner) → no-op; düzeltme sonrası partition anahtarı boş → no-op. Küme kipinde
// DROP `ON CLUSTER … SYNC` (SYNC şart: znode temizlenmeden gelen CREATE
// "Replica already exists" verirdi), tespit clusterAllReplicas ile HER
// replikaya bakar; cluster_name unset ise (prod'da sık — bkz.
// feedback-distributed-column-safety) clusterMode() false olur ve iki
// sorgu da yerel system.tables'a düşer.
package chstore

import (
	"context"
	"fmt"
	"log"
	"strings"
)

// tableDDLByName — mvDDLByName'in TABLO kardeşi: `tables` diliminden bir
// `CREATE TABLE IF NOT EXISTS <name>` ifadesini ADA göre çeker.
//
// Konumsal indeks YERİNE ad araması, mvDDLByName'in gerekçesiyle birebir
// aynı (v0.8.52: dilim yeniden sıralanınca konumsal migration sessizce
// yanlış tabloyu hedeflemişti). Ad eşleşmesi TAM: addan sonraki karakter
// boşluk, satırbaşı ya da '(' olmalı, böylece "spans" araması
// "span_links"i asla yakalamaz. Yoksa "" döner.
//
// SAF — tablo testli.
func tableDDLByName(tables []string, name string) string {
	needle := "CREATE TABLE IF NOT EXISTS " + name
	for _, q := range tables {
		i := strings.Index(q, needle)
		if i < 0 {
			continue
		}
		rest := q[i+len(needle):]
		if rest == "" || rest[0] == ' ' || rest[0] == '\n' ||
			rest[0] == '\t' || rest[0] == '\r' || rest[0] == '(' {
			return q
		}
	}
	return ""
}

// rootCauseNeedsRepartition — SAF kapı: system.tables'tan okunan partition
// anahtarı verildiğinde müdahale gerekli mi?
//
// Boş dize = partition yok = ya taze kurulum (tablo hiç yok, probe boş
// döner) ya da düzeltme zaten uygulanmış. İkisinde de no-op. Yalnız DOLU
// bir partition anahtarı eski şemayı kanıtlar.
//
// Ayrı bir fonksiyon çünkü kararın kendisi test edilebilir olmalı: müdahale
// YIKICI, ve "ne zaman koşar" sorusunun cevabı bir SQL dizesinin içinde
// gömülü kalmamalı.
func rootCauseNeedsRepartition(partitionKey string) bool {
	return strings.TrimSpace(partitionKey) != ""
}

// rootCauseDropStmt — eski tabloyu düşüren ifade. SAF (tablo testli).
//
// SYNC: Replicated motorda znode'un tamamen silinmesini bekler; beklemezsek
// hemen ardından gelen CREATE "Replica already exists" ile patlar.
// max_table_size_to_drop / max_partition_size_to_drop = 0: v0.8.190'da bir
// boot DROP'u CH'nin 50 GB koruma eşiğine takılıp prod'u crash-loop'a
// sokmuştu. Bu tablo küçük ama koruma bir SUNUCU varsayılanı ve operatör
// onu düşürmüş olabilir — sigorta bedava.
func rootCauseDropStmt(onCluster string) string {
	return "DROP TABLE IF EXISTS root_cause_hypotheses" + onCluster + " SYNC" +
		" SETTINGS max_table_size_to_drop = 0, max_partition_size_to_drop = 0"
}

// repartitionRootCauseHypotheses — eski (partition'lı) şemayı tespit edip
// tabloyu doğru şemayla yeniden kurar.
//
// migrate() içinde `existing := s.existingObjects(ctx)` anlık görüntüsünden
// ÖNCE çağrılır ve bu SIRA yük taşır:
//   - o noktada s.deferDDL henüz false, yani DROP+CREATE SENKRON koşar.
//     Ertelenmiş olsalardı (v0.9.614) tıkalı bir kuyrukta tablo DAKİKALARCA
//     yok kalırdı — "kopya satır" bug'ını "özellik tamamen kapalı"ya
//     çevirmek olurdu.
//   - anlık görüntü müdahaleden SONRA alındığı için tutarlı: CREATE
//     başarılıysa planDDL dilimdeki ifadeyi eler, başarısızsa gönderir.
//     Yani bu fonksiyon patlasa bile normal yol tabloyu geri kurar.
//
// Hata BOOT'U DÜŞÜRMEZ: bu bir onarım, bir önkoşul değil. Drop başarısız
// olursa eski tablo yerinde kalır ve davranış v0.9.1303 ile birebir aynıdır
// — bugün olduğundan daha kötü değil.
func (s *Store) repartitionRootCauseHypotheses(ctx context.Context, createDDL string) {
	partKey, err := s.rootCausePartitionKey(ctx)
	if err != nil {
		log.Printf("[chstore] root_cause_hypotheses partition probe: %v — "+
			"yeniden kurulum atlandı (mevcut şema korunuyor)", err)
		return
	}
	if !rootCauseNeedsRepartition(partKey) {
		return
	}
	if createDDL == "" {
		log.Printf("[chstore] root_cause_hypotheses CREATE ifadesi `tables` " +
			"diliminde bulunamadı — yeniden kurulum atlandı")
		return
	}
	log.Printf("[chstore] root_cause_hypotheses eski şemada (PARTITION BY %s) — "+
		"tablo yeniden kuruluyor; hipotezler synthesizer'ın bir sonraki "+
		"tikinde (≤30 sn) yeniden üretilir", partKey)
	if err := s.conn.Exec(ctx, rootCauseDropStmt(s.onCluster())); err != nil {
		log.Printf("[chstore] root_cause_hypotheses DROP: %v — eski şema "+
			"yerinde kalıyor", err)
		return
	}
	if err := s.execDDL(ctx, createDDL); err != nil {
		// Tablo şu an YOK. Panik yok: anlık görüntü bundan sonra alınıyor,
		// dolayısıyla `tables` dilimindeki CREATE bu boot'ta gönderilecek.
		log.Printf("[chstore] root_cause_hypotheses CREATE: %v — normal DDL "+
			"yolu yeniden kuracak", err)
		return
	}
	log.Printf("[chstore] root_cause_hypotheses partition'sız yeniden kuruldu " +
		"(v0.9.1304, Kural P1)")
}

// rootCausePartitionKey — tablonun system.tables'taki partition anahtarı.
//
// Skaler agregat (`max(...)` değil `any(...)`, ama boş kümede de TEK satır
// dönen bir şekil) yerine doğrudan max() kullanılıyor: eşleşen satır yoksa
// max() boş dize döner, yani "tablo yok" ile "partition yok" aynı kola
// düşer — ikisi de no-op, ayırt etmeye gerek yok.
//
// Küme kipinde clusterAllReplicas: replikalardan BİRİ hâlâ eski şemadaysa
// müdahale koşmalı (DROP zaten ON CLUSTER, hepsini onarır).
// skip_unavailable_shards=1 — düşük bir replika onarımı engellemesin.
func (s *Store) rootCausePartitionKey(ctx context.Context) (string, error) {
	const where = `WHERE database = currentDatabase() AND name = 'root_cause_hypotheses'`
	q := `SELECT max(partition_key) FROM system.tables ` + where +
		` SETTINGS max_execution_time = 5`
	if s.clusterMode() {
		q = fmt.Sprintf(
			`SELECT max(partition_key) FROM clusterAllReplicas(%s, system.tables) `+where+
				` SETTINGS skip_unavailable_shards = 1, max_execution_time = 5`,
			"`"+strings.ReplaceAll(s.cfg.ClusterName, "`", "")+"`")
	}
	var key string
	if err := s.conn.QueryRow(ctx, q).Scan(&key); err != nil {
		return "", err
	}
	return key, nil
}
