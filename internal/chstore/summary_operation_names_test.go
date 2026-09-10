package chstore

// summary_operation_names_test.go — v0.10.657 (operatör, prod: Traces'te
// servis seçmeden operasyon kutusuna yazınca "Navigation: /logs?q=…" gibi
// satırlar geliyordu — "global arama gibi çalışıyor").
//
// Kök neden: /api/operation-names servissiz aramada TÜM servislerin
// operasyonlarını `name ILIKE %x%` ile buluyor ve ALFABETİK sıralıyordu.
// Coremetry'nin kendi tarayıcı öz-telemetrisi (coremetry-frontend) sayfa
// URL'lerini span adı olarak yayar; URL'nin içinde servis adı geçtiği için
// bunlar eşleşiyor ve büyük harfli "Navigation:" alfabede öne geçiyordu.
//
// SÖZLEŞME (saf operationNamesQuery):
//   1. Servis seçili değilse öz-telemetri servisleri dışlanır (servis
//      açıkça seçilince yine listelenir — dogfood bozulmaz).
//   2. Joker karakter yoksa ÖNEK eşleşmesi öne gelir, sonra alfabetik.
//   3. Joker varsa (pay*, *pay*, p?y) sıralama alfabetik kalır.
// Mutasyon (ölçüldü): NOT IN'i kaldırmak 1. testi, önek sıralamasını
// kaldırmak 2. testi düşürür.

import (
	"strings"
	"testing"
)

func TestOperationNamesQueryExcludesSelfTelemetryWhenUnscoped(t *testing.T) {
	wc, _ := operationNamesQuery("", "bsa-mobile")
	sql := wc.sql()
	if !strings.Contains(sql, "service_name NOT IN") {
		t.Fatalf("servissiz aramada öz-telemetri dışlanmalı:\n%s", sql)
	}
	if !strings.Contains(sql, "name ILIKE ?") {
		t.Fatalf("ILIKE yüklemi kayboldu:\n%s", sql)
	}
	found := false
	for _, a := range wc.args {
		if s, ok := a.(string); ok && s == "%bsa-mobile%" {
			found = true
		}
	}
	if !found {
		t.Fatalf("ILIKE argümanı %%bsa-mobile%% olmalı: %v", wc.args)
	}
	// Servis seçiliyken dışlama YOK (dogfood: öz-telemetri açıkça seçilebilir).
	wc2, _ := operationNamesQuery("coremetry-frontend", "")
	if strings.Contains(wc2.sql(), "NOT IN") {
		t.Fatalf("servis seçiliyken dışlama olmamalı:\n%s", wc2.sql())
	}
}

func TestOperationNamesQueryPrefixFirstWithoutWildcard(t *testing.T) {
	_, order := operationNamesQuery("", "bsa-mobile")
	if !strings.Contains(order, "startsWith(lowerUTF8(name)") || !strings.HasSuffix(strings.TrimSpace(order), ", name") {
		t.Fatalf("jokersiz aramada önek eşleşmesi öne gelmeli, sonra alfabetik: %q", order)
	}
	_, order2 := operationNamesQuery("", "pay*")
	if strings.Contains(order2, "startsWith") {
		t.Fatalf("jokerli aramada önek sıralaması olmamalı: %q", order2)
	}
	_, order3 := operationNamesQuery("svc", "")
	if strings.Contains(order3, "startsWith") {
		t.Fatalf("boş desende önek sıralaması olmamalı: %q", order3)
	}
}
