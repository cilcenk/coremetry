package chstore

import (
	"context"
	"encoding/json"
	"fmt"
	"hash/fnv"
	"regexp"
	"sort"
	"strings"
)

// metric_exclusions.go — operatörün metrik grafiklerinden düşürmek
// istediği route'lar. v0.9.797.
//
// İSTEK: healthcheck / probe route'ları (`/health/checkStartup` gibi)
// gerçek trafiğin yanında yüksek hacimli ve TEKDÜZE. Route kırılımlı
// panelde ilk 10 serinin yarısını kapıyorlar ve GRUPSUZ toplamda
// ortalamayı aşağı çekiyorlar — yani "Toplam" çizgisi sağlıklı
// görünürken gerçek endpoint'ler yavaşlıyor olabiliyor.
//
// İKİ KADEMELİ, ve bu bilinçli:
//
//	OKUMA filtresi  — her zaman, GEÇMİŞ dahil. Kural eklendiği anda
//	                  eski veri de temizlenmiş görünür; kural silinince
//	                  aynı anda geri gelir. Hiçbir şey kaybolmaz.
//	INGEST drop     — kural BAŞINA çekbox, varsayılan KAPALI. Yazılmayan
//	                  datapoint geri gelmez; bu yüzden ayrı bir onay.
//
// KAPSAM: yalnız METRİK datapoint'leri. Span'lere DOKUNULMAZ — trace
// listesi, servis RED'i, hata oranı hepsi span türevli ve bu ayar
// onların hiçbirini değiştirmez. Operatör "grafikten düşür" derken
// metriği kastediyor; span tarafı ayrı bir karar (Pipeline kuralları
// zaten var).
//
// ATTR ANAHTARI şimdilik TEK: http.route. Alan (`attrKey`) modelde var
// ve ayarda saklanıyor ki genişletme bir şema değişikliği değil bir
// doğrulama gevşetmesi olsun; bugün başka bir değer 400 döner —
// desteklenmeyen bir anahtarı sessizce kabul edip hiçbir şeyi
// filtrelememek, bu depoda tekrarlayan sessiz-başarısızlık sınıfı.

// MetricExclusionAttrKey — bugün desteklenen TEK datapoint attr'ı.
const MetricExclusionAttrKey = "http.route"

// MetricExclusionWildcard — `metric` alanında "her metrik" anlamı.
const MetricExclusionWildcard = "*"

const metricExclusionsKey = "metric_exclusions"

// MetricExclusionRule — tek bir dışlama kuralı.
type MetricExclusionRule struct {
	// Metric — TAM metrik adı ya da "*" (her metrik). Ön ek/desen YOK:
	// metrik adı zaten bir katalog değeri, orada ikinci bir desen dili
	// açmak iki farklı eşleşme semantiği demek olurdu.
	Metric string `json:"metric"`
	// AttrKey — bugün yalnız "http.route". Boş gelirse ona normalize
	// edilir (elle düzenlenmiş bir settings satırı da çalışsın).
	AttrKey string `json:"attrKey"`
	// Pattern — RE2 deseni, ANKORSUZ: `/health` yolun herhangi bir
	// yerinde eşleşir. Bilinçli olarak FilterExpr'in `=~` operatöründen
	// FARKLI (o PromQL semantiği gereği ^(?:…)$ ile tam ankorlar):
	// operatör buraya "şu yolları at" diye bir parça yazıyor, bir PromQL
	// etiket eşleştiricisi değil. Tam eşleşme isteyen ^…$ yazar.
	//
	// Aynı ankorsuzluk ingest tarafında da geçerli (regexp.MatchString
	// da ankorsuz) — iki kademe AYNI kümeyi seçmezse okuma filtresi
	// ingest'in bıraktığı satırları temizleyemezdi.
	Pattern string `json:"pattern"`
	// DropAtIngest — datapoint hiç YAZILMASIN. Varsayılan false.
	DropAtIngest bool `json:"dropAtIngest"`
}

