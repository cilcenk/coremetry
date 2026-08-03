// v0.9.570 regresyon testi — çok kelimeli sinyal kalıpları KELİME
// SINIRINDA eşleşmeli.
//
// Bug: hasSlowTraceSignal sınırsız strings.Contains kullanıyordu ve
// Türkçede en doğal SRE sorusuyla çarpışıyordu:
//
//	"bu trace neden yavaş?" ⊃ "en yavaş"  →  ned|EN YAVAŞ
//	"neden uzun sürdü"      ⊃ "en uzun"   →  ned|EN UZUN sürdü
//
// Sonuç: operatör /trace sayfasında EKRANDAKİ trace'i sorarken router
// onu "en yavaş trace'leri listele" niyetine yönlendiriyor ve FİLO
// GENELİ bir liste dönüyordu. Hata görünmez — cevap makul bir cevap,
// yalnız sorulan soruya ait değil.
package api

import "testing"

func TestContainsPhraseRespectsWordBoundaries(t *testing.T) {
	cases := []struct {
		msg    string
		phrase string
		want   bool
		why    string
	}{
		// ASIL BUG: kalıp bir kelimenin ORTASINDA geçiyor.
		{"bu trace neden yavaş?", "en yavaş", false,
			"'neden yavaş' içindeki 'en yavaş' bir kelime sınırında DEĞİL"},
		{"neden uzun sürdü", "en uzun", false,
			"'neden uzun' içindeki 'en uzun' bir kelime sınırında DEĞİL"},

		// Gerçek kullanım BOZULMAMALI.
		{"en yavaş trace'ler", "en yavaş", true, "cümle başında, sınırda"},
		{"bana en yavaş 10 trace", "en yavaş", true, "boşluklar arasında"},
		{"trace en uzun neydi", "en uzun", true, "sınırda"},

		// Noktalama sınır sayılır.
		{"hangileri en yavaş?", "en yavaş", true, "soru işareti sınır"},
		{"(en yavaş)", "en yavaş", true, "parantez sınır"},

		// Türkçe harfler sınır SAYILMAZ — ASCII testi ş/ğ/ı'yı sınır
		// sanıp kalıbı bölerdi.
		{"şuen yavaş", "en yavaş", false, "önünde Türkçe harf var"},

		// SON EK SERBEST — bilinçli asimetri. İki uçta da sınır isteyen
		// ilk taslak mevcut testleri düşürdü ve haklı olarak düşürdü:
		// "slow trace" → "slow traceS" (İngilizce çoğul), "en yavaş" →
		// "en yavaşI"/"yavaşLAR" (Türkçe eklemeli yapı).
		{"slow traces in the last hour", "slow trace", true,
			"İngilizce çoğul eki eşleşmeyi bozmamalı"},
		{"en yavaşı hangisi", "en yavaş", true,
			"Türkçe iyelik eki eşleşmeyi bozmamalı"},

		// Kayarak arama: ilk geçiş sınırda değil, ikincisi sınırda.
		{"neden yavaş, en yavaş hangisi", "en yavaş", true,
			"ilk geçiş kelime içi, ikincisi sınırda — kayarak bulmalı"},

		{"", "en yavaş", false, "boş mesaj"},
		{"en yavaş", "", false, "boş kalıp asla eşleşmez"},
	}
	for _, c := range cases {
		if got := containsPhrase(c.msg, c.phrase); got != c.want {
			t.Errorf("containsPhrase(%q, %q) = %v, beklenen %v — %s",
				c.msg, c.phrase, got, c.want, c.why)
		}
	}
}

// v0.9.570 — sinyal seviyesinde korunan tek şey: KELİME İÇİ çarpışma.
//
// "neden yavaş" ARTIK açık bir kalıp (bağlamsız sorulduğunda "en yavaş
// trace'ler" makul bir cevap ve bu sözleşme pinli). Ama "neden uzun
// sürdü" hiçbir kalıba ait değil — eskiden "en uzun"a çarpıyordu.
func TestSlowTraceSignalNoMidWordCollision(t *testing.T) {
	shouldNotFire := []string{
		"neden uzun sürdü",
		"bu istek neden uzun sürüyor",
		"şuen yavaş bir şey", // kelime içi
	}
	for _, m := range shouldNotFire {
		if hasSlowTraceSignal(m) {
			t.Errorf("%q kelime İÇİ eşleşmeyle slow_traces tetikledi", m)
		}
	}

	shouldFire := []string{
		"en yavaş trace'ler",
		"slowest traces",
		"en uzun süren istekler",
		"yavaş trace göster",
		"neden yavaş", // bağlamsız: açık kalıp, sözleşme pinli
	}
	for _, m := range shouldFire {
		if !hasSlowTraceSignal(m) {
			t.Errorf("%q slow_traces tetiklemedi — gerçek kullanım bozuldu", m)
		}
	}
}

// İŞARET ZAMİRİ KAPISI — asıl düzeltme burada.
//
// "bu trace neden yavaş?" hem slow_traces sinyalini tetikler hem de
// ekrandaki trace'i işaret eder. Ayrımı taşıyan tek sinyal işaret
// zamiri: liste sorusu mu, ekrandaki şey mi?
func TestHasDemonstrativeTrace(t *testing.T) {
	yes := []string{
		"bu trace neden yavaş?",
		"bu trace'i açıkla",
		"şu trace nerede takıldı",
		"explain this trace",
		"bu izde ne oldu", // "bu iz"
	}
	for _, m := range yes {
		if !hasDemonstrativeTrace(m) {
			t.Errorf("%q ekrandaki trace'i işaret ediyor ama yakalanmadı — "+
				"operatör ekranındaki trace yerine FİLO GENELİ liste görür", m)
		}
	}

	no := []string{
		"en yavaş trace'ler", // LİSTE sorusu — ekrandakini istemiyor
		"slowest traces",
		"trace listesi ver",
		"neden yavaş", // trace'ten hiç bahsetmiyor
	}
	for _, m := range no {
		if hasDemonstrativeTrace(m) {
			t.Errorf("%q bir LİSTE sorusu ama ekrandaki trace'e yönlendirildi", m)
		}
	}
}
