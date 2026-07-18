
// v0.9.64 (review MAJÖR) — prior pencere sınırı: floor5(winStart)
// bucket'ı HER İKİ pencereye tam giriyordu (başlangıç aşağı yuvarlanır
// + son dahil). priorEnd = floor5(ws)-1s önceki bucket'ta bitmeli;
// round2 payload disiplinini de pinler.
func TestComparedPriorBoundaryAndRound2(t *testing.T) {
	ws := time.Date(2026, 7, 18, 10, 4, 30, 0, time.UTC)
	priorEnd := ws.Truncate(5 * time.Minute).Add(-time.Second)
	want := time.Date(2026, 7, 18, 9, 59, 59, 0, time.UTC)
	if !priorEnd.Equal(want) {
		t.Fatalf("priorEnd=%v, want %v", priorEnd, want)
	}
	// floor5(priorEnd) = 09:55 → floor5(ws)=10:00 bucket'ı prior'a giremez.
	if pe := priorEnd.Truncate(5 * time.Minute); !pe.Before(ws.Truncate(5 * time.Minute)) {
		t.Fatalf("prior son bucket'ı current'ın ilk bucket'ıyla çakışıyor: %v", pe)
	}
	for in, want := range map[float64]float64{12.3456: 12.35, 0.004: 0, 199.999: 200} {
		if got := round2(in); got != want {
			t.Errorf("round2(%v)=%v, want %v", in, got, want)
		}
	}
}
