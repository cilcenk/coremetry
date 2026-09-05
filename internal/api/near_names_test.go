package api

import (
	"strings"
	"testing"
)

// near_names_test.go — v0.10.429 (CoSRE router boşlukları D1): "hangisini
// kastettin?" — aday üretimi, router'ın sor/çöz kararı, çiplerin
// deterministik gidiş-dönüşü, takım kodu toleransı, sınıflandırıcı takım slotu.

func TestNearNames(t *testing.T) {
	live := []string{"bsa-login-external-prod", "bsa-login-internal-prod", "checkout-service", "payment-service", "mobile-commercial-bff-prod", "inventory"}
	cases := map[string]struct {
		q    string
		want []string
	}{
		"tam eş":                   {"checkout-service", []string{"checkout-service"}},
		"önek":                     {"checkout", []string{"checkout-service"}},
		"jeton kapsaması (boşluk)": {"login external", []string{"bsa-login-external-prod"}},
		"kısmi jeton → iki aday":   {"login", []string{"bsa-login-external-prod", "bsa-login-internal-prod"}},
		"yazım hatası":             {"chekout-service", []string{"checkout-service"}},
		"çok kısa":                 {"ap", nil},
		"katalogda yok":            {"zzqx", nil},
		"üç parça":                 {"mobile commercial bff", []string{"mobile-commercial-bff-prod"}},
	}
	for name, c := range cases {
		got := nearNames(c.q, live, 8)
		if strings.Join(got, ",") != strings.Join(c.want, ",") {
			t.Errorf("%s: %q → %v, want %v", name, c.q, got, c.want)
		}
	}
	if got := nearNames("login", live, 1); len(got) != 1 {
		t.Fatalf("tavan: %v", got)
	}
}

func TestServiceCandidates(t *testing.T) {
	services := []string{"bsa-login-external-prod", "bsa-login-internal-prod", "checkout-service", "mobile-commercial-bff-prod"}
	envs := []string{"prod", "uat"}
	if got := serviceCandidates("login external servisinde hata var mı", services, envs, 8); strings.Join(got, ",") != "bsa-login-external-prod" {
		t.Fatalf("tek aday çözülmeli: %v", got)
	}
	if got := serviceCandidates("login servisinde hata var mı", services, envs, 8); len(got) != 2 {
		t.Fatalf("iki aday sorulmalı: %v", got)
	}
	if got := serviceCandidates("bugün hava nasıl", services, envs, 8); got != nil {
		t.Fatalf("ad-şekilli jeton yok → nil: %v", got)
	}
	if got := serviceCandidates("prod ortamında hata var mı", services, envs, 8); got != nil {
		t.Fatalf("env jetonu aday üretmez: %v", got)
	}
}

// Router: belirsiz ad → ask_service (adaylar + sorulan niyet); tek bulanık ad
// → doğrudan o servis; adsız/uydurma → eski davranış.
func TestRouteGuidedIntentAsksOnAmbiguousService(t *testing.T) {
	services := []string{"bsa-login-external-prod", "bsa-login-internal-prod", "checkout-service"}
	envs := []string{"prod"}
	// Sağlık/hata şekli + parça adı: aile rotası ÖNCE (v0.9.192 — iki servis
	// yan yana); sorma yalnız ailenin bakmadığı şekillerde (neden/yavaş/deploy/log).
	r := routeGuidedIntent("login servisinde hata var mı", services, envs, nil, "")
	if r.Intent != guidedFamilyHealth || len(r.Family) != 2 {
		t.Fatalf("aile rotası korunmalı: %+v", r)
	}
	r = routeGuidedIntent("login neden yavaş", services, envs, nil, "")
	if r.Intent != guidedAskService || r.AskIntent != guidedRootCause || len(r.ServiceOptions) != 2 {
		t.Fatalf("belirsiz ad sormalı: %+v", r)
	}
	r = routeGuidedIntent("login external neden yavaş", services, envs, nil, "")
	if r.Intent != guidedRootCause || r.Service != "bsa-login-external-prod" {
		t.Fatalf("tek bulanık ad çözülmeli: %+v", r)
	}
	r = routeGuidedIntent("bugün hava nasıl", services, envs, nil, "")
	if r.Intent != guidedNone {
		t.Fatalf("konu dışı none kalmalı: %+v", r)
	}
	// Servis sinyali yoksa aday aranmaz ("login" tek başına bir soru değil).
	r = routeGuidedIntent("login", services, envs, nil, "")
	if r.Intent == guidedAskService {
		t.Fatalf("sinyalsiz mesaj sormamalı: %+v", r)
	}
}

