package api

// messaging_metric.go — v0.10.550 (docs/audit/messaging-kafka-metrics-2026-09-08.md,
// Faz 1: Messaging hibrit — client sağlığı VictoriaMetrics'ten).
//
// api.go BÜYÜMEYECEK kuralı: rotalar burada, kayıt route_registry defteriyle
// (init → registerRoutesExtra), api.go'ya satır girmez.
//
//   GET /api/messaging/clients?system=&cluster=&destination=&set=&from=&to=&env=&maxDataPoints=
//   GET /api/services/{name}/kafka-clients?from=&to=&env=&maxDataPoints=
//
// v0.10.575 — ?set= SORU SETİ. Topic detay sayfası üç ayrı yerde metrik
// gösteriyor; hepsini bir kerede çekmek 12 VM range sorgusu demek. Set, VM
// maliyetini istenen bloğa daraltır (ES/VM disiplini: açılışta yalnız üst
// grafik, ağır bloklar sekme seçilince):
//
//   set=topic   (VARSAYILAN, bugünkü davranış) — KafkaTopicQuestions, 5 soru,
//               kapsam TOPIC: her sorguda topic süzgeci var.
//   set=chart   — KafkaTopicChartQuestions, 2 soru (üst grafik), kapsam TOPIC.
//   set=clients — KafkaClientHealthQuestions, 9 soru, kapsam SERVİSLER: bu
//               metrikler `topic` label'ı TAŞIMAZ, topic'e göre süzülemez.
//               Sorgular Topic BOŞ gider, yanıt scope="services" der ve Note
//               bunu yazar — sessiz daraltma / yanlış okuma yasak.
//
// Bilinmeyen set 400'dür (varsayılana sessizce düşmek yanlış paneli çizer).
// Set cache anahtarına GİRER — girmezse iki set aynı gövdeyi alır (v0.5.187).
//
// Kaynak seam'dir (metricSourceFor: VM yapılandırılmışsa VM, değilse CH
// metric_points, ?metricsrc= deneme modu geçerli). Sorular sabit
// (vmetrics.Kafka*Questions, set'e göre seçilir), her soru bağımsız blok:
// biri hata verse diğerleri gelir. Kapsam topic ucunda SPAN tarafından gelir
// (messaging_caller_summary_5m → üretici/tüketici servisleri): üretici soruları
// üretici servislerle, tüketici soruları tüketicilerle daraltılır; rolü
// bilinmeyen servis iki kapsama da girer (üst küme; sessiz daraltma yasak).
//
// Graceful degrade (audit §3.4): hiç seri yoksa available=false + not; FE bölümü
// gizler, sayfa span türevli görünümde kalır. Env VM'de ifade edilemezse
// envAmbiguous (metricsource.go:617). Rol kapısı YOK — salt-okunur, viewer görür;
// serveCached 30 s, anahtar tüm girdileri taşır (messagingClientsKey, test pinli).

import (
	"context"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/cilcenk/coremetry/internal/chstore"
	"github.com/cilcenk/coremetry/internal/vmetrics"
)

func init() { registerRoutesExtra("messaging-metric", (*Server).registerMessagingMetricRoutes) }

func (s *Server) registerMessagingMetricRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/messaging/clients", s.getMessagingClients)
	mux.HandleFunc("GET /api/services/{name}/kafka-clients", s.getServiceKafkaClients)
}

const (
	kafkaClientsTTL         = 30 * time.Second
	kafkaClientsParallelism = 4
	kafkaClientsMdpMin      = 10
	kafkaClientsMdpMax      = 300
)

type messagingClientsPlan struct {
	System, Cluster, Destination, Env string
	Set                               string // topic (varsayılan) | chart | clients — v0.10.575
	From, To                          time.Time
	Mdp                               int
}

type serviceKafkaClientsPlan struct {
	Service, Env string
	From, To     time.Time
	Mdp          int
}

// Soru setleri — v0.10.575. Değerler URL sözleşmesidir, FE bunları yazar.
const (
	msgSetTopic   = "topic"
	msgSetChart   = "chart"
	msgSetClients = "clients"
)

// parseMessagingSet — ?set= ayrıştırıcı. Boş = topic (geriye dönük davranış).
// Bilinmeyen değer sessizce varsayılana DÜŞMEZ: yanlış paneli doğru sanmaktansa
// 400 dönmek dürüst.
func parseMessagingSet(raw string) (string, bool) {
	switch strings.TrimSpace(raw) {
	case "", msgSetTopic:
		return msgSetTopic, true
	case msgSetChart:
		return msgSetChart, true
	case msgSetClients:
		return msgSetClients, true
	}
	return "", false
}

