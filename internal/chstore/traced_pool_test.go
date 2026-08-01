package chstore

import (
	"os"
	"strings"
	"testing"
)

// v0.9.521 — her CH span'i hangi HAVUZDAN geçtiğini söylemeli.
//
// 2026-08-01 akşamı prod'da CPU dengesizliğinin sebebi üç hipotezle
// dolaylı olarak arandı (gecikmiş replika, dengesiz veri, dış istemci —
// üçü de veriyle öldü) ve asıl soru cevapsız kaldı: okumalar hangi
// havuzda koşuyor? Kod okuyarak "taşıdım" demek yetmedi; ölçüm gerekti.
// Bu etiket o soruyu tek sorguyla cevaplıyor:
//
//	SELECT attr_values[indexOf(attr_keys,'coremetry.ch_pool')] AS pool,
//	       count() FROM spans WHERE name LIKE 'clickhouse.%' GROUP BY pool
//
// Bir span türü etiketsiz kalırsa o yolun trafiği ölçümde görünmez ve
// yine körlemesine tartışırız — test bu yüzden BEŞ türü de sayıyor.
func TestEveryCHSpanCarriesPoolLabel(t *testing.T) {
	b, err := os.ReadFile("traced_conn.go")
	if err != nil {
		t.Fatal(err)
	}
	src := string(b)

	starts := strings.Count(src, "selfobs.Tracer().Start(")
	labels := strings.Count(src, `attribute.String("coremetry.ch_pool", t.pool)`)
	if starts != labels {
		t.Errorf("%d span kurucusu var ama %d tanesi havuz etiketi taşıyor — etiketsiz yolun trafiği ölçümde GÖRÜNMEZ", starts, labels)
	}
	if starts == 0 {
		t.Fatal("span kurucusu bulunamadı — test deseni kodla uyumsuz")
	}
}

// Havuz adları span etiketi olarak SABİT kalmalı: değişirse geçmiş
// spans verisi üzerinde çalışan sorgular sessizce boş döner.
func TestPoolNamesStable(t *testing.T) {
	for name, got := range map[string]string{
		"main":   poolMain,
		"ingest": poolIngest,
		"read":   poolRead,
	} {
		if got != name {
			t.Errorf("havuz adı değişmiş: %q → %q. Geçmiş span sorguları kırılır", name, got)
		}
	}
}

// Üç havuzun ÜÇÜ de etiketli sarmalayıcıdan geçmeli. Biri çıplak
// bağlanırsa o havuzun trafiği hiç izlenmez.
func TestAllThreePoolsAreTraced(t *testing.T) {
	b, err := os.ReadFile("store.go")
	if err != nil {
		t.Fatal(err)
	}
	src := string(b)
	for _, want := range []string{
		"newTracedConn(conn, poolMain)",
		"newTracedConn(ingest, poolIngest)",
		"newTracedConn(readConn, poolRead)",
	} {
		if !strings.Contains(src, want) {
			t.Errorf("%q yok — o havuzun trafiği ölçülemez", want)
		}
	}
}
