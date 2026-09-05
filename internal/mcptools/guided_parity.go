package mcptools

// Guided-parite tool'ları (v0.9.1147, AI Faz 3.4) — D6'nın kapanışı.
//
// Guided sohbet router'ı (api/copilot_guided.go) 16 intent tanıyor ve
// yedisinin MCP karşılığı YOKTU (tasarım §1.4). Bu dosya dördünü
// tool'a indiriyor — hepsi "in-app zengin, MCP fakir" listesinin
// SORULARI, arg'ları değil (Faz 3.2/3.3 sınıfının devamı):
//
//   - get_db_health              ("hangi db yavaş?"        — db_summary_5m)
//   - get_messaging_health       ("kafka lag nasıl?"       — messaging_summary_5m)
//   - get_pod_health             ("hangi pod'un heap'i dolu?" — OTel runtime)
//   - list_problem_window_events ("dün gece neler oldu?"   — problems FINAL)
//
// ORTAK VERİ KATMANI (D6'nın asıl işi). Bu dosya yalnız tool eklemiyor;
// aynı okumanın İKİ kopyası kalmasın diye veri katmanını buraya
// taşıyor. Sözleşme üç parça:
//
//	ReadX(ctx, d, …) (XData, error)   ← okuma + YAPISAL şekillendirme
//	xPayload(XData, …) map[string]any ← tool tarafı: JSON gövde
//	api: renderXEvidenceTR(XData, …)  ← guided tarafı: Türkçe kanıt metni
//
// Yani guided ve MCP artık AYNI okumadan ve AYNI satır sırasından
// besleniyor; ayrışabilecek tek şey biçim. Okuma api'de KOPYA
// bırakmadı — copilot_deps.go / copilot_guided.go / copilot_shift.go
// artık store'u bu yol dışında çağırmıyor (mutasyon testleriyle pinli:
// api/guided_shared_layer_test.go).
//
// PENCERE DÜRÜSTLÜĞÜ — dördünün pencere semantiği farklı, şema BAŞTAN
// söylüyor (kabul-edilip-yok-sayılan arg YASAK, Faz 3.2 doktrini):
//
//   - get_db_health: db_summary_5m 5 DAKİKALIK kovalar; okuma `from`u
//     kova başına AŞAĞI yuvarlıyor ve üst sınırı `< to` (v0.9.823). Yani
//     efektif pencere BAŞTA ~5dk geniş olabilir; range_s tabanı 300.
//   - get_messaging_health: aynı kova + aynı aşağı yuvarlama, ama üst
//     sınır `<= to`, yani pencere SONDA da bir kova (≤5dk) taşabilir.
//     Bu bir sapma değil ölçülmüş gerçek: v0.9.823 db tarafını `< to`ya
//     çekti, messaging okuması `<=` kaldı. Uydurmuyoruz, İLAN EDİYORUZ
//     (düzeltmek chstore + sayfa sayıları demek, /clickhouse-schema kapısı).
//   - get_pod_health: pencere BÖLÜNMÜŞ. Pod envanteri çağıranın
//     penceresini kullanır (ve `up` bayrağı pencerenin SON 2 dakikasına
//     bakar); heap doygunluğu ise HER ZAMAN canlı 10 dakikadır
//     (chstore.RuntimePodWindow) çünkü o okuma "sustained" ortalama
//     olarak tasarlandı — 7 günlük heap ortalaması bir baskı sinyali
//     değildir. Servis verilmezse envanter HİÇ okunmaz, yani range_s o
//     modda tamamen ilgisizdir. Üçü de şemada yazılı.
//   - list_problem_window_events: pencere çağıranınki, ama okuma
//     started_at ∈ [from,to) VEYA resolved_at ∈ [from,to) satırlarını
//     getirir — yani pencereden ÖNCE açılıp pencerede kapanan bir problem
//     de listede olur (vardiya sorusunun doğru cevabı budur). Varsayılan
//     12h = guided'ın vardiya varsayılanı.
//
// ENV ARG KARARI (v0.8.398 sözleşmesi) — tool BAŞINA, dördünde de YOK
// ama dört FARKLI sebeple:
//
//   - get_db_health: DatabasesQuery.Env VAR ama MV'yi DİSKALİFİYE eder
//     (getDatabasesRaw'a düşer, ham spans taraması). Arg'ı kabul etmek
//     "ucuz sandığın çağrı sessizce milyon-satır taraması" demekti;
//     guided de aynı sebeple "" geçiyor ve kanıt metninde söylüyor.
//     Kırılım kimliği (system, instance, db_name) env taşımadığı için
//     süzülen sonucu etiketleyecek alan da yok.
//   - get_messaging_health: GetMessaging env PARAMETRESİ ALMIYOR ve
//     MessagingInstance'ta env kolonu yok — süzülecek boyut mevcut değil.
//   - get_pod_health: ServiceInstances(service, from, to) ve
//     JVMHeapPodUsage(from, to) okumalarının env conjunct'ı yok; satırlar
//     pod/host kimliğiyle gruplanıyor.
//   - list_problem_window_events: list_problems'in env'i ProblemFilter
//     üzerinden SERVİS-kapsamlı çözülüyor (env_members); pencere okuması
//     ise filtreyi hiç almayan ODAKLI bir sorgu (problem.go:1521). Arg
//     eklemek ya ikinci bir okuma şekli ya da sessiz no-op olurdu.
//     ASİMETRİ BİLİNÇLİ ve tool açıklamasında yazılı.
//
// MinRole "" (viewer tabanı) — dördü de salt-okunur ve REST eşleri
// kapısız: GET /api/databases, GET /api/messaging,
// GET /api/services/{name}/instances, GET /api/annotations (pencere
// olaylarının canlı tüketicisi) + /shift sayfası.
//
// BİLİNÇLİ SAPMALAR:
//
//  1. get_db_health çağıran listesi TAŞIMIYOR. dbTopCallersSQL ayrı bir
//     MV turu (satır başına 5 çağıran) ve GetDatabases onu yalnız
//     IncludeCallers ile koşuyor; açmak guided yolunun maliyetini de
//     artırırdı (v0.9.821 tam bunu kaldırdı). Model "bu db'yi kim
//     çağırıyor" için get_topology'nin database kenarlarına gider.
//  2. get_messaging_health çağıran listesi TAŞIYOR — çünkü mevcut okuma
//     (GetMessaging) onu ZATEN ödüyor ve guided satırları kullanmıyordu.
//     Yeni maliyet yok; masadaki veri modele açıldı.
//  3. Satırlar chstore tiplerinin AYNISI değil: o şekiller UI için 20+
//     alan (Prior* kıyas alanları, camelCase) taşıyor ve bu okumada
//     doldurulmayan alanlar modelde "ölçüm 0" diye okunur. Alt küme +
//     snake_case + mcpFloat.

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

// depBucketS — db_summary_5m ve messaging_summary_5m'in kova boyutu.
// range_s'in TABANI: daha küçük bir pencere okuma tarafında yine bir tam
// kovaya yuvarlanır, dolayısıyla "son 60 saniyeye baktım" demek yalan
// olurdu (analysis.go'daki topoBucketS ile aynı gerekçe).
const depBucketS = 300

// mcpDeployLookback — problem OKUMA zenginleştirmesinin deploy penceresi.
// api tarafındaki problemDeployLookback (api/problem_enrich.go) ile AYNI
// olmak zorunda: farklı olsaydı iki yüzey yine ayrışırdı, sadece daha
// ince bir şekilde (v0.9.553'ün kapattığı sınıf). Bu paketteki İKİ eski
// çağrı noktası (list_problems, get_problem_root_cause) 30*time.Minute'ı
// satır içinde yazıyordu; üçüncü yazılış yerine tek sabit.
const mcpDeployLookback = 30 * time.Minute