// messagingSetQuestions — set → soru listesi. TEK yer; build ve test aynı gövde.
func messagingSetQuestions(set string) []vmetrics.KafkaQuestion {
	switch set {
	case msgSetChart:
		return vmetrics.KafkaTopicChartQuestions()
	case msgSetClients:
		return vmetrics.KafkaClientHealthQuestions()
	default:
		return vmetrics.KafkaTopicQuestions()
	}
}

// messagingSetScope — yanıttaki scope alanı. "services" = bloklar topic'e göre
// SÜZÜLMEDİ (metriklerde topic label'ı yok); "topic" = topic süzgeci uygulandı.
func messagingSetScope(set string) string {
	if set == msgSetClients {
		return "services"
	}
	return "topic"
}

// msgClientsScopeCaveat — scope="services" bloklarının okuma uyarısı. Not
// alanında da yazar: sayı bu topic'e dokunan servislerin İSTEMCİ metriğidir,
// aynı servisin diğer topic'leri de içindedir.
const msgClientsScopeCaveat = " Bu bloklar topic'e göre SÜZÜLEMEZ (bağlantı/gecikme/rebalance metrikleri `topic` label'ı taşımaz): kapsam bu topic'e dokunan servislerin istemcileridir, aynı istemcinin diğer topic trafiği de sayıya girer."

func messagingClientsKey(p messagingClientsPlan, srcName, mx string) string {
	return fmt.Sprintf("msg-clients:v1:src=%s:sys=%s:clu=%s:dest=%s:set=%s:%s:mdp%d:env=%s:mx=%s",
		srcName, p.System, p.Cluster, p.Destination, p.Set, cacheBucket(p.From, p.To), p.Mdp, p.Env, mx)
}

func serviceKafkaClientsKey(p serviceKafkaClientsPlan, srcName, mx string) string {
	return fmt.Sprintf("svc-kafka-clients:v1:src=%s:svc=%s:%s:mdp%d:env=%s:mx=%s",
		srcName, p.Service, cacheBucket(p.From, p.To), p.Mdp, p.Env, mx)
}

// kafkaMetricBlock — bir sorunun cevabı. Error dolu = o soru gelmedi, diğerleri
// geçerli (kısmi cevap ilan edilir, düşürülmez).
type kafkaMetricBlock struct {
	Metric  string                     `json:"metric"`
	Label   string                     `json:"label"`
	Unit    string                     `json:"unit"`
	Kind    string                     `json:"kind"`
	Agg     string                     `json:"agg"`
	GroupBy []string                   `json:"groupBy"`
	Series  []chstore.SpanMetricSeries `json:"series"`
	Error   string                     `json:"error,omitempty"`
}

type messagingClientsResponse struct {
	System       string                      `json:"system"`
	Cluster      string                      `json:"cluster"`
	Destination  string                      `json:"destination"`
	Scope        string                      `json:"scope"` // topic | services — v0.10.575
	Source       string                      `json:"source"`
	Available    bool                        `json:"available"`
	EnvAmbiguous bool                        `json:"envAmbiguous,omitempty"`
	Note         string                      `json:"note"`
	Producers    []string                    `json:"producers"`
	Consumers    []string                    `json:"consumers"`
	Blocks       map[string]kafkaMetricBlock `json:"blocks"`
}

type serviceKafkaClientsResponse struct {
	Service      string                      `json:"service"`
	Source       string                      `json:"source"`
	Available    bool                        `json:"available"`
	EnvAmbiguous bool                        `json:"envAmbiguous,omitempty"`
	Note         string                      `json:"note"`
	Blocks       map[string]kafkaMetricBlock `json:"blocks"`
}

// splitCallerRoles — üretici / tüketici kümeleri; rolü bilinmeyen ikisine de.
func splitCallerRoles(callers []chstore.MsgCallerService) (producers, consumers []string) {
	p, c := map[string]bool{}, map[string]bool{}
	for _, r := range callers {
		svc := strings.TrimSpace(r.Service)
		if svc == "" {
			continue
		}
		switch strings.ToLower(strings.TrimSpace(r.Role)) {
		case "producer":
			p[svc] = true
		case "consumer":
			c[svc] = true
		default:
			p[svc] = true
			c[svc] = true
		}
	}
	return kafkaSortedSet(p), kafkaSortedSet(c)
}

