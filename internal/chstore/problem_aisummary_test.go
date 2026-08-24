package chstore

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// v0.9.445 (prod hacim denetimi yan bulgusu) — UpsertProblemAISummary'nin
// kolon listesinde pod yoktu: AI özeti yazan her problem, bütün-satır
// replace'te pod bağlamını kaybediyordu (v0.9.403 pod kolonu eklendiğinde
// bu yazma yolu güncellenmemişti). Pin: problems tablosuna yazan HER
// explicit kolon listesi pod'u taşır ve Append argüman sayısı kolon
// sayısıyla eşleşir — bir sonraki kolon eklemesinde aynı sınıf tekrar
// yakalansın.
//
// v0.9.976 — pin KAYNAK TARAMASINDAN fonksiyona taşındı. İki yazma yolu
// artık tek bir problemInsertCols/problemInsertArgs çiftinden geçiyor
// (comparator kolonu eklenirken listeleri ikiye ayırmak, bu testin
// yakaladığı hata sınıfını üçe çıkarırdı). Kaynak taraması ikinci
// aşamada duruyor: elle yazılmış YENİ bir liste yeniden belirirse
// yakalanmalı.
//
// v0.9.1338 — bayrak TEK bool değil, problemCols struct'ı. Döngü DÖRT
// kombinasyonu da geziyor: kolon-probe'lu bir alan eklendiğinde "yalnız
// hepsi açıkken" test etmek, ertelenmiş-DDL boot'unun (hepsi kapalı)
// gövdesini muaf tutardı — bu dosyanın var olma sebebi tam o gövde.
func TestProblemWritePathsCarryPod(t *testing.T) {
	for _, c := range allProblemColCombos() {
		cols := problemInsertCols(c)
		if !strings.Contains(cols, "pod") {
			t.Errorf("problemInsertCols(%+v) pod'u atlıyor — bütün-satır replace "+
				"pod bağlamını siler:\n%s", c, cols)
		}
	}
}

// allProblemColCombos — probe'lu kolon bayraklarının TAM çarpımı.
// Yeni bir probe'lu kolon gelince BURASI genişler ve tüm pinler onu
// otomatik olarak her iki durumda dener.
func allProblemColCombos() []problemCols {
	var out []problemCols
	for _, cmp := range []bool{false, true} {
		for _, kind := range []bool{false, true} {
			out = append(out, problemCols{Comparator: cmp, Kind: kind})
		}
	}
	return out
}

// v0.9.448 — v0.5.254'ün "explicit liste ai kolonlarını dışlar,
// ReplacingMergeTree eski değere düşer" varsayımı yanlıştı: replace
// bütün-satırdır, dışlanan kolon DEFAULT ''e iner. Her refresh özeti
// siliyor, explainer boş görüp yeniden üretiyordu. Pin: problems'e
// yazan HER explicit kolon listesi ai_summary + ai_summary_at taşır.
func TestProblemWritePathsCarryAISummary(t *testing.T) {
	for _, c := range allProblemColCombos() {
		cols := problemInsertCols(c)
		if !strings.Contains(cols, "ai_summary") || !strings.Contains(cols, "ai_summary_at") {
			t.Errorf("problemInsertCols(%+v) ai kolonlarını atlıyor — bütün-satır "+
				"replace explainer çıktısını her refresh'te siler:\n%s", c, cols)
		}
	}
}

// TestProblemInsertsUseTheSharedColumnList — kaynak taraması (v0.9.976).
//
// Yukarıdaki iki pin yalnız PAYLAŞILAN listeyi doğruluyor; elle yazılmış
// yeni bir `INSERT INTO problems (…)` onların menzilinin dışında kalır ve
// v0.9.445/448 sınıfı geri döner. Bu test tam olarak onu yasaklıyor:
// problems'e yazan her ifade kolon listesini problemInsertCols'tan almalı.
func TestProblemInsertsUseTheSharedColumnList(t *testing.T) {
	b, err := os.ReadFile("problem.go")
	if err != nil {
		t.Fatal(err)
	}
	src := string(b)

	inserts := regexp.MustCompile(`INSERT INTO problems`).FindAllString(src, -1)
	if len(inserts) < 2 {
		t.Fatalf("problems'e yazan ≥2 ifade bekleniyordu, %d bulundu", len(inserts))
	}
	// Elle yazılmış liste = `INSERT INTO problems` hemen ardından "(" ile
	// başlayan bir kolon dizisi. Paylaşılan yol `("+problemInsertCols(` ile
	// devam eder.
	hand := regexp.MustCompile(`INSERT INTO problems\s*\n?\s*\(\s*[a-z_]+\s*,`).FindAllString(src, -1)
	if len(hand) > 0 {
		t.Errorf("elle yazılmış problems kolon listesi bulundu (%d) — "+
			"problemInsertCols kullanılmalı, yoksa pod/ai_summary/comparator "+
			"sınıfı geri döner:\n%v", len(hand), hand)
	}
	if n := strings.Count(src, "problemInsertCols("); n < 2 {
		t.Errorf("problemInsertCols yalnız %d yerde kullanılıyor — yazma "+
			"yollarından biri paylaşılan listeden kopmuş olabilir", n)
	}
}
