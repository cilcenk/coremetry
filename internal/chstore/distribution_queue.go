// v0.9.985 — Distributed spool derinliği: dağıtık kipte "yazdım" ≠ "indi".
//
// CANLI ARIZA (lokal küme, 2026-08-12, 3s39d): `spans_local`'a son yazım
// chc-0'da 08:22, chc-1'de 08:49 iken CH saati 12:02 — son 10 dakikada
// İKİ shard'da da 0 satır. Aynı anda /api/health yemyeşil:
//
//	clickhouse: "ok" · spans_write_failed: 0 · spans_queued: 0
//	spans_accepted: 109814 (ve TIRMANIYOR)
//
// Hiçbir sayaç yalan söylemiyordu — hepsi YANLIŞ KATMANI ölçüyordu.
//
// SÖZLEŞME: Coremetry `coremetry.spans` (Distributed) tablosuna yazar.
// Distributed motoru INSERT'i DİSKE SPOOL EDİP HEMEN OK döner; asıl
// gönderim arka plandaki gönderici thread'inde `*_local`'a olur. O
// gönderici code-241 (Memory limit total) / code-159 (Timeout) yerken
// uygulama katmanının gördüğü tek şey "OK"tir. Yazma yolundaki hiçbir Go
// sayacı bunu göremez — Accepted artar, WriteFailed 0 kalır, kuyruk boş
// görünür. Cevap YALNIZCA `system.distribution_queue`'da.
//
// Bu, dağıtık kipe özgü bir ürün açığıydı ve prod da dağıtık kipte.
//
// TEK-DÜĞÜM: `cluster_name` boşken Distributed tablo da yok, spool da yok.
// O kurulumda buradan HİÇBİR sorgu çıkmaz (distributionQueueSQL "" döner,
// çağıran nil ile erken döner) — davranış v0.9.984 ile bayt-bayt aynı.
package chstore

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"
)

// distributedBacklogFloor — altında spool derinliğinin ARIZA sayılmadığı
// dosya sayısı.
//
// Neden sabit ve neden 100 (CLAUDE.md: eşik ayarlanabilir DEĞİL, gerekçe
// yorumda): sağlıklı bir gönderici spool dosyasını saniyeler içinde
// boşaltır, yani derinlik tek/çift hanede gezinir. Canlı ölçüm
// (2026-08-12, arızalı küme): sağlıklı tablolar 0-1 dosya
// (span_links 1, logs 0, profiles 1), kilitlenmiş olanlar 12.702 ve
// 26.132 dosya. 100 bu iki rejimi rahatça ayırır ve örnekleme fazından
// gelen jitter'ı yutar — eşiksiz bir "artıyor mu" testi sağlıklı kümede
// 3→5 dosyalık salınımı arıza diye bağırır ve sinyali değersizleştirirdi.
const distributedBacklogFloor = 100

// distributedBacklogAlarm — MUTLAK TABAN: bu derinliğin üstünde spool
// TREND NE OLURSA OLSUN arızadır. Yön sorulmaz bile.
//
// CANLI FLAP (v0.9.987'nin sebebi, 2026-08-12 16:07, aynı arızalı küme):
//
//	16:07:40  degraded  44.320 dosya  "BÜYÜYOR"
//	16:07:51  ok        44.318 dosya  "drene oluyor"
//
// 44 bin dosyalık bir spool'da iki ölçüm arasında İKİ dosya eridi ve saf
// trend kararı sinyali OK'a çevirdi. Arıza aralıksız sürerken sinyal
// kendini yalanladı — düzeltmenin amacı olan "dürüst sinyal" tam tersine
// döndü. Trend tek başına asla yeterli değildi: mutlak derinliğe kör bir
// yön ölçüsü, 44 bin ile 4 dosyayı aynı sayar.
//
// Neden 2000: canlı ölçümde sağlıklı tablolar 0-1 dosyada geziniyor
// (span_links 1, logs 0, profiles 1); kilitlenmiş olanlar 12.702 /
// 26.132 / 29.837. İki rejimin arası ÜÇ büyüklük mertebesi. 2000, sessiz
// eşiğin (100) 20 katı — en vahşi ingest patlamasının bile üstünde — ve
// gözlenen arızanın 6 kat altında; sayı o boşluğun ortasına kondu.
// Sağlıklı bir gönderici dosyayı saniyeler içinde boşaltır, yani 2000
// dosya "yoğun an" değil "gönderici basamıyor" demektir.
//
// Trend YOK OLMADI: bu tabanın ALTINDA hâlâ tek karar ölçüsü (aşağıdaki
// giriş kapısı) ve tabanın üstünde de neden metninde yön bilgisi kalır.
const distributedBacklogAlarm = 2000

