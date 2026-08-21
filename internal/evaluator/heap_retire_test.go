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
		name     string
		p        *chstore.Problem
		vmActive bool
		want     bool
	}{
		{"nil satır", nil, false, false},
		{
			"açık heap problemi → tahliye",
			&chstore.Problem{RuleID: "runtime:jvm-heap", Status: "open"}, false, true,
		},
		{
			// v0.9.1213 — heap VM açıkken de emekli: v0.9.551 nedeni
			// sinyal hatasıydı (kaynakla ilgisi yok), VM dönüşü onu
			// diriltmez.
			"açık heap problemi, VM açık → yine tahliye",
			&chstore.Problem{RuleID: "runtime:jvm-heap", Status: "open"}, true, true,
		},
		{
			// Zaten kapalı satırı yeniden kapatmak her tikte
			// ResolvedAt'i ileri iter ve problemin gerçek
			// süresini bozar.
			"zaten kapalı heap problemi → dokunma",
			&chstore.Problem{RuleID: "runtime:jvm-heap", Status: "resolved"}, false, false,
		},
		{
			// v0.9.1075 — VM yokken GC çifti emekli; açık satırları
			// tahliye kapatır.
			"açık GC problemi, VM kapalı → tahliye",
			&chstore.Problem{RuleID: "runtime:jvm-gc", Status: "open"}, false, true,
		},
		{
			"açık GC-share problemi, VM kapalı → tahliye",
			&chstore.Problem{RuleID: "runtime:jvm-gc-share", Status: "open"}, false, true,
		},
		{
			// v0.9.1213 — VM açıkken GC çifti CANLI: tahliye dokunamaz,
			// yoksa her tik gerçek bir ihlali "kural kaldırıldı" notuyla
			// kapatırdı.
			"açık GC problemi, VM açık → CANLI, dokunma",
			&chstore.Problem{RuleID: "runtime:jvm-gc", Status: "open"}, true, false,
		},
		{
			"açık GC-share problemi, VM açık → CANLI, dokunma",
			&chstore.Problem{RuleID: "runtime:jvm-gc-share", Status: "open"}, true, false,
		},
		{
			"zaten kapalı GC problemi → dokunma",
			&chstore.Problem{RuleID: "runtime:jvm-gc", Status: "resolved"}, false, false,
		},
		{
			// EN ÖNEMLİ VAKA: tahliye yalnız EMEKLİ seti temizler.
			"ilgisiz kural → dokunma",
			&chstore.Problem{RuleID: "builtin-error-rate", Status: "open"}, false, false,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := shouldDrainRetiredRuntimeProblem(c.p, c.vmActive); got != c.want {
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
// v0.9.1213 — testin ESKİ hâli GC değerlendirmesinin geri gelmemesini
// pinliyordu; VM dönüşüyle sözleşme DEĞİŞTİ. Yeni pinler:
//
//	(a) heap değerlendirmesi geri GELMEZ (v0.9.551 sinyal-hatası —
//	    VM'le ilgisi yok),
//	(b) GC değerlendirmesi CH okuyucularına ASLA dönmez (kaynak yalnız
//	    VM — JVMGCPodPause/JVMGCActivity evaluator'da yasak; sessiz CH
//	    dönüşü v0.9.1075 operatör kararını arkadan dolanmak olurdu),
//	(c) vmActive kapısı ve tahliye çağrısı yerinde.
func TestRuntimeGCReturnsOnlyViaVM(t *testing.T) {
	read := func(name string) string {
		b, err := os.ReadFile(name)
		if err != nil {
			t.Fatalf("kaynak okunamadı: %v", err)
		}
		return string(b)
	}
	pods, vm := read("runtime_pods.go"), read("runtime_vm.go")

	if strings.Contains(pods+vm, "e.reconcileRuntimeHeap(") {
		t.Error("heap değerlendirmesi geri gelmiş — v0.9.551 kararı sinyal hatasıydı, VM dönüşü onu diriltmez")
	}
	for _, banned := range []string{"JVMGCPodPause", "JVMGCActivity"} {
		if strings.Contains(pods, banned) || strings.Contains(vm, banned) {
			t.Errorf("%s evaluator'da — GC alarmlarının kaynağı YALNIZ VM (v0.9.1213); CH okuyucusuna sessiz dönüş yasak", banned)
		}
	}
	if !strings.Contains(pods, "e.vmetrics != nil && e.vmetrics.Configured()") {
		t.Error("vmActive kapısı kaybolmuş — GC değerlendirmesi VM'siz koşamaz")
	}
	if !strings.Contains(pods, "e.drainRetiredRuntimeProblems(ctx, snap, vmActive)") {
		t.Error("tahliye çağrısı kaybolmuş — açık emekli-runtime problemleri " +
			"Problems'ta sonsuza dek kalır, kapatacak başka kod yok")
	}
}
