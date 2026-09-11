package api

// trace_bundle.go — v0.10.671 (trace kiosk modu, Dilim 1;
// docs/audit/trace-kiosk-mode-audit-2026-09-11.md §7-§9).
//
//   GET /api/traces/{id}/bundle?logLimit=500&oracleLimit=200
//
// Tek istekte üç bacak: span (traceDetailPayload — Tempo-önce / ClickHouse,
// /api/traces/{id} ile AYNI gövde ve alanlar), trace'e bağlı loglar
// (logstore.SearchWithTimeout, 3 s pivot bütçesi) ve Oracle hata satırları
// (oracle_logs_routes.go'nun aynı dönüştürücüsü). Log/Oracle penceresi
// span'lerden SUNUCUDA kurulur (spanWindow, ±60 s — hooks.ts
// traceLogWindow'un aynası): istemcinin "?tab=logs" derin bağlantısında
// attığı penceresiz (tüm-retention) ilk istek burada hiç oluşmaz.
//
// errgroup DEĞİL (v0.8.532, logs_context_halves.go): WithContext ilk hatada
// kardeşi iptal eder ve yavaş backend 200 {degraded} yerine 5xx olurdu.
// WaitGroup + bağımsız hata yuvaları: bir bacak düşerse bundle yine 200
// döner ve o slotta bunu söyler (degraded + reason). İstemci gittiyse
// (ctx iptal) gövde cache'lenmez — degraded slot 15 s boyunca başkasına
// servis edilmesin.
//
// api.go BÜYÜMEZ — route_registry defteri. Rol kapısı YOK: salt-okunur,
// viewer görür; küresel middleware kimliksizi 401'ler. serveCached 15 s
// (üç bacağın en kısa TTL'i); anahtar id + iki limit (limit cevabın
// UZUNLUĞUNU değiştirir, v0.5.187), clamp anahtarın ÖNÜNDE.
//
// Yanıt: /api/traces/{id} alanları + logs{total, logs, degraded?, reason?,
// nextCursor?, partial?, totalIsLowerBound?} + oracle{enabled, logs, total,
// degraded?, reason?} + truncated{spans, logs, oracle} + window{from,to}
// (ns) + logLimit.

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/cilcenk/coremetry/internal/chstore"
	"github.com/cilcenk/coremetry/internal/logstore"
)

func init() { registerRoutesExtra("trace-bundle", (*Server).registerTraceBundleRoutes) }

func (s *Server) registerTraceBundleRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/traces/{id}/bundle", s.getTraceBundle)
}

const (
	traceBundleLogDefault = 500
	traceBundleLogMax     = 1000
	traceBundleTTL        = 15 * time.Second
	// traceBundleWindowBuffer — hooks.ts TRACE_LOG_WINDOW_BUFFER_NS'in aynası
	// (±1 dk: host'lar arası saat kayması + sınırın hemen dışındaki loglar).
	traceBundleWindowBuffer = time.Minute
)

// traceBundleKey — SAF: id + iki limit. Limit cevabın uzunluğunu değiştirir;
// eksik bırakılsaydı 500'lük cevap 1000 isteyene servis edilirdi.
func traceBundleKey(id string, logLimit, oracleLimit int) string {
	return fmt.Sprintf("trace-bundle:v1:%s:ll=%d:ol=%d", id, logLimit, oracleLimit)
}

// clampBundleLimit — boş/0/negatif/çöp → varsayılan, tavan üstü → TAVAN
// (oracle ucundaki "tavan üstü → varsayılan" yerine; kiosk'un "daha fazla"
// düğmesi tavanı ister, varsayılana düşmesi sessiz bir küçülme olurdu).
func clampBundleLimit(raw string, def, max int) int {
	n := parseInt(raw, def)
	if n <= 0 {
		return def
	}
	if n > max {
		return max
	}
	return n
}

// spanWindow — SAF, hooks.ts traceLogWindow'un aynası: min(start)−buffer ..
// max(end)+buffer. 0 zamanlar yok sayılır (Tempo satırında end 0 olabilir);
// kullanılabilir start VE end yoksa ok=false → log/oracle bacağı koşmaz
// (penceresiz sorgu = trace_id ile tüm-retention taraması; bilinçli YOK).
func spanWindow(spans []chstore.SpanRow, buffer time.Duration) (from, to time.Time, ok bool) {
	var minStart, maxEnd int64
	for _, sp := range spans {
		if sp.StartTime > 0 && (minStart == 0 || sp.StartTime < minStart) {
			minStart = sp.StartTime
		}
		if sp.EndTime > 0 && sp.EndTime > maxEnd {
			maxEnd = sp.EndTime
		}
	}
	if minStart == 0 || maxEnd == 0 {
		return time.Time{}, time.Time{}, false
	}
	return time.Unix(0, minStart).Add(-buffer), time.Unix(0, maxEnd).Add(buffer), true
}

type traceBundleTruncated struct {
	Spans  bool `json:"spans"`
	Logs   bool `json:"logs"`
	Oracle bool `json:"oracle"`
}

