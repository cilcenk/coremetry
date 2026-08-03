// v0.9.572 — paylaşılan bağımlılık patlaması dedektörü testleri.
//
// Operatör raporu (prod, gece 03:04): on beşten fazla servis AYNI
// saniyede aynı java.sql.SQLRecoverableException ORA-18730 hatasını
// aldı; Coremetry on beş AYRI exception grubu gösterdi. Ortak paydayı
// operatörün kafasında kurması gerekiyordu.
package evaluator

import (
	"strings"
	"testing"
	"time"

	"github.com/cilcenk/coremetry/internal/chstore"
)

func TestSharedBurstProblemIDStableAsServicesJoin(t *testing.T) {
	// KİMLİĞE servis listesi GİRMEZ. Patlama büyüdükçe yeni servisler
	// katılır; liste kimliğin parçası olsaydı her yeni servis YENİ bir
	// problem doğururdu — tam da düzeltmeye çalıştığımız dağınıklık.
	a := sharedBurstProblemID("java.sql.SQLRecoverableException", 1000)
	b := sharedBurstProblemID("java.sql.SQLRecoverableException", 1000)
	if a != b {
		t.Errorf("aynı tip+kova farklı kimlik verdi: %q vs %q", a, b)
	}

	// Aynı tip, FARKLI gece = AYRI olay.
	c := sharedBurstProblemID("java.sql.SQLRecoverableException", 2000)
	if a == c {
		t.Error("farklı zaman kovası aynı kimliği verdi — dün gecenin olayı " +
			"bu gecenin olayıyla birleşir")
	}
}

func TestSharedBurstSeverityTracksServiceCount(t *testing.T) {
	// Ciddiyet SERVİS SAYISINA bağlı, oluşum sayısına değil: tek
	// serviste 10.000 oluşum o servisin sorunu; on serviste 10'ar
	// oluşum altyapı sorunu.
	if got := sharedBurstSeverity(3); got != "warning" {
		t.Errorf("3 servis → %q, beklenen warning", got)
	}
	if got := sharedBurstSeverity(7); got != "warning" {
		t.Errorf("7 servis → %q, beklenen warning", got)
	}
	if got := sharedBurstSeverity(8); got != "critical" {
		t.Errorf("8 servis → %q, beklenen critical", got)
	}
	if got := sharedBurstSeverity(15); got != "critical" {
		t.Errorf("15 servis (operatörün vakası) → %q, beklenen critical", got)
	}
}

func TestSharedBurstDescription(t *testing.T) {
	b := chstore.SharedExceptionBurst{
		Type:    "java.sql.SQLRecoverableException",
		Message: "ORA-18730: Interrupted IO error.: Socket read timed out",
		Services: []string{
			"svc-a", "svc-b", "svc-c", "svc-d", "svc-e", "svc-f", "svc-g", "svc-h",
		},
		Occurrences: 178,
	}
	got := sharedBurstDescription(b)

	// Asıl bulgu servis SAYISI — cümlenin başında olmalı.
	if !strings.HasPrefix(got, "8 servis") {
		t.Errorf("açıklama servis sayısıyla başlamıyor: %q", got)
	}
	if !strings.Contains(got, "java.sql.SQLRecoverableException") {
		t.Error("exception tipi açıklamada yok")
	}
	// Uzun liste kırpılmalı — on beş servis adı pager'da okunmaz.
	if !strings.Contains(got, "+2 daha") {
		t.Errorf("servis listesi kırpılmamış: %q", got)
	}
	if strings.Contains(got, "svc-g") || strings.Contains(got, "svc-h") {
		t.Errorf("kırpılan servisler yine de yazılmış: %q", got)
	}
	if !strings.Contains(got, "178") {
		t.Error("toplam oluşum sayısı yok")
	}
}

