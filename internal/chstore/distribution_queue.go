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
func distributionQueueSQL(clusterName string) string {
	cn := strings.TrimSpace(clusterName)
	if cn == "" {
		return ""
	}
	// argMax(...) ikinci argümanı: boş istisnalar -1'e itilir ki "en çok
	// hatalı hedef"in last_exception'ı BOŞSA bile dolu olan bir başkası
	// seçilsin — aksi hâlde teşhis metni sessizce boş kalırdı.
	return fmt.Sprintf(`
		SELECT table,
		       toUInt64(sum(data_files))            AS files,
		       toUInt64(sum(data_compressed_bytes)) AS bytes,
		       toUInt64(sum(broken_data_files))     AS broken,
		       toUInt64(sum(error_count))           AS errs,
		       substring(argMax(last_exception,
		           if(empty(last_exception), toInt64(-1), toInt64(error_count))),
		         1, 200)                            AS last_err
		FROM clusterAllReplicas('%s', system.distribution_queue)
		GROUP BY table
		HAVING files > 0 OR broken > 0 OR errs > 0
		ORDER BY files DESC, table
		SETTINGS skip_unavailable_shards = 1, max_execution_time = 3`,
		strings.ReplaceAll(cn, "'", ""))
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
	q := distributionQueueSQL(s.cfg.ClusterName)
	if q == "" {
		return nil // tek düğüm: Distributed tablo yok, spool yok, sorgu YOK
	}
	out := &DistributionQueue{Generated: time.Now().UnixNano()}
	rows, err := s.conn.Query(ctx, q)
	if err != nil {
		out.ProbeError = err.Error()
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var e DistributionQueueEntry
		if serr := rows.Scan(&e.Table, &e.Files, &e.Bytes,
			&e.BrokenFiles, &e.ErrorCount, &e.LastError); serr != nil {
			out.ProbeError = serr.Error()
			return out
		}
		out.Tables = append(out.Tables, e)
		out.Files += e.Files
		out.Bytes += e.Bytes
		out.BrokenFiles += e.BrokenFiles
		out.ErrorCount += e.ErrorCount
	}
	if err := rows.Err(); err != nil {
		out.ProbeError = err.Error()
		return out
	}
	out.Measured = true
	return out
}

// DistributionVerdict — SAF karar: spool sağlık sinyalini bozuyor mu?
//
// Girdi iki ARDIŞIK ölçüm; dönüş (degraded, kısa neden). Neden trend
// gerekiyor: `error_count` sunucu açılışından beri kümülatiftir ve
// AZALMAZ — üç gün önceki tek bir blip'i "şu an bozuk" diye okumak
// sinyali kalıcı olarak yakardı. Derinlik de tek başına yeterli değil:
// yoğun bir kümede anlık spool derinliği normaldir. ARIZA = derinliğin
// EŞİK ÜSTÜNDE ve DÜŞMÜYOR olması.
//
// nil cur (tek düğüm) ya da Measured=false (probe düştü) → sinyal YOK:
// iddia edilmeyen bir teşhis, uydurulan bir teşhisten iyidir.
//
// BrokenFiles bilerek verdict DIŞINDA: kalıcı ve yapışkan bir sayaçtır,
// günler önceki 1 bozuk dosya health'i sonsuza dek "degraded" yapardı.
// Panelde görünür, alarmı sürüklemez.
func DistributionVerdict(cur, prev *DistributionQueue) (bool, string) {
	if cur == nil || !cur.Measured {
		return false, ""
	}
	if cur.Files < distributedBacklogFloor {
		// Boş ya da normal çalkantı — sinyal yok.
		return false, ""
	}
	depth := fmt.Sprintf("Distributed spool %s dosya (%s)",
		fmtCount(cur.Files), humanBytes(cur.Bytes))
	if w := worstDistributionTable(cur.Tables); w != "" {
		depth += ", en derini " + w
	}
	switch {
	case prev == nil || !prev.Measured:
		// İlk ölçüm: derinlik biliniyor, YÖN bilinmiyor. "degraded" iddia
		// etmek yerine derinliği bildir — bir sonraki tur trendi getirir.
		return false, depth + "; trend henüz ölçülmedi."
	case cur.Files > prev.Files:
		return true, depth + fmt.Sprintf("; BÜYÜYOR (%s → %s). ClickHouse "+
			"INSERT'i kabul edip diske spool'luyor ama arka plan göndericisi "+
			"*_local'a basamıyor: uygulama 'yazdım' sanıyor, veri inmiyor.",
			fmtCount(prev.Files), fmtCount(cur.Files)) + lastErrorHint(cur.Tables)
	case cur.Files < prev.Files:
		return false, depth + fmt.Sprintf("; drene oluyor (%s → %s).",
			fmtCount(prev.Files), fmtCount(cur.Files))
	case cur.ErrorCount > 0:
		return true, depth + fmt.Sprintf("; SABİT (%s) ve gönderim hatası "+
			"var (%s hata). Kuyruk ne büyüyor ne eriyor — gönderici takılı.",
			fmtCount(cur.Files), fmtCount(cur.ErrorCount)) + lastErrorHint(cur.Tables)
	default:
		return false, depth + "; sabit, gönderim hatası yok."
	}
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
func lastErrorHint(tables []DistributionQueueEntry) string {
	var top DistributionQueueEntry
	for _, t := range tables {
		if t.Files > top.Files && t.LastError != "" {
			top = t
		}
	}
	if top.LastError == "" {
		return ""
	}
	msg := top.LastError
	// Tek satıra indir: bu metin /api/health JSON'una giriyor.
	msg = strings.Join(strings.Fields(msg), " ")
	if len(msg) > 160 {
		// Rune sınırında kes — CH istisnaları UTF-8 taşıyabilir.
		r := []rune(msg)
		if len(r) > 160 {
			msg = string(r[:160]) + "…"
		}
	}
	return " Son hata: " + msg
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
