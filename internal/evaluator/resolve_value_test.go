package evaluator

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// v0.9.977 — KAYNAK PİNİ: problem kapatan hiçbir yol Value'ya dokunmaz.
//
// Saf hesap (chstore.MarkResolved) testli, ama asıl hata ÇAĞRI
// yerlerindeydi: altı ayrı dosya kapanış bloğunda `X.Value = <toparlanmış
// değer>` yazıyordu. Aynı satır yarın yedinci bir dedektörde yeniden
// yazılabilir; bu tarama tam olarak onu yasaklıyor.
//
// İki şey aranıyor:
//  1. `Status = "resolved"` elle atanmıyor — kapanış tek kapıdan
//     (chstore.MarkResolved) geçmeli. İstisna: Incident kapanışı
//     (inc.Status) — problems tablosuna değil incidents'a yazıyor.
//  2. MarkResolved çağrısını izleyen üç satırda `.Value =` yok.
func TestNoResolvePathOverwritesBreachValue(t *testing.T) {
	files := []string{
		"evaluator.go",
		"watcher_eval.go",
		"db_capacity.go",
		"runtime_pods.go",
		"slo_burn.go",
		"fatal_exception.go",
		"shared_exception.go",
		"../anomaly/anomaly.go",
		"../monitor/runner.go",
		"../api/api_monitors.go",
	}

	markResolved := regexp.MustCompile(`chstore\.MarkResolved\(`)
	handStatus := regexp.MustCompile(`^\s*\w+(\.\w+)*\.Status\s*=\s*"resolved"`)
	valueAssign := regexp.MustCompile(`^\s*\w+(\.\w+)*\.Value\s*=`)

	seenMark := 0
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatalf("%s: %v", f, err)
		}
		lines := strings.Split(string(b), "\n")
		for i, l := range lines {
			if handStatus.MatchString(l) && !strings.Contains(l, "inc.Status") {
				t.Errorf("%s:%d elle kapanış yazıyor (%s) — chstore.MarkResolved "+
					"kullanılmalı; tek kapı olmadan Value ezme sınıfı geri döner",
					f, i+1, strings.TrimSpace(l))
			}
			if !markResolved.MatchString(l) {
				continue
			}
			seenMark++
			for j := i + 1; j < len(lines) && j <= i+3; j++ {
				if valueAssign.MatchString(lines[j]) {
					t.Errorf("%s:%d kapanıştan hemen sonra Value eziyor (%s) — "+
						"ihlal değeri KORUNMALI (v0.9.977)",
						f, j+1, strings.TrimSpace(lines[j]))
				}
			}
		}
	}
	if seenMark < 10 {
		t.Errorf("MarkResolved yalnız %d yerde bulundu — kapanış yollarından "+
			"biri taramanın dışına çıkmış olabilir", seenMark)
	}
}
