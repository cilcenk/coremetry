package chstore

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// v0.10.15 — oran paydası kapısı.
//
// Kusur: pay `(max-min)` verinin GERÇEKTEN kapsadığı aralığı ölçüyor,
// payda ise operatörün İSTEDİĞİ pencerenin tamamıydı. Sayı makul
// görünüyor — yalnız yanlış.
//
// ÖLÇÜLDÜ (lokal CH 26.2.4.23, canlı `oracledb.executions`). Sapma
// yapısal değil oransal: receiver'ın kazıma aralığı her pencerenin iki
// ucunda sabit bir boşluk bırakıyor, dolayısıyla pencere kısaldıkça
// oran BÜYÜYOR:
//
//	istenen   gözlenen   operatörün gördüğü
//	   900s       610s   %32.2 DÜŞÜK
//	  3600s      2810s   %21.9 DÜŞÜK
//	 21600s     21309s    %1.3 DÜŞÜK
//	 86400s     86110s    %0.3 DÜŞÜK
//
// Yani en çok bakılan pencerede (15dk triyaj) hata en büyüktü. Bu,
// kusurun neden fark edilmediğini de açıklıyor: uzun pencerede sayı
// doğru çıkıyor, kısa pencerede üçte bir eksik — ve kimse ikisini yan
// yana koymuyor.
//
// ⚠ BU KAPIYI DÜZELTMENİN KENDİSİ DOĞURDU. Paydayı değiştirirken bir
// sorguda `?` yer tutucusunu kaldırıp `windowSec` bind'ini bıraktım
// (bütün bind sırası kayar), iki sorguda tersini yaptım. `go build`
// hiçbirini göremez: SQL seviyesinde kusur, çalışma zamanında patlar.

// engineFiles — ARTIK ELLE TUTULMUYOR (v0.10.54).
//
// ⚠ Bu liste dört dosya sayıyordu ve kapı yalnız onlara bakıyordu. v0.10.15
// kümülatif oran paydasını düzeltirken db_capacity.go'daki BEŞİNCİ yeri
// atladı; kapı da göremedi, çünkü o dosya listede yoktu. Muhafız, koruduğu
// kurala değil bir DİLİM ADINA bağlıydı ([[feedback-guard-bound-to-slice-name]]).
//
// Artık paketin tamamı taranıyor: yeni bir motor okuyucusu eklendiğinde
// kimsenin listeyi güncellemesi gerekmiyor.
func engineSourceFiles(t *testing.T) []string {
	t.Helper()
	ents, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("paket okunamadı: %v", err)
	}
	var out []string
	for _, e := range ents {
		n := e.Name()
		if !e.IsDir() && strings.HasSuffix(n, ".go") && !strings.HasSuffix(n, "_test.go") {
			out = append(out, n)
		}
	}
	return out
}

func TestCumulativeRatesUseObservedSpan(t *testing.T) {
	// `(max(...) - min(...)) / ?` — istenen pencereye bölen eski kalıp.
	// ⚠ `minIf(` biçimi ESKİ regex'e takılmıyordu: `min\(` literali
	// istiyordu ve postgres.go per-database oranlarını `minIf(...)` ile
	// yazıyor. Kuralın ikinci YAZIMI ilkinden habersizdi
	// ([[feedback-gate-single-spelling]]).
	bad := regexp.MustCompile(`min(If)?\([^)]*\)\)?\s*/\s*\?`)
	for _, f := range engineSourceFiles(t) {
		t.Run(f, func(t *testing.T) {
			b, err := os.ReadFile(f)
			if err != nil {
				t.Fatalf("%s okunamadı: %v", f, err)
			}
			src := string(b)
			if m := bad.FindString(src); m != "" {
				t.Errorf("%s hâlâ istenen pencereye bölüyor (%q) — "+
					"veri pencereden kısaysa oran SESSİZCE düşük çıkar", f, m)
			}
			// Pozitif taraf: dosya oran hesaplıyorsa gözlenen aralığı
			// kullanmalı. Yalnız negatif kontrol, kalıbın tamamen
			// silinmesiyle de yeşile döner.
			if strings.Contains(src, "min(value))") && !strings.Contains(src, "observedSpanSQL") {
				t.Errorf("%s kümülatif oran hesaplıyor ama observedSpanSQL kullanmıyor", f)
			}
		})
	}
}

