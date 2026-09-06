package mcptools

// guided_parity_test.go — v0.9.1147 (AI Faz 3.4). Dört guided-parite
// tool'unun sözleşmesi, analysis_test.go'nun dört katmanıyla aynı düzende:
//
//	(a) katalogda kayıtlı + MinRole "" + her property açıklamalı,
//	(b) pencere/tavan/env semantiği ŞEMADA dürüst (kabul-edilip-yok-sayılan
//	    arg yok; env'in NEDEN olmadığı tool başına pinli),
//	(c) dürüstlük zarfları (truncated / store_capped / reasons / lag /
//	    restart) SAF üreticilerde tablo testli,
//	(d) handler'ların o üreticilerden VE doğru okuyucudan döndüğü kaynak
//	    pini ile bağlı — saf test tek başına BAĞLANMA kanıtı değildir.
//
// Ek olarak bu dosya ORTAK KATMAN sözleşmesini de pinliyor: guided
// tarafının (api/copilot_*.go) tükettiği yapısal alanlar burada test
// ediliyor, yani metin renderer'ları bozulmadan önce şekil bozulursa
// burada yanar.

import (
	"math"
	"os"
	"strings"
	"testing"

	"github.com/cilcenk/coremetry/internal/chstore"
)

// guidedParityToolNames — Faz 3.4'ün dördü.
var guidedParityToolNames = []string{
	"get_db_health", "get_messaging_health", "get_pod_health", "list_problem_window_events",
}

