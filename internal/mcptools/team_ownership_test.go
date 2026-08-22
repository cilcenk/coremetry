package mcptools

// team_ownership_test.go — v0.9.1244. list_teams + get_team_services
// sözleşmesi, discovery/analysis/guided_parity test dosyalarıyla aynı
// dört katmanda:
//
//	(a) katalogda kayıtlı + MinRole "" + her property açıklamalı,
//	(b) pencere/tavan/env semantiği ŞEMADA dürüst (kabul-edilip-yok-sayılan
//	    arg yok; env'in NEDEN olmadığı pinli),
//	(c) dürüstlük zarfları (trimmed / silent / reasons / has_more) SAF
//	    üreticilerde tablo testli,
//	(d) handler'ların o üreticilerden döndüğü + guided'ın AYNI seam'leri
//	    çağırdığı kaynak pini ile bağlı — saf test tek başına BAĞLANMA
//	    kanıtı değildir.
//
// TAŞINAN VAKALAR: TestTeamCatalogueOrderAndDedup ve
// TestSortServicesByErrorRate v0.9.1134'te api/copilot_team_services_test.go
// içinde doğdu; gövde v0.9.1244'te buraya taşınınca testleri de geldi
// (aynı vakalar, aynı beklentiler). api tarafında yalnız router ve kanıt
// METNİ kapıları kaldı.

import (
	"math"
	"os"
	"strings"
	"testing"

	"github.com/cilcenk/coremetry/internal/chstore"
)

var teamToolNames = []string{"list_teams", "get_team_services"}

func TestTeamToolsRegistered(t *testing.T) {
	tools := ToolList(Deps{}) // Deps{} güvenli: ToolList yalnız closure kurar.
	for _, name := range teamToolNames {
		t.Run(name, func(t *testing.T) {
			tool := toolByName(t, tools, name)
			// MinRole AÇIKÇA "": ikisi de salt-okunur ve REST eşleri
			// kapısız — GET /api/services-metadata (api.go:726) ve
			// GET /api/services?ownerTeam=… (api.go:576).
			if tool.MinRole != "" {
				t.Fatalf("%s: MinRole=%q — takım tool'ları viewer tabanında olmalı (REST eşleri kapısız)", name, tool.MinRole)
			}
			if tool.Handler == nil {
				t.Fatalf("%s: Handler nil", name)
			}
			if len(tool.Description) < 120 {
				t.Fatalf("%s: açıklama kontrat değil cümle (%d karakter)", name, len(tool.Description))
			}
			props := schemaProps(t, tool)
			if len(props) == 0 {
				t.Fatalf("%s: hiç property yok", name)
			}
			for pname, p := range props {
				pm, _ := p.(map[string]any)
				if desc, _ := pm["description"].(string); desc == "" {
					t.Fatalf("%s: %q property'sinin açıklaması yok — LLM tahmin eder", name, pname)
				}
			}
			if req, ok := tool.InputSchema["required"]; ok {
				if _, isStr := req.([]string); !isStr {
					t.Fatalf("%s: required[] %T — []string olmalı (v0.9.1050 kanonik şekli)", name, req)
				}
			}
		})
	}
}

// KİMLİK KAPSAMI — bu dilimin en kolay bozulacak kararı. Hiçbir tool
// çağıranın kimliğini okumuyor ("benim servislerim" guided'da kalıyor) ve
// açıklamalar bunu modele SÖYLÜYOR. Bir gelecek düzenleme "my_team" gibi
// bir arg eklerse ya da açıklamadaki uyarıyı silerse burası kırmızı:
// token bir ROL taşır, bir kullanıcı değil — kimliğe bağlanan arg dış MCP
// yolunda sessizce boş dönerdi.
func TestTeamToolsAreNotIdentityAware(t *testing.T) {
	tools := ToolList(Deps{})
	for _, name := range teamToolNames {
		tool := toolByName(t, tools, name)
		for pname := range schemaProps(t, tool) {
			low := strings.ToLower(pname)
			if strings.Contains(low, "my_") || low == "user" || low == "me" {
				t.Errorf("%s: %q arg'ı kimlik ima ediyor — MCP tarafında çağıranın kimliği YOK (cmk_ token = rol)", name, pname)
			}
		}
	}
	if !strings.Contains(toolByName(t, tools, "list_teams").Description, "role, not a user") {
		t.Error("list_teams açıklaması 'benim takımım' diye bir şey OLMADIĞINI söylemiyor — model kimlik uydurur")
	}
	if !strings.Contains(toolByName(t, tools, "get_team_services").Description, "no 'my team' over MCP") {
		t.Error("get_team_services açıklaması kimlik yokluğunu söylemiyor")
	}
}