// MetricExclusions — system_settings blob'u.
type MetricExclusions struct {
	Rules []MetricExclusionRule `json:"rules"`
}

// compiledExclusion — derlenmiş tek kural.
type compiledExclusion struct {
	metric  string
	attrKey string
	pattern string
	re      *regexp.Regexp
	drop    bool
}

// CompiledMetricExclusions — derlenmiş kural seti, atomic pointer'da
// taşınmak üzere DEĞİŞMEZ (immutable). Derleme PUT/hidrasyon anında bir
// kez yapılır; sıcak yolda (her datapoint, her sorgu) yalnız okunur.
//
// Sıfır değeri ve nil işaretçi GEÇERLİ ve BOŞ: her metot nil-güvenli,
// böylece "ayar hiç okunmadı" ile "kural yok" aynı davranır — Store'u
// doğrudan kuran testler de dahil.
type CompiledMetricExclusions struct {
	rules    []compiledExclusion
	digest   string
	anyDrop  bool
	metrics  map[string][]string // metrik → desenler (tam adlı kurallar)
	wildcard []string            // "*" kurallarının desenleri
}

// CompileMetricExclusions — ayar → derlenmiş set. Bozuk desen HATA
// döndürür (PUT 400); çağıran boot hidrasyonunda hatayı loglayıp BOŞ
// sette kalır (bir ayar okuması boot'u düşürmez).
func CompileMetricExclusions(c MetricExclusions) (*CompiledMetricExclusions, error) {
	out := &CompiledMetricExclusions{
		metrics: make(map[string][]string, len(c.Rules)),
	}
	for i, r := range c.Rules {
		metric := strings.TrimSpace(r.Metric)
		if metric == "" {
			return nil, fmt.Errorf("kural %d: metric boş olamaz (tam ad ya da %q)", i+1, MetricExclusionWildcard)
		}
		attrKey := strings.TrimSpace(r.AttrKey)
		if attrKey == "" {
			attrKey = MetricExclusionAttrKey
		}
		if attrKey != MetricExclusionAttrKey {
			return nil, fmt.Errorf("kural %d: attrKey bugün yalnız %q olabilir (gelen: %q)",
				i+1, MetricExclusionAttrKey, attrKey)
		}
		pattern := strings.TrimSpace(r.Pattern)
		if pattern == "" {
			return nil, fmt.Errorf("kural %d: pattern boş olamaz", i+1)
		}
		re, err := regexp.Compile(pattern)
		if err != nil {
			return nil, fmt.Errorf("kural %d: geçersiz RE2 deseni %q: %w", i+1, pattern, err)
		}
		ce := compiledExclusion{metric: metric, attrKey: attrKey, pattern: pattern, re: re, drop: r.DropAtIngest}
		out.rules = append(out.rules, ce)
		if r.DropAtIngest {
			out.anyDrop = true
		}
		if metric == MetricExclusionWildcard {
			out.wildcard = append(out.wildcard, pattern)
		} else {
			out.metrics[metric] = append(out.metrics[metric], pattern)
		}
	}
	out.digest = exclusionDigest(out.rules)
	return out, nil
}

// exclusionDigest — kural setinin SIRADAN BAĞIMSIZ özeti. Önbellek
// anahtarı parçası olarak kullanılır.
//
// CLAUDE.md sert kısıtı (v0.5.187): anahtar TÜM girdileri hash'ler,
// len() DEĞİL. Dışlama seti sorgunun SONUCUNU değiştiriyor — anahtarda
// olmasaydı kural eklenir eklenmez 30 sn boyunca filtresiz sonuç
// servis edilirdi, ve iki pod (biri yenilemiş biri yenilememiş) aynı
// panelde farklı sayı gösterirdi.
//
// Boş set → "0": çağıran parçayı koşullu olarak atlamak zorunda kalmaz
// (excludeKeyDigest'in aynı sözleşmesi).
func exclusionDigest(rules []compiledExclusion) string {
	if len(rules) == 0 {
		return "0"
	}
	parts := make([]string, 0, len(rules))
	for _, r := range rules {
		// drop bayrağı da özete girer: aynı desen ingest'te düşerken
		// düşmezken farklı bir dünya (rollup zamanla temizlenir).
		parts = append(parts, fmt.Sprintf("%s\x00%s\x00%s\x00%t", r.metric, r.attrKey, r.pattern, r.drop))
	}
	sort.Strings(parts)
	h := fnv.New64a()
	for _, p := range parts {
		h.Write([]byte(p))
		h.Write([]byte{0})
	}
	return fmt.Sprintf("%x", h.Sum64())
}