func TestGuidedParityToolsRegistered(t *testing.T) {
	tools := ToolList(Deps{}) // Deps{} güvenli: ToolList yalnız closure kurar.
	for _, name := range guidedParityToolNames {
		t.Run(name, func(t *testing.T) {
			tool := toolByName(t, tools, name)
			// MinRole AÇIKÇA "": dördü de salt-okunur ve REST eşleri
			// kapısız (/api/databases, /api/messaging,
			// /api/services/{name}/instances, /api/annotations).
			if tool.MinRole != "" {
				t.Fatalf("%s: MinRole=%q — guided-parite tool'ları viewer tabanında olmalı (REST eşleri kapısız)", name, tool.MinRole)
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

// ENV KARARI — dördünde de env arg'ı YOK ve sebep tool başına farklı
// (dosya başlığı). Bir gelecek düzenleme "tutarlılık için" env eklerse
// sessizce ya ham-spans taramasına ya no-op filtreye düşer.
func TestGuidedParityToolsDeclareNoEnvArg(t *testing.T) {
	tools := ToolList(Deps{})
	for _, name := range guidedParityToolNames {
		props := schemaProps(t, toolByName(t, tools, name))
		if _, has := props["env"]; has {
			t.Errorf("%s: env arg'ı eklenmiş — get_db_health'te MV'yi diskalifiye eder (ham spans), "+
				"messaging/pod'da süzülecek boyut YOK, pencere okuması filtre almıyor. Karar guided_parity.go başlığında", name)
		}
	}
}

// Tool'lar tüketicilerinin/ikizlerinin yanında durmalı (tools/list sıralı
// gelir; model komşuluğu okur).
func TestGuidedParityToolsGroupedNearFamilies(t *testing.T) {
	tools := ToolList(Deps{})
	pos := map[string]int{}
	for i, tool := range tools {
		pos[tool.Name] = i
	}
	pairs := []struct{ tool, neighbour string }{
		// Servis RED'inin ardındaki altyapı sorusu.
		{"get_pod_health", "get_service_health"},
		// Topolojinin database/queue düğümlerinin sağlık ikizleri.
		{"get_db_health", "get_topology"},
		{"get_messaging_health", "get_db_health"},
		// list_problems'in ZAMAN ikizi — ayrı düşerse model kapanmış bir
		// olayı "yok" sanır.
		{"list_problem_window_events", "list_problems"},
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
	// v0.9.1147 — 28 → 32; v0.9.1227 — 32 → 33 (operation_health.go:
	// get_operation_health); v0.9.1233 — 33 → 34 (exception_samples.go:
	// get_exception_samples); v0.9.1244 — 34 → 36 (team_ownership.go:
	// list_teams / get_team_services). Sayı ÜÇ yerde daha yazılı (tools.go
	// başlığı, api/mcp_authz_test.go duruş notu,
	// docs/runbooks/mcp-claude-code.md) ve discovery_test.go da aynı sayıyı
	// pinliyor.
	// v0.10.468 — 36 → 39 (entity_catalog.go: list_namespaces / list_workloads / list_pods).
	// v0.10.469 — 39 → 40 (resolve_entity.go).
	if len(tools) != 40 {
		t.Errorf("katalog %d tool — sayı değiştiyse tools.go başlığındaki sayımı, "+
			"api/mcp_authz_test.go'daki duruş notunu, discovery_test.go'daki pini ve "+
			"docs/runbooks/mcp-claude-code.md'yi de güncelle", len(tools))
	}
}

// ── şema dürüstlüğü ────────────────────────────────────────────

func TestGetDBHealthSchemaDeclaresBucketFloorAndFleetScope(t *testing.T) {
	tool := toolByName(t, ToolList(Deps{}), "get_db_health")
	props := schemaProps(t, tool)
	rangeProp, _ := props["range_s"].(map[string]any)
	if rangeProp == nil {
		t.Fatal("range_s property yok")
	}
	if max, _ := rangeProp["maximum"].(int); max != dbHealthMaxRangeS {
		t.Errorf("range_s maximum=%v — dbHealthMaxRangeS (%d) olmalı", rangeProp["maximum"], dbHealthMaxRangeS)
	}
	desc, _ := rangeProp["description"].(string)
	if !strings.Contains(desc, "300") {
		t.Error("range_s açıklaması 5 dakikalık kova TABANINI söylemiyor — LLM 60s isteyip 300s alır ve farkı bilmez")
	}
	if _, has := props["service"]; has {
		t.Error("service arg'ı eklenmiş — bu okuma FİLO GENELİ (db_summary_5m'de çağıran kırılımı ayrı MV turu); " +
			"servis vaadi süzülmeyen bir filtre olurdu")
	}
	if !strings.Contains(tool.Description, "FLEET-WIDE") {
		t.Error("açıklama kapsamın filo geneli olduğunu SÖYLEMİYOR")
	}
	if !strings.Contains(tool.Description, "5-minute bucket") {
		t.Error("açıklama kova yuvarlamasını söylemiyor — sayılar istenenden ~5dk geniş olabilir")
	}
}

func TestGetMessagingHealthSchemaDeclaresLagAbsence(t *testing.T) {
	tool := toolByName(t, ToolList(Deps{}), "get_messaging_health")
	// Lag YOKLUĞU açıklamada BÜYÜK harfle: modelin en olası halüsinasyonu
	// tam olarak bir lag sayısı uydurmak (broker metrikleri ingest
	// edilmiyor — memory: feedback-no-db-engine-metrics).
	if !strings.Contains(tool.Description, "CONSUMER LAG IS NOT AVAILABLE") {
		t.Error("açıklama consumer-lag'in ÖLÇÜLMEDİĞİNİ söylemiyor — model lag uydurur")
	}
	if !strings.Contains(tool.Description, "produce_p95_ms") || !strings.Contains(tool.Description, "consume_p95_ms") {
		t.Error("açıklama publish/process gecikme ayrışmasını anlatmıyor (v0.9.816)")
	}
	props := schemaProps(t, tool)
	limitProp, _ := props["limit"].(map[string]any)
	if desc, _ := limitProp["description"].(string); !strings.Contains(desc, "200") {
		t.Error("limit açıklaması STORE tavanını (200 destination) söylemiyor — 200 gerçekten ulaşılabilir bir tavan")
	}
}

func TestGetPodHealthSchemaDeclaresSplitWindow(t *testing.T) {
	tool := toolByName(t, ToolList(Deps{}), "get_pod_health")
	props := schemaProps(t, tool)
	rangeProp, _ := props["range_s"].(map[string]any)
	if rangeProp == nil {
		t.Fatal("range_s property yok")
	}
	desc, _ := rangeProp["description"].(string)
	for _, want := range []string{"INVENTORY", "Ignored entirely in fleet mode", "live 10 minutes"} {
		if !strings.Contains(desc, want) {
			t.Errorf("range_s açıklaması %q demiyor — arg'ın NEREYE etki ettiği belirsiz kalır (kabul edilip yok sayılan arg yasağı)", want)
		}
	}
	if max, _ := rangeProp["maximum"].(int); max != podHealthMaxRangeS {
		t.Errorf("range_s maximum=%v — podHealthMaxRangeS (%d) olmalı", rangeProp["maximum"], podHealthMaxRangeS)
	}
	if !strings.Contains(tool.Description, "RESTART COUNTS AND POD PHASE ARE NOT HERE") {
		t.Error("açıklama restart/faz yokluğunu söylemiyor — KSM/Thanos işi, model uydurur")
	}
	if !strings.Contains(tool.Description, "post_gc_pct") {
		t.Error("açıklama post-GC sinyalini anlatmıyor — testere-dişi heap'te used/max sağlıklı pod'da bile %85+ (v0.9.426)")
	}
}

func TestListProblemWindowEventsSchemaDeclaresResolvedInclusion(t *testing.T) {
	tool := toolByName(t, ToolList(Deps{}), "list_problem_window_events")
	for _, want := range []string{"RESOLVED ONES INCLUDED", "list_problems", "No env argument"} {
		if !strings.Contains(tool.Description, want) {
			t.Errorf("açıklama %q demiyor — bu tool'un list_problems'ten FARKI (zaman ikizi) ve env yokluğu açıkça yazılmalı", want)
		}
	}
	props := schemaProps(t, tool)
	rangeProp, _ := props["range_s"].(map[string]any)
	desc, _ := rangeProp["description"].(string)
	if !strings.Contains(desc, "43200") {
		t.Error("range_s açıklaması 12 saatlik VARDİYA varsayılanını söylemiyor")
	}
	if !strings.Contains(desc, "no bucket rounding") {
		t.Error("range_s açıklaması pencerenin TAM olduğunu söylemiyor — diğer üç tool kovaya yuvarlıyor, fark önemli")
	}
	if svc, _ := props["service"].(map[string]any); svc != nil {
		if d, _ := svc["description"].(string); !strings.Contains(d, "global") {
			t.Error("service açıklaması servise daraltmanın GLOBAL (servissiz) kuralları düşürdüğünü söylemiyor")
		}
	}
}

// ── pencere kelepçeleri (saf) ──────────────────────────────────

func TestDepWindowSClamps(t *testing.T) {
	cases := []struct {
		name string
		fn   func(int) int
		in   int
		want int
	}{
		{"db varsayılan", dbHealthWindowS, 0, dbHealthDefaultRangeS},
		{"db negatif de varsayılan", dbHealthWindowS, -5, dbHealthDefaultRangeS},
		{"db kova tabanı", dbHealthWindowS, 60, depBucketS},
		{"db tam kova geçer", dbHealthWindowS, 300, 300},
		{"db aradaki değer korunur", dbHealthWindowS, 7200, 7200},
		{"db 7 gün üstü kelepçe", dbHealthWindowS, 30 * 86400, dbHealthMaxRangeS},
		{"msg varsayılan", msgHealthWindowS, 0, msgHealthDefaultRangeS},
		{"msg kova tabanı", msgHealthWindowS, 1, depBucketS},
		{"msg 7 gün üstü kelepçe", msgHealthWindowS, 30 * 86400, msgHealthMaxRangeS},
		{"pod varsayılan", podHealthWindowS, 0, podHealthDefaultRangeS},
		// Pod'da kova TABANI yok: envanter ham metric_points, hizalama yok.
		{"pod dar pencere korunur", podHealthWindowS, 60, 60},
		{"pod 24h üstü kelepçe", podHealthWindowS, 7 * 86400, podHealthMaxRangeS},
		{"pencere olayları varsayılan 12h", pwWindowS, 0, pwDefaultRangeS},
		{"pencere olayları dar pencere korunur", pwWindowS, 300, 300},
		{"pencere olayları 7 gün kelepçe", pwWindowS, 30 * 86400, pwMaxRangeS},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := c.fn(c.in); got != c.want {
				t.Errorf("%d → %d, beklenen %d", c.in, got, c.want)
			}
		})
	}
}

// ── db: saf şekillendirme ──────────────────────────────────────

func TestDBHealthDataOrderCapAndFlags(t *testing.T) {
	ov := &chstore.DatabasesOverview{
		Rows: []chstore.DBInstance{
			{System: "postgresql", Instance: "pg-b", SpanCount: 10, P95Ms: 5},
			{System: "oracle", Instance: "ora-1", SpanCount: 100, ErrorCount: 3, ErrorRate: 3, AvgMs: 12.5, P95Ms: 90},
			// Eşit calls → tiebreak sistem adına göre (CH sırası garantili değil).
			{System: "redis", Instance: "r-1", SpanCount: 10, P95Ms: 1},
			{System: "postgresql", Instance: "pg-a", SpanCount: 10, P95Ms: 40},
		},
		RowsCapped: true,
		RowLimit:   5000,
	}
	data := dbHealthData(ov, 3)
	if data.Total != 4 {
		t.Errorf("Total=%d, beklenen 4 (kesme ÖNCESİ sayı)", data.Total)
	}
	if !data.Truncated {
		t.Error("Truncated false — 4 satır 3'e kesildi")
	}
	if !data.StoreCapped || data.StoreRowLimit != 5000 {
		t.Errorf("store tavanı taşınmadı: capped=%v limit=%d", data.StoreCapped, data.StoreRowLimit)
	}
	if len(data.Rows) != 3 {
		t.Fatalf("%d satır, beklenen 3", len(data.Rows))
	}
	if data.Rows[0].Instance != "ora-1" {
		t.Errorf("en yoğun ilk değil: %s", data.Rows[0].Instance)
	}
	// 10'luk üç satırın sırası: postgresql/pg-a, postgresql/pg-b, redis/r-1
	// (system → instance → db_name). Kesme 3'te olduğu için ikisi görünür.
	if data.Rows[1].Instance != "pg-a" || data.Rows[2].Instance != "pg-b" {
		t.Errorf("eşit calls'ta tiebreak uygulanmamış: %s, %s", data.Rows[1].Instance, data.Rows[2].Instance)
	}
	// Ham ondalık KORUNUR: yuvarlama yalnız JSON tarafında (guided metni
	// %.1f basıyor; ön-yuvarlanmış değer kenar durumda BAŞKA dize üretir).
	if data.Rows[0].AvgMs != 12.5 {
		t.Errorf("AvgMs=%v — ortak katman ham ölçümü taşımalı", data.Rows[0].AvgMs)
	}
	if got := dbHealthData(nil, 3); got.Total != 0 || got.StoreRowLimit != dbHealthStoreRowLimit {
		t.Errorf("nil zarf güvenli değil: %+v", got)
	}
}

func TestDBHealthSlowestByP95(t *testing.T) {
	rows := []DBHealthRow{
		{Instance: "a", P95Ms: 10},
		{Instance: "b", P95Ms: 90},
		{Instance: "c", P95Ms: 50},
	}
	got := DBHealthSlowestByP95(rows, 2)
	if len(got) != 2 || got[0].Instance != "b" || got[1].Instance != "c" {
		t.Errorf("p95 sırası yanlış: %+v", got)
	}
	// Girdi MUTASYONA UĞRAMAMALI: guided aynı dilimi hem satır listesi hem
	// "en yavaşlar" için kullanıyor, sıralama sızarsa satırlar p95'e göre
	// basılır ve hacim bağlamı kaybolur.
	if rows[0].Instance != "a" {
		t.Error("girdi dilimi mutasyona uğradı — çağıranın satır sırası bozulur")
	}
	if got := DBHealthSlowestByP95(nil, 3); len(got) != 0 {
		t.Errorf("boş girdi %d satır döndürdü", len(got))
	}
}

func TestSanitizeDBHealthRowsDropsNonFinite(t *testing.T) {
	rows := []DBHealthRow{{
		Instance: "x", AvgMs: math.Inf(1), P95Ms: math.NaN(), P99Ms: 1.23456, ErrorRatePct: math.Inf(-1),
	}}
	out := sanitizeDBHealthRows(rows)
	if out[0].AvgMs != 0 || out[0].P95Ms != 0 || out[0].ErrorRatePct != 0 {
		t.Errorf("sonlu olmayan değer sızdı: %+v — encoding/json TÜM çağrıyı hataya çevirir", out[0])
	}
	if out[0].P99Ms != 1.23 {
		t.Errorf("P99Ms=%v, beklenen 1.23 (2 hane)", out[0].P99Ms)
	}
	if !math.IsInf(rows[0].AvgMs, 1) {
		t.Error("girdi mutasyona uğradı — guided metni ham değeri bekliyor")
	}
}

func TestDBHealthPayloadEnvelopes(t *testing.T) {
	t.Run("boş liste sebep taşır", func(t *testing.T) {
		body := dbHealthPayload(DBHealthData{}, 3600)
		if body["count"] != 0 {
			t.Errorf("count=%v", body["count"])
		}
		if _, ok := body["reasons"].([]string); !ok {
			t.Error("boş sonuçta reasons yok — boş sonuç HATA değil, model 'db yok' diye uydurmasın")
		}
		if body["window_bucket_s"] != depBucketS {
			t.Errorf("window_bucket_s=%v — kova boyutu gövdede olmalı", body["window_bucket_s"])
		}
		if body["scope"] != "fleet-wide" {
			t.Errorf("scope=%v — kapsam filo geneli", body["scope"])
		}
	})
	t.Run("kesme p95 kısayolunun KAPSAMINI söyler", func(t *testing.T) {
		body := dbHealthPayload(DBHealthData{
			Rows:  []DBHealthRow{{Instance: "a", Calls: 5, P95Ms: 3}},
			Total: 9, Truncated: true,
		}, 3600)
		note, _ := body["note"].(string)
		if !strings.Contains(note, "slowest_by_p95 is the slowest AMONG THOSE") {
			t.Errorf("note kesmenin p95 kısayolunu daralttığını söylemiyor: %q", note)
		}
		if slow, _ := body["slowest_by_p95"].([]DBHealthRow); len(slow) != 1 {
			t.Errorf("slowest_by_p95 %d satır", len(slow))
		}
	})
	t.Run("store tavanı alt-sınır ilan eder", func(t *testing.T) {
		body := dbHealthPayload(DBHealthData{
			Rows: []DBHealthRow{{Instance: "a"}}, Total: 5000,
			StoreCapped: true, StoreRowLimit: 5000,
		}, 3600)
		if body["store_capped"] != true {
			t.Error("store_capped taşınmadı")
		}
		if note, _ := body["note"].(string); !strings.Contains(note, "LOWER bound") {
			t.Errorf("tavan notu alt-sınır demiyor: %q", note)
		}
	})
}

// ── messaging: saf şekillendirme ───────────────────────────────

func TestMessagingHealthDataOrderCapAndCallers(t *testing.T) {
	ov := &chstore.MessagingOverview{
		Rows: []chstore.MessagingInstance{
			{System: "kafka", Cluster: "b", Destination: "t1", SpanCount: 5},
			{System: "kafka", Cluster: "a", Destination: "t2", SpanCount: 5, Callers: []string{"svc-a"}},
			{System: "kafka", Cluster: "z", Destination: "t3", SpanCount: 50,
				ProduceCount: 30, ConsumeCount: 20, ProduceP95Ms: 2, ConsumeP95Ms: 800},
		},
		RowsCapped: true, RowLimit: 200,
	}
	data := messagingHealthData(ov, 2)
	if data.Total != 3 || !data.Truncated || !data.StoreCapped || data.StoreRowLimit != 200 {
		t.Fatalf("zarf yanlış: %+v", data)
	}
	if data.Rows[0].Destination != "t3" {
		t.Errorf("en yoğun ilk değil: %s", data.Rows[0].Destination)
	}
	if data.Rows[1].Cluster != "a" {
		t.Errorf("eşit calls'ta cluster tiebreak'i uygulanmamış: %s", data.Rows[1].Cluster)
	}
	if data.Rows[0].ConsumeP95Ms != 800 || data.Rows[0].ProduceP95Ms != 2 {
		t.Error("kind ayrışması taşınmadı — karışık p95 yavaş tüketiciyi saklar (v0.9.816)")
	}
	// Callers KOPYALANIR: store dilimini paylaşmak çağıranın altında
	// değişebilen bir dilim bırakır.
	ov.Rows[1].Callers[0] = "MUTATED"
	if data.Rows[1].Callers != nil && len(data.Rows[1].Callers) > 0 && data.Rows[1].Callers[0] == "MUTATED" {
		t.Error("Callers dilimi paylaşılmış — kopyalanmalı")
	}
}

func TestMessagingPayloadAlwaysCarriesLagNote(t *testing.T) {
	for _, data := range []MessagingHealthData{
		{},
		{Rows: []MessagingHealthRow{{Destination: "t"}}, Total: 1},
		{Rows: []MessagingHealthRow{{Destination: "t"}}, Total: 9, Truncated: true},
	} {
		body := messagingHealthPayload(data, 3600)
		if body["lag_note"] != messagingLagNote {
			t.Fatalf("lag_note eksik/farklı: %v — her gövdede olmalı, model lag uydurmasın", body["lag_note"])
		}
	}
	if body := messagingHealthPayload(MessagingHealthData{}, 3600); body["reasons"] == nil {
		t.Error("boş sonuçta reasons yok")
	}
}

// ── pod: saf şekillendirme ─────────────────────────────────────

func TestPodInstanceRowsDownFirstThenCPU(t *testing.T) {
	in := []chstore.ServiceInstance{
		{ID: "p-cpu-low", CPUPct: 10, Up: true},
		{ID: "p-silent", Up: false},
		{ID: "p-cpu-high", CPUPct: 90, Up: true},
		{ID: "p-cpu-high-2", CPUPct: 90, Up: true},
	}
	rows, total, up, truncated := podInstanceRows(in, 3)
	if total != 4 || up != 3 || !truncated {
		t.Fatalf("sayaçlar yanlış: total=%d up=%d truncated=%v", total, up, truncated)
	}
	// SESSİZ pod ilk: SRE ilk ona bakar.
	if rows[0].ID != "p-silent" {
		t.Errorf("düşen pod ilk değil: %s", rows[0].ID)
	}
	if rows[1].ID != "p-cpu-high" || rows[2].ID != "p-cpu-high-2" {
		t.Errorf("eşit CPU'da ID tiebreak'i yok: %s, %s", rows[1].ID, rows[2].ID)
	}
	if rows2, total2, up2, tr2 := podInstanceRows(nil, 5); len(rows2) != 0 || total2 != 0 || up2 != 0 || tr2 {
		t.Error("boş girdi güvenli değil")
	}
}

func TestPodHeapRowsFilterOrderAndPostGC(t *testing.T) {
	in := []chstore.CapacitySample{
		{Instance: "svc-a", Subkey: "pod-1", Usage: 50, Limit: 100},
		{Instance: "svc-b", Subkey: "pod-9", Usage: 95, Limit: 100},
		{Instance: "svc-a", Subkey: "pod-2", Usage: 90, Limit: 100, PostGC: 30},
		// Limit 0 → doluluk hesaplanamaz, satır DÜŞER.
		{Instance: "svc-a", Subkey: "pod-3", Usage: 10, Limit: 0},
	}
	t.Run("filo modu sıralar", func(t *testing.T) {
		rows, total, truncated := podHeapRows(in, "", 2)
		if total != 3 {
			t.Errorf("total=%d, beklenen 3 (Limit=0 düşer)", total)
		}
		if !truncated {
			t.Error("truncated false")
		}
		if rows[0].Pod != "pod-9" || rows[1].Pod != "pod-2" {
			t.Errorf("doluluk sırası yanlış: %s, %s", rows[0].Pod, rows[1].Pod)
		}
	})
	t.Run("servis modu süzer ve post-GC taşır", func(t *testing.T) {
		rows, total, _ := podHeapRows(in, "svc-a", 10)
		if total != 2 {
			t.Fatalf("total=%d, beklenen 2 (yalnız svc-a, Limit>0)", total)
		}
		if rows[0].Pod != "pod-2" {
			t.Errorf("en dolu ilk değil: %s", rows[0].Pod)
		}
		if rows[0].PostGCPct != 30 {
			t.Errorf("PostGCPct=%v, beklenen 30 — GERÇEK baskı sinyali (v0.9.426)", rows[0].PostGCPct)
		}
		// PostGC akmıyorsa alan 0 KALIR ve omitempty ile düşer: 0 "GC
		// sonrası boş" demek DEĞİL, ölçüm yokluğu.
		if rows[1].PostGCPct != 0 || rows[1].PostGCBytes != 0 {
			t.Errorf("PostGC akmayan satırda değer üretilmiş: %+v", rows[1])
		}
	})
}

func TestPodHealthPayloadModes(t *testing.T) {
	t.Run("filo modu range_s'in etkisiz olduğunu söyler", func(t *testing.T) {
		body := podHealthPayload(PodHealthData{
			Heap:        []PodHeapRow{{Service: "s", Pod: "p", HeapPct: 91}},
			HeapTotal:   1,
			HeapWindowS: 600,
		}, 86400)
		if body["scope"] != "fleet-wide" {
			t.Errorf("scope=%v", body["scope"])
		}
		if _, has := body["inventory_window_s"]; has {
			t.Error("filo modunda inventory_window_s var — envanter HİÇ okunmuyor, pencere vaadi yalan olur")
		}
		note, _ := body["window_note"].(string)
		if !strings.Contains(note, "range_s has no effect") {
			t.Errorf("filo modu range_s'in etkisizliğini söylemiyor: %q", note)
		}
		if body["heap_window_s"] != 600 {
			t.Errorf("heap_window_s=%v — canlı 10dk ilan edilmeli", body["heap_window_s"])
		}
		if body["restart_note"] != podHealthRestartNote {
			t.Error("restart notu eksik — KSM/Thanos işi, model restart sayısı uydurur")
		}
	})
	t.Run("servis modu up tanımını taşır", func(t *testing.T) {
		body := podHealthPayload(PodHealthData{
			Service:       "checkout",
			Instances:     []PodInstanceRow{{ID: "p1", Up: false}},
			InstanceTotal: 1,
			HeapWindowS:   600,
		}, 1800)
		if body["inventory_window_s"] != 1800 {
			t.Errorf("inventory_window_s=%v", body["inventory_window_s"])
		}
		if def, _ := body["up_definition"].(string); !strings.Contains(def, "LAST 2 MINUTES") {
			t.Errorf("up tanımı yok/eksik: %q — up=false 'çöktü' KANITI değil", def)
		}
		if hn, _ := body["heap_note"].(string); !strings.Contains(hn, "not a JVM") {
			t.Errorf("heap yokluğu notu eksik: %q", hn)
		}
	})
	t.Run("heap okuması düşerse envanter yaşar", func(t *testing.T) {
		body := podHealthPayload(PodHealthData{
			Service:         "checkout",
			Instances:       []PodInstanceRow{{ID: "p1", Up: true}},
			InstanceTotal:   1,
			HeapUnavailable: true,
			HeapWindowS:     600,
		}, 1800)
		if body["heap_unavailable"] != true {
			t.Error("heap_unavailable ilan edilmedi — model boş heap listesini 'heap sağlıklı' okur")
		}
		if body["pod_count"] != 1 {
			t.Errorf("pod_count=%v — envanter tek başına da cevap", body["pod_count"])
		}
	})
	t.Run("pod yoksa sebep taşır", func(t *testing.T) {
		body := podHealthPayload(PodHealthData{Service: "checkout", HeapWindowS: 600}, 1800)
		if _, ok := body["reasons"].([]string); !ok {
			t.Error("boş envanterde reasons yok")
		}
	})
}

// ── problem penceresi: saf şekillendirme ───────────────────────

func TestProblemWindowCounts(t *testing.T) {
	const from int64 = 1_000
	res := func(v int64) *int64 { return &v }
	cases := []struct {
		name                        string
		probs                       []chstore.Problem
		opened, resolved, stillOpen int
	}{
		{
			name:   "pencerede açıldı ve hâlâ açık",
			probs:  []chstore.Problem{{StartedAt: 2_000, Status: "open"}},
			opened: 1, stillOpen: 1,
		},
		{
			name:   "pencerede açıldı ve pencerede kapandı — İKİSİNE de sayılır",
			probs:  []chstore.Problem{{StartedAt: 2_000, Status: "resolved", ResolvedAt: res(3_000)}},
			opened: 1, resolved: 1,
		},
		{
			name:     "pencereden ÖNCE açıldı, pencerede kapandı",
			probs:    []chstore.Problem{{StartedAt: 500, Status: "resolved", ResolvedAt: res(1_500)}},
			resolved: 1,
		},
		{
			name:      "pencereden önce açıldı, hâlâ açık",
			probs:     []chstore.Problem{{StartedAt: 500, Status: "acknowledged"}},
			stillOpen: 1,
		},
		{
			name:  "pencereden önce kapanmış satır hiçbir sayaca girmez",
			probs: []chstore.Problem{{StartedAt: 100, Status: "resolved", ResolvedAt: res(200)}},
		},
		{
			name:   "tam sınır (started_at == from) AÇILDI sayılır",
			probs:  []chstore.Problem{{StartedAt: from, Status: "open"}},
			opened: 1, stillOpen: 1,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			o, r, s := problemWindowCounts(c.probs, from)
			if o != c.opened || r != c.resolved || s != c.stillOpen {
				t.Errorf("(%d,%d,%d), beklenen (%d,%d,%d)", o, r, s, c.opened, c.resolved, c.stillOpen)
			}
		})
	}
}

func TestProblemWindowDataOrderAndFlags(t *testing.T) {
	res := func(v int64) *int64 { return &v }
	probs := []chstore.Problem{
		{ID: "eski", StartedAt: 1_000, Status: "open", Priority: "P2"},
		{ID: "yeni", StartedAt: 9_000, Status: "resolved", ResolvedAt: res(9_500), Priority: "P1"},
		{ID: "orta", StartedAt: 5_000, Status: "open"},
	}
	data := problemWindowData(probs, 2_000, 2)
	if data.Total != 3 || !data.Truncated {
		t.Fatalf("zarf yanlış: %+v", data)
	}
	if data.Rows[0].ID != "yeni" || data.Rows[1].ID != "orta" {
		t.Errorf("en yeni önce değil: %s, %s", data.Rows[0].ID, data.Rows[1].ID)
	}
	// Sayaçlar KESME ÖNCESİ tüm pencereyi anlatır — "kaç açıldı" satır
	// listesi kırpılınca yanlışa dönmemeli.
	if data.Opened != 2 || data.Resolved != 1 || data.StillOpen != 2 {
		t.Errorf("sayaçlar kesmeye bağlı çıkmış: opened=%d resolved=%d stillOpen=%d", data.Opened, data.Resolved, data.StillOpen)
	}
	if !data.Rows[0].ResolvedInWindow || data.Rows[0].ResolvedAtNs != 9_500 {
		t.Errorf("çözülme bilgisi taşınmadı: %+v", data.Rows[0])
	}
	if data.Rows[1].OpenedInWindow != true {
		t.Error("orta satır pencerede açıldı olarak işaretlenmeli")
	}
	if data.Rows[0].Priority != "P1" {
		t.Error("Priority taşınmadı — boş kalırsa tüketici tier UYDURUR (v0.9.554)")
	}
	if data.StoreRowLimit != pwStoreRowLimit {
		t.Errorf("StoreRowLimit=%d", data.StoreRowLimit)
	}
}

func TestProblemWindowPayloadSemanticsAndCounters(t *testing.T) {
	body := problemWindowPayload(ProblemWindowData{
		Rows:  []ProblemWindowRow{{ID: "a", StartedAtNs: 5}},
		Total: 40, Truncated: true, Opened: 12, Resolved: 9, StillOpen: 4,
	}, "checkout", 43200)
	if body["scope"] != "one-service" || body["service"] != "checkout" {
		t.Errorf("servis kapsamı taşınmadı: %v %v", body["scope"], body["service"])
	}
	sem, _ := body["semantics"].(string)
	if !strings.Contains(sem, "opened+resolved do not sum to total") {
		t.Errorf("semantics sayaçların ayrık OLMADIĞINI söylemiyor: %q", sem)
	}
	if body["opened"] != 12 || body["resolved"] != 9 || body["still_open"] != 4 {
		t.Error("sayaçlar gövdeye taşınmadı")
	}
	if note, _ := body["note"].(string); !strings.Contains(note, "counters above cover the WHOLE window") {
		t.Errorf("kesme notu sayaçların tam pencere olduğunu söylemiyor: %q", note)
	}
	empty := problemWindowPayload(ProblemWindowData{}, "", 43200)
	if empty["scope"] != "fleet-wide" {
		t.Errorf("scope=%v", empty["scope"])
	}
	reasons, _ := empty["reasons"].([]string)
	if len(reasons) == 0 {
		t.Fatal("boş pencerede reasons yok — sessiz vardiya NORMAL, model olay uydurmasın")
	}
	// Servis verildiyse "ad yanlış olabilir" sebebi de eklenir.
	svcReasons, _ := problemWindowPayload(ProblemWindowData{}, "checkout", 43200)["reasons"].([]string)
	if len(svcReasons) <= len(reasons) {
		t.Error("servisli boş sonuçta ad-doğrulama sebebi eklenmemiş")
	}
}

// ── kaynak pini (BAĞLANMA) ─────────────────────────────────────

// Handler'lar gövdeyi SAF üreticiden döndürmeli — saf testler zarfı
// doğrular ama handler map'i satır içinde kurup zarfı düşürürse hepsi
// yeşil kalır (bu depoda yanan ders).
func TestGuidedParityHandlersReturnViaPurePayloads(t *testing.T) {
	src := guidedParitySource(t)
	for _, want := range []string{
		"return dbHealthPayload(data, windowS), nil",
		"return messagingHealthPayload(data, windowS), nil",
		"return podHealthPayload(data, windowS), nil",
		"return problemWindowPayload(data, service, windowS), nil",
	} {
		if !strings.Contains(src, want) {
			t.Errorf("handler saf üreticiden dönmüyor — bekleniyor: %s", want)
		}
	}
}

// MUTASYON PİNİ — db okuması HAFİF kalmalı ve env ALMAMALI. Env dolu
// gitseydi GetDatabases MV'yi bırakıp ham spans taramasına düşerdi
// (getDatabasesRaw); IncludeCallers/IncludeReceivers açılsaydı guided
// yolunun maliyeti v0.9.821 öncesine dönerdi.
func TestReadDBHealthStaysOnLightMVPath(t *testing.T) {
	src := guidedParitySource(t)
	i := strings.Index(src, "GetDatabases(ctx, chstore.DatabasesQuery{")
	if i < 0 {
		t.Fatal("GetDatabases çağrısı bulunamadı — okuma başka bir yola mı bağlandı?")
	}
	call := src[i:]
	if end := strings.Index(call, "\n"); end > 0 {
		call = call[:end]
	}
	for _, forbidden := range []string{"Env:", "IncludeCallers", "IncludeReceivers"} {
		if strings.Contains(call, forbidden) {
			t.Errorf("db okuması %s taşıyor: %q — MV'yi diskalifiye eder ya da ekstra tur ödetir", forbidden, call)
		}
	}
	if !strings.Contains(call, "From: from, To: to") {
		t.Errorf("db okuması pencereyi geçmiyor: %q", call)
	}
}

// MUTASYON PİNİ — heap okuması DAİMA canlı RuntimePodWindow penceresinde
// koşar; çağıranın (from, to)'su envanterin işi. Karıştırmak v0.9.1053'ün
// düzelttiği bug sınıfı (40 dk önceki incident'ın heap'i diye ŞU ANKİ
// heap kaydediliyordu) ve 7 günlük heap ortalaması bir baskı sinyali değil.
func TestReadPodHealthKeepsHeapWindowLive(t *testing.T) {
	src := guidedParitySource(t)
	i := strings.Index(src, "JVMHeapPodUsage(ctx")
	if i < 0 {
		t.Fatal("JVMHeapPodUsage çağrısı bulunamadı")
	}
	line := src[i:]
	if end := strings.Index(line, "\n"); end > 0 {
		line = line[:end]
	}
	if !strings.Contains(line, "chstore.RuntimePodWindow") {
		t.Errorf("heap penceresi canlı 10dk değil: %q", line)
	}
	if strings.Contains(line, "from, to") {
		t.Errorf("heap okuması ÇAĞIRANIN penceresini kullanıyor: %q — sustained ortalama semantiği kırılır", line)
	}
	j := strings.Index(src, "ServiceInstances(ctx")
	if j < 0 {
		t.Fatal("ServiceInstances çağrısı bulunamadı")
	}
	instLine := src[j:]
	if end := strings.Index(instLine, "\n"); end > 0 {
		instLine = instLine[:end]
	}
	if !strings.Contains(instLine, "from, to") {
		t.Errorf("envanter okuması pencere geçmiyor: %q", instLine)
	}
}

// MUTASYON PİNİ — pencere olayları OKUMA zenginleştirmesinden GEÇMELİ.
// Atlanırsa Priority boş kalır, omitempty ile JSON'dan düşer ve tüketici
// tier'ı uydurur; guided vardiya bloğu da "[/critical]" gibi yarım bir
// etiket basar (v0.9.553/554 sınıfı).
func TestReadProblemWindowEventsRunsEnrichChain(t *testing.T) {
	src := guidedParitySource(t)
	i := strings.Index(src, "func ReadProblemWindowEvents(")
	if i < 0 {
		t.Fatal("ReadProblemWindowEvents bulunamadı")
	}
	body := src[i:]
	if end := strings.Index(body, "\n}\n"); end > 0 {
		body = body[:end]
	}
	iRead := strings.Index(body, "ListProblemWindowEvents(ctx")
	iEnrich := strings.Index(body, "EnrichProblemsForRead(ctx")
	if iRead < 0 {
		t.Fatal("pencere okuması yok")
	}
	if iEnrich < 0 {
		t.Fatal("EnrichProblemsForRead çağrılmıyor — Priority boş kalır ve tüketici tier UYDURUR (v0.9.554)")
	}
	if iEnrich < iRead {
		t.Error("zenginleştirme okumadan ÖNCE — sıra bozuk")
	}
	if !strings.Contains(body, "mcpDeployLookback") {
		t.Error("deploy penceresi sabitten gelmiyor — api'nin problemDeployLookback'iyle ayrışır (v0.9.553'ün kapattığı sınıf)")
	}
	if !strings.Contains(body, "problemWindowData(probs, from.UnixNano(), limit)") {
		t.Error("sınıflama pencere BAŞLANGICINI almıyor — opened/resolved sayaçları yanlış çıkar")
	}
}

// mcpDeployLookback TEK yazılış olmalı: bu paketteki üç çağrı noktası da
// (list_problems, problems kaynağı, pencere olayları) aynı sabiti
// kullanıyor. Satır içi 30*time.Minute geri gelirse ayrışma da geri gelir.
func TestDeployLookbackHasOneSpelling(t *testing.T) {
	src := guidedParitySource(t) + toolsSource(t)
	if n := strings.Count(src, "30*time.Minute"); n != 0 {
		t.Errorf("%d yerde satır içi 30*time.Minute — mcpDeployLookback kullan", n)
	}
	if n := strings.Count(src, "EnrichProblemsForRead(ctx"); n < 3 {
		t.Errorf("EnrichProblemsForRead çağrısı %d yerde — üç tüketici bekleniyor (list_problems, problems kaynağı, pencere olayları)", n)
	}
}

func guidedParitySource(t *testing.T) string {
	t.Helper()
	return readStrippedSource(t, "guided_parity.go")
}

func toolsSource(t *testing.T) string {
	t.Helper()
	return readStrippedSource(t, "tools.go")
}

// readStrippedSource — YORUMSUZ kaynak. Yorumları ayıklamak zorunlu:
// gerekçe yorumları aranan dizeleri içeriyor ve ayıklamayan bir tarayıcı
// bu depoda KÖR koştu (kapı yorumu "tüketici" sanıyordu).
func readStrippedSource(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(name)
	if err != nil {
		t.Fatalf("%s okunamadı: %v", name, err)
	}
	return stripLineComments(string(b))
}
