package chstore

import (
	"strings"
	"testing"
)

// databases_series_test.go — v0.9.820.
//
// Sabitlenen sözleşmeler:
//   · İKİ SEVİYELİ ROLLUP'ın üç satır sınıfı birbirine KARIŞMAZ:
//     (0,"")=pencere, (t,"")=kova filo toplamı, (t,sys)=motor kırılımı.
//   · p50/p95/p99 ASLA motor ya da kova değerlerinden türetilmez.
//   · Son kova dolmaktaysa işaretlenir.
//   · Motor tavanı aşıldığında sonraki motorlar sessizce EK SATIR
//     üretmez (frontend foldTopN'i katlıyor).

func TestBuildDatabasesSeries_RollupClasses(t *testing.T) {
	rows := []dbSeriesRow{
		// ORDER BY t, db_system → pencere toplamı EN BAŞTA.
		{t: 0, system: "", queries: 300, errors: 30, p50Ms: 12, p95Ms: 186, p99Ms: 900},
		{t: 1000, system: "", queries: 100, errors: 10, p50Ms: 10, p95Ms: 150, p99Ms: 700},
		{t: 1000, system: "oracle", queries: 70},
		{t: 1000, system: "redis", queries: 30},
		{t: 1300, system: "", queries: 200, errors: 20, p50Ms: 14, p95Ms: 200, p99Ms: 800},
		{t: 1300, system: "oracle", queries: 150},
		{t: 1300, system: "redis", queries: 50},
	}
	got := buildDatabasesSeries(rows, 600, 1000e9, 1600e9, 100_000)

	if len(got.Points) != 2 {
		t.Fatalf("Points = %d, want 2 (yalnız kova FİLO satırları)", len(got.Points))
	}
	for _, p := range got.Points {
		if p.TimeS == 0 {
			t.Fatalf("pencere toplamı noktalara sızmış")
		}
	}
	if len(got.Engines) != 2 {
		t.Fatalf("Engines = %d, want 2", len(got.Engines))
	}
	// Motor sırası ilk görüldükleri sıra (ORDER BY db_system → alfabetik).
	if got.Engines[0].System != "oracle" || got.Engines[1].System != "redis" {
		t.Fatalf("motorlar = %q/%q", got.Engines[0].System, got.Engines[1].System)
	}
	if len(got.Engines[0].Points) != 2 {
		t.Fatalf("oracle noktaları = %d, want 2", len(got.Engines[0].Points))
	}
	// Motor toplamı filo toplamına eşit — kırılım kaybetmiyor.
	var oraSum, redSum uint64
	for _, p := range got.Engines[0].Points {
		oraSum += p.Queries
	}
	for _, p := range got.Engines[1].Points {
		redSum += p.Queries
	}
	if oraSum+redSum != 300 {
		t.Fatalf("motor toplamı %d, filo 300", oraSum+redSum)
	}

	// Pencere KPI'ları ROLLUP'tan: kova p95'lerinin ortalaması 175
	// OLURDU, maksimumu 200. İkisi de değil — gerçek merge 186.
	if got.P95Ms != 186 || got.P50Ms != 12 || got.P99Ms != 900 {
		t.Fatalf("pencere quantile = %v/%v/%v, want 12/186/900",
			got.P50Ms, got.P95Ms, got.P99Ms)
	}
	if got.Queries != 300 || got.Errors != 30 || got.ErrorRate != 10 {
		t.Fatalf("pencere sayaçları = %d/%d/%v", got.Queries, got.Errors, got.ErrorRate)
	}
	// 300 sorgu / 600 sn = 30/dk
	if got.QueriesPerMin != 30 {
		t.Fatalf("QueriesPerMin = %v, want 30", got.QueriesPerMin)
	}
	// Kova oranı 5 dk'ya bölünür: 100/5 = 20
	if got.Points[0].QueriesPerMin != 20 {
		t.Fatalf("kova QueriesPerMin = %v, want 20", got.Points[0].QueriesPerMin)
	}
	if got.Points[0].ErrorRate != 10 {
		t.Fatalf("kova ErrorRate = %v, want 10", got.Points[0].ErrorRate)
	}
	if got.BucketSeconds != dbSeriesBucketSeconds {
		t.Fatalf("BucketSeconds = %d", got.BucketSeconds)
	}
}

func TestBuildDatabasesSeries_EngineCap(t *testing.T) {
	rows := []dbSeriesRow{{t: 0, system: "", queries: 1}}
	for i := 0; i < dbSeriesMaxEngines+5; i++ {
		rows = append(rows, dbSeriesRow{t: 1000, system: "eng" + string(rune('a'+i)), queries: 1})
	}
	got := buildDatabasesSeries(rows, 300, 1000e9, 1300e9, 100_000)
	if len(got.Engines) != dbSeriesMaxEngines {
		t.Fatalf("Engines = %d, want %d (tavan)", len(got.Engines), dbSeriesMaxEngines)
	}
}