// distributedRecoverSamples — degraded'dan ok'a dönmek için gereken
// ARDIŞIK iyileşme ölçümü (histerezis).
//
// 3 × distQueueRefreshEvery (30 sn) = 90 saniye kesintisiz iyileşme.
// Tek örnekle geri dönüş YOKTUR, çünkü flap'in kaynağı tam olarak tek
// bir örneğin tüm kararı taşımasıydı. Asimetri BİLEREK: arızaya giriş
// tek ölçümle (hızlı uyarı), çıkış üç ölçümle (yavaş rahatlama) olur —
// yanlış alarmın maliyeti, sessiz veri kaybının maliyetinden düşüktür.
const distributedRecoverSamples = 3

// DistributionQueueEntry — bir Distributed tablonun spool durumu, küme
// genelinde toplanmış (her düğüm × her hedef shard tek satıra iner).
type DistributionQueueEntry struct {
	Table string `json:"table"`
	// Files — gönderilmeyi bekleyen spool dosyası sayısı. ASIL SİNYAL.
	Files uint64 `json:"files"`
	Bytes uint64 `json:"bytes"`
	// BrokenFiles — CH'nin gönderemeyip KALICI olarak kenara koyduğu
	// dosyalar (.broken). Bunlar bir daha denenmez: gerçek veri kaybı.
	// Verdict'e GİRMEZ (aşağıdaki gerekçe), ama panelde görünür.
	BrokenFiles uint64 `json:"brokenFiles"`
	// ErrorCount — gönderim denemesi hataları. Sunucu açılışından beri
	// KÜMÜLATİF ve azalmaz; tek başına "şu an bozuk" demek değildir.
	ErrorCount uint64 `json:"errorCount"`
	// LastError — en çok hata almış hedefin son istisnası, 200 karaktere
	// kırpılmış. Teşhisin tamamı burada: 241 mi (bellek), 159 mu (timeout).
	LastError string `json:"lastError,omitempty"`
	// LastErrorAtNs (v0.9.1077) — o istisnanın ZAMANI. 2026-08-16 prod
	// olayının dersi: 16 gün önceki bir 241, tarihsiz basılınca CANLI bir
	// bellek krizi sanıldı ve operatör yaşamayan bir arızayı kovaladı.
	// Eski CH'de (last_exception_time kolonu yok) 0 kalır.
	LastErrorAtNs int64 `json:"lastErrorAtNs,omitempty"`
}

// DistributionQueue — tek bir ölçüm (küme geneli).
type DistributionQueue struct {
	// Measured — sorgu GERÇEKTEN koştu ve sonuç okundu mu?
	//
	// Fail-open dersi (v0.9.984, [[fail-open-silently-unapplies]]):
	// probe düştüğünde Files 0 kalır ve bu "kuyruk boş" ile aynı
	// GÖRÜNÜR. İkisi aynı şey değil — biri "ölçtüm, temiz", diğeri
	// "ölçemedim". Ayrım olmadan bu sürüm de kendini sessizce iptal
	// eder ve arıza sırasında tam da ölçemediği için yeşil görünürdü.
	Measured   bool   `json:"measured"`
	ProbeError string `json:"probeError,omitempty"`
	// Partial — küme geneli fan-out DÜŞTÜ, sayılar YALNIZ bu düğümün
	// spool'u (v0.9.986). Yaklaşıklık İTİRAF EDİLİR (DDLQueueHealth'in
	// StuckCountApprox emsali): sessiz bir kırpma "hepsi bu" diye okunur.
	//
	// Neden gerekli: fan-out tam da ölçmek istediğimiz arızada yavaşlıyor
	// (ÖLÇÜLDÜ: sağlıklıya yakınken 646 ms medyan, küme baskı altındayken
	// 2.6 sn, ağır çekişmede 14.3 sn ile bütçeyi aştı). Fallback olmasaydı
	// sinyal tam ihtiyaç anında körleşirdi — fail-open'ın kendini iptal
	// etmesinin ta kendisi.
	Partial bool `json:"partial,omitempty"`

	Tables []DistributionQueueEntry `json:"tables,omitempty"`

	// Küme geneli toplamlar (Tables'ın toplamı; UI tekrar toplamasın).
	Files       uint64 `json:"files"`
	Bytes       uint64 `json:"bytes"`
	BrokenFiles uint64 `json:"brokenFiles"`
	ErrorCount  uint64 `json:"errorCount"`

	Generated int64 `json:"generated"`
}

