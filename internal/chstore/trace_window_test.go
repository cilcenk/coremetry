// v0.9.578 regresyon testi — trace pencere araması sınırlı kalmalı.
//
// Operator-reported (prod log'u):
//
//	[trace] <id> window lookup failed (code: 159, Timeout exceeded:
//	elapsed 3019ms, maximum: 3000ms) — unbounded spans scan
//
// Her trace açılışında tekrarlıyordu. İki kat kötüydü:
//
//	(a) Ön-sorgu `WHERE trace_id = ?` idi, zaman sınırı YOK.
//	    trace_summary_5m sıralaması (time_bucket, trace_id) ve
//	    partisyonu toDate(time_bucket) — trace_id ÖNCÜ kolon değil,
//	    yani 90 günlük TÜM partisyonlar taranıyordu. Her açılışta 3
//	    saniye boşa yanıyordu.
//	(b) Ön-sorgu başarısız olunca spans taraması ZAMAN SINIRSIZ
//	    kalıyordu — CLAUDE.md'nin sert kısıtının ihlali ve zaten bu
//	    optimizasyonun ÖNLEMEK için var olduğu şey.
package chstore

import (
	"os"
	"strings"
	"testing"
	"time"
)

func TestTraceWindowStepsStartNarrow(t *testing.T) {
	if len(traceWindowSteps) < 2 {
		t.Fatal("kademeli arama yok — tek geniş pencere partisyon budayamaz")
	}
	// Kademeler ARTAN olmalı: dar başlayıp genişlemek partisyon budar.
	for i := 1; i < len(traceWindowSteps); i++ {
		if traceWindowSteps[i] <= traceWindowSteps[i-1] {
			t.Errorf("kademe %d (%s) öncekinden (%s) geniş değil — dar başlama "+
				"avantajı kaybolur", i, traceWindowSteps[i], traceWindowSteps[i-1])
		}
	}
	// İlk kademe TAZE olmalı; operatörlerin açtığı trace'lerin ezici
	// çoğunluğu son gün içinde.
	if traceWindowSteps[0] > 48*time.Hour {
		t.Errorf("ilk kademe %s — çok geniş, partisyon budama kazancı kaybolur",
			traceWindowSteps[0])
	}
	// Son kademe MV'nin TTL'ini aşmamalı: aşarsa boş partisyon taranır.
	if traceWindowSteps[len(traceWindowSteps)-1] > 90*24*time.Hour {
		t.Errorf("son kademe %s — trace_summary_5m TTL'i (90 gün) aşıyor",
			traceWindowSteps[len(traceWindowSteps)-1])
	}
}

// ASIL REGRESYON: pencere çözülemese bile spans taraması SINIRLI kalmalı.
func TestGetTraceAlwaysBoundsTheSpansScan(t *testing.T) {
	b, err := os.ReadFile("repo.go")
	if err != nil {
		t.Fatalf("repo.go okunamadı: %v", err)
	}
	src := stripGoLineComments(string(b))

	i := strings.Index(src, "func (s *Store) GetTrace(")
	if i < 0 {
		t.Fatal("GetTrace bulunamadı")
	}
	body := src[i:]
	if end := strings.Index(body, "\nfunc "); end > 0 {
		body = body[:end]
	}

	// Ön-sorgu zaman sınırlı olmalı.
	if !strings.Contains(body, "time_bucket >= ?") {
		t.Error("pencere ön-sorgusu zaman sınırsız — trace_id ÖNCÜ kolon değil, " +
			"90 günlük tüm partisyonlar taranır ve sorgu zaman aşımına uğrar")
	}
	// Pencere çözülemediğinde SON ÇARE sınırı olmalı.
	if !strings.Contains(body, "traceScanFloor") {
		t.Error("pencere çözülemediğinde spans taraması SINIRSIZ kalıyor — " +
			"CLAUDE.md sert kısıtının ihlali")
	}
	// Eski "sınırsız tarama" kabullenişi geri gelmemeli.
	if strings.Contains(body, "unbounded spans scan") {
		t.Error("sınırsız tarama yolu geri gelmiş")
	}
}

// spans taraması tavanı, spans TTL'inden kısa OLMAMALI — kısa olsaydı
// retention içindeki geçerli bir trace sessizce bulunamaz hale gelirdi.
func TestTraceScanFloorCoversRetention(t *testing.T) {
	const spansTTL = 30 * 24 * time.Hour
	if traceScanFloor < spansTTL {
		t.Errorf("traceScanFloor %s < spans TTL %s — retention içindeki bir trace "+
			"sessizce bulunamaz olur", traceScanFloor, spansTTL)
	}
}
