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
func distributionQueueSQL(clusterName string) string {
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
		distributionQueueSelect+`
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
func distributionQueueLocalSQL() string {
	return distributionQueueSelect + `
		FROM system.distribution_queue` + distributionQueueTail + `
		SETTINGS max_execution_time = 3`
}

// İki dal aynı kolonları aynı sırada döndürmeli — tek scan döngüsü
// ikisini de okuyor. argMax(...) ikinci argümanı: boş istisnalar -1'e
// itilir ki "en çok hatalı hedef"in last_exception'ı BOŞSA bile dolu
// olan bir başkası seçilsin — aksi hâlde teşhis metni sessizce boş kalırdı.
const distributionQueueSelect = `
		SELECT table,
		       toUInt64(sum(data_files))            AS files,
		       toUInt64(sum(data_compressed_bytes)) AS bytes,
		       toUInt64(sum(broken_data_files))     AS broken,
		       toUInt64(sum(error_count))           AS errs,
		       substring(argMax(last_exception,
		           if(empty(last_exception), toInt64(-1), toInt64(error_count))),
		         1, 200)                            AS last_err`

const distributionQueueTail = `
		GROUP BY table
		HAVING files > 0 OR broken > 0 OR errs > 0
		ORDER BY files DESC, table`

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
	if err := s.scanDistributionQueue(ctx, distributionQueueLocalSQL(), local); err != nil {
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
		if serr := rows.Scan(&e.Table, &e.Files, &e.Bytes,
			&e.BrokenFiles, &e.ErrorCount, &e.LastError); serr != nil {
			return serr
		}
		out.Tables = append(out.Tables, e)
		out.Files += e.Files
		out.Bytes += e.Bytes
		out.BrokenFiles += e.BrokenFiles
		out.ErrorCount += e.ErrorCount
	}
	return rows.Err()
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
	if cur.Partial {
		depth += " [yalnız bu düğüm — küme geneli okuma düştü]"
	}
	switch {
	// KAPSAM KİLİDİ (v0.9.986): küme geneli bir ölçümle YALNIZ-BU-DÜĞÜM
	// ölçümü asla kıyaslanmaz. Canlı sayılarla 41.274 (küme) → 19.020
	// (yerel) "drene oluyor" diye okunurdu — süregelen bir arızada
	// SAHTE BİR RAHATLAMA. Farklı kapsam = trend yok, ilk ölçüm gibi
	// davran. (v0.5.187 ile aynı sınıf hata: kıyaslanamaz iki şeyi
	// kıyaslamak.)
	case prev == nil || !prev.Measured || prev.Partial != cur.Partial:
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
