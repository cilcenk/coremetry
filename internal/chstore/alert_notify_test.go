package chstore

// v0.10.519 — kural bazında ekip bildirimi: codec, normalize, doğrulama,
// kolon-yolu pinleri (target_json emsalinin ikizi).

import (
	"os"
	"strings"
	"testing"
)

func TestRuleNotifyNormalizeValidate(t *testing.T) {
	if NormalizeRuleNotify(nil) != nil || NormalizeRuleNotify(&RuleNotify{}) != nil ||
		NormalizeRuleNotify(&RuleNotify{Teams: []string{"", "  "}}) != nil {
		t.Fatal("boş hedef nil olmalı")
	}
	n := NormalizeRuleNotify(&RuleNotify{Teams: []string{" dijitalsy ", "DijitalSY", "avengers", ""}, Mode: " ONLY "})
	if len(n.Teams) != 2 || n.Teams[0] != "dijitalsy" || n.Teams[1] != "avengers" || n.Mode != RuleNotifyModeOnly {
		t.Fatalf("normalize: %+v", n)
	}
	if m := NormalizeRuleNotify(&RuleNotify{Teams: []string{"x"}}).Mode; m != RuleNotifyModeAdd {
		t.Fatalf("varsayılan kip add olmalı, %q", m)
	}
	if err := ValidateRuleNotify(nil); err != nil {
		t.Fatal(err)
	}
	if err := ValidateRuleNotify(&RuleNotify{Teams: []string{"a"}, Mode: "broadcast"}); err == nil {
		t.Fatal("tanınmayan kip reddedilmeli")
	}
	many := make([]string, RuleNotifyMaxTeams+1)
	for i := range many {
		many[i] = string(rune('a' + i))
	}
	if err := ValidateRuleNotify(&RuleNotify{Teams: many, Mode: RuleNotifyModeAdd}); err == nil {
		t.Fatal("ekip tavanı")
	}
}

func TestRuleNotifyCodecRoundTrip(t *testing.T) {
	if encodeRuleNotify(nil) != "" || encodeRuleNotify(&RuleNotify{}) != "" {
		t.Fatal("nil/boş → ''")
	}
	raw := encodeRuleNotify(&RuleNotify{Teams: []string{"ug-mobile", "sy"}, Mode: ""})
	got := decodeRuleNotify(raw)
	if got == nil || len(got.Teams) != 2 || got.Mode != RuleNotifyModeAdd {
		t.Fatalf("round-trip: %s → %+v", raw, got)
	}
	if decodeRuleNotify("not json") != nil || decodeRuleNotify(`{"teams":[]}`) != nil {
		t.Fatal("bozuk/boş → nil")
	}
}

// Kolon yolu: upsert/list/get notify_json'u target_json ile aynı kapıdan
// geçirir; boot probu var. Kaynak pinleri — çünkü CH'siz koşuyoruz.
func TestRuleNotifyColumnPathPins(t *testing.T) {
	p, err := os.ReadFile("problem.go")
	if err != nil {
		t.Fatal(err)
	}
	src := string(p)
	for _, want := range []string{
		"Notify *RuleNotify",
		"r.Notify != nil && !s.hasAlertRuleNotifyCol.Load() && !s.probeAlertRuleNotifyCol(ctx)",
		"encodeRuleNotify(r.Notify)",
		"s.alertRuleNotifySelect()",
		"r.Notify = decodeRuleNotify(notifyJSON)",
	} {
		if !strings.Contains(src, want) {
			t.Errorf("problem.go: %q yok", want)
		}
	}
	if strings.Count(src, "s.alertRuleNotifySelect()") != 2 || strings.Count(src, "r.Notify = decodeRuleNotify(notifyJSON)") != 2 {
		t.Error("list + get: notify_json ikisinde de okunmalı")
	}
	st, err := os.ReadFile("store.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"hasAlertRuleNotifyCol atomic.Bool",
		"ALTER TABLE alert_rules ADD COLUMN IF NOT EXISTS notify_json String DEFAULT ''",
		"notify_json  String       DEFAULT ''",
		"s.probeAlertRuleNotifyCol(ctx)",
	} {
		if !strings.Contains(string(st), want) {
			t.Errorf("store.go: %q yok", want)
		}
	}
}