// Empty — hiç kural yok mu. nil-güvenli.
func (c *CompiledMetricExclusions) Empty() bool {
	return c == nil || len(c.rules) == 0
}

// Digest — önbellek anahtarı parçası. nil-güvenli ("0").
func (c *CompiledMetricExclusions) Digest() string {
	if c == nil || c.digest == "" {
		return "0"
	}
	return c.digest
}

// AnyDropAtIngest — ingest sıcak yolu için TEK bayrak: false ise
// datapoint başına hiçbir ek iş yapılmaz.
func (c *CompiledMetricExclusions) AnyDropAtIngest() bool {
	return c != nil && c.anyDrop
}

// Rules — ayarın kendisi (UI/GET yanıtı). nil-güvenli, kopya döner.
func (c *CompiledMetricExclusions) Rules() []MetricExclusionRule {
	if c == nil {
		return nil
	}
	out := make([]MetricExclusionRule, 0, len(c.rules))
	for _, r := range c.rules {
		out = append(out, MetricExclusionRule{
			Metric: r.metric, AttrKey: r.attrKey, Pattern: r.pattern, DropAtIngest: r.drop,
		})
	}
	return out
}

// RoutePatterns — verilen metriğe uygulanan desenler (tam adlı kurallar
// ÖNCE, sonra "*" kuralları). Sıra DETERMİNİSTİK olmak zorunda: SQL
// bundan üretiliyor ve sırası değişen bir WHERE, önbellek anahtarı aynı
// kalırken farklı bir sorgu demek olurdu.
//
// Kural yoksa nil döner ve çağıran SQL'e HİÇBİR ŞEY eklemez — sıfır-etki
// pini (metric_exclusions_test.go) bunu bayt-bayt kilitliyor.
func (c *CompiledMetricExclusions) RoutePatterns(metric string) []string {
	if c == nil || len(c.rules) == 0 {
		return nil
	}
	exact := c.metrics[metric]
	if len(exact) == 0 && len(c.wildcard) == 0 {
		return nil
	}
	out := make([]string, 0, len(exact)+len(c.wildcard))
	out = append(out, exact...)
	out = append(out, c.wildcard...)
	return out
}

// DropAtIngest — bu (metrik, route) datapoint'i YAZILMAMALI mı.
//
// Yalnız dropAtIngest işaretli kurallar sayılır: okuma filtresi
// kurallarının ingest'e sızması, operatörün açıkça KAPALI bıraktığı bir
// çekboxu görmezden gelmek olurdu.
func (c *CompiledMetricExclusions) DropAtIngest(metric, route string) bool {
	if c == nil || !c.anyDrop {
		return false
	}
	for _, r := range c.rules {
		if !r.drop {
			continue
		}
		if r.metric != MetricExclusionWildcard && r.metric != metric {
			continue
		}
		if r.re.MatchString(route) {
			return true
		}
	}
	return false
}

