package insight

import (
	"strings"
	"testing"
	"unicode/utf8"
)

// prompt_test.go — v0.9.1129. Kartın prose yarısının KULLANICI prompt'u
// aynı kanıttan türüyor; testin işi "kartta gördüğün sayı modele de
// gitti mi" sorusunu kilitlemek.

func TestProblemPromptUserCarriesEvidence(t *testing.T) {
	now := int64(1_700_000_000) * 1e9
	ev := ProblemEvidence{
		ID: "p1", Service: "checkout", Metric: "error_rate",
		Severity: "critical", Priority: "P1", PriorityReason: "kritik + deploy",
		Comparator: ">", Value: 12.5, Threshold: 5,
		StartedNs: now - 2*3600*1e9, NowNs: now,
		Deploy:    &DeployRef{Version: "v2.1.0", AgeSec: 240, HasImpact: true, P99DeltaPct: 34, ErrDeltaPP: 2.4},
		Blast:     &BlastRef{TotalCallers: 12, CascadingCallers: 3, TopCallers: []string{"web", "bff"}},
		SlowOp:    &OpRef{Name: "POST /pay", P95Ms: 842, ErrorRate: 6.5},
	}
	got := ProblemPromptUser(ev, "")
	for _, want := range []string{
		"checkout", "error_rate", "12.5", "eşik 5", `ihlal yönü ">"`,
		"kritik", "P1 (kritik + deploy)", "açık, 2sa önce açıldı",
		"v2.1.0", "p99 +34%", "hata oranı +2.4 puan",
		"12 çağıran servis", "3 tanesinin KENDİ açık problemi var",
		"web, bff", "POST /pay", "842ms",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("prompt %q taşımıyor:\n%s", want, got)
		}
	}
	// Uydurma kalkanı + eyleme dönük kapanış: kartın "sonra ne yapayım"
	// yarısı prose'dan gelir.
	if !strings.Contains(got, "UYDURMA") || !strings.Contains(got, "İLK adımı") {
		t.Errorf("prompt kalkan/kapanış cümlelerini taşımıyor:\n%s", got)
	}
}

// Hipotez bloğu VARSA çıplak aday satırı yazılmaz: aynı bilgiyi iki kez
// yazmak token yakar ve iki farklı ifadeyle çelişme riski açar.
func TestProblemPromptUserDoesNotDuplicateHypothesis(t *testing.T) {
	ev := ProblemEvidence{ID: "p", Service: "s",
		Hyp: &HypothesisRef{TopSuspect: "redis", Confidence: 0.9}}

	withBlock := ProblemPromptUser(ev, "KÖK-NEDEN HİPOTEZİ (deterministik): redis")
	if strings.Contains(withBlock, "KÖK-NEDEN ADAYI (korelasyon motoru)") {
		t.Errorf("hipotez bloğu varken çıplak aday satırı da yazıldı:\n%s", withBlock)
	}
	if !strings.Contains(withBlock, "KÖK-NEDEN HİPOTEZİ (deterministik): redis") {
		t.Error("hipotez bloğu aynen taşınmadı")
	}

	withoutBlock := ProblemPromptUser(ev, "  ")
	if !strings.Contains(withoutBlock, "KÖK-NEDEN ADAYI (korelasyon motoru): redis, güven %90") {
		t.Errorf("blok yokken aday satırı yazılmadı:\n%s", withoutBlock)
	}
}

func TestProblemPromptUserSparseDoesNotPanic(t *testing.T) {
	got := ProblemPromptUser(ProblemEvidence{}, "")
	if !strings.Contains(got, "PROBLEM:") || !strings.Contains(got, "—") {
		t.Errorf("boş kanıtta prompt bozuldu:\n%s", got)
	}
}

// ════════════════════════════════════════════════════════════════════
// v0.9.1137 (Faz 2.4) — log-pattern + slow-query kullanıcı prompt'ları.
// ════════════════════════════════════════════════════════════════════

func TestLogPatternPromptUserCarriesEvidence(t *testing.T) {
	now := int64(1_700_000_000) * 1e9
	ev := LogPatternEvidence{
		Pattern: "Out of memory", Kind: "spike",
		CurrentCount: 1240, BaselineCount: 320, Ratio: 3.875,
		Service: "checkout", WindowSec: 300,
		TopServices: []PatternServiceRef{
			{Service: "checkout", Count: 900}, {Service: "web", Count: 340},
		},
		Sample:     "java.lang.OutOfMemoryError: Java heap space",
		LastSeenNs: now - 120*1e9, NowNs: now,
	}
	got := LogPatternPromptUser(ev)
	for _, want := range []string{
		"Out of memory", "son 5dk", "PATLAMA", "3.88 katı",
		"şimdi 1.240", "taban 320", "en çok basan servis: checkout",
		"checkout (900), web (340)", "2dk önce",
		"java.lang.OutOfMemoryError",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("prompt %q taşımıyor:\n%s", want, got)
		}
	}
	// KAPSAM BEYANI — sistem prompt'u PANEL paketini bekliyor (bölüm
	// başlıkları: Diğer Değişenler / Sürekli Gürültü). Kart paketinde o
	// bölümlerin kanıtı YOK; kapsam söylenmezse model onları uydurur.
	if !strings.Contains(got, "TEK bir log desenini kapsıyor") {
		t.Errorf("kapsam beyanı yok — model olmayan bölümleri doldurabilir:\n%s", got)
	}
	// OLMAYAN KIRILIM: severity karışımı yok ve bu AÇIKÇA söyleniyor.
	if !strings.Contains(got, "Severity (log seviyesi) kırılımı bu pakette YOK") {
		t.Errorf("severity kalkanı yok:\n%s", got)
	}
}

