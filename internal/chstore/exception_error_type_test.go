package chstore

import (
	"strings"
	"testing"
)

// exception_error_type_test.go — v0.8.494 (operatör isteği: "error.type
// kritik önem arz ediyor"). Exception hattı yalnız exception EVENT'li
// span'leri görüyordu; event'siz ama error.type attribute'lu hata
// span'leri (java.net.UnknownHostException gibi DNS/connect sınıfı
// client hataları) triage'da görünmezdi. Bu test ortak fragment'ların
// şeklini ve occurrencesQuery'nin iki kaynağı da taramasını sabitler.
func TestExceptionErrorTypeFragments(t *testing.T) {
	t.Run("match predicate covers both sources", func(t *testing.T) {
		for _, must := range []string{
			`events LIKE '%"exception"%'`,           // klasik event yolu
			`status_code = 'error'`,                 // attr yolu hata şartlı
			`has(attr_keys, 'error.type')`,          // attr varlığı
		} {
			if !strings.Contains(exMatchPred, must) {
				t.Fatalf("exMatchPred fragment %q kaybolmuş:\n%s", must, exMatchPred)
			}
		}
	})

	t.Run("type expr prefers the event, falls back to the attr", func(t *testing.T) {
		evIdx := strings.Index(exTypeExpr, `"exception.type"`)
		attrIdx := strings.Index(exTypeExpr, `'error.type'`)
		if evIdx < 0 || attrIdx < 0 {
			t.Fatalf("exTypeExpr iki dalı da içermeli:\n%s", exTypeExpr)
		}
		if evIdx > attrIdx {
			t.Fatalf("event dalı attr dalından ÖNCE gelmeli (en zengin kaynak öncelikli):\n%s", exTypeExpr)
		}
		if !strings.Contains(exTypeExpr, `'<unknown>'`) {
			t.Fatalf("default '<unknown>' dalı kaybolmuş:\n%s", exTypeExpr)
		}
	})

	t.Run("msg expr falls back to status_msg", func(t *testing.T) {
		if !strings.Contains(exMsgExpr, `"exception.message"`) ||
			!strings.Contains(exMsgExpr, "status_msg") {
			t.Fatalf("exMsgExpr iki dalı da içermeli:\n%s", exMsgExpr)
		}
	})

	// occurrencesQuery gerçek SQL üretimi — histogram, attr-doğumlu
	// grupta da saymalı; tek başına event LIKE filtresi kalırsa grup
	// detayı "0 occurrence" okur.
	t.Run("occurrences query scans both sources", func(t *testing.T) {
		q := occurrencesQuery(occurrenceBucketCap, "max_threads = 4")
		if !strings.Contains(q, "has(attr_keys, 'error.type')") {
			t.Fatalf("occurrencesQuery error.type dalını kaybetmiş:\n%s", q)
		}
		if !strings.Contains(q, `events LIKE '%"exception"%'`) {
			t.Fatalf("occurrencesQuery event dalını kaybetmiş:\n%s", q)
		}
	})
}

// TestExFragmentsFirstEvent — v0.8.563 regression. The type/message/stack
// expressions read $[0] — the FIRST array element, not the first EXCEPTION
// event. Instrumentations that emit a retry/log event before the exception
// (the java.net client libs among them) matched exMatchPred via the LIKE
// but yielded empty type/msg/stack, so real exceptions landed in an ''
// group. All extraction now goes through exFirstEvent (arrayFirst by event
// name); the behaviour pair was proven live before shipping:
// second-position exception → old '' / new java.net.UnknownHostException,
// first-position → identical on both.
func TestExFragmentsFirstEvent(t *testing.T) {
	for name, frag := range map[string]string{
		"exTypeExpr": exTypeExpr, "exMsgExpr": exMsgExpr, "exStackExpr": exStackExpr,
	} {
		if strings.Contains(frag, "$[0]") {
			t.Errorf("%s still reads $[0] — first ARRAY element is not first EXCEPTION event", name)
		}
		if !strings.Contains(frag, "arrayFirst") || !strings.Contains(frag, "'exception'") {
			t.Errorf("%s must select the first event NAMED exception via exFirstEvent", name)
		}
	}
	// The three query files that used to carry pasted $[0] stacktrace
	// copies must stay on the shared fragment — a fourth paste would
	// silently reintroduce the bug for that surface only.
	if !strings.Contains(exFirstEvent, "JSONExtractArrayRaw(events)") {
		t.Error("exFirstEvent must walk the events array")
	}
}
