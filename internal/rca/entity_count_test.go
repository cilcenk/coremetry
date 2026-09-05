package rca

import (
	"strings"
	"testing"
)

// v0.10.421 — E6 kalkan sayacı: gösterilen ad sayılmaz, uydurulan sayılır,
// tavan 255, bilinen küme ve teknik terim beyaz listesi geçerli.
func TestCountUnknownEntities(t *testing.T) {
	cases := map[string]struct {
		known  []string
		prompt string
		answer string
		want   uint8
	}{
		"temiz":              {nil, "checkout-svc yavaş", "checkout-svc p99 yüksek", 0},
		"bir uydurma":        {nil, "checkout-svc yavaş", "checkout-svc, ghost-gateway'e bağımlı", 1},
		"bilinen küme":       {[]string{"payment-api"}, "", "payment-api hatalı", 0},
		"teknik terim":       {nil, "", "error-rate ve root-cause yüksek", 0},
		"aynı ad bir kez":    {nil, "", "ghost-a ghost-a ghost-b", 2},
		"prompt gösterdiyse": {nil, "CHANNEL_CODE=internet-banking", "internet-banking kanalı", 0},
		// v0.10.431 — genel tireli terimler ve yüzdelik aralıkları sayılmaz.
		"teknik terimler 431": {nil, "", "rate-limiting devrede, p95-p99 farkı büyük, circuit-breaker açık, x-request-id yok", 0},
		"aralık + uydurma":    {nil, "", "p50-p95-p99 sapması ghost-gateway kaynaklı", 1},
	}
	for name, c := range cases {
		if got := CountUnknownEntities(LowerKnownSet(c.known...), c.prompt, c.answer); got != c.want {
			t.Errorf("%s: %d, want %d", name, got, c.want)
		}
	}
	var sb strings.Builder
	for i := 0; i < 300; i++ {
		sb.WriteString(" svc-" + strings.Repeat("x", i%7+1) + "-" + string(rune('a'+i%26)) + string(rune('a'+(i/26)%26)))
	}
	if got := CountUnknownEntities(nil, "", sb.String()); got != 255 {
		t.Fatalf("tavan 255 olmalı, %d", got)
	}
}