// ENV KARARI — get_team_services'in okuması (GetServicesAggFilteredIn)
// deploy_env conjunct'ı TAŞIMIYOR. Arg eklemek sessiz no-op olurdu;
// guided'ın takım bloğu da "tüm ortamların toplamı" diye yazıyor.
func TestTeamToolsDeclareNoEnvArg(t *testing.T) {
	tools := ToolList(Deps{})
	for _, name := range teamToolNames {
		if _, has := schemaProps(t, toolByName(t, tools, name))["env"]; has {
			t.Errorf("%s: env arg'ı eklenmiş ama okumanın env yolu YOK — ya no-op filtre ya yanlış vaat", name)
		}
	}
	if !strings.Contains(toolByName(t, tools, "get_team_services").Description, "NO env narrowing") {
		t.Error("açıklama env yokluğunu söylemiyor — model sayıları tek ortama atfeder")
	}
}

// list_teams bir KATALOG okumasıdır (service_metadata), telemetri değil:
// range_s koymak kabul edilip yok sayılan bir arg olurdu.
func TestListTeamsDeclaresNoRangeArg(t *testing.T) {
	tool := toolByName(t, ToolList(Deps{}), "list_teams")
	if _, ok := schemaProps(t, tool)["range_s"]; ok {
		t.Error("list_teams: range_s property'si var ama okuma pencere ALMIYOR")
	}
	if !strings.Contains(tool.Description, "NOT live telemetry") {
		t.Error("açıklama kaynağın KATALOG olduğunu söylemiyor — model boş takımı 'ölü' sanır")
	}
}

// get_team_services'in şeması 5 dakikalık kova TABANINI söylemeli: MV
// okuması `from`u kovaya yuvarlıyor, yani 60sn isteyen ~5dk alır.
func TestGetTeamServicesSchemaDeclaresBucketFloorAndCeiling(t *testing.T) {
	tool := toolByName(t, ToolList(Deps{}), "get_team_services")
	props := schemaProps(t, tool)
	rangeProp, _ := props["range_s"].(map[string]any)
	if rangeProp == nil {
		t.Fatal("range_s property yok")
	}
	if max, _ := rangeProp["maximum"].(int); max != teamSvcMaxRangeS {
		t.Errorf("range_s maximum=%v — teamSvcMaxRangeS (%d) olmalı", rangeProp["maximum"], teamSvcMaxRangeS)
	}
	if desc, _ := rangeProp["description"].(string); !strings.Contains(desc, "300") {
		t.Error("range_s açıklaması 5 dakikalık kova TABANINI söylemiyor — LLM 60s isteyip 300s alır ve farkı bilmez")
	}
	req, _ := tool.InputSchema["required"].([]string)
	if len(req) != 1 || req[0] != "team" {
		t.Errorf("required=%v — team zorunlu olmalı (kimlik yok, takım adı tek girdi)", req)
	}
	if !strings.Contains(tool.Description, "100") {
		t.Error("açıklama 100 servislik tavanı söylemiyor — büyük takımda liste sessizce eksik olurdu")
	}
	if !strings.Contains(tool.Description, "silent_services") {
		t.Error("açıklama sessiz servis ayrımını söylemiyor — model span üretmeyeni 'sağlıklı' ilan eder")
	}
}

// Tool'lar sahiplik ailesi olarak yan yana durmalı (tools/list sıralı
// gelir; model komşuluğu okur). list_teams, get_team_services'in `team`
// arg'ının keşif eşidir — ayrı düşerse model adı uydurur.
func TestTeamToolsGroupedNearOwnershipFamily(t *testing.T) {
	tools := ToolList(Deps{})
	pos := map[string]int{}
	for i, tool := range tools {
		pos[tool.Name] = i
	}
	pairs := []struct{ tool, neighbour string }{
		{"list_teams", "get_team_services"},
		// "Ne var" (filo) → "kimin" (sahiplik) aynı komşulukta.
		{"get_team_services", "list_services"},
	}
	for _, p := range pairs {
		ti, tok := pos[p.tool]
		ni, nok := pos[p.neighbour]
		if !tok || !nok {
			t.Fatalf("katalogda eksik: %s=%v %s=%v", p.tool, tok, p.neighbour, nok)
		}
		if diff := ti - ni; diff > 3 || diff < -3 {
			t.Errorf("%s, %s'ten %d sıra uzakta — aile birlikte dursun", p.tool, p.neighbour, diff)
		}
	}
}