// TestObservedSpanNeverDividesByZero — tek örnek tuzağı.
//
// Pencerede tek nokta varsa `min(time) == max(time)` ve aralık 0'dır.
// `greatest(..., 1)` olmadan SQL sıfıra bölerdi.
func TestObservedSpanNeverDividesByZero(t *testing.T) {
	if !strings.Contains(observedSpanSQL, "greatest(") {
		t.Error("observedSpanSQL sıfıra karşı korumasız — pencerede tek örnek varsa min==max")
	}
	if !strings.Contains(observedSpanSQL, "dateDiff('second'") {
		t.Error("observedSpanSQL gözlenen aralığı SANİYE olarak ölçmüyor")
	}
}

// TestBackMultiplyUsesTheSameSpan — payda düzeltilirken toplamın
// sessizce şişmemesi.
//
// `CPUTimeSec` oranı tekrar toplama çeviriyor. Payda gözlenen aralığa
// çevrildikten sonra o çarpım hâlâ `windowSec` kullanırsa toplam
// olduğundan büyük çıkar — ve bu, düzeltmenin YENİ bir yanlış sayı
// üretmesi demektir. Denetim bunu "sessizce yanlışlanabilecek tek satır"
// diye işaretlemişti.
func TestBackMultiplyUsesTheSameSpan(t *testing.T) {
	b, err := os.ReadFile("oracle.go")
	if err != nil {
		t.Fatalf("oracle.go okunamadı: %v", err)
	}
	src := stripGoCommentsCH(string(b))
	if strings.Contains(src, `rates["oracledb.cpu_time"] * windowSec`) {
		t.Error("CPUTimeSec hâlâ istenen pencereyle geri çarpıyor — " +
			"payda gözlenen aralığa çevrildi, toplam şişer")
	}
	if !strings.Contains(src, `rates["oracledb.cpu_time"] * observedSec`) {
		t.Error("CPUTimeSec gözlenen aralıkla geri çarpmıyor")
	}
}

func stripGoCommentsCH(src string) string {
	src = regexp.MustCompile(`(?s)/\*.*?\*/`).ReplaceAllString(src, "")
	var b strings.Builder
	for _, line := range strings.Split(src, "\n") {
		if i := strings.Index(line, "//"); i >= 0 {
			line = line[:i]
		}
		b.WriteString(line)
		b.WriteByte('\n')
	}
	return b.String()
}

// TestBackMultiplyUsesTheMetricsOwnSpan — v0.10.54.
//
// ⚠ queryOracleRates eskiden TEK bir `observed` değeri döndürüyordu ve
// döngüde her satırda ÜZERİNE yazıyordu: fonksiyon SON TARANAN metriğin
// aralığını veriyordu. Çağıran onunla cpu_time'ı geri çarpıyor.
//
// Metrikler farklı zamanlarda başlarsa (yeni receiver, yeniden başlatılmış
// instance) cpu_time BAŞKA bir metriğin aralığıyla çarpılıyordu ve
// CPUTimeSec makul görünen ama yanlış bir toplam oluyordu.
//
// Aralık artık metrik başına; geri çarpım metriğin KENDİ aralığını
// kullanmak zorunda.
func TestBackMultiplyUsesTheMetricsOwnSpan(t *testing.T) {
	b, err := os.ReadFile("oracle.go")
	if err != nil {
		t.Fatalf("oracle.go okunamadı: %v", err)
	}
	src := stripGoCommentsCH(string(b))

	if !strings.Contains(src, "observedFor[m] = obs") {
		t.Error("gözlenen aralık metrik başına taşınmıyor — tek değer, son " +
			"taranan satırın aralığıdır ve geri çarpım yanlış metriği kullanır")
	}
	if !strings.Contains(src, `rates["oracledb.cpu_time"] * observedSec["oracledb.cpu_time"]`) {
		t.Error("geri çarpım cpu_time'ın KENDİ aralığını kullanmıyor — " +
			"CPUTimeSec makul görünen ama yanlış bir toplam olur")
	}
}
