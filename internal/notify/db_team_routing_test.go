package notify

import (
	"testing"

	"github.com/cilcenk/coremetry/internal/chstore"
)

// db_team_routing_test.go — v0.9.1345.
//
// v0.9.1344 kusuru ÖLÇTÜ ama düzeltmedi: db-konulu bir problemin
// (`db:oracle@corebank-scan.prod`) katalog satırı yok → md nil → ekip yok
// → mail yok. Operatörün cümlesiyle: "Oracle doluyor ve kimse haber
// almıyor."
//
// Bu testler ekip-yönlendirmenin alıcı KAYNAĞINI pinliyor. Üç şart:
//
//  1. Operatörün katalog satırı VARSA türetim onu EZMEZ.
//  2. Çözülemeyen her hâlde nil — yani "kimseye gitmedi" işareti
//     ULAŞILABİLİR kalır (aşağıdaki wiring testi bunu zincirle gösterir).
//  3. Türetime yalnız gerektiğinde girilir (gereksiz CH okuması yok).

func TestNeedsDerivedTeam(t *testing.T) {
	mdRow := &chstore.ServiceMetadata{Service: "account-service", OwnerTeam: "core-banking"}

	tests := []struct {
		name string
		p    chstore.Problem
		md   *chstore.ServiceMetadata
		want bool
	}{
		{"db konusu + katalog satırı YOK → türet",
			chstore.Problem{Service: "db:oracle@corebank-scan.prod", Kind: chstore.ProblemKindDB},
			nil, true},
		{"db konusu ama katalog satırı VAR → türetme (operatör beyanı kazanır)",
			chstore.Problem{Service: "db:oracle@corebank-scan.prod", Kind: chstore.ProblemKindDB},
			mdRow, false},
		{"servis konusu + satır yok → türetme (bugünkü dal)",
			chstore.Problem{Service: "account-service", Kind: chstore.ProblemKindService},
			nil, false},
		{"boş kind = servis (eski satırlar) → türetme",
			chstore.Problem{Service: "account-service", Kind: ""},
			nil, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := needsDerivedTeam(tc.p, tc.md); got != tc.want {
				t.Fatalf("needsDerivedTeam = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestDerivedTeamMetadata(t *testing.T) {
	operatorRow := &chstore.ServiceMetadata{
		Service: "elle-girilmis", OwnerTeam: "operator-team", SRETeam: "operator-sre",
	}
	own := chstore.DBOwner{
		Caller: "account-service", Calls: 49898,
		OwnerTeam: "core-banking", SRETeam: "core-platform-sre",
	}

	t.Run("katalog satırı varsa türetim onu EZMEZ", func(t *testing.T) {
		got := derivedTeamMetadata(operatorRow, own, true)
		if got != operatorRow {
			t.Fatalf("operatörün satırı korunmalı, %+v döndü — türetim yalnız "+
				"BOŞLUĞU doldurur, beyanı değiştirmez", got)
		}
	})

	t.Run("çözülemedi → nil (işaret ULAŞILABİLİR kalır)", func(t *testing.T) {
		if got := derivedTeamMetadata(nil, chstore.DBOwner{}, false); got != nil {
			t.Fatalf("çözülemeyen hâlde nil dönmeli, %+v döndü — dolu bir satır "+
				"'kimseye gitmedi' işaretini ULAŞILAMAZ yapar ve v0.9.1344'ün "+
				"ölçtüğü kusur görünmez olur", got)
		}
	})

	t.Run("çözüldü → çağıranın takımları taşınır", func(t *testing.T) {
		got := derivedTeamMetadata(nil, own, true)
		if got == nil {
			t.Fatal("çözülmüş sahiplik nil döndü")
		}
		if got.OwnerTeam != "core-banking" || got.SRETeam != "core-platform-sre" {
			t.Fatalf("takımlar taşınmadı: %+v", got)
		}
		if got.Service != "account-service" {
			t.Errorf("Service = %q, want çağıranın adı (kanıt)", got.Service)
		}
	})
}

// TestDerivedTeamReachesRecipients — ZİNCİR: türetilmiş satır gerçekten
// alıcıya dönüşüyor mu, ve çözülemeyen hâl gerçekten kusur olarak mı
// raporlanıyor.
//
// İki yarım ayrı ayrı doğru olup zincir yine kopabilir (v0.9.1344'ün
// kendisi tam olarak buydu: sendTeamMail çalışıyordu ama sonucu kimse
// okumuyordu). Bu yüzden test teamMailReach'e KADAR gidiyor.
func TestDerivedTeamReachesRecipients(t *testing.T) {
	tc := chstore.TeamContacts{
		Enabled: true,
		Contacts: map[string]string{
			"core-banking":      "cb@bank.example",
			"core-platform-sre": "sre@bank.example",
		},
	}
	own := chstore.DBOwner{
		Caller: "account-service", Calls: 49898,
		OwnerTeam: "core-banking", SRETeam: "core-platform-sre",
	}

	t.Run("çözülmüş db sahipliği ADRESE ulaşır", func(t *testing.T) {
		md := derivedTeamMetadata(nil, own, true)
		to, outcome := teamMailReach(tc, md, "critical")
		if outcome != teamMailSent {
			t.Fatalf("outcome = %v, want teamMailSent — türetilmiş sahiplik "+
				"alıcıya dönüşmüyorsa özellik hiç çalışmıyor demektir", outcome)
		}
		if len(to) != 2 {
			t.Fatalf("alıcılar = %v, want iki adres (owner + sre)", to)
		}
	})

	// ⚠️ v0.9.1344'ün TESTİ hâlâ geçerli olmalı: GERÇEKTEN yönlendirilemez
	// bir problem hâlâ kusur olarak raporlanmalı. Bu dilim o kovayı
	// daraltıyor, KAPATMIYOR.
	t.Run("çözülemeyen db konusu HÂLÂ teamMailNoRecipients", func(t *testing.T) {
		md := derivedTeamMetadata(nil, chstore.DBOwner{}, false)
		to, outcome := teamMailReach(tc, md, "critical")
		if outcome != teamMailNoRecipients {
			t.Fatalf("outcome = %v, want teamMailNoRecipients — çözülemeyen "+
				"sahiplik kusur olarak görünmezse operatör 'Oracle doluyor ve "+
				"kimse haber almıyor' hâlini bir daha ASLA göremez", outcome)
		}
		if to != nil {
			t.Fatalf("alıcılar = %v, want nil", to)
		}
	})

	// ...ve o hâl gerçekten routingUnmatched'e varmalı (kanal da yoksa).
	t.Run("çözülemeyen db konusu routingUnmatched üretir", func(t *testing.T) {
		v := decideRouting(routingFacts{ChannelsOffered: 2, Team: teamMailNoRecipients})
		if v != routingUnmatched {
			t.Fatalf("verdict = %v, want routingUnmatched", v)
		}
	})

	// Çözülen hâl ise artık o kovada DEĞİL — özelliğin asıl amacı.
	t.Run("çözülen db konusu artık unmatched DEĞİL", func(t *testing.T) {
		v := decideRouting(routingFacts{ChannelsOffered: 0, Team: teamMailSent})
		if v != routingDelivered {
			t.Fatalf("verdict = %v, want routingDelivered", v)
		}
	})
}
