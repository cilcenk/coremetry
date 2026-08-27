package devops

import (
	"context"
	"strings"
	"testing"

	"github.com/cilcenk/coremetry/internal/stackparse"
)

// frame_budget_test.go — v0.10.71. TAVAN NEYE HARCANMALI.
//
// Operatör teşhisi: "'deneme tavanı (6) doldu — 4 frame denenmedi'
// uyarısı bu işin asıl darboğazı. Tavanı doğru frame'lere harcamak,
// tavanı YÜKSELTMEKTEN daha çok işe yarar."
//
// ⚠ İLK DENEMEM YANLIŞ KATMANDAYDI. "Iskalayan paketi öğren, aynı
// paketin kalanını atla" yazdım; deponun KENDİ testi
// (TestFetchCodeWalksPastMissesToLaterFrame) onu anında kırdı ve haklıydı:
// aynı depoda aynı paket hem bulunan hem bulunmayan dosyalar taşıyor
// (stack'te sınıf adı ile dosya adı ayrışabiliyor), yani paket öneki
// "başka bileşen" demek DEĞİL.
//
// Doğru katman maliyetti: TAM bir ağaçta `find` yalnız BestPathForFrame —
// yerel arama, ağ YOK. Bedava bir adıma tavan harcamak, stack birden çok
// bileşene yayıldığında (paylaşılan core deposunun sınıfları bu depoda
// asla yok) asıl iş sınıflarına hiç ulaşamamak demekti.

func budgetFrame(class, file string, line int) stackparse.Frame {
	return stackparse.Frame{Class: class, File: file, Line: line, Method: "m"}
}

// TestMissesDoNotBurnTheBudget — OPERATÖRÜN BİLDİRDİĞİ DURUM.
//
// Paylaşılan core deposundan altı frame (bu depoda yok) + asıl iş sınıfı.
// Eski maliyet modelinde tavan altı ıskada tükeniyor ve iş sınıfı HİÇ
// denenmiyordu.
func TestMissesDoNotBurnTheBudget(t *testing.T) {
	targets := []stackparse.Frame{
		budgetFrame("com.acme.core.rest.RestFilter", "RestFilter.java", 10),
		budgetFrame("com.acme.core.rest.BasicDispatcher", "BasicDispatcher.java", 20),
		budgetFrame("com.acme.core.rest.RestBackendExecutor", "RestBackendExecutor.java", 30),
		budgetFrame("com.acme.core.tx.TxManager", "TxManager.java", 40),
		budgetFrame("com.acme.core.aop.Interceptor", "Interceptor.java", 50),
		budgetFrame("com.acme.core.io.Reader", "Reader.java", 60),
		budgetFrame("com.acme.billing.CardService", "CardService.java", 29), // ASIL HEDEF
	}
	fetches := 0
	find := func(f stackparse.Frame) string {
		if strings.HasPrefix(f.Class, "com.acme.billing.") {
			return "/src/CardService.java"
		}
		return "" // core deposu — bu depoda yok, ve bu arama BEDAVA
	}
	fetch := func(context.Context, string) (string, error) {
		fetches++
		return javaFile("com.acme.billing", "CardService", 200, 29), nil
	}

	out := huntWindows(context.Background(), targets,
		huntLimits{windows: 4, lookups: 6, radius: 5}, find, fetch)

	if len(out.windows) != 1 {
		t.Fatalf("asıl iş sınıfı bulunamadı (pencere=%d) — tavan ıskalarda "+
			"tükenmiş olabilir", len(out.windows))
	}
	if fetches != 1 {
		t.Errorf("çekim=%d, 1 bekleniyordu — ıska çekim doğurmamalı", fetches)
	}
	if out.patience {
		t.Error("sabır ısırdı: ıskalar hâlâ tavandan düşüyor")
	}
	if len(out.misses) != 6 {
		t.Errorf("ıska=%d, 6 bekleniyordu — hepsi TARANMALIYDI", len(out.misses))
	}
}

// TestFetchBudgetStillBites — TAVAN KALDIRILMADI, YERİ DEĞİŞTİ.
//
// Pahalı iş (dosya çekimi) hâlâ sınırlı: yedi bulunabilir frame'de altı
// çekimden sonra durulur. Düzeltmenin kendi üreteceği gerileme burada
// olurdu.
func TestFetchBudgetStillBites(t *testing.T) {
	var targets []stackparse.Frame
	for _, n := range []string{"A", "B", "C", "D", "E", "F", "G"} {
		targets = append(targets, budgetFrame("com.acme.app."+n, n+".java", 10))
	}
	fetches := 0
	find := func(f stackparse.Frame) string { return "/src/" + f.File }
	fetch := func(context.Context, string) (string, error) {
		fetches++
		return javaFile("com.acme.app", "X", 200, 10), nil
	}
	out := huntWindows(context.Background(), targets,
		huntLimits{windows: 10, lookups: 6, radius: 5}, find, fetch)

	if fetches != 6 {
		t.Errorf("çekim=%d, tavan 6 olmalıydı — pahalı iş sınırsız kaldı", fetches)
	}
	if !out.patience {
		t.Error("çekim tavanı dolduğu hâlde sabır bildirilmedi")
	}
}

// ── v0.10.112 — TAVAN DOĞRU FRAME'LERE, DOĞRU SAYIDA ──────────────────

