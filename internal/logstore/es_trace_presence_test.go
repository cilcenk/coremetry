// v0.9.1084 regresyon testleri — hasTrace varlık kararı.
//
// Operator-reported (prod): "Logs sayfasında with trace dediğimde hiç
// log gelmiyor." Kök neden CH-vs-ES ayrışması: pivotlar gövde
// eşleşmesiyle de çalışır, hasTrace yalnız yapısal exists — alan hiç
// yoksa sonuç sessiz boştu. Korunan davranış: yokluk artık
// HasTraceUnapplied ile İTİRAF edilir ve karar kuralı exists
// semantiğine uyar (env kuralının aksine keyword ŞARTI YOK).
package logstore

import "testing"

func TestResolveTracePresenceFromCaps(t *testing.T) {
	cands := traceFieldCandidates("")
	cases := []struct {
		name string
		caps map[string]traceFieldCap
		want bool
	}{
		{
			"keyword alan mevcut → var",
			map[string]traceFieldCap{"trace.id": {Types: []string{"keyword"}}},
			true,
		},
		{
			// exists analize bakmaz — text-only alan da filtre için yeterli.
			// Env kuralı burada false derdi; o fark bu testin varlık sebebi.
			"text-only alan mevcut → YİNE var",
			map[string]traceFieldCap{"trace_id": {Types: []string{"text"}}},
			true,
		},
		{
			"hiçbir aday yok → yok (HasTraceUnapplied)",
			map[string]traceFieldCap{"message": {Types: []string{"text"}}},
			false,
		},
		{"boş caps → yok", map[string]traceFieldCap{}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, got := resolveTracePresenceFromCaps(cands, c.caps)
			if got != c.want {
				t.Errorf("resolveTracePresenceFromCaps = %v, beklenen %v", got, c.want)
			}
		})
	}
}

func TestTraceFieldCandidatesConfiguredFirst(t *testing.T) {
	got := traceFieldCandidates("fields.my_trace")
	if len(got) != 5 || got[0] != "fields.my_trace" {
		t.Errorf("configured alan ilk sırada olmalı: %v", got)
	}
	// Configured, ortak yazımlardan biriyse çiftlenmemeli.
	if got := traceFieldCandidates("trace_id"); len(got) != 4 {
		t.Errorf("çiftlenme var: %v", got)
	}
}
