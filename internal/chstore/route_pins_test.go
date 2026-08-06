package chstore

import (
	"os"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// route_pins_test.go — KATMANLAR-ARASI yönlendirme pinleri (v0.9.705,
// parite FAZ 1 dilim 1).
//
// Parite taban çizgisi (docs/charts/parity-baseline.md §C) yönlendirme
// mantığını beş karar noktasına dağılmış ölçtü ve "seçim mantığı tek
// yerde" şartını ihlalde işaretledi. Noktalar farklı kaynaklara hizmet
// ettiği için tek fonksiyona İNDİRİLMEDİ (o ayrı ve riskli bir dilim);
// bu testler daha sinsi hastalığı bitiriyor: noktaların paylaştığı
// eşiklerin BİRBİRİNDEN HABERSİZ kopyalar olması.
//
// Pin yazılırken GERÇEK drift bulundu: FE STEP_RUNGS'ta 14400 vardı,
// metricStepLadder'da yoktu → FE 4 saatlik adım isteyince metrik tarafı
// 21600'e yuvarlıyordu; span ve metrik grafikleri aynı sayfada FARKLI
// zaman kafesine oturuyordu. Kimse bildirmemişti — çünkü iki grafiği
// üst üste koyup bucket sınırı saymadıkça görünmez.

// feStepRungs — chartStep.ts'ten STEP_RUNGS dizisini çıkarır. Go testi
// TS kaynağını okuyor; ters yönün emsali logBridgeTab.test.ts (TS testi
// Go kaynağını okur). İki dilin sabitlerini tek testte buluşturmanın
// evdeki yolu bu.
func feStepRungs(t *testing.T) []int {
	t.Helper()
	b, err := os.ReadFile("../../frontend/src/lib/chartStep.ts")
	if err != nil {
		t.Fatalf("chartStep.ts okunamadı: %v", err)
	}
	m := regexp.MustCompile(`(?s)export const STEP_RUNGS = \[(.*?)\];`).FindSubmatch(b)
	if m == nil {
		t.Fatal("STEP_RUNGS bulunamadı — yeniden adlandırıldıysa bu pin de taşınmalı")
	}
	var out []int
	for _, tok := range regexp.MustCompile(`\d+`).FindAllString(string(m[1]), -1) {
		n, _ := strconv.Atoi(tok)
		out = append(out, n)
	}
	if len(out) < 10 {
		t.Fatalf("STEP_RUNGS şüpheli kısa (%d) — çıkarım bozulmuş olabilir", len(out))
	}
	return out
}

// FE'nin her basamağı metrik merdiveninde DE olmalı: aynı sayfadaki span
// ve metrik grafikleri aynı adım istediğinde AYNI kafese oturmalı.
// (Merdivende fazladan basamak olması serbest — 20/10800/21600 gibi ara
// değerler FE'nin üretmediği ama sunucunun kabul ettiği adımlar.)
func TestFEStepRungsSubsetOfMetricLadder(t *testing.T) {
	ladder := map[int]bool{}
	for _, v := range metricStepLadder {
		ladder[v] = true
	}
	for _, r := range feStepRungs(t) {
		if !ladder[r] {
			t.Errorf("STEP_RUNGS %d sn metricStepLadder'da YOK — FE bu adımı istediğinde "+
				"metrik tarafı yukarı yuvarlar ve span/metrik kafesleri ayrışır "+
				"(14400 vakası, v0.9.705)", r)
		}
	}
}

// op-MV kapısı ile 5m rollup kademesi AYNI sayı olmak zorunda: ikisi de
// "5 dakikalık bucket'ı daha inceye bölemezsin" gerçeğinin ifadesi.
// Ayrışırlarsa bir yüzey MV'ye girerken diğeri ham yola düşer ve aynı
// soru iki farklı granülle cevaplanır.
func TestOpMVGateMatchesNarrowTier(t *testing.T) {
	var m5 int64
	for _, tier := range narrowTiers {
		if tier.baseSec == 300 {
			m5 = tier.baseSec
		}
	}
	if m5 == 0 {
		t.Fatal("narrowTiers'ta 5m kademesi yok — yapı değiştiyse pin taşınmalı")
	}
	if int64(opMVMinStepSec) != m5 {
		t.Errorf("opMVMinStepSec=%d ≠ narrow 5m kademesi=%d", opMVMinStepSec, m5)
	}
}

// spanmetric.go'da çıplak `step < 300` literali KALMAMALI — üç kopya
// v0.9.705'te sabite indirildi; dördüncüsü sessizce geri gelmesin.
func TestNoBareOpMVGateLiteral(t *testing.T) {
	b, err := os.ReadFile("spanmetric.go")
	if err != nil {
		t.Fatal(err)
	}
	src := stripGoLineComments(string(b))
	if strings.Contains(src, "step < 300") {
		t.Error("spanmetric.go'da çıplak `step < 300` var — opMVMinStepSec kullanılmalı " +
			"(v0.9.705: aynı literalin ÜÇ kopyası vardı)")
	}
	if got := strings.Count(src, "step < opMVMinStepSec"); got != 3 {
		t.Errorf("opMVMinStepSec kapısı %d yerde, beklenen 3 — kapı sayısı değiştiyse "+
			"bu pin bilinçli güncellenmeli", got)
	}
}

// Metrik okuma tabanı, en ince narrow rollup kademesiyle hizalı: 10 sn
// tabanının altına inen bir okuma zaten hiçbir rollup'a oturamaz.
func TestMetricFloorAlignsWithFinestTier(t *testing.T) {
	if minMetricStepSec != 10 {
		t.Errorf("minMetricStepSec=%d — 10s narrow kademesiyle hizası bozulduysa "+
			"bu bilinçli bir karar olmalı, sessiz bir kayma değil", minMetricStepSec)
	}
	if narrowTiers[0].baseSec != 10 {
		t.Errorf("en ince narrow kademe %d — minMetricStepSec ile hizalı değil", narrowTiers[0].baseSec)
	}
}