// distributionQueueSQL — okuma metnini üretir. Boş küme adı → "" döner,
// yani ÇAĞIRAN HİÇ SORGU ÇALIŞTIRMAZ (tek-düğümde sıfır ek maliyet).
// Ayrı fonksiyon çünkü iki dalın da şekli canlı küme olmadan pinlenebilsin.
//
// clusterAllReplicas, cluster() DEĞİL: spool HER DÜĞÜMÜN KENDİ DİSKİNDE
// durur (node-düzeyi durum, veri değil — çift sayım riski yok). cluster()
// her shard'dan tek replika okur ve 2×2'lik bir kümede spool'un yarısını
// görünmez yapardı — system.disks/asynchronous_metrics ile aynı operatör
// bulgusu (v0.9.454).
func distributionQueueSQL(clusterName string, hasLastAt bool) string {
	cn := strings.TrimSpace(clusterName)
	if cn == "" {
		return ""
	}
	// Bütçe 10 sn (v0.9.986, ÖLÇÜLEREK yükseltildi — v0.9.985'te 3 sn'ydi
	// ve canlı arızada yetmedi). Ölçüm, aynı arızalı küme: fan-out medyanı
	// 646 ms; küme baskı altındayken 2.6 sn; ağır çekişmede 14.3 sn.
	// Sayı uydurulmadı — okuma ZATEN asenkron (kimse beklemiyor) ve
	// tazeleme aralığı 30 sn, yani 10 sn'lik bir tavan hiçbir isteği
	// geciktirmeden çekişmeli anların ezici çoğunluğunu kapsar.
	return fmt.Sprintf(
		distributionQueueSelect(hasLastAt)+`
		FROM clusterAllReplicas('%s', system.distribution_queue)`+
			distributionQueueTail+`
		SETTINGS skip_unavailable_shards = 1, max_execution_time = 10`,
		strings.ReplaceAll(cn, "'", ""))
}

// distributionQueueLocalSQL — fan-out düştüğünde YALNIZ bu düğümün
// spool'u. Kısmi ama körlükten iyi: büyüyen bir yerel spool da büyüyen
// spool'dur ve teşhis (241/159) zaten yerel istisnada.
//
// Bütçe 3 sn: yerel okuma bellek-içi (ÖLÇÜLDÜ: 0.2 / 0.4 / 1.1 sn aynı
// baskı altında). Fan-out'un 10 sn'sinden sonra koşabilmesi için kısa.
func distributionQueueLocalSQL(hasLastAt bool) string {
	return distributionQueueSelect(hasLastAt) + `
		FROM system.distribution_queue` + distributionQueueTail + `
		SETTINGS max_execution_time = 3`
}

// İki dal aynı kolonları aynı sırada döndürmeli — tek scan döngüsü
// ikisini de okuyor. argMax(...) ikinci argümanı: boş istisnalar -1'e
// itilir ki "en çok hatalı hedef"in last_exception'ı BOŞSA bile dolu
// olan bir başkası seçilsin — aksi hâlde teşhis metni sessizce boş kalırdı.
// hasLastAt=false: eski CH'de last_exception_time kolonu yok — sabit
// toDateTime(0) seçilir ki iki sürümde de kolon sayısı ve scan döngüsü
// aynı kalsın (yaş etiketi sessizce kapanır, sorgu düşmez).
func distributionQueueSelect(hasLastAt bool) string {
	lastAt := "toDateTime(0)"
	if hasLastAt {
		// argMax anahtarı last_err ile AYNI ifade: zaman, seçilen
		// istisnanın zamanı olmalı — en derin tablonun değil.
		lastAt = `argMax(last_exception_time,
		           if(empty(last_exception), toInt64(-1), toInt64(error_count)))`
	}
	return `
		SELECT table,
		       toUInt64(sum(data_files))            AS files,
		       toUInt64(sum(data_compressed_bytes)) AS bytes,
		       toUInt64(sum(broken_data_files))     AS broken,
		       toUInt64(sum(error_count))           AS errs,
		       substring(argMax(last_exception,
		           if(empty(last_exception), toInt64(-1), toInt64(error_count))),
		         1, 200)                            AS last_err,
		       ` + lastAt + `                       AS last_err_at`
}

const distributionQueueTail = `
		GROUP BY table
		HAVING files > 0 OR broken > 0 OR errs > 0
		ORDER BY files DESC, table`

// distributionQueueHasLastAt — system.distribution_queue tablosunda
// last_exception_time kolonu var mı? (CH ~23.4+). Süreç ömründe BİR kez
// probelanır; eski CH'de yaş etiketi sessizce kapanır, ölçümün kendisi
// düşmez. hasOpGroupCol probe'unun deseni (maybeCloseRows dahil —
// v0.8.185 boot-panic disiplini).
func (s *Store) distributionQueueHasLastAt(ctx context.Context) bool {
	s.distQueueLastAtOnce.Do(func() {
		rows, err := s.conn.Query(ctx,
			`SELECT last_exception_time FROM system.distribution_queue LIMIT 1 SETTINGS max_execution_time = 3`)
		maybeCloseRows(rows, err)
		s.distQueueHasLastAt = err == nil
		if !s.distQueueHasLastAt {
			log.Printf("[chstore] system.distribution_queue.last_exception_time yok (eski CH?) — spool teşhisinde yaş etiketi kapalı: %v", err)
		}
	})
	return s.distQueueHasLastAt
}

