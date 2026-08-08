package api

// endpoints_series.go — GET /api/endpoints/series (v0.9.819).
//
// /endpoints sayfasının KPI şeridi + üç grafiğinin TEK kaynağı. Tabloyla
// AYNI pencereyi ve AYNI filtreleri okur (?service= / ?search= / ?entry=
// tablonun kendi kontrolleridir) ki şeritteki sayı ile listedeki satırlar
// aynı kümeyi anlatsın.
//
// Bare (viewer-visible): salt-okunur bir toplama, /api/endpoints ile aynı
// duruş. Yazma yok → audit girdisi yok.

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/cilcenk/coremetry/internal/chstore"
)

// endpointsSeriesKey — cache anahtarı TÜM girdileri taşır (v0.5.187
// sınıfı). Pencere 30 sn ızgarasına snap'li (cacheBucket), yani aynı
// dakikada sayfayı açan iki operatör tek CH turu paylaşır.
//
// service/search operatör metni: ayraç olarak `:` kullanmak "a:b" +
// "c" ile "a" + "b:c" çakışmasına kapı açardı — bu yüzden ikisi
// endpointKeyDigest'ten (NUL ayraçlı FNV) geçiyor, aynı dosya ailesinin
// v0.8.360'ta kurduğu kural.
func endpointsSeriesKey(bucket, service, search, entry, env, cluster string, compare bool) string {
	return fmt.Sprintf("endpoints-series:v1:%s:f=%s:entry=%s:env=%s:cl=%s:cmp=%v",
		bucket, endpointKeyDigest(service, search), entry, env, cluster, compare)
}

func (s *Server) getEndpointsSeries(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	from, to := parseFromTo(r, time.Hour)
	service := q.Get("service")
	search := q.Get("search")
	env := strings.TrimSpace(q.Get("env"))
	cluster := q.Get("cluster")
	// Bilinmeyen entry değeri HTTP'ye düşer — elle düzenlenmiş bir URL
	// tanıdık yüzeye insin, boş bir grafiğe değil (getEndpoints ile aynı
	// kural).
	entry := chstore.EntryHTTP
	if strings.TrimSpace(q.Get("entry")) == "rpc" {
		entry = chstore.EntryRPC
	}
	// compare: "Filo p95" karosunun Δ'sı için bir önceki eş pencerenin
	// p95'i. Sayfanın kendi compare anahtarı sürer; ek maliyet TEK
	// SATIRLIK bir aggregate (GROUP BY yok).
	compare := q.Get("compare") == "prior"

	key := endpointsSeriesKey(cacheBucket(from, to), service, search, string(entry), env, cluster, compare)
	s.serveCached(w, r, key, 30*time.Second, func(ctx context.Context) (any, error) {
		return s.store.GetEndpointsSeries(ctx, chstore.EndpointsSeriesQuery{
			From: from, To: to,
			Service: service, Search: search, Entry: entry,
			Env: env, Cluster: cluster, Compare: compare,
		})
	})
}