// ─── get_db_health ─────────────────────────────────────────────

const (
	dbHealthDefaultRangeS = 3600
	dbHealthMaxRangeS     = 7 * 86400
	dbHealthDefaultRows   = 20
	dbHealthMaxRows       = 50
	// dbHealthStoreRowLimit — GetDatabases'in SQL LIMIT'i
	// (chstore.dbOverviewRowLimit). Zarf RowLimit'i taşıyorsa o kazanır;
	// bu yalnız zarf boş geldiğinde kullanılan yedek.
	dbHealthStoreRowLimit = 5000
)

// dbHealthWindowS — range_s → efektif saniye (varsayılan 1h, taban 300 =
// kova, tavan 7 gün). Saf, tablo testli.
func dbHealthWindowS(rangeS int) int {
	if rangeS <= 0 {
		return dbHealthDefaultRangeS
	}
	if rangeS < depBucketS {
		return depBucketS
	}
	if rangeS > dbHealthMaxRangeS {
		return dbHealthMaxRangeS
	}
	return rangeS
}

// DBHealthRow — bir (system, instance, db_name) satırı. Ondalıklar HAM:
// yuvarlama yalnız JSON tarafında (dbHealthPayload → sanitizeDBHealthRows).
// Guided metni %.1f/%.2f basıyor ve ön-yuvarlanmış bir değeri yeniden
// yuvarlamak kenar durumlarda BAŞKA bir dize üretir — ortak katman ham
// ölçümü taşır, biçim tüketicinin işi.
type DBHealthRow struct {
	System   string `json:"system"`
	Instance string `json:"instance"`
	DBName   string `json:"db_name,omitempty"`
	Calls    uint64 `json:"calls"`
	Errors   uint64 `json:"errors"`
	// ErrorRatePct 0..100.
	ErrorRatePct float64 `json:"error_rate_pct"`
	AvgMs        float64 `json:"avg_ms"`
	P50Ms        float64 `json:"p50_ms"`
	P95Ms        float64 `json:"p95_ms"`
	P99Ms        float64 `json:"p99_ms"`
}

// DBHealthData — ortak katmanın çıktısı. Rows SIRALI (calls DESC, tam
// tiebreak) ve Limit'e kesik; Total kesme ÖNCESİ sayı.
type DBHealthData struct {
	Rows      []DBHealthRow
	Total     int
	Truncated bool
	// StoreCapped — okuma STORE tavanına dayandı (liste eksik olabilir),
	// StoreRowLimit tavanın kendisi. Guided bugüne dek bunu hiç
	// söylemiyordu; artık ikisi de aynı bayrağı görüyor.
	StoreCapped   bool
	StoreRowLimit int
}

// dbHealthData — zarf → yapısal veri. SAF, tablo testli. Sıralama tam
// tiebreak'li (v0.8.324 sözleşmesi: eşit calls'ta sıra oturumlar arası
// oynamasın — CH'nin döndürdüğü ham sıra garantili değil).
func dbHealthData(ov *chstore.DatabasesOverview, limit int) DBHealthData {
	out := DBHealthData{StoreRowLimit: dbHealthStoreRowLimit}
	if ov == nil {
		return out
	}
	rows := make([]DBHealthRow, 0, len(ov.Rows))
	for _, r := range ov.Rows {
		rows = append(rows, DBHealthRow{
			System:       r.System,
			Instance:     r.Instance,
			DBName:       r.DBName,
			Calls:        r.SpanCount,
			Errors:       r.ErrorCount,
			ErrorRatePct: r.ErrorRate,
			AvgMs:        r.AvgMs,
			P50Ms:        r.P50Ms,
			P95Ms:        r.P95Ms,
			P99Ms:        r.P99Ms,
		})
	}
	sort.SliceStable(rows, func(i, j int) bool {
		if rows[i].Calls != rows[j].Calls {
			return rows[i].Calls > rows[j].Calls
		}
		if rows[i].System != rows[j].System {
			return rows[i].System < rows[j].System
		}
		if rows[i].Instance != rows[j].Instance {
			return rows[i].Instance < rows[j].Instance
		}
		return rows[i].DBName < rows[j].DBName
	})
	out.Total = len(rows)
	out.StoreCapped = ov.RowsCapped
	if ov.RowLimit > 0 {
		out.StoreRowLimit = ov.RowLimit
	}
	if limit > 0 && len(rows) > limit {
		rows = rows[:limit]
		out.Truncated = true
	}
	out.Rows = rows
	return out
}

// DBHealthSlowestByP95 — verilen satırlardan p95'e göre en yavaş n. SAF.
// KAPSAM: girdi ne ise o — çağıran zaten kesilmiş bir liste verdiyse
// "en yavaş" o listenin içindedir. Guided bunu böyle kullanıyordu ve
// davranış korunuyor; tool gövdesi de aynı alt kümeden türetiliyor, yani
// iki yüzey aynı üç adı söylüyor.
func DBHealthSlowestByP95(rows []DBHealthRow, n int) []DBHealthRow {
	src := append([]DBHealthRow(nil), rows...)
	sort.SliceStable(src, func(i, j int) bool { return src[i].P95Ms > src[j].P95Ms })
	if n > 0 && len(src) > n {
		src = src[:n]
	}
	return src
}

// sanitizeDBHealthRows — JSON'a giden kopya: ondalıklar mcpFloat'lı.
// SAF, girdi mutasyona uğramaz.
func sanitizeDBHealthRows(rows []DBHealthRow) []DBHealthRow {
	out := make([]DBHealthRow, len(rows))
	for i, r := range rows {
		r.ErrorRatePct = mcpFloat(r.ErrorRatePct)
		r.AvgMs = mcpFloat(r.AvgMs)
		r.P50Ms = mcpFloat(r.P50Ms)
		r.P95Ms = mcpFloat(r.P95Ms)
		r.P99Ms = mcpFloat(r.P99Ms)
		out[i] = r
	}
	return out
}

// ReadDBHealth — ORTAK OKUMA. GetDatabases'in HAFİF çağrısı: çağıran turu
// ve receiver keşfi KAPALI (IncludeCallers/IncludeReceivers false), yani
// maliyet GetDatabasesRollup'ın birebir aynısı — üstüne yalnız zarfın
// RowsCapped bayrağı geliyor (guided o bayrağı görmüyordu).
func ReadDBHealth(ctx context.Context, d Deps, from, to time.Time, limit int) (DBHealthData, error) {
	ov, err := d.Store.GetDatabases(ctx, chstore.DatabasesQuery{From: from, To: to})
	if err != nil {
		return DBHealthData{}, err
	}
	return dbHealthData(ov, limit), nil
}

// dbHealthEmptyReasons — hiç DB satırı yok. Boş sonuç hata DEĞİL.
func dbHealthEmptyReasons() []string {
	return []string{
		"no span in the window carried db.system — the database calls are not instrumented (no JDBC/driver instrumentation, or the client library is unsupported)",
		"the estate genuinely made no database call in this window (quiet night, batch-only workload)",
		"the window is narrower than one 5-minute aggregate bucket, or predates the aggregate's retention",
	}
}

