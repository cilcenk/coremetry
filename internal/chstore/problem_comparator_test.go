package chstore

import (
	"strings"
	"testing"
	"time"
)

// v0.9.976 — SAHTE P1: ters-çevirme kuralın comparator'ına BAKMIYORDU.
//
// computePriority'nin kapısı `ratio < 1 && ratio > 0` idi ve yorumu
// "'<' kuralları için" diyordu — ama Problem struct'ı comparator TAŞIMIYORDU,
// yani kuralın yönü hesaba hiç girmiyordu. Sonuç: eşiğin ALTINDA kalan her
// ">" kuralı ters çevrilip "büyük ihlal" sayılıyordu.
//
// CANLI VAKA (bu testin çivisi): sms-service error_rate = 3.922, eşik 15,
// kural ">" . Oran 0.261 → 1/0.261 = 3.82× → critical + büyük ihlal → P1.
// Halbuki değer eşiğin dörtte biri; problem ihlalden UZAK.
//
// Ölçüm (canlı lokal, 5717 problem / 11 gün): oran kaynaklı critical
// P1'lerin 299'u ters çevrilmişti; error_rate + db_p99 + http_p99 üçlüsünün
// tersleri 60 satır SAF SAPMA (üçünün de comparator'ı ">").
func TestRatioFlipOnlyForBelowRules(t *testing.T) {
	now := time.Now().UnixNano()
	fresh := now - int64(10*time.Minute) // genç: bayat-critical yolu kapalı

	cases := []struct {
		name             string
		cmp              string
		sev              string
		value, threshold float64
		wantPri          string
		wantReason       string // reason'da GÖRÜNMESİ gereken parça
	}{
		// ── FIRTINANIN VAKASI ────────────────────────────────────────
		{"'>' kuralı eşiğin altında: ters ÇEVRİLMEZ (sms-service 3.922/15)",
			">", "critical", 3.922, 15, "P2", "critical"},
		{"'>=' kuralı eşiğin altında: ters ÇEVRİLMEZ",
			">=", "critical", 3.922, 15, "P2", "critical"},
		{"boş comparator (eski satır / anomali-monitor üreticisi): ters ÇEVRİLMEZ",
			"", "critical", 3.922, 15, "P2", "critical"},
		{"bilinmeyen comparator: ters ÇEVRİLMEZ",
			"!=", "critical", 3.922, 15, "P2", "critical"},

		// ── '<' AİLESİ: ters çevirme YAŞIYOR ─────────────────────────
		{"'<' kuralı: uptime 40/99 ters çevrilir → 2.5×",
			"<", "critical", 40, 99, "P1", "2.5x"},
		{"'<=' kuralı: başarı oranı 40/99 ters çevrilir",
			"<=", "critical", 40, 99, "P1", "2.5x"},
		{"'<' ama sınırın altında kalan düşüş (60/99 = 1.65×)",
			"<", "critical", 60, 99, "P2", "critical"},
		{"'<' kuralı boşluklu yazılmış (\" < \") yine çevrilir",
			" < ", "critical", 40, 99, "P1", "2.5x"},

		// ── '>' AİLESİ GERÇEK İHLALİ: dokunulmadı ────────────────────
		{"'>' kuralı eşiğin 3 katı: büyük ihlal (30/10)",
			">", "critical", 30, 10, "P1", "3.0x"},
		{"comparator boş ama gerçek ihlal: büyük ihlal (30/10)",
			"", "warning", 30, 10, "P2", "3.0x"},

		// ── TOTAL LOSS KOLU (v0.9.825) — comparator'dan BAĞIMSIZ ─────
		// Monitor DOWN operatör kararıyla P1 kalır; ratio kolu değil,
		// Value==0 && Threshold>0 kolu karar veriyor.
		{"monitor DOWN (0/1), comparator YOK → P1",
			"", "critical", 0, 1, "P1", "tamamen kayıp"},
		{"monitor DOWN (0/1), comparator '<' → P1",
			"<", "critical", 0, 1, "P1", "tamamen kayıp"},
		{"monitor DOWN (0/1), comparator '>' → yine P1",
			">", "critical", 0, 1, "P1", "tamamen kayıp"},
		{"sağlıklı pod kalmadı (0/3), comparator YOK → P1",
			"", "critical", 0, 3, "P1", "tamamen kayıp"},
		{"negatif eşikte sıfır tam kayıp DEĞİL",
			">", "critical", 0, -5, "P2", "critical"},

		// ── info pini ve sıfır eşik ──────────────────────────────────
		{"info her koşulda P3", "<", "info", 1, 99, "P3", "info"},
		{"eşik sıfır → oran yok", "<", "critical", 5, 0, "P2", "critical"},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p := Problem{
				Severity: c.sev, Status: "open",
				Value: c.value, Threshold: c.threshold,
				Comparator: c.cmp,
				StartedAt:  fresh,
			}
			pri, reason := computePriority(p, now, DefaultProblemPriority())
			if pri != c.wantPri {
				t.Errorf("öncelik = %s (%q), %s bekleniyordu.\n\n"+
					"comparator %q, değer %.3f, eşik %.3f. Ters çevirme YALNIZ "+
					"'<' / '<=' ailesinde olmalı; '>' ailesinde eşiğin altında "+
					"kalmak ihlalin BÜYÜKLÜĞÜ değil, UZAKLIĞIDIR.",
					pri, reason, c.wantPri, c.cmp, c.value, c.threshold)
			}
			if c.wantReason != "" && !strings.Contains(reason, c.wantReason) {
				t.Errorf("gerekçe %q, %q içermeliydi — operatör sırayı gerekçeden okuyor",
					reason, c.wantReason)
			}
		})
	}
}

