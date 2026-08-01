package anomaly

import (
	"strings"
	"testing"

	"github.com/cilcenk/coremetry/internal/chstore"
)

// prompt_golden_test.go — v0.9.518. SENARYO tabanlı prompt altın-kümesi.
//
// NE ÖLÇER, NE ÖLÇMEZ — bu ayrım önemli:
//   ÖLÇER: modele GİDEN girdiyi. Doğru sinyaller toplandı mı, denetim izi
//     doğru mu, uydurma yasağı yerinde mi, kanıt yokken prompt eski haline
//     dürüstçe düşüyor mu. Hepsi deterministik, model çağrısı GEREKTİRMEZ.
//   ÖLÇMEZ: modelin ÇIKTI kalitesini. Onun için ya insan değerlendirmesi
//     ya canlı bir koşum gerekir; ikisi de CI'da koşamaz.
//
// Yani bu küme "AI iyi cevap veriyor mu" sorusunu cevaplamaz; "AI'a iyi
// bir soru sorduk mu" sorusunu cevaplar. Kontrol edebildiğimiz yarı bu ve
// bugüne kadar hiç pinli değildi.

// senaryo — gerçekçi bir P1 şekli ve ondan beklenen prompt özellikleri.
type promptScenario struct {
	name     string
	problem  chstore.Problem
	deep     chstore.DeepEvidence
	wantIn   []string // prompt'ta MUTLAKA geçmeli
	wantNotIn []string // prompt'ta ASLA geçmemeli
}

func TestPromptGoldenScenarios(t *testing.T) {
	scenarios := []promptScenario{
		{
			name: "hata orani + exception + kanal kirilimi",
			problem: chstore.Problem{
				Service: "checkout", RuleName: "hata oranı", Metric: "error_rate",
				Severity: "critical", Value: 4.2, Threshold: 1,
			},
			deep: chstore.DeepEvidence{
				Checked: []chstore.CheckedSignal{
					{Family: "exceptions", Found: true, Detail: "3 exception grubu", Records: 3},
					{Family: "logs", Found: false, Detail: "bakıldı, kayıt yok"},
					{Family: "business", Found: true, Detail: "2 iş boyutu değeri", Records: 2},
				},
				Exceptions: []chstore.ExceptionGroup{{Type: "SocketTimeout", Message: "connect timed out", Occurrences: 412}},
				Business: map[string][]chstore.BusinessSlice{
					"CHANNEL_CODE": {{Value: "0012", Calls: 1200, Errors: 340, ErrPct: 28.3}},
				},
				CodeMeaning: map[string]string{"0012": "0012 - Mobil Kanal"},
			},
			wantIn: []string{
				"SORUŞTURMA",           // denetim izi başlığı
				"exceptions", "VAR",    // bulunan sinyal
				"logs", "yok",          // bulunmayan sinyal AÇIKÇA yok diyor
				"SocketTimeout",        // asıl kanıt
				"0012", "Mobil Kanal",  // iş boyutu + RAG'dan çözülen anlam
				"KURAL",                // uydurma yasağı
				"sebep olarak GÖSTERME",
			},
		},
		{
			name: "gecikme + heap doygunlugu",
			problem: chstore.Problem{
				Service: "payments", RuleName: "p99", Metric: "p99_ms",
				Severity: "critical", Value: 2100, Threshold: 800,
			},
			deep: chstore.DeepEvidence{
				Checked: []chstore.CheckedSignal{
					{Family: "saturation", Found: true, Detail: "2 pod heap örneği", Records: 2},
					{Family: "operations", Found: true, Detail: "5 operasyon", Records: 5},
				},
				Heap:    []chstore.CapacitySample{{Instance: "payments", Subkey: "pod-a", Usage: 94, Limit: 100}},
				SlowOps: []chstore.OperationSummary{{Name: "POST /pay", P99Ms: 1980, SpanCount: 5000}},
			},
			wantIn: []string{"SORUŞTURMA", "saturation", "pod-a", "POST /pay", "KURAL"},
		},
		{
			// EN ÖNEMLİ SENARYO: hiçbir sinyalde kanıt yok. Model burada
			// sebep uydurmaya en yatkın; prompt onu açıkça yasaklamalı.
			name: "hicbir sinyalde kanit yok",
			problem: chstore.Problem{
				Service: "ledger", RuleName: "hata oranı", Metric: "error_rate", Severity: "critical",
			},
			deep: chstore.DeepEvidence{
				Checked: []chstore.CheckedSignal{
					{Family: "exceptions", Found: false, Detail: "bakıldı, kayıt yok"},
					{Family: "logs", Found: false, Detail: "bakıldı, kayıt yok"},
				},
			},
			wantIn: []string{
				"SORUŞTURMA", "exceptions", "yok",
				"sebep uydurma", // yasak metni birebir orada
			},
		},
	}

	for _, sc := range scenarios {
		t.Run(sc.name, func(t *testing.T) {
			bundle := EvidenceBundle{Problem: sc.problem, Deep: sc.deep}
			got := buildProblemPrompt(sc.problem, bundle, nil)
			for _, w := range sc.wantIn {
				if !strings.Contains(got, w) {
					t.Errorf("prompt %q içermiyor:\n%s", w, got)
				}
			}
			for _, w := range sc.wantNotIn {
				if strings.Contains(got, w) {
					t.Errorf("prompt %q içermemeli:\n%s", w, got)
				}
			}
		})
	}
}

// Derin kanıt YOKKEN prompt v0.9.510 ÖNCESİ haliyle bayt-aynı olmalı.
// P2/P3 problemlerin tamamı bu yoldan geçiyor; buraya sızacak bir
// "SORUŞTURMA" başlığı modele "bakıldı ama bulunamadı" izlenimi verir —
// oysa hiç bakılmadı. Yanlış izlenim, sessiz kalitesizlik.
func TestPromptUnchangedWithoutDeepEvidence(t *testing.T) {
	p := chstore.Problem{Service: "checkout", RuleName: "r", Metric: "error_rate", Severity: "warning"}

	withEmpty := buildProblemPrompt(p, EvidenceBundle{Problem: p}, nil)
	if strings.Contains(withEmpty, "SORUŞTURMA") {
		t.Errorf("derin kanıt yokken SORUŞTURMA bloğu basılmamalı:\n%s", withEmpty)
	}
	if strings.Contains(withEmpty, "KURAL") {
		t.Errorf("derin kanıt yokken uydurma yasağı da basılmamalı (bağlamsız kural gürültü):\n%s", withEmpty)
	}
}

// Denetim izi ile anlatım arasındaki ÇELİŞKİ yakalanabilir olmalı: iz
// "bakıldı, kayıt yok" derken prompt o sinyali kanıt gibi sunmamalı.
// Bu testin işi, izin bulunmayan sinyali "VAR" diye göstermediğini pinlemek.
func TestAuditTrailDoesNotClaimMissingSignals(t *testing.T) {
	var sb strings.Builder
	renderDeepEvidence(&sb, chstore.DeepEvidence{
		Checked: []chstore.CheckedSignal{
			{Family: "saturation", Found: false, Detail: "bakıldı, kayıt yok"},
		},
	})
	out := sb.String()
	if strings.Contains(out, "saturation: VAR") {
		t.Errorf("bulunmayan sinyal VAR olarak işaretlenmiş:\n%s", out)
	}
	if !strings.Contains(out, "saturation: yok") {
		t.Errorf("bulunmayan sinyal açıkça 'yok' demeli:\n%s", out)
	}
}
