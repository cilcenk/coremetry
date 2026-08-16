package chstore

import (
	"context"
	"log"
	"strings"
)

// distributed_batching.go — v0.9.1076 (operatör isteği, 2026-08-16 prod
// olayı): Distributed motorlu tabloların arka plan göndericisinde batch
// modunu BOOT'ta otomatik açar.
//
// Olayın kendisi: prod'da 31 Temmuz'daki bir bellek tepesi (kod 241)
// spool'u 3,66M dosya / 1.2 TiB'a şişirdi. Bellek günler önce düzeldiği
// hâlde drenaj ~5 dosya/dk'da süründü, ETA "14+ gün" gösterdi. Neden:
// gönderici varsayılanda spool dosyalarını TEK TEK INSERT'ler —
// milyonlarca küçük dosyada tur maliyeti + backoff toplamı drenajı
// öldürür. batch modu dosyaları tek INSERT'te birleştirir;
// split_batch_on_failure da bozuk tek batch'te dosya-başına moda geri
// düşen sigortadır (241 tekrarında veri kenara atılmaz).
//
// Operatör konsoldan uygulayamaz: /admin/sql bilinçli salt-okunur.
// Bu yüzden geçiş boot'ta ve otomatik — "alterı bir sonraki imageda
// sen otomatik yap".
//
// Güvenlik sınırları:
//   - Soft-fail: hiçbir dal boot'u düşürmez (go-dep dersi v0.9.görünümü:
//     prod crashloop'un en pahalı sınıfı boot-yolu sürprizi).
//   - İdempotent: ayar zaten açıksa (engine_full'da görünür) tablo
//     atlanır; tekrar ALTER da zararsız olurdu ama sessiz boot tercih.
//   - Sürüm toleransı: yeni ad (background_insert_batch, CH ≥23.3) önce,
//     "Unknown setting" hâlinde eski ad (monitor_batch_inserts) denenir.
//   - Küme adı UYGULAMA CONFIG'İNDEN ALINMAZ (prod'da çoğu zaman boş —
//     v0.8.185/186 sınıfı): tablonun kendi engine_full'undan ayrıştırılır.
//     ON CLUSTER denemesi başarısızsa yerel ALTER'a düşülür — spool
//     zaten uygulamanın bağlandığı node'da.

// distributedClusterOf — Distributed(...) engine_full metninden küme
// adını çıkarır (saf, tablo testli). İlk argüman tırnaklı ('ad') ya da
// çıplak (ad) olabilir; ayrıştırılamayan her şekil "" döner (→ yalnız
// yerel ALTER denenir; yanlış ad üretmekten her zaman daha güvenli).
func distributedClusterOf(engineFull string) string {
	const prefix = "Distributed("
	i := strings.Index(engineFull, prefix)
	if i < 0 {
		return ""
	}
	rest := engineFull[i+len(prefix):]
	j := strings.IndexByte(rest, ',')
	if j < 0 {
		return ""
	}
	arg := strings.TrimSpace(rest[:j])
	arg = strings.Trim(arg, "'`\"")
	// Kimlik hijyeni: backtick'lenerek SQL'e girecek — backtick veya
	// tırnak İÇEREN bir "ad" ayrıştırma hatasıdır, güvenle boş dön.
	if arg == "" || strings.ContainsAny(arg, "'`\" ") {
		return ""
	}
	return arg
}

// hasDistributedBatching — ayar zaten açık mı? (saf). CH, tablo-düzeyi
// ayarları SHOW CREATE / engine_full sonundaki SETTINGS ekinde
// `ad = 1` biçiminde basar; iki sürüm adı da kontrol edilir.
func hasDistributedBatching(engineFull string) bool {
	return strings.Contains(engineFull, "background_insert_batch = 1") ||
		strings.Contains(engineFull, "monitor_batch_inserts = 1")
}

// batchingAlters — bir tablo için denenecek ALTER'lar, sırayla (saf,
// tablo testli). Sıranın mantığı: en geniş kapsam + en yeni ad önce;
// her başarısızlık bir sonrakine düşer.
//
//  1. ON CLUSTER + yeni adlar  — ayar her node'a gider (spool birden
//     çok node'da olabilir; insert havuzu round-robin).
//  2. ON CLUSTER + eski adlar  — eski CH sürümü.
//  3. yerel + yeni adlar       — dağıtık DDL kuyruğu yok/izinsiz.
//  4. yerel + eski adlar.
//
// ON CLUSTER denemeleri kısa DDL zamanaşımı taşır: kuyruk tıkalıysa
// boot 180s×tablo beklememeli — kod 159 zaten "kuyruğa alındı" demek
// ve çağıran onu başarı sayar (v0.9.604 emsali).
func batchingAlters(table, cluster string) []string {
	const newNames = "background_insert_batch = 1, background_insert_split_batch_on_failure = 1"
	const oldNames = "monitor_batch_inserts = 1, monitor_split_batch_on_failure = 1"
	var out []string
	if cluster != "" {
		on := "ALTER TABLE `" + table + "` ON CLUSTER `" + cluster + "` MODIFY SETTING "
		const ddlTimeout = " SETTINGS distributed_ddl_task_timeout = 20"
		out = append(out, on+newNames+ddlTimeout, on+oldNames+ddlTimeout)
	}
	local := "ALTER TABLE `" + table + "` MODIFY SETTING "
	return append(out, local+newNames, local+oldNames)
}

// ensureDistributedBatching — boot geçişi. Distributed tabloları keşfet,
// batch modu kapalı olanlara sırayla ALTER dene. Tümü soft-fail.
func (s *Store) ensureDistributedBatching(ctx context.Context) {
	rows, err := s.conn.Query(ctx, `SELECT name, engine_full FROM system.tables
		WHERE database = currentDatabase() AND engine = 'Distributed'
		SETTINGS max_execution_time = 5`)
	if err != nil {
		log.Printf("[chstore] distributed batching keşfi düştü (boot sürüyor): %v", err)
		return
	}
	type target struct{ name, engineFull string }
	var targets []target
	for rows.Next() {
		var t target
		if err := rows.Scan(&t.name, &t.engineFull); err != nil {
			rows.Close()
			log.Printf("[chstore] distributed batching keşfi okunamadı (boot sürüyor): %v", err)
			return
		}
		targets = append(targets, t)
	}
	rows.Close()
	for _, t := range targets {
		if hasDistributedBatching(t.engineFull) {
			continue
		}
		alters := batchingAlters(t.name, distributedClusterOf(t.engineFull))
		var lastErr error
		applied := ""
		for _, stmt := range alters {
			err := s.conn.Exec(ctx, stmt)
			if err == nil || isDistributedDDLQueued(err) {
				applied = stmt
				break
			}
			lastErr = err
		}
		if applied != "" {
			log.Printf("[chstore] distributed batching açıldı: %s", applied)
		} else {
			log.Printf("[chstore] distributed batching açılamadı %s (boot sürüyor): %v", t.name, lastErr)
		}
	}
}
