package mcptools

// team_ownership.go — list_teams + get_team_services (v0.9.1244):
// D6'nın kalan üç intent'inden ikisinin kapısı.
//
// Guided router SAHİPLİK sorularını yanıtlıyor ("benim servislerim",
// "ödeme takımının durumu", "takımımın problemleri" — api/copilot_guided.go
// my_services / my_problems / my_exceptions / team_services) ama serbest
// tool döngüsünde ve dış MCP istemcisinde TAKIM diye bir kavram YOKTU:
// guided regex'lerinin dışına düşen her sahiplik sorusu çıkmazdı, çünkü
// katalogda takım okuyan tek bir tool yoktu. Bu dosya o boşluğu kapatıyor.
//
// KİMLİK KAPSAMI — BİLİNÇLİ OLARAK DIŞARIDA (bu dilimin en önemli kararı).
// "BENİM servislerim" bir kimlik sorusudur; guided onu CallMeta.UserID →
// users tablosu → User.Team zinciriyle çözer. MCP tarafında böyle bir
// zincir YOK ve ucuz da değil:
//
//   - Deps yalnız Store/LogStore/Metrics taşır (api/mcp_deps.go); hiçbir
//     tool bugün çağıranın kimliğini OKUMUYOR.
//   - Dış MCP istemcisi zaten kişiye bağlı değil: cmk_ token'ı bir ROL
//     taşır, bir kullanıcı değil (Claims.UserID = "token:<id>",
//     docs/runbooks/mcp-claude-code.md §1). Yani "benim takımım" o yolda
//     TANIMSIZ — kimliğe bağlansaydı token yolunda sessizce boş dönerdi.
//
// Dolayısıyla iki tool da TAKIM ADI ile çalışır ve açıklamaları modele
// "kullanıcının takımını bilmiyorsan SOR ya da list_teams'ten seçtir"
// diyor. Kimlik-farkındalıklı MCP ayrı bir sözleşme değişikliğidir
// (Deps'e kimlik, token↔kullanıcı eşlemesi, rol/kişi ayrımı) ve buraya
// kaçak sokulmaz. my_services/my_problems guided'da ÇALIŞMAYA DEVAM
// EDİYOR — kapanan boşluk, takım ADI verilen soruların döngüye açılması.
//
// AYNI OKUMA (drift = v0.9.553 sınıfı). Bu dosya guided'ın okumasını
// kopyalamıyor, guided'ın okuması BURAYA taşındı ve api artık buradan
// besleniyor:
//
//	TeamCatalogue(ta, mds)          ← "hangi takımlar var" (saf)
//	TeamServiceNames(ta, mds, team) ← takım → servisler (saf, BİRLEŞİM)
//	ReadTeamCatalogue(ctx, d)       ← katalog okuması (mds + alias)
//	ReadTeamServiceNames(ctx, d, t) ← çözümleme + 100'lük tavan
//	ReadTeamServicesRED(ctx, d, …)  ← tek MV okuması + hata-oranı sırası
//
// api/copilot_guided.go'daki guidedTeamNames / guidedTeamServicesBundle /
// guidedMyTeamBundle bu seam'leri çağırıyor; oradaki üç yardımcı tek
// satırlık delegasyona indi. İKİ uygulama yok, yani "MCP'nin saydığı
// servisler guided'ınkinden farklı" diye bir sınıf da yok.
//
// Neden İKİ seam (çözümleme + RED) ve ayrıca bir birleştirici: guided
// adım çiplerini (⚙ resolve_team_services → team_services_red) okumaların
// ARASINDA yayınlıyor; tek birleşik okuma çip zamanlamasını değiştirirdi.
// Tool birleştiriciyi (ReadTeamServices) çağırır, guided iki yarıyı.
//
// PENCERE DÜRÜSTLÜĞÜ. RED okuması service_summary_5m üzerinde
// (GetServicesAggFilteredIn) ve chstore `from`u kova başına AŞAĞI
// yuvarlıyor (alignBucketStart, v0.9.555) — yani 60 saniyelik bir istek
// gerçekte ~5 dakikadır. range_s tabanı bu yüzden 300 ve şema bunu
// SÖYLÜYOR (get_db_health ile aynı gerekçe, depBucketS).
//
// ENV ARG'I YOK. GetServicesAggFilteredIn'in env conjunct'ı yoktur;
// guided'ın takım bloğu da "RED değerleri tüm ortamların toplamı" diye
// açıkça yazar. Arg'ı kabul edip yok saymak Faz 3.2 doktrininin
// yasakladığı şey; env daraltması istendiğinde doğru yol list_services'in
// env arg'ıdır (o okumanın env yolu var).
//
// SATIR ŞEKLİ: chstore.ServiceSummary DEĞİL — o tip UI için 20+ alan
// taşıyor (Apdex, Health, Prior* kıyas alanları) ve bu okumada
// doldurulmayanlar modelde "ölçüm 0" diye okunur. Alt küme + snake_case +
// mcpFloat (guided_parity doktrini, sapma #3).

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/cilcenk/coremetry/internal/chstore"
	"github.com/cilcenk/coremetry/internal/mcp"
)