// dbHealthPayload — gövde üreticisi (saf, tablo testli).
func dbHealthPayload(data DBHealthData, windowS int) map[string]any {
	body := map[string]any{
		"window_s":        windowS,
		"window_bucket_s": depBucketS,
		"scope":           "fleet-wide",
		"databases":       sanitizeDBHealthRows(data.Rows),
		"count":           len(data.Rows),
		"total":           data.Total,
		"truncated":       data.Truncated,
		"slowest_by_p95":  sanitizeDBHealthRows(DBHealthSlowestByP95(data.Rows, 3)),
	}
	if len(data.Rows) == 0 {
		body["reasons"] = dbHealthEmptyReasons()
		body["note"] = "Say plainly that no database call was observed in this window; do NOT invent database names or latencies."
	}
	if data.Truncated {
		body["note"] = fmt.Sprintf("Only the %d busiest of %d rows are returned (ordered by calls), so slowest_by_p95 is the slowest AMONG THOSE — a quiet-but-slow database can sit outside the list. Raise limit to widen it.", len(data.Rows), data.Total)
	}
	if data.StoreCapped {
		body["store_capped"] = true
		body["store_row_limit"] = data.StoreRowLimit
		body["note"] = fmt.Sprintf("The store-side read hit its %d-row ceiling, so `total` is a LOWER bound on how many database instances exist.", data.StoreRowLimit)
	}
	body["callers"] = "not included — reading per-database caller lists is a second aggregate pass; use get_topology and read the `database` target edges for who calls what."
	body["next"] = "Bir DB yavaşsa onu çağıran servisin RED'i için get_service_health; yavaş sorgunun kendisi için /databases sayfası (tool yok); eş zamanlı değişim için get_correlated_changes."
	return body
}

type getDBHealthArgs struct {
	RangeS int `json:"range_s,omitempty"`
	Limit  int `json:"limit,omitempty"`
}

func getDBHealthTool(d Deps) mcp.Tool {
	return mcp.Tool{
		Name:             "get_db_health",
		ShortDescription: "Hangi VERİTABANI yavaş/hatalı — filo geneli (db.system, instance, database) RED kırılımı. Servis filtresi YOK; gecikme İSTEMCİ tarafında ölçülür (havuz beklemesi dahil).",
		Description: "Answer 'which DATABASE is slow / erroring' — the fleet-wide database breakdown from the 5-minute pre-aggregate: one row per " +
			"(db.system, instance, database) with calls, errors, error rate, avg and p50/p95/p99 latency, busiest first, plus a slowest_by_p95 shortcut. " +
			"This is the client-side view: latency is measured on the CALLING service's span, so it includes network and connection-pool wait, not just " +
			"server execution time. " +
			"SCOPE IS ALWAYS FLEET-WIDE — there is no per-service filter here; a database is shared infrastructure and the question 'is the DB slow' is not " +
			"per-caller. To attribute a slow database to one caller, pair it with get_service_health / get_topology. " +
			"COST: one pre-aggregate read, cheap. BOUNDS: the window snaps DOWN to the 5-minute bucket boundary, so counts can include up to 5 minutes " +
			"before the requested start. Rows beyond `limit` are dropped and truncated=true says so. " +
			"An empty list is NOT an error (no db.system spans = no DB instrumentation) and you get reasons. Never invent a database name.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"range_s": map[string]any{
					"type":    "integer",
					"minimum": 0,
					"maximum": dbHealthMaxRangeS,
					"description": "Lookback seconds. Default 3600 (1h, the /databases page default), max 604800 (7d). Minimum effective value 300 " +
						"(the aggregate's 5-minute bucket) — a narrower ask still reads one full bucket.",
				},
				"limit": map[string]any{
					"type":        "integer",
					"minimum":     1,
					"maximum":     dbHealthMaxRows,
					"description": "Max database rows, busiest first. Default 20, max 50.",
				},
			},
		},
		// Salt-okunur; REST eşi GET /api/databases viewer'a açık.
		// Env arg'ı YOK: env filtresi MV'yi diskalifiye eder (ham spans
		// taraması) — dosya başlığındaki karar.
		MinRole: "",
		Handler: func(ctx context.Context, raw json.RawMessage) (any, error) {
			var a getDBHealthArgs
			if len(raw) > 0 {
				if err := json.Unmarshal(raw, &a); err != nil {
					return nil, fmt.Errorf("decode args: %w", err)
				}
			}
			windowS := dbHealthWindowS(a.RangeS)
			from, to := rangeWindow(ctx, windowS)
			data, err := ReadDBHealth(ctx, d, from, to, clampLimit(a.Limit, dbHealthDefaultRows, dbHealthMaxRows))
			if err != nil {
				return nil, err
			}
			return dbHealthPayload(data, windowS), nil
		},
	}
}

// ─── get_messaging_health ──────────────────────────────────────

const (
	msgHealthDefaultRangeS = 3600
	msgHealthMaxRangeS     = 7 * 86400
	msgHealthDefaultRows   = 20
	msgHealthMaxRows       = 50
	// msgHealthStoreRowLimit — chstore.msgOverviewRowLimit yedeği (zarf
	// RowLimit'i taşıyorsa o kazanır). 200 destination gerçekten
	// ULAŞILABİLİR bir tavan, o yüzden bayrak metne de giriyor.
	msgHealthStoreRowLimit = 200
)

// msgHealthWindowS — range_s → efektif saniye. Saf, tablo testli.
func msgHealthWindowS(rangeS int) int {
	if rangeS <= 0 {
		return msgHealthDefaultRangeS
	}
	if rangeS < depBucketS {
		return depBucketS
	}
	if rangeS > msgHealthMaxRangeS {
		return msgHealthMaxRangeS
	}
	return rangeS
}

// MessagingHealthRow — bir (system, cluster, destination) satırı.
// Ondalıklar HAM (bkz. DBHealthRow gerekçesi).
type MessagingHealthRow struct {
	System      string `json:"system"`
	Cluster     string `json:"cluster"`
	Destination string `json:"destination"`
	Calls       uint64 `json:"calls"`
	Errors      uint64 `json:"errors"`
	// ErrorRatePct 0..100.
	ErrorRatePct float64 `json:"error_rate_pct"`
	AvgMs        float64 `json:"avg_ms"`
	P50Ms        float64 `json:"p50_ms"`
	P95Ms        float64 `json:"p95_ms"`
	P99Ms        float64 `json:"p99_ms"`
	// Produce*/Consume* — kind kırılımı (messaging_caller_summary_5m).
	// GECİKME AYRIŞMASI: publish (broker'a yazma) ile process (mesajı
	// işleme) farklı işler; karışık p95 yavaş tüketiciyi hızlı üreticinin
	// içinde saklar (v0.9.816). 0 ms bir ölçüm DEĞİL, ölçüm yokluğudur.
	ProduceCalls  uint64  `json:"produce_calls"`
	ConsumeCalls  uint64  `json:"consume_calls"`
	ProduceErrors uint64  `json:"produce_errors"`
	ConsumeErrors uint64  `json:"consume_errors"`
	ProduceP95Ms  float64 `json:"produce_p95_ms,omitempty"`
	ConsumeP95Ms  float64 `json:"consume_p95_ms,omitempty"`
	// Callers — bu destination'a dokunan en yoğun servisler (grup başına
	// 5, LIMIT n BY ile — v0.9.813). Mevcut okuma bunu zaten ödüyor.
	Callers []string `json:"callers,omitempty"`
}

// MessagingHealthData — DBHealthData ikizi.
type MessagingHealthData struct {
	Rows          []MessagingHealthRow
	Total         int
	Truncated     bool
	StoreCapped   bool
	StoreRowLimit int
}

