package chstore

import (
	"strings"
	"testing"
	"time"
)

// endpoints_series_test.go — v0.9.819.
//
// Sabitlenen sözleşmeler:
//   · WITH ROLLUP satırı (t=0) bir KOVA DEĞİL, pencere toplamıdır;
//     noktalara sızarsa grafik epoch'ta sahte bir nokta çizer.
//   · p50/p95 ASLA kova değerlerinden türetilmez (quantile toplanmaz).
//   · Son kova iki ayrı sebeple kısmi olabilir (to'yu aşmak / henüz
//     kapanmamak) ve ikisi de işaretlenmeli.
//   · env/cluster kapsamı CH'ye HİÇ gitmeden ilan edilir.
//   · Entry yüklemi tablonun (GetEndpointsMV) yüklemiyle BİREBİR aynı.

func TestSeriesLastBucketPartial(t *testing.T) {
	const bucket = int64(60)
	cases := []struct {
		name               string
		lastStart, to, now int64
		want               bool
	}{
		{"kapanmış kova, pencere içinde", 1000, 2000, 2000, false},
		{"sonu tam to'ya oturuyor → TAM", 1940, 2000, 3000, false},
		{"sonu tam now'a oturuyor → TAM", 1940, 5000, 2000, false},
		{"kova to'yu aşıyor → kısmi", 1980, 2000, 5000, true},
		{"kova henüz kapanmadı → kısmi", 1980, 5000, 2000, true},
		{"ikisi birden", 1990, 2000, 2000, true},
		{"bucketSec 0 → asla kısmi (sıfır bölmesi yok)", 1990, 1000, 1000, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			b := bucket
			if strings.Contains(c.name, "bucketSec 0") {
				b = 0
			}
			if got := seriesLastBucketPartial(c.lastStart, b, c.to, c.now); got != c.want {
				t.Fatalf("seriesLastBucketPartial(%d,%d,%d,%d) = %v, want %v",
					c.lastStart, b, c.to, c.now, got, c.want)
			}
		})
	}
}

func TestBuildEndpointsSeries_RollupIsNotAPoint(t *testing.T) {
	rows := []endpointsSeriesRow{
		// ROLLUP satırı ÖNCE gelir (ORDER BY t, t=0 en küçük).
		{t: 0, calls: 300, errors: 30, p50Ms: 40, p95Ms: 900},
		{t: 1000, calls: 100, errors: 10, p50Ms: 30, p95Ms: 500},
		{t: 1060, calls: 200, errors: 20, p50Ms: 50, p95Ms: 700},
	}
	got := buildEndpointsSeries(rows, 60, 120, 1000e9, 1120e9, "spanmetrics_1m", 5000)
	if len(got.Points) != 2 {
		t.Fatalf("Points = %d, want 2 (ROLLUP satırı nokta DEĞİL)", len(got.Points))
	}
	for _, p := range got.Points {
		if p.TimeS == 0 {
			t.Fatalf("epoch'ta nokta var — ROLLUP satırı seriye sızmış")
		}
	}
	if got.Calls != 300 || got.Errors != 30 {
		t.Fatalf("pencere sayaçları = %d/%d, want 300/30", got.Calls, got.Errors)
	}
	// p95 ROLLUP'tan: kova p95'lerinin ortalaması 600 OLURDU, en
	// büyüğü 700. İkisi de değil — gerçek merge 900.
	if got.P95Ms != 900 || got.P50Ms != 40 {
		t.Fatalf("pencere p50/p95 = %v/%v, want 40/900 (ROLLUP merge'ü)", got.P50Ms, got.P95Ms)
	}
	if got.ErrorRate != 10 {
		t.Fatalf("ErrorRate = %v, want 10", got.ErrorRate)
	}
	// 300 çağrı / 120 sn = 150/dk
	if got.CallsPerMin != 150 {
		t.Fatalf("CallsPerMin = %v, want 150", got.CallsPerMin)
	}
	if got.BucketSeconds != 60 || got.SourceMV != "spanmetrics_1m" {
		t.Fatalf("zarf = %d/%s", got.BucketSeconds, got.SourceMV)
	}
	if got.CoveredFromNs != 1000e9 || got.CoveredToNs != 1120e9 {
		t.Fatalf("kapsanan aralık = %d..%d", got.CoveredFromNs, got.CoveredToNs)
	}
}

