package api

import (
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/cilcenk/coremetry/internal/chstore"
)

// v0.9.524 — operatör-bildirimli: /inbox'ta her satırda "N in last 5min"
// yazıyordu ve N, ÖMÜR BOYU toplam occurrence'tı. 28 Haziran'da ilk
// görülen bir grup, 35 günlük 18.217 occurrence'ının tamamını son 5
// dakikada olmuş gibi gösteriyordu.
//
// Kök sebep: freshMin grubun SON GÖRÜLME zamanını ölçer, g.Occurrences
// toplamı taşır — ikisini tek cümlede birleştirmek yalan üretiyordu.
// Gerçek pencereli sayı veri modelinde YOK; onu üretmek grup başına yeni
// sorgu demek (v0.9.522/523'te tam o sınıfı azalttık). Doğru çözüm sayıyı
// uydurmak değil, iki gerçeği ayrı ayrı söylemek.
func TestExceptionPriorityReasonDoesNotClaimWindowedCount(t *testing.T) {
	now := time.Now().UnixNano()
	old := chstore.ExceptionGroup{
		Fingerprint: "fp",
		FirstSeen:   now - int64(35*24*time.Hour), // 35 gün önce
		LastSeen:    now - int64(30*time.Second),  // az önce yine görüldü
		Occurrences: 18217,
	}

	prio, reason := exceptionPriority(old)
	if prio != "P1" {
		t.Fatalf("taze + yüksek hacim P1 olmalı, got %q", prio)
	}
	// YASAK: toplamı pencereli sayı gibi sunmak.
	if strings.Contains(reason, "18217 in last") || strings.Contains(reason, "18,217 in last") {
		t.Errorf("gerekçe toplamı pencereli sayı gibi sunuyor (v0.9.524 gerilemesi): %q", reason)
	}
	// Gerekçe İKİ gerçeği de taşımalı: tazelik + toplamın toplam olduğu.
	if !strings.Contains(reason, "last 5min") {
		t.Errorf("tazelik bilgisi kaybolmuş: %q", reason)
	}
	if !strings.Contains(reason, "total") {
		t.Errorf("sayının TOPLAM olduğu söylenmiyor: %q", reason)
	}
	if !strings.Contains(reason, "18,217") {
		t.Errorf("sayı binlik ayraçlı okunmalı: %q", reason)
	}
}

func TestFmtThousands(t *testing.T) {
	for in, want := range map[uint64]string{
		0: "0", 7: "7", 999: "999", 1000: "1,000",
		18217: "18,217", 137656: "137,656", 1234567: "1,234,567",
	} {
		if got := fmtThousands(in); got != want {
			t.Errorf("fmtThousands(%d) = %q, beklenen %q", in, got, want)
		}
	}
}

// v0.9.525 — ?since= yalnız sabit basamak kabul eder: cache anahtarına
// giriyor, serbest değer anahtar kardinalitesini patlatır (v0.8.270).
func TestNormalizeInboxSince(t *testing.T) {
	// v0.9.954 (F5/Ö13) — "1h" ve "30m" artık GEÇERLİ basamaklar. Eski
	// tablo "1h": "" bekliyordu, yani Ö13'ün düzelttiği kısıtı zorunlu
	// kılıyordu; pinin yönü döndü.
	for in, want := range map[string]string{
		"30m": "30m", "1h": "1h", "2h": "2h", "24h": "24h", "7d": "7d",
		"": "", "30d": "", "abc": "", "2H": "", "17m": "",
	} {
		if got := normalizeInboxSince(in); got != want {
			t.Errorf("normalizeInboxSince(%q) = %q, beklenen %q", in, got, want)
		}
	}
	// Süre eşlemesi normalizasyonla birebir: normalize edilen her basamak
	// pozitif süre vermeli, boş sıfır vermeli — ayrışırlarsa filtre
	// sessizce no-op olur.
	for _, v := range inboxSinceRungs {
		if inboxSinceDuration(v) <= 0 {
			t.Errorf("inboxSinceDuration(%q) pozitif olmalı", v)
		}
	}
	if inboxSinceDuration("") != 0 {
		t.Error("boş since süre üretmemeli")
	}
}

