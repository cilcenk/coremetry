package mcptools

// discovery_test.go — v0.9.1141 (AI Faz 3.2). Beş keşif tool'unun
// sözleşmesi: (a) katalogda kayıtlı + MinRole "" + her property açıklamalı,
// (b) pencere semantiği ŞEMADA dürüst (kabul edip yok sayılan arg yok),
// (c) dürüstlük zarfları (truncated / has_more / found=false) saf
// üreticilerde tablo testli, (d) handler'ların gerçekten o üreticilerden
// döndüğü kaynak pini ile bağlı — saf test tek başına BAĞLANMA kanıtı
// değildir (bu depoda yanan ders: kapı saf fonksiyonu doğruluyor ama
// çağrı yolu sessizce başka bir gövde döndürebiliyor).

import (
	"os"
	"strings"
	"testing"
	"time"
)

// discoveryToolNames — Faz 3.2'nin beşlisi. Yeni keşif tool'u eklenirse
// buraya da girer (aşağıdaki tüm property/rol taramaları bu listeden koşar).
var discoveryToolNames = []string{
	"list_operations", "list_environments", "list_clusters",
	"list_deploys", "find_trace_by_span",
}

func TestDiscoveryToolsRegistered(t *testing.T) {
	tools := ToolList(Deps{}) // Deps{} güvenli: ToolList yalnız closure kurar.
	for _, name := range discoveryToolNames {
		t.Run(name, func(t *testing.T) {
			tool := toolByName(t, tools, name)
			// MinRole AÇIKÇA "" — salt-okunur ve REST eşi viewer'a açık.
			// (api/mcp_authz_test.go bunu registry genelinde de pinler.)
			if tool.MinRole != "" {
				t.Fatalf("%s: MinRole=%q — keşif tool'ları viewer tabanında olmalı", name, tool.MinRole)
			}
			if tool.Handler == nil {
				t.Fatalf("%s: Handler nil", name)
			}
			if len(tool.Description) < 120 {
				t.Fatalf("%s: açıklama kontrat değil cümle (%d karakter) — ne döndürdüğü + ne zaman çağrılacağı + maliyeti geçmeli",
					name, len(tool.Description))
			}
			props := schemaProps(t, tool)
			if len(props) == 0 {
				t.Fatalf("%s: hiç property yok", name)
			}
			for pname, p := range props {
				pm, _ := p.(map[string]any)
				if desc, _ := pm["description"].(string); desc == "" {
					// /mcp-tools kuralı: açıklamasız property'yi LLM tahmin eder.
					t.Fatalf("%s: %q property'sinin açıklaması yok", name, pname)
				}
			}
			// required[] kanonik şekil: []string. []any olsaydı diğer
			// testlerin .([]string) assert'i sessizce atlanırdı (v0.9.1050).
			if req, ok := tool.InputSchema["required"]; ok {
				if _, isStr := req.([]string); !isStr {
					t.Fatalf("%s: required[] %T — []string olmalı", name, req)
				}
			}
		})
	}
}

// Pencere dürüstlüğü: ListOperationNames'in pencere argümanı YOK
// (operation_summary_5m üzerinde DISTINCT = katalog), ListEnvironments
// store-side sabit pencereye kelepçeli. İkisine range_s koymak "kabul
// edilen ama yok sayılan arg" olurdu — Faz 3.2'nin kapatmaya çalıştığı
// sınıfın aynısı, ters yönde.
func TestCatalogueToolsDeclareNoRangeArg(t *testing.T) {
	tools := ToolList(Deps{})
	// v0.9.1142 — find_trace_by_request_id de bu kümede: penceresi
	// kimliğin İÇİNDEKİ damgadan geliyor, yani range_s ya yok sayılırdı
	// ya da modeli kimliğin zaten söylediği pencereyi tahmin etmeye
	// davet ederdi.
	for _, name := range []string{"list_operations", "list_environments", "find_trace_by_request_id"} {
		tool := toolByName(t, tools, name)
		if _, ok := schemaProps(t, tool)["range_s"]; ok {
			t.Errorf("%s: range_s property kazandı ama okuması pencere ALMIYOR — ya kaldır ya okumayı pencerele", name)
		}
	}
}