// CollectDistributionQueue — tek round-trip spool ölçümü.
//
// nil = tek-düğüm kurulumu (sorgu ÇALIŞMADI, sinyal yok). Hata hâlinde
// nil DÖNMEZ: Measured=false + ProbeError ile döner, çünkü "ölçemedim"
// ile "temiz" birbirine karışmamalı.
//
// system.distribution_queue bellek-içi bir sistem tablosudur; maliyeti
// veri hacminden bağımsızdır. Ölçüm (2026-08-12, ARIZALI küme, query_log
// medyanı): 646 ms, tepe 1300 ms, 787 KB bellek, 44 satır. Bu, 5 saniyede
// bir dönen /api/health için SENKRON çalıştırılamayacak kadar pahalı —
// çağıran katman (internal/api) asenkron cache'ler.
func (s *Store) CollectDistributionQueue(ctx context.Context) *DistributionQueue {
	hasLastAt := s.distributionQueueHasLastAt(ctx)
	q := distributionQueueSQL(s.cfg.ClusterName, hasLastAt)
	if q == "" {
		return nil // tek düğüm: Distributed tablo yok, spool yok, sorgu YOK
	}
	out := &DistributionQueue{Generated: time.Now().UnixNano()}
	if err := s.scanDistributionQueue(ctx, q, out); err == nil {
		out.Measured = true
		return out
	} else {
		out.ProbeError = err.Error()
	}
	// Fan-out düştü — YEREL spool'a düş (v0.9.986). Fan-out tam da
	// ölçmek istediğimiz arızada yavaşlıyor; fallback olmasaydı sinyal
	// ihtiyaç anında körleşirdi. Kısmi sonuç Partial ile İTİRAF edilir.
	local := &DistributionQueue{Generated: out.Generated, Partial: true,
		ProbeError: "küme geneli okuma düştü, yalnız bu düğüm: " + out.ProbeError}
	if err := s.scanDistributionQueue(ctx, distributionQueueLocalSQL(hasLastAt), local); err != nil {
		// İkisi de düştü: "ölçemedim" hâli korunur, sıfır İDDİA EDİLMEZ.
		out.ProbeError += " · yerel okuma da düştü: " + err.Error()
		return out
	}
	local.Measured = true
	return local
}

// scanDistributionQueue — iki dalın ortak okuma döngüsü. Toplamları
// out'a yazar; hata hâlinde out kısmen dolmuş olabilir, o yüzden
// çağıran Measured'ı yalnız nil hata için işaretler.
func (s *Store) scanDistributionQueue(ctx context.Context, q string, out *DistributionQueue) error {
	rows, err := s.conn.Query(ctx, q)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var e DistributionQueueEntry
		var lastAt time.Time
		if serr := rows.Scan(&e.Table, &e.Files, &e.Bytes,
			&e.BrokenFiles, &e.ErrorCount, &e.LastError, &lastAt); serr != nil {
			return serr
		}
		// toDateTime(0) (eski CH dalı) ve "hiç istisna yok" ikisi de
		// epoch'tur — ikisinde de yaş İDDİA EDİLMEZ.
		if lastAt.Unix() > 0 {
			e.LastErrorAtNs = lastAt.UnixNano()
		}
		out.Tables = append(out.Tables, e)
		out.Files += e.Files
		out.Bytes += e.Bytes
		out.BrokenFiles += e.BrokenFiles
		out.ErrorCount += e.ErrorCount
	}
	return rows.Err()
}

// DistributionState — spool sinyalinin HİSTEREZİS durumu. Saf verdict'in
// hem girdisi hem çıktısı; çağıran katman (internal/api) taşır.
//
// Neden durum gerekiyor (v0.9.987): durumsuz bir karar, her ölçümde
// sıfırdan hüküm verir ve tek bir örneğin gürültüsü tüm sinyali çevirir.
// Canlı flap 44.320 → 44.318 (iki dosya) ile "degraded → ok" oldu.
// Süregelen bir arızada sinyalin kendini yalanlamaması için karar,
// önceki kararı BİLMEK zorunda.
type DistributionState struct {
	// Degraded — sağlık ucunun gördüğü hâl. YAPIŞKAN: bir kez kurulunca
	// distributedRecoverSamples ardışık iyileşme olmadan düşmez.
	Degraded bool
	// Detail — operatöre gösterilen tek satırlık neden. Boş = sinyal yok.
	Detail string
	// Recovering — ardışık iyileşme ölçümü sayacı. Arada TEK bir kötü
	// (ya da ölçülemeyen) örnek sayacı sıfırlar.
	Recovering int
}

