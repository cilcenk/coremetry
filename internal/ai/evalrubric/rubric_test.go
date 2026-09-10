package evalrubric

// rubric_test.go — v0.10.666 Faz A. SÖZLEŞME: deterministik rubrik; n/a
// boyutlar ağırlıktan düşer; hata → uygulanabilir boyutlar 0; Türkçe
// sezgisi tablo-testli. Mutasyon (ölçüldü): finalize'da n/a düşürmeyi
// kaldırmak (Max<=0 kontrolü) 'JSON yüzeyi dil cezası almaz' vakasını,
// mustNotContain ihlalinde 0 yerine kapsama oranını döndürmek 'yasak
// ifade' vakasını düşürür.

import (
	"errors"
	"math"
	"testing"
)

func dim(r Result, name string) Dim {
	for _, d := range r.Dims {
		if d.Name == name {
			return d
		}
	}
	return Dim{}
}

func TestScoreFullMarks(t *testing.T) {
	r := Score(Input{Answer: "checkout servisinde hata oranı arttı çünkü bağımlılık zaman aşımı veriyor.",
		MustContain: []string{"checkout", "hata"}, ExpectTurkish: true, MaxChars: 500})
	if r.Total != 1 || r.Applicable != 4 {
		t.Fatalf("tam puan beklenir: total=%v applicable=%d dims=%+v", r.Total, r.Applicable, r.Dims)
	}
}

func TestScoreGroundedCap(t *testing.T) {
	ok := Score(Input{Answer: "servis için bir cevap", UnknownCount: 1, MaxUnknown: 1, ExpectTurkish: true})
	bad := Score(Input{Answer: "servis için bir cevap", UnknownCount: 2, MaxUnknown: 1, ExpectTurkish: true})
	if dim(ok, "grounded").Score != 1 || dim(bad, "grounded").Score != 0 {
		t.Fatalf("grounded tavanı: ok=%+v bad=%+v", dim(ok, "grounded"), dim(bad, "grounded"))
	}
	if !(bad.Total < ok.Total) {
		t.Fatalf("kanıtsız cevap daha düşük puan almalı: %v vs %v", bad.Total, ok.Total)
	}
}

func TestScoreAnswersQuestion(t *testing.T) {
	half := Score(Input{Answer: "checkout yavaş", MustContain: []string{"checkout", "payment"}})
	if d := dim(half, "answers_question"); d.Score != 1 || d.Max != 2 {
		t.Fatalf("2 beklentiden 1'i → 1/2: %+v", d)
	}
	forbidden := Score(Input{Answer: "grafana panosuna bak", MustContain: []string{"grafana"}, MustNotContain: []string{"grafana"}})
	if d := dim(forbidden, "answers_question"); d.Score != 0 {
		t.Fatalf("yasak ifade → 0: %+v", d)
	}
	none := Score(Input{Answer: "bir cevap"})
	if d := dim(none, "answers_question"); d.Score != 2 {
		t.Fatalf("beklenti yoksa tam puan: %+v", d)
	}
}

func TestScoreNAWeightsRenormalize(t *testing.T) {
	// JSON yüzeyi: dil, uzunluk, tool n/a → yalnız grounded + answers; ikisi tam → 1.0
	r := Score(Input{Answer: `{"intent":"service_health"}`, MustContain: []string{`"intent"`}})
	if r.Applicable != 2 || r.Total != 1 {
		t.Fatalf("JSON yüzeyi dil cezası almamalı: applicable=%d total=%v", r.Applicable, r.Total)
	}
	if dim(r, "language_tr").Max != 0 {
		t.Fatal("ExpectTurkish=false iken dil boyutu n/a olmalı")
	}
}

func TestScoreErrorZeroes(t *testing.T) {
	r := Score(Input{Err: errors.New("timeout"), MustContain: []string{"x"}, ExpectTurkish: true, MaxChars: 10})
	if r.Total != 0 || r.Applicable != 4 {
		t.Fatalf("hata: uygulanabilir boyutlar 0, liste durur: %+v", r)
	}
}

func TestScoreLengthAndTools(t *testing.T) {
	long := Score(Input{Answer: "abcdefghijk", MaxChars: 5})
	if dim(long, "length_ok").Score != 0 {
		t.Fatal("uzun cevap length_ok=0")
	}
	yes, no := true, false
	if dim(Score(Input{Answer: "x", ToolCallsValid: &yes}), "tool_calls_valid").Score != 1 ||
		dim(Score(Input{Answer: "x", ToolCallsValid: &no}), "tool_calls_valid").Score != 0 {
		t.Fatal("tool_calls_valid 1/0")
	}
	if dim(Score(Input{Answer: "x"}), "tool_calls_valid").Max != 0 {
		t.Fatal("nil → n/a")
	}
}

func TestIsTurkish(t *testing.T) {
	cases := []struct {
		text string
		want bool
	}{
		{"checkout servisinde hata oranı yükseldi ve p95 arttı", true},
		{"Bağlantı zaman aşımı nedeniyle istekler düşüyor.", true},
		{"The checkout service error rate increased because the dependency is timing out.", false},
		{"", false},
		{"ok", false},
		{`{"intent":"x"}`, false},
	}
	for _, c := range cases {
		if got := IsTurkish(c.text); got != c.want {
			t.Errorf("IsTurkish(%q) = %v, istenen %v", c.text, got, c.want)
		}
	}
}

func TestWeightsSumToOne(t *testing.T) {
	if math.Abs(wGrounded+wAnswers+wLanguage+wLength+wTools-1) > 1e-9 {
		t.Fatal("ağırlıklar 1,0 toplamalı")
	}
}
