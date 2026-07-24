package chstore

import (
	"strings"
	"testing"
	"time"
)

// v0.9.205 — Operator-reported: image-tag'siz bir serviste (zincir
// service.version'a düşer; "2.1.6.Final-redhat-00001" gibi SABİT bir
// artifact sürümü) Service Overview her pencerenin SOL KENARINA sahte
// bir deploy işaretçisi basıyordu — pencere-içi min(time) "deploy"
// sayılıyordu. Sözleşme: tarama pencere-öncesi lookback'i kapsar ve
// HAVING yalnız pencere İÇİNDE başlayan sürümleri geçirir. Bu test o
// iki parçayı pinler; kaldıranı yakalar.
func TestServiceDeploysSQLWindowStartContract(t *testing.T) {
	if !strings.Contains(serviceDeploysSQL, "first_seen_ns >= ?") {
		t.Fatal("serviceDeploysSQL lost the HAVING first_seen_ns >= ? window-start bound — constant-version services will fake a deploy at every window's left edge again")
	}
	// HAVING, GROUP BY'dan sonra gelmeli (pencere-başı filtresi aggregate üstünde).
	g := strings.Index(serviceDeploysSQL, "GROUP BY version")
	h := strings.Index(serviceDeploysSQL, "first_seen_ns >= ?")
	if g < 0 || h < g {
		t.Fatal("window-start bound must live in the HAVING clause after GROUP BY")
	}
	if deployLookback < 24*time.Hour {
		t.Fatalf("deployLookback=%v — 24h altına inerse günlük batch servislerinin sessiz aralıkları sabit sürümü yine 'yeni' gösterir", deployLookback)
	}
}