func TestListOperationsSchema(t *testing.T) {
	tool := toolByName(t, ToolList(Deps{}), "list_operations")
	props := schemaProps(t, tool)

	svc, ok := props["service"].(map[string]any)
	if !ok || svc["type"] != "string" {
		t.Fatalf("service property eksik/yanlış tip: %v", props["service"])
	}
	req, _ := tool.InputSchema["required"].([]string)
	if len(req) != 1 || req[0] != "service" {
		t.Fatalf("required = %v, want [service] (10k servislik kurulumda filo-geneli op listesi kullanılamaz)", req)
	}
	lim, _ := props["limit"].(map[string]any)
	if lim["maximum"] != 500 {
		t.Fatalf("limit maximum = %v, want 500", lim["maximum"])
	}
	// Katalog semantiği açıklamada OLMALI: "canlı mı" sorusuna cevap
	// vermediğini model bilmezse emekli bir op'u yaşıyor sanır.
	d := strings.ToLower(tool.Description)
	for _, want := range []string{"catalogue", "retention", "span names"} {
		if !strings.Contains(d, want) {
			t.Errorf("açıklama %q geçmiyor — katalog/pencere semantiği gizli kalır", want)
		}
	}
}

func TestListEnvironmentsSchema(t *testing.T) {
	tool := toolByName(t, ToolList(Deps{}), "list_environments")
	props := schemaProps(t, tool)
	if _, ok := props["pattern"].(map[string]any); !ok {
		t.Fatal("pattern property eksik (aramanın pencereyi 1h→24h genişletmesi tek keşif yolu)")
	}
	lim, _ := props["limit"].(map[string]any)
	if lim["maximum"] != 200 {
		t.Fatalf("limit maximum = %v, want 200 (store tavanı)", lim["maximum"])
	}
	// env DEĞER SÖZLÜĞÜ açıklamada olmalı — env arg'ı taşıyan tool'ların
	// (v0.8.398) testiyle aynı gerekçe: küçük model 'production-eu-1' uydurur.
	if !strings.Contains(tool.Description, "uat") {
		t.Errorf("açıklama int/uat/prep tarzı değer sözlüğünü öğretmiyor: %q", tool.Description)
	}
	if req, ok := tool.InputSchema["required"]; ok {
		if rs, _ := req.([]string); len(rs) != 0 {
			t.Errorf("required = %v — keşif tool'u argümansız çağrılabilmeli", rs)
		}
	}
}

func TestListClustersSchemaCarriesStoreClamp(t *testing.T) {
	tool := toolByName(t, ToolList(Deps{}), "list_clusters")
	rs, ok := schemaProps(t, tool)["range_s"].(map[string]any)
	if !ok {
		t.Fatal("range_s property eksik")
	}
	// Kelepçe ŞEMADA: LLM'e 24h sorup 1h cevap almayı keşfettirmiyoruz
	// (clampClusterFrom / clusterScanWindow, v0.8.188 prod timeout'u).
	if rs["maximum"] != clusterEnumRangeS {
		t.Fatalf("range_s maximum = %v, want %d (store clusterScanWindow'a kelepçeliyor)", rs["maximum"], clusterEnumRangeS)
	}
}

func TestListDeploysSchemaAndImpactBoundary(t *testing.T) {
	tool := toolByName(t, ToolList(Deps{}), "list_deploys")
	props := schemaProps(t, tool)
	if req, ok := tool.InputSchema["required"]; ok {
		if rs, _ := req.([]string); len(rs) != 0 {
			t.Errorf("required = %v — service OPSİYONEL (filo-geneli 'ne değişti' ana kullanım)", rs)
		}
	}
	rs, _ := props["range_s"].(map[string]any)
	if rs["maximum"] != 604800 {
		t.Fatalf("range_s maximum = %v, want 604800", rs["maximum"])
	}
	lim, _ := props["limit"].(map[string]any)
	if lim["maximum"] != 100 {
		t.Fatalf("limit maximum = %v, want 100", lim["maximum"])
	}
	// İŞ BÖLÜMÜ açıklamada olmalı: etkiyi bu tool HESAPLAMAZ, ölçüm
	// get_deploy_diff'in işi; impact_ready yalnız "şimdi sormak anlamlı mı".
	d := tool.Description
	if !strings.Contains(d, "get_deploy_diff") || !strings.Contains(d, "does NOT compute") {
		t.Errorf("açıklama iş bölümünü söylemiyor (etki get_deploy_diff'in işi): %q", d)
	}
	if !strings.Contains(d, "impact_ready") {
		t.Error("açıklama impact_ready bayrağının anlamını taşımıyor — alanı veri olarak vermek yetmez")
	}
}