// messagingHealthData — zarf → yapısal veri. SAF, tablo testli.
func messagingHealthData(ov *chstore.MessagingOverview, limit int) MessagingHealthData {
	out := MessagingHealthData{StoreRowLimit: msgHealthStoreRowLimit}
	if ov == nil {
		return out
	}
	rows := make([]MessagingHealthRow, 0, len(ov.Rows))
	for _, r := range ov.Rows {
		rows = append(rows, MessagingHealthRow{
			System:        r.System,
			Cluster:       r.Cluster,
			Destination:   r.Destination,
			Calls:         r.SpanCount,
			Errors:        r.ErrorCount,
			ErrorRatePct:  r.ErrorRate,
			AvgMs:         r.AvgMs,
			P50Ms:         r.P50Ms,
			P95Ms:         r.P95Ms,
			P99Ms:         r.P99Ms,
			ProduceCalls:  r.ProduceCount,
			ConsumeCalls:  r.ConsumeCount,
			ProduceErrors: r.ProduceErrors,
			ConsumeErrors: r.ConsumeErrors,
			ProduceP95Ms:  r.ProduceP95Ms,
			ConsumeP95Ms:  r.ConsumeP95Ms,
			Callers:       append([]string(nil), r.Callers...),
		})
	}
	sort.SliceStable(rows, func(i, j int) bool {
		if rows[i].Calls != rows[j].Calls {
			return rows[i].Calls > rows[j].Calls
		}
		if rows[i].System != rows[j].System {
			return rows[i].System < rows[j].System
		}
		if rows[i].Cluster != rows[j].Cluster {
			return rows[i].Cluster < rows[j].Cluster
		}
		return rows[i].Destination < rows[j].Destination
	})
	out.Total = len(rows)
	out.StoreCapped = ov.RowsCapped
	if ov.RowLimit > 0 {
		out.StoreRowLimit = ov.RowLimit
	}
	if limit > 0 && len(rows) > limit {
		rows = rows[:limit]
		out.Truncated = true
	}
	out.Rows = rows
	return out
}

// sanitizeMessagingHealthRows — JSON kopyası, ondalıklar mcpFloat'lı. SAF.
func sanitizeMessagingHealthRows(rows []MessagingHealthRow) []MessagingHealthRow {
	out := make([]MessagingHealthRow, len(rows))
	for i, r := range rows {
		r.ErrorRatePct = mcpFloat(r.ErrorRatePct)
		r.AvgMs = mcpFloat(r.AvgMs)
		r.P50Ms = mcpFloat(r.P50Ms)
		r.P95Ms = mcpFloat(r.P95Ms)
		r.P99Ms = mcpFloat(r.P99Ms)
		r.ProduceP95Ms = mcpFloat(r.ProduceP95Ms)
		r.ConsumeP95Ms = mcpFloat(r.ConsumeP95Ms)
		out[i] = r
	}
	return out
}

// ReadMessagingHealth — ORTAK OKUMA (GetMessaging; çağıran turu dahil,
// çünkü bu okuma onu zaten koşuyor — bkz. dosya başlığı sapma #2).
func ReadMessagingHealth(ctx context.Context, d Deps, from, to time.Time, limit int) (MessagingHealthData, error) {
	ov, err := d.Store.GetMessaging(ctx, from, to)
	if err != nil {
		return MessagingHealthData{}, err
	}
	return messagingHealthData(ov, limit), nil
}

// messagingLagNote — TEK kaynak: hem tool gövdesi hem guided kanıt metni
// bu cümleyi kullanıyor. Consumer lag DOĞRUDAN ölçülmüyor (broker
// metrikleri ingest edilmiyor, memory: feedback-no-db-engine-metrics) ve
// model bunu bilmezse "lag şu kadar" diye UYDURUR.
const messagingLagNote = "Consumer lag is NOT measured — broker metrics are not ingested. A high avg/p95 on the consumer side is PROCESSING time, not queue depth; never report a lag figure."

// messagingEmptyReasons — hiç messaging satırı yok.
func messagingEmptyReasons() []string {
	return []string{
		"no span in the window carried messaging.system — producers/consumers are not instrumented for messaging semconv",
		"the estate genuinely published/consumed nothing in this window",
		"the window is narrower than one 5-minute aggregate bucket, or predates the aggregate's retention",
	}
}

// messagingHealthPayload — gövde üreticisi (saf, tablo testli).
func messagingHealthPayload(data MessagingHealthData, windowS int) map[string]any {
	body := map[string]any{
		"window_s":        windowS,
		"window_bucket_s": depBucketS,
		"scope":           "fleet-wide",
		"destinations":    sanitizeMessagingHealthRows(data.Rows),
		"count":           len(data.Rows),
		"total":           data.Total,
		"truncated":       data.Truncated,
		"lag_note":        messagingLagNote,
	}
	if len(data.Rows) == 0 {
		body["reasons"] = messagingEmptyReasons()
		body["note"] = "Say plainly that no messaging span was observed in this window; do NOT invent topics, queues or lag numbers."
	}
	if data.Truncated {
		body["note"] = fmt.Sprintf("Only the %d busiest of %d destinations are returned (ordered by calls). Raise limit to widen it.", len(data.Rows), data.Total)
	}
	if data.StoreCapped {
		body["store_capped"] = true
		body["store_row_limit"] = data.StoreRowLimit
		body["note"] = fmt.Sprintf("The store-side read hit its %d-row ceiling, so `total` is a LOWER bound on how many destinations exist.", data.StoreRowLimit)
	}
	body["next"] = "Tüketici yavaşsa o servisin RED'i için get_service_health; üretici/tüketici zincirini iz üzerinde takip için get_linked_traces; hata patlamasında get_log_histogram."
	return body
}

type getMessagingHealthArgs struct {
	RangeS int `json:"range_s,omitempty"`
	Limit  int `json:"limit,omitempty"`
}

func getMessagingHealthTool(d Deps) mcp.Tool {
	return mcp.Tool{
		Name:             "get_messaging_health",
		ShortDescription: "Kuyruk/topic tarafı — (sistem, cluster, destination) RED + producer/consumer ayrımı. Filo geneli. CONSUMER LAG ÖLÇÜLMÜYOR; asla lag değeri verme.",
		Description: "Answer 'how is the QUEUE/TOPIC side doing' — the fleet-wide messaging breakdown from the 5-minute pre-aggregate: one row per " +
			"(messaging.system, cluster, destination) with calls, errors, error rate, avg/p50/p95/p99, plus the producer/consumer split " +
			"(produce_calls / consume_calls and their separate p95s) and the busiest calling services. " +
			"READ THE SPLIT: produce_p95_ms is publish time (writing to the broker, usually fast); consume_p95_ms is PROCESSING time of the consumer. " +
			"CONSUMER LAG IS NOT AVAILABLE — broker metrics are not ingested, so there is no queue-depth or lag figure anywhere in this response. " +
			"Never state a lag value; if asked, say lag is not measured and reason from consume_p95_ms and error rate instead. " +
			"SCOPE IS ALWAYS FLEET-WIDE (a topic is shared infrastructure). COST: one pre-aggregate read, cheap. " +
			"BOUNDS: the window snaps DOWN to the 5-minute bucket at the start AND can include up to one bucket past the end, so treat counts as " +
			"±5 minutes at the edges. An empty list is NOT an error and you get reasons. Never invent a topic name.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"range_s": map[string]any{
					"type":    "integer",
					"minimum": 0,
					"maximum": msgHealthMaxRangeS,
					"description": "Lookback seconds. Default 3600 (1h, the /messaging page default), max 604800 (7d). Minimum effective value 300 " +
						"(the aggregate's 5-minute bucket).",
				},
				"limit": map[string]any{
					"type":        "integer",
					"minimum":     1,
					"maximum":     msgHealthMaxRows,
					"description": "Max destination rows, busiest first. Default 20, max 50. The store itself caps at 200 destinations and store_capped says so.",
				},
			},
		},
		// Salt-okunur; REST eşi GET /api/messaging viewer'a açık.
		// Env arg'ı YOK: okuma env parametresi almıyor ve satırda env
		// kolonu yok — süzülecek boyut mevcut değil.
		MinRole: "",
		Handler: func(ctx context.Context, raw json.RawMessage) (any, error) {
			var a getMessagingHealthArgs
			if len(raw) > 0 {
				if err := json.Unmarshal(raw, &a); err != nil {
					return nil, fmt.Errorf("decode args: %w", err)
				}
			}
			windowS := msgHealthWindowS(a.RangeS)
			from, to := rangeWindow(ctx, windowS)
			data, err := ReadMessagingHealth(ctx, d, from, to, clampLimit(a.Limit, msgHealthDefaultRows, msgHealthMaxRows))
			if err != nil {
				return nil, err
			}
			return messagingHealthPayload(data, windowS), nil
		},
	}
}

