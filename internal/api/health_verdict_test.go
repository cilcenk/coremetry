package api

import (
	"net/http"
	"testing"
)

// v0.8.339 (HA audit H3) — readiness verdict. The audited hole: a
// fast-refusing dead ClickHouse keeps the ingest queues EMPTY (flushers
// discard instantly), so the old queue-depth-only health reported "ok"
// while 100% of telemetry was being thrown away. CH reachability now
// dominates the verdict; overload stays a 503 (drain out of the LB) and
// degraded stays 200 (visible, not evicting).
// v0.9.985 — spoolDegraded dördüncü sinyal olarak eklendi. Sözleşme
// değişti, kapı da değişti: bir INSERT'in "OK" dönmesi dağıtık kipte
// verinin İNDİĞİ anlamına gelmiyor (Distributed motoru diske spool'layıp
// hemen OK diyor), ve 2026-08-12'de lokal küme 3s39d hiç span yazamazken
// bu fonksiyonun bütün girdileri "sağlıklı"ydı.
func TestHealthVerdict(t *testing.T) {
	cases := []struct {
		name                                      string
		overloaded, degraded, spoolDegraded, chOK bool
		wantStatus                                string
		wantCode                                  int
	}{
		{"all healthy", false, false, false, true, "ok", http.StatusOK},
		{"degraded is visible but routable", false, true, false, true, "degraded", http.StatusOK},
		{"overload evicts from LB", true, false, false, true, "overloaded", http.StatusServiceUnavailable},
		{"CH down with EMPTY queues still 503s", false, false, false, false, "clickhouse-unreachable", http.StatusServiceUnavailable},
		{"CH down wins over overload in the label", true, true, false, false, "clickhouse-unreachable", http.StatusServiceUnavailable},

		// v0.9.985 — spool sinyali.
		{
			// ASIL VAKA: kuyruklar boş, CH ping'i cevap veriyor, hiçbir
			// yazma hatası yok — ve veri 3.5 saattir inmiyor. v0.9.984
			// bu satıra "ok" derdi.
			"spool backlog is its OWN label, not a generic degraded",
			false, false, true, true, "clickhouse-spool-backlog", http.StatusOK,
		},
		{
			// 200 KALIR: backlog ClickHouse tarafında; pod'u LB'den
			// düşürmek tek bayt kurtarmaz, API yarısını da karartır.
			"spool backlog never evicts the pod",
			false, false, true, true, "clickhouse-spool-backlog", http.StatusOK,
		},
		{
			// Spool, queue-degraded'ın ÜSTÜNDE: %70 dolu bir tampon henüz
			// veri kaybettirmiyor, inmeyen bir spool zaten kaybettiriyor.
			"spool outranks queue pressure in the label",
			false, true, true, true, "clickhouse-spool-backlog", http.StatusOK,
		},
		{
			// Ama taşan kuyruk hâlâ 503: orası GERÇEK tahliye sebebi.
			"overload still outranks spool",
			true, false, true, true, "overloaded", http.StatusServiceUnavailable,
		},
		{
			"unreachable outranks everything",
			true, true, true, false, "clickhouse-unreachable", http.StatusServiceUnavailable,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			status, code := healthVerdict(c.overloaded, c.degraded, c.spoolDegraded, c.chOK)
			if status != c.wantStatus || code != c.wantCode {
				t.Fatalf("healthVerdict(%v,%v,%v,%v) = (%q,%d), want (%q,%d)",
					c.overloaded, c.degraded, c.spoolDegraded, c.chOK,
					status, code, c.wantStatus, c.wantCode)
			}
		})
	}
}

// chStatusLabel — gövdedeki `clickhouse` alanı. v0.9.985 öncesi iki
// hâlliydi: TCP'ye cevap veren her ClickHouse "ok" derdi, hiçbir span'in
// inmediği küme dâhil.
func TestCHStatusLabel(t *testing.T) {
	cases := []struct {
		chOK, spoolDegraded bool
		want                string
	}{
		{true, false, "ok"},
		{true, true, "degraded"},
		{false, false, "unreachable"},
		// Erişilemezlik spool'u bastırır: spool ölçümü zaten o CH'den
		// geliyor, bayat bir "degraded" gerçeği yumuşatmamalı.
		{false, true, "unreachable"},
	}
	for _, c := range cases {
		if got := chStatusLabel(c.chOK, c.spoolDegraded); got != c.want {
			t.Fatalf("chStatusLabel(%v,%v) = %q, want %q",
				c.chOK, c.spoolDegraded, got, c.want)
		}
	}
}
