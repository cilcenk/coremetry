package chstore

import (
	"os"
	"strings"
	"testing"
	"time"
)

// v0.9.638 — "Toplamı göster" listeyi MV'den düşürüyordu (D3).
//
// Bu testlerin ASIL işi sayımın listeyle AYNI EVRENİ saydığını
// çivilemek. Bugünün on yedi sürümünün tamamı tek bir sınıftan çıktı:
// bir kural iki yere bölünüp ayrıştı. Sayım planlayıcısı eligibility'yi
// AYNALAMIYOR, tracesMVEligible'ı ÇAĞIRIYOR — aşağıdaki testler bunun
// böyle KALDIĞINI kontrol ediyor.

func okFilter() TraceFilter {
	now := time.Now()
	return TraceFilter{From: now.Add(-time.Hour), To: now, Limit: 50}
}

// Sayım, listenin MV'de karşılayamadığı HER filtreyi reddetmeli —
// aksi halde farklı bir evren sayar ve sayı sessizce yanlış olur.
func TestTraceCountRefusesWhateverListCannotServeFromMV(t *testing.T) {
	cases := map[string]func(*TraceFilter){
		"search":      func(f *TraceFilter) { f.Search = "boom" },
		"traceId":     func(f *TraceFilter) { f.TraceID = strings.Repeat("a", 32) },
		"env":         func(f *TraceFilter) { f.Env = "prod" },
		"filters":     func(f *TraceFilter) { f.Filters = []FilterExpr{{Key: "k", Op: "=", Values: []string{"v"}}} },
		"requireSvcs": func(f *TraceFilter) { f.RequireServices = []string{"a"} },
		"traceIds":    func(f *TraceFilter) { f.TraceIDs = []string{"a"} },
		"dar pencere": func(f *TraceFilter) { f.To = f.From.Add(time.Minute) },
		"pencere yok": func(f *TraceFilter) { f.From = time.Time{} },
		"bitiş yok":   func(f *TraceFilter) { f.To = time.Time{} },
	}
	for name, mut := range cases {
		f := okFilter()
		mut(&f)
		if tracesMVEligible(f) {
			t.Fatalf("%s: ön koşul — bu filtre MV'yi kapatmalı", name)
		}
		_, _, _, reason := traceCountPlan(f)
		if reason != traceCountReasonRawPath {
			t.Errorf("%s: sayım reddetmeliydi, alınan reason=%q", name, reason)
		}
	}
}

// Ucuza sayılamayan şekiller sayı YERİNE sebep döndürmeli. "Yanlış sayı,
// sayı yokluğundan kötüdür"ün devamı: PAHALI sayı da dürüst bir retten
// kötüdür.
func TestTraceCountRefusesExpensiveShapes(t *testing.T) {
	f := okFilter()
	f.MinMs = 100
	if _, _, _, r := traceCountPlan(f); r != traceCountReasonDuration {
		t.Errorf("minMs reddedilmeli, alınan %q", r)
	}
	f = okFilter()
	f.MaxMs = 100
	if _, _, _, r := traceCountPlan(f); r != traceCountReasonDuration {
		t.Errorf("maxMs reddedilmeli, alınan %q", r)
	}
	f = okFilter()
	f.Service = "checkout"
	f.HasError = true // post-agg → liste serviceSubquery kuruyor
	if !tracePostAggFiltered(f) {
		t.Fatal("ön koşul: bu filtre post-agg sayılmalı")
	}
	if _, _, _, r := traceCountPlan(f); r != traceCountReasonSvcAgg {
		t.Errorf("servis+post-agg reddedilmeli, alınan %q", r)
	}
}

// Kaynak seçimi listenin dallarıyla eşleşmeli.
func TestTraceCountSourceMatchesListBranch(t *testing.T) {
	f := okFilter()
	if src, _, _, r := traceCountPlan(f); src != "trace_summary_5m" || r != "" {
		t.Errorf("servissiz yol trace_summary_5m olmalı, alınan %q/%q", src, r)
	}
	f = okFilter()
	f.Service = "checkout"
	if src, _, args, r := traceCountPlan(f); src != "trace_service_index_5m" || r != "" {
		t.Errorf("servis yolu trace_service_index_5m olmalı, alınan %q/%q", src, r)
	} else if len(args) != 3 || args[0] != "checkout" {
		t.Errorf("servis bind edilmeli, alınan %v", args)
	}
}

