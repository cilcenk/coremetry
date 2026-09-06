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

func logsPatternsKey(f logstore.Filter, fromRaw, toRaw string, limit, sample int) string {
	return fmt.Sprintf("logs-patterns:v2:svc=%s:clu=%s:env=%s:sev=%d:trace=%s:span=%s:ht=%t:from=%s:to=%s:q=%s:lim=%d:smp=%d",
		f.Service, f.Cluster, f.Env, f.SeverityMin, f.TraceID, f.SpanID, f.HasTrace,
		fromRaw, toRaw, f.Search, limit, sample)
}

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
	keyFrom, keyTo := snapLogsWindow(&f, q.Get("from"), q.Get("to"), 30*time.Second) // v0.10.446 (A6-V2: TTL kadar; varsayılan pencere de anahtara girer)
	key := logsPatternsKey(f, keyFrom, keyTo, limit, sample)
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
		return res, nil
	})
}
