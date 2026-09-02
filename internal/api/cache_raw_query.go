package api

// cache_raw_query.go — v0.10.256 (perf profili §7 madde 2, C2 ⭐ + C4).
//
// Beş uç (`traces`, `traces-count`, `traces-agg`, `span-metric`, `exc-groups`)
// anahtarı `r.URL.RawQuery` ile kuruyordu. SPA her poll'da `to=Date.now()`
// (ns) gönderdiği için anahtar her istekte değişiyor, cache fiilen MISS —
// çok pod'da N× CH taraması. Ayrıca `?refresh=1` anahtara giriyordu (C4):
// BYPASS yazımı kimsenin okumadığı bir anahtara gidiyordu.
//
// cacheRawQuery: from/to (unix ns) 30 s grid'e (cacheBucket ile aynı
// basamak) yuvarlanır, `refresh` düşer, kalan parametreler ANAHTAR SIRALI
// yeniden kodlanır (url.Values.Encode). Yükleyici gerçek pencereyi kullanır;
// aynı grid'deki ikinci istek ilkinin gövdesini alır (≤30 s kayma — diğer
// uçların cacheBucket sözleşmesi). SAF; cache_key_test.go pinler.

import (
	"net/http"
	"net/url"
	"strconv"
	"time"
)

const cacheRawQueryGrid = 30 * time.Second

// cacheRawQueryString — sorgu dizesini anahtar için normalize eder.
func cacheRawQueryString(raw string) string {
	q, err := url.ParseQuery(raw)
	if err != nil {
		return raw
	}
	q.Del("refresh")
	for _, k := range []string{"from", "to"} {
		v := q.Get(k)
		if v == "" {
			continue
		}
		ns, err := strconv.ParseInt(v, 10, 64)
		if err != nil || ns <= 0 {
			continue
		}
		q.Set(k, strconv.FormatInt(time.Unix(0, ns).Truncate(cacheRawQueryGrid).UnixNano(), 10))
	}
	return q.Encode()
}

// cacheRawQuery — istek için anahtar parçası.
func cacheRawQuery(r *http.Request) string { return cacheRawQueryString(r.URL.RawQuery) }