// ── saf çözümleme (v0.9.1134'ten TAŞINAN vakalar) ──────────────

func TestTeamCatalogueOrderAndDedup(t *testing.T) {
	mds := map[string]chstore.ServiceMetadata{
		"a": {OwnerTeam: "Avengersy", SRETeam: "Platform"},
		"b": {OwnerTeam: "avengerSY"},
		"c": {SRETeam: "Platform"},
		// Aynı takım iki rolde → servis BİR kez sayılmalı.
		"d": {OwnerTeam: "Ödeme", SRETeam: "Ödeme"},
		"e": {OwnerTeam: "  ", SRETeam: ""},
	}
	got := TeamCatalogueNames(TeamCatalogue(chstore.TeamAliases{}, mds))
	want := []string{"Avengersy", "Platform", "Ödeme"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("TeamCatalogue = %v, want %v", got, want)
	}
	// Sayımlar da doğru: Avengersy 2 (a + b, alias'sız iki yazım AYRI
	// değil — katlama zaten tek takıma indiriyor), Platform 2, Ödeme 1.
	rows := TeamCatalogue(chstore.TeamAliases{}, mds)
	counts := map[string]int{}
	for _, r := range rows {
		counts[r.Team] = r.Services
	}
	if counts["Avengersy"] != 2 || counts["Platform"] != 2 || counts["Ödeme"] != 1 {
		t.Errorf("servis sayıları yanlış: %+v", rows)
	}
	// Alias tablosu iki yazımı TEK takıma indirir; sayım da birleşir,
	// yani alias'lı takım sıralamada yukarı çıkar.
	ta := chstore.TeamAliases{Aliases: map[string]string{"SY-Dijital Bankacılık": "Ödeme"}}
	mds["f"] = chstore.ServiceMetadata{OwnerTeam: "SY-Dijital Bankacılık"}
	mds["g"] = chstore.ServiceMetadata{OwnerTeam: "SY-Dijital Bankacılık"}
	got = TeamCatalogueNames(TeamCatalogue(ta, mds))
	if len(got) != 3 {
		t.Fatalf("alias birleştirmesi çalışmadı: %v", got)
	}
	if got[0] != "Ödeme" {
		t.Errorf("alias'lı takım 3 servisle başa geçmeli, sıra: %v", got)
	}
	// Determinizm: aynı girdi aynı sıra (map iterasyonu sızmıyor).
	for i := 0; i < 20; i++ {
		again := TeamCatalogueNames(TeamCatalogue(ta, mds))
		if strings.Join(again, "|") != strings.Join(got, "|") {
			t.Fatalf("sıra deterministik değil: %v vs %v", again, got)
		}
	}
}

// BİRLEŞİM semantiği: ownerTeam VEYA sreTeam. api/problems_filter.go'daki
// servicesForTeam (AND, inbox filtresi) ile karıştırılmamalı.
func TestTeamServiceNamesUnion(t *testing.T) {
	mds := map[string]chstore.ServiceMetadata{
		"checkout": {OwnerTeam: "Avengersy"},
		"ledger":   {SRETeam: "avengerSY"}, // yalnız SRE + farklı yazım
		"search":   {OwnerTeam: "Platform", SRETeam: "Platform"},
		"idle":     {},
	}
	cases := []struct {
		name string
		team string
		want []string
	}{
		{"owner + sre birleşimi, katlamalı", "Avengersy", []string{"checkout", "ledger"}},
		{"çıplak yazım da eşleşir", "avengersy", []string{"checkout", "ledger"}},
		{"tek takım", "Platform", []string{"search"}},
		{"eşleşmeyen takım", "Yok", nil},
		{"boş takım nil döner (tüm filo DEĞİL)", "", nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := TeamServiceNames(chstore.TeamAliases{}, mds, c.team)
			if strings.Join(got, "|") != strings.Join(c.want, "|") {
				t.Fatalf("= %v, want %v", got, c.want)
			}
		})
	}
	// Alias tablosu: LDAP yazımı telemetri yazımına eşlenince servisler
	// LDAP adıyla da bulunur.
	ta := chstore.TeamAliases{Aliases: map[string]string{"SY-Dijital Bankacılık": "Avengersy"}}
	if got := TeamServiceNames(ta, mds, "SY-Dijital Bankacılık"); len(got) != 2 {
		t.Errorf("alias'lı takım adı çözülmedi: %v", got)
	}
}

