package mcptools

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

// cost_claim_test.go — v0.10.25, Copilot denetimi bulgusu.
//
// ── NEDEN BU KAPI VAR ───────────────────────────────────────────────────
//
// `list_services` açıklaması modele "5 dakikalık ön-toplamı okur, ucuz,
// tekrar tekrar çağır" diyordu; handler ise KOŞULSUZ ham `spans` GROUP BY
// yapıyordu. İkisi arasında hiçbir bağ yoktu: açıklama bir dizge, okuma
// yolu bir fonksiyon çağrısı, ve `go build` ikisinin ayrıştığını GÖREMEZ.
//
// Bu ayrışmanın iki ayrı zararı var ve ikincisi sinsi:
//
//  1. MV varken ham spans okumak zaten bug (CLAUDE.md sert kısıtı).
//  2. Açıklama, MODELİN MALİYET MODELİDİR. "Ucuz, tekrar çağır" diyen bir
//     katalog küçük modeli döngüde tekrar çağırmaya İTER — yani yalan
//     kendi maliyetini büyütür. Üstelik ileride bir denetim o satırı
//     okuyup "MV kullanılıyor" diye işaretler ve gerçek ihlal görünmez
//     kalır (feedback-audit-prescriptions-get-executed sınıfı).
//
// Kapı statik olarak çağrı grafiğini izleyemez, o yüzden şunu zorluyor:
// "ön-toplam / pre-aggregate" iddiası taşıyan HER tool, aşağıdaki
// kayıtta GEREKÇESİYLE yer almak zorunda. Kayıt insan tarafından tutulur;
// kapının işi, sessiz sürüklenmeyi GÖRÜNÜR bir karara çevirmek —
// v0.10.17'deki purge kapsam kapısıyla aynı şekil.

// mvCostClaims — "ön-toplam okur" diyen tool'lar ve iddianın DAYANAĞI.
//
// Bir tool bu listede yoksa ama açıklamasında ön-toplam iddiası varsa
// test kırmızıya döner.
var mvCostClaims = map[string]string{
	"list_services": "service_summary_5m — readServices(), range_s>=300 ve env=='' kapısı; " +
		"aksi hâlde ham spans ve açıklama bunu SÖYLÜYOR (v0.10.25)",
	"get_service_health": "list_services ile AYNI okuyucu (readServices)",
	"get_metrics_for_span": "spanmetrics MV fast-path'i YALNIZ step>=300s'de; " +
		"açıklama koşulu ve dar pencerede ham taramaya düştüğünü söylüyor (v0.10.25)",
	"get_topology":           "topology_edges_5m — koşulsuz MV",
	"get_operation_health":   "operation_summary_5m — koşulsuz MV",
	"get_correlated_changes": "db_caller_summary_5m + problems; koşulsuz MV",
	"get_deploy_diff":        "5 dakikalık ön-toplam; window_s [300,21600]'e kelepçeli",

	// ── v0.10.25'te kapının BULDUKLARI ──────────────────────────────────
	// Kapı ilk koşusunda sekiz kayıtsız iddia çıkardı; her biri kaynağına
	// kadar izlendi. YEDİSİ dürüst çıktı, BİRİ yalandı.
	//
	// ⚠ Bu sayı ilk yazımda "ikisi yalan" idi ve YANLIŞTI: search_traces'in
	// açıklaması zaten koşullu ("pre-aggregate when possible, falls back to
	// bounded raw scans") — yani dürüst. Metni okumadan ham-spans
	// okuyucusu olmasına bakıp yalan saymıştım. Tek gerçek yalan
	// list_slo_status'tü (ComputeSLOStatus → FROM spans, açıklama "bounded
	// pre-aggregate read" diyordu) ve düzeltildi, o yüzden artık iddia
	// taşımıyor ve bu listede yok.
	"get_blast_radius": "GetServiceBlastRadius → FROM service_callers_5m (doğrudan izlendi)",
	"list_operations":  "ListOperationNames → FROM operation_summary_5m (doğrudan izlendi)",
	"get_db_health": "ReadDBHealth → GetDatabases → FROM db_summary_5m; " +
		"env süzgeci ham spans'e düşürüyor (list_services ile aynı koşullu şekil). " +
		"Ayrıca TestReadDBHealthStaysOnLightMVPath ile zaten pinli",
	"get_team_services": "ReadTeamServicesRED → GetServicesAggFilteredIn (MV toplamı); " +
		"team_ownership_test.go'da pin var",
	"list_problem_window_events": "ListProblemWindowEvents → FROM problems — teknik olarak " +
		"bir ön-toplam DEĞİL, küçük bir state tablosu; ama MALİYET iddiası (ucuz) doğru " +
		"ve pin guided_parity_test.go'da",
	// ⚠ TEK DOĞRUDAN İZLENMEYEN. GetMessaging'in FROM'u dolaylı; MV
	// kataloğunda messaging_summary_5m var ve tool onun üstünde duruyor,
	// ama SQL'e kadar inilmedi. Kayıt bunu saklamıyor.
	"get_messaging_health": "ReadMessagingHealth → GetMessaging; messaging_summary_5m " +
		"olduğu KUVVETLE muhtemel ama SQL'e kadar doğrulanmadı (v0.10.25)",
	"search_traces": "trace_summary_5m ön-toplamı UYGUN OLDUĞUNDA; açıklama ham " +
		"taramaya düştüğünü ZATEN söylüyordu — koşullu ve dürüst",
}