// "new" dalı oran İDDİA ETMEZ: tabansız bir desende "3 kat arttı"
// cümlesi uydurma olur (detektörün kendi ayrımı — qualifyLogPattern).
func TestLogPatternPromptUserNewBranchClaimsNoRatio(t *testing.T) {
	got := LogPatternPromptUser(LogPatternEvidence{
		Pattern: "Disk full", Kind: "new", CurrentCount: 42, Service: "worker",
		WindowSec: 60, Ratio: 42})
	if !strings.Contains(got, "YENİ") || !strings.Contains(got, "taban YOK") {
		t.Errorf("yeni dalı taban yokluğunu söylemiyor:\n%s", got)
	}
	if strings.Contains(got, "katı") {
		t.Errorf("tabansız desende oran iddiası yazıldı:\n%s", got)
	}
	if strings.Contains(got, "taban 42") {
		t.Errorf("uydurma taban yazıldı:\n%s", got)
	}
}

func TestLogPatternPromptUserSparseDoesNotPanic(t *testing.T) {
	got := LogPatternPromptUser(LogPatternEvidence{})
	if !strings.Contains(got, "DESEN: —") {
		t.Errorf("boş kanıtta prompt bozuldu:\n%s", got)
	}
}

func TestSlowQueryPromptUserCarriesEvidence(t *testing.T) {
	now := int64(1_700_000_000) * 1e9
	ev := SlowQueryEvidence{
		StmtParam: "12345|oracle",
		Statement: "SELECT * FROM ACCOUNTS WHERE ID = ?",
		Sample:    "SELECT * FROM ACCOUNTS WHERE ID = 42",
		DBSystem:  "oracle", DBName: "COREBANK",
		Calls: 12345, Errors: 82,
		TotalMs: 41_200, AvgMs: 3.3, P95Ms: 842, P99Ms: 1240, MaxMs: 3100,
		Callers: []CallerRef{
			{Service: "payments-api", Calls: 8100, P95Ms: 902, TotalMs: 30_000},
			{Service: "web", Calls: 4245, P95Ms: 400, TotalMs: 11_200},
		},
		FromNs: now - 3600*1e9, ToNs: now, NowNs: now,
	}
	got := SlowQueryPromptUser(ev)
	// Sistem prompt'u (SystemPromptSlowQuery) bu etiketleri SAYIYOR:
	// normalize ifade + literalli örnek + motor + toplam istatistikler.
	for _, want := range []string{
		"DB engine: oracle", "Database: COREBANK", "Window: 1sa",
		"Calls in window: 12.345", "Errors: 82 (0.7%)",
		"p95=842ms", "p99=1.2sn", "max=3.1sn",
		"Total wall-clock time spent in this query class: 41.2sn",
		"payments-api — 8.100 calls", "web — 4.245 calls",
		"Normalized statement (literals replaced with ?):",
		"SELECT * FROM ACCOUNTS WHERE ID = ?",
		"Real sample with literals:", "WHERE ID = 42",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("prompt %q taşımıyor:\n%s", want, got)
		}
	}
	// db_name katlanıyor — prompt bunu SÖYLÜYOR, yoksa model "COREBANK'ta"
	// diye tekil bir iddiada bulunur.
	if !strings.Contains(got, "folds db.name") {
		t.Errorf("db.name katlanma notu yok:\n%s", got)
	}
	if !strings.Contains(got, "Don't invent") {
		t.Errorf("uydurma kalkanı yok:\n%s", got)
	}
}

// Aynı metin İKİ kez yazılmaz: normalize form ile örnek aynıysa
// "Real sample" bloğu atlanır (token + çelişki riski).
func TestSlowQueryPromptUserSkipsDuplicateSample(t *testing.T) {
	got := SlowQueryPromptUser(SlowQueryEvidence{
		Statement: "SELECT 1", Sample: "SELECT 1", DBSystem: "mysql", Calls: 1})
	if strings.Contains(got, "Real sample with literals:") {
		t.Errorf("aynı metin iki kez yazıldı:\n%s", got)
	}
}

// 'default' MV nöbetçisi bir veritabanı ADI gibi geçmez.
func TestSlowQueryPromptUserHidesDefaultSentinel(t *testing.T) {
	got := SlowQueryPromptUser(SlowQueryEvidence{
		Statement: "SELECT 1", DBSystem: "postgresql", DBName: "default", Calls: 1})
	if strings.Contains(got, "Database: default") {
		t.Errorf("nöbetçi ad prompt'a girdi:\n%s", got)
	}
}

// capRunes — RUNE sınırında keser: bayt kesmesi geçersiz UTF-8 üretir ve
// bozuk baytlar modele gider (mevcut handler bayt kesiyor).
func TestSlowQueryPromptUserCapsStatementOnRuneBoundary(t *testing.T) {
	long := strings.Repeat("ı", 5000)
	got := SlowQueryPromptUser(SlowQueryEvidence{
		Statement: long, DBSystem: "oracle", Calls: 1})
	if !utf8.ValidString(got) {
		t.Error("prompt geçersiz UTF-8 taşıyor (bayt kesmesi)")
	}
	if !strings.Contains(got, "[truncated]") {
		t.Error("kesme İTİRAF EDİLMEDİ — model tam bir ifade gördüğünü sanar")
	}
	if strings.Count(got, "ı") > 4001 {
		t.Errorf("tavan uygulanmadı: %d rune", strings.Count(got, "ı"))
	}
}
