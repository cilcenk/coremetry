package logstore

import (
	"strings"
	"testing"

	"github.com/cilcenk/coremetry/internal/chstore"
)

// log_chains_pin_test.go — v0.10.279. logs tablosunun res-array zincirleri
// iki pakette tanımlı (logstore: histogram/FieldStats kırılımı; chstore:
// liste yolu + logql alan çözümü). Anlamca ıraksarlarsa `cluster:"x"`
// histogramda bir kümeyi, listede başka bir kümeyi anlatır (v0.8.400
// map-access sınıfı). Boşluk-normalize birebir eşitlik.
//
// Namespace BİLİNÇLİ dışarıda: filtre chstore/identity.go'nun tek sözlüğünü
// (namespaceExpr) kullanır, kırılım burada kendi zincirini — birleştirme
// ayrı dilim (v0.10.279 notu).
func TestLogsChainsMatchChstore(t *testing.T) {
	norm := func(s string) string { return strings.Join(strings.Fields(s), " ") }
	for _, tc := range []struct{ name, ls, cs string }{
		{"cluster", chLogsClusterExpr, chstore.LogsClusterChainSQL},
		{"env", chLogsEnvExpr, chstore.LogsEnvChainSQL},
		{"pod", chLogsPodExpr, chstore.LogsPodChainSQL},
	} {
		if norm(tc.ls) != norm(tc.cs) {
			t.Errorf("%s zinciri ıraksadı:\n logstore: %s\n chstore:  %s", tc.name, norm(tc.ls), norm(tc.cs))
		}
	}
}
