package api

import (
	"net/http/httptest"
	"testing"

	"github.com/cilcenk/coremetry/internal/copilot"
)

// v0.9.1119 (Faz 0.3) regresyon — tek-atış ✨ yüzeylerinin geri
// bildirim rayı. v0.9.593 taşımayı yalnız JSON sarmalayıcısına
// getirmişti; copilotExplain ctx'teki kimliği DÜŞÜRÜYORDU ve 15 prose
// yüzeyi oylanamıyordu. İki yarım: withExchange ctx'e kimlik koyar +
// (ai_observability.go'daki taşıma satırı) sarmalayıcı onu okur.
func TestWithExchangeSeedsContext(t *testing.T) {
	r := httptest.NewRequest("POST", "/api/copilot/explain-span/x", nil)
	r2, xid := withExchange(r)
	if xid == "" {
		t.Fatal("boş exchange kimliği")
	}
	if got := copilot.MetaFromContext(r2.Context()).ExchangeID; got != xid {
		t.Fatalf("ctx kimliği taşımıyor: %q != %q", got, xid)
	}
	// Aynı istekte iki mint iki farklı kimlik üretir (yanıt-başına ray).
	_, xid2 := withExchange(r)
	if xid2 == xid {
		t.Error("kimlikler benzersiz olmalı")
	}
}
