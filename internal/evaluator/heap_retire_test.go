// v0.9.551 + v0.9.1075 regresyon testi — emekliye ayrılan JVM
// runtime kuralları.
//
// v0.9.551 operatörü: "JVM heap ile ilgili alertleri kaldırabiliriz,
// daha sonra spesifik kurallarla geliriz, çok fazla false pozitif
// üretiyor."
// v0.9.1075 operatörü (2026-08-16): "JVM GC alertlerini şimdilik
// kaldıralım; daha sonra VictoriaMetrics geçme planım var, o zaman
// düşünürüz." — v0.9.551'de bilinçli YERİNDE bırakılan GC çifti de
// bu kararla emekli oldu; buradaki eski "GC'ye ASLA dokunma" pinleri
// o kararla birlikte tersine çevrildi.
//
// Bu dosyanın koruduğu şey kaldırmanın kendisi DEĞİL, kaldırmanın
// güvenli hâli: bir kuralın değerlendirmesini silmek o kuralın
// KAPATICI kolunu da siler, dolayısıyla açık satırlar Problems'ta
// sonsuza dek kalırdı. False pozitiflerden kurtulmak için onları
// kalıcılaştırmak, şikâyetin daha kötü hâli olurdu — tahliye bu
// yüzden var ve bu yüzden test ediliyor.
package evaluator

import (
	"os"
	"strings"
	"testing"

	"github.com/cilcenk/coremetry/internal/chstore"
)

func TestShouldDrainRetiredRuntimeProblem(t *testing.T) {
	cases := []struct {
		name string
		p    *chstore.Problem
		want bool
	}{
		{"nil satır", nil, false},
		{
			"açık heap problemi → tahliye",
			&chstore.Problem{RuleID: "runtime:jvm-heap", Status: "open"}, true,
		},
		{
			// Zaten kapalı satırı yeniden kapatmak her tikte
			// ResolvedAt'i ileri iter ve problemin gerçek
			// süresini bozar.
			"zaten kapalı heap problemi → dokunma",
			&chstore.Problem{RuleID: "runtime:jvm-heap", Status: "resolved"}, false,
		},
		{
			// v0.9.1075 — operatör kararıyla GC çifti de emekli;
			// açık kalan satırları artık tahliye KAPATIR.
			"açık GC problemi → tahliye (v0.9.1075)",
			&chstore.Problem{RuleID: "runtime:jvm-gc", Status: "open"}, true,
		},
		{
			"açık GC-share problemi → tahliye (v0.9.1075)",
			&chstore.Problem{RuleID: "runtime:jvm-gc-share", Status: "open"}, true,
		},
		{
			"zaten kapalı GC problemi → dokunma",
			&chstore.Problem{RuleID: "runtime:jvm-gc", Status: "resolved"}, false,
		},
		{
			// EN ÖNEMLİ VAKA: tahliye yalnız EMEKLİ seti temizler.
			// Buranın gevşemesi (örn. "runtime:" öneki yeter demek)
			// gelecekte eklenecek runtime kurallarını da sessizce
			// kapatırdı.
			"ilgisiz kural → dokunma",
			&chstore.Problem{RuleID: "builtin-error-rate", Status: "open"}, false,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := shouldDrainRetiredRuntimeProblem(c.p); got != c.want {
				t.Errorf("shouldDrainRetiredRuntimeProblem = %v, beklenen %v", got, c.want)
			}
		})
	}
}

// TestRuntimeEvaluationRemoved — kaldırmanın kendisini sabitler.
//
// Kaynak denetimi, snapshot_batch_test.go'daki emsalin aynısı:
// evaluator'ın store alanı somut *chstore.Store olduğu için tik
// uçtan uca sahte store ile koşturulamıyor. Birinin heap ya da GC
// değerlendirmesini "geçici olarak" geri açması hâlinde bu test
// düşer ve operatörün kararı sessizce geri alınmaz.
func TestRuntimeEvaluationRemoved(t *testing.T) {
	b, err := os.ReadFile("runtime_pods.go")
	if err != nil {
		t.Fatalf("kaynak okunamadı: %v", err)
	}
	src := string(b)

	for banned, why := range map[string]string{
		"e.reconcileRuntimeHeap(":    "operatör kararı v0.9.551: heap alarmı false pozitif oranı yüzünden kaldırıldı",
		"e.reconcileRuntimeGC(":      "operatör kararı v0.9.1075: JVM GC alarmları VictoriaMetrics geçişine dek askıda",
		"e.reconcileRuntimeGCShare(": "operatör kararı v0.9.1075: JVM GC alarmları VictoriaMetrics geçişine dek askıda",
	} {
		if strings.Contains(src, banned) {
			t.Errorf("%s geri gelmiş — %s", banned, why)
		}
	}
	if !strings.Contains(src, "e.drainRetiredRuntimeProblems(ctx, snap)") {
		t.Error("tahliye çağrısı kaybolmuş — açık emekli-runtime problemleri " +
			"Problems'ta sonsuza dek kalır, kapatacak başka kod yok")
	}
}
