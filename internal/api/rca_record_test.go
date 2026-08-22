// v0.9.591 — verdict kaydının regresyon testi.
//
// Kayıt LEARN'in ön koşulu: hangi verdict'e 👎 verildiğini bilmeden
// neyi öğreteceğimizi de bilemeyiz (docs/cosre-verdict-design.md §11).
// Ama kaydın kendisi sessizce yanlış olabilir ve o hâlde ölçüm
// yanıltıcı olur — yanlış ölçüm, ölçüm YOKLUĞUNDAN kötüdür, çünkü
// kararı ona dayandırırız.
package api

import (
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/cilcenk/coremetry/internal/chstore"
)

func sampleVerdict() *RCAVerdict {
	return &RCAVerdict{
		Verdict:              "probable_cause",
		Title:                "Oracle havuzu",
		Confidence:           0.55,
		ModelConfidence:      0.9,
		HypothesisConfidence: 0.45,
		Shields: rcaShieldReport{
			Parsed:           true,
			Repaired:         true,
			ConfidenceCapped: true,
			Notes:            []string{"güven tavanlandı"},
		},
	}
}

// TestRecordCarriesTheSHIELDEDVerdict — kaydedilen, operatöre
// GÖSTERİLEN karardır; modelin ham çıktısı değil.
//
// Ham çıktı zaten ai_calls.response_sample'da. Aradaki farkı kalkanlar
// üretiyor ve tam da o fark ölçülmek isteniyor: model 0.9 dedi, biz
// 0.55 gösterdik. İkisini birden saklamazsak tavanın ne kadar
// indirdiği görünmez.
func TestRecordCarriesTheShieldedVerdict(t *testing.T) {
	h := &chstore.RootCauseHypothesis{Version: 42, Service: "checkout-service"}
	rec, ok := rcaVerdictRecordOf("ex-1", "problem", "p-9",
		chstore.RCAVerdictSourceOperator, h, sampleVerdict(), nil)
	if !ok {
		t.Fatal("kayıt üretilmedi")
	}
	if rec.Confidence != 0.55 || rec.ModelConf != 0.9 || rec.HypoConf != 0.45 {
		t.Errorf("üç güven de ayrı saklanmalı: nihai=%v model=%v hipotez=%v — "+
			"tavanın ne kadar indirdiği ancak üçü birlikte görünür",
			rec.Confidence, rec.ModelConf, rec.HypoConf)
	}
	if rec.HypoVersion != 42 {
		t.Errorf("hipotez sürümü taşınmadı (%d) — hangi girdiye bakıldığını "+
			"bilmeden 'bu verdict yanlıştı' geri bildirimi yorumlanamaz", rec.HypoVersion)
	}
	if rec.Service != "checkout-service" {
		t.Errorf("servis taşınmadı: %q", rec.Service)
	}
	if !rec.Parsed || !rec.Repaired {
		t.Errorf("kalkan bayrakları taşınmadı: parsed=%v repaired=%v", rec.Parsed, rec.Repaired)
	}
	if len(rec.ShieldNotes) != 1 {
		t.Errorf("kalkan notları taşınmadı: %v — kalkanların NE YAPTIĞI "+
			"başka hiçbir yerde kalmıyor", rec.ShieldNotes)
	}
	if rec.ExchangeID != "ex-1" || rec.AnchorKind != "problem" || rec.AnchorID != "p-9" {
		t.Errorf("çapa alanları bozuk: %+v", rec)
	}
}

// TestRecordSkippedWithoutIdentity — kimliksiz kayıt YAZILMAZ.
//
// exchange_id ai_feedback'e join anahtarı; onsuz satır ne ölçüme ne
// geri bildirime yarar. Yazmak, sonradan hiçbir soruya cevap vermeyen
// satır biriktirmek olurdu — üstelik toplamları şişirerek.
func TestRecordSkippedWithoutIdentity(t *testing.T) {
	if _, ok := rcaVerdictRecordOf("", "problem", "p-1",
		chstore.RCAVerdictSourceOperator, nil, sampleVerdict(), nil); ok {
		t.Error("kimliksiz verdict kaydedilecekti — ai_feedback'e join olamaz, " +
			"yalnız toplamları şişirir")
	}
	if _, ok := rcaVerdictRecordOf("ex-1", "problem", "p-1",
		chstore.RCAVerdictSourceOperator, nil, nil, nil); ok {
		t.Error("nil verdict kaydedilecekti — boş satır ölçümde 'karar verildi' " +
			"diye sayılır ama hiçbir şey söylemez")
	}
}

