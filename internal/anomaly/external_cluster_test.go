package anomaly

// external_cluster_test.go — v0.10.597: dış kaynakta KÜME Problem'i.
//
// Oracle audit'i (§6.5) + operatör: "aynı operasyonda birden fazla hata
// kodu aynı anda patlıyorsa merge — Problem seli üretme". 587 tavanla seli
// kesti ama aynı ilk boyutta (operasyon) patlayan N seri hâlâ N ayrı
// Problem'di. Sözleşme:
//   - groupBy'ın İLK boyutu aynı olan ≥ clusterMinMembers (3) TAZE açılış
//     → TEK küme Problem'i (özne ext:<kaynak>/<ilk-boyut>, kararlı ID),
//     üyelerin bireysel Problem'i AÇILMAZ (Clustered sayılır)
//   - < 3 üye → kümeleme yok, bireysel açılış
//   - tek boyutlu groupBy → kümeleme yok (her seri kendi anahtarı)
//   - kümeleme tavandan ÖNCE: üyeler tavan yuvası yemez
//   - üyeler sakinleşince küme Problem'i resolve olur

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/cilcenk/coremetry/internal/chstore"
)

// opSeries — aynı OP altında n farklı hata kodu.
func opSeries(op string, n int, end time.Time, dwell int, level float64) []chstore.SpanMetricSeries {
	out := make([]chstore.SpanMetricSeries, 0, n)
	for i := 0; i < n; i++ {
		vals := append(baselineVals(30), repeat(level, dwell)...)
		out = append(out, extSeries(vals, end, op, fmt.Sprintf("E%02d", i)))
	}
	return out
}

func TestExternalScan_ClustersSameFirstDimension(t *testing.T) {
	cfg := chstore.DefaultAnomalySensitivity()
	now := time.Date(2026, 9, 10, 14, 0, 0, 0, time.UTC)
	f := &fakeExtStore{cfg: cfg, series: opSeries("OP_PAY", 4, now, cfg.DwellBuckets, 60)}
	rep, err := newExtScanner(f, now).Scan(context.Background(), extTarget)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Opened != 0 || rep.Clustered != 4 {
		t.Fatalf("4 üye tek kümede, bireysel açılış 0: %+v", rep)
	}
	var cluster *chstore.Problem
	for i := range f.upserts {
		if strings.HasPrefix(f.upserts[i].RuleID, clusterRulePrefix) {
			cluster = &f.upserts[i]
		}
	}
	if cluster == nil || len(f.upserts) != 1 {
		t.Fatalf("tek küme Problem'i bekleniyordu: %+v", f.upserts)
	}
	if cluster.Service != "ext:REDACTED/OP_PAY" || cluster.Kind != chstore.ProblemKindExternal || cluster.Value != 4 {
		t.Fatalf("özne ext:<kaynak>/<ilk boyut>, kind external, Value=üye sayısı: %+v", *cluster)
	}
	if !strings.Contains(cluster.Description, "E00") || !strings.Contains(cluster.Description, "4") {
		t.Fatalf("gerekçe üyeleri ve sayıyı söylemeli: %q", cluster.Description)
	}
}

func TestExternalScan_NoClusterBelowMin(t *testing.T) {
	cfg := chstore.DefaultAnomalySensitivity()
	now := time.Date(2026, 9, 10, 14, 0, 0, 0, time.UTC)
	f := &fakeExtStore{cfg: cfg, series: opSeries("OP_PAY", 2, now, cfg.DwellBuckets, 60)}
	rep, err := newExtScanner(f, now).Scan(context.Background(), extTarget)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Opened != 2 || rep.Clustered != 0 {
		t.Fatalf("2 üye kümelenmez, ikisi de bireysel: %+v", rep)
	}
}

func TestExternalScan_SingleDimensionNeverClusters(t *testing.T) {
	cfg := chstore.DefaultAnomalySensitivity()
	now := time.Date(2026, 9, 10, 14, 0, 0, 0, time.UTC)
	var series []chstore.SpanMetricSeries
	for i := 0; i < 4; i++ {
		series = append(series, extSeries(append(baselineVals(30), repeat(60, cfg.DwellBuckets)...), now, fmt.Sprintf("OP%d", i)))
	}
	f := &fakeExtStore{cfg: cfg, series: series}
	target := extTarget
	target.GroupBy = []string{"REDACTED"}
	rep, err := newExtScanner(f, now).Scan(context.Background(), target)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Clustered != 0 || rep.Opened != 4 {
		t.Fatalf("tek boyutta kümeleme yok: %+v", rep)
	}
}

// Kümeleme TAVANDAN önce: 5 aynı-OP serisi tek kümeye, 2 farklı OP
// bireysel adaya; tavan 1 → biri açılır, biri tavana takılır.
func TestExternalScan_ClusterBeforeCap(t *testing.T) {
	cfg := chstore.DefaultAnomalySensitivity()
	cfg.ExternalOpenCapPerTick = 1
	now := time.Date(2026, 9, 10, 14, 0, 0, 0, time.UTC)
	// Üyelerin z'si bireysel adaylardan YÜKSEK (600 vs 60/30): üyeler tavan
	// yuvası yeseydi tek yuvayı bir üye alır, OP_A tavana takılır, hiçbir
	// bireysel Problem açılmazdı — mutasyon tam bunu ölçer.
	series := opSeries("OP_PAY", 5, now, cfg.DwellBuckets, 600)
	series = append(series, extSeries(append(baselineVals(30), repeat(60, cfg.DwellBuckets)...), now, "OP_A", "E1"))
	series = append(series, extSeries(append(baselineVals(30), repeat(30, cfg.DwellBuckets)...), now, "OP_B", "E1"))
	f := &fakeExtStore{cfg: cfg, series: series}
	rep, err := newExtScanner(f, now).Scan(context.Background(), extTarget)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Clustered != 5 || rep.Opened != 1 || rep.Capped != 1 {
		t.Fatalf("clustered=5 opened=1 capped=1 bekleniyordu: %+v", rep)
	}
}

func TestExternalScan_ClusterResolvesWhenMembersCalm(t *testing.T) {
	cfg := chstore.DefaultAnomalySensitivity()
	now := time.Date(2026, 9, 10, 14, 0, 0, 0, time.UTC)
	open := chstore.Problem{ID: clusterProblemID("ext:REDACTED/OP_PAY"), RuleID: clusterRulePrefix + "ext:REDACTED/OP_PAY",
		Service: "ext:REDACTED/OP_PAY", Kind: chstore.ProblemKindExternal, Status: "open", Value: 4, StartedAt: now.Add(-time.Hour).UnixNano()}
	// Üyeler sakin (taban), küme açık kalmış.
	f := &fakeExtStore{cfg: cfg, series: opSeries("OP_PAY", 4, now, 0, 5), open: []chstore.Problem{open}}
	if _, err := newExtScanner(f, now).Scan(context.Background(), extTarget); err != nil {
		t.Fatal(err)
	}
	resolved := false
	for _, p := range f.upserts {
		if p.ID == open.ID && p.Status == "resolved" {
			resolved = true
		}
	}
	if !resolved {
		t.Fatal("üyeler sakinken küme Problem'i resolve edilmeli")
	}
}