func TestBuildEndpointsSeries_PerPointDerivations(t *testing.T) {
	rows := []endpointsSeriesRow{
		{t: 0, calls: 120, errors: 12},
		{t: 600, calls: 120, errors: 12, p50Ms: 5, p95Ms: 50},
		{t: 660, calls: 0, errors: 0}, // sessiz kova — sıfır bölmesi yok
	}
	got := buildEndpointsSeries(rows, 60, 120, 600e9, 720e9, "spanmetrics_1m", 10_000)
	if got.Points[0].Rps != 2 { // 120 / 60s
		t.Fatalf("Rps = %v, want 2", got.Points[0].Rps)
	}
	if got.Points[0].ErrorRate != 10 {
		t.Fatalf("nokta ErrorRate = %v, want 10", got.Points[0].ErrorRate)
	}
	if got.Points[1].ErrorRate != 0 || got.Points[1].Rps != 0 {
		t.Fatalf("sessiz kova NaN üretti: %+v", got.Points[1])
	}
}

func TestBuildEndpointsSeries_NoRollup(t *testing.T) {
	// ROLLUP satırı gelmezse (boş pencere ya da beklenmedik sürücü
	// davranışı) sayaçlar kovalardan toplanır AMA quantile TÜRETİLMEZ.
	rows := []endpointsSeriesRow{
		{t: 100, calls: 10, errors: 1, p50Ms: 7, p95Ms: 70},
		{t: 160, calls: 30, errors: 3, p50Ms: 9, p95Ms: 90},
	}
	got := buildEndpointsSeries(rows, 60, 120, 100e9, 220e9, "spanmetrics_10s", 10_000)
	if got.Calls != 40 || got.Errors != 4 {
		t.Fatalf("sayaçlar = %d/%d, want 40/4", got.Calls, got.Errors)
	}
	if got.P50Ms != 0 || got.P95Ms != 0 {
		t.Fatalf("quantile türetilmiş (%v/%v) — merge olmadan ASLA", got.P50Ms, got.P95Ms)
	}
}

func TestBuildEndpointsSeries_Empty(t *testing.T) {
	got := buildEndpointsSeries(nil, 60, 3600, 0, 0, "spanmetrics_1m", 10_000)
	if got.Points == nil {
		t.Fatalf("Points nil — JSON'da null olur, frontend .map() ile patlar")
	}
	if len(got.Points) != 0 || got.PartialLastBucket {
		t.Fatalf("boş seri kısmi işaretlendi: %+v", got)
	}
	if got.ErrorRate != 0 || got.CallsPerMin != 0 {
		t.Fatalf("boş pencerede sıfır bölmesi: %+v", got)
	}
}

func TestBuildEndpointsSeries_PartialFlag(t *testing.T) {
	// Son kova 1000..1060; to = 1120e9 ns (1120 s) → kova pencere içinde,
	// now = 5000 → kapanmış. Kısmi DEĞİL.
	full := buildEndpointsSeries(
		[]endpointsSeriesRow{{t: 1000, calls: 1}}, 60, 120, 940e9, 1120e9, "m", 5000)
	if full.PartialLastBucket {
		t.Fatalf("tam kova kısmi işaretlendi")
	}
	// Aynı kova, now = 1030 → henüz kapanmadı.
	live := buildEndpointsSeries(
		[]endpointsSeriesRow{{t: 1000, calls: 1}}, 60, 120, 940e9, 1120e9, "m", 1030)
	if !live.PartialLastBucket {
		t.Fatalf("dolmakta olan kova işaretlenmedi — grafik sahte düşüş çizer")
	}
}

func TestEndpointsSeriesUnsupportedScope(t *testing.T) {
	cases := []struct {
		env, cluster, want string
	}{
		{"", "", ""},
		{"uat", "", "env"},
		{"", "prod-eu", "cluster"},
		{"uat", "prod-eu", "env + cluster"},
	}
	for _, c := range cases {
		q := EndpointsSeriesQuery{Env: c.env, Cluster: c.cluster}
		if got := q.unsupportedScope(); got != c.want {
			t.Fatalf("unsupportedScope(env=%q,cluster=%q) = %q, want %q",
				c.env, c.cluster, got, c.want)
		}
	}
}

