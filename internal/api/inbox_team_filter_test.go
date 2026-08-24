package api

// inbox_team_filter_test.go — v0.9.1246, operatör istegi:
// "Takımımın exceptionları dediğinde o takım filtreli exceptions
// açabilir."
//
// Dilim iki yarım: /inbox'ın ?team= süzgeci (owner ∪ SRE) ve sohbetin o
// süzgece giden derin linki. Bu dosya ÜÇ korkuyu çiviliyor:
//
//  1. BİRLEŞİM vs KESİŞİM. Sohbetin cevabı takımı birleşim üzerinden
//     sayıyor (servicesForUserTeam → mcptools.TeamServiceNames). Süzgeç
//     owner-only olsaydı link, cevabın SAYDIĞINDAN dar bir sayfa açardı:
//     SRE'si o takım olan servislerin satırları sessizce düşer, iki sayı
//     birbirini tutmazdı.
//
//  2. CACHE ANAHTARI. ?team= satırları değiştiriyor; anahtara girmezse
//     v0.5.187'nin çapraz-zehirlenmesi (bu kez takım süzgeciyle) —
//     filtreli sayfa filtresiz isteğe servis edilir. Anahtar ayrıca
//     KATLANMIŞ ad taşımalı: "sy" ile "SY" aynı satırları döndürür, iki
//     ayrı girdi olurlarsa aynı görünüm 15sn boyunca iki farklı yaşta
//     veri gösterir.
//
//  3. K4 ÖLÜ-PARAM EŞLEŞMESİ (v0.9.1130 sınıfı). Link, hedef sayfanın
//     OKUDUĞU paramı yazmalı. Bu yüzden sayfa okuması ÖNCE geldi, köprü
//     sonra — ve test ikisini BİRLİKTE tutuyor: param adı bir tarafta
//     değişirse burası kırmızı yanar, prod'da ise link sessizce
//     filtresiz kuyruğu açardı.
//
// KISA KOD VARSAYIMI: gerçek takım adları "SY"/"UG" gibi 2 harfli
// olabilir (operatör, 2026-08-22). Uzunluk/biçim varsayımı hiçbir
// katmanda yok; tablo bunu her katmanda geziyor.

import (
	"os"
	"strings"
	"testing"

	"github.com/cilcenk/coremetry/internal/chstore"
)

func TestInboxTeamKeepsRow(t *testing.T) {
	// Alias tablosu: LDAP yazımı ile telemetri yazımı aynı takım.
	ta := chstore.TeamAliases{Aliases: map[string]string{"SY-Dijital": "SY"}}

	cases := []struct {
		name             string
		owner, sre, want string
		keep             bool
	}{
		{"boş süzgeç her satırı tutar", "payments", "sre-core", "", true},
		{"owner eşleşmesi", "payments", "sre-core", "payments", true},
		{"SRE eşleşmesi — BİRLEŞİM'in yarısı", "checkout", "payments", "payments", true},
		{"ikisi de değil", "checkout", "sre-core", "payments", false},
		{"takımsız satır düşer", "", "", "payments", false},
		// Harf kasası: link katalog yazımıyla ("SY"), operatör elle
		// küçük harfle ("sy") gelebilir; ikisi AYNI takım.
		{"2 harflik kod — küçük harf istek", "SY", "", "sy", true},
		{"2 harflik kod — büyük harf istek", "sy", "", "SY", true},
		{"2 harflik kod SRE tarafında", "checkout", "ug", "UG", true},
		{"2 harflik kod eşleşmiyor", "SY", "UG", "AB", false},
		// Türkçe katlama (NormTeamName'in iki I tuzağı).
		{"ı/i katlaması", "Bankacılık", "", "BANKACILIK", true},
		// Alias: farklı yazım, aynı takım.
		{"alias hedefe iner", "SY-Dijital", "", "SY", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := inboxTeamKeepsRow(ta, c.owner, c.sre, c.want); got != c.keep {
				t.Errorf("inboxTeamKeepsRow(owner=%q, sre=%q, want=%q) = %v, beklenen %v",
					c.owner, c.sre, c.want, got, c.keep)
			}
		})
	}
}

// matchesTeamFilter (owner+SRE = KESİŞİM) ile inboxTeamKeepsRow
// (?team= = BİRLEŞİM) BİLEREK farklı. İkisini "tutarlılık" adına
// eşitlemek, ya takım süzgecini SRE satırlarına kör bırakır ya da
// owner/sre ikilisini sessizce genişletir.
func TestTeamAxesAreNotInterchangeable(t *testing.T) {
	var ta chstore.TeamAliases
	// Servisin owner'ı checkout-team, SRE'si payments.
	if matchesTeamFilter(ta, "checkout-team", "payments", "payments", "") {
		t.Error("owner ekseni SRE eşleşmesini kabul etti — ?owner= birleşime dönüşmüş")
	}
	if !inboxTeamKeepsRow(ta, "checkout-team", "payments", "payments") {
		t.Error("?team= SRE eşleşmesini eledi — sohbetin saydığı kümeden DAR bir sayfa açılır")
	}
}

