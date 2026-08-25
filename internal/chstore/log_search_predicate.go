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

// escapeLikeNeedle — operatörün yazdığı metni LIKE JOKERİNE dönüşmekten
// korur.
//
// `body LIKE '%' || <metin> || '%'` kalıbında metnin içindeki `%` ve `_`
// SQL jokeridir. Yani `50%` araması "50 ile başlayan her şey", `a_b`
// araması "a, herhangi bir karakter, b" demek oluyordu — operatörün
// yazdığı şey değil. Kardeş yol (multiSearchAny) metni LİTERAL alıyor,
// yani bu aynı zamanda bir hizalama.
//
// `\` önce kaçışlanmalı, yoksa sonradan eklenen kaçışlar da kaçışlanır.
func escapeLikeNeedle(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `%`, `\%`)
	s = strings.ReplaceAll(s, `_`, `\_`)
	return s
}

// logSearchConjunct — arama metninin WHERE parçası ve argümanları.
//
// HARF DUYARLILIĞI BİLİNÇLİ OLARAK DEĞİŞTİRİLMEDİ (v0.9.1385). Histogram
// `multiSearchAnyCaseInsensitive` kullanıyor, burası `LIKE` (duyarlı) ve
// bu gerçek bir tutarsızlık. Hizalamanın YÖNÜ ise ölçüm gerektiriyor:
// `body` üzerinde `tokenbf_v1` atlama indeksi var (store.go), ClickHouse'un
// belgelenmiş tokenbf destek listesi `like`i içeriyor ama harf-DUYARSIZ
// multiSearch varyantlarını içermiyor. Yani listeyi histograma hizalamak
// (duyarsız yapmak) en sıcak log yolunda atlama indeksini kaybettirebilir;
// tersi indeksi korur ama histogramı daraltır. Milyar satırlık bir tabloda
// bu yön tahminle seçilemez — tek bir ad-hoc zamanlama da yalan söyler.
// Ölçüm yapılana dek DAVRANIŞ KORUNUYOR ve tutarsızlık burada YAZILI.
func logSearchConjunct(search string) (string, []any) {
	if IsBareHexID(search) {
		// v0.8.521 sözleşmesi. Liste yolunda YOKTU — yani "Search'e
		// trace id yapıştır, bulsun" ekranın yalnız yarısında çalışıyordu:
		// histogram sayıyordu, tablo göstermiyordu.
		id := strings.ToLower(strings.TrimSpace(search))
		return `(body LIKE ? ESCAPE '\\' OR trace_id = ? OR span_id = ?)`,
			[]any{"%" + escapeLikeNeedle(search) + "%", id, id}
	}
	return `body LIKE ? ESCAPE '\\'`, []any{"%" + escapeLikeNeedle(search) + "%"}
}