// MaxTeamServices — takım-kapsamlı MV okumasının IN listesi tavanı.
// v0.9.1134'te api'de doğdu (maxTeamServices); v0.9.1244'te buraya taşındı
// çünkü artık İKİ yüzey aynı tavana uymak zorunda — iki ayrı sayı zamanla
// ayrışırdı ve ayrışma "MCP takımın 100 servisini, guided 80'ini saydı"
// diye görünürdü.
const MaxTeamServices = 100

const (
	// teamSvcBucketS — service_summary_5m kova boyutu = range_s TABANI.
	teamSvcBucketS       = 300
	teamSvcDefaultRangeS = 3600
	teamSvcMaxRangeS     = 7 * 86400
	teamSvcDefaultRows   = 20
	teamSvcMaxRows       = MaxTeamServices
	// teamSilentNamesMax — pencerede hiç span üretmeyen servislerden kaç
	// tanesi ADIYLA söylenir. Sayı tek başına "hangi servisler sessiz"i
	// cevaplamıyor; tam liste ise büyük takımda gövdeyi şişirir.
	teamSilentNamesMax = 20

	teamsDefaultRows = 30
	teamsMaxRows     = 200
)

// teamSvcWindowS — range_s → efektif saniye (varsayılan 1sa, taban 300 =
// MV kovası, tavan 7 gün). Saf, tablo testli.
func teamSvcWindowS(rangeS int) int {
	if rangeS <= 0 {
		return teamSvcDefaultRangeS
	}
	if rangeS < teamSvcBucketS {
		return teamSvcBucketS
	}
	if rangeS > teamSvcMaxRangeS {
		return teamSvcMaxRangeS
	}
	return rangeS
}

// ─── saf çözümleme (guided ile ORTAK) ──────────────────────────

// TeamCatalogueEntry — bir takım ve katalogdaki servis sayısı.
type TeamCatalogueEntry struct {
	Team     string `json:"team"`
	Services int    `json:"services"`
}

// betterTeamDisplay — aynı takımın iki yazımı arasında gösterileni seçer.
// KANONİK yazım (normali kendi kanonuna eşit olan, yani alias tablosunun
// HEDEF tarafı) her zaman kazanır; ikisi de kanonik ya da ikisi de alias
// ise alfabetik küçük olan. "İlk görülen" KULLANILMAZ: map iterasyon
// sırası rastgeledir, aynı soru iki farklı katalog üretirdi.
func betterTeamDisplay(cand, cur, canon string) bool {
	candCanon := chstore.NormTeamName(cand) == canon
	curCanon := chstore.NormTeamName(cur) == canon
	if candCanon != curCanon {
		return candCanon
	}
	return cand < cur
}

