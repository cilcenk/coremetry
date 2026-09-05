package logstore

import (
	"os"
	"strings"
	"testing"
)

// v0.10.414 — log arama denetimi A7: SkipTotal İKİ backend'de de uyulur
// (logstore.Filter.Pod başlığındaki "sessiz no-op" bug sınıfı). ES gövdesi
// helper üzerinden, CH sarmalayıcı alanı iletir ve tavan bayrağını kurmaz.
func TestSkipTotalHonouredByBothBackends(t *testing.T) {
	if esTrackTotalHits(true) != false || esTrackTotalHits(false) != 10000 {
		t.Fatal("esTrackTotalHits: skip → false, aksi 10000")
	}
	es, _ := os.ReadFile("elasticsearch.go")
	if !strings.Contains(string(es), `"track_total_hits": esTrackTotalHits(f.SkipTotal)`) {
		t.Fatal("ES Search gövdesi SkipTotal'ı track_total_hits'e bağlamıyor")
	}
	ch, _ := os.ReadFile("clickhouse.go")
	if !strings.Contains(string(ch), "SkipTotal: f.SkipTotal") || !strings.Contains(string(ch), "!f.SkipTotal && total >= chstore.LogsCountCap") {
		t.Fatal("CH sarmalayıcı SkipTotal'ı iletmiyor ya da tavan bayrağını kuruyor")
	}
}