// DistributionVerdict — SAF karar: spool sağlık sinyalini bozuyor mu?
//
// Girdi: önceki karar durumu + iki ardışık ölçüm. Dönüş: yeni durum.
//
// Üç katmanlı karar, bu SIRAYLA:
//
//  1. MUTLAK TABAN — Files >= distributedBacklogAlarm ise trend'e
//     BAKILMADAN degraded. 44 bin dosya, iki dosya eridi diye "ok"
//     olamaz (v0.9.987 flap'inin ta kendisi).
//  2. HİSTEREZİS — degraded'dan çıkış, tabanın altına inmiş VE ardışık
//     distributedRecoverSamples ölçüm iyileşmiş olmayı ister.
//  3. GİRİŞ KAPISI — taban ALTINDA trend hâlâ tek ölçü: `error_count`
//     sunucu açılışından beri kümülatiftir ve AZALMAZ, yani üç gün
//     önceki bir blip'i "şu an bozuk" diye okumak sinyali yakardı.
//
// nil cur (tek düğüm) → sinyal SIFIRLANIR: o kurulumda spool kavramı yok.
//
// Measured=false (probe düştü) → "ÖLÇEMEDİM" hâli. Bu ASLA "ok" DEĞİLDİR:
// yerleşik bir arızayı temizlemez ve iyileşme SAYILMAZ. Yeni bir arıza da
// iddia etmez — dürüst etiket gövdedeki `distributed_spool_measured`
// alanıdır (v0.9.984 fail-open dersi: bir düzeltmenin kendini sessizce
// iptal edebildiği yer tam olarak burasıdır).
//
// BrokenFiles bilerek verdict DIŞINDA: kalıcı ve yapışkan bir sayaçtır,
// günler önceki 1 bozuk dosya health'i sonsuza dek "degraded" yapardı.
// Panelde görünür, alarmı sürüklemez.
func DistributionVerdict(st DistributionState, cur, prev *DistributionQueue) DistributionState {
	if cur == nil {
		return DistributionState{}
	}
	if !cur.Measured {
		// "Ölçemedim" ≠ "temiz" ve ≠ "iyileşiyor".
		st.Recovering = 0
		why := oneLine(cur.ProbeError, 160)
		if why == "" {
			why = "bilinmeyen hata"
		}
		if st.Degraded {
			st.Detail = "Spool ÖLÇÜLEMEDİ (" + why +
				"); son bilinen arıza hâli KORUNUYOR — ölçememek iyileşme değildir."
		} else {
			st.Detail = "Spool ölçülemedi (" + why +
				"); derinlik BİLİNMİYOR — 'temiz' anlamına GELMEZ."
		}
		return st
	}

	depth := fmt.Sprintf("Distributed spool %s dosya (%s)",
		fmtCount(cur.Files), humanBytes(cur.Bytes))
	if w := worstDistributionTable(cur.Tables); w != "" {
		depth += ", en derini " + w
	}
	if cur.Partial {
		depth += " [yalnız bu düğüm — küme geneli okuma düştü]"
	}

	// ── (1) MUTLAK TABAN: yön ne olursa olsun arıza ──────────────
	if cur.Files >= distributedBacklogAlarm {
		st.Degraded = true
		st.Recovering = 0
		st.Detail = depth + distributionTrendNote(cur, prev) +
			". ClickHouse INSERT'i kabul edip diske spool'luyor ama arka plan " +
			"göndericisi *_local'a basamıyor: uygulama 'yazdım' sanıyor, veri " +
			"inmiyor." + lastErrorHint(cur.Tables, cur.Generated)
		return st
	}

	// ── (2) HİSTEREZİS: taban altındayız, çıkış doğrulanır ───────
	if st.Degraded {
		if distributionImproving(cur, prev) {
			st.Recovering++
		} else {
			st.Recovering = 0
		}
		if st.Recovering < distributedRecoverSamples {
			st.Detail = depth + fmt.Sprintf("; mutlak tabanın (%s dosya) altına "+
				"indi ama iyileşme HENÜZ DOĞRULANMADI (%d/%d ardışık ölçüm) — "+
				"degraded korunuyor.", fmtCount(distributedBacklogAlarm),
				st.Recovering, distributedRecoverSamples)
			return st
		}
		// Histerezis doldu: sinyal temizlenir ve karar aşağıdaki normal
		// giriş kapısına düşer (tabanın altındaki trend mantığı).
		st = DistributionState{}
	}

	// ── (3) GİRİŞ KAPISI: taban altında trend tek ölçü ───────────
	if cur.Files < distributedBacklogFloor {
		// Boş ya da normal çalkantı — sinyal yok.
		return DistributionState{}
	}
	switch {
	// KAPSAM KİLİDİ (v0.9.986): küme geneli bir ölçümle YALNIZ-BU-DÜĞÜM
	// ölçümü asla kıyaslanmaz. Canlı sayılarla 41.274 (küme) → 19.020
	// (yerel) "drene oluyor" diye okunurdu — süregelen bir arızada
	// SAHTE BİR RAHATLAMA. Farklı kapsam = trend yok, ilk ölçüm gibi
	// davran. (v0.5.187 ile aynı sınıf hata: kıyaslanamaz iki şeyi
	// kıyaslamak.) Mutlak taban üstünde bu kilit zararsızdır — orada
	// karar zaten trend'e bakmıyor.
	case prev == nil || !prev.Measured || prev.Partial != cur.Partial:
		// İlk ölçüm: derinlik biliniyor, YÖN bilinmiyor. "degraded" iddia
		// etmek yerine derinliği bildir — bir sonraki tur trendi getirir.
		return DistributionState{Detail: depth + "; trend henüz ölçülmedi."}
	case cur.Files > prev.Files:
		return DistributionState{Degraded: true, Detail: depth +
			fmt.Sprintf("; BÜYÜYOR (%s → %s)", fmtCount(prev.Files), fmtCount(cur.Files)) +
			distributionDrainNote(cur, prev) +
			". ClickHouse INSERT'i kabul edip diske spool'luyor ama arka plan " +
			"göndericisi *_local'a basamıyor: uygulama 'yazdım' sanıyor, veri " +
			"inmiyor." + lastErrorHint(cur.Tables, cur.Generated)}
	case cur.Files < prev.Files:
		return DistributionState{Detail: depth +
			fmt.Sprintf("; drene oluyor (%s → %s)", fmtCount(prev.Files), fmtCount(cur.Files)) +
			distributionDrainNote(cur, prev) + "."}
	case cur.ErrorCount > 0:
		return DistributionState{Degraded: true, Detail: depth +
			fmt.Sprintf("; SABİT (%s) ve gönderim hatası var (%s hata). Kuyruk ne "+
				"büyüyor ne eriyor — gönderici takılı.",
				fmtCount(cur.Files), fmtCount(cur.ErrorCount)) + lastErrorHint(cur.Tables, cur.Generated)}
	default:
		return DistributionState{Detail: depth + "; sabit, gönderim hatası yok."}
	}
}

