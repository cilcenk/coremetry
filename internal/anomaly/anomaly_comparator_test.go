package anomaly

import (
	"testing"
	"time"

	"github.com/cilcenk/coremetry/internal/chstore"
)

// v0.9.978 (operatör kararı: "P1 kalsın") — DÜŞÜŞ yönlü anomaliler.
//
// v0.9.976 ters-çevirmeyi kuralın comparator'ına bağladı ve haklı olarak
// comparator'sız satırlarda çevirmeyi kapattı. Ama anomali satırlarında
// Threshold bir İHLAL EŞİĞİ değil, BASELINE MEDYANI: value/threshold
// "baseline'ın kaç katı" demek. Düşüş yönlü bir olayda (trafik baseline'ın
// binde birine iner) oran doğal olarak <1 olur ve çevirme olmadan bu
// satırlar "küçük sapma" gibi sıralanırdı.
//
// Ölçüm (canlı, 2026-08-12): ters çevrilmiş 299 critical satırın 239'u tam
// olarak bu aile (metric=request_rate, kural YOK — hepsi anomali üreticisi).
// Kalan 60'ı ('>' kurallı error_rate/db_p99/http_p99) v0.9.976'nın kestiği
// SAHTE P1'ler ve kesik AYNEN duruyor.
func TestAnomalyComparatorFromDirection(t *testing.T) {
	cases := []struct {
		direction string
		want      string
	}{
		{"dropped", "<"},
		{"spiked", ">"},
		{"", ">"}, // decideAnomaly açık bir karar döndürmediyse yükseliş varsayımı
	}
	for _, c := range cases {
		if got := anomalyComparator(c.direction); got != c.want {
			t.Errorf("anomalyComparator(%q) = %q, %q bekleniyordu", c.direction, got, c.want)
		}
	}
}

// TestAnomalyDirectionDrivesPriority — uçtan uca: yön → comparator →
// öncelik. chstore.computePriority'nin kendisini (EnrichProblemsWithPriority
// üzerinden) çağırıyor ki kopya kural olmasın.
func TestAnomalyDirectionDrivesPriority(t *testing.T) {
	fresh := time.Now().Add(-10 * time.Minute).UnixNano() // bayat-critical yolu kapalı

	cases := []struct {
		name             string
		direction        string
		sev              string
		value, baseline  float64
		want             string
		wantFlipInReason bool
	}{
		// TRAFİK ÇÖKÜŞÜ — operatörün P1 kalmasını istediği vaka.
		// current 0.12 rps, baseline 12 rps = baseline'ın %1'i → 100× sapma.
		{"trafik çöküşü (0.12/12) → P1", "dropped", "critical", 0.12, 12, "P1", true},
		// Sınırdaki düşüş: baseline'ın %60'ı = 1.67× → büyük ihlal DEĞİL.
		{"hafif düşüş (7.2/12) → P2", "dropped", "critical", 7.2, 12, "P2", false},
		// SIÇRAMA — oran zaten >1, çevirme yok, davranış değişmedi.
		{"sıçrama (48/12 = 4×) → P1", "spiked", "critical", 48, 12, "P1", true},
		{"küçük sıçrama (18/12 = 1.5×) → P2", "spiked", "critical", 18, 12, "P2", false},
		// TAM KAYIP comparator'dan bağımsız (v0.9.825): sıfır her hâlükârda P1.
		{"trafik tamamen kesildi (0/12) → P1", "dropped", "critical", 0, 12, "P1", false},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p := chstore.Problem{
				Severity: c.sev, Status: "open", Metric: "request_rate",
				Value: c.value, Threshold: c.baseline,
				Comparator: anomalyComparator(c.direction),
				StartedAt:  fresh,
			}
			out := chstore.EnrichProblemsWithPriority([]chstore.Problem{p})
			if got := out[0].Priority; got != c.want {
				t.Errorf("öncelik = %s (%q), %s bekleniyordu — yön %q, %v/%v",
					got, out[0].PriorityReason, c.want, c.direction, c.value, c.baseline)
			}
		})
	}
}

// TestRuleFalseP1StaysCut — v0.9.976'nın pini bu katmanda da yeşil kalmalı.
//
// Anomali yönünü comparator'a yazmak, KURAL yollarındaki '>' comparator'lı
// eşik-altı satırları yeniden P1 yapmamalı: canlı vaka sms-service
// error_rate 3.922 / eşik 15.
func TestRuleFalseP1StaysCut(t *testing.T) {
	p := chstore.Problem{
		Severity: "critical", Status: "open", Metric: "error_rate",
		Value: 3.922, Threshold: 15, Comparator: ">",
		StartedAt: time.Now().Add(-10 * time.Minute).UnixNano(),
	}
	out := chstore.EnrichProblemsWithPriority([]chstore.Problem{p})
	if out[0].Priority != "P2" {
		t.Errorf("'>' kuralı eşiğin altında %s (%q) — P2 kalmalıydı; anomali "+
			"düzeltmesi sahte P1 kesiğini geri açmamalı",
			out[0].Priority, out[0].PriorityReason)
	}
}
