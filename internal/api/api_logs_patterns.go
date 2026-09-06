package api

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/cilcenk/coremetry/internal/logstore"
)

// api_logs_patterns.go — v0.10.296 (log-search audit Dilim 2, B3):
// GET /api/logs/patterns — pencere içindeki mesajları NormalizeSignature
// imzasıyla gruplar (logstore.GroupBySignature, örneklemeli ≤2000 satır).
//
// Parametreler /api/logs ile aynı filtre kümesi + limit (basamak 20/50/100
// — v0.8.270 disiplini: serbest tamsayı cache kardinalitesini şişirir).
// Cache 30 s; anahtar TÜM girdileri taşır (v0.5.187). Degrade sözleşmesi
// (v0.8.350): yavaş/erişilemez backend → HTTP 200 {degraded:true, reason}
// + boş gruplar; gerçek sorgu hatası 5xx.

var logsPatternsLimitRungs = []int{20, 50, 100}

const logsPatternsBudget = 10 * time.Second

func logsPatternsLimit(want int) int {
	if want <= 0 {
		return 50
	}
	for _, r := range logsPatternsLimitRungs {
		if want <= r {
			return r
		}
	}
	return logsPatternsLimitRungs[len(logsPatternsLimitRungs)-1]
}

// logsPatternsSampleRungs — v0.10.452 (C1, ES maliyeti): örnek tavanı
// basamaklı (500 = 1 ES sayfası, 2000 = 4). Problem detayı 500 ister;
// /logs paneli varsayılanı korur. Anahtara girer.
var logsPatternsSampleRungs = []int{500, 2000}

func logsPatternsSample(want int) int {
	if want <= 0 {
		return logstore.PatternsSampleCap
	}
	for _, r := range logsPatternsSampleRungs {
		if want <= r {
			return r
		}
	}
	return logstore.PatternsSampleCap
}

func logsPatternsKey(f logstore.Filter, fromRaw, toRaw string, limit, sample int, baseline bool) string {
	return fmt.Sprintf("logs-patterns:v3:svc=%s:clu=%s:env=%s:sev=%d:trace=%s:span=%s:ht=%t:from=%s:to=%s:q=%s:lim=%d:smp=%d:base=%t",
		f.Service, f.Cluster, f.Env, f.SeverityMin, f.TraceID, f.SpanID, f.HasTrace,
		fromRaw, toRaw, f.Search, limit, sample, baseline)
}

// logsPatternsBaselineWindow — v0.10.508 (C6) SAF: tabanın penceresi =
// hemen önceki EŞİT uzunluk ([from−(to−from), from)). Sıfır/negatif
// pencerede ok=false (taban yok).
func logsPatternsBaselineWindow(from, to time.Time) (bFrom, bTo time.Time, ok bool) {
	d := to.Sub(from)
	if d <= 0 {
		return time.Time{}, time.Time{}, false
	}
	return from.Add(-d), from, true
}

// logsPatternsBaselineSample — taban örneklemesi TEK ES sayfası (500):
// mevcut pencerenin örneklemesi 2000'e çıksa da taban 500'de kalır (ES
// maliyet disiplini: Trend düğmesi bir ek sayfa, dört değil).
const logsPatternsBaselineSample = 500

func (s *Server) getLogsPatterns(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	sev, _ := strconv.Atoi(q.Get("severity"))
	f := logstore.Filter{
		Service:     q.Get("service"),
		Cluster:     q.Get("cluster"),
		Env:         strings.TrimSpace(q.Get("env")),
		Search:      q.Get("search"),
		From:        parseTime(q.Get("from")),
		To:          parseTime(q.Get("to")),
		SeverityMin: uint8(sev),
		TraceID:     q.Get("traceId"),
		SpanID:      q.Get("spanId"),
		HasTrace:    parseBoolParam(q.Get("hasTrace")),
	}
	if f.From.IsZero() || f.To.IsZero() {
		f.To = time.Now()
		f.From = f.To.Add(-time.Hour)
	}
	if s.rejectLogQuerySyntax(w, f.Search) { // v0.10.280 sözleşmesi
		return
	}
	limit := logsPatternsLimit(parseInt(q.Get("limit"), 0))
	sample := logsPatternsSample(parseInt(q.Get("sample"), 0))                       // v0.10.452
	baseline := q.Get("baseline") == "1"                                             // v0.10.508 (C6) — Trend: önceki pencere örneklemesi
	keyFrom, keyTo := snapLogsWindow(&f, q.Get("from"), q.Get("to"), 30*time.Second) // v0.10.446 (A6-V2: TTL kadar; varsayılan pencere de anahtara girer)
	key := logsPatternsKey(f, keyFrom, keyTo, limit, sample, baseline)
	s.serveCached(w, r, key, 30*time.Second, func(ctx context.Context) (any, error) {
		tctx, cancel := context.WithTimeout(ctx, logsPatternsBudget)
		defer cancel()
		res, err := logstore.GroupBySignatureN(tctx, s.logs, f, limit, sample)
		if err != nil {
			if mapped := logstore.MapBackendSlow(err, tctx, ctx); errors.Is(mapped, logstore.ErrBackendSlow) {
				log.Printf("[logs] patterns degraded (backend=%s, svc=%q): %v", s.logs.Backend(), f.Service, err)
				return map[string]any{
					"groups": []logstore.SignatureGroup{}, "sampled": 0, "total": 0,
					"cap": sample, "truncated": false, "distinct": 0,
					"coveredFromNs": 0, "coveredToNs": 0, // v0.10.441 (C4) — sağlıklı gövdeyle aynı şekil
					"partial": false, "shardsFailed": 0, "totalIsLowerBound": false, // v0.10.452
					"degraded": true, "reason": "log backend slow/unreachable",
				}, nil
			}
			return nil, err
		}
		if res.Groups == nil {
			res.Groups = []logstore.SignatureGroup{}
		}
		// v0.10.508 (C6) — taban: hemen önceki eşit pencere, aynı süzgeç, tek
		// sayfa; hash ile birleşir. Okunamazsa gruplar tabansız kalır ve zarf
		// degraded der (yokluk sıfır değil). Tam grup listesi (limit yerine
		// patternsMaxGroups) — mevcut top-N'in tabanı kesilmiş listede
		// "yok" görünmesin.
		if baseline {
			if bFrom, bTo, ok := logsPatternsBaselineWindow(f.From, f.To); ok {
				bf := f
				bf.From, bf.To = bFrom, bTo
				bctx, bcancel := context.WithTimeout(ctx, logsPatternsBudget)
				base, berr := logstore.GroupBySignatureN(bctx, s.logs, bf, logstore.PatternsMaxGroups, logsPatternsBaselineSample)
				bcancel()
				res.Baseline = &logstore.PatternsBaseline{FromNs: bFrom.UnixNano(), ToNs: bTo.UnixNano()}
				if berr != nil {
					log.Printf("[logs] patterns baseline degraded (backend=%s, svc=%q): %v", s.logs.Backend(), f.Service, berr)
					res.Baseline.Degraded, res.Baseline.Reason = true, "baseline sample failed"
				} else {
					res.Baseline.Sampled, res.Baseline.Distinct, res.Baseline.Truncated = base.Sampled, base.Distinct, base.Truncated
					logstore.JoinPatternBaseline(res, base)
				}
			}
		}
		return res, nil
	})
}