// TeamCatalogue — CANLI takım kataloğu: service_metadata'nın boş olmayan
// ownerTeam + sreTeam değerleri, SERVİS SAYISINA göre azalan (eşitlikte
// ada göre artan — deterministik sıra, map iterasyonu sızmaz).
//
// Tekilleştirme CanonTeam üzerinden: alias tablosu ve Türkçe katlama
// dahil, yani "avengerSY"/"Avengersy" TEK takımdır. Aynı servis hem owner
// hem SRE olarak aynı takımı gösteriyorsa İKİ kez sayılmaz.
//
// v0.9.1134'te api/copilot_guided.go'da doğdu; v0.9.1244'te buraya taşındı
// (list_teams tool'u aynı sayımı ve aynı sırayı vermek zorunda — api
// tarafındaki teamCatalogue artık bunun ad görünümü).
func TeamCatalogue(ta chstore.TeamAliases, mds map[string]chstore.ServiceMetadata) []TeamCatalogueEntry {
	type entry struct {
		display string
		n       int
	}
	byCanon := map[string]*entry{}
	for _, md := range mds {
		seen := map[string]bool{} // aynı servis owner=sre ise İKİ kez saymasın
		for _, name := range []string{md.OwnerTeam, md.SRETeam} {
			c := ta.CanonTeam(name)
			if c == "" || seen[c] {
				continue
			}
			seen[c] = true
			disp := strings.TrimSpace(name)
			if e, ok := byCanon[c]; ok {
				e.n++
				if betterTeamDisplay(disp, e.display, c) {
					e.display = disp
				}
				continue
			}
			byCanon[c] = &entry{display: disp, n: 1}
		}
	}
	out := make([]entry, 0, len(byCanon))
	for _, e := range byCanon {
		out = append(out, *e)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].n != out[j].n {
			return out[i].n > out[j].n
		}
		return out[i].display < out[j].display
	})
	rows := make([]TeamCatalogueEntry, 0, len(out))
	for _, e := range out {
		rows = append(rows, TeamCatalogueEntry{Team: e.display, Services: e.n})
	}
	return rows
}

// TeamCatalogueNames — kataloğun yalnız AD görünümü (api'nin çip listesi
// bunu istiyor). Ayrı bir sayım DEĞİL, aynı sıranın izdüşümü.
func TeamCatalogueNames(rows []TeamCatalogueEntry) []string {
	names := make([]string, 0, len(rows))
	for _, r := range rows {
		names = append(names, r.Team)
	}
	return names
}

// TeamServiceNames — takıma ait servisler: ownerTeam VEYA sreTeam
// eşleşmesi, alias + Türkçe katlama farkındalıklı, ada göre sıralı.
//
// BİRLEŞİM semantiği (v0.9.375'te api'de doğdu): "bu takımın servisleri"
// sorusu iki rolün birleşimidir. api/problems_filter.go'daki
// servicesForTeam bundan FARKLIDIR — orada owner ve SRE ayrı süzgeçlerdir
// (AND, inbox filtresi). Karıştırmak inbox'ın filtresini sessizce genişletir.
func TeamServiceNames(ta chstore.TeamAliases, mds map[string]chstore.ServiceMetadata, team string) []string {
	if team == "" {
		return nil
	}
	out := make([]string, 0, 16)
	for svc, md := range mds {
		if ta.TeamEqual(md.OwnerTeam, team) || ta.TeamEqual(md.SRETeam, team) {
			out = append(out, svc)
		}
	}
	sort.Strings(out)
	return out
}