func TestFindTraceBySpanSchema(t *testing.T) {
	tool := toolByName(t, ToolList(Deps{}), "find_trace_by_span")
	props := schemaProps(t, tool)
	req, _ := tool.InputSchema["required"].([]string)
	if len(req) != 1 || req[0] != "span_id" {
		t.Fatalf("required = %v, want [span_id]", req)
	}
	rs, _ := props["range_s"].(map[string]any)
	// İNDEKSSİZ tarama: 7 günlük genel tavan burada yanlış sinyal.
	if rs["maximum"] != spanScanMaxRangeS {
		t.Fatalf("range_s maximum = %v, want %d (span_id indekssiz, pencere maliyetin tek sınırı)", rs["maximum"], spanScanMaxRangeS)
	}
	d := tool.Description
	if !strings.Contains(d, "found=false") {
		t.Error("açıklama bulunamamanın HATA OLMADIĞINI söylemiyor — model boş sonucu 'trace yok' sanar")
	}
}

// ─── saf üreticiler / kelepçeler ───────────────────────────────

func TestEnvEnumWindowS(t *testing.T) {
	cases := []struct {
		name    string
		pattern string
		want    int
	}{
		{"aramasız: 1 saat", "", 3600},
		{"boşluk = aramasız", "   ", 3600},
		{"arama: 24 saat", "release", 86400},
		{"tek harf de arama", "u", 86400},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := envEnumWindowS(tc.pattern); got != tc.want {
				t.Fatalf("envEnumWindowS(%q) = %d, want %d", tc.pattern, got, tc.want)
			}
		})
	}
}

func TestClusterEnumWindowS(t *testing.T) {
	cases := []struct {
		in, want int
	}{
		{0, 3600},      // varsayılan
		{-10, 3600},    // negatif → varsayılan
		{300, 300},     // daraltma GEÇERLİ
		{3600, 3600},   // sınır
		{7200, 3600},   // genişletme kelepçelenir (store zaten kelepçeliyor)
		{604800, 3600}, // 7 gün de 1 saate iner
	}
	for _, tc := range cases {
		if got := clusterEnumWindowS(tc.in); got != tc.want {
			t.Errorf("clusterEnumWindowS(%d) = %d, want %d", tc.in, got, tc.want)
		}
	}
}

func TestDeployEnumWindowS(t *testing.T) {
	cases := []struct {
		name     string
		in, want int
	}{
		// guidedDeployBundle gerekçesi: "son deploy" nadiren son yarım
		// saattedir — 30dk'lık sohbet varsayılanı boş liste demek olurdu.
		{"varsayılan 6 saat", 0, 21600},
		{"negatif → 6 saat", -1, 21600},
		{"sohbetin 30dk'sı tabana çekilir", 1800, 21600},
		{"6 saat sınırı", 21600, 21600},
		{"12 saat geçer", 43200, 43200},
		{"7 gün sınırı", 604800, 604800},
		{"30 gün kelepçelenir", 30 * 86400, 604800},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := deployEnumWindowS(tc.in); got != tc.want {
				t.Fatalf("deployEnumWindowS(%d) = %d, want %d", tc.in, got, tc.want)
			}
		})
	}
}

func TestSpanScanWindowS(t *testing.T) {
	cases := []struct {
		in, want int
	}{
		{0, 1800}, {-5, 1800}, {60, 60}, {86400, 86400},
		{86401, 86400}, {604800, 86400},
	}
	for _, tc := range cases {
		if got := spanScanWindowS(tc.in); got != tc.want {
			t.Errorf("spanScanWindowS(%d) = %d, want %d", tc.in, got, tc.want)
		}
	}
}

func TestOperationsPayloadTruncation(t *testing.T) {
	cases := []struct {
		name      string
		names     []string
		total     int
		truncated bool
	}{
		{"tam liste", []string{"a", "b"}, 2, false},
		{"kesme ifşa edilir", []string{"a", "b"}, 4000, true},
		{"boş servis", []string{}, 0, false},
		// total < len olamaz ama olursa da yalan söylememeli.
		{"tutarsız total sessiz kalmaz", []string{"a", "b"}, 1, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := operationsPayload("checkout", tc.names, tc.total)
			if got["truncated"] != tc.truncated {
				t.Fatalf("truncated = %v, want %v", got["truncated"], tc.truncated)
			}
			if got["count"] != len(tc.names) {
				t.Fatalf("count = %v, want %d", got["count"], len(tc.names))
			}
			if got["service"] != "checkout" {
				t.Fatalf("service echo = %v", got["service"])
			}
			if _, ok := got["total"]; !ok {
				t.Fatal("total alanı yok — model kaç tanesini görmediğini bilemez")
			}
		})
	}
}

