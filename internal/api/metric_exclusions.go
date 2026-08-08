// v0.9.797 — metrik route dışlama kuralları operatörün eline geçti.
//
// İSTEK: healthcheck / probe route'ları grafiklerden düşsün. Yüksek
// hacimli ve tekdüze oldukları için hem route kırılımlı panelin ilk 10
// serisini yiyorlar hem de GRUPSUZ ortalamayı aşağı çekiyorlar —
// "Toplam" çizgisi sağlıklı görünürken gerçek endpoint'ler yavaşlıyor
// olabiliyor.
//
// KABLO: exception_triage (v0.9.775) emsali — system_settings blob'u +
// boot hidrasyonu + 30 sn yenileme + admin PUT + audit. Fark şu: ayar
// bir SAYI değil bir KURAL SETİ, yani derlenmiş regex'ler taşıyor;
// derleme PUT/hidrasyon anında bir kez yapılıp atomic pointer'da
// yayınlanıyor (chstore.CompiledMetricExclusions).
//
// İNGEST DROP'U KENDİ MOTORUNU KURMUYOR — bu sürümün en önemli kararı.
// Üründe metrik ingest drop motoru ZATEN VAR: internal/pipeline
// (AcceptMetric, v0.8.282). İkinci bir drop yolu açmak, aynı soruya
// ("bu datapoint yazılsın mı?") iki ayrı yerde cevap veren iki motor
// demekti: sayaçlar ayrışır, /admin/stats'ta drop'ların yarısı görünür,
// ve operatör Pipeline sekmesinde HİÇBİR iz olmadan veri kaybı yaşardı.
// Bunun yerine `dropAtIngest` çekboxı bir pipeline kuralı TÜRETİYOR
// (deriveExclusionRule): tek motor, tek sayaç (MetricsDroppedByPipeline),
// ve kural Pipeline sekmesinde de görünüyor.
package api

import (
	"context"
	"encoding/json"
	"fmt"
	"hash/fnv"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/cilcenk/coremetry/internal/chstore"
	"github.com/cilcenk/coremetry/internal/pipeline"
)

// LoadMetricExclusions hydrates the compiled rule set from
// system_settings into the Store. Bozuk blob → BOŞ set + log: bir ayar
// okuması boot'u düşürmez ve yarım derlenmiş bir set (bazı kurallar
// uygulanıyor, bazıları değil) her ikisinden de kötü olurdu.
func (s *Server) LoadMetricExclusions(ctx context.Context) {
	cfg := s.store.GetMetricExclusions(ctx)
	compiled, err := chstore.CompileMetricExclusions(cfg)
	if err != nil {
		log.Printf("[settings] metric_exclusions derlenemedi, kural UYGULANMIYOR: %v", err)
		compiled, _ = chstore.CompileMetricExclusions(chstore.MetricExclusions{})
	}
	s.store.SetMetricExclusions(compiled)
}

// StartMetricExclusionsRefresh — çok-pod yakınsaması (exception_triage
// kablosunun aynısı). PUT'u alan pod anında günceller; diğerleri bir
// sonraki tikte. Leader gerekmiyor: bu bir OKUMA.
func (s *Server) StartMetricExclusionsRefresh(ctx context.Context, interval time.Duration) {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			s.LoadMetricExclusions(ctx)
		}
	}
}

// getMetricExclusions — GET /api/settings/metric-exclusions.
//
// CANLI derlenmiş setten değil KAYITLI blob'dan okunuyor: ikisi ayrışmış
// olabilir (bu pod henüz yenilemedi) ve ayar sayfası KAYDEDİLENİ
// göstermeli — operatör ne yazdığını görsün, hangi pod'a düştüğünü değil.
func (s *Server) getMetricExclusions(w http.ResponseWriter, r *http.Request) {
	cfg := s.store.GetMetricExclusions(r.Context())
	if cfg.Rules == nil {
		cfg.Rules = []chstore.MetricExclusionRule{}
	}
	writeJSON(w, cfg)
}

// putMetricExclusions validates + persists the rules, swaps the compiled
// set on THIS pod (restart yok), ve dropAtIngest'li kuralların pipeline
// ikizlerini senkronlar.
func (s *Server) putMetricExclusions(w http.ResponseWriter, r *http.Request) {
	var cfg chstore.MetricExclusions
	if err := json.NewDecoder(r.Body).Decode(&cfg); err != nil {
		http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
		return
	}
	if len(cfg.Rules) > maxMetricExclusionRules {
		http.Error(w, fmt.Sprintf("en çok %d kural", maxMetricExclusionRules), http.StatusBadRequest)
		return
	}
	// Derleme = doğrulama: bozuk RE2, boş metric/pattern, desteklenmeyen
	// attrKey hepsi burada 400 döner. Kaydedip sonra "uygulanamadı"
	// demek, bu depoda tekrarlayan sessiz-başarısızlık sınıfı olurdu.
	compiled, err := chstore.CompileMetricExclusions(cfg)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err := s.store.SaveMetricExclusions(r.Context(), cfg); err != nil {
		writeErr(w, err)
		return
	}
	s.store.SetMetricExclusions(compiled)
	// Pipeline ikizleri: dropAtIngest işaretli kurallar için türetilmiş
	// drop kuralları yazılır, işareti kalkanlarınki SİLİNİR. Hata
	// yutulmuyor ama isteği de düşürmüyor: okuma filtresi ZATEN kaydedildi
	// ve çalışıyor, ingest köprüsü ikinci kademe.
	if err := s.syncExclusionPipelineRules(r.Context(), cfg); err != nil {
		log.Printf("[settings] metric_exclusions → pipeline köprüsü: %v", err)
	}
	s.audit(r, "settings.update", "metric_exclusions", "metric_exclusions",
		fmt.Sprintf(`{"rules":%d,"dropAtIngest":%d,"digest":%q}`,
			len(cfg.Rules), countDropAtIngest(cfg), compiled.Digest()))
	log.Printf("[settings] metric_exclusions: %d kural (%d ingest-drop) · digest %s",
		len(cfg.Rules), countDropAtIngest(cfg), compiled.Digest())
	writeJSON(w, cfg)
}

