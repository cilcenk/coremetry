package api

// family_traces.go — v0.10.465 (CoSRE sohbet paritesi D2): "hatalı / yavaş
// trace'ler(i getir)" + AİLE (ya da açık 2+ ad, ya da tek servis).
//
// Ölçüm (v0.10.460 probu): "mobile bff son hatalı traceler" ve "mobile
// bff'nin son 1 saatteki hatalı trace'lerini getir" aile rotasına
// (family_health: yan yana RED) düşüyordu — operatör trace LİSTESİ istemişti;
// "mobile bff yavaş traceler" iki adayla ask_service oluyordu, oysa "mobile
// bff" ikisini birden kastediyor. "checkout hatalı traceler" ise
// service_health'e çöküyordu.
//
// Kural: mesaj TRACE kökü taşıyor + hata ya da yavaş sinyali → trace listesi;
// kapsam = açık 2+ ad > ad-parçası ailesi > açık tek servis > sayfa
// bağlamı. Trace kökü yoksa ("mobile bff hataları") aile sağlığı AYNEN
// kalır. Liste tek sorgu: service IN (...) çipi (FilterExpr, /traces'ın
// kendi `filters=` kodeği) + hasError; yavaş → duration desc, hatalı →
// zaman desc (en yeni hatalar). Link aynı görünümü açar.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/cilcenk/coremetry/internal/chstore"
)

const guidedFamilyTraces guidedIntent = "family_traces"

const familyTracesMaxServices = 40 // extractServiceFamily tavanıyla aynı

func routeFamilyTraces(msg string, toks []string, svc, env string, services, envs []string, ctxService string) (guidedRoute, bool) {
	if !tokenHasPrefix(toks, "trace") {
		return guidedRoute{}, false
	}
	errSig, slowSig := hasErrorSignal(toks), hasSlowTraceSignal(msg)
	if !errSig && !slowSig {
		return guidedRoute{}, false
	}
	var fam []string
	switch {
	case svc != "":
		if multi := extractServiceEntities(msg, services); len(multi) >= 2 {
			fam = multi
		} else {
			fam = []string{svc}
		}
	default:
		fam = extractServiceFamily(msg, services, envs)
		if len(fam) == 0 {
			if opts := serviceCandidates(msg, services, envs, familyTracesMaxServices); len(opts) > 0 {
				fam = opts
			} else if ctxService != "" {
				fam = []string{ctxService}
			}
		}
	}
	if len(fam) == 0 {
		return guidedRoute{}, false
	}
	// TEK servis + yalnız yavaş → mevcut slow_traces rotası AYNEN (kendi
	// bundle'ı, çipleri, linkleri; TestAskServiceChipsRoundTrip pini). Bu
	// kademe tek serviste yalnız HATALI liste için yenidir ("checkout hatalı
	// trace'ler" eskiden service_health'e çöküyordu).
	if len(fam) == 1 && !errSig {
		return guidedRoute{}, false
	}
	if len(fam) > familyTracesMaxServices {
		fam = fam[:familyTracesMaxServices]
	}
	r := guidedRoute{Intent: guidedFamilyTraces, Family: fam, Env: env, TraceErrorsOnly: errSig && !slowSig}
	if len(fam) == 1 {
		r.Service = fam[0]
	}
	return r, true
}

// familyTraceFilter — tek sorgu: service IN (...) çipi + hasError; yavaş →
// duration desc, hatalı → zaman desc (en yeni hatalar önce).
func familyTraceFilter(services []string, errorsOnly bool, env string, from, to time.Time) chstore.TraceFilter {
	f := chstore.TraceFilter{Env: env, From: from, To: to, Limit: 10, CountMode: "skip", HasError: errorsOnly, Sort: "duration", Order: "desc"}
	if errorsOnly {
		f.Sort = "time"
	}
	if len(services) == 1 {
		f.Service = services[0]
	} else {
		f.Filters = []chstore.FilterExpr{{Key: "service", Op: "IN", Values: services}}
	}
	return f
}

