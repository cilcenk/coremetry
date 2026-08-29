package api

// dashboards_data.go — v0.10.146 (perf bütçesi P5: bundle 1.6 MB / 12 panel,
// cache'siz).
//
// POST /api/dashboards/data api.go'dan buraya taşındı (api.go BÜYÜMEYECEK
// kuralı; rota kaydı orada tek satır). Değişenler:
//
//   - Panel-başı cache. Tekil /api/spans/metric ve /api/metrics/query 30 s
//     SWR cache'liyken bundle CACHE'SİZDİ: auto-refresh tick'i (v0.9.779)
//     ve ikinci operatör her seferinde CH'ye iniyordu. Her slot
//     dashPanelKey (saf, TÜM girdileri hash'ler; pencere 30 s kovaya
//     oturur çünkü FE her tick'te from/to'yu yeniden hesaplar) ile
//     cachedJSON'dan (serveCached çekirdeği) geçer; TTL tekil uçla aynı.
//   - Hata slot'u CACHE'LENMEZ: CH timeout'u bir paneli 30 s (SWR ile 90 s)
//     boyunca hatada dondurmasın; bir sonraki tick yeniden dener.
//   - Boş sonuç `"series": []` (eskiden omitempty ile düşerdi): FE
//     `series === undefined`'ı "henüz bundle'lanmadı → kendi fetch'ini yap"
//     sayıyor (PanelDataOverride) — boş panel bundle'a rağmen bir kez daha
//     sorguluyordu.
//
// BİLİNÇLİ OLARAK YAPILMAYAN — top-N kırpması. Tekil uç QuerySpanMetricTopN
// ile top-50'yi döner; bundle'ı ona yakınsatmak gövdeyi ~yarıya indirirdi
// (peer/caller panelleri 95-96 seri). Ama dashboard'ın "others" katlaması
// (foldTopN, v0.9.946) kuyruğu TAM seriden toplar: kırpılmış girdiyle
// "others" çizgisi kuyruk kütlesini sessizce kaybeder — boş panelden
// tehlikeli olan "makul görünen yanlış sayı" sınıfı. Doğru yol kuyruk
// ön-toplamları (othersSum/othersCount, birim-bağımsız; FE kesin katlar) —
// ayrı spec. O gelene dek spanMetric dalı QuerySpanMetric (tam seri).
//
// Request body:
//
//	{ "from": <unix ns>, "to": <unix ns>,
//	  "requests": [
//	    { "id": "p1", "type": "metric",
//	      "name": "...", "service": "...", "agg": "...",
//	      "groupBy": [...], "step": 60 },
//	    { "id": "p2", "type": "spanMetric",
//	      "agg": "p99", "field": "duration_ms",
//	      "filters": "<json>", "dsl": "...",
//	      "groupBy": [...], "step": 60 },
//	  ] }
//
// Response:
//
//	{ "p1": { "series": [...] },
//	  "p2": { "series": [], "error": "..." } }

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"hash/fnv"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/cilcenk/coremetry/internal/chstore"
)

// dashPanelTTL — tekil /api/spans/metric ve /api/metrics/query ile aynı
// (30 s). Farklı bir TTL, aynı panelin iki yoldan farklı tazelikte
// görünmesi demek olurdu.
const dashPanelTTL = 30 * time.Second

// bundleReq — bundle gövdesindeki tek panel isteği. Adlandırıldı çünkü
// dashPanelKey (saf) ve bundleSlot onu paylaşıyor.
type bundleReq struct {
	ID   string `json:"id"`
	Type string `json:"type"`
	// metric
	Name    string `json:"name"`
	Service string `json:"service"`
	// shared
	Agg     string   `json:"agg"`
	Field   string   `json:"field"`
	GroupBy []string `json:"groupBy"`
	Step    int      `json:"step"`
	// span metric
	Filters json.RawMessage `json:"filters"`
	DSL     string          `json:"dsl"`
}

// bundleSlot — tek panelin yanıtı.
type bundleSlot struct {
	// Series omitempty DEĞİL — boş sonuç `[]` yazılır (dosya başlığı).
	Series []chstore.SpanMetricSeries `json:"series"`
	// RowsCapped (v0.9.459, dürüstlük A1b) — 50k satır tavanı doldu:
	// alfabetik-son seriler eksik olabilir. Bundle yolu tekil
	// endpoint'lerin v0.9.458 zarfını atlıyordu.
	RowsCapped bool   `json:"rowsCapped,omitempty"`
	Error      string `json:"error,omitempty"`
	// Note (v0.9.1157, VM Faz 2) — bu slot neden BOŞ döndü. Tekil
	// handler'ın zarfıyla aynı alan, aynı gerekçeyle: VM yolunda bir
	// yüzdelik, operatörün yazmadığı `<ad>_bucket` serisini sorguyor.
	// Notu tekil uca koyup burada atlamak, aynı yüzdeliğin Explore'da
	// SEBEBİYLE, dashboard panelinde SESSİZCE boş görünmesi demekti.
	Note string `json:"note,omitempty"`
}