// distributionTrendNote — mutlak taban üstündeki karara EKLENEN yön
// bilgisi. Kararı DEĞİŞTİRMEZ (orada hüküm zaten kesin), yalnız operatöre
// "eriyor mu" sorusunun cevabını verir. Kıyaslanamaz kapsam → yön yok.
func distributionTrendNote(cur, prev *DistributionQueue) string {
	if prev == nil || !prev.Measured || prev.Partial != cur.Partial {
		return "; MUTLAK TABANIN ÜSTÜNDE (yön henüz ölçülmedi)"
	}
	switch {
	case cur.Files > prev.Files:
		return fmt.Sprintf("; BÜYÜYOR (%s → %s)", fmtCount(prev.Files), fmtCount(cur.Files)) +
			distributionDrainNote(cur, prev)
	case cur.Files < prev.Files:
		// Flap'in kaynağı buydu: 44.320 → 44.318 "drene oluyor" sayılıp
		// sinyali OK'a çeviriyordu. Artık yalnızca bir NOT.
		//
		// v0.9.1038 — ETA tam da BURADA en çok işe yarıyor: "azalıyor"
		// tek başına rahatlatıcı okunuyordu; "≥2 gün 20 sa" o rahatlamayı
		// hak edilmiş hâle getiriyor (ya da yıkıyor).
		return fmt.Sprintf("; azalıyor (%s → %s) ama hâlâ mutlak tabanın (%s dosya) ÜSTÜNDE",
			fmtCount(prev.Files), fmtCount(cur.Files), fmtCount(distributedBacklogAlarm)) +
			distributionDrainNote(cur, prev)
	default:
		return fmt.Sprintf("; SABİT (%s)", fmtCount(cur.Files))
	}
}

// spoolETACeilingDays — bunun ötesindeki bir tahmin sayı olmaktan çıkar.
//
// Drenaj hızı sıfıra yaklaştıkça ETA sonsuza gider: 44 bin dosyalık bir
// spool'da iki ölçüm arasında 1 dosya erimesi "≥14 yıl" gibi teknik
// olarak doğru ama OKUNMAZ bir sayı üretir. Böyle bir sayı operatöre
// hiçbir şey söylemez; söylenmesi gereken şey "bu hızla kapanmıyor".
const spoolETACeilingDays = 99

