package anomaly

// external_cap_test.go — v0.10.587: dış hatta TİK BAŞINA açılış tavanı.
//
// Oracle audit'inin (2026-09-09, §6.5) en sert boşluğu: ExternalScanner her
// seri için apply çağırıyor, açılan Problem sayısına ÜST SINIR YOK. Yüksek
// kardinaliteli groupBy (operasyon × hata kodu × kanal) bir tikte yüzlerce
// Problem açabilir. Sözleşme:
//   - yeni açılış ≤ tavan; en güçlü z'ler önce, eşitlikte ruleID (deterministik)
//   - aşan seriler Problem AÇMAZ, Capped sayılır; tek bir ÖZET Problem açılır
//     (ext:<kaynak> öznesi, Value = aşan sayı)
//   - tavan refresh/touch/resolve'u KAPILAMAZ — açık Problem'ler yaşar
//   - aşım bitince özet Problem resolve olur

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/cilcenk/coremetry/internal/chstore"
)

func spikingSeries(n int, end time.Time, dwell int, level float64) []chstore.SpanMetricSeries {
	out := make([]chstore.SpanMetricSeries, 0, n)
	for i := 0; i < n; i++ {
		vals := append(baselineVals(30), repeat(level, dwell)...)
		out = append(out, extSeries(vals, end, fmt.Sprintf("OP%02d", i), "E1"))
	}
	return out
}

func TestExternalScan_OpenCapLimitsNewOpens(t *testing.T) {
	cfg := chstore.DefaultAnomalySensitivity()
	cfg.ExternalOpenCapPerTick = 3
	now := time.Date(2026, 9, 10, 10, 0, 0, 0, time.UTC)
	f := &fakeExtStore{cfg: cfg, series: spikingSeries(7, now, cfg.DwellBuckets, 60)}
	rep, err := newExtScanner(f, now).Scan(context.Background(), extTarget)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Opened != 3 || rep.Capped != 4 {
		t.Fatalf("3 açılmalı, 4 tavana takılmalı: %+v", rep)
	}
	var summary *chstore.Problem
	perSeries := 0
	for i := range f.upserts {
		p := f.upserts[i]
		if strings.HasPrefix(p.RuleID, "anomaly:ext-cap:") {
			summary = &p
		} else {
			perSeries++
		}
	}
	if perSeries != 3 {
		t.Fatalf("seri Problem'i 3 olmalı, %d", perSeries)
	}
	if summary == nil {
		t.Fatal("tavan aşıldı ama ÖZET Problem açılmadı — sel sessizce yutulmuş olur")
	}
	if summary.Value != 4 || summary.Service != "ext:ggfail" || summary.Kind != chstore.ProblemKindExternal || summary.Status != "open" {
		t.Fatalf("özet: Value=aşan sayı, özne ext:<kaynak>, kind=external: %+v", *summary)
	}
	if !strings.Contains(summary.Description, "4") {
		t.Fatalf("özet gerekçesi aşan sayıyı söylemeli: %q", summary.Description)
	}
}

// En güçlü z ÖNCE: tavan 1 iken 600'lük seri açılır, 60'lık takılır.
func TestExternalScan_OpenCapPrefersStrongestZ(t *testing.T) {
	cfg := chstore.DefaultAnomalySensitivity()
	cfg.ExternalOpenCapPerTick = 1
	now := time.Date(2026, 9, 10, 10, 0, 0, 0, time.UTC)
	weak := extSeries(append(baselineVals(30), repeat(60, cfg.DwellBuckets)...), now, "OPA", "E1")
	strong := extSeries(append(baselineVals(30), repeat(600, cfg.DwellBuckets)...), now, "OPB", "E1")
	f := &fakeExtStore{cfg: cfg, series: []chstore.SpanMetricSeries{weak, strong}}
	rep, err := newExtScanner(f, now).Scan(context.Background(), extTarget)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Opened != 1 || rep.Capped != 1 {
		t.Fatalf("%+v", rep)
	}
	for _, p := range f.upserts {
		if strings.HasPrefix(p.RuleID, "anomaly:ext-cap:") {
			continue
		}
		if p.Service != "ext:ggfail/OPB/E1" {
			t.Fatalf("en güçlü z (OPB) açılmalıydı, açılan: %s", p.Service)
		}
	}
}

// Tavan yalnız YENİ açılışı kapılar: açık Problem'ler refresh edilir, hiçbiri
// Capped sayılmaz — aksi hâlde bayat süpürücü onları "source silent" kapatırdı.
func TestExternalScan_OpenCapDoesNotGateRefresh(t *testing.T) {
	cfg := chstore.DefaultAnomalySensitivity()
	cfg.ExternalOpenCapPerTick = 1
	now := time.Date(2026, 9, 10, 10, 0, 0, 0, time.UTC)
	series := spikingSeries(4, now, cfg.DwellBuckets, 60)
	var open []chstore.Problem
	for _, sr := range series {
		subj := ExternalSubject("ggfail", sr.GroupKey)
		open = append(open, chstore.Problem{ID: "p-" + subj, RuleID: "anomaly:" + subj + ":ext:tfail_adet",
			Service: subj, Kind: chstore.ProblemKindExternal, Status: "open", StartedAt: now.Add(-time.Hour).UnixNano()})
	}
	f := &fakeExtStore{cfg: cfg, series: series, open: open}
	rep, err := newExtScanner(f, now).Scan(context.Background(), extTarget)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Refreshed != 4 || rep.Capped != 0 || rep.Opened != 0 {
		t.Fatalf("dördü de refresh, sıfır capped/opened: %+v", rep)
	}
}

// Aşım bitince özet Problem RESOLVE olur; kalıcı bir hayalet bırakılmaz.
func TestExternalScan_OpenCapSummaryResolvesWhenClear(t *testing.T) {
	cfg := chstore.DefaultAnomalySensitivity()
	cfg.ExternalOpenCapPerTick = 5
	now := time.Date(2026, 9, 10, 10, 0, 0, 0, time.UTC)
	summary := chstore.Problem{ID: "cap-1", RuleID: "anomaly:ext-cap:ext:ggfail:ext:tfail_adet",
		Service: "ext:ggfail", Kind: chstore.ProblemKindExternal, Status: "open", Value: 9, StartedAt: now.Add(-time.Hour).UnixNano()}
	// Sakin tek seri: aşım yok.
	f := &fakeExtStore{cfg: cfg, series: []chstore.SpanMetricSeries{extSeries(baselineVals(33), now, "OP1", "E1")}, open: []chstore.Problem{summary}}
	if _, err := newExtScanner(f, now).Scan(context.Background(), extTarget); err != nil {
		t.Fatal(err)
	}
	resolved := false
	for _, p := range f.upserts {
		if p.ID == "cap-1" && p.Status == "resolved" {
			resolved = true
		}
	}
	if !resolved {
		t.Fatal("aşım bittiği hâlde özet Problem resolve edilmedi")
	}
}