// Çipler tam kılavuz cümle: router her birini AYNI niyete ve servise çözer
// (çıplak ad kapıdan geçmez — çipe tıklamak serbest döngüye düşmez).
func TestAskServiceChipsRoundTrip(t *testing.T) {
	services := []string{"checkout-service", "payment-service"}
	for _, intent := range []guidedIntent{guidedServiceHealth, guidedRootCause, guidedSlowTraces, guidedDeployImpact, guidedLogErrors, guidedPodHealth, guidedDBHealth, guidedMessagingHealth, guidedProblems} {
		chip := askServiceChip(intent, "checkout-service")
		r := routeGuidedIntent(chip, services, nil, nil, "")
		if r.Intent != intent || r.Service != "checkout-service" {
			t.Errorf("çip %q → %s/%s, want %s/checkout-service", chip, r.Intent, r.Service, intent)
		}
	}
	route := guidedRoute{Intent: guidedAskService, AskIntent: guidedLogErrors, ServiceOptions: []string{"checkout-service", "payment-service"}}
	chips := guidedSuggestions(route)
	if len(chips) != 2 || chips[0] != "checkout-service hata logları?" {
		t.Fatalf("çipler: %v", chips)
	}
}

func TestTeamCodeTolerance(t *testing.T) {
	teams := []string{"SY-XYZ", "UG", "Avengersy"}
	if got := extractTeamEntity("sy xyz takımının servisleri", teams); got != "SY-XYZ" {
		t.Fatalf("boşluklu kod: %q", got)
	}
	if got := extractTeamEntity("SY-XYZ'e ait servisleri listele", teams); got != "SY-XYZ" {
		t.Fatalf("tireli kod: %q", got)
	}
	if got := extractTeamEntity("avengersy-legacy nasıl", teams); got != "" {
		t.Fatalf("tek jetonlu ad tireli uzun adın içinde eşleşmemeli: %q", got)
	}
	if got := matchLiveTeam("sy-xyz", teams); got != "SY-XYZ" {
		t.Fatalf("matchLiveTeam katlanmış eş: %q", got)
	}
	if got := matchLiveTeam("ZZ", teams); got != "" {
		t.Fatalf("katalogda yok: %q", got)
	}
	if !mayNameTeam("sy-xyz takımına ait servisleri listeler misin") {
		t.Fatal("takım kökü taşıyan 6 jetonlu cümle kapıdan geçmeli")
	}
	if mayNameTeam("bu uzun cümle hiçbir sinyal taşımıyor ama altı jeton var evet") {
		t.Fatal("sinyalsiz uzun mesaj kapıdan geçmemeli")
	}
	r := routeGuidedIntent("SY-XYZ takımına ait servisleri listele", []string{"checkout-service"}, nil, teams, "")
	if r.Intent != guidedTeamServices || r.Team != "SY-XYZ" {
		t.Fatalf("takım kodu rotası: %+v", r)
	}
}

// Sınıflandırıcı: team slotu canlı katalogla; uydurma takım none.
func TestParseIntentTeamServices(t *testing.T) {
	teams := []string{"SY-XYZ", "UG"}
	r, _, ok := parseIntentJSON(`{"intent":"team_services","team":"sy-xyz"}`, nil, nil, teams, "")
	if !ok || r.Intent != guidedTeamServices || r.Team != "SY-XYZ" {
		t.Fatalf("takım rotası: ok=%v %+v", ok, r)
	}
	if _, _, ok := parseIntentJSON(`{"intent":"team_services","team":"ghost"}`, nil, nil, teams, ""); ok {
		t.Fatal("uydurma takım none olmalı")
	}
	// Yaklaşık servis adı → ask_service, adaylar canlı katalogdan.
	r, _, ok = parseIntentJSON(`{"intent":"service_health","service":"login external"}`, []string{"bsa-login-external-prod", "checkout-service"}, nil, nil, "")
	if !ok || r.Intent != guidedAskService || r.AskIntent != guidedServiceHealth || len(r.ServiceOptions) != 1 {
		t.Fatalf("yaklaşık ad: ok=%v %+v", ok, r)
	}
}
