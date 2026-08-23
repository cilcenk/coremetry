// v0.9.1308 — state tabloları TEK replikasyon grubunda.
//
// ÖLÇÜLEN BUG. Küme kipinde adaptDDL HER Replicated tablo için ZK yolunu
// `<prefix>/{shard}/<ad>` üretiyordu. Telemetri için doğru: her shard
// kendi dilimini tutar, Distributed sarmalayıcı okurken birleştirir.
// State tabloları (problems, alert_rules, users, system_settings, …)
// ise `defaultShardPolicy`/`highVolumeTables` kayıtlarında YOK — yani
// Distributed sarmalayıcıları da yok. Sonuç: her shard AYRI bir
// replikasyon grubu, uygulama hangi host'a bağlanırsa onun dilimini
// görür, veri sessizce bölünür.
//
//	LOKAL (chc-0 shard=01 / chc-1 shard=02), 2026-08-23:
//	  problems 4631/191 · anomaly_events 222/60 · incidents 2894/57
//	  alert_rules 8/0 · users 1/0 · system_settings 3/0 · dashboards 10/0
//	PROD (uptrace_all, 2 shard × 2 replica):
//	  problems 633.236 (bp01/bp02) vs 4.169 (bp03/bp04) — AÇIK
//	  problemlerin tamamı küçük shard'da.
//
// HEDEF TASARIM (operatör onaylı, ayrı küme tanımı YOK):
//
//	ENGINE = ReplicatedReplacingMergeTree(
//	  '<prefix>/state/<ad>',   -- {shard} YOK: tek grup
//	  '{shard}-{replica}',     -- her node benzersiz
//	  version)
//
// `{shard}-{replica}` bilinçli: prod'da `{replica}` makrosunun shard
// başına tekrar ediyor olması İHTİMAL (doğrulanmadı); bu yazım her iki
// makro düzeninde de benzersiz kalır.
//
// SPLIT-BRAIN KARARI — bkz. useUnifiedStatePath.
package chstore

import (
	"context"
	"fmt"
	"log"
	"strings"
)

// stateReplicaDir — birleşik state yolunda `{shard}` yerine geçen sabit
// segment. `/clickhouse/tables/state/problems` gibi.
//
// "state" bir shard makrosu DEĞİL, sabit metin: bu yüzden dört host da
// aynı znode'a bakar ve tek replikasyon grubu oluşur.
const stateReplicaDir = "state"

// stateReplicaName — birleşik gruptaki replika adı şablonu.
//
// Telemetri tarafı `{replica}` kullanır ve bu yeterlidir, çünkü orada
// grup zaten shard başına ayrıdır. Birleşik grupta dört host TEK grupta
// buluşur; `{replica}` shard başına tekrar ediyorsa iki host aynı
// replika adını iddia eder ve ikincisi REPLICA_ALREADY_EXISTS alır.
// `{shard}-{replica}` her iki makro düzeninde de benzersizdir.
const stateReplicaName = "{shard}-{replica}"

// shardedTelemetryTable — bu ad cluster.go'daki ÜÇ kayıttan herhangi
// birinde geçiyor mu?
//
// TEK KAYNAK. Elle yazılmış ikinci bir "state tabloları" listesi
// TUTULMUYOR: iki liste zamanla ıraksar ve tam da bu repo bunu iki kez
// yaşadı (v0.5.426 defaultShardPolicy⊅highVolumeTables, v0.9.350
// operation_group_summary_5m). Kural tersine çevrilmiştir:
//
//	"shard'lı telemetri" = üç kayıttan birinde ADI GEÇEN
//	"state"              = geçmeyen
//
// Yani yeni bir telemetri tablosu eklerken kaydı UNUTMAK, tabloyu
// state sınıfına düşürür — sessiz shard bölünmesi değil, gürültülü
// "tek grupta çok fazla veri" yönünde hata. Yeni bir state tablosu
// eklerken ise yapılacak HİÇBİR ŞEY yok: doğru davranışı otomatik alır.
func shardedTelemetryTable(name string) bool {
	if highVolumeTables[name] || tablesWithoutTraceID[name] {
		return true
	}
	_, ok := defaultShardPolicy[name]
	return ok
}

