// anomaly_events TOPLU YAZIMI (v0.9.957) — taşınan semantiğin pinleri.
//
// ─── Hangi olayı zorunlu kılıyor ─────────────────────────────────────
// Davranış motoru (v0.9.936) bir tikte 37 olay yazıyordu ve bunu 37
// ardışık UpsertAnomalyEvent çağrısıyla yapıyordu. ÖLÇÜLDÜ (lokal,
// 2026-08-11): 25 618 ms'lik tikin ~20 saniyesi bu döngüdeydi — 28
// günlük MV sorgusu değil, olay YAZIMI. Her çağrı iki gidiş-dönüş
// (bir FINAL SELECT + bir INSERT) demekti, yani 74 round-trip; her biri
// TEK satır için, ve her FINAL bir ReplacingMergeTree birleştirmesi
// ödüyordu.
//
// Yazım yolu tek toplu ifadeye indirilirken taşınması gereken iki
// semantik vardı ve ikisi de sessizce bozulabilirdi:
//
//	started_at KORUNUR — "süregelen anomali TEK satırdır" sözleşmesinin
//	  tamamı. Tazelenirse /anomalies'te 4 saattir açık bir olay her
//	  tikte "az önce başladı" der ve P1 triyajı ("open ≥ 4h") çöker.
//	peak_ratio yalnız YÜKSELİR — terfi kapısının (promoteStrongAnomalies
//	  15× eşiği) girdisi. Geri düşerse zirvesi geçmiş bir olay Problem
//	  açmaz.
//
// İkisi de mergeAnomalyCarry'de tek yerde duruyor; bu dosya orayı
// tablo-testliyor.
package chstore

import (
	"testing"
	"time"
)

// TestMergeAnomalyCarry — carry-forward tablosu. HER dal ayrı bir
// üretim davranışı; biri sessizce ters dönerse üstteki iki sistem
// bozulur ve hiçbir ekran bunu söylemez.
func TestMergeAnomalyCarry(t *testing.T) {
	// Sabit anlar — "şimdi"ye bağlı test, gece yarısı kırılan testtir.
	oldStart := time.Unix(1_700_000_000, 0).UTC()
	newStartNs := int64(1_700_090_000) * int64(time.Second)

	cases := []struct {
		name      string
		ev        AnomalyEvent
		prevStart time.Time
		prevPeak  float64
		exists    bool
		wantStart time.Time
		wantPeak  float64
	}{
		{
			name:      "ilk görülme — olayın kendi değerleri",
			ev:        AnomalyEvent{StartedAt: newStartNs, CurrentRatio: 3.0},
			exists:    false,
			wantStart: time.Unix(0, newStartNs),
			wantPeak:  3.0,
		},
		{
			name:      "var olan satır — started_at KORUNUR",
			ev:        AnomalyEvent{StartedAt: newStartNs, CurrentRatio: 3.0},
			prevStart: oldStart, prevPeak: 2.0, exists: true,
			wantStart: oldStart,
			wantPeak:  3.0, // yeni oran daha yüksek → zirve yükselir
		},
		{
			name:      "oran DÜŞTÜ — zirve korunur (monotonluk)",
			ev:        AnomalyEvent{StartedAt: newStartNs, CurrentRatio: 1.2},
			prevStart: oldStart, prevPeak: 9.0, exists: true,
			wantStart: oldStart,
			wantPeak:  9.0,
		},
		{
			name:      "oran EŞİT — zirve değişmez",
			ev:        AnomalyEvent{StartedAt: newStartNs, CurrentRatio: 4.0},
			prevStart: oldStart, prevPeak: 4.0, exists: true,
			wantStart: oldStart,
			wantPeak:  4.0,
		},
		{
			// DÜŞÜŞ adayları (davranış motoru, ratio < 1). Zirve ilk
			// görülen orana takılır ve terfi eşiğini geçmez — bilinçli:
			// bir düşüşü "5× peak" diye raporlamak yalan olurdu.
			name:      "düşüş adayı — zirve 1'in altında kalır",
			ev:        AnomalyEvent{StartedAt: newStartNs, CurrentRatio: 0.4},
			prevStart: oldStart, prevPeak: 0.6, exists: true,
			wantStart: oldStart,
			wantPeak:  0.6,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			gotStart, gotPeak := mergeAnomalyCarry(c.ev, c.prevStart, c.prevPeak, c.exists)
			if !gotStart.Equal(c.wantStart) {
				t.Errorf("started_at = %v, want %v", gotStart, c.wantStart)
			}
			if gotPeak != c.wantPeak {
				t.Errorf("peak_ratio = %v, want %v", gotPeak, c.wantPeak)
			}
		})
	}
}

// TestUniqueAnomalyIDs — IN listesi tekil VE sıra korunur.
//
// Sıra ŞART: bind argümanlarının sırası deterministik değilse aynı
// yazım iki podda iki farklı sorgu METNİ üretir ve query_log'da tek bir
// ifade olarak görünmez — perf ölçümünün dayandığı şey tam olarak o
// gruplama.
func TestUniqueAnomalyIDs(t *testing.T) {
	cases := []struct {
		name string
		in   []AnomalyEvent
		want []string
	}{
		{"boş", nil, []string{}},
		{"tekil", []AnomalyEvent{{ID: "a"}, {ID: "b"}}, []string{"a", "b"}},
		{
			// Aynı tikte aynı fingerprint iki kez aday olabilir; `id IN
			// (x, x)` hem fazladan bind hem de çift satır demek.
			"tekrarlı — ilk görülme sırası korunur",
			[]AnomalyEvent{{ID: "b"}, {ID: "a"}, {ID: "b"}, {ID: "c"}, {ID: "a"}},
			[]string{"b", "a", "c"},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := uniqueAnomalyIDs(c.in)
			if len(got) != len(c.want) {
				t.Fatalf("uniqueAnomalyIDs = %v, want %v", got, c.want)
			}
			for i := range got {
				if got[i] != c.want[i] {
					t.Fatalf("uniqueAnomalyIDs = %v, want %v", got, c.want)
				}
			}
		})
	}
}

// TestUpsertAnomalyEventsEmptyIsNoop — boş dilim CH'ye HİÇ dokunmamalı.
// Aday üretmeyen bir tik query_log'da iz bırakmamalı: hem ucuz hem de
// dürüst. nil conn'la çağrılıyor — bir sorgu denenirse panik/hata olur,
// yani "dokunmadı" gerçekten kanıtlanıyor.
func TestUpsertAnomalyEventsEmptyIsNoop(t *testing.T) {
	s := &Store{} // conn nil
	if err := s.UpsertAnomalyEvents(nil, nil); err != nil {
		t.Errorf("boş dilim hata döndürdü: %v", err)
	}
	if err := s.UpsertAnomalyEvents(nil, []AnomalyEvent{}); err != nil {
		t.Errorf("boş dilim hata döndürdü: %v", err)
	}
}
