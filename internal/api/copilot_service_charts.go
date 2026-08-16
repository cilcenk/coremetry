package api

import (
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/cilcenk/coremetry/internal/chstore"
	"github.com/cilcenk/coremetry/internal/copilot"
)

// copilotExplainCharts — Service → Details grafiklerinin AI özeti
// (onaylı mockup; Ⓐ toolbar = tüm kartlar, Ⓑ kart başlığı = tek kart).
//
// explain-service'ten (AI triage) AYRI bir yüzey:
//   - soru farklı ("şu an sağlıklı mı" değil, "bu pencerede ne oldu"),
//   - kanıt paketi daha geniş (deploy/rollout + op-delta + anomali),
//   - ai_calls.surface = "explain-charts" (aiSurfaceFromPath yolun 3.
//     segmentini alır), yani /ai'da maliyeti AYRI okunur.
//
// Küçük-model sözleşmesi: TÜM sinyaller burada, sunucuda toplanır; model
// tek geçişte yalnız ANLATIR (tool-loop yok). Toplama mantığının kendisi
// saf ve testli — explain_service_charts.go.
//
// Maliyet duruşu: AI üretimi CACHE'LENMEZ (non-determinist + her üretim
// bir ai_calls satırı = /ai görünürlüğü). Sadece UÇUŞTA-TEK üretim
// garantisi var: aynı (servis, kapsam, pencere) için eşzamanlı istekler
// singleflight ile tek LLM çağrısına iner. Prefetch sorguları MV'ye
// dayanıyor ve zaman-sınırlı; ayrı serveCached sarmalamaya gerek yok.
func (s *Server) copilotExplainCharts(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	service := strings.TrimSpace(q.Get("service"))
	if service == "" {
		http.Error(w, `{"error":"service required"}`, http.StatusBadRequest)
		return
	}
	scope := normalizeChartScope(q.Get("scope"))
	from, to := parseFromTo(r, time.Hour)

	// Uçuşta-tek anahtarı TÜM girdileri taşır (v0.5.187 dersinin
	// singleflight karşılığı): servis adı QueryEscape'ten geçer, yoksa
	// içindeki ':' iki farklı isteği aynı anahtara düşürebilirdi.
	key := fmt.Sprintf("explain-charts:%s:%s:%d:%d",
		url.QueryEscape(service), scope, from.UnixNano(), to.UnixNano())

	v, err, _ := s.sf.Do(key, func() (any, error) {
		in := s.buildServiceChartsInput(r, service, scope, from, to)
		out, nerr := s.copilotExplain(r, copilot.SystemPromptServiceCharts(), buildServiceChartsUser(in))
		return buildChartsResult(scope, in.Signals, out, nerr), nil
	})
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, v)
}