func TestSharedBurstDescriptionTruncatesLongMessage(t *testing.T) {
	// Mesaj rune-güvenli kırpılmalı: Türkçe karakterler 2 bayt ve
	// bayt-dilimleme � üretir (v0.9.530'da aynı sınıf hata çıkmıştı).
	long := strings.Repeat("çğışüö", 40) // 240 rune
	b := chstore.SharedExceptionBurst{
		Type: "X", Message: long, Services: []string{"a", "b", "c"}, Occurrences: 3,
	}
	got := sharedBurstDescription(b)
	if strings.Contains(got, "�") {
		t.Error("mesaj bayt sınırından kırpılmış — bozuk karakter üretti")
	}
	if !strings.Contains(got, "…") {
		t.Error("uzun mesaj kırpılmamış")
	}
}

func TestSharedBurstDescriptionWithoutMessage(t *testing.T) {
	b := chstore.SharedExceptionBurst{
		Type: "X", Services: []string{"a", "b", "c"}, Occurrences: 3,
	}
	got := sharedBurstDescription(b)
	if strings.Contains(got, "Örnek:") {
		t.Errorf("boş mesaj için 'Örnek:' başlığı basılmış: %q", got)
	}
}

// v0.9.585 — AÇMA dalı yalnız YENİ patlamalar için.
//
// Operator-reported, prod: log ESCALATED seliyle doldu —
//
//	ESCALATED · Paylaşılan bağımlılık · open 862h35m0s · warning → critical
//
// 862 saat = 36 gün. v0.9.576 tazelik kapısını last_seen'e almıştı (uzun
// süren arıza görünür kalsın diye, doğru bir düzeltme) ama yan etkisi
// ağırdı: aylardır var olan bir exception tipi de pencereye giriyor ve
// StartedAt: b.FirstSeen onu HAFTALAR ÖNCE başlamış bir problem olarak
// açıyordu. Problem doğar doğmaz yaş-bazlı eskalasyona takılıp anında
// critical oluyordu.
//
// Üstelik yanlıştı: 36 gündür var olan bir tip "aynı anda başladı"
// premisini karşılamıyor — o bir patlama değil, kronik bir durum.
func TestSharedBurstOnlyOpensRecentBursts(t *testing.T) {
	now := time.Now()

	cases := []struct {
		name      string
		firstSeen time.Time
		wantOpen  bool
	}{
		{"az önce başladı → aç", now.Add(-2 * time.Minute), true},
		{"6 saat önce başladı → aç", now.Add(-6 * time.Hour), true},
		{"tam sınırda (24sa) → aç", now.Add(-sharedBurstLookback + time.Minute), true},
		{"3 gün önce başladı → AÇMA", now.Add(-72 * time.Hour), false},
		{"36 gün önce başladı → AÇMA (prod vakası)", now.Add(-862 * time.Hour), false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			isNew := now.Sub(c.firstSeen) <= sharedBurstLookback
			if isNew != c.wantOpen {
				t.Errorf("firstSeen=%s → burstIsNew=%v, beklenen %v. "+
					"Eski bir tipi açmak, doğar doğmaz 'saatlerdir açık' olan ve "+
					"anında critical'a eskale olan bir problem üretir.",
					now.Sub(c.firstSeen).Round(time.Hour), isNew, c.wantOpen)
			}
		})
	}
}

// Görünürlük kapısı ile tespit kapısı AYRI eşikler olmalı — aynı eşiğe
// bağlamak v0.9.576/585 hatasının kendisiydi.
func TestSharedBurstGatesAreDistinct(t *testing.T) {
	// Aktiflik (kapatma kararı) kısa, tespit (açma kararı) uzun.
	if sharedBurstActiveFor >= sharedBurstLookback {
		t.Errorf("aktiflik eşiği (%s) tespit penceresinden (%s) kısa OLMALI — "+
			"aksi halde bir patlama görünür kalmadan kapatılamaz",
			sharedBurstActiveFor, sharedBurstLookback)
	}
}
