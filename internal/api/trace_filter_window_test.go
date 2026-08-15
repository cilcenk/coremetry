package api

import (
	"net/url"
	"testing"
	"time"
)

// v0.9.1055 regresyon pini — /api/traces penceresiz istekle ARTIK
// sınırsız olamaz. from/to gelmeyince filtre sıfır-zamanla iniyor,
// chstore'un zaman predicate'i koşullu olduğundan (buildTraceWhere
// `if !f.From.IsZero()`) spans TAM TARAMAYA dönüyor ve 25s
// max_execution_time'da 500 veriyordu. UI her zaman pencere yolladığı
// için latent kaldı; API-doğrudan/MCP çağıranlar düşüyordu. Sözleşme:
// to yoksa now, from yoksa to-1h; açık değerler AYNEN korunur.
func TestParseTraceFilterDefaultWindow(t *testing.T) {
	t.Run("penceresiz istek 1h varsayılana iner", func(t *testing.T) {
		before := time.Now()
		f, err := parseTraceFilter(url.Values{})
		if err != nil {
			t.Fatal(err)
		}
		after := time.Now()
		if f.From.IsZero() || f.To.IsZero() {
			t.Fatalf("pencere hâlâ sıfır: from=%v to=%v — sınırsız spans taraması geri döndü", f.From, f.To)
		}
		if f.To.Before(before) || f.To.After(after) {
			t.Fatalf("to ≈ now değil: %v", f.To)
		}
		if got := f.To.Sub(f.From); got != time.Hour {
			t.Fatalf("varsayılan pencere %v, want 1h", got)
		}
	})

	t.Run("açık from/to aynen korunur", func(t *testing.T) {
		from := time.Date(2026, 8, 15, 9, 0, 0, 0, time.UTC)
		to := time.Date(2026, 8, 15, 12, 0, 0, 0, time.UTC)
		q := url.Values{}
		q.Set("from", "1786784400000000000")
		q.Set("to", "1786795200000000000")
		f, err := parseTraceFilter(q)
		if err != nil {
			t.Fatal(err)
		}
		if !f.From.Equal(from) || !f.To.Equal(to) {
			t.Fatalf("açık pencere değişti: from=%v to=%v", f.From, f.To)
		}
	})
}
