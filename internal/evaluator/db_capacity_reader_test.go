package evaluator

// v0.10.366 — VM dilim 3b-1: the DB capacity sweep reads through
// chstore.CapacityReader, VictoriaMetrics when configured. Source pins
// keep the sweep from quietly re-binding to *chstore.Store (the
// "tested but unreachable" class: a VM reader that exists and is never
// called).

import (
	"strings"
	"testing"

	"github.com/cilcenk/coremetry/internal/vmetrics"
)

func TestCapacityReaderPrefersConfiguredVM(t *testing.T) {
	e := &Evaluator{}
	if _, isVM := e.capacityReader().(*vmetrics.Service); isVM {
		t.Fatal("no vmetrics → ClickHouse store must answer")
	}
	vm := vmetrics.New()
	e.vmetrics = vm
	if _, isVM := e.capacityReader().(*vmetrics.Service); isVM {
		t.Fatal("unconfigured vmetrics must not be chosen")
	}
	vm.Configure(vmetrics.Settings{Enabled: true, BaseURL: "http://vm.invalid:8428"})
	if rd, isVM := e.capacityReader().(*vmetrics.Service); !isVM || rd != vm {
		t.Fatal("configured vmetrics must answer the capacity sweep")
	}
}

func TestCapacitySweepReadsThroughTheReader(t *testing.T) {
	src := mustReadEvaluatorSource(t, "db_capacity.go")
	if strings.Contains(src, "st *chstore.Store)") {
		t.Fatal("capacityCheck.read must take chstore.CapacityReader, not *chstore.Store")
	}
	for _, want := range []string{
		"rd := e.capacityReader()",
		"rd.MetricExists(ctx, c.probe)",
		"c.read(ctx, rd)",
		"rd.UsageTrend(ctx, c.trendMetric, c.trendAttr, capacityEtaWindow)",
	} {
		if !strings.Contains(src, want) {
			t.Fatalf("db_capacity.go must contain %q", want)
		}
	}
	for _, stale := range []string{"e.store.MetricExists(", "e.store.UsageTrend(", "c.read(ctx, e.store)"} {
		if strings.Contains(src, stale) {
			t.Fatalf("capacity sweep still pinned to ClickHouse via %q", stale)
		}
	}
}
