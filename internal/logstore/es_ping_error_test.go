package logstore

import (
	"errors"
	"fmt"
	"strings"
	"testing"
)

// v0.9.630 — "ELASTICSEARCH UNREACHABLE AT BOOT" çoğu zaman YALAN'dı.
//
// Operatör-bildirimli: satır çıkıyor ama ES kendiliğinden düzeliyor.
// Sebep ağ değil sıralama — API key system_settings'te duruyor,
// buildLogStore LoadPersisted'dan ÖNCE koşuyor, ping auth=none gidip
// 401 yiyor. Küme ayakta, adresler doğru. Eski satır operatörü ağa ve
// adreslere bakmaya yolluyordu; bu testler her sınıfın DOĞRU cümleyi
// aldığını çiviliyor.

func TestESPingErrorClassification(t *testing.T) {
	net := &ESPingError{Status: 0, AuthMode: "api-key", Err: errors.New("dial tcp: i/o timeout")}
	if !net.Unreachable() || net.Unauthorized() || net.CredentialsAbsent() {
		t.Fatal("ağ hatası yalnız Unreachable olmalı")
	}

	noAuth := &ESPingError{Status: 401, AuthMode: "none", Body: "security_exception"}
	if noAuth.Unreachable() || !noAuth.Unauthorized() || !noAuth.CredentialsAbsent() {
		t.Fatal("kimliksiz 401 hem Unauthorized hem CredentialsAbsent olmalı")
	}

	badKey := &ESPingError{Status: 401, AuthMode: "api-key", Body: "security_exception"}
	if !badKey.Unauthorized() || badKey.CredentialsAbsent() {
		t.Fatal("kimlik VARKEN gelen 401 CredentialsAbsent OLMAMALI — bu gerçek bir yapılandırma hatası")
	}

	other := &ESPingError{Status: 503, AuthMode: "api-key", Body: "unavailable"}
	if other.Unreachable() || other.Unauthorized() || other.CredentialsAbsent() {
		t.Fatal("503 ne ulaşılamaz ne yetkisiz")
	}
}

func TestESBootDiagnosis(t *testing.T) {
	cases := []struct {
		name            string
		err             error
		expectPersisted bool
		wantHeadline    string
		mustNotSay      string
	}{
		{
			// OPERATÖRÜN VAKASI: küme cevap verdi, kimlik henüz yüklenmedi.
			name:            "kimliksiz 401 + kaydedilmiş ayar var",
			err:             &ESPingError{Status: 401, AuthMode: "none"},
			expectPersisted: true,
			wantHeadline:    "henüz yüklenmedi",
			mustNotSay:      "ULAŞILAMIYOR",
		},
		{
			// Kaydedilmiş ayar YOK: bu gerçekten yapılandırma eksiği.
			name:            "kimliksiz 401 + kaydedilmiş ayar yok",
			err:             &ESPingError{Status: 401, AuthMode: "none"},
			expectPersisted: false,
			wantHeadline:    "KİMLİK REDDETTİ",
			mustNotSay:      "ULAŞILAMIYOR",
		},
		{
			name:            "bozuk api-key",
			err:             &ESPingError{Status: 403, AuthMode: "api-key"},
			expectPersisted: true,
			wantHeadline:    "KİMLİK REDDETTİ",
			mustNotSay:      "ULAŞILAMIYOR",
		},
		{
			name:            "gerçekten ulaşılamıyor",
			err:             &ESPingError{Status: 0, AuthMode: "api-key", Err: errors.New("i/o timeout")},
			expectPersisted: true,
			wantHeadline:    "ULAŞILAMIYOR",
			mustNotSay:      "KİMLİK",
		},
		{
			name:            "başka HTTP hatası",
			err:             &ESPingError{Status: 503, AuthMode: "api-key"},
			expectPersisted: false,
			wantHeadline:    "YAPILANDIRMA HATASI",
			mustNotSay:      "ULAŞILAMIYOR",
		},
		{
			// Tipsiz hata (ping dışı bir başarısızlık) — genel cümle.
			name:            "tipsiz hata",
			err:             fmt.Errorf("create ES client: bad url"),
			expectPersisted: false,
			wantHeadline:    "BAŞLATILAMADI",
			mustNotSay:      "ULAŞILAMIYOR",
		},
	}
	for _, c := range cases {
		headline, hint := ESBootDiagnosis(c.err, c.expectPersisted)
		if !strings.Contains(headline, c.wantHeadline) {
			t.Errorf("%s: başlık %q, içermeliydi %q", c.name, headline, c.wantHeadline)
		}
		if strings.Contains(headline, c.mustNotSay) {
			t.Errorf("%s: başlık yanlış sebebi söylüyor (%q içinde %q)", c.name, headline, c.mustNotSay)
		}
		if hint == "" {
			t.Errorf("%s: ipucu boş — operatöre ne yapacağı söylenmiyor", c.name)
		}
	}
}

// Ulaşılabilir bir kümenin hatası ASLA "ağ" diye okunmamalı: eski
// davranışta 401 de dial-timeout da aynı satırı basıyordu ve operatör
// yanlış yere bakıyordu.
func TestReachableClusterNeverReportedAsNetwork(t *testing.T) {
	for _, status := range []int{400, 401, 403, 404, 429, 500, 503} {
		err := &ESPingError{Status: status, AuthMode: "api-key"}
		headline, hint := ESBootDiagnosis(err, false)
		if strings.Contains(headline, "ULAŞILAMIYOR") {
			t.Errorf("HTTP %d ulaşılabilir bir küme — 'ULAŞILAMIYOR' denemez: %s", status, headline)
		}
		if !strings.Contains(hint, "ULAŞILABİLİR") {
			t.Errorf("HTTP %d ipucu kümenin ulaşılabilir olduğunu söylemeli: %s", status, hint)
		}
	}
}

// Hata metni bilgi KAYBETMEMELİ: tipleştirme, operatörün gördüğü
// ayrıntıyı azaltmamalı.
func TestESPingErrorMessageKeepsDetail(t *testing.T) {
	e := &ESPingError{
		Status: 401, AuthMode: "none",
		Addresses: []string{"https://es:9200"}, Body: "security_exception",
	}
	msg := e.Error()
	for _, want := range []string{"auth=none", "es:9200", "security_exception", "COREMETRY_ES_API_KEY"} {
		if !strings.Contains(msg, want) {
			t.Errorf("hata metni %q içermeli: %s", want, msg)
		}
	}

	n := &ESPingError{Status: 0, Addresses: []string{"https://es:9200"}, Err: errors.New("i/o timeout")}
	if !strings.Contains(n.Error(), "i/o timeout") || !strings.Contains(n.Error(), "es:9200") {
		t.Errorf("ağ hatası taşınan hatayı ve adresi korumalı: %s", n.Error())
	}
	if !errors.Is(n, n.Err) {
		t.Error("Unwrap zinciri kopuk")
	}
}