// dashPanelKey — SAF: panel isteğinin cache anahtarı. Regresyon testi
// (dashboards_data_test.go) bunu doğrudan pinler.
//
// Anahtara giren HER şey yanıtı değiştirir (v0.5.187 kuralı): panel
// türü, metrik adı/servis, agg/field, groupBy (SIRALI — sunucu
// tuple sırasını korur), step, filtreler (json.Compact — FE'nin
// boşluk farkı ayrı girdi üretmesin), DSL, kaynak damgası (metric
// dalında backend adı + ad-kuralı damgası + dışlama özeti; spanMetric
// dalında boş — tekil uç da kaynağa bağlı değil) ve 30 s'lik pencere
// kovası (cacheBucket). Kova, tekil uçtan bilinçli fark: FE tekil
// istekte from/to'yu memoize eder, bundle'da her tick yeniden hesaplar;
// kovasız anahtar hiç isabet almazdı. Bedel ≤ 30 s'lik pencere kayması —
// SWR'ın zaten kabul ettiği bayatlık.
func dashPanelKey(srcTag string, q bundleReq, from, to time.Time) string {
	var filt string
	if len(bytes.TrimSpace(q.Filters)) > 0 {
		var buf bytes.Buffer
		if err := json.Compact(&buf, q.Filters); err == nil {
			filt = buf.String()
		} else {
			filt = string(q.Filters)
		}
		// null / [] = filtre yok — parseFilters ikisini de boş derler,
		// ayrı girdi açmak aynı sorguyu iki kez hesaplatırdı.
		if filt == "null" || filt == "[]" {
			filt = ""
		}
	}
	field := q.Field
	if q.Type == "metric" {
		field = "" // metric dalı Field okumaz (bundleSlot); anahtara girmesin.
	}
	// Parçalar UZUNLUK-önekli: çıplak ayırıcıyla ("a\x00b","c") ve
	// ("a","b\x00c") aynı ön-görüntüyü üretirdi (inceleme, v0.10.146).
	h := fnv.New64a()
	for _, part := range []string{
		q.Type, q.Name, q.Service, q.Agg, field,
		strconv.Itoa(len(q.GroupBy)), strings.Join(q.GroupBy, "\x1f"), strconv.Itoa(q.Step),
		filt, q.DSL, srcTag, cacheBucket(from, to),
	} {
		h.Write([]byte(strconv.Itoa(len(part))))
		h.Write([]byte{':'})
		h.Write([]byte(part))
	}
	return fmt.Sprintf("dash-panel:v1:%s:%x", q.Type, h.Sum64())
}

