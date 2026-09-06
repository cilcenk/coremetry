package vmetrics

import (
	"os"
	"strings"
	"testing"
	"time"
)

// v0.10.504 — dış skill denetimi A6: VictoriaMetrics `query_range` örneği
// kovanın SONUNA damgalı, CH serileri BAŞLANGICINA. Üç tüketici (capacity,
// hosts, endpoints) kendi başına `t − step` yapıyor, ötekilerde (Explore,
// servis RED, dashboard) x ekseni bir adım kayıyordu (7 g'de ~34 dk).
// Kaydırma artık decode'da tek yerde; pencere önündeki ilk kısmi kova düşer.
func TestAtRequestStart(t *testing.T) {
	if !atRequestStart(1_700_000_000, 1_700_000_000) || atRequestStart(1_700_000_060, 1_700_000_000) || atRequestStart(1_699_999_940, 1_700_000_000) {
		t.Fatal("only the sample stamped exactly at start is the pre-window bucket")
	}
}

func TestBucketStartNs(t *testing.T) {
	cases := []struct {
		ts   float64
		step int
		want int64
	}{
		{1_700_000_060, 60, 1_700_000_000 * int64(time.Second)},
		{1_700_000_300, 300, 1_700_000_000 * int64(time.Second)},
		{1_700_000_000, 60, (1_700_000_000 - 60) * int64(time.Second)},
	}
	for _, c := range cases {
		if got := bucketStartNs(c.ts, c.step); got != c.want {
			t.Errorf("bucketStartNs(%v, %d) = %d, want %d", c.ts, c.step, got, c.want)
		}
	}
}

// Kaynak pinleri: iki decode noktası bucketStartNs kullanır ve pencere
// önünü düşürür; üç tüketici telafisi geri gelmez.
func TestEndStampShiftIsSingleSourced(t *testing.T) {
	read := func(p string) string {
		b, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	for _, f := range []string{"throughput.go", "histogram.go"} {
		src := read(f)
		if !strings.Contains(src, "bucketStartNs(ts, step)") || !strings.Contains(src, "if atRequestStart(ts, startSec) {") {
			t.Fatalf("%s must stamp bucket starts via bucketStartNs and drop the sample stamped at the request start", f)
		}
	}
	if strings.Contains(read("capacity.go"), "- capacityTrendStepSec") {
		t.Fatal("capacity.go must not compensate the end stamp any more")
	}
	for _, f := range []string{"../api/hosts_metric.go", "../api/endpoints_metric.go"} {
		if strings.Contains(read(f), "endStamped") {
			t.Fatalf("%s must not carry its own end-stamp compensation", f)
		}
	}
}
