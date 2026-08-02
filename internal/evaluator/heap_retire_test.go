// v0.9.551 regresyon testi — emekliye ayrılan JVM heap kuralı.
//
// Operatör: "JVM heap ile ilgili alertleri kaldırabiliriz, daha sonra
// spesifik kurallarla geliriz, çok fazla false pozitif üretiyor."
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

func TestShouldDrainHeapProblem(t *testing.T) {
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
			// EN ÖNEMLİ VAKA: tahliye yalnız KENDİ kuralını
			// temizler. Buranın gevşemesi (örn. "runtime:" öneki
			// yeter demek) GC alarmlarını da sessizce kapatırdı —
			// operatörün açıkça YERİNDE KALMASINI istediği kurallar.
			"açık GC problemi → ASLA dokunma",
			&chstore.Problem{RuleID: "runtime:jvm-gc", Status: "open"}, false,
		},
		{
			"açık GC-share problemi → ASLA dokunma",
			&chstore.Problem{RuleID: "runtime:jvm-gc-share", Status: "open"}, false,
		},
		{
			"ilgisiz kural → dokunma",
			&chstore.Problem{RuleID: "builtin-error-rate", Status: "open"}, false,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := shouldDrainHeapProblem(c.p); got != c.want {
				t.Errorf("shouldDrainHeapProblem = %v, beklenen %v", got, c.want)
			}
		})
	}
}

// TestHeapEvaluationRemoved — kaldırmanın kendisini sabitler.
//
// Kaynak denetimi, snapshot_batch_test.go'daki emsalin aynısı:
// evaluator'ın store alanı somut *chstore.Store olduğu için tik
// uçtan uca sahte store ile koşturulamıyor. Birinin heap
// değerlendirmesini "geçici olarak" geri açması hâlinde bu test
// düşer ve operatörün kararı sessizce geri alınmaz.
func TestHeapEvaluationRemoved(t *testing.T) {
	b, err := os.ReadFile("runtime_pods.go")
	if err != nil {
		t.Fatalf("kaynak okunamadı: %v", err)
	}
	src := string(b)

	if strings.Contains(src, "e.reconcileRuntimeHeap(") {
		t.Error("heap değerlendirmesi geri gelmiş — operatör kararı v0.9.551: " +
			"heap alarmı false pozitif oranı yüzünden kaldırıldı")
	}
	if !strings.Contains(src, "e.drainRetiredHeapProblems(ctx, snap)") {
		t.Error("tahliye çağrısı kaybolmuş — açık heap problemleri Problems'ta " +
			"sonsuza dek kalır, kapatacak başka kod yok")
	}
	// GC kuralları operatörün açıkça yerinde bıraktığı sinyaller.
	for _, must := range []string{"e.reconcileRuntimeGC(", "e.reconcileRuntimeGCShare("} {
		if !strings.Contains(src, must) {
			t.Errorf("%s kaybolmuş — heap kaldırıldı ama GC BASKISI kuralları "+
				"kalmalıydı (gerçek sinyal onlar)", must)
		}
	}
}
