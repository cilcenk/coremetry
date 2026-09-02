package api

import (
	"fmt"
	"os"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/cilcenk/coremetry/internal/thanos"
)

// thanos_mdp_test.go — v0.10.287 (chart audit D2 / Dilim 1.6): iki trend
// ucu istemci bütçesini okur, basamağa snap eder, ANAHTARA yazar
// (v0.5.187: cache anahtarı tüm girdileri hash'ler) ve Thanos'a geçirir.
// FE basamak listesi Go ile çivili (route_pins_test deseni).

func TestThanosTrendHandlersHonourMaxDataPoints(t *testing.T) {
	src := readAPISourceNoComments(t, "thanos_handlers.go")
	for _, h := range []struct{ fn, call string }{
		{"func (s *Server) getClusterDeployTrend(", "s.thanos.DeployTrend(qctx, cfg, ns, deploy, metric, byPod, from, to, mdp)"},
		{"func (s *Server) getClusterNamespacePodsTrend(", "s.thanos.NamespacePodsTrend(qctx, cfg, namespace, from, to, mdp)"},
	} {
		i := strings.Index(src, h.fn)
		if i < 0 {
			t.Fatalf("%s bulunamadı", h.fn)
		}
		body := src[i:]
		if j := strings.Index(body[1:], "\nfunc "); j > 0 {
			body = body[:j+1]
		}
		for _, want := range []string{`thanos.TrendMaxDataPointsRung(parseInt(q.Get("maxDataPoints"), 0))`, ":mdp=%d", h.call} {
			if !strings.Contains(body, want) {
				t.Errorf("%s: %q yok — bütçe okunmuyor / anahtara girmiyor / geçmiyor", h.fn, want)
			}
		}
	}
}

func TestThanosMDPRungsMatchFrontend(t *testing.T) {
	b, err := os.ReadFile("../../frontend/src/lib/chartStep.ts")
	if err != nil {
		t.Fatalf("chartStep.ts okunamadı: %v", err)
	}
	m := regexp.MustCompile(`(?s)export const THANOS_MDP_RUNGS = \[(.*?)\]`).FindSubmatch(b)
	if m == nil {
		t.Fatal("THANOS_MDP_RUNGS bulunamadı — yeniden adlandırıldıysa bu pin de taşınmalı")
	}
	var fe []int
	for _, tok := range regexp.MustCompile(`\d+`).FindAllString(string(m[1]), -1) {
		n, _ := strconv.Atoi(tok)
		fe = append(fe, n)
	}
	if fmt.Sprint(fe) != fmt.Sprint(thanos.TrendMaxDataPointsRungs) {
		t.Errorf("FE basamakları %v ≠ Go %v", fe, thanos.TrendMaxDataPointsRungs)
	}
}