func (s *Server) dashboardsData(w http.ResponseWriter, r *http.Request) {
	// v0.9.1151 — backend seçimi bundle'ın BAŞINDA, gövdeyi okumadan
	// ÖNCE. İki sebep: (a) geçersiz bir ?metricsrc= 50 panellik gövdeyi
	// ayrıştırmadan reddedilir, (b) seçim bundle başına TEK kalır — 50
	// panelin yarısı CH yarısı VM'den okunmuş bir dashboard, ayarın tam
	// ortasında değiştiği tek bir istekte üretilebilirdi ve hangi panelin
	// nereden geldiği ekranda görünmezdi.
	//
	// POST olduğu için param SORGU DİZESİNDE taşınıyor (gövdede değil):
	// istemci tarafında tek merkezî yardımcı (lib/metricSource.ts) hem
	// GET hem POST uçlarına aynı işareti basıyor, ve sayfanın URL'si
	// zaten kaynak-of-truth.
	metricSrc, srcErr := s.metricSourceFor(r)
	if srcErr != nil {
		writeErr(w, srcErr)
		return
	}
	var body struct {
		From     int64       `json:"from"`
		To       int64       `json:"to"`
		Requests []bundleReq `json:"requests"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
		return
	}
	if len(body.Requests) == 0 {
		writeJSON(w, map[string]any{})
		return
	}
	if len(body.Requests) > 50 {
		http.Error(w, "too many requests (max 50 per bundle)", http.StatusBadRequest)
		return
	}
	from := time.Unix(0, body.From)
	to := time.Unix(0, body.To)
	bypass := r.URL.Query().Get("refresh") == "1"

	// metricSrc (v0.9.1150) handler'ın en başında bir kez çözülüyor —
	// gerekçesi ve v0.9.1151'in istek-başı override'ı orada. Damga,
	// tekil /api/metrics/query anahtarının (metric-query:v4) kaynak
	// bileşenleriyle aynı: backend adı, ad-kuralı damgası (VM'de adın
	// çözüldüğü seri değişebilir, istek bayt-aynı kalır), dışlama özeti.
	metricTag := metricSrc.Name() + "|" + s.metricNameRuleTag(metricSrc) + "|" + s.store.MetricExclusions().Digest()

	out := make(map[string]json.RawMessage, len(body.Requests))
	var mu sync.Mutex
	var wg sync.WaitGroup
	served := true // her slot cache'ten geldi mi (X-Cache özeti)

	for _, req := range body.Requests {
		req := req
		wg.Add(1)
		go func() {
			defer wg.Done()
			tag := ""
			if req.Type == "metric" {
				tag = metricTag
			}
			key := dashPanelKey(tag, req, from, to)
			raw, tier, err := s.cachedJSON(r.Context(), key, dashPanelTTL, bypass, func(ctx context.Context) (any, error) {
				return s.bundleSlot(ctx, metricSrc, req, from, to)
			})
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				// Hata slot'u cache DIŞI (dosya başlığı): gövdede yerini
				// alır, bir sonraki tick yeniden dener.
				b, _ := json.Marshal(bundleSlot{Series: []chstore.SpanMetricSeries{}, Error: err.Error()})
				out[req.ID] = b
				served = false
				return
			}
			if tier == "MISS" || tier == "BYPASS" {
				served = false
			}
			out[req.ID] = raw
		}()
	}
	wg.Wait()
	// Slot gövdeleri cachedJSON'da zaten NaN/Inf'ten temizlenip
	// marshal'landı; writeJSON'un sanitizeFloats'unu ham baytlar üzerinde
	// yeniden koşturmamak için doğrudan yazılıyor.
	w.Header().Set("Content-Type", "application/json")
	switch {
	case bypass:
		w.Header().Set("X-Cache", "BYPASS")
	case served:
		w.Header().Set("X-Cache", "HIT") // her slot bir cache katmanından (taze ya da SWR-bayat)
	default:
		w.Header().Set("X-Cache", "MISS")
	}
	json.NewEncoder(w).Encode(out)
}

// bundleSlot — tek panelin verisini hesaplar (cache miss yolu). metric →
// queryMetricNoted (tekil /api/metrics/query ile AYNI seam; ayrışma bu
// dalın bilinen bug sınıfı, v0.9.566), spanMetric → QuerySpanMetric (tam
// seri; dosya başlığı). Kaynak-tarama testi (dashboards_data_test.go)
// çağrı adlarını pinler.
func (s *Server) bundleSlot(ctx context.Context, metricSrc metricSource, req bundleReq, from, to time.Time) (*bundleSlot, error) {
	slot := &bundleSlot{}
	switch req.Type {
	case "metric":
		// v0.10.118 — derlenemeyen filtre bu panelde HATA olur (slot
		// Error), sessiz "filtresiz seri" değil — parseFilters sözleşmesi.
		mfilters, ferr := parseFilters(string(req.Filters))
		if ferr != nil {
			return nil, ferr
		}
		// v0.9.566 — FİLTRELER SQL'e iner (bu dal bir kez atlamıştı:
		// jvm.memory.type="heap" filtresi uygulanmayınca panel heap +
		// non-heap toplamını "heap" diye çiziyordu — yanlış ama makul
		// görünen sayı, boş panelden tehlikelidir).
		series, note, err := queryMetricNoted(ctx, metricSrc, chstore.MetricQueryFilter{
			Name:        req.Name,
			Service:     req.Service,
			Aggregation: req.Agg,
			GroupBy:     req.GroupBy,
			Filters:     mfilters,
			From:        from,
			To:          to,
			StepSeconds: req.Step,
		})
		if err != nil {
			return nil, err
		}
		slot.Series = series
		slot.RowsCapped = chstore.SeriesRowsCapped(series)
		slot.Note = note
	case "spanMetric":
		filters, ferr := parseFiltersAndDSL(string(req.Filters), req.DSL)
		if ferr != nil {
			return nil, ferr
		}
		// TAM seri — top-N bilinçli olarak YOK (dosya başlığı: "others"
		// katlaması kuyruğu tam seriden toplar).
		series, err := s.store.QuerySpanMetric(ctx, chstore.SpanMetricFilter{
			Filters:     filters,
			Aggregation: req.Agg,
			Field:       req.Field,
			GroupBy:     req.GroupBy,
			From:        from,
			To:          to,
			StepSeconds: req.Step,
		})
		if err != nil {
			return nil, err
		}
		slot.Series = series
		slot.RowsCapped = chstore.SeriesRowsCapped(series)
	default:
		return nil, fmt.Errorf("unknown panel type %q", req.Type)
	}
	if slot.Series == nil {
		slot.Series = []chstore.SpanMetricSeries{}
	}
	return slot, nil
}