// TeamDisplayName — bir takım adının KATALOGDAKİ yazımı ("sy" → "SY").
// Eşleşme yoksa girdinin kırpılmışı döner (uydurma yapmaz).
//
// v0.9.1246 (operatör: gerçek takım adları "SY"/"UG" gibi 2 harfli
// KISA kodlar): sohbetten üretilen derin link URL'e bir takım adı
// yazıyor ve o ad operatöre ÇİP olarak geri görünüyor. Kullanıcının
// yazdığı ("sy") ya da users tablosundaki yazım katalogunkinden farklı
// olabilir; eşleşme zaten katlamalı (TeamEqual) olduğu için ikisi de
// AYNI satırları getirir, ama çipte "sy" yazması operatöre kataloğun
// başka bir takımıymış gibi okunur. Kanonik yazımı TEK yerden seçiyoruz:
// TeamCatalogue'un betterTeamDisplay'i (alias hedefi kazanır, eşitlikte
// alfabetik) — ikinci bir "hangi yazım doğru" kuralı yazmak, iki yüzeyin
// aynı takımı farklı adlandırması demekti.
//
// UZUNLUK VARSAYIMI YOK: 2 harflik ad da tam yurttaş (v0.9.1246).
func TeamDisplayName(ta chstore.TeamAliases, mds map[string]chstore.ServiceMetadata, team string) string {
	t := strings.TrimSpace(team)
	if t == "" {
		return ""
	}
	canon := ta.CanonTeam(t)
	for _, e := range TeamCatalogue(ta, mds) {
		if ta.CanonTeam(e.Team) == canon {
			return e.Team
		}
	}
	return t
}

// SortServicesByErrorRate — ORAN birincil, SAYI eşitlik bozucu, ad üçüncü.
//
// Operatörün açık istegi (v0.9.1134): "en çok hata alan / error rate
// yüksek olanlara göre sırala" — 10 istekte 5 hata alan servis, 1M istekte
// 100 hata alandan daha kötüdür. Üçüncü anahtar ad: eşit oran+sayıda sıra
// deterministik kalsın (aynı soru iki farklı liste üretmesin).
func SortServicesByErrorRate(rows []chstore.ServiceSummary) {
	sort.SliceStable(rows, func(i, j int) bool {
		if rows[i].ErrorRate != rows[j].ErrorRate {
			return rows[i].ErrorRate > rows[j].ErrorRate
		}
		if rows[i].ErrorCount != rows[j].ErrorCount {
			return rows[i].ErrorCount > rows[j].ErrorCount
		}
		return rows[i].Name < rows[j].Name
	})
}

// ─── okuma katmanı (guided + tool ORTAK) ───────────────────────

// teamAliasesOf — alias tablosunun istek-anlık okuması. Hata → BOŞ tablo
// (eşleme yokmuş gibi davran); api/team_aliases.go'daki teamAliasesCtx ile
// aynı sözleşme. Alias okuması bir ayar point-read'idir; onun arızası
// yüzünden takım sorusunu tamamen reddetmek, cevabı kötüleştirir.
func teamAliasesOf(ctx context.Context, d Deps) chstore.TeamAliases {
	ta, err := d.Store.GetTeamAliases(ctx)
	if err != nil {
		return chstore.TeamAliases{}
	}
	return ta
}

// TeamCatalogueData — katalog okumasının yapısal sonucu.
type TeamCatalogueData struct {
	Teams []TeamCatalogueEntry
	// Catalogued — service_metadata'daki satır sayısı (katalogda HİÇ
	// satırı olmayan servis burada da yok, payload bunu söylüyor).
	Catalogued int
	// Unassigned — katalogda satırı olan ama ne owner ne SRE takımı
	// atanmış servisler. "Takım listesi kısa" ile "kimse atamamış"
	// farkını modelin görmesi için.
	Unassigned int
}

// ReadTeamCatalogue — takım kataloğu okuması: service_metadata (30sn
// süreç-içi cache'li FINAL okuma) + alias tablosu. Telemetriye HİÇ
// dokunmaz, o yüzden pencere argümanı da yok.
func ReadTeamCatalogue(ctx context.Context, d Deps) (TeamCatalogueData, error) {
	mds, err := d.Store.ListServiceMetadata(ctx)
	if err != nil {
		return TeamCatalogueData{}, err
	}
	ta := teamAliasesOf(ctx, d)
	out := TeamCatalogueData{Teams: TeamCatalogue(ta, mds), Catalogued: len(mds)}
	for _, md := range mds {
		if ta.CanonTeam(md.OwnerTeam) == "" && ta.CanonTeam(md.SRETeam) == "" {
			out.Unassigned++
		}
	}
	return out, nil
}

