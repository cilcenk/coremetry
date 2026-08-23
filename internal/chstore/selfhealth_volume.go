// selfhealth_volume.go — v0.9.1294: hacim sıçraması kuralının
// (self-volume-spike) OKUMA yarısı. Karar yarısı
// internal/evaluator/selfhealth_volume.go.
//
// NEDEN VAR: cardinality.go'nun başlık yorumu sınıfı zaten yazıyor —
// "tek bir servis her zamankinin 10 katı span üretmeye başladığında
// (bozuk deploy, sonsuz retry döngüsü) bu, disk dolmadan GÜNLER önce
// burada görünür". Ama YALNIZ admin /system/cardinality'ye bakarsa.
// v0.9.1279'un tezi neyse burada da o: panel, kimse bakmıyorsa alarm
// değildir. Bu dosya aynı soruyu operatöre GETİREN ölçümü veriyor.
//
// VERİ KAYNAĞI SEÇİMİ (bilinçli, cardinality.go'dan AYRI):
// GetCardinality'nin top-emitter okuması `FROM spans ... GROUP BY
// service_name` ile HAM TABLOYU tarıyor. O sorgu admin'in elle açtığı,
// 5 dakika cache'lenen, 25 saniyelik bütçesi olan bir panel için
// yazılmış. Bir DEDEKTÖRÜN aynı taramayı yapması mimari değişmez #3'ün
// tarifi gereği bug olurdu (milyar satırda agregat için ham spans).
// Bu yüzden kaynak service_summary_5m: ReadIngestWindows'un
// countMergeIf desenini paylaşır, iki pencere TEK sorguda gelir ve
// PARTITION BY toDate(time_bucket) sayesinde 48 saatlik pencere en çok
// üç partition okur.
package chstore

import (
	"context"
	"fmt"
	"time"
)

// ServiceVolume — bir servisin iki ardışık pencerede span sayımı.
//
// Cur/Prev EŞİK için değil ORAN için okunuyor; ikisi de aynı uzunlukta
// ve aynı kova hizasında olmak zorunda, yoksa oran pencerenin kendi
// kaymasını ölçer. Bkz. sorgudaki `anchor`.
type ServiceVolume struct {
	Service string
	Cur     uint64
	Prev    uint64
}

// ReadServiceVolumeWindows — servis başına iki pencere span sayımı,
// MV'den.
//
// minSpans SQL'e ÖN-FİLTRE olarak iniyor (HAVING) ve aynı sayı Go
// tarafında kararın kapısı olarak yeniden uygulanıyor. Bu ikizleme
// değil: tek kaynak cfg.VolumeSpikeMinSpans, SQL onu yalnız
// SIRALAMAYI anlamlı kılmak için kullanıyor — `LIMIT n` olmadan
// binlerce servislik bir kurulumda tik başına sınırsız satır dönerdi,
// tabanın altındakileri de sıralamaya sokan bir LIMIT ise gerçek bir
// sıçramayı listenin dışında bırakabilirdi.
//
// `anchor` = TAMAMLANMIŞ son 5 dakikalık kova. İki pencere de tam 288
// kova taşır ve devam etmekte olan (yarım) kova İKİSİNE DE girmez —
// yarım kovayı cur'a katmak oranı sistematik olarak aşağı çeker, prev'e
// katmak yukarı: hiza, kova sınırı taramasının (v0.9.1176) bu okumaya
// düşen payı.
func (s *Store) ReadServiceVolumeWindows(ctx context.Context, window time.Duration, minSpans uint64, limit int) ([]ServiceVolume, error) {
	w := int64(window / time.Second)
	if w <= 0 {
		return nil, fmt.Errorf("selfhealth: geçersiz hacim penceresi %s", window)
	}
	if limit <= 0 {
		limit = 500
	}
	rows, err := s.conn.Query(ctx, `
		WITH toStartOfInterval(now(), INTERVAL 5 MINUTE) AS anchor
		SELECT
		  service_name,
		  toUInt64(countMergeIf(span_count_state, time_bucket >= anchor - toIntervalSecond(?))) AS cur,
		  toUInt64(countMergeIf(span_count_state, time_bucket <  anchor - toIntervalSecond(?))) AS prev
		FROM service_summary_5m
		WHERE time_bucket >= anchor - toIntervalSecond(?)
		  AND time_bucket <  anchor
		  AND service_name != ''
		GROUP BY service_name
		HAVING cur >= ?
		ORDER BY cur DESC
		LIMIT ?
		SETTINGS max_execution_time = 20`, w, w, 2*w, minSpans, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]ServiceVolume, 0, 16)
	for rows.Next() {
		var v ServiceVolume
		if err := rows.Scan(&v.Service, &v.Cur, &v.Prev); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}