func kafkaSortedSet(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// runKafkaQuestions — soruları seam'e sınırlı paralellikle sorar. scope nil
// dönerse soru atlanır (kapsam boş), blok bunu Error ile söyler.
func runKafkaQuestions(ctx context.Context, src metricSource, qs []vmetrics.KafkaQuestion, env string,
	scopeFor func(m vmetrics.KafkaMetric) *vmetrics.KafkaScope) (map[string]kafkaMetricBlock, bool, bool) {
	blocks := make([]kafkaMetricBlock, len(qs))
	envAmbiguous := false
	if env != "" {
		_, applied := src.EnvFilterExpr(env)
		envAmbiguous = !applied
	}
	sem := make(chan struct{}, kafkaClientsParallelism)
	var wg sync.WaitGroup
	for i, q := range qs {
		m, ok := vmetrics.KafkaMetricByName(q.Metric)
		b := kafkaMetricBlock{Metric: q.Metric, Label: q.TR, GroupBy: append([]string(nil), q.GroupBy...), Series: []chstore.SpanMetricSeries{}}
		if !ok {
			b.Error = "katalogda yok"
			blocks[i] = b
			continue
		}
		b.Unit, b.Kind, b.Agg = m.Unit, m.Kind, m.Agg
		sc := scopeFor(m)
		if sc == nil {
			b.Error = "kapsam boş: span tarafında " + m.Side + " servisi yok"
			blocks[i] = b
			continue
		}
		f, err := vmetrics.KafkaQuery(m, *sc, q.GroupBy)
		if err != nil {
			b.Error = err.Error()
			blocks[i] = b
			continue
		}
		if env != "" {
			f = withEnvFilter(f, env, src)
		}
		blocks[i] = b
		wg.Add(1)
		go func(i int, f chstore.MetricQueryFilter) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			series, err := src.QueryMetric(ctx, f)
			if err != nil {
				blocks[i].Error = err.Error()
				return
			}
			if series == nil {
				series = []chstore.SpanMetricSeries{}
			}
			blocks[i].Series = series
		}(i, f)
	}
	wg.Wait()
	out := make(map[string]kafkaMetricBlock, len(qs))
	available := false
	for i, q := range qs {
		out[q.Key] = blocks[i]
		if len(blocks[i].Series) > 0 {
			available = true
		}
	}
	return out, available, envAmbiguous
}

func kafkaClientsNote(source string, available, envAmbiguous bool, reason, scopeCaveat string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Kaynak: METRİK (%s)", source)
	switch {
	case reason != "":
		b.WriteString(" — " + reason)
	case available:
		b.WriteString(" — Kafka client metrikleri (OTel Java agent kafka-clients-metrics). Lag = bu istemcinin gördüğü partition lag'i; consumer group lag'i DEĞİL, broker tarafı ölçülmüyor.")
	default:
		b.WriteString(" — Kafka client metriği bulunamadı: servis OTel Java agent ile enstrümante değil ya da kafka-clients-metrics kapalı; sayfa span türevli görünümde.")
	}
	b.WriteString(scopeCaveat)
	if envAmbiguous {
		b.WriteString(" env filtresi bu depoda ifade edilemiyor, seriler TÜM ortamları kapsıyor.")
	}
	return b.String()
}

// buildMessagingClients — SAF (kaynak arayüz + caller listesi): topic soruları.
func buildMessagingClients(ctx context.Context, src metricSource, p messagingClientsPlan, callers []chstore.MsgCallerService) (messagingClientsResponse, error) {
	set := p.Set
	if set == "" {
		set = msgSetTopic // plan doğrudan kurulursa (test/çağrı) varsayılan
	}
	resp := messagingClientsResponse{
		System: p.System, Cluster: p.Cluster, Destination: p.Destination,
		Scope: messagingSetScope(set), Source: src.Name(),
		Producers: []string{}, Consumers: []string{}, Blocks: map[string]kafkaMetricBlock{},
	}
	// clients setinde topic süzgeci UYGULANMAZ: bu metriklerde `topic` label'ı
	// yok (KafkaQuery da hata verirdi). Kapsam servis kümesidir; caveat bunu
	// hem scope alanında hem notta ilan eder.
	topic := p.Destination
	caveat := ""
	if set == msgSetClients {
		topic, caveat = "", msgClientsScopeCaveat
	}
	resp.Producers, resp.Consumers = splitCallerRoles(callers)
	if len(resp.Producers) == 0 && len(resp.Consumers) == 0 {
		resp.Note = kafkaClientsNote(resp.Source, false, false, "span tarafında bu topic için üretici/tüketici görülmedi; metrik sorgusu atılmadı.", caveat)
		return resp, nil
	}
	scopeFor := func(m vmetrics.KafkaMetric) *vmetrics.KafkaScope {
		svcs := resp.Consumers
		if m.Side == "producer" {
			svcs = resp.Producers
		}
		if len(svcs) == 0 {
			return nil
		}
		return &vmetrics.KafkaScope{Services: svcs, Topic: topic, From: p.From, To: p.To, MaxDataPoints: p.Mdp}
	}
	blocks, available, envAmbiguous := runKafkaQuestions(ctx, src, messagingSetQuestions(set), p.Env, scopeFor)
	resp.Blocks, resp.Available, resp.EnvAmbiguous = blocks, available, envAmbiguous
	resp.Note = kafkaClientsNote(resp.Source, available, envAmbiguous, "", caveat)
	return resp, nil
}