// applyMetricExclusionWhere — okuma-zamanı dışlamayı bir whereClause'a
// enjekte eder. metric_points üzerinde çalışan HER MetricQueryFilter
// yolunun (ham GROUP BY, rate/increase) tek kapısı.
//
// BIND-ARG ŞART: desen operatörden geliyor. Elle string birleştirmek
// hem enjeksiyon yüzeyi hem de önbellek/plan cache kirliliği olurdu —
// bu depoda `toDateTime64(?)` disiplininin aynısı.
//
// route ifadesi groupKeyExprMetric'ten geliyor, yani panelin GRUPLADIĞI
// ifadenin BİREBİR aynısı: kırılımda görünen değer ile dışlanan değer
// ayrışamaz.
//
// http.route attr'ı OLMAYAN satırlar '' üretir ve `/health` gibi bir
// desen '' ile eşleşmez → satır KALIR. Route'suz datapoint'leri elemek
// isteyen `^$` yazar; sessizce elemek, "grafikte bir şey eksik ama ne
// bilmiyorum" sınıfı olurdu.
func applyMetricExclusionWhere(wc *whereClause, ex *CompiledMetricExclusions, metric string) {
	pats := ex.RoutePatterns(metric)
	if len(pats) == 0 {
		return // SIFIR ETKİ: kural yokken SQL bayt-bayt eski
	}
	expr, exprArgs := groupKeyExprMetric(MetricExclusionAttrKey)
	for _, p := range pats {
		args := make([]any, 0, len(exprArgs)+1)
		args = append(args, exprArgs...)
		args = append(args, p)
		wc.add("NOT match("+expr+", ?)", args...)
	}
}

// rollupAttrlessBlocked — ROLLUP DÜRÜSTLÜK KAPISI.
//
// rollup_metrics_{1m,5m,1h} (aile-C) ve histogram ikizi attr TAŞIMIYOR:
// satırlar route boyutunda ZATEN katlanmış. Bir dışlama kuralı aktifken
// o kademeden okumak, dışlanan route'un katkısını İÇİNDE taşıyan bir
// sayı döndürmek demek — panel "temizlendi" der ama sayı kirli kalır.
// Ham yol yavaş ama DOĞRU; bu tercih bu dosyalardaki fail-open duruşuyla
// aynı yönde (yanlış hızlıdansa doğru yavaş).
//
// route-tier (rollup_metrics_route_*) bu kapıya TAKILMAZ: orada route
// bir KOLON, NOT koşulu doğrudan eklenebiliyor (buildRollupRouteSQL).
//
// Fark KAPANIR: dropAtIngest işaretli bir kural zamanla rollup'ları da
// temizler (yeni satırlar dışlanan route'u hiç görmez), ama TTL boyunca
// eski satırlar kirli kalır — o yüzden kapı bayrağa değil KURALIN
// VARLIĞINA bakıyor.
func rollupAttrlessBlocked(ex *CompiledMetricExclusions, metric string) bool {
	return len(ex.RoutePatterns(metric)) > 0
}

// ── Kalıcılık (system_settings) ───────────────────────────────────────

// GetMetricExclusions — kaydedilmiş ayar, yoksa boş set. Soft-fail:
// geçici bir CH tökezlemesi grafikleri sessizce DEĞİŞTİREMEZ, boş sete
// düşer (GetExceptionTriage'ın duruşu).
func (s *Store) GetMetricExclusions(ctx context.Context) MetricExclusions {
	raw, err := s.GetSetting(ctx, metricExclusionsKey)
	if err != nil || len(raw) == 0 {
		return MetricExclusions{}
	}
	var c MetricExclusions
	if err := json.Unmarshal(raw, &c); err != nil {
		return MetricExclusions{}
	}
	return c
}

// SaveMetricExclusions — system_settings'e yazar. Yeni şema YOK.
func (s *Store) SaveMetricExclusions(ctx context.Context, c MetricExclusions) error {
	raw, err := json.Marshal(c)
	if err != nil {
		return err
	}
	return s.PutSetting(ctx, metricExclusionsKey, raw)
}

// SetMetricExclusions — derlenmiş seti yayınlar (atomic). PUT anında ve
// 30 sn'lik yenileme tikinde çağrılır; okuyucular kilitsiz okur.
func (s *Store) SetMetricExclusions(c *CompiledMetricExclusions) {
	s.metricExclusions.Store(c)
}

// MetricExclusions — canlı derlenmiş set. ASLA nil dönmez, ama nil
// dönse de bütün metotlar nil-güvenli.
func (s *Store) MetricExclusions() *CompiledMetricExclusions {
	return s.metricExclusions.Load()
}
