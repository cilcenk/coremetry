// v0.9.1100 (F3.5) regresyon testleri — log desen kanıt paketi.
// Korunanlar: kind dallarının Türkçe etiketleri, kesme ifşası,
// "okunamadı ≠ sıfır" itirafı, örneklerin tek-satır/rune-güvenli kesimi.
package api

import (
	"strings"
	"testing"
	"time"

	"github.com/cilcenk/coremetry/internal/anomaly"
	"github.com/cilcenk/coremetry/internal/chstore"
)

func TestRenderLogPatternsEvidence(t *testing.T) {
	hit := func(kind string, cur, base uint64) anomaly.LogPatternAnomaly {
		return anomaly.LogPatternAnomaly{Pattern: "conn refused", Kind: kind,
			CurrentCount: cur, BaselineCount: base, Ratio: float64(cur) / float64(max(base, 1)),
			Service: "checkout", Sample: "line1\nline2  spaced"}
	}

	t.Run("iki kind dalı da etiketli + örnek tek satır", func(t *testing.T) {
		got := renderLogPatternsEvidence(30*time.Minute,
			[]anomaly.LogPatternAnomaly{hit("new", 40, 0), hit("spike", 90, 10)},
			nil, true, true)
		for _, want := range []string{
			"son 30 dakika",
			"[YENİ]", "[PATLAMA]",
			"şimdi 90, taban 10 (9.0x)",
			"en çok checkout",
			"örnek: line1 line2 spaced", // \n ve çift boşluk düzleşir
			"kayıtlı log şablonu yok",
		} {
			if !strings.Contains(got, want) {
				t.Errorf("pakette %q yok:\n%s", want, got)
			}
		}
	})

	t.Run("kesme ifşası — 10'dan fazlası söylenir", func(t *testing.T) {
		many := make([]anomaly.LogPatternAnomaly, 14)
		for i := range many {
			many[i] = hit("spike", 50, 5)
		}
		got := renderLogPatternsEvidence(15*time.Minute, many, nil, true, true)
		if !strings.Contains(got, "14 bulundu, ilk 10 listede") {
			t.Errorf("kesme ifşası eksik:\n%s", got)
		}
	})

	t.Run("okunamadı ≠ sıfır — iki kaynak ayrı itiraf", func(t *testing.T) {
		got := renderLogPatternsEvidence(5*time.Minute, nil, nil, false, false)
		if !strings.Contains(got, "taraması OKUNAMADI") || !strings.Contains(got, "kataloğu OKUNAMADI") {
			t.Errorf("itiraflar eksik:\n%s", got)
		}
		if strings.Contains(got, "desen yok") || strings.Contains(got, "şablonu yok") {
			t.Errorf("okuma hatası 'yok' ile karışmamalı:\n%s", got)
		}
	})

	t.Run("şablonlar hacim+servis+exception ile", func(t *testing.T) {
		got := renderLogPatternsEvidence(30*time.Minute, nil, []chstore.LogTemplate{
			{Template: "GET <path> took <num> ms", TotalCount: 120000,
				Services: []string{"a", "b", "c"}, ExceptionType: "TimeoutError"},
		}, true, true)
		for _, want := range []string{"120000 kayıt, 3 servis", "exception=TimeoutError"} {
			if !strings.Contains(got, want) {
				t.Errorf("şablon satırında %q yok:\n%s", want, got)
			}
		}
	})
}

func TestOneLineSample(t *testing.T) {
	// Rune-güvenli kesim: Türkçe karakter sınırda bölünmemeli.
	s := strings.Repeat("ş", 130)
	got := oneLineSample(s, 120)
	if !strings.HasSuffix(got, "…") || len([]rune(got)) != 121 {
		t.Errorf("rune kesimi yanlış: len=%d", len([]rune(got)))
	}
	if oneLineSample("a\n\nb\tc", 50) != "a b c" {
		t.Errorf("çok satır tek satıra inmeli")
	}
}