// TestCachedBodyDoesNotBurnTheBudget — aynı dosyanın farklı satırları
// (filter zinciri, dispatcher döngüsü) tek GET ile kesilir ve tavandan
// DÜŞMEZ. Eski hâlde lookups++ cache kontrolünden önce artıyor, altı
// "RestFilter.java" satırı GET atmadan tavanı yiyordu.
func TestCachedBodyDoesNotBurnTheBudget(t *testing.T) {
	var targets []stackparse.Frame
	for _, ln := range []int{10, 20, 30, 40, 50, 60} {
		targets = append(targets, budgetFrame("com.acme.core.rest.RestFilter", "RestFilter.java", ln))
	}
	targets = append(targets, budgetFrame("com.acme.billing.CardService", "CardService.java", 29))
	fetches := 0
	find := func(f stackparse.Frame) string { return "/src/" + f.File }
	fetch := func(_ context.Context, p string) (string, error) {
		fetches++
		return javaFile("com.acme", strings.TrimSuffix(p, ".java"), 200, 29), nil
	}
	out := huntWindows(context.Background(), targets,
		huntLimits{windows: 10, lookups: 3, radius: 5}, find, fetch)
	if fetches != 2 {
		t.Errorf("çekim=%d, 2 bekleniyordu (RestFilter bir kez, CardService bir kez)", fetches)
	}
	if out.patience {
		t.Error("sabır ısırdı: cache isabeti tavandan düşüyor")
	}
	if len(out.windows) != 7 {
		t.Errorf("pencere=%d, 7 bekleniyordu", len(out.windows))
	}
	if out.lookups != 2 || out.lookupCap != 3 {
		t.Errorf("lookups=%d cap=%d — sayaçlar gözlemlenebilirlik için yanlış", out.lookups, out.lookupCap)
	}
}

// TestPatienceNoteUsesEffectiveCap — not metni SABİTİ değil, döngünün
// koştuğu tavanı basar; operatör 12 yazıp "tavan (6) doldu" okumamalı.
func TestPatienceNoteUsesEffectiveCap(t *testing.T) {
	h := huntOutcome{patience: true, untried: 2, lookupCap: 12}
	if got := h.note(1, 5, ""); !strings.Contains(got, "deneme tavanı (12) doldu — 2 frame denenmedi") {
		t.Errorf("not: %q", got)
	}
	// cap bilinmiyorsa (elle kurulan outcome) varsayılan basılır.
	h.lookupCap = 0
	if got := h.note(1, 5, ""); !strings.Contains(got, "deneme tavanı (6) doldu") {
		t.Errorf("varsayılan not: %q", got)
	}
}

// TestDefaultLookupLimitSingleSource — iki sabit tek sayı: code.go'daki
// son çare ile Settings varsayılanı ayrışırsa not metni yalan söyler.
func TestDefaultLookupLimitSingleSource(t *testing.T) {
	if codeLookupLimit != DefaultCodeLookupLimit {
		t.Fatalf("codeLookupLimit=%d ≠ DefaultCodeLookupLimit=%d", codeLookupLimit, DefaultCodeLookupLimit)
	}
	for in, want := range map[int]int{0: 6, -1: 6, 1: 1, 12: 12, 30: 30, 31: 30, 999: 30} {
		if got := (Settings{CodeLookupLimit: in}).lookupLimit(); got != want {
			t.Errorf("lookupLimit(%d)=%d, istenen %d", in, got, want)
		}
	}
}

// TestFetchCodeHonoursAppPrefixesAndLookupLimit — AYAR GERÇEKTEN
// BAĞLI (feedback-tested-but-unreachable): sahte TFS'te kurum-içi
// çerçeve dosyaları iş sınıfının önünde; tavan 1 ile yalnız BİR çekim
// hakkı var. Önek ayarı olmadan çekilen RestFilter, önekle CardService.
func TestFetchCodeHonoursAppPrefixesAndLookupLimit(t *testing.T) {
	f := newFakeTFS(t)
	const core = "/src/main/java/com/acme/core/rest/RestFilter.java"
	const app = "/src/main/java/com/acme/billing/CardService.java"
	f.tree = []string{core, app}
	f.files[core] = javaFile("com.acme.core.rest", "RestFilter", 120, 10)
	f.files[app] = javaFile("com.acme.billing", "CardService", 120, 29)
	stack := "" +
		"java.lang.IllegalStateException: boom\n" +
		"\tat com.acme.core.rest.RestFilter.doFilter(RestFilter.java:10)\n" +
		"\tat com.acme.billing.CardService.charge(CardService.java:29)\n"
	frames := stackparse.ParseJava(stack)

	run := func(cfg Settings) CodeContext {
		svc := New()
		svc.Configure(cfg)
		return svc.FetchCode(context.Background(), "core-service", ProjectHint{}, frames, nil, nil)
	}
	base := f.settings()
	base.CodeLookupLimit = 1

	got := run(base)
	if len(got.Windows) != 1 || got.Windows[0].Path != core {
		t.Fatalf("öneksiz: stack sırası beklenir (RestFilter), pencereler=%+v", got.Windows)
	}
	if !strings.Contains(got.Reason, "deneme tavanı (1) doldu") {
		t.Errorf("öneksiz: yürürlükteki tavan (1) notta yok: %q", got.Reason)
	}

	base.AppPrefixes = []string{"com.acme.billing."}
	got = run(base)
	if len(got.Windows) != 1 || got.Windows[0].Path != app {
		t.Fatalf("önekli: iş sınıfı önce gelmeli (CardService), pencereler=%+v", got.Windows)
	}
}
