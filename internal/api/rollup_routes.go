package api

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/cilcenk/coremetry/internal/chstore"
)

// rollup_routes.go — rollup okuma uçları (Aşama 2, docs/rollup-design.md §5).
// api.go BÜYÜTÜLMEZ kısıtı: tüm rollup route'ları burada, api.go yalnız
// registerRollupRoutes(mux, s) tek satırını çağırır.
//
// GET /api/rollup/red
//
//	from, to           unix-ns (diğer uçlarla aynı parseTime)
//	maxDataPoints      panel nokta bütçesi (0 → 1500; clamp 4000)
//	service, kind, status, endpoint, channel, function   eşitlik filtreleri
//	groupBy            tek boyut (service_name|span_kind|status_code|endpoint|channel_code|function_code)
//	exact=1            SLO-hassas tdigest şartı (geniş boyutla çelişirse 400)
//	maxGroups          groupBy grubu tavanı (0 → 20)
//
// Yanıt zarfı: {plan:{table,stepSeconds,quantileMode,reason,partialWindow},
// truncated, series:[{group,points:[{ts,calls,errors,avgMs,p50Ms,p95Ms,p99Ms}]}]}
// stepSeconds SÖZLEŞMENİN parçası — grafik-audit Faz B kontratıyla aynı.
// Tablolar yoksa (migrations elle uygulanmadı) 424 + dürüst mesaj; boot ve
// ingest bu uçtan tamamen bağımsızdır.
func registerRollupRoutes(mux *http.ServeMux, s *Server) {
	mux.HandleFunc("GET /api/rollup/red", s.getRollupRED)
}

func (s *Server) getRollupRED(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	from := parseTime(q.Get("from"))
	to := parseTime(q.Get("to"))
	if from.IsZero() || to.IsZero() {
		to = time.Now()
		from = to.Add(-1 * time.Hour)
	}
	mdp, _ := strconv.Atoi(q.Get("maxDataPoints"))

	f := chstore.RollupSeriesFilter{
		Service:  strings.TrimSpace(q.Get("service")),
		Kind:     strings.TrimSpace(q.Get("kind")),
		Status:   strings.TrimSpace(q.Get("status")),
		Endpoint: strings.TrimSpace(q.Get("endpoint")),
		Channel:  strings.TrimSpace(q.Get("channel")),
		Function: strings.TrimSpace(q.Get("function")),
		GroupBy:  strings.TrimSpace(q.Get("groupBy")),
	}
	f.MaxGroups, _ = strconv.Atoi(q.Get("maxGroups"))

	// Aile seçimini süren boyut kümesi: groupBy + DOLU geniş filtreler.
	// (Dar filtreler aileyi değiştirmez ama bilinmeyen-ad denetimi için
	// seçiciye groupBy üzerinden zaten giriyor.)
	dims := []string{}
	if f.GroupBy != "" {
		dims = append(dims, f.GroupBy)
	}
	for dim, val := range map[string]string{
		"endpoint": f.Endpoint, "channel_code": f.Channel, "function_code": f.Function,
	} {
		if val != "" {
			dims = append(dims, dim)
		}
	}

	plan, err := chstore.PickRollup(chstore.RollupQuery{
		From: from, To: to, MaxDataPoints: mdp,
		Dims: dims, NeedExactQuant: q.Get("exact") == "1",
	}, time.Now())
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	// Tablo yokluğu (migrations elle uygulanmadı) serveCached'in ÖNÜNDE
	// denetlenir: 424 cache'e yazılmaz, mesaj dürüst. writeErr typed-status
	// bilmediği için bu erken-dönüş tek doğru yer.
	if ready, perr := s.store.RollupTableReady(r.Context(), plan.Table); perr == nil && !ready {
		http.Error(w, "rollup tabloları bu ClickHouse'ta yok — migrations/0001-0002 uygulanmalı (docs/rollup-design.md §6)",
			http.StatusFailedDependency)
		return
	}

	// Cache anahtarı TÜM girdileri taşır (v0.5.187 kuralı); pencere 30s
	// grid'e cacheBucket ile oturur (kardeş handler'lar gibi).
	key := fmt.Sprintf("rollup-red:v1:t=%s:s=%d:svc=%s:k=%s:st=%s:ep=%s:ch=%s:fn=%s:g=%s:mg=%d:w=%s",
		plan.Table, plan.StepSeconds, f.Service, f.Kind, f.Status,
		f.Endpoint, f.Channel, f.Function, f.GroupBy, f.MaxGroups, cacheBucket(from, to))
	s.serveCached(w, r, key, 30*time.Second, func(ctx context.Context) (any, error) {
		series, truncated, err := s.store.QueryRollupRED(ctx, plan, f, from, to)
		if err != nil {
			return nil, err
		}
		return map[string]any{
			"plan":      plan,
			"truncated": truncated,
			"series":    series,
		}, nil
	})
}