// stateTableDDL — bu DDL hedefi birleşik yolda yaşaması gereken bir
// state tablosu mu? SAF.
//
// kind: identifyDDLTarget'ın döndürdüğü tür.
//   - "table"      → aday
//   - "mv"         → ASLA. Bir MV'nin hedefi shard-yerel `_local`
//     kaynaktan besleniyor; birleşik gruba yazmak iki shard'ın MV'sini
//     aynı tabloya toplardı. Bu ayrı (ve çok daha büyük) bir karar,
//     0009'un kapsamı DEĞİL.
//   - "altertable" → ALTER'ın ENGINE cümlesi yok, ZK yolu üretilmiyor.
func stateTableDDL(name, kind string) bool {
	if kind != "table" || name == "" {
		return false
	}
	return !shardedTelemetryTable(name)
}

// stateProbeTable — boot probe'unda GÖZLEME dahil edilecek mi?
//
// system.replicas gerçek CH tablo adlarını döndürür; adaptDDL ise
// çıplak adı görür. Küme kipine özgü üç ad biçimi elenir, yoksa
// `spans_local` "state" sanılırdı:
//   - `.inner_id.…`  → combined MV'nin iç tablosu
//   - `…_local`      → shard-yerel telemetri
//   - `…_old` / `…_unified` → 0009 göçünün geçici tabloları. Bilinçli
//     dışarıda: `_old` göç sonrası doğrulama bitene dek YAŞAR ve
//     eski (shard'lı) yolda durur — sayılsaydı kuşak kararını
//     "göç öncesi"ne kilitlerdi.
func stateProbeTable(name string) bool {
	switch {
	case strings.HasPrefix(name, ".inner"):
		return false
	case strings.HasSuffix(name, "_local"):
		return false
	case strings.HasSuffix(name, "_old"):
		return false
	case strings.HasSuffix(name, "_unified"):
		return false
	}
	return stateTableDDL(name, "table")
}

// replicatedArgs — Replicated*MergeTree'nin İLK İKİ argümanı.
//
// unified=false dalı v0.9.1308 ÖNCESİYLE BAYT BAYT AYNIDIR
// (state_replication_test.go bunu eski biçim dizesine karşı pinliyor):
// telemetri tablolarının ZK yolu bu değişiklikte bir karakter bile
// değişmez.
func replicatedArgs(zkPrefix, name string, unified bool) string {
	if unified {
		return fmt.Sprintf("'%s', '%s'", unifiedStatePath(zkPrefix, name), stateReplicaName)
	}
	return fmt.Sprintf("'%s/{shard}/%s', '{replica}'", zkPrefix, name)
}

// unifiedStatePath — birleşik ZK yolunun tam metni.
func unifiedStatePath(zkPrefix, name string) string {
	return zkPrefix + "/" + stateReplicaDir + "/" + name
}

// stateObservation — boot probe'unun sonucu.
//
// Sıfır değeri (ok=false) BİLİNÇLİ olarak "eski yol" demektir: probe
// hiç koşmamış bir Store (testler, tek düğüm, adaptDDL'i migrate'ten
// önce çağıran bir yol) v0.9.1308 öncesiyle birebir aynı DDL üretir.
type stateObservation struct {
	ok bool
	// paths: state tablosu adı → kümede GÖZLENEN zookeeper_path.
	paths map[string]string
}