// ReadTeamServiceNames — takım → servis adları + 100'lük tavan.
// İkinci dönüş tavana takılıp HİÇ okunmayan servis sayısı (dürüstlük
// zarfı: "takımın 130 servisi var, 100'ünü okudum").
func ReadTeamServiceNames(ctx context.Context, d Deps, team string) ([]string, int, error) {
	mds, err := d.Store.ListServiceMetadata(ctx)
	if err != nil {
		return nil, 0, err
	}
	svcs := TeamServiceNames(teamAliasesOf(ctx, d), mds, team)
	trimmed := 0
	if len(svcs) > MaxTeamServices {
		trimmed = len(svcs) - MaxTeamServices
		svcs = svcs[:MaxTeamServices]
	}
	return svcs, trimmed, nil
}

// ReadTeamServicesRED — çözülmüş servis listesinin RED'i: TEK MV okuması
// (servis başına fan-out YOK) + hata-oranı sırası. Boş liste → boş sonuç,
// okuma hiç yapılmaz (boş IN listesi tüm filoyu döndürürdü — sessiz
// kapsam patlaması).
func ReadTeamServicesRED(ctx context.Context, d Deps, names []string, from, to time.Time) ([]chstore.ServiceSummary, error) {
	if len(names) == 0 {
		return nil, nil
	}
	rows, err := d.Store.GetServicesAggFilteredIn(ctx, from, to, "", names, "", "", len(names), 0)
	if err != nil {
		return nil, err
	}
	SortServicesByErrorRate(rows)
	return rows, nil
}

// TeamServicesData — takım okumasının tam sonucu (tool tarafı; guided iki
// yarıyı ayrı çağırıyor, bkz. dosya başlığı).
type TeamServicesData struct {
	Team     string
	Services []string // ÇÖZÜLEN adlar (tavan uygulanmış), ada göre sıralı
	Trimmed  int      // tavana takılıp hiç okunmayan
	Rows     []chstore.ServiceSummary
}

// ReadTeamServices — iki seam'in birleştiricisi.
func ReadTeamServices(ctx context.Context, d Deps, team string, from, to time.Time) (TeamServicesData, error) {
	svcs, trimmed, err := ReadTeamServiceNames(ctx, d, team)
	if err != nil {
		return TeamServicesData{}, err
	}
	rows, err := ReadTeamServicesRED(ctx, d, svcs, from, to)
	if err != nil {
		return TeamServicesData{}, err
	}
	return TeamServicesData{Team: team, Services: svcs, Trimmed: trimmed, Rows: rows}, nil
}

// ─── saf zarflar (tool gövdeleri) ──────────────────────────────

// teamsPayload — list_teams gövdesi. Saf, tablo testli.
func teamsPayload(rows []TeamCatalogueEntry, data TeamCatalogueData, nameContains string, limit int) map[string]any {
	total := len(rows)
	hasMore := false
	if limit > 0 && total > limit {
		rows = rows[:limit]
		hasMore = true
	}
	if rows == nil {
		rows = []TeamCatalogueEntry{}
	}
	out := map[string]any{
		"teams":               rows,
		"count":               len(rows),
		"total_teams":         total,
		"has_more":            hasMore,
		"catalogued_services": data.Catalogued,
		"unassigned_services": data.Unassigned,
		"source":              "service catalog (ownerTeam/sreTeam), operator-curated — NOT derived from live telemetry",
	}
	if nameContains != "" {
		out["name_contains"] = nameContains
	}
	if len(rows) == 0 {
		out["reasons"] = teamsReasons(data, nameContains)
	}
	return out
}

