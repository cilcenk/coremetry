package anomaly

import (
	"os"
	"strings"
	"testing"

	"github.com/cilcenk/coremetry/internal/chstore"
)

// v0.9.1051 (Faz 0.3) regresyon pini — service_silent dedektörü.
// "Servis tamamen sustu" sınıfı varsayılan kurulumda tamamen kördü
// (request_rate izleme kapalı, gömülü kuralların hiçbiri rate değil) ve
// susan servisin açık anomalisi "recovered" gerekçesiyle kapanıyordu.
// Bu tablo iki sözleşmeyi mühürler: (1) istikrarlı-akışlı servisin 3
// sıfır kovası silent=true + baseline medyanı döner, (2) batch/cron
// (baseline'ı delikli) ve trafiği süren servisler ASLA silent olmaz.
func TestSilenceVerdict(t *testing.T) {
	steady := func(n int, v float64) []float64 {
		out := make([]float64, n)
		for i := range out {
			out[i] = v
		}
		return out
	}

	t.Run("istikrarlı servis + 3 sıfır kuyruk → silent, baseline medyanı", func(t *testing.T) {
		rates := append(steady(20, 4.0), 0, 0, 0)
		silent, base := silenceVerdict(rates, 3, 0.9)
		if !silent || base != 4.0 {
			t.Fatalf("got silent=%v base=%.2f, want true/4.0", silent, base)
		}
	})

	t.Run("kuyrukta tek kova bile trafik varsa silent değil", func(t *testing.T) {
		rates := append(steady(20, 4.0), 0, 0.2, 0)
		if silent, _ := silenceVerdict(rates, 3, 0.9); silent {
			t.Fatal("trafik taşıyan kuyruk silent sayıldı")
		}
	})

	t.Run("batch/cron: delikli baseline (aktif pay < %90) → asla silent", func(t *testing.T) {
		base := steady(20, 3.0)
		for i := 0; i < 6; i++ { // 20 kovanın 6'sı sıfır → pay 0.7
			base[i*3] = 0
		}
		rates := append(base, 0, 0, 0)
		if silent, _ := silenceVerdict(rates, 3, 0.9); silent {
			t.Fatal("delikli baseline'lı servis silent sayıldı — batch/cron FP sınıfı")
		}
	})

	t.Run("kısa geçmiş (< trailing+minSamples) → silent değil", func(t *testing.T) {
		rates := append(steady(5, 4.0), 0, 0, 0)
		if silent, _ := silenceVerdict(rates, 3, 0.9); silent {
			t.Fatal("yetersiz geçmişle silent kararı verildi")
		}
	})

	t.Run("hiç trafik görmemiş seri (hep sıfır) → silent değil", func(t *testing.T) {
		if silent, _ := silenceVerdict(steady(30, 0), 3, 0.9); silent {
			t.Fatal("hiç trafiksiz servis silent sayıldı")
		}
	})
}

// v0.10.543 — operatör: "Anomali service silent'lara ihtiyacım yok. Olmasınlar."
// Dedektör VARSAYILAN KAPALI (nil ⇒ false); kapalıyken açık service_silent
// problemleri kapatılır; tik kapıyı ServiceSilentEnabled ile okur.
func TestServiceSilentDisabledByDefault(t *testing.T) {
	var c chstore.AnomalySensitivityConfig
	if c.ServiceSilentEnabled() {
		t.Fatal("nil ⇒ kapalı olmalı")
	}
	if chstore.DefaultAnomalySensitivity().ServiceSilentEnabled() {
		t.Fatal("varsayılan kapalı")
	}
	on := chstore.NormalizeAnomalySensitivity(chstore.AnomalySensitivityConfig{ServiceSilent: boolPtrT(true)})
	if !on.ServiceSilentEnabled() {
		t.Fatal("açıkça true → açık")
	}
	off := chstore.NormalizeAnomalySensitivity(chstore.AnomalySensitivityConfig{})
	if off.ServiceSilent == nil || *off.ServiceSilent {
		t.Fatal("normalize kapalıyı açık yazmalı (blob dürüst)")
	}
	all := []*chstore.Problem{
		{ID: "1", RuleID: "anomaly:api:service_silent", Service: "api"},
		{ID: "2", RuleID: "anomaly:api:error_rate", Service: "api"},
		nil,
		{ID: "", RuleID: "anomaly:x:service_silent"},
		{ID: "3", RuleID: "anomaly:pay:service_silent", Service: "pay"},
	}
	got := silentProblemsToResolve(all)
	if len(got) != 2 || got[0].ID != "1" || got[1].ID != "3" {
		t.Fatalf("yalnız açık service_silent: %+v", got)
	}
	src, err := os.ReadFile("anomaly.go")
	if err != nil {
		t.Fatal(err)
	}
	s := string(src)
	i := strings.Index(s, "if !sens.ServiceSilentEnabled() {")
	j := strings.Index(s, "d.resolveSilentProblems(ctx, snap)")
	k := strings.Index(s, "d.checkSilence(ctx, svc, rates, snap, sens)")
	if i < 0 || j < 0 || k < 0 || !(i < j && j < k) {
		t.Fatalf("tik kapısı: kapalı → çöz, açık → checkSilence (i=%d j=%d k=%d)", i, j, k)
	}
}

func boolPtrT(b bool) *bool { return &b }
