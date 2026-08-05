package api

import "testing"

// v0.9.650 — operatör: "Takımıma ait servislerin hataları (Exceptions)
// neler?"
//
// Problem ve Exception AYRI yüzeyler: Problem bir alarm kuralının açtığı
// kayıt, Exception ise span'lerden gruplanan ham hata. "Takımımın
// problemleri" ikincisini KAPSAMIYORDU ve soru sessizce yanlış yüzeye
// düşüyordu.
//
// Ayrım kılpayı: hasErrorSignal zaten "exception" kelimesini kapsıyor,
// yani takım+exception cümlesi problems dalına gidiyordu. Bu testler
// ayrımın DURDUĞUNU çiviliyor.

func routeOf(t *testing.T, q string) guidedRoute {
	t.Helper()
	return routeGuidedIntent(q, nil, nil, "")
}

func TestTeamExceptionQuestionRoutesToExceptions(t *testing.T) {
	for _, q := range []string{
		"Takımıma ait servislerin hataları (Exceptions) neler?",
		"takımımın exception'ları neler",
		"benim takımın exceptionları",
		"takımımın servislerindeki istisnalar",
	} {
		if got := routeOf(t, q).Intent; got != guidedMyExceptions {
			t.Errorf("%q → %q, beklenen my_exceptions", q, got)
		}
	}
}

// EXPLICIT exception kelimesi YOKSA bugünkü davranış korunmalı:
// "takımımın hataları" açık PROBLEM'lere gitmeye devam etsin.
func TestTeamErrorQuestionStaysOnProblems(t *testing.T) {
	for _, q := range []string{
		"takımımın hataları neler",
		"takımımın açık problemleri",
		"benim takımda sorun var mı",
	} {
		if got := routeOf(t, q).Intent; got != guidedMyProblems {
			t.Errorf("%q → %q, beklenen my_problems (exception kelimesi yok)", q, got)
		}
	}
}

// Takım sinyali YOKSA exception kelimesi takım dalını AÇMAMALI —
// "exception'lar neler" filo geneli bir soru.
func TestExceptionWordAloneDoesNotClaimTeamScope(t *testing.T) {
	if got := routeOf(t, "exception'lar neler").Intent; got == guidedMyExceptions {
		t.Error("takım sinyali olmadan my_exceptions seçilmemeli")
	}
}

func TestHasExceptionWordIsNarrow(t *testing.T) {
	// hasErrorSignal geniş; hasExceptionWord DAR olmalı, yoksa ayrım yok.
	if hasExceptionWord([]string{"hata"}) {
		t.Error("'hata' exception kelimesi sayılmamalı — ayrımın tamamı bu")
	}
	if hasExceptionWord([]string{"error"}) {
		t.Error("'error' exception kelimesi sayılmamalı")
	}
	if !hasExceptionWord([]string{"exceptionlari"}) {
		t.Error("'exceptionlari' yakalanmalı (Türkçe ek)")
	}
}

// v0.9.650 — Türkçe iyelik SİMETRİSİ. Üstteki test bunu ORTAYA ÇIKARDI:
// hasTeamSelfSignal yalnız "takımım" ekini tanıyordu, "benim takımın" /
// "bizim takımda" gibi çok doğal kalıplar DÜŞÜYOR ve soru sessizce FİLO
// GENELİNE gidiyordu — operatör "takımımın" dediğini sanırken tüm
// kurumun problemlerini görüyordu.
func TestTurkishPossessiveTeamSignal(t *testing.T) {
	for _, q := range []string{
		"benim takımın problemleri",
		"bizim takımda sorun var mı",
		"benim ekibin servisleri",
	} {
		got := routeOf(t, q).Intent
		if got != guidedMyProblems && got != guidedMyServices {
			t.Errorf("%q → %q, takım-kapsamlı bir intent bekleniyordu", q, got)
		}
	}
}

// Aşırı eşleşme kontrolü: "benim" tek başına takım kapsamı AÇMAMALI.
func TestBenimAloneDoesNotClaimTeamScope(t *testing.T) {
	got := routeOf(t, "benim için en yavaş trace'ler").Intent
	if got == guidedMyProblems || got == guidedMyServices {
		t.Errorf("'benim' tek başına takım kapsamı açmamalı, alınan %q", got)
	}
}