// teamsReasons — BOŞ katalog üç ayrı şey demek olabilir ve yalnız zarf
// hangisi olduğunu bilir. Sırayla: süzgeç eledi → katalog boş → katalog
// dolu ama kimseye takım atanmamış. Saf, tablo testli.
func teamsReasons(data TeamCatalogueData, nameContains string) []string {
	if nameContains != "" {
		return []string{fmt.Sprintf("no team name contains %q — re-run without name_contains to see the whole catalogue", nameContains)}
	}
	if data.Catalogued == 0 {
		return []string{"the service catalog is empty — no service has a catalog row yet, so ownership is unknown; ask the operator to name the service instead"}
	}
	return []string{fmt.Sprintf("%d services have catalog rows but NONE carries an ownerTeam/sreTeam — ownership is unassigned, not missing data; team questions cannot be answered until Settings → Service Catalog assigns teams", data.Catalogued)}
}

// TeamServiceRow — takımın bir servisi. Alt küme + snake_case.
type TeamServiceRow struct {
	Service      string  `json:"service"`
	ErrorRatePct float64 `json:"error_rate_pct"`
	ErrorCount   uint64  `json:"error_count"`
	SpanCount    uint64  `json:"span_count"`
	AvgMs        float64 `json:"avg_ms"`
	P99Ms        float64 `json:"p99_ms"`
}

// teamServiceRows — MV satırları → tool satırları. Sıra KORUNUR (hata
// oranına göre azalan). mcpFloat: sonsuz/NaN JSON'da geçersizdir ve
// sanitize edilmezse gövde sessizce bozulur. Saf, tablo testli.
func teamServiceRows(in []chstore.ServiceSummary) []TeamServiceRow {
	out := make([]TeamServiceRow, 0, len(in))
	for _, r := range in {
		out = append(out, TeamServiceRow{
			Service:      r.Name,
			ErrorRatePct: mcpFloat(r.ErrorRate),
			ErrorCount:   r.ErrorCount,
			SpanCount:    r.SpanCount,
			AvgMs:        mcpFloat(r.AvgMs),
			P99Ms:        mcpFloat(r.P99Ms),
		})
	}
	return out
}

// silentTeamServices — çözülen ama pencerede HİÇ span üretmeyen servisler.
// "Listede yok" ile "sorunsuz" aynı şey değil: bu ayrım verilmezse model
// sessiz bir servisi sağlıklı ilan eder. Sıra çözümlemenin sırasıdır
// (alfabetik), tavan uygulanır. Saf, tablo testli.
func silentTeamServices(resolved []string, rows []chstore.ServiceSummary, max int) ([]string, int) {
	seen := make(map[string]bool, len(rows))
	for _, r := range rows {
		seen[r.Name] = true
	}
	var names []string
	total := 0
	for _, s := range resolved {
		if seen[s] {
			continue
		}
		total++
		if max <= 0 || len(names) < max {
			names = append(names, s)
		}
	}
	return names, total
}

// teamServicesPayload — get_team_services gövdesi. Saf, tablo testli.
func teamServicesPayload(data TeamServicesData, windowS, limit int) map[string]any {
	rows := teamServiceRows(data.Rows)
	truncated := false
	if limit > 0 && len(rows) > limit {
		rows = rows[:limit]
		truncated = true
	}
	silentNames, silentTotal := silentTeamServices(data.Services, data.Rows, teamSilentNamesMax)
	out := map[string]any{
		"team":             data.Team,
		"window_s":         windowS,
		"services":         rows,
		"count":            len(rows),
		"matched_services": len(data.Services) + data.Trimmed,
		"read_services":    len(data.Services),
		"trimmed_services": data.Trimmed,
		"truncated":        truncated,
		"order":            "error_rate_pct desc, then error_count desc, then service name",
		"silent_services":  silentTotal,
	}
	if len(silentNames) > 0 {
		out["silent_service_names"] = silentNames
	}
	if len(rows) == 0 {
		out["reasons"] = teamServicesReasons(data, windowS)
	}
	return out
}

