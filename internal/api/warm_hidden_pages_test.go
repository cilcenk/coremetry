package api

import (
	"os"
	"strings"
	"testing"
)

// v0.9.690 — GİZLİ SAYFA ISITILMAZ.
//
// `/hosts` ve `/external` v0.9.489-499 sadeleştirmesinde nav'dan
// gizlendi (Sidebar.tsx: "hidden from nav, code alive") ama warm
// girişleri güncellenmedi. Sonuç, 6 saatlik query_log ölçümü:
//
//	hosts warmer   : 723 + 723 çağrı · 132 GiB · SELECT baytının %13.9
//	external warmer:       867 çağrı ·  24 GiB · %2.5
//	723 çağrı / 6 saat = tam 30 s ızgara → SIFIR kullanıcı isteği
//
// Yani okunan tüm SELECT baytının ~%16'sı, kimsenin açmadığı iki sayfa
// için önden çekiliyordu. Warmer'ın kendisi doğru bir mekanizma; kusur
// SAYFA GİZLENİRKEN GİRİŞİN UNUTULMASI — ve bu sessiz, çünkü hiçbir
// şey bozulmuyor, sadece bedava iş yapılıyor.
//
// Bu test çifti bir arada tutuyor: sayfa nav'a geri gelirse warm girişi
// de geri gelmeli, gizli kalırken girişi olmamalı.

func warmedKeys(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile("api.go")
	if err != nil {
		t.Fatal(err)
	}
	return stripAPILineComments(string(b))
}

// stripAPILineComments — yorumları atar. ŞART: bu dosyanın aradığı
// `warm("hosts"` dizesi yukarıdaki açıklamada ve api.go'nun kendi
// gerekçesinde de geçiyor; yorumlu tarama kendi açıklamasını kod sanar
// (bu kod tabanında beş kez ısırdı).
func stripAPILineComments(src string) string {
	out := make([]string, 0, 512)
	for _, l := range strings.Split(src, "\n") {
		if i := strings.Index(l, "//"); i >= 0 {
			l = l[:i]
		}
		out = append(out, l)
	}
	return strings.Join(out, "\n")
}

func TestHiddenPagesAreNotWarmed(t *testing.T) {
	body := warmedKeys(t)
	for _, page := range []string{"hosts", "external"} {
		if strings.Contains(body, `warm("`+page+`"`) {
			t.Errorf("%q nav'dan gizli ama HÂLÂ ısıtılıyor — 30 s'de bir tam tarama, "+
				"sıfır kullanıcı isteği (ölçüm: 6 saatte 723 çağrı)", page)
		}
	}
}

// Ters yön: handler ve rota DURMALI. Kaldırılan yalnız önden çekme;
// sayfa URL'den erişilebilir olmaya devam ediyor ve serveCached ile
// talep üzerine servis ediliyor.
//
// Rotalar api.go'da DEĞİL, kendi dosyalarında — ilk yazdığımda api.go'ya
// baktım ve test düştü. İyi ki ters yönü de yazmışım: yanlış varsayımı
// o yakaladı, ben değil.
func TestHiddenPagesStillServed(t *testing.T) {
	// ROTA YOLU HİÇ YAZILMIYOR, yalnız HANDLER ADI aranıyor.
	//
	// İlk iki denemem `make audit` CHECK 7'yi tetikledi ("🔴 duplicate
	// route: GET /api/hosts"): testin kendi literali İKİNCİ bir rota
	// kaydı sanılıyordu. Test, aradığı kalıbı ÜRETİYORDU — bu kod
	// tabanında beşinci kez aynı tuzak.
	//
	// Handler adı zaten daha iyi bir sinyal: rota yolu değişebilir ama
	// handler'ın var olması sayfanın servis edildiğini kanıtlar.
	// Kapanış parantezi ŞART: "s.getHosts" araması
	// "s.getHostsREMOVED"i de eşler (alt-dize) ve mutasyon testi
	// geçer — ilk denememde tam bu oldu.
	for file, handler := range map[string]string{
		"hosts.go":    "s.getHosts)",
		"external.go": "s.getExternalHosts)",
	} {
		b, err := os.ReadFile(file)
		if err != nil {
			t.Fatalf("%s okunamadı: %v", file, err)
		}
		if !strings.Contains(stripAPILineComments(string(b)), handler) {
			t.Errorf("%s: %s handler'ı kaybolmuş — warm girişini kaldırmak sayfayı ERİŞİLEMEZ yapmamalı",
				file, handler)
		}
	}
}