func TestBuildDatabasesSeries_Empty(t *testing.T) {
	got := buildDatabasesSeries(nil, 3600, 0, 0, 100_000)
	if got.Points == nil || got.Engines == nil {
		t.Fatalf("nil dilim — JSON'da null olur, frontend .map() ile patlar")
	}
	if got.ErrorRate != 0 || got.QueriesPerMin != 0 || got.PartialLastBucket {
		t.Fatalf("boş pencerede sıfır bölmesi / sahte bayrak: %+v", got)
	}
}

func TestBuildDatabasesSeries_SilentBucket(t *testing.T) {
	// Trafiği olmayan bir kova NaN üretmemeli.
	got := buildDatabasesSeries([]dbSeriesRow{
		{t: 0, system: "", queries: 0, errors: 0},
		{t: 1000, system: "", queries: 0, errors: 0},
	}, 300, 1000e9, 1300e9, 100_000)
	if got.Points[0].ErrorRate != 0 || got.ErrorRate != 0 {
		t.Fatalf("sessiz kova NaN üretti: %+v", got)
	}
}

func TestBuildDatabasesSeries_PartialFlag(t *testing.T) {
	rows := []dbSeriesRow{{t: 0, system: "", queries: 1}, {t: 1000, system: "", queries: 1}}
	// coveredTo = 1600 sn, now = 100000 → kova (1000..1300) TAM.
	if buildDatabasesSeries(rows, 600, 700e9, 1600e9, 100_000).PartialLastBucket {
		t.Fatalf("tam kova kısmi işaretlendi")
	}
	// now = 1100 → kova hâlâ doluyor.
	if !buildDatabasesSeries(rows, 600, 700e9, 1600e9, 1100).PartialLastBucket {
		t.Fatalf("dolmakta olan kova işaretlenmedi — grafik sahte düşüş çizer")
	}
}

func TestDBSeriesFilterSQL(t *testing.T) {
	t.Run("filtresiz", func(t *testing.T) {
		sql, args := dbSeriesFilterSQL("", "")
		if sql != "" || len(args) != 0 {
			t.Fatalf("boş filtre yüklem üretti: %q %v", sql, args)
		}
	})
	t.Run("arg sırası SQL sırasıyla aynı", func(t *testing.T) {
		sql, args := dbSeriesFilterSQL("oracle", "COREBANK")
		if strings.Index(sql, "db_system") > strings.Index(sql, "db_name") {
			t.Fatalf("db_system yüklemi db_name'den sonra: %q", sql)
		}
		if len(args) != 2 || args[0] != "oracle" || args[1] != "COREBANK" {
			t.Fatalf("args = %v", args)
		}
	})
	t.Run("yalnız db_name", func(t *testing.T) {
		sql, args := dbSeriesFilterSQL("", "COREBANK")
		if !strings.Contains(sql, "db_name = ?") || strings.Contains(sql, "db_system") {
			t.Fatalf("sql = %q", sql)
		}
		if len(args) != 1 {
			t.Fatalf("args = %v", args)
		}
	})
}

// dbTrendCurBucket — sağlık rozetinin kaynağı SON TAM kova olmalı.
func TestDBTrendPickCurrent(t *testing.T) {
	const bucket = int64(dbSeriesBucketSeconds)
	pts := []DBTrendPoint{
		{T: 1000 * 1e9, Rps: 1, P99Ms: 10},
		{T: 1300 * 1e9, Rps: 2, P99Ms: 20},
		{T: 1600 * 1e9, Rps: 0.1, P99Ms: 3}, // DOLMAKTA olan kova
	}
	t.Run("dolmakta olan kova atlanır", func(t *testing.T) {
		i, partial := dbTrendCurrentIdx(pts, bucket, 1700)
		if partial {
			t.Fatalf("tam kova varken partial bayrağı açıldı")
		}
		if i != 1 {
			t.Fatalf("idx = %d, want 1 (son TAM kova)", i)
		}
	})
	t.Run("hiç tam kova yoksa sona düşer ve İLAN eder", func(t *testing.T) {
		i, partial := dbTrendCurrentIdx(pts[:1], bucket, 1100)
		if i != 0 || !partial {
			t.Fatalf("idx=%d partial=%v, want 0/true", i, partial)
		}
	})
	t.Run("boş seri", func(t *testing.T) {
		i, partial := dbTrendCurrentIdx(nil, bucket, 1100)
		if i != -1 || partial {
			t.Fatalf("idx=%d partial=%v, want -1/false", i, partial)
		}
	})
	t.Run("hepsi tamsa son nokta", func(t *testing.T) {
		i, partial := dbTrendCurrentIdx(pts, bucket, 999_999)
		if i != 2 || partial {
			t.Fatalf("idx=%d partial=%v, want 2/false", i, partial)
		}
	})
}