func TestEnvironmentsPayloadEnvelope(t *testing.T) {
	full := environmentsPayload([]string{"prod", "uat"}, 2, "")
	if full["truncated"] != false || full["window_s"] != 3600 {
		t.Fatalf("aramasız tam liste: %v", full)
	}
	cut := environmentsPayload([]string{"prod", "uat"}, 9, "u")
	if cut["truncated"] != true {
		t.Fatal("total > sayfa iken truncated=true olmalı")
	}
	if cut["window_s"] != 86400 {
		t.Fatalf("aramalı window_s = %v, want 86400 (store 24h'e genişletiyor)", cut["window_s"])
	}
}

func TestClustersPayloadCapExposed(t *testing.T) {
	small := clustersPayload([]string{"ocp-prod"}, 3600)
	if small["truncated"] != false {
		t.Fatal("tek kümede truncated=false olmalı")
	}
	names := make([]string, clusterEnumCap)
	for i := range names {
		names[i] = "c"
	}
	capped := clustersPayload(names, 3600)
	if capped["truncated"] != true {
		t.Fatalf("SQL LIMIT'ine dayanan sonuç truncated=true olmalı (store total döndürmüyor)")
	}
}

func TestDeployRowsOrderCapAndImpactReady(t *testing.T) {
	now := time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC)
	mk := func(svc string, ago time.Duration) deployRow {
		return deployRow{Service: svc, Version: "v-" + svc, TimeUnixNs: now.Add(-ago).UnixNano()}
	}
	rows := []deployRow{
		mk("orta", 30*time.Minute),
		mk("eski", 4*time.Hour),
		mk("yeni", 2*time.Minute),
	}

	got := deployRows(rows, now, 10)
	wantOrder := []string{"yeni", "orta", "eski"}
	if len(got) != 3 {
		t.Fatalf("len = %d, want 3", len(got))
	}
	for i, w := range wantOrder {
		if got[i].Service != w {
			t.Fatalf("sıra[%d] = %s, want %s (en yeni önce)", i, got[i].Service, w)
		}
	}
	// ISO damgası ISO olmalı — model kendi tarih aritmetiğini kurmasın.
	if _, err := time.Parse(time.RFC3339, got[0].TimeISO); err != nil {
		t.Fatalf("time_iso ayrıştırılamıyor (%q): %v", got[0].TimeISO, err)
	}

	// impact_ready eşiği: ±10dk pencerenin "after" tarafı dolmuş mu.
	// Sınır DEĞERLER kritik — value+unit sınıfı burada da geçerli.
	boundary := []struct {
		name string
		ago  time.Duration
		want bool
	}{
		{"1 dakika önce — ölçüm hazır DEĞİL", time.Minute, false},
		{"9m59s — hazır değil", deployImpactWindowS*time.Second - time.Second, false},
		{"tam 10 dakika — hazır", deployImpactWindowS * time.Second, true},
		{"1 saat önce — hazır", time.Hour, true},
	}
	for _, tc := range boundary {
		t.Run(tc.name, func(t *testing.T) {
			out := deployRows([]deployRow{mk("s", tc.ago)}, now, 5)
			if out[0].ImpactReady != tc.want {
				t.Fatalf("ImpactReady = %v, want %v", out[0].ImpactReady, tc.want)
			}
		})
	}

	// Tavan: limit uygulanır ve girdi MUTASYONA UĞRAMAZ (çağıran
	// has_more'u len(rows) ile karşılaştırıyor — girdi kırpılırsa zarf yalan söyler).
	capped := deployRows(rows, now, 2)
	if len(capped) != 2 || capped[0].Service != "yeni" {
		t.Fatalf("limit=2 → %v", capped)
	}
	if len(rows) != 3 || rows[0].Service != "orta" {
		t.Fatalf("girdi dilimi değişti: %v", rows)
	}
}