// buildServiceKafkaClients — SAF: servis paneli soruları, kapsam tek servis.
func buildServiceKafkaClients(ctx context.Context, src metricSource, p serviceKafkaClientsPlan) (serviceKafkaClientsResponse, error) {
	resp := serviceKafkaClientsResponse{Service: p.Service, Source: src.Name(), Blocks: map[string]kafkaMetricBlock{}}
	scopeFor := func(vmetrics.KafkaMetric) *vmetrics.KafkaScope {
		return &vmetrics.KafkaScope{Services: []string{p.Service}, From: p.From, To: p.To, MaxDataPoints: p.Mdp}
	}
	blocks, available, envAmbiguous := runKafkaQuestions(ctx, src, vmetrics.KafkaServiceQuestions(), p.Env, scopeFor)
	resp.Blocks, resp.Available, resp.EnvAmbiguous = blocks, available, envAmbiguous
	resp.Note = kafkaClientsNote(resp.Source, available, envAmbiguous, "", "")
	return resp, nil
}

func kafkaClientsMdp(raw string) int {
	mdp := parseInt(raw, vmetrics.KafkaDefaultMaxDataPoints)
	if mdp < kafkaClientsMdpMin || mdp > kafkaClientsMdpMax {
		return vmetrics.KafkaDefaultMaxDataPoints
	}
	return mdp
}

// messagingClientsPlanFrom — sorgu dizesi → plan. SAF (yalnız URL okur), 400
// gerekçesini hata olarak döner: handler tek satırla çağırır, ayrıştırma testi
// gerçek istekle koşar (kablolama parametreye kaçmasın).
func messagingClientsPlanFrom(r *http.Request) (messagingClientsPlan, error) {
	q := r.URL.Query()
	system := strings.TrimSpace(q.Get("system"))
	dest := strings.TrimSpace(q.Get("destination"))
	if system == "" || dest == "" {
		return messagingClientsPlan{}, fmt.Errorf("system ve destination parametreleri zorunlu")
	}
	set, ok := parseMessagingSet(q.Get("set"))
	if !ok {
		return messagingClientsPlan{}, fmt.Errorf("set parametresi geçersiz: %s | %s | %s", msgSetTopic, msgSetChart, msgSetClients)
	}
	cluster := strings.TrimSpace(q.Get("cluster"))
	if cluster == "" {
		cluster = "(default)"
	}
	from, to := parseFromTo(r, time.Hour)
	return messagingClientsPlan{
		System: system, Cluster: cluster, Destination: dest, Env: strings.TrimSpace(q.Get("env")), Set: set,
		From: from, To: to, Mdp: kafkaClientsMdp(q.Get("maxDataPoints")),
	}, nil
}

// getMessagingClients — GET /api/messaging/clients
func (s *Server) getMessagingClients(w http.ResponseWriter, r *http.Request) {
	p, err := messagingClientsPlanFrom(r)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	src, err := s.metricSourceFor(r)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	mx := s.store.MetricExclusions().Digest()
	s.serveCached(w, r, messagingClientsKey(p, src.Name(), mx), kafkaClientsTTL, func(ctx context.Context) (any, error) {
		callers, err := s.store.MessagingCallerServices(ctx, p.System, p.Cluster, p.Destination, p.From, p.To)
		if err != nil {
			return nil, err
		}
		return buildMessagingClients(ctx, src, p, callers)
	})
}

// getServiceKafkaClients — GET /api/services/{name}/kafka-clients
func (s *Server) getServiceKafkaClients(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimSpace(r.PathValue("name"))
	if name == "" {
		writeJSONError(w, http.StatusBadRequest, "service name required")
		return
	}
	q := r.URL.Query()
	from, to := parseFromTo(r, time.Hour)
	p := serviceKafkaClientsPlan{Service: name, Env: strings.TrimSpace(q.Get("env")), From: from, To: to, Mdp: kafkaClientsMdp(q.Get("maxDataPoints"))}
	src, err := s.metricSourceFor(r)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	mx := s.store.MetricExclusions().Digest()
	s.serveCached(w, r, serviceKafkaClientsKey(p, src.Name(), mx), kafkaClientsTTL, func(ctx context.Context) (any, error) {
		return buildServiceKafkaClients(ctx, src, p)
	})
}