// TestFlippedReasonMatchesFlippedGate — gerekçe metni ile kapı AYNI oranı
// kullanmalı (v0.8.321'in dersi, comparator kapısıyla birlikte).
//
// '>' ailesinde eşiğin altındaki bir değer artık ÇEVRİLMEDİĞİ için gerekçe
// de ham oranı yazmalı; "3.8x threshold" yazan bir P2 satırı operatöre
// "neden P1 değil?" diye sordururdu.
func TestFlippedReasonMatchesFlippedGate(t *testing.T) {
	now := time.Now().UnixNano()
	fresh := now - int64(10*time.Minute)

	// '>' kuralı, eşiğin altında: ham oran (0.3x), çevrilmiş 3.8x DEĞİL.
	p := Problem{Severity: "warning", Status: "open", Value: 3.922, Threshold: 15,
		Comparator: ">", StartedAt: fresh}
	_, reason := computePriority(p, now, DefaultProblemPriority())
	if strings.Contains(reason, "3.8x") {
		t.Errorf("gerekçe hâlâ çevrilmiş oranı yazıyor: %q", reason)
	}

	// '<' kuralı: çevrilmiş oran hem kapıda hem gerekçede.
	q := Problem{Severity: "warning", Status: "open", Value: 40, Threshold: 99,
		Comparator: "<", StartedAt: fresh}
	pri, reason := computePriority(q, now, DefaultProblemPriority())
	if pri != "P2" || !strings.Contains(reason, "2.5x") {
		t.Errorf("'<' warning 40/99 → (%s, %q); P2 + 2.5x bekleniyordu", pri, reason)
	}
}

// TestIsBelowRule — kapının kendisi, tablo-güdümlü.
func TestIsBelowRule(t *testing.T) {
	cases := map[string]bool{
		"<": true, "<=": true, " < ": true, "\t<=\n": true,
		">": false, ">=": false, "": false, "=": false, "!=": false,
		"lt": false, "LESS": false,
	}
	for in, want := range cases {
		if got := isBelowRule(in); got != want {
			t.Errorf("isBelowRule(%q) = %v, %v bekleniyordu", in, got, want)
		}
	}
}

// TestProblemColumnListsAgree — comparator'ı okuyan ve yazan listeler AYNI
// bayrakla kuruluyor mu?
//
// Bu simetri load-bearing: ReplacingMergeTree bütün-satır replace yapıyor.
// Okuma comparator'ı ATLAYIP yazma EKLERSE her ack/refresh/AI-özeti satırın
// yönünü DEFAULT ''e indirir ve öncelik hesabı sessizce eski hatalı
// davranışa döner (v0.9.445/448'in aynı sınıfı).
func TestProblemColumnListsAgree(t *testing.T) {
	for _, withCmp := range []bool{false, true} {
		cols := strings.Split(problemInsertCols(withCmp), ",")
		args := problemInsertArgs(Problem{Comparator: "<"}, withCmp)
		if len(cols) != len(args) {
			t.Fatalf("withComparator=%v: %d kolon, %d argüman — INSERT hizası bozuk",
				withCmp, len(cols), len(args))
		}
		if got := strings.Contains(problemInsertCols(withCmp), "comparator"); got != withCmp {
			t.Errorf("withComparator=%v ama kolon listesi: %s", withCmp, problemInsertCols(withCmp))
		}
	}
	// Değer gerçekten bağlanıyor mu (son argüman comparator olmalı).
	args := problemInsertArgs(Problem{Comparator: "<="}, true)
	if got, ok := args[len(args)-1].(string); !ok || got != "<=" {
		t.Errorf("son argüman comparator olmalıydı, %v geldi", args[len(args)-1])
	}
}
