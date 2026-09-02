package api

// trace_routes.go — v0.10.275 (trace view Dilim 1b): GET /api/traces/{id}
// api.go'dan buraya TAŞINDI (route_registry.go defteri, init kaydı; api.go
// kısaldı). Yanıta `analysis` (chstore.TraceAnalysis) eklendi —
// docs/audit/trace-view.md §4: ağaç iki kez istemcide kuruluyor ve iki
// farklı öz-süre tanımı yaşıyordu; tek kaynak burada. Alan EKLEME, sürüm
// DEĞİL: eski istemci alanı yok sayar; cache anahtarı v3.

import (
	"context"
	"net/http"
	"time"

	"github.com/cilcenk/coremetry/internal/chstore"
)

func init() { registerRoutesExtra("trace", (*Server).registerTraceRoutes) }

func (s *Server) registerTraceRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/traces/{id}", s.getTrace)
}

func (s *Server) getTrace(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		http.Error(w, "trace id required", http.StatusBadRequest)
		return
	}
	// 30s cache. A trace is immutable once stored, but a
	// just-flipped Tempo backend setting should rescue stale
	// "not found" entries within a short window — hence the
	// short TTL. `source` distinguishes CH-resident vs Tempo-
	// fallback so the frontend can banner-tag the result.
	key := "trace:v3:" + id // v0.10.275 — analysis alanı eklendi; eski gövde yeni FE'ye servis edilmesin
	s.serveCached(w, r, key, 30*time.Second, func(ctx context.Context) (any, error) {
		// v0.9.632 — çözümleme TEK yerde (trace_resolve.go) ve sırası
		// operatör kararıyla ÖNCE TEMPO: elinde bir trace_id varken TAM
		// trace istenir, Coremetry'nin örneklemesinden sağ kalan
		// parçası değil. Tempo yapılandırılmamışsa / bütçeyi aşarsa /
		// bulamazsa ClickHouse'a düşülür.
		//
		// Buradaki eski kopya (CH önce, ıskalarsa Tempo) explain'in
		// Tempo-only bir trace'te 404 vermesinin sebebiydi.
		spans, src, err := s.resolveTraceSpans(ctx, id)
		if err != nil {
			return nil, err
		}
		if len(spans) > 0 {
			out := map[string]any{"traceId": id, "spans": spans, "source": src}
			// v0.9.457 (dürüstlük A2) — 50k tavanına çarpan trace "tam"
			// diye render ediliyordu: kesilen çocuklar orphan olup
			// TraceHonesty şeridi operatörün SDK'sını ("parent yok —
			// context propagation bozuk") suçluyordu. Tavan dolunca
			// söyle; gerçek span sayısı MV stub'ından (best-effort).
			//
			// Tavan yalnız ClickHouse yolunda anlamlı: 50k limiti
			// GetTrace'in LIMIT'i, Tempo yanıtının değil.
			capped := src == traceSourceCH && len(spans) >= 50000
			if capped {
				out["spanCapped"] = true
				if stub, ok := s.store.GetTraceAggregateStub(ctx, id); ok {
					out["spanTotal"] = stub.SpanCount
				}
			}
			// v0.10.275 (Dilim 1b) — ağaç + kritik yol + öz süre + servis özeti Go'da,
			// tek payload (chstore.BuildTraceAnalysis, saf). Frontend yalnız çizer;
			// 5000 span ≈ 1.4 ms (BenchmarkBuildTraceAnalysis5000).
			out["analysis"] = chstore.BuildTraceAnalysis(spans, capped)
			return out, nil
		}
		// v0.6.34 — operator-reported: /traces aggregate view
		// showed traces, clicking them opened an empty detail.
		// Root cause: trace_summary_5m MV has 90-day TTL while
		// raw `spans` has 30-day. Past day-30 the aggregate row
		// survives but the detail data is gone — and we returned
		// an opaque empty array. Check if the MV still holds the
		// trace and surface an honest "aged out of raw spans"
		// hint with the aggregate stats so the operator gets
		// SOMETHING useful instead of a blank pane.
		if stub, ok := s.store.GetTraceAggregateStub(ctx, id); ok {
			return map[string]any{
				"traceId": id,
				"spans":   []any{},
				"source":  "mv_only",
				"stub":    stub,
			}, nil
		}
		return map[string]any{"traceId": id, "spans": spans, "source": "clickhouse"}, nil
	})
}
