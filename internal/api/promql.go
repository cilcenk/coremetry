package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// maxPromQLQueryLen caps the raw query length before parsing (review CRITICAL,
// v0.9.122). Real PromQL is well under a KB; 8KB leaves generous headroom while
// stopping a megabyte-scale nested-paren DoS.
const maxPromQLQueryLen = 8192

// queryPromQL backs GET /api/metrics/promql?query=&from=&to=&step=&maxDataPoints=
// — an industry-standard PromQL range query over the OTel metric store
// (F4). On the ClickHouse path it parses + evaluates via internal/promql: the
// HYBRID model — leaf selectors + rate/increase/histogram_quantile push down to
// the bounded chstore machinery (F1-F3: LIMIT + max_execution_time +
// time-bounded WHERE, distributed-safe).
//
// Returns the same SpanMetricSeries[] shape the UI charts already consume.
// Auth: viewer+ via the global GET middleware (like /api/metrics/query — a
// read-only surface, no audit). serveCached (30s) keys on the full query so a
// dashboard's repeated polls stay cheap; sanitizeFloats (in serveCached)
// scrubs the NaN/Inf a PromQL expression can legitimately produce.
//
// PERFORMANCE (operator hard constraint): the evaluator rejects a too-deep or
// nameless query BEFORE any CH round trip, reuses the bounded leaf fetches, and
// caps the result series count — no unbounded selector, no full-catalogue scan.
//
// v0.9.1157 (VM Faz 2) — SEAM'e girdi, ve bu uçta seam'in İKİ YARISI
// BİLİNÇLİ OLARAK FARKLI DAVRANIR:
//
//	ClickHouse → ValidatePromQL parse eder. Değerlendirici bir PromQL ALT
//	  KÜMESİ uyguluyor; ifade edemediğini önceden reddetmek operatöre
//	  parser'ın kendi metniyle temiz bir 400 verir.
//	VictoriaMetrics → ValidatePromQL nil döner. VM'nin dili MetricsQL, yani
//	  PromQL'in ÜST KÜMESİ: `WITH(…)`, subquery, `keep_metric_names`,
//	  `rollup_rate` bizim parser'ımızın reddedeceği ama vmui'de çalışan
//	  sorgular. Sorguyu koşacak motordan daha katı olmak, operatöre
//	  "korkuluk" değil "bozuk uç" gibi görünür — sorgu dizesi OLDUĞU GİBİ
//	  VM'e gider ve sözdizimi hatasını VM'in kendi cevabı bildirir.
//
// Bizde KALAN sınır UZUNLUK: iki backend için de burada uygulanıyor, çünkü
// megabaytlık bir dizeyi ne biz parse etmek ne VM'e yollamak isteriz.
func (s *Server) queryPromQL(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	query := strings.TrimSpace(q.Get("query"))
	if query == "" {
		writePromQLError(w, http.StatusBadRequest, "query is required")
		return
	}
	// Reject an oversized query BEFORE parsing (review CRITICAL): a huge
	// deeply-nested string would otherwise cost 100s of MB to parse or crash
	// the process via stack overflow. Real PromQL is tiny; 8KB is generous.
	if len(query) > maxPromQLQueryLen {
		writePromQLError(w, http.StatusBadRequest, "query too long")
		return
	}
	// v0.9.1157 — src anahtarda VE okumada. Aynı zarf şekli iki farklı
	// motordan üretilebiliyor; işaret olmadan bir deneme isteği (ya da
	// Settings toggle'ı) 30 sn boyunca öteki backend'in SAYILARINI servis
	// ederdi (v0.5.187 sınıfı, girdisi bir sorgu parametresi olan hâli).
	src, srcErr := s.metricSourceFor(r)
	if srcErr != nil {
		writeErr(w, srcErr)
		return
	}
	// Sözdizimi kapısı — CACHE'TEN ÖNCE, kaynağın kendi diline göre. CH
	// yolunda bu, v0.9.1157 öncesi buradaki promql.Parse + 400'ün ta
	// kendisi (aynı statü, aynı {"error":…} zarfı); VM yolunda no-op.
	if verr := src.ValidatePromQL(query); verr != nil {
		writeErr(w, verr)
		return
	}

	step, _ := strconv.Atoi(q.Get("step"))
	maxDP, _ := strconv.Atoi(q.Get("maxDataPoints"))
	if maxDP < 0 {
		maxDP = 0
	} else if maxDP > 4000 {
		maxDP = 4000
	}
	to := parseTime(q.Get("to"))
	from := parseTime(q.Get("from"))
	now := time.Now()
	if to.IsZero() {
		to = now
	}
	if from.IsZero() {
		from = to.Add(-1 * time.Hour)
	}

	key := fmt.Sprintf("promql:src=%s:q=%s:step=%d:mdp=%d:from=%d:to=%d",
		src.Name(), query, step, maxDP, from.Unix()/60, to.Unix()/60)
	s.serveCached(w, r, key, 30*time.Second, func(ctx context.Context) (any, error) {
		return src.QueryPromQLRange(ctx, query, from, to, step, maxDP)
	})
}

func writePromQLError(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": msg})
}
