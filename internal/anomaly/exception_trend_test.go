package anomaly

import (
	"strings"
	"testing"
	"time"

	"github.com/cilcenk/coremetry/internal/chstore"
)

// exception_trend_test.go — v0.9.1129 (AI Faz 2.1).
//
// BuildExceptionExplainInput'un iki iç hesabı, insight kartı aynı
// sayıları göstereceği için SAF fonksiyonlara ayrıldı. Refactor'ın tek
// riski prompt'un kayması: prompt satırları BAYT BAYT eskisi gibi
// kalmalı, yoksa modelin okuduğu şekil ve altın-örnek testleri sessizce
// değişir. Aşağıdaki iki pin tam olarak bunu tutuyor.

func TestSummarizeExceptionOccurrences(t *testing.T) {
	now := time.Date(2026, 8, 16, 12, 0, 0, 0, time.UTC).UnixNano()
	h := int64(time.Hour)

	cases := []struct {
		name string
		occ  []chstore.OccurrencePoint
		want ExceptionTrend
	}{
		{name: "boş", occ: nil, want: ExceptionTrend{}},
		{
			name: "24 saat sınırı: tam kenarda olan SAYILIR",
			occ: []chstore.OccurrencePoint{
				{Time: now - 48*h, Count: 100}, // pencere dışı
				{Time: now - 24*h, Count: 7},   // TAM kenar → içeri
				{Time: now - 1*h, Count: 3},
			},
			want: ExceptionTrend{Total: 110, Last24: 10, Peak: 100, PeakAtNs: now - 48*h, Buckets: 3},
		},
		{
			name: "tepe İLK maksimumda kalır (eşitlikte kaymaz)",
			occ: []chstore.OccurrencePoint{
				{Time: now - 3*h, Count: 9},
				{Time: now - 2*h, Count: 9},
			},
			want: ExceptionTrend{Total: 18, Last24: 18, Peak: 9, PeakAtNs: now - 3*h, Buckets: 2},
		},
		{
			name: "gelecek damgası da son-24h'a girer (saat kayması)",
			occ:  []chstore.OccurrencePoint{{Time: now + h, Count: 4}},
			want: ExceptionTrend{Total: 4, Last24: 4, Peak: 4, PeakAtNs: now + h, Buckets: 1},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := SummarizeExceptionOccurrences(tc.occ, now)
			if got != tc.want {
				t.Errorf("got %+v; want %+v", got, tc.want)
			}
		})
	}
}

// TestExceptionTrendPromptLineUnchanged — prompt satırının ŞEKLİ.
// v0.9.1129 öncesindeki fmt.Sprintf ile bayt bayt aynı olmak zorunda
// (tepe damgası UTC + "2006-01-02 15:04").
func TestExceptionTrendPromptLineUnchanged(t *testing.T) {
	peak := time.Date(2026, 8, 16, 9, 5, 0, 0, time.UTC)
	line := ExceptionTrend{Total: 1240, Last24: 380, Peak: 91,
		PeakAtNs: peak.UnixNano(), Buckets: 48}.PromptLine()
	const want = "toplam=1240 son24h=380 tepe=91@2026-08-16 09:05 bucket=48"
	if line != want {
		t.Errorf("prompt satırı = %q\nwant             %q", line, want)
	}
}

// TestPickDeploysAroundStartRefs — SEÇİM yarısı: yön bayrağı + MUTLAK
// uzaklık. Yönü işaretle kodlamamanın sebebi burada görünür: bir
// "sonra" deploy'u sub-saniye ise OffsetSec 0'a düşer ve 0'ın işareti
// yoktur.
func TestPickDeploysAroundStartRefs(t *testing.T) {
	first := int64(1_700_000_000) * int64(time.Second)
	dep := func(v string, off time.Duration) chstore.Deploy {
		return chstore.Deploy{Version: v, TimeUnixNs: first + int64(off)}
	}

	refs := PickDeploysAroundStartRefs([]chstore.Deploy{
		dep("v1", -5*time.Hour), dep("v2", -200*time.Minute), dep("v3", -90*time.Minute),
		dep("v4", -30*time.Minute), dep("v5", -5*time.Minute),
		dep("v6", 500*time.Millisecond), dep("v7", 2*time.Hour), dep("v8", 10*time.Hour),
	}, first)

	want := []NearbyDeploy{
		{Version: "v3", OffsetSec: 5400},
		{Version: "v4", OffsetSec: 1800},
		{Version: "v5", OffsetSec: 300},
		{Version: "v6", OffsetSec: 0, After: true}, // sub-saniye SONRA
		{Version: "v7", OffsetSec: 7200, After: true},
	}
	if len(refs) != len(want) {
		t.Fatalf("aday sayısı = %d (%+v); want %d", len(refs), refs, len(want))
	}
	for i := range want {
		if refs[i] != want[i] {
			t.Errorf("aday %d = %+v; want %+v", i, refs[i], want[i])
		}
	}

	// Ve BİÇİMLEME yarısı eski metni üretmeye devam ediyor: "0 dk SONRA"
	// satırı, yön bilgisini işaretten değil bayraktan alıyor.
	lines := PickDeploysAroundStart([]chstore.Deploy{dep("v6", 500*time.Millisecond)}, first)
	if len(lines) != 1 || !strings.Contains(lines[0], "0 dk SONRA — kök neden OLAMAZ") {
		t.Errorf("sub-saniye sonra satırı = %v", lines)
	}
}

// TestPickDeploysAroundStartRefsEmpty — deploy yokken çökme yok.
func TestPickDeploysAroundStartRefsEmpty(t *testing.T) {
	if got := PickDeploysAroundStartRefs(nil, 1); len(got) != 0 {
		t.Errorf("boş girdide %d aday", len(got))
	}
}
