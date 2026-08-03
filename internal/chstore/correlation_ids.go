// v0.9.580 — korelasyon kimliği örnekleri.
//
// Operatör: "CoSRE örnek request_id, CHANNEL_CODE değerlerini de
// söylesin."
//
// Neden değerli: bir cevabın eyleme dönüşebilmesi için operatörün
// ARAYABİLECEĞİ bir şey vermesi gerekir. "Ödeme servisinde hata oranı
// %14" bir gözlemdir; "…örnek request_id: 8f3c-…, en çok hata üreten
// CHANNEL_CODE: 0012" bir BAŞLANGIÇ NOKTASIDIR. Operatör onu alıp
// kendi log'una, kendi çağrı merkezine, kendi kaydına gider.
//
// Neden ayrı bir okuma: request_id kasten YÜKSEK KARDİNALİTELİ bir
// alan ve bu yüzden kırılım/agregasyon yollarından bilerek dışlanmış
// (bubbleup.go highCardinalityKey). Burada kırılım İSTEMİYORUZ —
// birkaç ÖRNEK istiyoruz, ki bu bambaşka bir sorgu şekli: dar zaman
// penceresi, hata span'leri, LIMIT.
package chstore

import (
	"context"
	"sort"
	"strings"
	"time"
)

// CorrelationSample — bir korelasyon anahtarı ve ondan birkaç örnek.
type CorrelationSample struct {
	// Key — attribute adı (request_id, correlation_id, x-request-id…).
	// Filoda hangi yazımın kullanıldığını KEŞFEDİYORUZ, dayatmıyoruz.
	Key string `json:"key"`
	// Values — birkaç gerçek örnek, tekrarsız.
	Values []string `json:"values"`
}

const (
	// corrSampleSpans — kaç hata span'i okunur. 20: birkaç farklı
	// request_id görmeye yeter, tarama maliyeti ihmal edilebilir.
	corrSampleSpans = 20
	// corrSampleValuesPerKey — anahtar başına kaç örnek gösterilir.
	// 3: operatörün aramaya başlaması için yeter; daha fazlası
	// prompt'u şişirir ve küçük modelde gürültü olur.
	corrSampleValuesPerKey = 3
	// corrSampleMaxKeys — kaç farklı korelasyon anahtarı taşınır.
	corrSampleMaxKeys = 3
)

// isCorrelationKey — bu attribute bir istek/işlem kimliği mi?
//
// SAF (tablo testli). Sezgi bubbleup.go'nun yüksek-kardinalite
// testiyle AYNI aileden ama TERS amaçla: orada bu anahtarlar
// kırılımdan DIŞLANIYOR, burada tam da onları ARIYORUZ.
//
// trace_id / span_id BİLEREK dışarıda: onlar zaten cevabın başka
// yerinde (exemplar) veriliyor ve burada tekrarlamak yer kaplar.
func isCorrelationKey(key string) bool {
	lk := strings.ToLower(key)
	if strings.Contains(lk, "trace") || strings.Contains(lk, "span") {
		return false
	}
	for _, p := range []string{"request_id", "requestid", "request-id",
		"correlation", "corelation", "islem_no", "transaction_id", "txid"} {
		if strings.Contains(lk, p) {
			return true
		}
	}
	return false
}

// pickCorrelationSamples — attr anahtar/değer dizilerinden örnekleri
// toplar. SAF: SQL'den ayrı olduğu için tablo-testli, ve bu ayrım
// bilinçli — bu oturumda iki kez SQL'e dokunmayan testler yüzünden
// çalışmayan kod gönderildi (v0.9.543, v0.9.572).
func pickCorrelationSamples(rows [][2][]string) []CorrelationSample {
	byKey := map[string][]string{}
	seen := map[string]map[string]bool{}
	for _, r := range rows {
		keys, vals := r[0], r[1]
		for i, k := range keys {
			if i >= len(vals) || !isCorrelationKey(k) {
				continue
			}
			v := strings.TrimSpace(vals[i])
			if v == "" {
				continue
			}
			if seen[k] == nil {
				seen[k] = map[string]bool{}
			}
			if seen[k][v] || len(byKey[k]) >= corrSampleValuesPerKey {
				continue
			}
			seen[k][v] = true
			byKey[k] = append(byKey[k], v)
		}
	}
	out := make([]CorrelationSample, 0, len(byKey))
	for k, v := range byKey {
		out = append(out, CorrelationSample{Key: k, Values: v})
	}
	// Deterministik sıra: aynı girdi her çağrıda aynı çıktıyı vermeli,
	// yoksa prompt her seferinde değişir ve cache anahtarları ıskalar.
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	if len(out) > corrSampleMaxKeys {
		out = out[:corrSampleMaxKeys]
	}
	return out
}

// SampleCorrelationIDs — bir servisin son hatalarından örnek istek
// kimlikleri.
//
// Bounded: service+time WHERE, LIMIT, max_execution_time (spans sert
// kısıtı). Hata span'i yoksa boş döner — uydurma yok.
func (s *Store) SampleCorrelationIDs(ctx context.Context, service string, from, to time.Time) ([]CorrelationSample, error) {
	if service == "" {
		return nil, nil
	}
	rows, err := s.telemetryReadConn().Query(ctx, `
		SELECT attr_keys, attr_values
		FROM spans
		WHERE service_name = ? AND time >= ? AND time <= ?
		  AND status_code = 'error'
		ORDER BY time DESC
		LIMIT ?
		SETTINGS max_execution_time = 5`,
		service, chDateTime64Arg(from), chDateTime64Arg(to), corrSampleSpans)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var raw [][2][]string
	for rows.Next() {
		var keys, vals []string
		if err := rows.Scan(&keys, &vals); err != nil {
			return nil, err
		}
		raw = append(raw, [2][]string{keys, vals})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return pickCorrelationSamples(raw), nil
}