// buildServiceChartsInput — CH'den kanıt toplama. HER kaynak SOFT-FAIL:
// bir sinyal alınamazsa o bölüm prompt'ta hiç görünmez ve anlatım kalan
// kanıtla sürer. Tek bir yavaş/boş sorgu yüzünden operatörün hiç cevap
// alamaması, eksik cevap almasından kötüdür.
func (s *Server) buildServiceChartsInput(
	r *http.Request, service, scope string, from, to time.Time,
) serviceChartsInput {
	ctx := r.Context()
	in := serviceChartsInput{Service: service, Scope: scope, From: from, To: to}

	// RED — operasyon bazlı. Tek Multi çağrısı = tek CH taraması
	// (spanmetric MV yolu; uygun değilse kendi içinde sınırlı fallback).
	//
	// kind IN (server, consumer): Details panelleri v0.9.348'den beri
	// rootOnly çiziyor. Bu filtre olmadan anlatım, operatörün EKRANDA
	// GÖRMEDİĞİ giden çağrıları da sayardı — "grafikte yok ama AI
	// bahsediyor" sınıfı bir tutarsızlık (giriş-span ilkesi).
	svcFilter := []chstore.FilterExpr{
		{Key: "service.name", Op: "=", Values: []string{service}},
		{Key: "kind", Op: "IN", Values: []string{"server", "consumer"}},
	}
	redM, _, _ := s.store.QuerySpanMetricMulti(ctx, chstore.SpanMetricBatchFilter{
		Filters: svcFilter, From: from, To: to, GroupBy: []string{"name"},
		Aggs: []chstore.SpanMetricAggSpec{
			{Name: "rate", Aggregation: "rate"},
			{Name: "error_rate", Aggregation: "error_rate"},
			{Name: "p99", Aggregation: "p99", Field: "duration_ms"},
		},
	})
	in.RPS = summarizeSeries(redM["rate"], 3)
	in.ErrRate = summarizeSeries(redM["error_rate"], 3)
	in.P99 = summarizeSeries(redM["p99"], 3)

	// Deploy/rollout — grafiklerdeki ↻ işaretinin BESLEYİCİSİ ile aynı
	// yol (GetServiceRollouts), yani anlatımdaki saat ile çizgideki
	// işaret aynı olaydır. Pencerede birden çok varsa SON olan alınır ve
	// gerçek sürüm değişimi ("deploy") yeniden başlatmaya ("restart")
	// TERCİH EDİLİR: operatörün sorduğu soru neredeyse her zaman sürüm
	// değişimiyle ilgili (v0.8.405 kind ayrımı).
	if rr, err := s.store.GetServiceRollouts(ctx, service, from, to); err == nil && rr != nil {
		var pick *chstore.Rollout
		for i := range rr.Rollouts {
			ro := &rr.Rollouts[i]
			if pick == nil ||
				(ro.Kind == "deploy" && pick.Kind != "deploy") ||
				(ro.Kind == pick.Kind && ro.TimeUnixNs > pick.TimeUnixNs) {
				pick = ro
			}
		}
		if pick != nil {
			in.Signals.Deploy = &ChartDeploySignal{
				TimeUnixNs:    pick.TimeUnixNs,
				Kind:          pick.Kind,
				VersionBefore: pick.VersionBefore,
				VersionAfter:  pick.VersionAfter,
				PodsReplaced:  pick.PodsRemoved,
			}
		}
	}

	// Açık problemler — pencereye değil, "şu an açık" durumuna bakar
	// (kapanmış bir problem grafikteki dalgayı açıklamaz ama açık olan
	// operatörün bir sonraki adımıdır).
	if probs, err := s.store.ListProblems(ctx, chstore.ProblemFilter{
		Service: service, Status: "open", Limit: 5,
	}); err == nil {
		for _, p := range probs {
			in.Signals.Problems = append(in.Signals.Problems, ChartProblemSignal{
				ID: p.ID, Title: p.RuleName, Severity: p.Severity, Priority: p.Priority,
				StartedAt: p.StartedAt, Metric: p.Metric, Value: p.Value, Threshold: p.Threshold,
			})
		}
	}

	// Anomaliler — pencerede BAŞLAYANLAR (FromNs/ToNs, v0.9.394 şeridiyle
	// aynı sözleşme). Services STRICT IN: servissiz satır eşleşmez.
	if evs, err := s.store.ListAnomalyEvents(ctx, chstore.ListAnomalyEventsFilter{
		Services: []string{service},
		FromNs:   from.UnixNano(), ToNs: to.UnixNano(),
		Limit: 5,
	}); err == nil {
		for _, e := range evs {
			in.Signals.Anomalies = append(in.Signals.Anomalies, ChartAnomalySignal{
				ID: e.ID, Kind: e.Kind, Pattern: e.Pattern,
				StartedAt: e.StartedAt, PeakRatio: e.PeakRatio, Status: e.Status,
			})
		}
	}

	// Op-delta — pencere vs bir önceki EŞ pencere. GetOperationSummaryCompared
	// MV-öncelikli ve v0.9.64'ün bucket-örtüşme düzeltmesini taşıyor
	// (prior penceresi, winStart'ı içeren 5dk kovasını DIŞLAR); kendi
	// elimizle ikinci bir pencere hesaplasaydık o düzeltmeyi kaybederdik.
	// normalized=false: grafikler ham operasyon adıyla çiziliyor, anlatım
	// da aynı adları anmalı.
	// env "" — the /ai charts-explain subject carries no env (OUT of the
	// env(a) scope, which is the rendered RED surfaces); op deltas stay
	// service-wide for the narration.
	if rows, err := s.store.GetOperationSummaryCompared(ctx, service, 0, from, to, false, ""); err == nil {
		in.Signals.OpDeltas, in.Signals.OtherOps = selectOpDeltas(rows, 3)
	}
	return in
}