// ─── get_pod_health ────────────────────────────────────────────

const (
	podHealthDefaultRangeS = 1800
	// podHealthMaxRangeS — envanter okuması metric_points üzerinde HAM bir
	// tarama (pod envanterinin MV'si yok, LIMIT 200 + 10s duvar saati).
	// 24 saat tavanı bilinçli: daha geniş pencere yalnız "o gün var olan"
	// pod'ları biriktirir ve `up` bayrağı zaten pencerenin son 2
	// dakikasına bakar, yani geniş pencere bilgi KATMIYOR.
	podHealthMaxRangeS           = 24 * 3600
	podHealthDefaultInstanceRows = 12
	podHealthMaxInstanceRows     = 50
	podHealthDefaultHeapRows     = 10
	podHealthMaxHeapRows         = 50
)

// podHealthWindowS — range_s → efektif saniye (varsayılan 30dk, tavan
// 24h). Taban YOK: envanter okuması ham metric_points, kova hizalaması
// olmadığı için 60 saniyelik pencere de dürüst bir cevaptır (yalnız
// tazelik penceresi 2dk olduğundan çok dar pencerede `up` hepsi false
// olabilir — açıklama söylüyor). Saf, tablo testli.
func podHealthWindowS(rangeS int) int {
	if rangeS <= 0 {
		return podHealthDefaultRangeS
	}
	if rangeS > podHealthMaxRangeS {
		return podHealthMaxRangeS
	}
	return rangeS
}

// PodInstanceRow — bir pod/instance (OTel host.name kimliği).
type PodInstanceRow struct {
	ID     string  `json:"id"`
	Zone   string  `json:"zone,omitempty"`
	CPUPct float64 `json:"cpu_pct"`
	// MemBytes/MemPct — MemPct yalnız runtime bir bellek LİMİTİ
	// bildirdiğinde dolu (JVM bildirir, Go bildirmez) — 0 = limit
	// bilinmiyor, "bellek boş" DEĞİL.
	MemBytes float64 `json:"mem_bytes"`
	MemPct   float64 `json:"mem_pct,omitempty"`
	// Up — pencerenin SON 2 dakikasında örnek görüldü mü. false =
	// "sessiz": düşmüş, drene olmuş ya da metrik yayınlamayı kesmiş.
	Up         bool  `json:"up"`
	LastSeenNs int64 `json:"last_seen_unix_ns"`
}

// PodHeapRow — bir pod'un JVM heap doygunluğu (10 dk ortalaması).
type PodHeapRow struct {
	Service string `json:"service"`
	Pod     string `json:"pod"`
	// HeapPct = used/limit. PostGCPct = used_after_last_gc/limit ve
	// GERÇEK baskı sinyali odur (testere-dişi heap'te used/max sağlıklı
	// pod'da bile %85+ görünür — v0.9.426 operatör raporu). PostGCPct 0 =
	// metrik AKMIYOR, "GC sonrası boş" DEĞİL.
	HeapPct     float64 `json:"heap_pct"`
	PostGCPct   float64 `json:"post_gc_pct,omitempty"`
	UsedBytes   float64 `json:"used_bytes"`
	LimitBytes  float64 `json:"limit_bytes"`
	PostGCBytes float64 `json:"post_gc_bytes,omitempty"`
}

// PodHealthData — iki mod tek şekil. Service=="" → filo modu: envanter
// HİÇ okunmaz (Instances nil), Heap filo geneli sıralamadır.
type PodHealthData struct {
	Service            string
	Instances          []PodInstanceRow
	InstanceTotal      int
	InstancesTruncated bool
	UpCount            int
	Heap               []PodHeapRow
	HeapTotal          int
	HeapTruncated      bool
	// HeapWindowS — heap okumasının penceresi. DAİMA canlı
	// chstore.RuntimePodWindow; çağıranın range_s'i buraya karışmaz.
	HeapWindowS int
	// HeapUnavailable — heap okuması başarısız oldu ama servis modunda bu
	// ÖLÜMCÜL DEĞİL: envanter tek başına da cevap. Filo modunda heap tek
	// veri olduğu için hata çağırana döner.
	HeapUnavailable bool
}

// podInstanceRows — satır şekli + SIRA: önce DÜŞENLER (SRE ilk onlara
// bakar), sonra CPU DESC, sonra ID (tam tiebreak — CH sırası garantili
// değil). Kesme ikinci dönüşte. SAF, tablo testli.
func podInstanceRows(in []chstore.ServiceInstance, limit int) (rows []PodInstanceRow, total, up int, truncated bool) {
	out := make([]PodInstanceRow, 0, len(in))
	for _, i := range in {
		if i.Up {
			up++
		}
		out = append(out, PodInstanceRow{
			ID:         i.ID,
			Zone:       i.Zone,
			CPUPct:     i.CPUPct,
			MemBytes:   i.MemBytes,
			MemPct:     i.MemPct,
			Up:         i.Up,
			LastSeenNs: i.LastSeen,
		})
	}
	sort.SliceStable(out, func(a, b int) bool {
		if out[a].Up != out[b].Up {
			return !out[a].Up
		}
		if out[a].CPUPct != out[b].CPUPct {
			return out[a].CPUPct > out[b].CPUPct
		}
		return out[a].ID < out[b].ID
	})
	total = len(out)
	if limit > 0 && len(out) > limit {
		out = out[:limit]
		truncated = true
	}
	return out, total, up, truncated
}

// podHeapRows — CapacitySample'ları heap satırına çevirir; service != ""
// ise YALNIZ o servis. SIRA heap doluluk DESC + tam tiebreak: kaynak
// sorgunun ORDER BY'ı YOK (runtime_pods.go), yani ham sıra CH'nin
// keyfidir — sıralamamak "en dolu pod" sorusunu kumar yapardı.
// SAF, tablo testli.
func podHeapRows(in []chstore.CapacitySample, service string, limit int) (rows []PodHeapRow, total int, truncated bool) {
	out := make([]PodHeapRow, 0, len(in))
	for _, h := range in {
		if h.Limit <= 0 {
			continue
		}
		if service != "" && h.Instance != service {
			continue
		}
		row := PodHeapRow{
			Service:    h.Instance,
			Pod:        h.Subkey,
			HeapPct:    h.Usage / h.Limit * 100,
			UsedBytes:  h.Usage,
			LimitBytes: h.Limit,
		}
		if h.PostGC > 0 {
			row.PostGCBytes = h.PostGC
			row.PostGCPct = h.PostGC / h.Limit * 100
		}
		out = append(out, row)
	}
	sort.SliceStable(out, func(a, b int) bool {
		if out[a].HeapPct != out[b].HeapPct {
			return out[a].HeapPct > out[b].HeapPct
		}
		if out[a].Service != out[b].Service {
			return out[a].Service < out[b].Service
		}
		return out[a].Pod < out[b].Pod
	})
	total = len(out)
	if limit > 0 && len(out) > limit {
		out = out[:limit]
		truncated = true
	}
	return out, total, truncated
}