func TestDeploysPayloadHasMore(t *testing.T) {
	now := time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC)
	mk := func(i int) deployRow {
		return deployRow{Service: "s", Version: "v", TimeUnixNs: now.Add(-time.Duration(i) * time.Hour).UnixNano()}
	}
	three := []deployRow{mk(1), mk(2), mk(3)}

	full := deploysPayload(three, now, 21600, 10, false)
	if full["has_more"] != false {
		t.Fatal("her şey döndüyse has_more=false")
	}
	if full["impact_window_s"] != deployImpactWindowS {
		t.Fatalf("impact_window_s = %v, want %d", full["impact_window_s"], deployImpactWindowS)
	}
	if full["window_s"] != 21600 {
		t.Fatalf("window_s = %v", full["window_s"])
	}
	// (a) benim limitim kırptı
	if got := deploysPayload(three, now, 21600, 2, false); got["has_more"] != true {
		t.Fatal("limit kırptıysa has_more=true")
	}
	// (b) okumanın KENDİ tavanı doldu (GetRecentDeploys limit /
	// serviceDeploysSQL LIMIT 50) — sessiz kalmamalı.
	if got := deploysPayload(three, now, 21600, 10, true); got["has_more"] != true {
		t.Fatal("store tavanı dolduysa has_more=true (sessiz tavan yok)")
	}
}

// BULUNAMAMAK HATA DEĞİL (guidedSpanBundle, v0.9.548). Üç olası neden
// gövdede olmalı ve biri "pencereyi genişlet" olmalı — yoksa model
// yapıştırılan doğru bir id için "böyle bir span yok" der.
func TestSpanLookupPayload(t *testing.T) {
	t.Run("bulundu", func(t *testing.T) {
		got := spanLookupPayload("00000000000000ab", "1234567890abcdef1234567890abcdef", 1800)
		if got["found"] != true {
			t.Fatal("found=true olmalı")
		}
		if got["trace_id"] != "1234567890abcdef1234567890abcdef" {
			t.Fatalf("trace_id = %v", got["trace_id"])
		}
		if _, ok := got["reasons"]; ok {
			t.Fatal("bulunduğunda reasons taşınmamalı")
		}
		if next, _ := got["next"].(string); !strings.Contains(next, "get_trace") {
			t.Fatalf("next zincirin sonraki halkasını söylemeli: %v", got["next"])
		}
	})
	t.Run("bulunamadı", func(t *testing.T) {
		got := spanLookupPayload("00000000000000ab", "", 900)
		if got["found"] != false {
			t.Fatal("found=false olmalı")
		}
		if got["trace_id"] != nil {
			t.Fatalf("bulunamayan aramada trace_id TAŞINMAMALI (uydurma zemini): %v", got["trace_id"])
		}
		reasons, ok := got["reasons"].([]string)
		if !ok || len(reasons) != 3 {
			t.Fatalf("reasons = %v — üç olası neden bekleniyor", got["reasons"])
		}
		joined := strings.ToLower(strings.Join(reasons, " | "))
		for _, want := range []string{"range_s", "retention", "16 hex"} {
			if !strings.Contains(joined, want) {
				t.Errorf("nedenler %q geçmiyor: %s", want, joined)
			}
		}
		if got["window_s"] != 900 {
			t.Fatalf("window_s echo = %v, want 900 (model neyi genişleteceğini bilmeli)", got["window_s"])
		}
	})
}

// ─── kaynak pini (BAĞLANMA) ────────────────────────────────────

// stripLineComments — tam satır `//` yorumlarını atar. Yorumlu satırları
// ayıklamadan kaynak taraması yapmak bu depoda yanmış bir sınıf: gerekçe
// yorumu aranan dizeyi içerdiği için kapı KÖR koşuyordu.
func stripLineComments(src string) string {
	var b strings.Builder
	for _, line := range strings.Split(src, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "//") {
			continue
		}
		b.WriteString(line)
		b.WriteString("\n")
	}
	return b.String()
}

// Handler'lar gövdeyi SAF üreticiden döndürmeli. Saf testler zarfı
// doğrular ama handler map'i satır içinde kurup zarfı düşürürse hepsi
// yeşil kalır — bu pin o boşluğu kapatır.
func TestDiscoveryHandlersReturnViaPurePayloads(t *testing.T) {
	b, err := os.ReadFile("discovery.go")
	if err != nil {
		t.Fatalf("discovery.go okunamadı: %v", err)
	}
	src := stripLineComments(string(b))
	for _, want := range []string{
		"return operationsPayload(service, names, total), nil",
		"return environmentsPayload(names, total, a.Pattern), nil",
		"return clustersPayload(names, windowS), nil",
		"return deploysPayload(rows, now, windowS, limit, storeCapped), nil",
		"return spanLookupPayload(spanID, traceID, windowS), nil",
	} {
		if !strings.Contains(src, want) {
			t.Errorf("handler saf üreticiden dönmüyor — bekleniyor: %s", want)
		}
	}
}

