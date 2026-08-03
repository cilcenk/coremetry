// v0.9.609 — altyapı-ölümcül exception dedektörünün regresyon testi.
//
// Operatör isteği: "UnknownHostException alan varsa çok kritik, hemen
// P1 üret."
//
// Testin iki yönü var ve ikincisi daha önemli: doğru tipin
// yakalandığı KADAR, yanlış tipin yakalanMADIĞI da. P1'in değeri
// seyrekliğinden gelir — ConnectException'ı da P1 yapsaydık operatör
// P1'lere bakmayı bırakırdı ve dedektör kendi amacını yok ederdi.
package evaluator

import (
	"strings"
	"testing"

	"github.com/cilcenk/coremetry/internal/chstore"
)

func TestFatalExceptionTypeMatching(t *testing.T) {
	yes := []string{
		"java.net.UnknownHostException",
		"UnknownHostException",
		"javax.naming.UnknownHostException",
	}
	for _, ty := range yes {
		if !chstore.IsFatalExceptionType(ty) {
			t.Errorf("%q ölümcül sayılmadı — operatörün açık isteği bu tip", ty)
		}
	}
}

// TestNonFatalTypesAreNotP1 — TEK OLUŞUMDA P1 seyrek kalmalı.
//
// ConnectException hedefin VAR olduğunu ama cevap vermediğini söyler:
// çoğu zaman geçici, yeniden deneme düzeltir, pod yeniden başlarken
// normaldir. Tek oluşumda P1 yapmak, operatörün P1'e verdiği tepkiyi
// değersizleştirir — ve o tepki dedektörün tek çıktısı.
//
// ⚠ BU "ASLA P1 OLMAZ" DEMEK DEĞİL (v0.9.610, operatör düzeltmesi).
// Aynı tip AYNI ANDA dört ayrı serviste görünüyorsa paylaşılan bir
// bağımlılık düşmüştür ve paylaşılan-patlama dedektörü onu critical
// açar. İki dedektör bir sözleşme paylaşıyor: bu test TEKİL yolu
// kapatıyor, TestRetryableTypesBecomeP1WhenShared PAYLAŞILAN yolun
// açık kaldığını doğruluyor. İkisi birden kapanırsa vaka hiç
// görünmez.
func TestNonFatalTypesAreNotP1(t *testing.T) {
	no := []string{
		"java.net.ConnectException",
		"java.net.SocketTimeoutException",
		"java.sql.SQLRecoverableException",
		"java.lang.NullPointerException",
		"org.apache.kafka.common.errors.TimeoutException",
		"",
		"   ",
		// Sonek eşleşmesi ÖNEK'e kaymamalı: bu tip UnknownHostException
		// ile BAŞLIYOR ama o değil.
		"UnknownHostExceptionHandler",
	}
	for _, ty := range no {
		if chstore.IsFatalExceptionType(ty) {
			t.Errorf("%q ölümcül sayıldı — P1 seyrek kalmalı, yoksa operatör "+
				"P1'lere bakmayı bırakır ve dedektör amacını yok eder", ty)
		}
	}
}

// TestDescriptionCarriesTheTarget — cevap EYLEME dönüşebilmeli.
//
// "UnknownHostException aldı" bir gözlem; "şu adı çözemiyor" bir
// başlangıç noktası. Operatör hedef adı olmadan hiçbir yere gidemez.
func TestDescriptionCarriesTheTarget(t *testing.T) {
	d := fatalExcDescription(chstore.FatalException{
		Type:    "java.net.UnknownHostException",
		Service: "odeme-servisi",
		Message: "calendar-api.internal.svc.cluster.local",
		Count:   42,
	})
	for _, want := range []string{"odeme-servisi", "calendar-api.internal.svc.cluster.local", "42"} {
		if !strings.Contains(d, want) {
			t.Errorf("açıklama %q taşımıyor: %q", want, d)
		}
	}
	// Yeniden denemenin işe yaramayacağı SÖYLENMELİ: operatörün ilk
	// refleksi "geçer herhalde" olmasın.
	if !strings.Contains(d, "Yeniden deneme") {
		t.Errorf("açıklama yeniden denemenin çare olmadığını söylemiyor: %q", d)
	}
}

// TestProblemIDHasNoTimeBucket — bu bir DURUM, olay değil.
//
// Kimliği zaman kovasına bağlasaydık saatlerdir süren TEK bir arıza her
// kovada yeni bir P1 doğururdu — operatörün gece 3'te göreceği son şey
// aynı arızanın on iki kopyası.
func TestProblemIDHasNoTimeBucket(t *testing.T) {
	a := fatalExcProblemID("java.net.UnknownHostException", "svc-a")
	b := fatalExcProblemID("java.net.UnknownHostException", "svc-a")
	if a != b {
		t.Errorf("aynı (tip, servis) iki farklı kimlik üretti: %q vs %q", a, b)
	}
	if a == fatalExcProblemID("java.net.UnknownHostException", "svc-b") {
		t.Error("farklı servisler aynı kimliği paylaşıyor — biri ötekini ezer")
	}
}