// v0.9.530 — "hesaplandı, çöpe atıldı" sınıfının İKİNCİ nüshası.
//
// v0.9.255 aynısını runbookUrl/recentDeploy için kapatmıştı: listInbox
// zenginleştirme turlarını ödüyor, mapper sonucu atıyordu. AISummary'de
// aynı şey oluyordu — ListProblems (problem.go:789) ve
// ListExceptionGroups (exception_inbox.go) ai_summary'yi ZATEN
// SELECT'liyor, iki mapper da kopyalamıyordu.
//
// Bu sınıfın sessiz olması tehlikeli: hata yok, sorgu yavaşlamıyor,
// yalnız operatör hiç göremiyor. Test kopyalamayı pinler.
func TestInboxMappersCarryAISummary(t *testing.T) {
	const at = int64(1_700_000_000_000_000_000)

	t.Run("problem", func(t *testing.T) {
		it := problemToInbox(chstore.Problem{
			ID: "p1", AISummary: "Olası neden: deploy v42", AISummaryAt: at,
		})
		if it.AISummary != "Olası neden: deploy v42" {
			t.Errorf("özet taşınmadı: %q", it.AISummary)
		}
		if it.AISummaryAt != at {
			t.Errorf("damga taşınmadı: %d", it.AISummaryAt)
		}
	})

	t.Run("exception", func(t *testing.T) {
		it := exceptionToInbox(chstore.ExceptionGroup{
			Fingerprint: "fp", Type: "NullPointerException",
			AISummary: "Olası neden: null config", AISummaryAt: at,
		})
		if it.AISummary != "Olası neden: null config" {
			t.Errorf("özet taşınmadı: %q", it.AISummary)
		}
		if it.AISummaryAt != at {
			t.Errorf("damga taşınmadı: %d", it.AISummaryAt)
		}
	})

	// Özet YOKSA damga da gitmemeli — "0 dk önce" gösterimi yalan olurdu.
	t.Run("özetsiz satır damga taşımaz", func(t *testing.T) {
		it := exceptionToInbox(chstore.ExceptionGroup{Fingerprint: "fp"})
		if it.AISummary != "" || it.AISummaryAt != 0 {
			t.Errorf("boş özet + sıfır damga beklenirdi: %q / %d", it.AISummary, it.AISummaryAt)
		}
	})
}

// Kırpma RUNE sınırında olmalı. Eski `s[:n]` çok baytlı bir karakteri
// ortadan böler; Türkçe'de ç/ğ/ı/ö/ş/ü hepsi 2 bayt, yani AI özetinde
// bölünme olasılığı yüksek. Bozuk bayt JSON'a girince U+FFFD'ye döner
// ve operatör triage satırında "�" görür.
func TestInboxTruncateIsRuneSafe(t *testing.T) {
	// 240 baytlık bütçenin tam sınırına Türkçe karakter denk getir.
	long := strings.Repeat("ş", 400) // her biri 2 bayt
	got := inboxTruncate(long, inboxAISummaryMax)
	if !utf8.ValidString(got) {
		t.Fatalf("kırpma geçersiz UTF-8 üretti: %q", got)
	}
	if !strings.HasSuffix(got, "…") {
		t.Errorf("kırpıldığı belli olmalı: %q", got)
	}
	if len(got) > inboxAISummaryMax+len("…") {
		t.Errorf("bütçe aşıldı: %d bayt", len(got))
	}

	// Her uzunlukta geçerli UTF-8 — sınırın hangi bayta düştüğü fark
	// etmemeli.
	for n := 1; n <= 60; n++ {
		s := strings.Repeat("ğüşiöç", 20)
		if out := inboxTruncate(s, n); !utf8.ValidString(out) {
			t.Errorf("n=%d geçersiz UTF-8: %q", n, out)
		}
	}

	// Bütçenin altındaki metin DEĞİŞMEZ — gereksiz "…" eklenmemeli.
	short := "kısa özet"
	if got := inboxTruncate(short, inboxAISummaryMax); got != short {
		t.Errorf("kısa metin değişmemeli: %q", got)
	}

	// ASCII davranışı korunmalı (mevcut g.Message kullanımı).
	if got := inboxTruncate(strings.Repeat("a", 300), 10); got != "aaaaaaaaaa…" {
		t.Errorf("ASCII kırpma bozuldu: %q", got)
	}
}