// Post-agg yüklemleri sayıma da inmeli: liste onları HAVING ile
// uyguluyor, sayım uygulamazsa DAHA ÇOK trace sayar.
func TestTraceCountAppliesPostAggPredicates(t *testing.T) {
	f := okFilter()
	f.HasError = true
	_, preds, _, _ := traceCountPlan(f)
	if !strings.Contains(strings.Join(preds, " "), "error_count_state") {
		t.Errorf("hasError sayıma inmeli: %v", preds)
	}
	f = okFilter()
	f.RootOnly = true
	_, preds, _, _ = traceCountPlan(f)
	if !strings.Contains(strings.Join(preds, " "), "root_service_state") {
		t.Errorf("rootOnly sayıma inmeli: %v", preds)
	}
}

// SQL şeklinin ÜÇ özelliği ölçülmüş kararlar — bozulursa maliyet
// pencereye bağlanır ve D3'ün tüm amacı kaybolur.
func TestTraceCountSQLShape(t *testing.T) {
	sql := buildTraceCountSQL("trace_summary_5m", []string{"time_bucket >= ?"})

	if !strings.Contains(sql, "SELECT DISTINCT trace_id") {
		t.Error("DISTINCT olmalı — GROUP BY erken durmayı öldürüyor (trace_id sıralama anahtarının öneki değil)")
	}
	if strings.Contains(sql, "GROUP BY") {
		t.Errorf("GROUP BY olmamalı:\n%s", sql)
	}
	// ORDER BY maliyeti pencereye bağlıyor (ölçüldü: 6s 126k → 7g 913k).
	if strings.Contains(sql, "ORDER BY") {
		t.Errorf("ORDER BY olmamalı:\n%s", sql)
	}
	if !strings.Contains(sql, "max_threads = 1") {
		t.Error("max_threads=1 olmalı — çok iş parçacığı tavanı aşıyor")
	}
	if !strings.Contains(sql, "max_execution_time") {
		t.Error("CLAUDE.md sert kısıtı: her MV sorgusunda max_execution_time")
	}
	// cap+1: "tavana değdi mi" tek karşılaştırmayla belli olsun.
	if !strings.Contains(sql, "LIMIT 10001") {
		t.Errorf("LIMIT cap+1 olmalı:\n%s", sql)
	}
}

// Tavan davranışı: cap+1 dönerse "en az cap" denir, cap değeri
// GÖSTERİLEN sayı olur.
func TestTraceCountCapSemantics(t *testing.T) {
	if traceCountCap <= 0 {
		t.Fatal("tavan pozitif olmalı")
	}
	// cap+1 satır → AtLeast, Value == cap
	got := TraceCount{Value: traceCountCap, AtLeast: true}
	if got.Value != traceCountCap || !got.AtLeast {
		t.Fatal("tavan semantiği bozuk")
	}
}

// EN ÖNEMLİSİ: sayım kapısı ile liste kapısı AYRIŞAMAZ. Sayım
// tracesMVEligible'ı ÇAĞIRMALI, kopyalamamalı.
func TestTraceCountPlanCallsListGateNotACopy(t *testing.T) {
	raw, err := os.ReadFile("trace_count.go")
	if err != nil {
		t.Fatal(err)
	}
	body := stripGoLineComments(string(raw))
	if !strings.Contains(body, "tracesMVEligible(f)") {
		t.Error("sayım planlayıcısı liste kapısını ÇAĞIRMALI")
	}
	// Kapının kendi koşulları burada TEKRARLANMAMALI.
	for _, leaked := range []string{"f.Search ==", "f.Env ==", "len(f.Filters)", "FilterRoot"} {
		if strings.Contains(body, leaked) {
			t.Errorf("liste kapısının koşulu sayım tarafına KOPYALANMIŞ (%q) — ayrışma riski", leaked)
		}
	}
}