// teamServicesReasons — BOŞ liste iki ayrı şey demek: takım katalogda
// yok/atanmamış, ya da takımın servisleri bu pencerede hiç span üretmedi.
// İkisi taban tabana zıt eylem gerektirir (adı düzelt vs pencereyi aç).
// Saf, tablo testli.
func teamServicesReasons(data TeamServicesData, windowS int) []string {
	if len(data.Services) == 0 {
		return []string{fmt.Sprintf("no service lists %q as ownerTeam or sreTeam in the service catalog — the team name may be spelled differently (run list_teams and copy it verbatim) or nobody assigned it yet", data.Team)}
	}
	return []string{fmt.Sprintf("the team owns %d service(s) but none produced a single span in the last %ds — widen range_s, or treat them as silent/idle rather than healthy", len(data.Services), windowS)}
}

// ─── list_teams ────────────────────────────────────────────────

type listTeamsArgs struct {
	NameContains string `json:"name_contains,omitempty"`
	Limit        int    `json:"limit,omitempty"`
}

func listTeamsTool(d Deps) mcp.Tool {
	return mcp.Tool{
		Name: "list_teams",
		ShortDescription: "Servis kataloğundaki TAKIMLAR ve her birinin servis sayısı (ownerTeam/sreTeam). " +
			"get_team_services'in `team` arg'ının eşi: takım adını uydurma, buradan al. " +
			"Telemetri değil katalog okuması — pencere argümanı yok.",
		Description: "List the teams that own services in Coremetry, with how many services each one owns. " +
			"Source is the operator-curated service catalog (ownerTeam / sreTeam on each service), NOT live telemetry — so a team appears here the moment it is assigned, even if its services are idle. " +
			"Use it as the entry point for every ownership question ('who owns checkout', 'how is the payments team doing') and ALWAYS to resolve the exact spelling before calling get_team_services — team names are Turkish and case/alias variants ('avengerSY' vs 'Avengersy') are folded into ONE row here. " +
			"NO 'my team': an MCP token carries a role, not a user, so there is nobody to resolve 'mine' to. If the operator asks about 'my services', ask which team they mean and offer the names from this list — never guess. " +
			"Sorted by service count descending, so the largest teams come first. Cheap: a cached catalog read with no time window and no span scan. " +
			"An empty list is not an error: `reasons` distinguishes an empty catalog from a catalog where nobody has been assigned a team yet.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"name_contains": map[string]any{
					"type":        "string",
					"description": "Case-insensitive substring filter over team names. Empty = every team. Use it only when the operator already named a team; otherwise list them all, the catalogue is small.",
				},
				"limit": map[string]any{
					"type":        "integer",
					"minimum":     1,
					"maximum":     teamsMaxRows,
					"description": "Max teams to return. Default 30, max 200. `total_teams` and `has_more` always report the untruncated count.",
				},
			},
		},
		// Salt-okunur; REST eşi GET /api/services-metadata kapısız
		// (viewer-açık, api.go:726) — takım atamaları Service Catalog'da
		// zaten her role görünür.
		MinRole: "",
		Handler: func(ctx context.Context, raw json.RawMessage) (any, error) {
			var a listTeamsArgs
			if len(raw) > 0 {
				if err := json.Unmarshal(raw, &a); err != nil {
					return nil, fmt.Errorf("decode args: %w", err)
				}
			}
			limit := clampLimit(a.Limit, teamsDefaultRows, teamsMaxRows)
			data, err := ReadTeamCatalogue(ctx, d)
			if err != nil {
				return nil, err
			}
			rows := data.Teams
			if q := strings.TrimSpace(a.NameContains); q != "" {
				filtered := make([]TeamCatalogueEntry, 0, len(rows))
				for _, r := range rows {
					if strings.Contains(chstore.NormTeamName(r.Team), chstore.NormTeamName(q)) {
						filtered = append(filtered, r)
					}
				}
				rows = filtered
			}
			return teamsPayload(rows, data, strings.TrimSpace(a.NameContains), limit), nil
		},
	}
}

