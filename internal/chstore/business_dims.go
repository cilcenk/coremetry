package chstore

import (
	"context"
	"fmt"
	"time"
)

// business_dims.go — v0.9.511. İŞ boyutu kırılımı (kanal / fonksiyon kodu).
//
// Operatör: "channel code ve function code değerleri, cluster bilgisi de
// değerli… trace'teki attribute'ları da bilgi olarak alışverebilirsin."
//
// Bir SRE'nin teknik cevabı ("checkout hata oranı %4") ile işin duyduğu
// cevap ("mobil kanaldan gelen bireysel işlemler patlıyor") arasındaki
// farkı bu kırılım kapatıyor. Kodlar zaten span attribute'u olarak akıyor
// (CHANNEL_CODE / FUNCTION_CODE) ve v0.9.198'de spans üzerinde
// MATERIALIZED kolona terfi edildi.
//
// NEDEN HAM spans — normalde bir aggregate MV'den okunur (CLAUDE.md
// invariant #3). Bu boyutlar hiçbir mevcut MV'de yok: rollup'ların GENİŞ
// ailesi taşıyor ama o aile prod'da HENÜZ UYGULANMADI, yani rollup'a
// dayanmak özelliği tam gerektiği yerde ölü bırakırdı. Bu okuma bunun
// yerine dar kapsamlı: P1 başına bir kez, tek servis, olay penceresi.
// service_name + time ORDER BY önekiyle birebir eşleştiği için partition
// pruning tam çalışır; LIMIT + max_execution_time + zaman-sınırlı WHERE
// üçlüsü yerinde. Rollup'lar prod'a uygulandığında buradaki okuma
// QueryRollupRED'e taşınmalı — o gün geldiğinde bu yorum kılavuz.

// BusinessSlice — bir iş boyutu değerinin pencere içindeki ağırlığı.
type BusinessSlice struct {
	Value  string  `json:"value"`  // ham kod (CHANNEL_CODE değeri gibi)
	Calls  uint64  `json:"calls"`
	Errors uint64  `json:"errors"`
	ErrPct float64 `json:"errPct"`
}

// businessDimExpr — bir attribute anahtarını GROUP BY edilebilir SQL
// ifadesine çevirir. traceExtrasProjection ile AYNI çözümleme sırası:
//  1. terfi etmiş native kolon (en ucuz),
//  2. well-known semconv kolonu,
//  3. genel dizi araması.
//
// Saf — tablo-testli (business_dims_test.go). Anahtar bind arg olarak
// gider; çağıran zaten sabit anahtar veriyor ama dizi yolunda parametre
// kullanmak enjeksiyon yüzeyini tamamen kapatıyor.
func businessDimExpr(key string) (string, []any) {
	if col, ok := traceAttrMaterialized[key]; ok {
		return col, nil
	}
	// v0.9.624 — anahtar bir terfi attribute'unun BAŞKA YAZIMI olabilir.
	//
	// Operator-reported zincirinin devamı: businessDimKeys
	// (anomaly/investigation.go:61) ve copilot kırılım listeleri
	// (api/copilot_aianalyze.go:269, :375) "CHANNEL_CODE" / "FUNCTION_CODE"
	// sabitlerini taşıyor, prod verisi ise küçük harf yazıyor. Sonuç:
	// `WHERE <ifade> != ''` hiçbir satır tutmuyordu ve "hangi kanal
	// patlıyor" kanıt bloğu (v0.9.511, v0.9.580 — operatörün açıkça
	// istediği bölüm) prod'da SESSİZCE BOŞ dönüyordu.
	//
	// Çağrı noktalarına dokunmak yerine çözümleme burada yapılıyor:
	// üç yerde üç liste düzeltmek, dördüncüsü eklendiğinde yine
	// ayrışırdı. promotedAttrResolve neden yalnız kod içi listeler
	// için harf duyarsız olduğunu anlatıyor.
	if expr, args, ok := promotedAttrResolve(key); ok {
		return expr, args
	}
	if col, ok := WellKnownTraceCol[key]; ok {
		return col, nil
	}
	return "attr_values[indexOf(attr_keys, ?)]", []any{key}
}

// businessDimLimit — kırılımda taşınan en fazla değer. Kanal kodu tipik
// olarak tek haneli sayıda; tavan yine de var çünkü yanlış yapılandırılmış
// bir attribute yüksek kardinaliteli olabilir.
const businessDimLimit = 8

// BusinessBreakdown — servis + pencere için verilen attribute anahtarının
// hata-ağırlıklı kırılımı. En çok HATA üreten değer başta: bir SRE'nin
// sorduğu "hangi kanal patlıyor" sorusunun sırası bu, hacim sırası değil.
//
// Boş değerler elenir — attribute'u taşımayan span'ler kırılımı
// kirletmemeli (o span'ler "bilinmeyen kanal" değil, "bu boyutla ilgisiz").
func (s *Store) BusinessBreakdown(ctx context.Context, service, attrKey string, from, to time.Time) ([]BusinessSlice, error) {
	expr, exprArgs := businessDimExpr(attrKey)

	// Argüman sırası SQL'deki placeholder sırasıyla birebir: SELECT'teki
	// ifade önce (dizi yolunda bir bind), sonra WHERE'deki service+zaman,
	// sonra HAVING yerine kullanılan ifade tekrarı, en sonda LIMIT.
	args := make([]any, 0, len(exprArgs)*2+4)
	args = append(args, exprArgs...)
	args = append(args, service, from, to)
	args = append(args, exprArgs...)
	args = append(args, businessDimLimit)

	sql := fmt.Sprintf(`
		SELECT %s AS dim,
		       count()                          AS calls,
		       countIf(status_code = 'error')    AS errors
		FROM spans
		WHERE service_name = ?
		  AND time >= ?
		  AND time < ?
		  AND %s != ''
		GROUP BY dim
		ORDER BY errors DESC, calls DESC
		LIMIT ?
		SETTINGS max_execution_time = 5`, expr, expr)

	rows, err := s.telemetryReadConn().Query(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []BusinessSlice
	for rows.Next() {
		var b BusinessSlice
		if err := rows.Scan(&b.Value, &b.Calls, &b.Errors); err != nil {
			return nil, err
		}
		if b.Calls > 0 {
			b.ErrPct = float64(b.Errors) / float64(b.Calls) * 100
		}
		out = append(out, b)
	}
	return out, rows.Err()
}
