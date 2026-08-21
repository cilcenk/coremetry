package chstore

// v0.9.1195 — `FROM t FINAL AS f` SÖZDİZİMSEL OLARAK GEÇERSİZ (Code 62);
// doğru sıra alias ÖNCE: `FROM t AS f FINAL`. ListNegativeFeedbackCalls
// v0.9.423'ten beri bu ters sırayla yazılmıştı ve her çağrısı sözdizimi
// hatası yiyordu — /ai negatif paneli hiç veri gösteremedi, hata cache
// katmanında yutulup boş panel gibi göründüğü için kimse fark etmedi.
// Faz 5.2 (KB terfi kuyruğu) geliştirmesinin docker runtime doğrulaması
// yakaladı. Dosya-geneli sıfır sayımı üçüncü bir kopyanın doğmamasını
// garanti eder.

import (
	"os"
	"strings"
	"testing"
)

// TestNoFinalBeforeAlias — `FROM t FINAL AS f` SÖZDİZİMİ GEÇERSİZ ve bu
// pin, runtime doğrulamasının yakaladığı GERÇEK bir gemideki bug'ın izi:
// ListNegativeFeedbackCalls v0.9.423'ten v0.9.1195'e dek bu sırayla
// yazılmıştı ve HER çağrısı Code 62 (Syntax error) yiyordu — /ai negatif
// paneli hiç veri gösteremedi, kimse fark etmedi çünkü hata cache
// katmanında yutulup boş panel gibi görünüyordu. Doğru sıra alias ÖNCE:
// `FROM t AS f FINAL`. Dosya-geneli sıfır sayımı üçüncü bir kopyanın da
// doğamamasını garanti eder.
func TestNoFinalBeforeAlias(t *testing.T) {
	b, err := os.ReadFile("ai_feedback.go")
	if err != nil {
		t.Fatalf("ai_feedback.go okunamadı: %v", err)
	}
	if n := strings.Count(string(b), "FINAL AS "); n > 0 {
		t.Errorf("%d adet `FINAL AS` — alias FINAL'den ÖNCE gelmeli (Code 62, v0.9.1195'in bulduğu v0.9.423 bug'ı)", n)
	}
}
