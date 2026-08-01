package chstore

import (
	"os"
	"strings"
	"testing"
	"time"
)

// v0.9.493 (operator-reported, prod: "/deploys 30 saniye sonra timeout") —
// v0.9.478 lookback'i filo-geneli taramayı +24h yapınca ham spans yolu
// (satır başına 14 indexOf'lu effectiveVersionExpr) sürücünün 30s
// ReadTimeout'unu aşıyordu; 5 dakikalık pencerede bile sayfa sonsuz
// spinner'daydı. Sözleşme:
//
//  1. GetDeploysInWindow MV hızlı yolu dener (deployMVCovers kapısı,
//     v0.9.249 deseni) — service_version_5m'den okur.
//  2. MV sorgusu hüküm sözleşmesini korur: HAVING first_seen >= from
//     (pencerede DOĞAN sürümler; v0.9.478'in düzelttiği totoloji geri
//     gelmesin) + minMerge KESİN first_seen (v0.9.250).
//  3. Ham fallback'in max_execution_time'ı sürücü ReadTimeout'unun
//     (30s, v0.8.340) ALTINDA kalır — taşımanın veremeyeceği süre
//     vaat edilmez.
func TestDeploysWindowMVContract(t *testing.T) {
	for _, want := range []string{
		"FROM service_version_5m",
		"minMerge(first_seen_state)",
		"HAVING version != ''",
		"first_seen >= ?",
		"ORDER BY first_seen DESC",
		"LIMIT ?",
		"finalizeAggregation(span_count_state)",
	} {
		if !strings.Contains(deploysWindowMVSQL, want) {
			t.Errorf("deploysWindowMVSQL %q içermiyor — MV yolu hüküm/sıralama sözleşmesinden sapmış", want)
		}
	}
}

func TestDeploysWindowRawCapUnderDriverTimeout(t *testing.T) {
	src, err := os.ReadFile("deploys.go")
	if err != nil {
		t.Fatal(err)
	}
	s := string(src)
	for _, banned := range []string{"maxExecSec = 60", "maxExecSec = 30"} {
		if strings.Contains(s, banned) {
			t.Errorf("deploys.go %q içeriyor — 30s sürücü ReadTimeout'unu aşan max_execution_time istemci tarafında kopan bağlantı demek (v0.9.493)", banned)
		}
	}
	if !strings.Contains(s, "maxExecSec = 25") {
		t.Error("ham fallback'in 25s tavanı kayıp (v0.9.493)")
	}
	// MV kapısı GetDeploysInWindow'da duruyor mu?
	if !strings.Contains(s, "if s.deployMVCovers(ctx, scanFrom) {") {
		t.Error("GetDeploysInWindow deployMVCovers kapısını kaybetmiş — filo-geneli ham tarama tekrar varsayılan yol olur (v0.9.493)")
	}
	// v0.9.493 verify bulgusu: global latch geri gelmesin — kapı her
	// çağrının KENDİ need'ini önbellekli earliest ile karşılaştırmalı.
	if strings.Contains(s, "deployMVReady") {
		t.Error("deployMVReady latch'i geri gelmiş — 24h'lik bir probe 48h'lik yolu erken açar, tarihsel pencereler MV'ye kaçar (v0.9.493 verify bulgusu)")
	}
}

// deployMVCoverageAnswers — kapının saf karşılaştırması (v0.9.493):
// earliest bilinmiyorsa kapalı; MV'nin en eski verisi need'den sonraysa
// kapalı (tarihsel pencere ham yola düşer); kapsıyorsa açık.
func TestDeployMVCoverageAnswers(t *testing.T) {
	base := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		name     string
		earliest time.Time
		need     time.Time
		want     bool
	}{
		{"earliest bilinmiyor → kapalı", time.Time{}, base, false},
		{"earliest need'den önce → açık", base.Add(-48 * time.Hour), base.Add(-24 * time.Hour), true},
		{"earliest == need → açık", base.Add(-24 * time.Hour), base.Add(-24 * time.Hour), true},
		{"MV genç, need eski (48h yolu erken açılmasın)", base.Add(-25 * time.Hour), base.Add(-48 * time.Hour), false},
		{"tarihsel pencere MV doğumundan eski → kapalı", base.Add(-10 * 24 * time.Hour), base.Add(-30 * 24 * time.Hour), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := deployMVCoverageAnswers(tc.earliest, tc.need); got != tc.want {
				t.Fatalf("deployMVCoverageAnswers(%v, %v) = %v, want %v", tc.earliest, tc.need, got, tc.want)
			}
		})
	}
}