// claimRe — açıklamada ön-toplam İDDİASI.
//
// ⚠ YALANLAMA İDDİA DEĞİLDİR. İlk yazımda düz kelime araması yapıyordum
// ve list_slo_status'ün DÜZELTİLMİŞ metnini ("COST: NOT a pre-aggregate
// read") iddia sandı — yani kapı, kendisinin doğurduğu doğru cevabı
// kırmızıya çevirdi. Bu, bu depoda tekrar eden sınıfın (kapının kendi
// dokümantasyonunu ısırması: v0.9.1375, v0.9.1382, v0.10.17) bir başka
// yüzü. Olumsuzlanan biçimler önce sökülüyor.
var negatedClaimRe = regexp.MustCompile(`(?i)(not a|never a|değil(dir)?[^.]{0,20})\s*(5-minute\s+)?(pre-aggregate|ön-toplam)`)

var claimRe = regexp.MustCompile(`(?i)pre-aggregate|ön-toplam|önToplam`)

// claimsPreAggregate — blok bir ön-toplam iddiası TAŞIYOR mu.
func claimsPreAggregate(block string) bool {
	return claimRe.MatchString(negatedClaimRe.ReplaceAllString(block, " "))
}

// TestEveryPreAggregateClaimIsRegistered — asıl kapı.
func TestEveryPreAggregateClaimIsRegistered(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	// Tool adı ile açıklamayı eşlemek için basit bir tarama: `Name:` ve
	// onu izleyen `Description:` aynı literal içinde.
	nameRe := regexp.MustCompile(`Name:\s*"([a-z0-9_]+)"`)
	found := 0
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		src := string(b)
		for _, m := range nameRe.FindAllStringSubmatchIndex(src, -1) {
			name := src[m[2]:m[3]]
			// Bu tool'un bloğu: adından bir sonraki `Name:`e kadar.
			end := len(src)
			if nx := nameRe.FindStringIndex(src[m[1]:]); nx != nil {
				end = m[1] + nx[0]
			}
			block := src[m[0]:end]
			if !claimsPreAggregate(block) {
				continue
			}
			found++
			if _, ok := mvCostClaims[name]; !ok {
				t.Errorf("%s (%s): açıklaması ön-toplam İDDİA EDİYOR ama mvCostClaims'te yok.\n"+
					"  Okuma yolu gerçekten MV'ye mi gidiyor? Gidiyorsa gerekçesiyle kaydet;\n"+
					"  gitmiyorsa AÇIKLAMAYI düzelt — model bu satırı maliyet modeli olarak okuyor\n"+
					"  ve 'ucuz, tekrar çağır' diyen bir yalan kendi maliyetini büyütür.", name, f)
			}
		}
	}
	if found < 3 {
		// Tarama bozulduysa kapı sessizce yeşile döner — korumayı
		// kaybetmenin en olası yolu.
		t.Fatalf("yalnız %d iddia bulundu; regex ya da glob bozulmuş olmalı", found)
	}
}

// TestCostClaimsAreJustified — kayıt çürümesin.
func TestCostClaimsAreJustified(t *testing.T) {
	for name, why := range mvCostClaims {
		if len(strings.TrimSpace(why)) < 20 {
			t.Errorf("%q kaydı gerekçesiz (%q) — gerekçesiz muafiyet sessiz bir kaçış kapısıdır", name, why)
		}
	}
}

// TestServicesReadUseMV — kapının kendisi.
//
// `/api/services`'in servicesUseMV sözleşmesiyle aynı: env boyutu MV'de
// YOK, ve MV kovaları 5 dakikalık.
func TestServicesReadUseMV(t *testing.T) {
	for _, tc := range []struct {
		name   string
		window time.Duration
		env    string
		want   bool
	}{
		{"geniş pencere, env yok → MV", 30 * time.Minute, "", true},
		{"tam eşikte → MV", 5 * time.Minute, "", true},
		// MV kovaları 5 dakikalık; daha dar pencerede MV'den okumak
		// kovanın TAMAMINI o pencereye atfetmek olurdu.
		{"dar pencere → ham", 4 * time.Minute, "", false},
		{"1 dakika → ham", time.Minute, "", false},
		// MV'de deploy_env boyutu YOK.
		{"env süzgeci → ham", time.Hour, "uat", false},
		{"env + dar pencere → ham", time.Minute, "prod", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := servicesReadUseMV(tc.window, tc.env); got != tc.want {
				t.Errorf("servicesReadUseMV(%v, %q) = %v; want %v", tc.window, tc.env, got, tc.want)
			}
		})
	}
}

// TestBothToolsShareOneReader — KABLOLAMA PİNİ.
//
// İki tool ayrı okuma yollarına ayrılırsa aynı soru, hangi tool'a
// düştüğüne göre iki farklı maliyetle cevaplanır — kusurun ta kendisi.
func TestBothToolsShareOneReader(t *testing.T) {
	b, err := os.ReadFile("tools.go")
	if err != nil {
		t.Fatal(err)
	}
	src := string(b)
	if n := strings.Count(src, "readServices(ctx, d,"); n != 2 {
		t.Errorf("paylaşılan okuyucu %d yerde çağrılıyor; list_services ve "+
			"get_service_health'in İKİSİ de kullanmalı", n)
	}
	// Eski koşulsuz ham çağrı geri gelmemeli.
	if strings.Contains(src, `d.Store.GetServicesFilteredIn(ctx, 0, from, to, a.NameContains`) {
		t.Error("list_services yeniden KOŞULSUZ ham spans okuyor — v0.10.25 regresyonu")
	}
}
