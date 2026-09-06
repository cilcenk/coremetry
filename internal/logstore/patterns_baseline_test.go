package logstore

import "testing"

// v0.10.508 — log arama denetimi C6: Desenler'de trend/yeni-mi. Taban =
// önceki eşit pencerenin örneklemesi; birleşim hash ile; tabanda satır
// yoksa "yeni" denmez (yokluk kanıt değil).
func TestJoinPatternBaseline(t *testing.T) {
	cur := &PatternsResult{Groups: []SignatureGroup{
		{Hash: "a", Count: 120}, {Hash: "b", Count: 30}, {Hash: "c", Count: 5},
	}}
	base := &PatternsResult{Sampled: 500, Groups: []SignatureGroup{
		{Hash: "a", Count: 40}, {Hash: "b", Count: 60},
	}}
	JoinPatternBaseline(cur, base)
	if g := cur.Groups[0]; g.PrevCount != 40 || g.Ratio != 3 || g.New {
		t.Fatalf("a: %+v (want prev 40, ratio 3, not new)", g)
	}
	if g := cur.Groups[1]; g.PrevCount != 60 || g.Ratio != 0.5 || g.New {
		t.Fatalf("b: %+v (want prev 60, ratio 0.5)", g)
	}
	if g := cur.Groups[2]; g.PrevCount != 0 || g.Ratio != 0 || !g.New {
		t.Fatalf("c: %+v (want new)", g)
	}

	t.Run("boş taban örneklemesi yeni demez", func(t *testing.T) {
		cur := &PatternsResult{Groups: []SignatureGroup{{Hash: "x", Count: 9}}}
		JoinPatternBaseline(cur, &PatternsResult{Sampled: 0})
		if cur.Groups[0].New || cur.Groups[0].PrevCount != 0 {
			t.Fatalf("yokluk kanıt değil: %+v", cur.Groups[0])
		}
	})
	t.Run("nil taban dokunmaz", func(t *testing.T) {
		cur := &PatternsResult{Groups: []SignatureGroup{{Hash: "x", Count: 9}}}
		JoinPatternBaseline(cur, nil)
		if cur.Groups[0].New || cur.Groups[0].Ratio != 0 {
			t.Fatalf("nil taban alan yazmamalı: %+v", cur.Groups[0])
		}
	})
}
