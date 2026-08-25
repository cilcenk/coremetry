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

var engineFiles = []string{"oracle.go", "postgres.go", "mysql.go", "redis.go"}

func TestCumulativeRatesUseObservedSpan(t *testing.T) {
	// `(max(...) - min(...)) / ?` — istenen pencereye bölen eski kalıp.
	bad := regexp.MustCompile(`min\([^)]*\)\)\s*/\s*\?`)
	for _, f := range engineFiles {
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
