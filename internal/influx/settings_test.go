package influx

import (
	"strings"
	"testing"
)

// settings_test.go — v0.10.222 (Influx D1, audit §2 settings.go).
//
// Normalize sözleşmesi (PUT + test-connection aynı kapıdan geçer):
//   • Ad zorunlu + tekil (service_name olur); URL http(s) + org zorunlu
//     (etkinken); tokenRef biçimi ValidTokenRef; aralık varsayılan 30 s,
//     [10, 3600] kelepçe; sorgu adı slug (^[a-z0-9_]{1,40}$) + tekil;
//     etkin kaynakta ≥1 sorgu, her sorguda flux + groupBy zorunlu.
//   • ID SUNUCU sahipli: id ile, yoksa ad ile önceki kayıttan taşınır;
//     yeni kayıt newID() alır. Bir rename id'yi (ve ileride durumunu)
//     korur — thanos ClusterConfig.ID sözleşmesi.
//   • Girdi DEĞİŞTİRİLMEZ (kopya döner).

func validSource() SourceConfig {
	return SourceConfig{
		Name: "ggfail", URL: "https://influx.example:8086", Org: "bank",
		TokenRef: "env:COREMETRY_INFLUX_TOKEN_GG", Enabled: true,
		Queries: []QueryConfig{{
			Name:    "tfail_adet",
			Flux:    `from(bucket: "GGFailTraceBckt") |> range(start: -2m) |> sum()`,
			GroupBy: []string{"OPERATIONCODE", "ERRORCODE"},
			AttrMap: map[string]string{"OPERATIONCODE": "operation", "ERRORCODE": "error.code"},
		}},
	}
}

func TestNormalize_DefaultsAndIDs(t *testing.T) {
	ids := []string{"i-aaaaaaaa", "i-bbbbbbbb"}
	newID := func() string { id := ids[0]; ids = ids[1:]; return id }

	in := Settings{Sources: []SourceConfig{validSource()}}
	got, err := Normalize(in, Settings{}, newID)
	if err != nil {
		t.Fatalf("normalize: %v", err)
	}
	s := got.Sources[0]
	if s.ID != "i-aaaaaaaa" {
		t.Fatalf("new source gets newID(); got %q", s.ID)
	}
	if s.IntervalSec != DefaultIntervalSec {
		t.Fatalf("interval default %d; got %d", DefaultIntervalSec, s.IntervalSec)
	}
	if in.Sources[0].ID != "" || in.Sources[0].IntervalSec != 0 {
		t.Fatalf("input must not be mutated: %+v", in.Sources[0])
	}

	// Rename by id keeps the id; a same-name re-PUT without id also keeps it.
	// (Dilimi KOPYALA: Settings değer kopyası backing array'i paylaşır —
	// `got`u yerinde değiştirmek "ad ile yeniden bağlama" vakasını bozar.)
	renamed := Settings{Sources: append([]SourceConfig(nil), got.Sources...)}
	renamed.Sources[0].Name = "ggfail-prod"
	got2, err := Normalize(renamed, got, newID)
	if err != nil || got2.Sources[0].ID != "i-aaaaaaaa" {
		t.Fatalf("rename by id must keep id; got %+v / %v", got2.Sources[0].ID, err)
	}
	noID := Settings{Sources: []SourceConfig{validSource()}}
	got3, err := Normalize(noID, got, newID)
	if err != nil || got3.Sources[0].ID != "i-aaaaaaaa" {
		t.Fatalf("same name without id must reattach the stored id; got %q / %v", got3.Sources[0].ID, err)
	}
}

func TestNormalize_Rejects(t *testing.T) {
	mut := func(f func(s *SourceConfig)) Settings {
		s := validSource()
		f(&s)
		return Settings{Sources: []SourceConfig{s}}
	}
	cases := []struct {
		name string
		in   Settings
		want string
	}{
		{"empty name", mut(func(s *SourceConfig) { s.Name = "  " }), "ad"},
		{"bad url scheme", mut(func(s *SourceConfig) { s.URL = "influx.example:8086" }), "url"},
		{"missing org", mut(func(s *SourceConfig) { s.Org = "" }), "org"},
		{"plain token instead of ref", mut(func(s *SourceConfig) { s.TokenRef = "my-secret-token" }), "tokenRef"},
		{"interval below floor", mut(func(s *SourceConfig) { s.IntervalSec = 5 }), "aralık"},
		{"interval above ceiling", mut(func(s *SourceConfig) { s.IntervalSec = 4000 }), "aralık"},
		{"no queries when enabled", mut(func(s *SourceConfig) { s.Queries = nil }), "sorgu"},
		{"query name not a slug", mut(func(s *SourceConfig) { s.Queries[0].Name = "TFAIL adet" }), "sorgu adı"},
		{"query without flux", mut(func(s *SourceConfig) { s.Queries[0].Flux = "" }), "flux"},
		{"query without groupBy", mut(func(s *SourceConfig) { s.Queries[0].GroupBy = nil }), "groupBy"},
		{"duplicate source names", Settings{Sources: []SourceConfig{validSource(), validSource()}}, "tekil"},
		{"duplicate query names", mut(func(s *SourceConfig) { s.Queries = append(s.Queries, s.Queries[0]) }), "tekil"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := Normalize(c.in, Settings{}, func() string { return "i-00000000" })
			if err == nil || !strings.Contains(strings.ToLower(err.Error()), strings.ToLower(c.want)) {
				t.Fatalf("want error mentioning %q, got %v", c.want, err)
			}
		})
	}
}