// useUnifiedStatePath — SPLIT-BRAIN MUHAFIZI. SAF.
//
// SORUN. `CREATE TABLE IF NOT EXISTS` var olan tabloya dokunmaz, yani
// mevcut kurulum göç koşana dek eski yolda kalır — doğru davranış. AMA
// kümeye YENİ bir node eklenirse ya da bir node'un tablosu silinmişse,
// o node kodun ürettiği yeni yolla yaratır ve komşularından FARKLI bir
// ZK grubuna katılır: bugünkü bug'ın birebir tekrarı, üstelik sessiz.
//
// KARAR: (a) — yol KODDAN değil, KÜMEDEN okunur.
//
// Kodun ürettiği birleşik yol yalnızca bir ÖNERİdir. Gerçekte CREATE'e
// giren şey "komşularımın bu tablo için kullandığı yol"dur; birleşik
// yol yalnız tabloyu HİÇ KİMSE tutmuyorsa kullanılır.
//
// Neden (b) — system_settings bayrağı — DEĞİL:
//   - Bayrak İKİNCİ bir doğruluk kaynağıdır ve iki yönde de yanlış
//     olabilir: göçten önce açılırsa bölünme yaratır, göçten sonra
//     kapalı kalırsa uygulama birleşik tabloların yanına eski yollu
//     tablolar kurar.
//   - system_settings'in KENDİSİ bir state tablosudur; yolunu ondan
//     okumak, okumak için var olmasını gerektirir (döngü).
//   - (a) kendiliğinden tamamlanır: 0009'un RENAME'i bittiği an dört
//     host da birleşik yolu raporlar, uygulama bir sonraki boot'ta
//     ONU görür. Çevrilecek bir düğme, ikinci bir deploy yoktur.
//
// KARAR SIRASI:
//  1. Probe koşmadı → ESKİ yol. "Hiçbir şeyi değiştirme" mevcut bir
//     kümeyi bölemez. (Taze kurulumda bugünkü davranışı tekrarlar —
//     ve o durumda probe'un başarısız olması için bağlantının ölmüş
//     olması gerekir, ki o hâlde boot zaten çöker.)
//  2. Tablo kümede GÖZLENDİ → gözlenen yolun kendisi. Komşularına
//     katıl, ne olursa olsun.
//  3. Tablo hiçbir node'da yok → kurulumun KUŞAĞI: herhangi bir state
//     tablosu hâlâ eski yoldaysa kurulum göç ÖNCESİdir → eski yol.
//     Bu, "yeni node eklendi" senaryosunun tam karşılığıdır: yeni node
//     30+ eski yollu komşu görür ve eksik tablosunu da eski yolla kurar.
//  4. Hiçbir state tablosu yok (taze kurulum) ya da hepsi birleşik
//     (göç sonrası) → BİRLEŞİK yol.
func useUnifiedStatePath(obs stateObservation, zkPrefix, name string) (bool, string) {
	if !obs.ok {
		return false, "probe koşmadı — mevcut kuruluma dokunulmuyor (eski yol)"
	}
	if p, seen := obs.paths[name]; seen {
		if p == unifiedStatePath(zkPrefix, name) {
			return true, "tablo zaten birleşik yolda"
		}
		return false, "tablo kümede eski yolda (" + p + ") — komşularına katılıyor"
	}
	legacy := 0
	for t, p := range obs.paths {
		if p != unifiedStatePath(zkPrefix, t) {
			legacy++
		}
	}
	if legacy > 0 {
		return false, fmt.Sprintf("kurulum göç ÖNCESİ (%d state tablosu eski yolda)", legacy)
	}
	return true, "taze veya göç SONRASI kurulum"
}

