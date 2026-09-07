package notify

// v0.10.519 — kural bazında ekip bildirimi: alıcı çözümü (add/only, dedup,
// adressiz ekip), kapılar (ana vida, info ciddiyeti), nil → eski yol.

import (
	"reflect"
	"testing"

	"github.com/cilcenk/coremetry/internal/chstore"
)

func ruleNotifyFixture() (chstore.TeamContacts, *chstore.ServiceMetadata) {
	tc := chstore.TeamContacts{Enabled: true, MinSeverity: "critical", Contacts: map[string]string{
		"ug-mobile": "ug@example.com",
		"sy":        "sy@example.com",
		"DevTeam":   "dev1@example.com, dev2@example.com",
		"shared":    "sy@example.com", // aynı DL — dedup
	}}
	md := &chstore.ServiceMetadata{OwnerTeam: "ug-mobile", SRETeam: "sy"}
	return tc, md
}

func TestResolveRuleTeamRecipients(t *testing.T) {
	tc, md := ruleNotifyFixture()
	cases := []struct {
		name string
		rn   *chstore.RuleNotify
		want []string
	}{
		{"nil → nil", nil, nil},
		{"only: yalnız kural ekipleri", &chstore.RuleNotify{Teams: []string{"devteam"}, Mode: chstore.RuleNotifyModeOnly}, []string{"dev1@example.com", "dev2@example.com"}},
		{"add: sahip+SRE + kural ekipleri, sıra korunur", &chstore.RuleNotify{Teams: []string{"DevTeam"}, Mode: chstore.RuleNotifyModeAdd}, []string{"ug@example.com", "sy@example.com", "dev1@example.com", "dev2@example.com"}},
		{"aynı DL tekrar etmez", &chstore.RuleNotify{Teams: []string{"shared"}, Mode: chstore.RuleNotifyModeAdd}, []string{"ug@example.com", "sy@example.com"}},
		{"adressiz ekip sessizce düşer", &chstore.RuleNotify{Teams: []string{"ghost"}, Mode: chstore.RuleNotifyModeOnly}, nil},
		{"only + katalog yok (db konusu) yine gider", &chstore.RuleNotify{Teams: []string{"sy"}, Mode: chstore.RuleNotifyModeOnly}, []string{"sy@example.com"}},
	}
	for _, c := range cases {
		mdArg := md
		if c.name == "only + katalog yok (db konusu) yine gider" {
			mdArg = nil
		}
		got := resolveRuleTeamRecipients(c.rn, mdArg, tc)
		if !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s: got %v want %v", c.name, got, c.want)
		}
	}
}

func TestTeamMailReachRule(t *testing.T) {
	tc, md := ruleNotifyFixture()
	rn := &chstore.RuleNotify{Teams: []string{"DevTeam"}, Mode: chstore.RuleNotifyModeOnly}
	// minSeverity=critical ama kural hedefi warning'de de gider (eşik atlanır).
	if to, out := teamMailReachRule(tc, md, "warning", rn); out != teamMailSent || len(to) != 2 {
		t.Errorf("warning + kural hedefi: %v %v", to, out)
	}
	// info → sel koruması: mail yok, "vida/ciddiyet dışı" hâli.
	if to, out := teamMailReachRule(tc, md, "info", rn); out != teamMailOff || to != nil {
		t.Errorf("info: %v %v", to, out)
	}
	// Ana vida kapalı → kural hedefi de gitmez.
	off := tc
	off.Enabled = false
	if _, out := teamMailReachRule(off, md, "critical", rn); out != teamMailOff {
		t.Errorf("vida kapalı: %v", out)
	}
	// Adressiz hedef → denendi, kimse yok (kusur görünür kalır).
	if _, out := teamMailReachRule(tc, md, "critical", &chstore.RuleNotify{Teams: []string{"ghost"}, Mode: chstore.RuleNotifyModeOnly}); out != teamMailNoRecipients {
		t.Errorf("adressiz: %v", out)
	}
	// nil / ekipsiz → eski yol birebir (minSeverity=critical, warning dışlanır).
	if _, out := teamMailReachRule(tc, md, "warning", nil); out != teamMailOff {
		t.Errorf("nil hedef eski yolu izlemeli: %v", out)
	}
	if to, out := teamMailReachRule(tc, md, "critical", &chstore.RuleNotify{}); out != teamMailSent || len(to) != 2 {
		t.Errorf("ekipsiz hedef eski yol: %v %v", to, out)
	}
}
