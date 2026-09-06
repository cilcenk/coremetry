package logstore

import "testing"

// v0.10.509 — log arama denetimi C5: "hatalıyı ayıran alan" loglarda.
// Seçim (hata logları) ile tabanın (aynı süzgeç, tüm seviyeler) payları;
// lift = selPct − basePct (puan), büyükten küçüğe.
func TestLiftFieldStats(t *testing.T) {
	sel := &FieldStatsResult{Field: "pod", Total: 100, Values: []FieldValueCount{
		{Value: "a", Count: 80}, {Value: "b", Count: 15}, {Value: "c", Count: 5},
	}}
	base := &FieldStatsResult{Field: "pod", Total: 1000, Values: []FieldValueCount{
		{Value: "a", Count: 200}, {Value: "b", Count: 600},
	}}
	out := LiftFieldStats(sel, base)
	if out.Values[0].Value != "a" || out.Values[0].Lift != 60 || out.Values[0].SelPct != 80 || out.Values[0].BasePct != 20 {
		t.Fatalf("a: %+v (want lift 60 = 80 − 20)", out.Values[0])
	}
	if out.Values[1].Value != "c" || out.Values[1].BasePct != 0 || out.Values[1].Lift != 5 {
		t.Fatalf("c (taban top-N dışı → basePct 0): %+v", out.Values[1])
	}
	if out.Values[2].Value != "b" || out.Values[2].Lift != 15-60 {
		t.Fatalf("b (hatada az, tabanda çok → negatif): %+v", out.Values[2])
	}
	if sel.Values[0].Lift != 0 {
		t.Fatal("girdi mutasyona uğramamalı")
	}
	t.Run("boş taban / nil seçim", func(t *testing.T) {
		if LiftFieldStats(nil, base) != nil {
			t.Fatal("nil seçim nil")
		}
		out := LiftFieldStats(sel, nil)
		if out.Values[0].BasePct != 0 || out.Values[0].Lift != out.Values[0].SelPct {
			t.Fatalf("tabansız: lift = selPct: %+v", out.Values[0])
		}
	})
}