// distributionDrainNote — kıyaslanabilir iki ardışık ölçümden NET drenaj
// hızı ve o hızla kalan süre. Boş dönüş = "yön/hız söylenemez".
//
// v0.9.1038 — operatörün spool arızasında sorduğu ikinci soru "eriyor mu"
// DEĞİL (onu v0.9.987 zaten söylüyor), "NE ZAMAN biter". 2026-08-12
// arızasında bu soru elle hesaplanmıştı: 18.9k dosya × ~5 sn/dosya ≈ 10
// saat. Aynı aritmetik artık sinyalin kendisinde.
//
// KAPSAM KİLİDİ AYNEN (v0.9.986 / [[fallback-must-carry-scope]]): küme
// geneli bir ölçümle yalnız-bu-düğüm ölçümü kıyaslanmaz. 41.274 (küme)
// → 19.020 (yerel) farkı "saniyede 2000 dosya eriyor, 10 sn'de biter"
// gibi tamamen uydurma bir ETA üretirdi — v0.5.187 sınıfı hata.
//
// Tek örneklemde ETA YOKTUR: hız iki noktadan çıkar, bir noktadan değil.
// Bu dal mevcut "yön henüz ölçülmedi" dilini olduğu gibi bırakır.
//
// Hız NET'tir (gelen − giden): ölçülen tek şey derinlik farkı. Bu bir
// eksiklik değil, doğru büyüklük — operatörün sorduğu "kuyruk ne zaman
// kapanır" sorusunun cevabı net hızdır, brüt gönderim hızı değil.
func distributionDrainNote(cur, prev *DistributionQueue) string {
	if cur == nil || prev == nil || !cur.Measured || !prev.Measured ||
		prev.Partial != cur.Partial {
		return ""
	}
	// Generated iki ölçümün gerçek zaman damgası. Eşit ya da geri giden
	// bir saat (aynı örnek, saat ayarı) hız üretemez — sessizce boş.
	elapsed := float64(cur.Generated-prev.Generated) / 1e9
	if elapsed <= 0 {
		return ""
	}
	// + = eriyor, − = birikiyor. int64'e ÇEVİRMEDEN uint64 çıkarması
	// yapılamaz: büyüyen kuyrukta prev-cur sarmalanıp devasa bir "erime"
	// üretirdi.
	delta := float64(prev.Files) - float64(cur.Files)
	switch {
	case delta > 0:
		if cur.Files == 0 {
			return "" // zaten boş; "ne zaman biter" sorusu yok
		}
		perSec := delta / elapsed
		return "; bu hızla boşalması ≥" +
			fmtSpoolETA(float64(cur.Files)/perSec) + " sürer"
	case delta < 0:
		return "; birikiyor (" + fmtSpoolRate(-delta/elapsed) + ")"
	default:
		return ""
	}
}

// fmtSpoolETA — saniye → okunur süre. İKİ birim, daha fazlası değil:
// "2 gün 20 sa" yeterli, "2 gün 20 sa 13 dk 4 sn" tahminin taşımadığı
// bir kesinlik iddia eder.
//
// Tavanın üstünde SAYI VERMEZ (bkz. spoolETACeilingDays).
func fmtSpoolETA(sec float64) string {
	if sec <= 0 {
		return "0 sn"
	}
	if sec >= float64(spoolETACeilingDays)*86400 {
		return fmt.Sprintf("%d gün (bu hızla pratikte kapanmıyor)", spoolETACeilingDays)
	}
	total := int64(sec + 0.5)
	switch {
	case total < 60:
		return fmt.Sprintf("%d sn", total)
	case total < 3600:
		m, s := total/60, total%60
		if s == 0 {
			return fmt.Sprintf("%d dk", m)
		}
		return fmt.Sprintf("%d dk %d sn", m, s)
	case total < 86400:
		h, m := total/3600, (total%3600)/60
		if m == 0 {
			return fmt.Sprintf("%d sa", h)
		}
		return fmt.Sprintf("%d sa %d dk", h, m)
	default:
		d, h := total/86400, (total%86400)/3600
		if h == 0 {
			return fmt.Sprintf("%d gün", d)
		}
		return fmt.Sprintf("%d gün %d sa", d, h)
	}
}

// fmtSpoolRate — dosya/saniye → okunur birikme hızı.
//
// Birim SEÇİLİR, sabitlenmez: dakikada 0.3 dosya "+0.3 dosya/dk" olarak
// okunmaz (yuvarlanınca 0 görünür ve "birikmiyor" gibi okunur), saatte
// 18 dosya olarak okunur. v0.6.36 dersi: değer+birim taşıyan her şablon
// HER BİRİMİ test etmek zorunda.
func fmtSpoolRate(perSec float64) string {
	perMin := perSec * 60
	if perMin >= 1 {
		return "+" + trimRate(perMin) + " dosya/dk"
	}
	return "+" + trimRate(perSec*3600) + " dosya/sa"
}

