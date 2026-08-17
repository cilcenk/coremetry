package anomaly

import (
	"os"
	"strings"
	"testing"
)

// v0.9.1138 kaynak-pini — iki arka plan açıklayıcı da vidaya
// danışmak ZORUNDA; yeni bir üçüncüsü eklenirse bu test ona da
// genişletilmeli (dosya adı desenine bilinçli bağlı değil: liste
// açık, unutulan dosya kırmızı yakmaz ama listedekiler gevşeyemez).
func TestExplainersConsultAutoExplainGate(t *testing.T) {
	for _, f := range []string{"problem_explainer.go", "exception_explainer.go"} {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatalf("%s okunamadı: %v", f, err)
		}
		if !strings.Contains(string(b), "AutoExplainEnabled()") {
			t.Errorf("%s otomatik-açıklama vidasına danışmıyor (Settings→AI kapatması işlemez)", f)
		}
	}
}