// SIRALAMA SÖZLEŞMESİ (operatörün cümlesi): hata oranı azalan, eşitlikte
// hata sayısı azalan, sonra ad. Aile bundle'ının SAYI-birincil sırasıyla
// karıştırılmamalı.
func TestSortServicesByErrorRate(t *testing.T) {
	rows := []chstore.ServiceSummary{
		{Name: "low-rate-high-count", ErrorRate: 0.5, ErrorCount: 5000, SpanCount: 1_000_000},
		{Name: "worst", ErrorRate: 42.0, ErrorCount: 42, SpanCount: 100},
		{Name: "tie-b", ErrorRate: 10.0, ErrorCount: 100},
		{Name: "tie-a", ErrorRate: 10.0, ErrorCount: 900},
		{Name: "tie-name-b", ErrorRate: 1.0, ErrorCount: 1},
		{Name: "tie-name-a", ErrorRate: 1.0, ErrorCount: 1},
		{Name: "clean", ErrorRate: 0, ErrorCount: 0, SpanCount: 500},
	}
	SortServicesByErrorRate(rows)
	want := []string{"worst", "tie-a", "tie-b", "tie-name-a", "tie-name-b", "low-rate-high-count", "clean"}
	for i, w := range want {
		if rows[i].Name != w {
			t.Fatalf("sıra[%d] = %q, want %q (tam sıra: %v)", i, rows[i].Name, w, teamRowNames(rows))
		}
	}
}

func teamRowNames(rows []chstore.ServiceSummary) []string {
	out := make([]string, 0, len(rows))
	for _, r := range rows {
		out = append(out, r.Name)
	}
	return out
}

// ── pencere + zarflar ──────────────────────────────────────────

func TestTeamSvcWindowS(t *testing.T) {
	cases := []struct {
		name string
		in   int
		want int
	}{
		{"sıfır → varsayılan 1sa", 0, 3600},
		{"negatif → varsayılan", -30, 3600},
		{"kova altı yükseltilir", 60, 300},
		{"tam kova geçer", 300, 300},
		{"aradaki değer korunur", 7200, 7200},
		{"tavan 7 gün", 30 * 86400, 7 * 86400},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := teamSvcWindowS(c.in); got != c.want {
				t.Fatalf("teamSvcWindowS(%d) = %d, want %d", c.in, got, c.want)
			}
		})
	}
}

func TestTeamsPayloadEnvelope(t *testing.T) {
	rows := []TeamCatalogueEntry{{Team: "A", Services: 5}, {Team: "B", Services: 2}, {Team: "C", Services: 1}}
	data := TeamCatalogueData{Teams: rows, Catalogued: 8, Unassigned: 1}
	out := teamsPayload(rows, data, "", 2)
	if out["count"] != 2 || out["total_teams"] != 3 || out["has_more"] != true {
		t.Fatalf("kırpma zarfı yanlış: %+v", out)
	}
	if out["unassigned_services"] != 1 || out["catalogued_services"] != 8 {
		t.Errorf("katalog sayaçları yok: %+v", out)
	}
	if _, has := out["reasons"]; has {
		t.Error("dolu listede reasons basılmış")
	}
	full := teamsPayload(rows, data, "", 10)
	if full["has_more"] != false || full["count"] != 3 {
		t.Errorf("tavan altında kırpma iddiası: %+v", full)
	}
	// BOŞ liste: sebep zorunlu ve gövde yine de dizi taşımalı (nil değil).
	empty := teamsPayload(nil, TeamCatalogueData{Catalogued: 8}, "", 10)
	if _, has := empty["reasons"]; !has {
		t.Error("boş katalogda reasons yok — model 'takım yok' ile 'atanmamış'ı ayıramaz")
	}
	if got, ok := empty["teams"].([]TeamCatalogueEntry); !ok || got == nil {
		t.Errorf("teams alanı nil — JSON'da null olur, model diziyi bekliyor: %#v", empty["teams"])
	}
}