func TestInboxListKeyCarriesTeam(t *testing.T) {
	key := func(owner, sre, team string) string {
		return inboxListKey("open", "", "", owner, sre, team, "", 200, "priority", "desc", 0, nil, nil, inboxSubjectService)
	}
	base := key("", "", "")

	t.Run("takım anahtarı değiştirir", func(t *testing.T) {
		if got := key("", "", "payments"); got == base {
			t.Errorf("?team= anahtarı değiştirmedi (%q) — filtreli sayfa filtresiz isteğe servis edilir", got)
		}
	})

	t.Run("iki takım çakışmaz", func(t *testing.T) {
		seen := map[string]string{}
		// 2 harflik kodlar dahil: kısa adlar en çok çakışma riski
		// taşıyan girdiler.
		for _, team := range []string{"", "SY", "UG", "payments", "payment", "sy-dijital"} {
			k := key("", "", team)
			if prev, dup := seen[k]; dup {
				t.Errorf("team=%q ile team=%q aynı anahtara düştü: %q", team, prev, k)
			}
			seen[k] = team
		}
	})

	t.Run("yazım katlanır — sy ile SY tek girdi", func(t *testing.T) {
		for _, spelling := range []string{"SY", "sy", "Sy", "sY"} {
			if got, want := key("", "", spelling), key("", "", "sy"); got != want {
				t.Errorf("team=%q anahtarı %q, beklenen %q — aynı görünüm iki cache girdisi üretiyor",
					spelling, got, want)
			}
		}
		// Türkçe katlama da anahtarda: "BANKACILIK" ile "bankacılık"
		// aynı takım (NormTeamName'in ı→i kuralı).
		if key("", "", "BANKACILIK") != key("", "", "bankacilik") {
			t.Error("Türkçe katlama anahtara yansımıyor")
		}
	})

	t.Run("eksenler birbirinin yerine geçmez", func(t *testing.T) {
		// owner=payments ile team=payments AYNI sorgu değil (kesişim vs
		// birleşim); tek anahtar paylaşırlarsa biri diğerinin cevabını alır.
		if key("payments", "", "") == key("", "", "payments") {
			t.Error("?owner=X ile ?team=X aynı anahtara düşüyor — farklı satır kümeleri, tek girdi")
		}
		if key("", "payments", "") == key("", "", "payments") {
			t.Error("?sre=X ile ?team=X aynı anahtara düşüyor")
		}
	})
}

// K4 EŞLEŞMESİ — köprünün yazdığı param ile sayfanın okuduğu param.
//
// Bu testin varlık sebebi v0.9.1130: guided çipi /logs'a sayfanın HİÇ
// okumadığı bir param yazıyordu, "error logları" vaat edip tüm
// seviyeleri açıyordu ve hiçbir gate ısırmıyordu. Buradaki zincir üç
// dosyaya yayılıyor, o yüzden üçü de aynı anda pinli.
func TestInboxTeamParamPairing(t *testing.T) {
	// 1) Sunucu: handler paramı OKUYOR ve cache anahtarına veriyor.
	inbox := readSrc(t, "inbox.go")
	if !strings.Contains(inbox, `team := strings.TrimSpace(q.Get("team"))`) {
		t.Error("/api/inbox ?team= okumuyor — link filtreli görünüm vaat edip filtresiz kuyruğu açar")
	}
	if !strings.Contains(inbox, "inboxListKey(statusFilter, service, search, ownerTeam, sreTeam, team, env,") {
		t.Error("?team= cache anahtarına girmiyor (v0.5.187 sınıfı çapraz-zehirlenme)")
	}

	// 2) Köprü: link tam olarak o paramı yazıyor.
	link, ok := inboxTeamExceptionsLink("SY")
	if !ok {
		t.Fatal("takım çözülmüşken köprü linki üretilmedi")
	}
	if link.Href != "/inbox?kind=exception&team=SY" {
		t.Errorf("köprü linki %q — beklenen /inbox?kind=exception&team=SY", link.Href)
	}
	if esc, _ := inboxTeamExceptionsLink("SY Dijital"); !strings.Contains(esc.Href, "SY+Dijital") {
		t.Errorf("takım adı escape edilmemiş: %s", esc.Href)
	}
	if _, ok := inboxTeamExceptionsLink("   "); ok {
		t.Error("boş takımda link üretildi — yanlış kapsamlı link linksizlikten kötü")
	}

	// 3) Sayfa: aynı param adını okuyor ve isteğe geçiriyor.
	codec, err := os.ReadFile("../../frontend/src/lib/inboxUrl.ts")
	if err != nil {
		t.Skipf("inboxUrl.ts okunamadı: %v", err)
	}
	if !strings.Contains(string(codec), `INBOX_TEAM_PARAM = 'team'`) {
		t.Error("frontend param adı 'team' değil — köprü ile sayfa ayrışmış")
	}
	page, err := os.ReadFile("../../frontend/src/pages/Inbox.tsx")
	if err != nil {
		t.Skipf("Inbox.tsx okunamadı: %v", err)
	}
	src := string(page)
	if !strings.Contains(src, "readInboxTeam(searchParams)") {
		t.Error("Inbox sayfası ?team='i okumuyor")
	}
	if !strings.Contains(src, "team: teamFilter || undefined") {
		t.Error("okunan takım /api/inbox isteğine geçmiyor — URL'de görünüp hiçbir şeyi daraltmayan bir param")
	}
	// GÖRÜNÜRLÜK: filtre çip olarak basılmalı. Görünmeyen bir daraltma,
	// kısa listeyi "kuyrukta bir şey yok" diye okutur.
	if !strings.Contains(src, "takım: {teamFilter}") || !strings.Contains(src, "setTeamFilter('')") {
		t.Error("takım süzgeci çipi (ya da × temizleme) yok — sessiz daraltma")
	}
}
