package chstore

import (
	"testing"
	"time"
)

// v0.9.1047 (Faz 0.1) regresyon pini — GetServiceBlastRadius süre yerine
// PENCERE alır. Eski imza `since time.Duration` [now-since, now] kuruyordu;
// kök-neden yolu problemin süresini geçtiği için 3 saat önce çözülmüş 20
// dakikalık bir problemin blast radius'u SON 20 dakikayı okuyordu. Bu tablo
// pencere normalizasyonunu mühürler: verilen pencere AYNEN korunur, now'a
// çapalanmaz; yalnız sıfır/ters girişler güvenli varsayılana düşer.
func TestBlastRadiusWindow(t *testing.T) {
	now := time.Date(2026, 8, 15, 12, 0, 0, 0, time.UTC)
	past20mStart := now.Add(-200 * time.Minute) // 3s20dk önce başladı
	past20mEnd := now.Add(-180 * time.Minute)   // 3s önce çözüldü

	cases := []struct {
		name     string
		from, to time.Time
		wantFrom time.Time
		wantTo   time.Time
	}{
		{
			// Bug'ın kendisi: çözülmüş problemin penceresi AYNEN kalmalı.
			name: "çözülmüş problem penceresi korunur, now'a çapalanmaz",
			from: past20mStart, to: past20mEnd,
			wantFrom: past20mStart, wantTo: past20mEnd,
		},
		{
			name: "açık problem: from geçmiş, to=now",
			from: now.Add(-45 * time.Minute), to: now,
			wantFrom: now.Add(-45 * time.Minute), wantTo: now,
		},
		{
			name: "sıfır to → now; sıfır from → to-1h (eski canlı varsayılan)",
			from: time.Time{}, to: time.Time{},
			wantFrom: now.Add(-time.Hour), wantTo: now,
		},
		{
			name: "ters pencere (from >= to) → to-1h'e düşer",
			from: now, to: now.Add(-time.Hour),
			wantFrom: now.Add(-2 * time.Hour), wantTo: now.Add(-time.Hour),
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gotFrom, gotTo := blastRadiusWindow(tc.from, tc.to, now)
			if !gotFrom.Equal(tc.wantFrom) || !gotTo.Equal(tc.wantTo) {
				t.Fatalf("blastRadiusWindow(%v, %v) = [%v, %v]; want [%v, %v]",
					tc.from, tc.to, gotFrom, gotTo, tc.wantFrom, tc.wantTo)
			}
		})
	}
}