// BOŞ katalog üç ayrı şey demek ve üçünün eylemi farklı.
func TestTeamsReasonsDistinguishCauses(t *testing.T) {
	cases := []struct {
		name         string
		data         TeamCatalogueData
		nameContains string
		want         string
	}{
		{"süzgeç eledi", TeamCatalogueData{Catalogued: 8}, "zzz", "no team name contains"},
		{"katalog boş", TeamCatalogueData{}, "", "service catalog is empty"},
		{"kimseye atanmamış", TeamCatalogueData{Catalogued: 8}, "", "NONE carries an ownerTeam"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := teamsReasons(c.data, c.nameContains)
			if len(got) != 1 || !strings.Contains(got[0], c.want) {
				t.Fatalf("reasons = %v, %q geçmeli", got, c.want)
			}
		})
	}
}

// SESSİZ servisler: çözülmüş ama pencerede satırı olmayanlar. "Listede
// yok" ≠ "sorunsuz" — bu ayrım verilmezse model sessizi sağlıklı ilan eder.
func TestSilentTeamServices(t *testing.T) {
	resolved := []string{"a", "b", "c", "d"}
	rows := []chstore.ServiceSummary{{Name: "b"}}
	names, total := silentTeamServices(resolved, rows, 10)
	if total != 3 || strings.Join(names, "|") != "a|c|d" {
		t.Fatalf("sessizler = %v (toplam %d), want a|c|d (3)", names, total)
	}
	// Tavan adları kırpar ama SAYIYI kırpmaz.
	names, total = silentTeamServices(resolved, rows, 2)
	if total != 3 || len(names) != 2 {
		t.Fatalf("tavan sayımı bozdu: %v / %d", names, total)
	}
	// Hepsi konuşuyorsa sessiz yok.
	if _, total := silentTeamServices([]string{"a"}, []chstore.ServiceSummary{{Name: "a"}}, 10); total != 0 {
		t.Errorf("sessiz olmayanı sessiz saydı")
	}
}

func TestTeamServicesPayloadEnvelope(t *testing.T) {
	data := TeamServicesData{
		Team:     "Avengersy",
		Services: []string{"a", "b", "c"},
		Trimmed:  7,
		Rows: []chstore.ServiceSummary{
			{Name: "a", ErrorRate: 5, ErrorCount: 10, SpanCount: 200, AvgMs: 3, P99Ms: 90},
			{Name: "b", ErrorRate: 1, ErrorCount: 1, SpanCount: 100, AvgMs: 2, P99Ms: 50},
		},
	}
	out := teamServicesPayload(data, 3600, 1)
	if out["matched_services"] != 10 || out["read_services"] != 3 || out["trimmed_services"] != 7 {
		t.Fatalf("tavan zarfı yanlış: %+v", out)
	}
	if out["truncated"] != true || out["count"] != 1 {
		t.Errorf("limit kırpması bildirilmedi: %+v", out)
	}
	if out["silent_services"] != 1 {
		t.Errorf("sessiz servis sayısı yanlış: %+v", out["silent_services"])
	}
	if names, _ := out["silent_service_names"].([]string); len(names) != 1 || names[0] != "c" {
		t.Errorf("sessiz servis ADI yok: %+v", out["silent_service_names"])
	}
	if _, has := out["reasons"]; has {
		t.Error("dolu listede reasons basılmış")
	}
	// Sıra iddiası gövdede yazılı olmalı — model "ilk satır en kötü" diye
	// okuyor ve bu yalnızca sıralama sözleşmesi geçerliyse doğru.
	if ord, _ := out["order"].(string); !strings.Contains(ord, "error_rate_pct desc") {
		t.Errorf("order alanı sıralama sözleşmesini söylemiyor: %q", ord)
	}
}