// ─── get_team_services ─────────────────────────────────────────

type getTeamServicesArgs struct {
	Team   string `json:"team"`
	RangeS int    `json:"range_s,omitempty"`
	Limit  int    `json:"limit,omitempty"`
}

func getTeamServicesTool(d Deps) mcp.Tool {
	return mcp.Tool{
		Name: "get_team_services",
		ShortDescription: "Bir TAKIMIN servisleri ve RED'i (hata oranı, p99, hacim), en çok hata alan önce. " +
			"list_teams ile adı al, sonra bunu çağır. Sahiplik sorusunun cevabı; env daraltması YOK, " +
			"sessiz servisler ayrıca sayılır.",
		Description: "Return every service owned by ONE team (ownerTeam OR sreTeam match in the service catalog) together with its RED numbers over the window: error rate, error count, span volume, average and p99 latency. " +
			"This is the ownership lens on list_services: use it for 'how is the payments team doing', 'which of my team's services is broken', 'who should look at this'. Get the exact team name from list_teams first — a misspelled team returns an empty list, not an error. " +
			"There is no 'my team' over MCP: the caller is a token carrying a ROLE, not a user, so when the operator says 'my services' you must ASK which team and take the name from list_teams — never guess one. " +
			"Rows are ordered by ERROR RATE descending (then error count, then name), so the first row is the team's worst service and is the one to name in the answer. " +
			"Reads the 5-minute service pre-aggregate in ONE query for the whole team (no per-service fan-out), so it is cheap even for a 100-service team. A team wider than 100 services is read up to that ceiling and `trimmed_services` says how many were left out. " +
			"HONESTY: services that produced NO span in the window are absent from `services` and counted in `silent_services` (with names) — a silent service is idle or broken-upstream, NEVER 'healthy'. " +
			"NO env narrowing: this read has no deploy_env conjunct, so the numbers are the sum across all environments; use list_services with `env` when the operator asks about one environment.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"team": map[string]any{
					"type":        "string",
					"description": "Team name exactly as list_teams returns it (alias and Turkish case variants are folded, but a wrong name still yields nothing). Required — there is no 'my team' over MCP: a token carries a role, not a user.",
				},
				"range_s": map[string]any{
					"type":    "integer",
					"minimum": 0,
					"maximum": teamSvcMaxRangeS,
					"description": "Lookback seconds. Default 3600 (1h), max 604800 (7d). FLOOR 300: the read is a 5-minute pre-aggregate and the window start is rounded DOWN to a bucket, " +
						"so asking for 60 seconds really answers for ~5 minutes.",
				},
				"limit": map[string]any{
					"type":        "integer",
					"minimum":     1,
					"maximum":     teamSvcMaxRows,
					"description": "Max service rows returned. Default 20, max 100. Rows are error-rate ordered, so a small limit keeps the worst ones; `read_services` reports how many were actually queried.",
				},
			},
			"required": []string{"team"},
		},
		// Salt-okunur; REST eşi GET /api/services?ownerTeam=…&sreTeam=…
		// kapısız (viewer-açık, api.go:576) — aynı çözümlemenin sayfa hali.
		MinRole: "",
		Handler: func(ctx context.Context, raw json.RawMessage) (any, error) {
			var a getTeamServicesArgs
			if len(raw) > 0 {
				if err := json.Unmarshal(raw, &a); err != nil {
					return nil, fmt.Errorf("decode args: %w", err)
				}
			}
			team := strings.TrimSpace(a.Team)
			if team == "" {
				return nil, fmt.Errorf("team is required — get the exact name from list_teams")
			}
			windowS := teamSvcWindowS(a.RangeS)
			limit := clampLimit(a.Limit, teamSvcDefaultRows, teamSvcMaxRows)
			from, to := rangeWindow(ctx, windowS)
			data, err := ReadTeamServices(ctx, d, team, from, to)
			if err != nil {
				return nil, err
			}
			return teamServicesPayload(data, windowS, limit), nil
		},
	}
}
