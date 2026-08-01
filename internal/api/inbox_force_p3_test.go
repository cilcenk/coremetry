package api

import (
	"strings"
	"testing"
)

// v0.9.487 (operatör kararı, prod) — "bakmadığın türde yazmasına gerek yok,
// onlar hep P3 olsun, defaultta sadece P1'ler gözüksün": exception dışındaki
// her tür inbox'ta P3'e sabitlenir; exception satırlarının önceliği ve
// halihazırda P3 olan satırların reason'ı DOKUNULMAZ.
func TestForceNonExceptionP3(t *testing.T) {
	cases := []struct {
		name       string
		kind       string
		prio       string
		reason     string
		wantPrio   string
		wantReason string // "" = değişmemeli (girdi reason'ı kalır)
	}{
		{"exception P1 dokunulmaz", "exception", "P1", "3180 occ / 5dk", "P1", ""},
		{"exception P2 dokunulmaz", "exception", "P2", "volume", "P2", ""},
		{"exception P3 dokunulmaz", "exception", "P3", "az occurrence", "P3", ""},
		{"problem P1 → P3", "problem", "P1", "critical + 2x threshold", "P3", "kaynak önceliği P1"},
		{"problem P2 → P3", "problem", "P2", "today", "P3", "kaynak önceliği P2"},
		{"httperror P1 → P3", "httperror", "P1", "849 occ", "P3", "kaynak önceliği P1"},
		{"anomaly P2 → P3", "anomaly", "P2", "spike", "P3", "kaynak önceliği P2"},
		{"incident P1 → P3", "incident", "P1", "declared sev", "P3", "kaynak önceliği P1"},
		// Zaten P3 olan exception-dışı satır: reason'ı ezilmez — zorlama
		// yeni bir bilgi eklemiyor, kaynağın kendi gerekçesi daha değerli.
		{"incident P3 reason korunur", "incident", "P3", "low sev", "P3", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			items := []InboxItem{{Kind: tc.kind, Priority: tc.prio, PriorityReason: tc.reason}}
			forceNonExceptionP3(items)
			if items[0].Priority != tc.wantPrio {
				t.Fatalf("Priority = %q, want %q", items[0].Priority, tc.wantPrio)
			}
			if tc.wantReason == "" {
				if items[0].PriorityReason != tc.reason {
					t.Fatalf("PriorityReason değişti: %q (girdi %q korunmalıydı)", items[0].PriorityReason, tc.reason)
				}
			} else if !strings.Contains(items[0].PriorityReason, tc.wantReason) {
				t.Fatalf("PriorityReason = %q, %q içermeli", items[0].PriorityReason, tc.wantReason)
			}
		})
	}
}
