package api

// copilot_chat_test.go — CoSRE Faz-2 (MCP-backed structured chart cards).
// chatChartBlock turns the render_chart handler's VALIDATED output into
// the deterministic ```chart``` fence the server appends to the final
// answer — a gemma4-class model never formats chart JSON itself, and a
// hallucinated service never reaches the UI (handler emits ok:false,
// which must map to an empty block). Pure-function, table-driven.

import (
	"os"
	"strings"
	"testing"
	"time"
)

func TestChatChartBlock(t *testing.T) {
	cases := []struct {
		name     string
		in       string
		wantJSON string // exact JSON payload inside the fence; "" = no block
		wantKey  string
	}{
		{
			name:     "service-level spec",
			in:       `{"ok":true,"spec":{"service":"checkout","agg":"error_rate","rangeS":1800},"note":"x"}`,
			wantJSON: `{"title":"checkout · error_rate","service":"checkout","agg":"error_rate","rangeS":1800}`,
			wantKey:  "checkout\x00\x00error_rate\x00",
		},
		{
			name:     "operation-scoped spec titles by operation",
			in:       `{"ok":true,"spec":{"service":"checkout","operation":"GET /orders/:id","agg":"p99","rangeS":3600}}`,
			wantJSON: `{"title":"GET /orders/:id · p99","service":"checkout","operation":"GET /orders/:id","agg":"p99","rangeS":3600}`,
			wantKey:  "checkout\x00GET /orders/:id\x00p99\x00",
		},
		{
			name: "ok:false (unknown service) → no block",
			in:   `{"ok":false,"error":"service \"nope\" not found"}`,
		},
		{
			name: "malformed JSON → no block",
			in:   `not json at all`,
		},
		{
			name: "missing agg → no block",
			in:   `{"ok":true,"spec":{"service":"checkout","rangeS":1800}}`,
		},
		{
			name: "missing service → no block",
			in:   `{"ok":true,"spec":{"agg":"rate","rangeS":1800}}`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			block, key := chatChartBlock(tc.in)
			if tc.wantJSON == "" {
				if block != "" || key != "" {
					t.Fatalf("want no block, got block=%q key=%q", block, key)
				}
				return
			}
			// The fence must be EXACTLY what chartFence produces — the
			// frontend renderMessage splits on ```chart fences and
			// JSON.parses the payload verbatim.
			wantBlock := "\n```chart\n" + tc.wantJSON + "\n```\n"
			if block != wantBlock {
				t.Fatalf("block mismatch:\n got %q\nwant %q", block, wantBlock)
			}
			if key != tc.wantKey {
				t.Fatalf("key = %q, want %q", key, tc.wantKey)
			}
		})
	}

	// Dedup contract: same service+operation+agg with a DIFFERENT
	// window must produce the same key (the loop keeps the first).
	_, k1 := chatChartBlock(`{"ok":true,"spec":{"service":"s","agg":"p95","rangeS":1800}}`)
	_, k2 := chatChartBlock(`{"ok":true,"spec":{"service":"s","agg":"p95","rangeS":7200}}`)
	if k1 == "" || k1 != k2 {
		t.Fatalf("dedup keys differ across rangeS: %q vs %q", k1, k2)
	}
	// ...and a different agg must NOT collide.
	_, k3 := chatChartBlock(`{"ok":true,"spec":{"service":"s","agg":"p50","rangeS":1800}}`)
	if k3 == k1 {
		t.Fatal("p50 and p95 dedup keys collide")
	}

	// The emitted agg vocabulary is CosreChart's AGG_META key set —
	// spot-check every value survives the round trip.
	for _, agg := range []string{"rate", "error_rate", "p50", "p95", "p99"} {
		block, _ := chatChartBlock(`{"ok":true,"spec":{"service":"s","agg":"` + agg + `","rangeS":1800}}`)
		if !strings.Contains(block, `"agg":"`+agg+`"`) {
			t.Fatalf("agg %q not preserved in block %q", agg, block)
		}
	}
}

// v0.10.541 — tipli chart bloğu: fence ile aynı spec + MUTLAK pencere.
func TestChatChartSpecAbsoluteWindow(t *testing.T) {
	now := time.Date(2026, 9, 7, 12, 0, 0, 0, time.UTC)
	out := `{"ok":true,"spec":{"service":"api","operation":"GET /x","agg":"p95","rangeS":3600,"groupBy":""}}`
	spec, ok := chatChartSpec(out, now)
	if !ok || spec.Service != "api" || spec.Agg != "p95" || spec.RangeS != 3600 {
		t.Fatalf("spec: %+v %v", spec, ok)
	}
	if spec.ToNs != now.UnixNano() || spec.FromNs != now.Add(-time.Hour).UnixNano() {
		t.Fatalf("mutlak pencere: from=%d to=%d", spec.FromNs, spec.ToNs)
	}
	if _, ok := chatChartSpec(`{"ok":false}`, now); ok {
		t.Fatal("ok:false blok üretmez")
	}
	src, _ := os.ReadFile("copilot_chat.go")
	for _, want := range []string{`emit("block", blockSeq.Next(blocks.TypeChart, spec))`, `emit("block", blockSeq.Next(blocks.TypeLink, l))`} {
		if !strings.Contains(string(src), want) {
			t.Errorf("blok yayını yok: %s", want)
		}
	}
}

// v0.10.547 — chart fence/blok compare + source taşır; başlık karşılaştırma ekini alır.
func TestChatChartBlockCarriesCompare(t *testing.T) {
	out := `{"ok":true,"spec":{"service":"api","agg":"p95","rangeS":3600,"groupBy":"","compare":{"kind":"prev_day","shiftS":86400},"source":"metric"}}`
	block, _ := chatChartBlock(out)
	for _, want := range []string{`"compare":{"kind":"prev_day","shiftS":86400}`, `"source":"metric"`, "dün aynı saat"} {
		if !strings.Contains(block, want) {
			t.Errorf("fence %q taşımalı:\n%s", want, block)
		}
	}
	spec, ok := chatChartSpec(out, time.Unix(1_700_000_000, 0))
	if !ok || spec.Compare == nil || spec.Compare.ShiftS != 86400 || spec.Source != "metric" {
		t.Fatalf("blok spec: %+v", spec)
	}
	if b, _ := chatChartBlock(`{"ok":true,"spec":{"service":"api","agg":"rate","rangeS":600}}`); strings.Contains(b, "compare") {
		t.Fatal("compare yokken alan yazılmaz")
	}
}