func TestNormalize_TokenStoredAndPreserved(t *testing.T) {
	// v0.10.224 (operatör): düz token SAKLANIR; boş girdi saklıyı korur;
	// referans varsa o kazanır; ikisi de yoksa etkin kaynak reddedilir.
	newID := func() string { return "i-aaaaaaaa" }
	first := validSource()
	first.TokenRef, first.Token = "", "plain-secret"
	got, err := Normalize(Settings{Sources: []SourceConfig{first}}, Settings{}, newID)
	if err != nil || got.Sources[0].Token != "plain-secret" {
		t.Fatalf("plain token must be stored: %+v / %v", got, err)
	}
	// İkinci PUT token'sız (form maskeli değeri geri alamaz) → saklı korunur.
	again := validSource()
	again.TokenRef, again.Token = "", ""
	got2, err := Normalize(Settings{Sources: []SourceConfig{again}}, got, newID)
	if err != nil || got2.Sources[0].Token != "plain-secret" {
		t.Fatalf("empty token must preserve the stored one: %q / %v", got2.Sources[0].Token, err)
	}
	// Yeni token eskisini değiştirir.
	again.Token = "rotated"
	got3, _ := Normalize(Settings{Sources: []SourceConfig{again}}, got, newID)
	if got3.Sources[0].Token != "rotated" {
		t.Fatalf("new token replaces: %q", got3.Sources[0].Token)
	}
	// Ne token ne referans → etkin kaynak reddedilir.
	none := validSource()
	none.TokenRef, none.Token = "", ""
	if _, err := Normalize(Settings{Sources: []SourceConfig{none}}, Settings{}, newID); err == nil || !strings.Contains(err.Error(), "token") {
		t.Fatalf("enabled source without any token must be rejected, got %v", err)
	}
}

func TestSnapshot_MasksStoredToken(t *testing.T) {
	svc := New()
	a := validSource()
	a.ID, a.TokenRef, a.Token = "i-aaaaaaaa", "", "plain-secret"
	svc.Configure(Settings{Sources: []SourceConfig{a}})
	snap := svc.Snapshot()
	if snap.Sources[0].Token != "" || !snap.Sources[0].HasToken || !snap.Sources[0].TokenResolved {
		t.Fatalf("stored token must be masked but reported: %+v", snap.Sources[0])
	}
	tok, err := svc.tokenFor(a)
	if err != nil || tok != "plain-secret" {
		t.Fatalf("tokenFor falls back to the stored token: %q / %v", tok, err)
	}
	b := a
	b.TokenRef = "env:NOPE"
	if _, err := svc.tokenFor(b); err == nil {
		t.Fatalf("a set tokenRef wins over the stored token, even when unresolvable")
	}
}

func TestNormalize_DisabledSourceIsLenient(t *testing.T) {
	// Kapalı kaynak URL/org/sorgu taşımak zorunda değil — operatör
	// yarım bırakıp sonra dönebilmeli (VM "disable keeps url" duruşu).
	s := SourceConfig{Name: "draft", Enabled: false}
	got, err := Normalize(Settings{Sources: []SourceConfig{s}}, Settings{}, func() string { return "i-11111111" })
	if err != nil || len(got.Sources) != 1 || got.Sources[0].ID != "i-11111111" {
		t.Fatalf("disabled draft must be accepted; got %+v / %v", got, err)
	}
}

func TestSnapshot_TokenResolvedFlag(t *testing.T) {
	svc := New()
	svc.getenv = func(k string) string {
		if k == "COREMETRY_INFLUX_TOKEN_GG" {
			return "tok"
		}
		return ""
	}
	a := validSource()
	a.ID = "i-aaaaaaaa"
	b := validSource()
	b.ID, b.Name, b.TokenRef = "i-bbbbbbbb", "other", "env:NOPE"
	svc.Configure(Settings{Sources: []SourceConfig{a, b}})
	snap := svc.Snapshot()
	if len(snap.Sources) != 2 {
		t.Fatalf("want 2, got %d", len(snap.Sources))
	}
	if !snap.Sources[0].TokenResolved || snap.Sources[1].TokenResolved {
		t.Fatalf("tokenResolved must reflect resolvability: %+v", snap.Sources)
	}
	if snap.Sources[0].TokenRef != "env:COREMETRY_INFLUX_TOKEN_GG" {
		t.Fatalf("tokenRef is a REFERENCE, shown as-is: %q", snap.Sources[0].TokenRef)
	}
	if !svc.HasEnabledSources() {
		t.Fatalf("HasEnabledSources")
	}
}