// trimRate — 10 ve üstünde tam sayı, altında tek ondalık. Sağlık
// metninde "+109.4 dosya/dk"nın ondalığı gürültü, "+0.4"unki bilgi.
//
// problem.go'daki trimFloat DEĞİL: o tam kesinliği yazıyor
// (strconv 'f', -1) çünkü orada sayı bir EŞİK — operatörün girdiği
// değer aynen görünmeli. Burada sayı bir ÖLÇÜM ve kesinliği sahte.
func trimRate(v float64) string {
	if v >= 10 {
		return fmt.Sprintf("%.0f", v)
	}
	return strings.TrimSuffix(fmt.Sprintf("%.1f", v), ".0")
}

// distributionImproving — bu ölçüm bir İYİLEŞME adımı sayılır mı?
//
// Ya sessiz eşiğin altına inmiş, ya da KIYASLANABİLİR bir önceki ölçüme
// göre küçülmüş olmalı. Kıyaslanamayan (kapsam değişmiş ya da ölçülememiş)
// önceki örnek iyileşme SAYILMAZ — v0.9.986 kapsam kilidinin histerezis
// karşılığı: küme→yerel daralması "eriyor" diye okunamaz.
func distributionImproving(cur, prev *DistributionQueue) bool {
	if cur.Files < distributedBacklogFloor {
		return true
	}
	if prev == nil || !prev.Measured || prev.Partial != cur.Partial {
		return false
	}
	return cur.Files < prev.Files
}

// worstDistributionTable — en derin tablonun "ad (N dosya)" özeti.
// Tables sorguda files DESC sıralı gelir ama sıraya GÜVENMEZ: saf
// fonksiyon, testte elle kurulan girdilerle de doğru cevap vermeli.
func worstDistributionTable(tables []DistributionQueueEntry) string {
	var top DistributionQueueEntry
	for _, t := range tables {
		if t.Files > top.Files {
			top = t
		}
	}
	if top.Table == "" {
		return ""
	}
	return fmt.Sprintf("%s (%s dosya)", top.Table, fmtCount(top.Files))
}

// lastErrorHint — arızalı tablonun son istisnasını neden metnine ekler.
// Operatörün ilk sorusu "neden inmiyor"; cevabı zaten elimizde.
//
// nowNs (v0.9.1077): istisnanın YAŞI da basılır. 2026-08-16 prod olayı:
// 16 gün önceki bir 241 tarihsiz basılınca canlı bir bellek krizi
// sanıldı — oysa gönderim günlerdir hatasızdı, sorun saf drenaj hızıydı.
// Çağıran ölçümün kendi Generated damgasını verir (fonksiyon saf kalır);
// zaman bilgisi yoksa (eski CH / boş) etiket eskisi gibi tarihsizdir.
func lastErrorHint(tables []DistributionQueueEntry, nowNs int64) string {
	var top DistributionQueueEntry
	for _, t := range tables {
		if t.Files > top.Files && t.LastError != "" {
			top = t
		}
	}
	if top.LastError == "" {
		return ""
	}
	age := ""
	if top.LastErrorAtNs > 0 && nowNs > top.LastErrorAtNs {
		age = " (" + fmtAgeShort(nowNs-top.LastErrorAtNs) + " önce)"
	}
	return " Son hata" + age + ": " + oneLine(top.LastError, 160)
}

// fmtAgeShort — tek birimli kompakt yaş (saf, HER birim tablo testli —
// unit-mixing dersi: off-axis dal sessizce bozulur). Sınırlar kaba
// bilinçli: bu bir teşhis etiketi, kronometre değil.
func fmtAgeShort(deltaNs int64) string {
	switch d := time.Duration(deltaNs); {
	case d < 90*time.Second:
		return "az önce"
	case d < 90*time.Minute:
		return fmt.Sprintf("%d dk", int(d.Minutes()+0.5))
	case d < 48*time.Hour:
		return fmt.Sprintf("%d sa", int(d.Hours()+0.5))
	default:
		return fmt.Sprintf("%d gün", int(d.Hours()/24+0.5))
	}
}

// oneLine — çok satırlı bir CH istisnasını tek satıra indirir ve RUNE
// sınırında keser. İki şart da zorunlu: bu metin /api/health JSON'una
// giriyor (satır sonu taşıyamaz) ve CH istisnaları UTF-8 taşıyabilir —
// bayt sınırında kesmek geçersiz UTF-8 üretirdi.
func oneLine(s string, max int) string {
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > max {
		return string(r[:max]) + "…"
	}
	return s
}

// fmtCount — 26132 → "26.132" (binlik ayraç). Saf, log/JSON metinleri için.
func fmtCount(n uint64) string {
	s := fmt.Sprintf("%d", n)
	if len(s) <= 3 {
		return s
	}
	var b strings.Builder
	pre := len(s) % 3
	if pre > 0 {
		b.WriteString(s[:pre])
	}
	for i := pre; i < len(s); i += 3 {
		if b.Len() > 0 {
			b.WriteByte('.')
		}
		b.WriteString(s[i : i+3])
	}
	return b.String()
}
