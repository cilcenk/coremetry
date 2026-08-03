// v0.9.572 — paylaşılan bağımlılık patlaması tespiti.
//
// Operatör raporu (prod, gece 03:04): on beşten fazla servis AYNI
// saniyede aynı `java.sql.SQLRecoverableException` ORA-18730
// "Socket read timed out" hatasını aldı. Coremetry bunları on beş ayrı
// exception grubu olarak gösterdi — her biri "şu servisin sorunu" gibi.
//
// Oysa bu servis başına bir sorun DEĞİL: paylaşılan bir bağımlılıkta
// (Oracle) tek bir olay. On beş satır okuyup aradaki ortak paydayı
// operatörün kafasında kurması gerekiyordu.
//
// Sinyal basit ve güçlü: AYNI exception tipi, DAR bir zaman
// penceresinde, ÇOK sayıda ayrı serviste BAŞLIYOR. Tek bir servisin
// kendi hatası yayılmaz; paylaşılan bir bağımlılığın hatası yayılır.
//
// Tespit deterministik — LLM YOK. Bu, RCA paketinin [2] CORRELATE
// katmanı: model tespit etmez, tespit edileni anlatır.
package chstore

import (
	"context"
	"fmt"
	"sort"
	"time"
)

// SharedExceptionBurst — birden çok serviste eşzamanlı başlayan aynı
// exception tipi.
type SharedExceptionBurst struct {
	// Type — exception sınıfı (java.sql.SQLRecoverableException gibi).
	Type string `json:"type"`
	// Message — temsilci mesaj (ilk grubunki). Gruplar arasında
	// değişebilir; tanı için tipin kendisi taşıyıcı olan.
	Message string `json:"message"`
	// Services — etkilenen servisler, alfabetik (deterministik kimlik).
	Services []string `json:"services"`
	// FirstSeen / LastSeen — patlamanın ucu (unix ns).
	FirstSeen int64 `json:"firstSeen"`
	LastSeen  int64 `json:"lastSeen"`
	// Occurrences — tüm servislerdeki toplam.
	Occurrences uint64 `json:"occurrences"`
	// BucketStart — patlamanın ait olduğu zaman kovası (unix ns).
	// Kimliğin parçası: aynı tip, farklı gecelerde ayrı olaydır.
	BucketStart int64 `json:"bucketStart"`
}

// sharedBurstWindow — "eşzamanlı" sayılan pencere.
//
// 5 dakika: operatörün vakasında tüm gruplar AYNI SANİYEDE başladı,
// ama bir bağımlılık arızası servislere birkaç saniye/dakika arayla
// vurabilir (bağlantı havuzu boşalma sırası, retry backoff). Daha
// geniş bir pencere ilgisiz olayları birleştirmeye başlar.
const sharedBurstWindow = 5 * time.Minute

// sharedBurstMinServices — kaç servis "paylaşılan" saydırır.
//
// 3: iki servis tesadüf olabilir (ortak kütüphane, aynı deploy). Üç ve
// üstü bir örüntüdür. Operatörün vakası 15+ idi; eşiği oraya çekmek
// daha küçük ama gerçek olayları kaçırırdı.
const sharedBurstMinServices = 3

// FindSharedExceptionBursts — son `since` içinde başlamış, en az
// minServices ayrı serviste görülen aynı-tip exception patlamaları.
//
// Kova mantığı ŞART: kova olmadan, aylardır her serviste ara sıra
// görülen yaygın bir exception tipi (NullPointerException gibi) her
// taramada "paylaşılan patlama" sayılırdı. Kova, EŞZAMANLI BAŞLAMA
// koşulunu SQL'e taşıyor.
func (s *Store) FindSharedExceptionBursts(ctx context.Context, since time.Duration, minServices int) ([]SharedExceptionBurst, error) {
	if since <= 0 {
		since = 24 * time.Hour
	}
	if minServices < 2 {
		minServices = sharedBurstMinServices
	}
	windowSec := int(sharedBurstWindow / time.Second)

	rows, err := s.conn.Query(ctx, fmt.Sprintf(`
		SELECT ex_type,
		       any(ex_message)                                   AS msg,
		       groupArray(service)                               AS services,
		       toUnixTimestamp64Nano(min(first_seen))            AS first_ns,
		       toUnixTimestamp64Nano(max(last_seen))             AS last_ns,
		       sum(occurrences)                                  AS occ,
		       toUnixTimestamp64Nano(toStartOfInterval(min(first_seen), INTERVAL %d SECOND)) AS bucket_ns
		FROM (
		    SELECT ex_type, ex_message, service, first_seen, last_seen, occurrences,
		           toStartOfInterval(first_seen, INTERVAL %d SECOND) AS bucket
		    FROM exception_groups FINAL
		    WHERE first_seen >= ?
		      AND state != 'ignored'
		      AND ex_type != ''
		)
		GROUP BY ex_type, bucket
		HAVING uniqExact(service) >= ?
		ORDER BY occ DESC
		LIMIT 50
		SETTINGS max_execution_time = 10`, windowSec, windowSec),
		chDateTime64Arg(time.Now().Add(-since)), minServices)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]SharedExceptionBurst, 0, 16)
	for rows.Next() {
		var b SharedExceptionBurst
		if err := rows.Scan(&b.Type, &b.Message, &b.Services,
			&b.FirstSeen, &b.LastSeen, &b.Occurrences, &b.BucketStart); err != nil {
			return nil, err
		}
		// groupArray tekrar içerebilir (aynı servis, farklı fingerprint —
		// mesajı değişen aynı tip). Kimlik ve sayım için tekilleştir.
		b.Services = uniqueSorted(b.Services)
		if len(b.Services) < minServices {
			continue
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

// uniqueSorted — tekilleştirilmiş + alfabetik. Sıra deterministik
// olmalı: servis listesi patlamanın KİMLİĞİNE giriyor ve kayan bir
// sıra, aynı olayı her taramada yeni bir olay gibi gösterirdi.
func uniqueSorted(in []string) []string {
	seen := make(map[string]bool, len(in))
	out := make([]string, 0, len(in))
	for _, v := range in {
		if v == "" || seen[v] {
			continue
		}
		seen[v] = true
		out = append(out, v)
	}
	sort.Strings(out)
	return out
}