// BOŞ liste İKİ ayrı şey demek ve eylemleri zıt: adı düzelt vs pencereyi aç.
func TestTeamServicesReasonsDistinguishCauses(t *testing.T) {
	noTeam := teamServicesReasons(TeamServicesData{Team: "Yok"}, 3600)
	if len(noTeam) != 1 || !strings.Contains(noTeam[0], "list_teams") {
		t.Fatalf("bilinmeyen takım sebebi list_teams'e yönlendirmiyor: %v", noTeam)
	}
	silent := teamServicesReasons(TeamServicesData{Team: "A", Services: []string{"a", "b"}}, 900)
	if len(silent) != 1 || !strings.Contains(silent[0], "silent/idle rather than healthy") {
		t.Fatalf("sessiz takım sebebi 'sessiz ≠ sağlıklı' demiyor: %v", silent)
	}
	if !strings.Contains(silent[0], "900") {
		t.Errorf("sessiz sebebi pencereyi söylemiyor — model ne kadar geniş açacağını bilemez: %v", silent)
	}
	if strings.Contains(silent[0], "list_teams") {
		t.Error("sessiz takıma 'adı yanlış' denmiş — model doğru takımı reddeder")
	}
}

func TestTeamServiceRowsSanitizeAndKeepOrder(t *testing.T) {
	in := []chstore.ServiceSummary{
		{Name: "a", ErrorRate: 5.5, ErrorCount: 3, SpanCount: 10, AvgMs: 1.5, P99Ms: 9.5},
		{Name: "b"},
	}
	got := teamServiceRows(in)
	if len(got) != 2 || got[0].Service != "a" || got[1].Service != "b" {
		t.Fatalf("sıra korunmadı: %+v", got)
	}
	if got[0].ErrorRatePct != 5.5 || got[0].P99Ms != 9.5 || got[0].ErrorCount != 3 {
		t.Errorf("alan eşlemesi yanlış: %+v", got[0])
	}
	// Sonsuz/NaN JSON'da geçersizdir — mcpFloat'tan geçmeli.
	bad := teamServiceRows([]chstore.ServiceSummary{{Name: "x", ErrorRate: math.Inf(1), P99Ms: math.NaN()}})
	if bad[0].ErrorRatePct != 0 || bad[0].P99Ms != 0 {
		t.Errorf("sonlu-olmayan değer sanitize edilmedi: %+v", bad[0])
	}
}

// ── bağlanma pinleri ───────────────────────────────────────────

// Handler'lar gövdeyi SAF üreticiden döndürmeli.
func TestTeamHandlersReturnViaPurePayloads(t *testing.T) {
	b, err := os.ReadFile("team_ownership.go")
	if err != nil {
		t.Fatalf("team_ownership.go okunamadı: %v", err)
	}
	src := stripLineComments(string(b))
	for _, want := range []string{
		"return teamsPayload(rows, data, strings.TrimSpace(a.NameContains), limit), nil",
		"return teamServicesPayload(data, windowS, limit), nil",
	} {
		if !strings.Contains(src, want) {
			t.Errorf("handler saf üreticiden dönmüyor — bekleniyor: %s", want)
		}
	}
}

// MUTASYON PİNİ — RED okuması MV'den gelmeli ve SIRALI dönmeli. Ham
// spans'e düşmek ya da sıralamayı çağırana bırakmak (guided sıralar, tool
// sıralamaz) tam olarak D6'nın kapattığı sapma sınıfıdır.
func TestReadTeamServicesREDStaysOnMVAndSorts(t *testing.T) {
	b, err := os.ReadFile("team_ownership.go")
	if err != nil {
		t.Fatalf("team_ownership.go okunamadı: %v", err)
	}
	src := stripLineComments(string(b))
	i := strings.Index(src, "func ReadTeamServicesRED(")
	if i < 0 {
		t.Fatal("ReadTeamServicesRED bulunamadı")
	}
	body := src[i:]
	if j := strings.Index(body[1:], "\nfunc "); j >= 0 {
		body = body[:j]
	}
	if !strings.Contains(body, "GetServicesAggFilteredIn(") {
		t.Error("takım RED'i service_summary_5m MV'sinden okumuyor — ham spans taraması D6 sözleşmesini bozar")
	}
	if !strings.Contains(body, "SortServicesByErrorRate(rows)") {
		t.Error("okuma sıralamıyor — sıra çağırana kalırsa guided ve tool farklı 'en kötü servis' gösterir")
	}
	if !strings.Contains(body, "len(names) == 0") {
		t.Error("boş liste koruması yok — boş IN listesi TÜM filoyu döndürür (sessiz kapsam patlaması)")
	}
}

// Guided tarafının AYNI seam'leri çağırdığı pin api paketinde yaşıyor
// (kaynak dosyanın yanında): api/guided_shared_layer_test.go →
// TestGuidedTeamPathUsesSharedLayer.
