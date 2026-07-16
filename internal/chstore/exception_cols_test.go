// exception_cols_test.go — v0.8.566 (perf #19). Exception tip/mesaj/stack/
// match INSERT anında spans'ın MATERIALIZED kolonlarına iner; beş sorgu
// sitesi exFragments üzerinden düz kolon okur. Bu testler kolon yolunun
// v0.8.494 + v0.8.563 semantiğinden sapmamasını ve DDL ifadelerinin
// guard'lı kalmasını sabitler.
package chstore

import (
	"strings"
	"testing"
)

func TestExFragmentsColumnPath(t *testing.T) {
	f := exFragments(true)
	want := exFrag{Match: "ex_match = 1", Type: "ex_type", Msg: "ex_msg", Stack: "ex_stack"}
	if f != want {
		t.Fatalf("kolon yolu = %+v, istenen %+v", f, want)
	}
	for _, v := range []string{f.Match, f.Type, f.Msg, f.Stack} {
		if strings.Contains(v, "JSON_VALUE") || strings.Contains(v, "events") {
			t.Errorf("kolon yolu events/JSON'a dokunmamalı: %s", v)
		}
	}
}

func TestExFragmentsExpressionFallback(t *testing.T) {
	// external Distributed + cluster_name unset: ALTER atlanır, probe false
	// okur, beş site bugünkü ifadelerle aynen çalışır — v0.8.494'ün çift
	// kaynağı (event + error.type attr'lı hata span'i) korunmalı.
	f := exFragments(false)
	if f.Match != exMatchPred || f.Type != exTypeExpr || f.Msg != exMsgExpr || f.Stack != exStackExpr {
		t.Fatal("ifade yolu kanonik fragmanların birebir kendisi olmalı")
	}
	if !strings.Contains(f.Match, "error.type") || !strings.Contains(f.Match, `"exception"`) {
		t.Fatal("match predicate iki kaynağı da taşımalı (v0.8.494)")
	}
	// v0.8.563: event dalı attr dalından ÖNCE (en zengin kaynak kazanır).
	if ev, at := strings.Index(f.Type, `"exception"`), strings.Index(f.Type, "error.type"); ev < 0 || at < 0 || ev > at {
		t.Fatal("exTypeExpr'de event dalı attr dalından önce gelmeli")
	}
}

func TestExDefExprsGuarded(t *testing.T) {
	// DDL ifadeleri INSERT anında HER satırda değerlenir (multiIf eager).
	// Guard, JSONExtractArrayRaw'un argümanını exception taşımayan satırda
	// '[]' yapar — bunu bir "sadeleştirme" silerse events-ağır korpusta
	// insert maliyeti sessizce katlanır.
	for name, e := range map[string]string{
		"exTypeDefExpr": exTypeDefExpr, "exMsgDefExpr": exMsgDefExpr, "exStackDefExpr": exStackDefExpr,
	} {
		if !strings.Contains(e, exEventsGuard) {
			t.Errorf("%s guard'sız kalmış:\n%s", name, e)
		}
		// D1'in ?-yasağı: literal ? clickhouse-go'da pozisyonel bind sayılır
		// ve ALTER çalışma anında, buradan çok uzakta patlar.
		if strings.Contains(e, "?") {
			t.Errorf("%s bind placeholder içeremez", name)
		}
	}
	if exMatchDefExpr != exMatchPred {
		t.Error("ex_match kolonu sorgu-anı predicate'inin birebir kendisi olmalı — iki tanım sapamaz")
	}
}