// maxMetricExclusionRules — her kural HER metrik sorgusunun WHERE'ine bir
// NOT match() ekliyor. Üst sınır, "50 kural yazdım ve grafikler durdu"
// vakasını kod tarafında imkânsız kılıyor.
const maxMetricExclusionRules = 50

func countDropAtIngest(c chstore.MetricExclusions) int {
	n := 0
	for _, r := range c.Rules {
		if r.DropAtIngest {
			n++
		}
	}
	return n
}

// ── Pipeline köprüsü ──────────────────────────────────────────────────

// exclusionRuleIDPrefix — TÜRETİLMİŞ pipeline kurallarının kimlik öneki.
// Sahiplik işareti: bu önekle başlayan kurallar metric_exclusions'a
// aittir ve senkronizasyon onları silebilir. Operatörün elle yazdığı
// kurallar (rule-…) ASLA dokunulmaz.
const exclusionRuleIDPrefix = "metric-excl-"

// deriveExclusionRule — dışlama kuralı → pipeline drop kuralı. SAF
// (tablo-testli).
//
// ŞEKİL, ve neden böyle:
//
//	when: http.route =~ <pattern>   — okuma filtresiyle AYNI desen, AYNI
//	                                  ankorsuz RE2 semantiği. İki kademe
//	                                  farklı küme seçerse "grafikte yok
//	                                  ama tabloda var" olurdu.
//	and:  metric = <ad>             — YALNIZ tam adlı kuralda. Bu koşul
//	                                  olmasaydı tek bir metrik için
//	                                  kurulan dışlama BÜTÜN metriklerin
//	                                  o route'unu yazılmaz yapardı —
//	                                  operatörün istemediği veri kaybı.
//	                                  "*" kuralında zaten kasıt bu, koşul
//	                                  eklenmez.
//
// ID deterministik (metric+pattern özeti): aynı kural iki kez
// kaydedilince ikinci bir ikiz DEĞİL aynı ikiz güncellenir; kural
// silinince ikizi de kimliğinden bulunup silinir.
func deriveExclusionRule(r chstore.MetricExclusionRule) pipeline.Rule {
	metric := strings.TrimSpace(r.Metric)
	pattern := strings.TrimSpace(r.Pattern)
	h := fnv.New64a()
	h.Write([]byte(metric))
	h.Write([]byte{0})
	h.Write([]byte(pattern))
	out := pipeline.Rule{
		ID:      fmt.Sprintf("%s%x", exclusionRuleIDPrefix, h.Sum64()),
		Name:    fmt.Sprintf("metric-excl: %s ~ %s", metric, pattern),
		Kind:    pipeline.KindDrop,
		Signal:  pipeline.SignalMetrics,
		Enabled: true,
		When: pipeline.Condition{
			Key: chstore.MetricExclusionAttrKey, Op: pipeline.OpMatches, Value: pattern,
		},
	}
	if metric != chstore.MetricExclusionWildcard {
		out.And = []pipeline.Condition{{Key: "metric", Op: pipeline.OpEq, Value: metric}}
	}
	return out
}

// desiredExclusionRules — SAF: ayar → istenen ikiz kural kümesi (ID'ye
// göre). dropAtIngest işaretsiz kurallar burada YOK: okuma filtresi
// kurallarının ingest'e sızması, operatörün açıkça kapalı bıraktığı bir
// çekboxu görmezden gelmek olurdu.
func desiredExclusionRules(c chstore.MetricExclusions) map[string]pipeline.Rule {
	out := make(map[string]pipeline.Rule)
	for _, r := range c.Rules {
		if !r.DropAtIngest {
			continue
		}
		d := deriveExclusionRule(r)
		out[d.ID] = d
	}
	return out
}

// staleExclusionRuleIDs — SAF: mevcut pipeline kataloğu + istenen küme →
// silinecek ikizler. Yalnız `metric-excl-` önekli kurallar aday; elle
// yazılmış kurallara dokunulmaz.
func staleExclusionRuleIDs(existing []pipeline.Rule, want map[string]pipeline.Rule) []string {
	var out []string
	for _, e := range existing {
		if !strings.HasPrefix(e.ID, exclusionRuleIDPrefix) {
			continue
		}
		if _, ok := want[e.ID]; !ok {
			out = append(out, e.ID)
		}
	}
	return out
}

// syncExclusionPipelineRules — köprünün icra tarafı. Pipeline motoru
// yoksa (api-only pod, testler) sessizce no-op.
//
// Yenileme tikinde ÇAĞRILMIYOR, yalnız PUT'ta: her pod her 30 saniyede
// pipeline blob'unu yeniden yazsaydı bu bir yazma fırtınası olurdu.
// Blob'un podlar arası yayılımı pipeline'ın KENDİ StartConfigRefresh'i
// üzerinden — tek yayılım mekanizması, tek sahip.
func (s *Server) syncExclusionPipelineRules(ctx context.Context, cfg chstore.MetricExclusions) error {
	if s.pipeline == nil {
		return nil
	}
	want := desiredExclusionRules(cfg)
	var firstErr error
	for _, id := range staleExclusionRuleIDs(s.pipeline.Rules(), want) {
		if err := s.pipeline.Delete(ctx, s.store, id); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	for _, r := range want {
		if _, err := s.pipeline.Upsert(ctx, s.store, r); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}