// TestRecordSurvivesNilHypothesis — savunmacı yol çökmemeli.
func TestRecordSurvivesNilHypothesis(t *testing.T) {
	rec, ok := rcaVerdictRecordOf("ex-2", "anomaly", "a-1",
		chstore.RCAVerdictSourceOperator, nil, sampleVerdict(), nil)
	if !ok {
		t.Fatal("hipotez nil diye kayıt tamamen düştü — verdict yine de verildi")
	}
	if rec.HypoVersion != 0 || rec.Service != "" {
		t.Errorf("hipotez yokken sürüm/servis uydurulmuş: %+v", rec)
	}
}

// TestExplainResponseCarriesTheExchangeID — KONUM sözleşmesi.
//
// Kimlik önbelleğin İÇİNDE basılmalı: serveCached gövdeyi olduğu gibi
// saklar, yani kimlik gövdeyle birlikte yaşamalı. Dışarıda basılsaydı
// her istek yeni bir kimlik üretir ve önbellekten sunulan AYNI cevap
// her seferinde farklı kimlikle giderdi — oylar dağılır, hiçbiri
// ai_calls satırıyla eşleşmezdi.
func TestExchangeIDIsMintedInsideTheCachedProducer(t *testing.T) {
	src := readAPISourceNoComments(t, "rootcause.go")
	i := strings.Index(src, "func (s *Server) rootCauseExplainProse(")
	if i < 0 {
		t.Fatal("rootCauseExplainProse bulunamadı — test bayatladı")
	}
	body := src[i:]
	if j := strings.Index(body[1:], "\nfunc "); j >= 0 {
		body = body[:j+1]
	}
	mint := strings.Index(body, "newRandID(")
	cache := strings.Index(body, "s.serveCached(")
	if mint < 0 {
		t.Fatal("exchangeID hiç basılmıyor — 👍/👎 bağlanacağı kimlik yok")
	}
	if cache < 0 {
		t.Fatal("serveCached bulunamadı — test bayatladı")
	}
	if mint < cache {
		t.Error("exchangeID önbellek üreticisinin DIŞINDA basılıyor. serveCached " +
			"gövdeyi olduğu gibi saklar; dışarıda basılan kimlik her istekte " +
			"değişir ve önbellekten sunulan AYNI cevap her seferinde farklı " +
			"kimlikle gider — oylar dağılır ve hiçbiri ai_calls satırıyla eşleşmez.")
	}
	if !strings.Contains(body, "ExchangeID: exchangeID") {
		t.Error("exchangeID CallMeta'ya geçmiyor — ai_calls satırına düşmez, " +
			"POST /api/ai/feedback surface'i çözemez")
	}
}

var apiLineComment = regexp.MustCompile(`(?m)^\s*//.*$`)

// readAPISourceNoComments — kaynağı YORUMSUZ okur.
//
// Bu oturumda İKİ KEZ, kaynak-tarama testi kendi düzeltmesinin
// açıklayıcı yorumunda alıntılanan ESKİ kodla eşleşip yanlış yeşil
// verdi. Yukarıdaki testin aradığı dizgeler ("newRandID(",
// "ExchangeID: exchangeID") bu dosyanın da yorumlarında geçiyor —
// yani tuzak burada gerçek, teorik değil.
func readAPISourceNoComments(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(name)
	if err != nil {
		t.Fatalf("%s okunamadı: %v", name, err)
	}
	return apiLineComment.ReplaceAllString(string(b), "")
}