type bundleTruncationInput struct {
	SpanCapped     bool
	LogsTotal      int
	LogsLen        int
	LogsLowerBound bool
	OracleLen      int
	OracleLimit    int
}

// bundleTruncation — SAF. Oracle ucu kesilme sinyali taşımadığı için
// (total = len) "limit doldu" sezgisi; dürüst not: tam limit kadar satır
// olan bir trace de kesilmiş görünür.
func bundleTruncation(in bundleTruncationInput) traceBundleTruncated {
	return traceBundleTruncated{
		Spans:  in.SpanCapped,
		Logs:   in.LogsTotal > in.LogsLen || in.LogsLowerBound,
		Oracle: in.OracleLimit > 0 && in.OracleLen >= in.OracleLimit,
	}
}

type traceBundleOracle struct {
	Enabled  bool           `json:"enabled"`
	Logs     []oracleLogRow `json:"logs"`
	Total    int            `json:"total"`
	Degraded bool           `json:"degraded,omitempty"`
	Reason   string         `json:"reason,omitempty"`
}

func (s *Server) getTraceBundle(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(r.PathValue("id"))
	if id == "" {
		writeJSONError(w, http.StatusBadRequest, "trace id required")
		return
	}
	q := r.URL.Query()
	logLimit := clampBundleLimit(q.Get("logLimit"), traceBundleLogDefault, traceBundleLogMax)
	oracleLimit := clampBundleLimit(q.Get("oracleLimit"), oracleLogsDefaultLimit, oracleLogsMaxLimit)
	key := traceBundleKey(id, logLimit, oracleLimit)
	s.serveCached(w, r, key, traceBundleTTL, func(ctx context.Context) (any, error) {
		out, spans, err := s.traceDetailPayload(ctx, id)
		if err != nil {
			return nil, err
		}
		oracleOn := s.oracle != nil && s.oracle.HasEnabledSources()
		logsSlot := map[string]any{"total": 0, "logs": []*logstore.LogRecord{}}
		oracleSlot := traceBundleOracle{Enabled: oracleOn, Logs: []oracleLogRow{}}
		trunc := bundleTruncationInput{SpanCapped: out["spanCapped"] == true, OracleLimit: oracleLimit}

		from, to, ok := spanWindow(spans, traceBundleWindowBuffer)
		if ok {
			var (
				wg         sync.WaitGroup
				logsPage   *logstore.Page
				logsErr    error
				oracleRows []chstore.OracleErrorRow
				oracleErr  error
			)
			if s.logs != nil {
				wg.Add(1)
				go func() {
					defer wg.Done()
					logsPage, logsErr = logstore.SearchWithTimeout(ctx, s.logs,
						logstore.Filter{TraceID: id, From: from, To: to, Limit: logLimit}, 0)
				}()
			}
			if oracleOn {
				wg.Add(1)
				go func() {
					defer wg.Done()
					oracleRows, oracleErr = s.store.OracleErrorsByTrace(ctx, strings.ToLower(id), from, to, oracleLimit)
				}()
			}
			wg.Wait()
			if ctx.Err() != nil {
				return nil, ctx.Err() // istemci gitti: degraded gövde cache'lenmesin (→ 499)
			}
			switch {
			case s.logs == nil:
			case logsErr != nil:
				reason := "log backend error"
				if errors.Is(logsErr, logstore.ErrBackendSlow) {
					reason = "log backend slow/unreachable"
				}
				log.Printf("[trace-bundle] logs (backend=%s, trace=%q): %v", s.logs.Backend(), id, logsErr)
				logsSlot["degraded"] = true
				logsSlot["reason"] = reason
			default:
				if logsPage.Logs == nil {
					logsPage.Logs = []*logstore.LogRecord{} // null yerine [] — FE .map()'liyor
				}
				logsSlot = logsSearchPayload(logsPage)
				trunc.LogsTotal, trunc.LogsLen, trunc.LogsLowerBound = logsPage.Total, len(logsPage.Logs), logsPage.TotalIsLowerBound
			}
			if oracleOn {
				if oracleErr != nil {
					log.Printf("[trace-bundle] oracle (trace=%q): %v", id, oracleErr)
					oracleSlot.Degraded = true
					oracleSlot.Reason = "oracle backend error"
				} else {
					names := s.oracleSourceNames()
					for _, row := range oracleRows {
						oracleSlot.Logs = append(oracleSlot.Logs, oracleLogRowFromStore(row, names[row.SourceID]))
					}
					oracleSlot.Total = len(oracleSlot.Logs)
					trunc.OracleLen = oracleSlot.Total
				}
			}
			out["window"] = map[string]int64{"from": from.UnixNano(), "to": to.UnixNano()}
		}
		out["logs"] = logsSlot
		out["oracle"] = oracleSlot
		out["truncated"] = bundleTruncation(trunc)
		out["logLimit"] = logLimit
		return out, nil
	})
}