func TestGetEndpointsSeries_UnsupportedScopeSkipsClickHouse(t *testing.T) {
	// nil Store: CH'ye dokunulsaydı nil-pointer panic olurdu. Kapının
	// sorgudan ÖNCE olduğunun kanıtı bu.
	var s *Store
	got, err := s.GetEndpointsSeries(nil, EndpointsSeriesQuery{
		From: time.Unix(1000, 0), To: time.Unix(2000, 0), Env: "uat",
	})
	if err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
	if got.UnsupportedScope != "env" {
		t.Fatalf("UnsupportedScope = %q, want env", got.UnsupportedScope)
	}
	if len(got.Points) != 0 {
		t.Fatalf("cevaplanamaz kapsamda nokta döndü")
	}
}

func TestEndpointsSeriesEntrySQLMatchesTable(t *testing.T) {
	// Grafik ile tablo AYNI popülasyonu okumak ZORUNDA. Tablonun
	// yüklemi GetEndpointsMV içinde satır içi yazılı; buradaki metin
	// ondan ayrışırsa grafik başka bir kümeyi çizer.
	httpWhere, httpDim := endpointsSeriesEntrySQL(EntryHTTP)
	if httpWhere != " AND kind NOT IN ('client', 'producer') AND http_route != ''" {
		t.Fatalf("HTTP entry yüklemi tablodan ayrıştı: %q", httpWhere)
	}
	if httpDim != "http_route" {
		t.Fatalf("HTTP arama kolonu = %q, want http_route", httpDim)
	}
	rpcWhere, rpcDim := endpointsSeriesEntrySQL(EntryRPC)
	if rpcWhere != " AND kind IN ('server', 'consumer') AND http_route = ''" {
		t.Fatalf("RPC entry yüklemi tablodan ayrıştı: %q", rpcWhere)
	}
	// v0.9.324 dersi: RPC sekmesinde arama http_route'ta yapılamaz
	// (o kolon tanım gereği boş) — dim `name` olmak ZORUNDA.
	if rpcDim != "name" {
		t.Fatalf("RPC arama kolonu = %q, want name (v0.9.324)", rpcDim)
	}
	// Boş EntryKind = HTTP varsayılanı (mevcut çağıranlar bozulmasın).
	if w, _ := endpointsSeriesEntrySQL(""); w != httpWhere {
		t.Fatalf("boş entry HTTP'ye düşmedi")
	}
}

func TestEndpointsSeriesFilterSQL(t *testing.T) {
	t.Run("filtresiz", func(t *testing.T) {
		sql, args := endpointsSeriesFilterSQL("", "", "http_route")
		if sql != "" || len(args) != 0 {
			t.Fatalf("boş filtre yüklem üretti: %q %v", sql, args)
		}
	})
	t.Run("arg sırası SQL sırasıyla aynı", func(t *testing.T) {
		sql, args := endpointsSeriesFilterSQL("checkout", "/orders", "http_route")
		if strings.Index(sql, "service_name") > strings.Index(sql, "positionCaseInsensitive") {
			t.Fatalf("service yüklemi aramadan sonra geliyor: %q", sql)
		}
		if len(args) != 2 || args[0] != "checkout" || args[1] != "/orders" {
			t.Fatalf("args = %v", args)
		}
	})
	t.Run("ILIKE değil positionCaseInsensitive", func(t *testing.T) {
		sql, _ := endpointsSeriesFilterSQL("", "%_", "name")
		if strings.Contains(sql, "ILIKE") {
			t.Fatalf("ILIKE kullanılmış — operatörün %% karakteri desen anlamı kazanır: %q", sql)
		}
		if !strings.Contains(sql, "positionCaseInsensitive(name, ?)") {
			t.Fatalf("arama dim kolonunda değil: %q", sql)
		}
	})
	t.Run("uzun arama kırpılır (anahtar kardinalitesi)", func(t *testing.T) {
		long := strings.Repeat("x", endpointsSeriesSearchMax+50)
		_, args := endpointsSeriesFilterSQL("", long, "http_route")
		if got := args[0].(string); len(got) != endpointsSeriesSearchMax {
			t.Fatalf("arama kırpılmadı: %d", len(got))
		}
	})
}