// sanitizePodInstanceRows / sanitizePodHeapRows — JSON kopyaları. SAF.
func sanitizePodInstanceRows(rows []PodInstanceRow) []PodInstanceRow {
	out := make([]PodInstanceRow, len(rows))
	for i, r := range rows {
		r.CPUPct = mcpFloat(r.CPUPct)
		r.MemBytes = mcpFloat(r.MemBytes)
		r.MemPct = mcpFloat(r.MemPct)
		out[i] = r
	}
	return out
}

func sanitizePodHeapRows(rows []PodHeapRow) []PodHeapRow {
	out := make([]PodHeapRow, len(rows))
	for i, r := range rows {
		r.HeapPct = mcpFloat(r.HeapPct)
		r.PostGCPct = mcpFloat(r.PostGCPct)
		r.UsedBytes = mcpFloat(r.UsedBytes)
		r.LimitBytes = mcpFloat(r.LimitBytes)
		r.PostGCBytes = mcpFloat(r.PostGCBytes)
		out[i] = r
	}
	return out
}

// ReadPodHealth — ORTAK OKUMA. İki okuma, iki PENCERE:
//
//   - heap: HER ZAMAN [now-RuntimePodWindow, now] (sustained 10dk
//     ortalaması; v0.9.1053'te pencere parametre oldu ve "şimdi"
//     semantiği çağıranda kuruluyor).
//   - envanter: çağıranın (from, to) penceresi, YALNIZ service != "" iken.
//
// Hata sözleşmesi: filo modunda heap hatası çağırana döner (tek veri o);
// servis modunda envanter hatası döner, heap hatası HeapUnavailable ile
// işaretlenir (envanter tek başına da cevaptır — guided davranışı).
func ReadPodHealth(ctx context.Context, d Deps, service string, from, to time.Time, instLimit, heapLimit int) (PodHealthData, error) {
	data := PodHealthData{Service: service, HeapWindowS: int(chstore.RuntimePodWindow / time.Second)}
	heapNow := nowOrAnchor(ctx)
	heap, herr := d.runtimePods().JVMHeapPodUsage(ctx, heapNow.Add(-chstore.RuntimePodWindow), heapNow)
	if service == "" {
		if herr != nil {
			return PodHealthData{}, herr
		}
		data.Heap, data.HeapTotal, data.HeapTruncated = podHeapRows(heap, "", heapLimit)
		return data, nil
	}
	inst, ierr := d.Store.ServiceInstances(ctx, service, from, to)
	if ierr != nil {
		return PodHealthData{}, ierr
	}
	data.Instances, data.InstanceTotal, data.UpCount, data.InstancesTruncated = podInstanceRows(inst, instLimit)
	if herr != nil {
		data.HeapUnavailable = true
		return data, nil
	}
	data.Heap, data.HeapTotal, data.HeapTruncated = podHeapRows(heap, service, heapLimit)
	return data, nil
}

// podHealthRestartNote — TEK kaynak (guided kanıt metni + tool gövdesi).
// Restart sayısı / pod fazı kube-state-metrics ister; OTel runtime
// metrikleri onları taşımaz ve model bunu bilmezse UYDURUR.
const podHealthRestartNote = "Restart counts and pod PHASE are NOT in this data — they need kube-state-metrics/Thanos and live on the service page's Pods tab. Never report a restart count from here."

// podHealthPayload — gövde üreticisi (saf, tablo testli).
func podHealthPayload(data PodHealthData, windowS int) map[string]any {
	body := map[string]any{
		"heap_window_s":  data.HeapWindowS,
		"heap":           sanitizePodHeapRows(data.Heap),
		"heap_count":     len(data.Heap),
		"heap_total":     data.HeapTotal,
		"heap_truncated": data.HeapTruncated,
		"has_more":       data.HeapTruncated, // v0.10.407 — sayfa doluysa "hepsi bu" değil (CoSRE denetimi M3)
		"restart_note":   podHealthRestartNote,
	}
	if data.Service == "" {
		body["scope"] = "fleet-wide"
		body["mode"] = "heap-saturation-ranking"
		// Filo modunda range_s'in HİÇBİR etkisi yok — söyle.
		body["window_note"] = "Fleet mode reads ONLY the live heap window; range_s has no effect here. Pass a service to get its pod inventory over a window."
		if len(data.Heap) == 0 {
			body["reasons"] = []string{
				"no pod in the estate publishes jvm.memory.used/limit (OTel runtime metrics) — normal for non-JVM services",
				"the JVM agent is running without runtime metrics enabled",
				"the pods restarted within the last 10 minutes and have not emitted a full window yet",
			}
			body["note"] = "Say plainly that no JVM heap metric is flowing; do NOT infer heap pressure from anything else here."
		}
		body["next"] = "Bir pod dolu görünüyorsa o servisin envanteri için service arg'ıyla tekrar çağır; sürekli baskı problem açtıysa list_problems; GC duraklamaları /services Pods sekmesinde."
		return body
	}
	body["service"] = data.Service
	body["scope"] = "one-service"
	body["mode"] = "pod-inventory+heap"
	body["inventory_window_s"] = windowS
	body["pods"] = sanitizePodInstanceRows(data.Instances)
	body["pod_count"] = len(data.Instances)
	body["pod_total"] = data.InstanceTotal
	body["pods_truncated"] = data.InstancesTruncated
	body["pods_up"] = data.UpCount
	body["up_definition"] = "up=true means a metric sample landed in the LAST 2 MINUTES of the window; up=false is 'silent' (crashed, drained, or stopped emitting) — it is not proof of a crash."
	if data.HeapUnavailable {
		body["heap_unavailable"] = true
		body["heap_note"] = "The heap read failed; the pod inventory above is still valid. Do NOT report heap figures."
	} else if len(data.Heap) == 0 {
		body["heap_note"] = "This service publishes no JVM heap metric (normal if it is not a JVM). Do not read that as healthy heap."
	}
	if len(data.Instances) == 0 {
		body["reasons"] = []string{
			"no pod of this service emitted metrics in the window — it may be scaled to zero, or metrics are not flowing",
			"the service name is not exact — confirm it with list_services",
			"the window predates the pods (freshly deployed) or is narrower than the scrape interval",
		}
		body["note"] = "Say plainly that no pod was observed for this service in the window; do NOT invent pod names."
	}
	body["next"] = "Pod sessizse (up=false) o servisin problemleri için list_problems; heap doluysa hata dalgası için get_log_histogram; yukarı-akış etkisi için get_blast_radius."
	return body
}

type getPodHealthArgs struct {
	Service string `json:"service,omitempty"`
	RangeS  int    `json:"range_s,omitempty"`
	Limit   int    `json:"limit,omitempty"`
}

