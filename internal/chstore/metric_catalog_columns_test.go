package chstore

import (
	"strings"
	"testing"
)

// v0.9.833 — /metrics katalog satırları "Last seen" + "Services"
// kolonları kazandı. İkisi de metric_catalog MV'sinden ŞEMA
// DEĞİŞİKLİĞİ OLMADAN geliyor:
//
//   • maxMerge(last_seen_state) zaten HESAPLANIYORDU (susmuş metriği
//     picker'dan düşüren HAVING) ama projeksiyona hiç girmiyordu,
//   • service_name MV'nin İLK ORDER BY kolonu ve okunan her satırda
//     zaten var, yani mevcut GROUP BY üstünde uniqExact ek tarama
//     getirmiyor.
//
// Bu tablo iki şeyi çiviliyor:
//
//  1. İki yol AYNI kolonları döndürüyor. Ham metric_points geri
//     düşüşü yalnız taze yükseltmelerin ilk dakikalarında koşar —
//     kolon listesi kayarsa katalog her deploy'dan sonra kimsenin
//     bakmadığı bir pencerede sessizce fakirleşir (ve Scan hedef
//     sayısı tutmadığı için hata verir).
//  2. Maliyet korumaları yerinde: HAVING tazelik bağı, ORDER BY
//     metric, LIMIT/OFFSET yalnız sayfalıyken ve HER İKİ yolda da
//     SETTINGS max_execution_time (CLAUDE.md sert kısıtı).

// projections both metric-name paths MUST carry, in Scan order.
var wantMetricNameCols = []string{
	"metric",
	"toUnixTimestamp64Nano(",
	"uniqExact(service_name)",
}

func TestMetricNameSelectsShareColumns(t *testing.T) {
	cases := []struct {
		name  string
		sql   string
		table string
		exec  string
	}{
		{"catalog paged", metricCatalogSelectSQL("WHERE service_name = ?", true), "metric_catalog", "max_execution_time = 10"},
		{"catalog unpaged", metricCatalogSelectSQL("", false), "metric_catalog", "max_execution_time = 10"},
		{"raw paged", metricNamesRawSelectSQL("WHERE time >= ?", true), "metric_points", "max_execution_time = 25"},
		{"raw unpaged", metricNamesRawSelectSQL("WHERE time >= ?", false), "metric_points", "max_execution_time = 25"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			for _, col := range wantMetricNameCols {
				if !strings.Contains(c.sql, col) {
					t.Errorf("missing projection %q in:\n%s", col, c.sql)
				}
			}
			if !strings.Contains(c.sql, "FROM "+c.table) {
				t.Errorf("expected FROM %s in:\n%s", c.table, c.sql)
			}
			if !strings.Contains(c.sql, "GROUP BY metric") {
				t.Errorf("missing GROUP BY metric in:\n%s", c.sql)
			}
			if !strings.Contains(c.sql, "ORDER BY metric") {
				t.Errorf("missing ORDER BY metric in:\n%s", c.sql)
			}
			// Sert kısıt: metric_points / spans üstündeki her sorgu
			// max_execution_time taşır.
			if !strings.Contains(c.sql, c.exec) {
				t.Errorf("missing SETTINGS %s in:\n%s", c.exec, c.sql)
			}
		})
	}
}

// Scan hedefleri 6 kolon bekliyor; iki sorgunun da tam 6 projeksiyonu
// olmalı. Virgül saymak kaba ama YANLIŞ SAYIYI yakalar ve yanlış sayı
// çalışma anında "expected 6 destination arguments" hatasıdır.
func TestMetricNameSelectsProjectSixColumns(t *testing.T) {
	for _, c := range []struct {
		name string
		sql  string
	}{
		{"catalog", metricCatalogSelectSQL("", true)},
		{"raw", metricNamesRawSelectSQL("", true)},
	} {
		t.Run(c.name, func(t *testing.T) {
			head := c.sql[strings.Index(c.sql, "SELECT")+len("SELECT") : strings.Index(c.sql, "FROM ")]
			if got := topLevelCommas(head) + 1; got != 6 {
				t.Fatalf("projection count = %d, want 6 (Scan hedefleriyle eşleşmeli)\n%s", got, head)
			}
		})
	}
}

// topLevelCommas counts commas at paren depth 0 — nested ones belong to
// a function's own args (toUnixTimestamp64Nano(maxMerge(x))).
func topLevelCommas(s string) int {
	depth, n := 0, 0
	for _, r := range s {
		switch r {
		case '(':
			depth++
		case ')':
			depth--
		case ',':
			if depth == 0 {
				n++
			}
		}
	}
	return n
}

func TestMetricCatalogSelectPaging(t *testing.T) {
	paged := metricCatalogSelectSQL("", true)
	unpaged := metricCatalogSelectSQL("", false)
	if !strings.Contains(paged, "LIMIT ? OFFSET ?") {
		t.Errorf("paged query must carry LIMIT/OFFSET:\n%s", paged)
	}
	if strings.Contains(unpaged, "LIMIT") {
		t.Errorf("unpaged query must NOT carry LIMIT:\n%s", unpaged)
	}
	// LIMIT, SETTINGS'ten ÖNCE gelmeli — sonra gelirse CH sözdizimi hatası.
	if li, si := strings.Index(paged, "LIMIT"), strings.Index(paged, "SETTINGS"); li < 0 || si < 0 || li > si {
		t.Errorf("LIMIT must precede SETTINGS:\n%s", paged)
	}
	// Tazelik bağı her iki biçimde de zorunlu: düşerse 7 gündür susan
	// metrikler picker'a geri döner (v0.8.311/396'nın kaldırdığı yük).
	for _, q := range []string{paged, unpaged} {
		if !strings.Contains(q, "HAVING maxMerge(last_seen_state) >= ?") {
			t.Errorf("missing freshness HAVING:\n%s", q)
		}
	}
}

// WHERE parçası verildiği gibi, GROUP BY'dan ÖNCE gömülmeli.
func TestMetricNameSelectsEmbedWhere(t *testing.T) {
	for _, c := range []struct {
		name, sql string
	}{
		{"catalog", metricCatalogSelectSQL("WHERE service_name = ? AND metric ILIKE ?", true)},
		{"raw", metricNamesRawSelectSQL("WHERE time >= ? AND service_name = ?", true)},
	} {
		t.Run(c.name, func(t *testing.T) {
			wi, gi := strings.Index(c.sql, "WHERE"), strings.Index(c.sql, "GROUP BY")
			if wi < 0 || gi < 0 || wi > gi {
				t.Fatalf("WHERE must sit before GROUP BY:\n%s", c.sql)
			}
		})
	}
}
