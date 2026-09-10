package chstore

// poller_owned_subject_test.go — v0.10.605: poller-sahipli kuraldan özne
// çıkarımı. Süpürücünün canlılık sorusu bu özneyle sorulur; yanlış çıkarım
// = ya ölü kaynağın Problem'i sonsuza dek açık (muafiyet kalır) ya da canlı
// kaynağınki her tikte "source silent" (flapping).

import "testing"

func TestPollerOwnedSubject(t *testing.T) {
	cases := []struct {
		rule string
		want string
		ok   bool
	}{
		{RuleExtDownPrefix + "ext:prod-eu", "ext:prod-eu", true},
		{RuleExtCapPrefix + "ext:prod-eu:ext:tfail_adet", "ext:prod-eu", true}, // metrik iki nokta taşır
		{RuleExtCapPrefix + "ext:prod-eu:cnt", "ext:prod-eu", true},
		{RuleExtCapPrefix + "ext:prod-eu", "ext:prod-eu", true}, // metriksiz (savunma)
		{RuleExtDownPrefix, "", false},
		{RuleExtCapPrefix + ":x", "", false},
		{"anomaly:ext:prod-eu/OP1:ext:tfail_adet", "", false}, // seri Problem'i — sahipli değil
		{"anomaly:shop:p99_ms", "", false},
		{"", "", false},
	}
	for _, c := range cases {
		got, ok := PollerOwnedSubject(c.rule)
		if got != c.want || ok != c.ok {
			t.Errorf("%q → (%q,%v), want (%q,%v)", c.rule, got, ok, c.want, c.ok)
		}
		if ok != PollerOwnedRule(c.rule) && c.rule != RuleExtDownPrefix && c.rule != RuleExtCapPrefix+":x" {
			t.Errorf("%q: PollerOwnedRule ile tutarsız", c.rule)
		}
	}
}
