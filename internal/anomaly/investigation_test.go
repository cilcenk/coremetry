package anomaly

import (
	"strings"
	"testing"

	"github.com/cilcenk/coremetry/internal/chstore"
)

// v0.9.510 — CoSRE P1 soruşturması. Playbook SAF olduğu için sözleşmesi
// burada pinli: hangi problem şekli hangi sinyal ailelerini tetikler.
//
// Neden tablo-testi: dallanma "gerçek SRE refleksi"ni kodluyor (hata
// oranı → exception+log, gecikme → doygunluk+operasyon). Bir metrik adı
// eklenip yanlış dala düşerse soruşturma sessizce YANLIŞ kanıtı toplar ve
// model onu güvenle anlatır — sessiz kalitesizlik. Test o sessizliği kırar.

func TestInvestigationPlan(t *testing.T) {
	cases := []struct {
		name   string
		metric string
		rule   string
		want   []signalFamily
	}{
		// Hata ailesi
		{"error_rate", "error_rate", "Checkout hata oranı", []signalFamily{familyExceptions, familyLogs}},
		{"errors mutlak", "errors", "", []signalFamily{familyExceptions, familyLogs}},
		{"kural adı exception der", "custom_metric", "Exception spike", []signalFamily{familyExceptions, familyLogs}},

		// Gecikme ailesi — doygunluk ÖNCE (heap/GC sessiz yavaşlatıcıdır)
		{"p99", "p99_ms", "", []signalFamily{familySaturation, familyOperations}},
		{"p95", "p95_ms", "", []signalFamily{familySaturation, familyOperations}},
		{"avg", "avg_ms", "", []signalFamily{familySaturation, familyOperations}},
		{"latency", "latency_ms", "", []signalFamily{familySaturation, familyOperations}},

		// Ayakta mı
		{"down", "down", "", []signalFamily{familyRuntime, familyLogs}},
		{"availability", "availability", "", []signalFamily{familyRuntime, familyLogs}},
		{"kural adı DOWN der", "custom", "Service DOWN", []signalFamily{familyRuntime, familyLogs}},

		// Doygunluk kuralları
		{"heap", "jvm_heap_pct", "", []signalFamily{familySaturation, familyRuntime}},
		{"gc", "gc_pause_ms", "", []signalFamily{familySaturation, familyRuntime}},
		{"restart", "pod_restarts", "", []signalFamily{familySaturation, familyRuntime}},

		// Derin soruşturma EKLEMEYENLER — hacim düşüşü genelde yukarı
		// akıştan gelir; bugünkü komşuluk paketi zaten doğru araç ve
		// gereksiz okuma maliyettir.
		{"request_rate", "request_rate", "", nil},
		{"throughput", "throughput", "", nil},
		{"bilinmeyen metrik", "some_custom_gauge", "", nil},
		{"boş", "", "", nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := investigationPlan(chstore.Problem{Metric: c.metric, RuleName: c.rule})
			if len(got) != len(c.want) {
				t.Fatalf("plan(%q,%q) = %v, beklenen %v", c.metric, c.rule, got, c.want)
			}
			for i := range got {
				if got[i] != c.want[i] {
					t.Errorf("plan(%q) [%d] = %q, beklenen %q — SIRA da sözleşmenin parçası",
						c.metric, i, got[i], c.want[i])
				}
			}
		})
	}
}

// Metrik adı büyük/küçük harf ya da boşlukla gelirse de doğru dala
// gitmeli — kural adları operatör-şekilli serbest metin.
func TestInvestigationPlanNormalizes(t *testing.T) {
	for _, m := range []string{"ERROR_RATE", "  error_rate  ", "Error_Rate"} {
		got := investigationPlan(chstore.Problem{Metric: m})
		if len(got) != 2 || got[0] != familyExceptions {
			t.Errorf("plan(%q) = %v — normalize edilmeli", m, got)
		}
	}
}

// Denetim izi DÜRÜST olmalı: okuma patlarsa "bakılamadı", kayıt yoksa
// "bakıldı, kayıt yok". Sessizce atlamak izi yalancı yapar ve izin tek
// işi modelin anlatımını denetlenebilir kılmak.
func TestDeepEvidenceNoteHonesty(t *testing.T) {
	var d DeepEvidence

	d.note(familyExceptions, errFake{}, 0, func() string { return "olmamalı" })
	if len(d.Checked) != 1 || d.Checked[0].Found {
		t.Fatal("hata durumu Found=false olmalı")
	}
	if !strings.Contains(d.Checked[0].Detail, "okunamadı") {
		t.Errorf("hata detayı okunamadığını söylemeli, got %q", d.Checked[0].Detail)
	}

	d.note(familyLogs, nil, 0, func() string { return "olmamalı" })
	if d.Checked[1].Found || !strings.Contains(d.Checked[1].Detail, "kayıt yok") {
		t.Errorf("boş sonuç 'bakıldı, kayıt yok' demeli, got %+v", d.Checked[1])
	}

	d.note(familyRuntime, nil, 3, func() string { return "üç kayıt" })
	if !d.Checked[2].Found || d.Checked[2].Records != 3 {
		t.Errorf("dolu sonuç Found=true + Records taşımalı, got %+v", d.Checked[2])
	}
}

// renderDeepEvidence boş izde HİÇBİR ŞEY basmamalı — derin soruşturma
// koşmamış bir problemin kanıt bloğuna boş "SORUŞTURMA" başlığı düşmesi
// modele "bakıldı ama bulunamadı" izlenimi verir. Yanlış izlenim.
func TestRenderDeepEvidenceEmpty(t *testing.T) {
	var sb strings.Builder
	renderDeepEvidence(&sb, DeepEvidence{})
	if sb.Len() != 0 {
		t.Errorf("boş kanıtta çıktı olmamalı, got %q", sb.String())
	}
}

func TestRenderDeepEvidenceListsWhatWasChecked(t *testing.T) {
	var sb strings.Builder
	renderDeepEvidence(&sb, DeepEvidence{
		Checked: []CheckedSignal{
			{Family: familySaturation, Found: true, Detail: "2 pod heap örneği", Records: 2},
			{Family: familyOperations, Found: false, Detail: "bakıldı, kayıt yok"},
		},
	})
	out := sb.String()
	for _, want := range []string{"SORUŞTURMA", "saturation", "VAR", "operations", "yok"} {
		if !strings.Contains(out, want) {
			t.Errorf("çıktıda %q yok:\n%s", want, out)
		}
	}
}

type errFake struct{}

func (errFake) Error() string { return "ch down" }
