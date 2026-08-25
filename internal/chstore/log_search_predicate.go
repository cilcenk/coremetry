package chstore

import "strings"

// log_search_predicate.go — /logs serbest-metin aramasının SQL yüklemi
// (v0.9.1385).
//
// Bu dosyanın var olma sebebi bir AYRIŞMA. Aynı ekranın iki yarısı, aynı
// arama metnini iki farklı yüklemle soruyordu:
//
//   liste     (GetLogs → logsWhere)          body LIKE '%<metin>%'
//   histogram (logstore/clickhouse.go)       multiSearchAnyCaseInsensitive
//                                            + isBareHexID kolon dalı
//
// Ölçülen sonuç: `search=Account-Service` → tablo 0 satır, histogram 210.
// Operatör DOLU bir histogramın altında BOŞ bir tablo görüyor ve hangisine
// inanacağını bilmiyor.
//
// v0.9.1385 iki ayrışmayı kapatıyor; ÜÇÜNCÜSÜ (harf duyarlılığı) bilinçli
// olarak AÇIK bırakıldı — gerekçesi aşağıda.

// IsBareHexID — serbest metin çıplak bir trace/span id'si mi (32 ya da 16
// hex, çevresinde boşluk olabilir)?
//
// v0.8.521 (operatör bildirimi): Logs'un Search kutusuna YAPIŞTIRILAN bir
// id, id'yi gövdeye YAZMAYAN kurulumlarda da bulunsun diye kolon
// eşleşmesine yükseltilir.
//
// Gövde v0.9.1385'te logstore'dan buraya taşındı: yüklem artık liste
// yolunda da gerekiyor ve o yol burada. logstore chstore'u import ettiği
// için ters yön mümkün değildi; ikinci bir kopya yazmak ise bu sözleşmenin
// iki yerde ıraksamasının garantisiydi — zaten bir kez ıraksadı.
func IsBareHexID(q string) bool {
	q = strings.TrimSpace(q)
	if len(q) != 32 && len(q) != 16 {
		return false
	}
	for _, c := range q {
		switch {
		case c >= '0' && c <= '9', c >= 'a' && c <= 'f', c >= 'A' && c <= 'F':
		default:
			return false
		}
	}
	return true
}

// v0.9.1385'in `escapeLikeNeedle`'ı v0.10.2'de SİLİNDİ. Gerekçesi
// LIKE'ın `%`/`_` jokerleriydi; yüklem artık LIKE değil ve
// `multiSearchAnyCaseInsensitive` iğneyi LİTERAL alıyor, yani kaçışlanacak
// bir joker yok. Kusur ortadan kalktı, korunması gereken bir davranış
// kalmadı — kaçış kodunu bırakmak, çözülmüş bir sorunun bakımını
// sürdürmek olurdu.

// logSearchConjunct — arama metninin WHERE parçası ve argümanları.
//
// ── HARF DUYARLILIĞI: v0.10.2'de ÖLÇÜLEREK KAPANDI ──────────────────────
//
// v0.9.1385 bu kararı ERTELEDİ ve gerekçesi şuydu: "`body` üzerinde
// tokenbf_v1 atlama indeksi var; listeyi histograma hizalamak (duyarsız
// yapmak) en sıcak log yolunda indeksi kaybettirebilir." Ölçüm bu
// gerekçeyi ÇÜRÜTTÜ.
//
// Canlı CH 24.8.14, `EXPLAIN indexes=1` (planlayıcının kendi beyanı,
// zamanlama değil):
//
//	hasToken(body, <yok>)                → Granules 0/8   ← TEK etkili
//	body LIKE '%<yok>%'                  → Granules 7/7   ← budamıyor
//	multiSearchAny(body, [<yok>])        → Granules 6/6   ← budamıyor
//	multiSearchAnyCaseInsensitive(...)   → Skip bölümü HİÇ YOK
//
// Yani liste yolu indeksten ZATEN yararlanmıyordu: kaybedilecek bir şey
// yoktu. `hasToken` etkili ama farklı semantik (tam token, alt-dize değil)
// — drop-in değil, ayrı bir dilim.
//
// query_log medyanı, beşer koşum (tek ad-hoc zamanlama yalan söyler):
//
//	LIKE          → 93 ms · 8326 satır · 667 KiB · CPU 24.5 ms
//	msaCI         → 96 ms · 8326 satır · 667 KiB · CPU 17.3 ms
//
// Aynı I/O, duvar saati gürültü içinde, CPU DAHA UCUZ — ClickHouse'un
// SIMD'li çoklu-desen araması `%…%` eşleştiricisinden verimli.
//
// ⚠ Ölçüm LOKAL fixture üzerinde (25 MiB · 107 granül · 8326 satır).
// İndeks bulguları planlayıcı-yapısal, yani ölçekten bağımsız; CPU oranı
// satır-başı iş olduğu için kabaca doğrusal ölçeklenmeli. Ama prod
// ölçeğinde ÖLÇÜLMEDİ.
//
// Sonuç: liste artık histogramla AYNI yüklemi kullanıyor. Operatörün
// gördüğü "dolu histogramın altında boş tablo" çelişkisi kapandı; arama
// da harf duyarsız oldu (sonuç kümesi genişler — kasıtlı).
func logSearchConjunct(search string) (string, []any) {
	if IsBareHexID(search) {
		// v0.8.521 sözleşmesi. Liste yolunda YOKTU — yani "Search'e
		// trace id yapıştır, bulsun" ekranın yalnız yarısında çalışıyordu:
		// histogram sayıyordu, tablo göstermiyordu.
		id := strings.ToLower(strings.TrimSpace(search))
		return "(multiSearchAnyCaseInsensitive(body, [?]) OR trace_id = ? OR span_id = ?)",
			[]any{search, id, id}
	}
	return "multiSearchAnyCaseInsensitive(body, [?])", []any{search}
}