func getPodHealthTool(d Deps) mcp.Tool {
	return mcp.Tool{
		Name:             "get_pod_health",
		ShortDescription: "Pod/JVM durumu (OTel runtime): `service` ile o servisin pod envanteri + heap doluluğu, service'siz filo geneli heap sıralaması. Restart sayısı ve pod fazı YOK.",
		Description: "Answer 'how are the PODS / the JVM doing' from OTel runtime metrics (no kube-state-metrics needed). TWO MODES: " +
			"with `service` you get that service's pod inventory (per pod: up flag, CPU %, memory, last-seen) PLUS its JVM heap saturation; " +
			"without `service` you get the fleet-wide heap-saturation ranking, fullest first. " +
			"heap_pct is the 10-minute average of used/limit; post_gc_pct (used_after_last_gc/limit) is the REAL pressure signal — a saw-tooth heap " +
			"reads 85%+ on a healthy pod, so judge pressure from post_gc_pct when it is present (0 means that metric is not flowing, NOT that the heap is empty). " +
			"TWO WINDOWS, on purpose: the heap read is ALWAYS the live last 10 minutes (it is a sustained average by design), while range_s only widens the " +
			"pod INVENTORY — and in fleet mode range_s does nothing at all. " +
			"RESTART COUNTS AND POD PHASE ARE NOT HERE (they need kube-state-metrics/Thanos); never state a restart count from this tool. " +
			"COST: one bounded metric read per mode. An empty result is NOT an error (non-JVM services publish no heap) and you get reasons.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"service": map[string]any{
					"type": "string",
					"description": "Exact service name (from list_services) for pod inventory + that service's heap. " +
						"Empty = fleet-wide heap-saturation ranking (no inventory, live 10-minute window only).",
				},
				"range_s": map[string]any{
					"type":    "integer",
					"minimum": 0,
					"maximum": podHealthMaxRangeS,
					"description": "Lookback seconds for the POD INVENTORY only. Default 1800 (30m), max 86400 (24h). Ignored entirely in fleet mode, " +
						"and it never changes the heap window (always the live 10 minutes). Note `up` is judged on the last 2 minutes of the window, " +
						"so a historical window can show every pod as silent.",
				},
				"limit": map[string]any{
					"type":        "integer",
					"minimum":     1,
					"maximum":     podHealthMaxInstanceRows,
					"description": "Max rows per list (pods and heap each). Default 12 pods / 10 heap rows.",
				},
			},
		},
		// Salt-okunur; REST eşleri GET /api/services/{name}/instances ve
		// runtime doygunluk okumaları viewer'a açık. Env arg'ı YOK:
		// okumaların env conjunct'ı yok (dosya başlığı).
		MinRole: "",
		Handler: func(ctx context.Context, raw json.RawMessage) (any, error) {
			var a getPodHealthArgs
			if len(raw) > 0 {
				if err := json.Unmarshal(raw, &a); err != nil {
					return nil, fmt.Errorf("decode args: %w", err)
				}
			}
			service := strings.TrimSpace(a.Service)
			windowS := podHealthWindowS(a.RangeS)
			from, to := rangeWindow(ctx, windowS)
			instLimit := clampLimit(a.Limit, podHealthDefaultInstanceRows, podHealthMaxInstanceRows)
			heapLimit := clampLimit(a.Limit, podHealthDefaultHeapRows, podHealthMaxHeapRows)
			data, err := ReadPodHealth(ctx, d, service, from, to, instLimit, heapLimit)
			if err != nil {
				return nil, err
			}
			return podHealthPayload(data, windowS), nil
		},
	}
}

// ─── list_problem_window_events ────────────────────────────────

const (
	// pwDefaultRangeS — 12 saat = guided'ın VARDİYA varsayılanı
	// (v0.9.416). "Dün gece neler oldu" sorusunun cevabı 30 dakikada
	// yok; sohbetin 30dk varsayılanı burada yanlış cevap olurdu.
	pwDefaultRangeS = 12 * 3600
	pwMaxRangeS     = 7 * 86400
	pwDefaultRows   = 25
	pwMaxRows       = 100
	// pwStoreRowLimit — ListProblemWindowEvents'in SQL LIMIT'i
	// (problem.go). Store total döndürmüyor; "tam bu sayıda geldi" tek
	// kesme sinyalimiz (>= bilinçli, dbOverviewCapped ile aynı kural).
	pwStoreRowLimit = 500
)

// pwWindowS — range_s → efektif saniye (varsayılan 12h, tavan 7 gün).
// Taban YOK: problems bir state tablosu, kova hizalaması yok, 5 dakikalık
// pencere de dürüst bir cevaptır. Saf, tablo testli.
func pwWindowS(rangeS int) int {
	if rangeS <= 0 {
		return pwDefaultRangeS
	}
	if rangeS > pwMaxRangeS {
		return pwMaxRangeS
	}
	return rangeS
}

// ProblemWindowRow — pencerede AÇILAN ya da ÇÖZÜLEN bir problem.
// chstore.Problem AYNEN geçmiyor: o şekil 25+ alan taşıyor ve bu odaklı
// okuma çoğunu (Value/Threshold/Reason/CoFiring…) HİÇ doldurmuyor —
// modelde "ölçüm 0" diye okunurdu.
type ProblemWindowRow struct {
	ID       string `json:"id"`
	RuleID   string `json:"rule_id,omitempty"`
	RuleName string `json:"rule_name"`
	Service  string `json:"service,omitempty"`
	Severity string `json:"severity"`
	Status   string `json:"status"`
	// Priority — OKUMA ANI hesabı (deploy sonra öncelik zinciri;
	// chstore.EnrichProblemsForRead). Boş kalırsa tüketici tier UYDURUR
	// (v0.9.554'ün dersi).
	Priority     string `json:"priority,omitempty"`
	StartedAtNs  int64  `json:"started_at_unix_ns"`
	ResolvedAtNs int64  `json:"resolved_at_unix_ns,omitempty"`
	// OpenedInWindow / ResolvedInWindow — satırın pencereye NİYE girdiği.
	// İkisi de false olabilir mi? Evet: pencereden ÖNCE açılmış ve hâlâ
	// açık bir problem, resolved_at penceredeyse listeye girer; sınıflama
	// bunu "hâlâ açık" sayar.
	OpenedInWindow   bool `json:"opened_in_window"`
	ResolvedInWindow bool `json:"resolved_in_window"`
}

// ProblemWindowData — sayaçlar + satırlar. Sayaçlar KESME ÖNCESİ tüm
// pencere üzerinden (satır listesi kırpılsa da "kaç açıldı" doğru kalır).
type ProblemWindowData struct {
	Rows      []ProblemWindowRow
	Total     int
	Truncated bool
	Opened    int
	Resolved  int
	StillOpen int
	// StoreCapped — okuma store tavanına dayandı; sayaçlar LOWER BOUND.
	StoreCapped   bool
	StoreRowLimit int
}

// problemWindowCounts — SAF sınıflama (opened / resolved / stillOpen).
// Kurallar guided vardiya özetinden birebir:
//   - opened:   started_at >= from
//   - resolved: resolved_at != nil && *resolved_at >= from
//   - stillOpen: çözülmemiş VE status open|acknowledged
//
// Bir satır AYNI ZAMANDA opened ve resolved olabilir (pencerede açılıp
// pencerede kapandı) — bu doğru, sayaçlar ayrık kümeler değil.
func problemWindowCounts(probs []chstore.Problem, fromNs int64) (opened, resolved, stillOpen int) {
	for _, p := range probs {
		if p.StartedAt >= fromNs {
			opened++
		}
		if p.ResolvedAt != nil && *p.ResolvedAt >= fromNs {
			resolved++
		} else if p.Status == "open" || p.Status == "acknowledged" {
			stillOpen++
		}
	}
	return opened, resolved, stillOpen
}