// MUTASYON PİNİ — find_trace_by_span'in bulunamama yolu HATA döndürmeye
// dönerse burası kırmızı. (Şekil hatası ayrı: 16-hex olmayan id taramaya
// bile girmeden hata döner, o self-correct edilebilir çağıran hatasıdır.)
func TestFindTraceBySpanNotFoundIsNotAnError(t *testing.T) {
	b, err := os.ReadFile("discovery.go")
	if err != nil {
		t.Fatalf("discovery.go okunamadı: %v", err)
	}
	src := stripLineComments(string(b))
	i := strings.Index(src, "FindTraceIDBySpan(ctx")
	if i < 0 {
		t.Fatal("FindTraceIDBySpan çağrısı bulunamadı — tool başka bir okumaya mı bağlandı?")
	}
	tail := src[i:]
	if !strings.Contains(tail, "spanLookupPayload(") {
		t.Fatal("arama sonrası gövde spanLookupPayload'dan geçmiyor")
	}
	// Aramadan SONRA tek meşru dönüş yolu payload'dır; Errorf burada
	// "bulunamadı = hata" mutasyonunun imzasıdır.
	if strings.Contains(tail, "Errorf") {
		t.Error("arama SONRASINDA hata üretiliyor — bulunamamak dürüst kanıttır (found=false), hata değil")
	}
}

// Keşif tool'ları katalogda TÜKETİCİLERİNİN yanında durmalı: model
// arg'ı gördüğü sırada listesini de görsün (tools/list sıralı gelir).
func TestDiscoveryToolsGroupedNearConsumers(t *testing.T) {
	tools := ToolList(Deps{})
	pos := map[string]int{}
	for i, tool := range tools {
		pos[tool.Name] = i
	}
	pairs := []struct{ discovery, consumer string }{
		{"list_environments", "list_problems"},   // env arg'ı taşıyan üçlünün hemen ardı
		{"list_clusters", "search_logs"},         // cluster arg'ı
		{"list_operations", "list_metric_names"}, // katalog ikizi
		{"find_trace_by_span", "search_traces"},  // trace id'ye iki giriş
		{"list_deploys", "get_deploy_diff"},      // sürümü BULMANIN yolu
		// v0.9.1142 — yapıştırılan kimlik ailesi yan yana dursun.
		{"find_trace_by_request_id", "find_trace_by_span"},
	}
	for _, p := range pairs {
		di, dok := pos[p.discovery]
		ci, cok := pos[p.consumer]
		if !dok || !cok {
			t.Fatalf("katalogda eksik: %s=%v %s=%v", p.discovery, dok, p.consumer, cok)
		}
		if diff := di - ci; diff > 3 || diff < -3 {
			t.Errorf("%s, %s'ten %d sıra uzakta — keşif tool'u tüketicisinin yanında dursun", p.discovery, p.consumer, diff)
		}
	}
	// v0.9.1146 — 25 → 28 (analysis.go: get_topology / get_blast_radius /
	// get_log_histogram); v0.9.1147 — 28 → 32 (guided_parity.go:
	// get_db_health / get_messaging_health / get_pod_health /
	// list_problem_window_events); v0.9.1227 — 32 → 33 (get_operation_
	// health); v0.9.1233 — 33 → 34 (get_exception_samples); v0.9.1244 —
	// 34 → 36 (team_ownership.go: list_teams / get_team_services). Sayı ÜÇ
	// yerde daha yazılı ve üçü de bu testle birlikte güncellenir; aksi
	// hâlde katalog büyürken doküman ve duruş notu sessizce bayatlar.
	// v0.10.468 — 36 → 39 (entity_catalog.go: list_namespaces / list_workloads / list_pods).
	// v0.10.469 — 39 → 40 (resolve_entity.go).
	// v0.10.472 — 40 → 42 (attr_discovery.go).
	// v0.10.474 — 42 → 43 (trace_stats.go).
	if len(tools) != 43 {
		t.Errorf("katalog %d tool — sayı değiştiyse tools.go başlığındaki sayım yorumunu, "+
			"api/mcp_authz_test.go'daki duruş notunu ve docs/runbooks/mcp-claude-code.md'yi de güncelle", len(tools))
	}
}