// resolveStateReplicaPaths — boot probe'u. migrate()'in ilk DDL'inden
// ÖNCE, tek goroutine'den çağrılır.
//
// İKİ AŞAMA:
//  1. YEREL system.replicas — probe'un KOŞTUĞUNU bu belirler. Bellek
//     içi bir sistem tablosu; başarısız olması bağlantının ölmesi
//     demektir, o hâlde boot zaten sürmez.
//  2. clusterAllReplicas(…) — best effort, `skip_unavailable_shards=1`
//     ile: rolling CH restart sırasında bir replika kapalıyken probe'un
//     tamamen düşmesini engeller. Ulaşılabilen node'ların cevabı
//     "bu tablo hangi grupta" sorusu için yeterlidir.
//
// ÇAKIŞMA (aynı tablo, iki farklı yol) = kurulum ZATEN bölünmüş.
// Muhafazakâr davranılır: ESKİ yol kazanır. Yeni bir yola geçmek
// bölünmeyi büyütür; eski yolda kalmak en fazla "hiçbir şey yapma"dır.
func (s *Store) resolveStateReplicaPaths(ctx context.Context) {
	s.stateObs = stateObservation{}
	if !s.clusterMode() {
		return
	}
	zkPrefix := s.zkPrefix()

	local, err := s.queryReplicaPaths(ctx, "system.replicas", false)
	if err != nil {
		log.Printf("[chstore] state ZK yolu probe'u BAŞARISIZ (%v) — state tabloları ESKİ (shard'lı) yolda kalıyor; "+
			"birleşik yola geçiş 0009 göçüyle operatörde", err)
		return
	}
	obs := stateObservation{ok: true, paths: local}

	if cl, cerr := s.queryReplicaPaths(ctx, fmt.Sprintf("clusterAllReplicas(%s, system.replicas)",
		quoteCHIdent(s.cfg.ClusterName)), true); cerr != nil {
		log.Printf("[chstore] küme geneli state yolu okunamadı (%v) — yalnız YEREL gözleme göre karar veriliyor", cerr)
	} else {
		for t, p := range cl {
			old, dup := obs.paths[t]
			if dup && old != p {
				log.Printf("[chstore] ⚠ %s İKİ farklı ZK yolunda görüldü (%q vs %q) — kurulum ZATEN bölünmüş; "+
					"eski yol tercih ediliyor, 0009 göçü bunu onarır", t, old, p)
				if old == unifiedStatePath(zkPrefix, t) {
					obs.paths[t] = p // eski olan kazanır
				}
				continue
			}
			obs.paths[t] = p
		}
	}
	s.stateObs = obs

	unified, legacy := 0, 0
	for t, p := range obs.paths {
		if p == unifiedStatePath(zkPrefix, t) {
			unified++
		} else {
			legacy++
		}
	}
	log.Printf("[chstore] state ZK yolu probe'u: %d tablo gözlendi (%d birleşik, %d eski) — yeni state tabloları %s yola kurulacak",
		len(obs.paths), unified, legacy, map[bool]string{true: "BİRLEŞİK", false: "ESKİ"}[legacy == 0])
}

// queryReplicaPaths — <src>'ten (tablo → zookeeper_path) okur, yalnız
// state adaylarını tutar.
func (s *Store) queryReplicaPaths(ctx context.Context, src string, skipUnavailable bool) (map[string]string, error) {
	q := "SELECT DISTINCT table, zookeeper_path FROM " + src + " WHERE database = currentDatabase()"
	if skipUnavailable {
		q += " SETTINGS skip_unavailable_shards = 1"
	}
	rows, err := s.conn.Query(ctx, q)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var t, p string
		if err := rows.Scan(&t, &p); err != nil {
			return nil, err
		}
		if !stateProbeTable(t) {
			continue
		}
		out[t] = p
	}
	return out, rows.Err()
}

// zkPrefix — Replicated yollarının kök öneki. adaptDDL ile AYNI
// varsayılana düşer; iki yerde ayrı yazılsaydı probe'un karşılaştırdığı
// yol ile üretilen yol sessizce ıraksardı.
func (s *Store) zkPrefix() string {
	p := strings.TrimRight(s.cfg.ReplicaPath, "/")
	if p == "" {
		p = "/clickhouse/tables"
	}
	return p
}

// quoteCHIdent — küme adını clusterAllReplicas'a string literal olarak
// geçirir. Tek tırnak ikilenir; makul olmayan bir küme adı sorguyu
// bozamaz.
func quoteCHIdent(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "''") + "'"
}
