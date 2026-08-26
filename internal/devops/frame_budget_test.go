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