// familyTracesHref — /traces linki; aynı süzgeç (Traces.tsx `filters=` JSON
// kodeği + hasError + sort/order).
func familyTracesHref(services []string, errorsOnly bool) string {
	q := url.Values{}
	if len(services) == 1 {
		q.Set("service", services[0])
	} else {
		b, _ := json.Marshal([]chstore.FilterExpr{{Key: "service", Op: "IN", Values: services}})
		q.Set("filters", string(b))
	}
	if errorsOnly {
		q.Set("hasError", "true")
		q.Set("sort", "time")
	} else {
		q.Set("sort", "duration")
	}
	q.Set("order", "desc")
	return "/traces?" + q.Encode()
}

func familyScopeTR(services []string) string {
	if len(services) == 1 {
		return services[0]
	}
	if len(services) <= 3 {
		return strings.Join(services, ", ")
	}
	return fmt.Sprintf("%s, %s … (%d servis)", services[0], services[1], len(services))
}

func renderFamilyTracesEvidenceTR(rows []chstore.TraceRow, services []string, errorsOnly bool, env string, rangeS int64) string {
	kind, order := "En yavaş trace'ler", "duration'a göre"
	if errorsOnly {
		kind, order = "Hatalı trace'ler", "en yeniden eskiye"
	}
	scope := familyScopeTR(services)
	if env != "" {
		scope += ", ortam: " + env
	}
	var b strings.Builder
	if len(rows) == 0 {
		fmt.Fprintf(&b, "%s (son %s; kapsam: %s): bu pencerede trace bulunamadı.\n", kind, fmtAgoTR(rangeS), scope)
		return b.String()
	}
	fmt.Fprintf(&b, "%s (son %s; kapsam: %s; %s):\n", kind, fmtAgoTR(rangeS), scope, order)
	for _, r := range rows {
		flag := ""
		if r.HasError {
			flag = ", HATA"
		}
		fmt.Fprintf(&b, "- %s · %.0fms — %s / %s (%d span%s) trace=%s\n",
			time.Unix(0, r.StartTime).UTC().Format("15:04:05"), r.DurationMs, r.ServiceName, r.RootName, r.SpanCount, flag, r.TraceID)
	}
	if len(services) > 1 {
		fmt.Fprintf(&b, "Kapsam %d servis: %s\n", len(services), strings.Join(services, ", "))
	}
	return b.String()
}

func (s *Server) guidedFamilyTracesBundle(ctx context.Context, emit func(string, any), route *guidedRoute, from, to time.Time, rangeS int64) (string, string, error) {
	mode := "slow"
	if route.TraceErrorsOnly {
		mode = "errors"
	}
	n := emitGuidedStep(emit, "family_traces", withEnvArg(fmt.Sprintf(`{"services":%d,"mode":%q}`, len(route.Family), mode), route.Env))
	rows, _, _, err := s.store.GetTraces(ctx, familyTraceFilter(route.Family, route.TraceErrorsOnly, route.Env, from, to))
	if err != nil {
		emitGuidedStepResult(emit, n, "family_traces", "", err)
		return "", "", err
	}
	ev := renderFamilyTracesEvidenceTR(rows, route.Family, route.TraceErrorsOnly, route.Env, rangeS)
	emitGuidedStepResult(emit, n, "family_traces", ev, nil)
	src := fmt.Sprintf("%d servis kapsamlı trace listesi (son %s)", len(route.Family), fmtAgoTR(rangeS))
	if route.TraceErrorsOnly {
		src = fmt.Sprintf("%d servis kapsamlı hatalı trace listesi (son %s)", len(route.Family), fmtAgoTR(rangeS))
	}
	if route.Env != "" {
		src += ", ortam: " + route.Env
	}
	return ev, src, nil
}