// problemWindowData — satır şekli + sıra (started_at DESC, en yeni önce)
// + kesme + sayaçlar. SAF, tablo testli.
func problemWindowData(probs []chstore.Problem, fromNs int64, limit int) ProblemWindowData {
	out := ProblemWindowData{StoreRowLimit: pwStoreRowLimit}
	out.Opened, out.Resolved, out.StillOpen = problemWindowCounts(probs, fromNs)
	out.Total = len(probs)
	out.StoreCapped = len(probs) >= pwStoreRowLimit
	rows := make([]ProblemWindowRow, 0, len(probs))
	for _, p := range probs {
		row := ProblemWindowRow{
			ID:             p.ID,
			RuleID:         p.RuleID,
			RuleName:       p.RuleName,
			Service:        p.Service,
			Severity:       p.Severity,
			Status:         p.Status,
			Priority:       p.Priority,
			StartedAtNs:    p.StartedAt,
			OpenedInWindow: p.StartedAt >= fromNs,
		}
		if p.ResolvedAt != nil {
			row.ResolvedAtNs = *p.ResolvedAt
			row.ResolvedInWindow = *p.ResolvedAt >= fromNs
		}
		rows = append(rows, row)
	}
	sort.SliceStable(rows, func(i, j int) bool { return rows[i].StartedAtNs > rows[j].StartedAtNs })
	if limit > 0 && len(rows) > limit {
		rows = rows[:limit]
		out.Truncated = true
	}
	out.Rows = rows
	return out
}

// ReadProblemWindowEvents — ORTAK OKUMA. Zincir: pencere okuması →
// OKUMA zenginleştirmesi (deploy ÖNCE, öncelik SONRA — tek çağrılabilir
// kural, chstore.EnrichProblemsForRead) → yapısal veri.
//
// Zenginleştirme atlanamaz: Priority bir CH kolonu değil okuma anı
// hesabıdır ve boş kalırsa tüketici tier'ı uydurur (v0.9.553/554).
func ReadProblemWindowEvents(ctx context.Context, d Deps, service string, from, to time.Time, limit int) (ProblemWindowData, error) {
	probs, err := d.Store.ListProblemWindowEvents(ctx, service, from, to)
	if err != nil {
		return ProblemWindowData{}, err
	}
	if len(probs) > 0 {
		probs = d.Store.EnrichProblemsForRead(ctx, probs, mcpDeployLookback)
	}
	return problemWindowData(probs, from.UnixNano(), limit), nil
}

// problemWindowEmptyReasons — pencerede hiç olay yok.
func problemWindowEmptyReasons(service string) []string {
	base := []string{
		"nothing fired and nothing resolved in this window — a quiet shift is the normal case, say so plainly",
		"the alert rules that would cover this are disabled, muted, or scoped to other services",
		"the window is too narrow — widen range_s before concluding the shift was quiet",
	}
	if service != "" {
		base = append(base, fmt.Sprintf("the service name %q is not exact, or its problems are attached to a global (service-less) rule — confirm with list_services", service))
	}
	return base
}

// problemWindowPayload — gövde üreticisi (saf, tablo testli).
func problemWindowPayload(data ProblemWindowData, service string, windowS int) map[string]any {
	body := map[string]any{
		"window_s":   windowS,
		"scope":      "fleet-wide",
		"events":     data.Rows,
		"count":      len(data.Rows),
		"total":      data.Total,
		"truncated":  data.Truncated,
		"opened":     data.Opened,
		"resolved":   data.Resolved,
		"still_open": data.StillOpen,
		"semantics": "A problem is listed when it OPENED in the window OR RESOLVED in the window — so a problem that started before the window and " +
			"closed inside it is here too (that is the right answer for 'what happened overnight'). opened+resolved do not sum to total; " +
			"a problem that opened AND closed inside the window counts in both.",
	}
	if service != "" {
		body["scope"] = "one-service"
		body["service"] = service
	}
	if len(data.Rows) == 0 {
		body["reasons"] = problemWindowEmptyReasons(service)
		body["note"] = "Say plainly that no alert opened or resolved in this window; do NOT invent incidents."
	}
	if data.Truncated {
		body["note"] = fmt.Sprintf("Only the %d newest of %d events are returned; the counters above cover the WHOLE window, so use them for totals.", len(data.Rows), data.Total)
	}
	if data.StoreCapped {
		body["store_capped"] = true
		body["store_row_limit"] = data.StoreRowLimit
		body["note"] = fmt.Sprintf("The store-side read hit its %d-row ceiling, so every counter here is a LOWER bound. Narrow the window or the service before quoting numbers.", data.StoreRowLimit)
	}
	body["next"] = "Bir olayın kök nedeni için get_problem_root_cause; aynı pencerede ne değiştiği için list_deploys + get_correlated_changes; hâlâ açık olanlar için list_problems."
	return body
}

type listProblemWindowEventsArgs struct {
	Service string `json:"service,omitempty"`
	RangeS  int    `json:"range_s,omitempty"`
	Limit   int    `json:"limit,omitempty"`
}

func listProblemWindowEventsTool(d Deps) mcp.Tool {
	return mcp.Tool{
		Name:             "list_problem_window_events",
		ShortDescription: "Pencerede AÇILAN ve ÇÖZÜLEN tüm Problem'ler (vardiya devri sorusu). list_problems yalnız ŞU ANKİ açık kümeyi verir — gece patlayıp kapanan olay orada görünmez.",
		Description: "Answer 'what HAPPENED in this window' — every Problem that opened OR resolved inside it, RESOLVED ONES INCLUDED, with " +
			"opened/resolved/still_open counters. This is the shift-handover question ('what happened overnight'), and it is the tool list_problems " +
			"cannot answer: list_problems shows the CURRENT set (status=open by default), so an incident that fired at 02:00 and cleared at 02:40 is " +
			"invisible there and is exactly what this returns. " +
			"Each row carries the P1/P2/P3 priority computed the same way the Problems page computes it (deploy enrichment then priority), so the tier " +
			"here matches the UI — never re-derive a tier yourself. " +
			"Use it to open an incident narrative, then get_problem_root_cause for the why and list_deploys for what changed. " +
			"COST: one bounded state-table read, cheap. BOUNDS: 500 events store-side; store_capped=true means every counter is a lower bound. " +
			"No env argument: this focused read takes no filter (list_problems' env is service-scoped via a different query path). " +
			"An empty window is NOT an error — a quiet shift is the normal case, and you get reasons.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"service": map[string]any{
					"type": "string",
					"description": "Exact service name (from list_services) to narrow to one service. Empty = fleet-wide, INCLUDING global " +
						"(service-less) rules — narrowing to a service drops those.",
				},
				"range_s": map[string]any{
					"type":    "integer",
					"minimum": 0,
					"maximum": pwMaxRangeS,
					"description": "Lookback seconds. Default 43200 (12h — a shift; 'last night' is not answerable in 30 minutes), max 604800 (7d). " +
						"The window is exact (no bucket rounding): problems are state rows, not a pre-aggregate.",
				},
				"limit": map[string]any{
					"type":        "integer",
					"minimum":     1,
					"maximum":     pwMaxRows,
					"description": "Max events to return, newest first. Default 25, max 100. The counters always cover the whole window.",
				},
			},
		},
		// Salt-okunur; aynı okumanın canlı tüketicileri GET /api/annotations
		// ve /shift sayfası, ikisi de viewer'a açık. Env arg'ı YOK —
		// gerekçe açıklamada ve dosya başlığında.
		MinRole: "",
		Handler: func(ctx context.Context, raw json.RawMessage) (any, error) {
			var a listProblemWindowEventsArgs
			if len(raw) > 0 {
				if err := json.Unmarshal(raw, &a); err != nil {
					return nil, fmt.Errorf("decode args: %w", err)
				}
			}
			service := strings.TrimSpace(a.Service)
			windowS := pwWindowS(a.RangeS)
			from, to := rangeWindow(ctx, windowS)
			data, err := ReadProblemWindowEvents(ctx, d, service, from, to, clampLimit(a.Limit, pwDefaultRows, pwMaxRows))
			if err != nil {
				return nil, err
			}
			return problemWindowPayload(data, service, windowS), nil
		},
	}
}
