// Package thanos implements a read-only client for the Thanos
// Querier endpoints of external OpenShift clusters (v0.8.575,
// audit: docs/audit/thanos-multicluster-metrics-audit.md). It
// powers the /clusters surface: per-(namespace, pod) CPU + memory
// pulled straight from each cluster's platform monitoring stack —
// telemetry the applications themselves never emit.
//
// Structure mirrors internal/tempo/client.go (the canonical
// external-query-service template): typed Settings blob in
// system_settings under "thanos_clusters", narrow settingsStore
// interface, LoadPersisted at boot + StartConfigRefresh 30s poll
// for multi-pod sync, SavePersisted + live Configure swap, and a
// masked Snapshot for the settings UI. The one structural
// difference: Settings holds a LIST of clusters, and TLS-verify
// varies per cluster — so instead of tempo's rebuild-on-toggle
// single client, this package uses the Zoom two-singleton pattern
// (notify.go:1106): one verifying client + one lazily-built
// insecure twin, picked per request via thanosClientFor.
package thanos

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"
)

// ClusterConfig is one remote cluster entry. Name doubles as the
// APM join key: it must equal the cluster value spans carry
// (k8s.cluster.name / openshift.cluster.name — clusterDeriveExpr)
// for the service→cluster pivot to light up; the Settings UI
// suggests observed names for exactly that reason.
type ClusterConfig struct {
	Name string `json:"name"`
	URL  string `json:"url"`
	// AuthType — none | bearer. Bearer covers the standard
	// OpenShift path: a ServiceAccount token with the
	// cluster-monitoring-view ClusterRole against the
	// oauth-proxy'd thanos-querier route.
	AuthType string `json:"authType,omitempty"`
	// Token — never echoed; Snapshot exposes HasToken only.
	Token string `json:"token,omitempty"`
	// NamespaceFilter is a PromQL regex injected as
	// namespace=~"..." into every query — the cardinality shield
	// that keeps a 10k-pod estate from riding home in one
	// response. Empty = all namespaces (topk still caps rows).
	NamespaceFilter    string `json:"namespaceFilter,omitempty"`
	InsecureSkipVerify bool   `json:"insecureSkipVerify,omitempty"`
	Enabled            bool   `json:"enabled"`
}

// Settings is the persisted blob: the whole cluster list, written
// atomically (custom_roles convention — no per-row races at the
// realistic N≤20 scale).
type Settings struct {
	Clusters []ClusterConfig `json:"clusters"`
}

// ClusterSnapshot mirrors ClusterConfig with the token masked.
type ClusterSnapshot struct {
	Name               string `json:"name"`
	URL                string `json:"url"`
	AuthType           string `json:"authType,omitempty"`
	HasToken           bool   `json:"hasToken"`
	NamespaceFilter    string `json:"namespaceFilter,omitempty"`
	InsecureSkipVerify bool   `json:"insecureSkipVerify,omitempty"`
	Enabled            bool   `json:"enabled"`
}

// Snapshot is what GET /api/settings/thanos returns.
type Snapshot struct {
	Clusters []ClusterSnapshot `json:"clusters"`
}

// PodRow is one (cluster, namespace, pod) sample from the merged
// instant queries. CPU is CORES (rate of cpu-seconds), not the
// 0-1 utilization ratio HostRow carries — deliberately a separate
// shape (audit §7). Pct fields are 0 when the cluster doesn't
// expose kube-state-metrics limits — same "0 = unknown" contract
// HostRow.MemPct already established.
type PodRow struct {
	Cluster   string  `json:"cluster"`
	Namespace string  `json:"namespace"`
	Pod       string  `json:"pod"`
	CPUCores  float64 `json:"cpuCores"`
	MemBytes  float64 `json:"memBytes"`
	CPUPct    float64 `json:"cpuPct,omitempty"`
	MemPct    float64 `json:"memPct,omitempty"`
	// Request-based percentages (v0.8.580) — provisioning accuracy
	// axis, alongside the limit-based throttle/OOM axis above. Can
	// legitimately exceed 100 (pod using more than it requested) —
	// deliberately NOT clamped like the limit pcts; the overshoot
	// IS the signal. 0 = requests not exposed (best-effort).
	CPUPctOfReq float64 `json:"cpuPctOfReq,omitempty"`
	MemPctOfReq float64 `json:"memPctOfReq,omitempty"`
	// Ham limit/request değerleri (v0.9.3, trend-upgrade audit §1
	// düzeltmesi): threshold referans ÇİZGİLERİ mutlak değer ister
	// (cores/bytes ekseninde) — yüzdeler yetmez. acc'ta zaten
	// vardı, satıra indirildi; 0 = bilinmiyor.
	CPULimitCores   float64 `json:"cpuLimitCores,omitempty"`
	MemLimitBytes   float64 `json:"memLimitBytes,omitempty"`
	CPURequestCores float64 `json:"cpuRequestCores,omitempty"`
	MemRequestBytes float64 `json:"memRequestBytes,omitempty"`
	// v0.9.9 — pod network hızı (cAdvisor, best-effort).
	NetInBps  float64 `json:"netInBps,omitempty"`
	NetOutBps float64 `json:"netOutBps,omitempty"`
	// Service (v0.9.11) — Coremetry servis eşleşmesi (host_name =
	// pod adı köprüsü). API katmanı doldurur (chstore.PodServiceMap
	// + pickPodService); thanos paketi bu alana yazmaz. Boş =
	// eşleşme yok (instrument edilmemiş / infra pod'u / belirsiz).
	Service string `json:"service,omitempty"`
}

// PodSeriesTrend — multi-pod görünümün seri birimi (v0.9.3): bir
// pod'un dakika-bucket trendi.
type PodSeriesTrend struct {
	Pod   string       `json:"pod"`
	Trend []TrendPoint `json:"trend"`
}

// TrendPoint matches HostTrendPoint's bucket contract: unix
// SECONDS on minute boundaries, so the frontend drawer reuses the
// same rendering path.
type TrendPoint struct {
	Bucket   int64   `json:"bucket"`
	CPUCores float64 `json:"cpuCores"`
	MemBytes float64 `json:"memBytes"`
}

// Service holds the live cluster list. Concurrency contract is
// tempo's: RWMutex around the config, background refresh poll
// keeping multi-pod deployments in sync via the shared blob.
type Service struct {
	mu  sync.RWMutex
	cfg Settings
}

func New() *Service { return &Service{} }

// settingsStore — narrow chstore surface (tempo precedent; avoids
// an import cycle if chstore ever depends back on thanos).
type settingsStore interface {
	GetThanosSettingsRaw(ctx context.Context) ([]byte, error)
	PutThanosSettingsRaw(ctx context.Context, raw []byte) error
}

// LoadPersisted hydrates from system_settings. Missing blob =
// empty list (HasEnabledClusters reports false; handlers 404).
func (s *Service) LoadPersisted(ctx context.Context, store settingsStore) error {
	if s == nil || store == nil {
		return nil
	}
	raw, err := store.GetThanosSettingsRaw(ctx)
	if err != nil {
		return err
	}
	if len(raw) == 0 {
		return nil
	}
	var cfg Settings
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return fmt.Errorf("thanos decode: %w", err)
	}
	s.Configure(cfg)
	return nil
}

// StartConfigRefresh — multi-pod blob sync, 30s default (tempo
// v0.5.324 precedent). Run as a goroutine from main().
func (s *Service) StartConfigRefresh(ctx context.Context, store settingsStore, interval time.Duration) {
	if s == nil || store == nil {
		return
	}
	if interval <= 0 {
		interval = 30 * time.Second
	}
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if err := s.LoadPersisted(ctx, store); err != nil {
				log.Printf("[thanos] config refresh: %v", err)
			}
		}
	}
}

// SavePersisted writes the merged config (handler does the
// empty-token-preserves-stored merge first) and swaps it live.
func (s *Service) SavePersisted(ctx context.Context, store settingsStore, cfg Settings) error {
	if s == nil || store == nil {
		return nil
	}
	raw, err := json.Marshal(cfg)
	if err != nil {
		return err
	}
	if err := store.PutThanosSettingsRaw(ctx, raw); err != nil {
		return err
	}
	s.Configure(cfg)
	return nil
}

// Configure swaps the live cluster list.
func (s *Service) Configure(cfg Settings) {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.cfg = cfg
	s.mu.Unlock()
}

// Snapshot returns the masked config for the settings UI.
func (s *Service) Snapshot() Snapshot {
	if s == nil {
		return Snapshot{}
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := Snapshot{Clusters: make([]ClusterSnapshot, 0, len(s.cfg.Clusters))}
	for _, c := range s.cfg.Clusters {
		out.Clusters = append(out.Clusters, ClusterSnapshot{
			Name: c.Name, URL: c.URL, AuthType: c.AuthType,
			HasToken: c.Token != "", NamespaceFilter: c.NamespaceFilter,
			InsecureSkipVerify: c.InsecureSkipVerify, Enabled: c.Enabled,
		})
	}
	return out
}

// CurrentSettings — full config INCLUDING tokens; only for the
// PUT handler's stored-token merge. Never echo over the wire
// (tempo contract).
func (s *Service) CurrentSettings() Settings {
	if s == nil {
		return Settings{}
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	cp := Settings{Clusters: make([]ClusterConfig, len(s.cfg.Clusters))}
	copy(cp.Clusters, s.cfg.Clusters)
	return cp
}

// ClusterByName returns the ENABLED cluster entry for name.
func (s *Service) ClusterByName(name string) (ClusterConfig, bool) {
	if s == nil {
		return ClusterConfig{}, false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, c := range s.cfg.Clusters {
		if c.Enabled && c.Name == name {
			return c, true
		}
	}
	return ClusterConfig{}, false
}

// HasEnabledClusters gates the /clusters surface: false → the
// page shows its Empty state without any HTTP attempts.
func (s *Service) HasEnabledClusters() bool {
	if s == nil {
		return false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, c := range s.cfg.Clusters {
		if c.Enabled {
			return true
		}
	}
	return false
}

// ── HTTP transport — Zoom two-singleton pattern ─────────────────

// thanosHTTPClient is the default verifying client. 15s hard
// timeout (Zoom precedent): a wedged Querier must never pin a
// serveCached singleflight slot indefinitely — handlers also pass
// a tighter per-query ctx deadline on top.
var thanosHTTPClient = &http.Client{Timeout: 15 * time.Second}

var (
	thanosInsecureOnce   sync.Once
	thanosInsecureClient *http.Client
)

// thanosClientFor picks the verifying or the lazily-built
// insecure client. Per-cluster TLS variance is a single bool, so
// two shared clients cover every cluster (Zoom proof); building a
// client per cluster would just fragment connection pools.
func thanosClientFor(skipVerify bool) *http.Client {
	if !skipVerify {
		return thanosHTTPClient
	}
	thanosInsecureOnce.Do(func() {
		thanosInsecureClient = &http.Client{
			Timeout: 15 * time.Second,
			Transport: &http.Transport{
				TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
			},
		}
	})
	return thanosInsecureClient
}

// ── Prometheus query API ────────────────────────────────────────

// promEnvelope is the /api/v1/query(_range) response shape. On
// status!="success" Prometheus fills errorType/error instead of
// data.
type promEnvelope struct {
	Status    string `json:"status"`
	ErrorType string `json:"errorType"`
	Error     string `json:"error"`
	Data      struct {
		ResultType string       `json:"resultType"`
		Result     []promSeries `json:"result"`
	} `json:"data"`
}

// promSeries — instant vectors fill Value ([ts, "v"]), range
// matrices fill Values. Sample values arrive as STRINGS per the
// Prometheus JSON contract.
type promSeries struct {
	Metric map[string]string   `json:"metric"`
	Value  []json.RawMessage   `json:"value"`
	Values [][]json.RawMessage `json:"values"`
}

// maxSeriesParsed is the defensive backstop behind the query-side
// topk cap: even a misbehaving Querier can't hand us more rows
// than this.
const maxSeriesParsed = 1000

func (s *Service) doQuery(ctx context.Context, c ClusterConfig, path string, params url.Values) ([]promSeries, error) {
	u := strings.TrimRight(c.URL, "/") + path + "?" + params.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	if c.AuthType == "bearer" && c.Token != "" {
		req.Header.Set("Authorization", "Bearer "+c.Token)
	}
	resp, err := thanosClientFor(c.InsecureSkipVerify).Do(req)
	if err != nil {
		return nil, fmt.Errorf("thanos %s: %w", c.Name, err)
	}
	defer resp.Body.Close()
	// 8MB cap — a topk(500) vector is a few hundred KB; anything
	// bigger means the cardinality shield failed upstream.
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if resp.StatusCode >= 300 {
		return nil, fmt.Errorf("thanos %s: HTTP %d: %s", c.Name, resp.StatusCode,
			strings.TrimSpace(firstN(string(body), 200)))
	}
	var env promEnvelope
	if err := json.Unmarshal(body, &env); err != nil {
		return nil, fmt.Errorf("thanos %s decode: %w", c.Name, err)
	}
	if env.Status != "success" {
		return nil, fmt.Errorf("thanos %s: %s: %s", c.Name, env.ErrorType, env.Error)
	}
	if len(env.Data.Result) > maxSeriesParsed {
		env.Data.Result = env.Data.Result[:maxSeriesParsed]
	}
	return env.Data.Result, nil
}

func firstN(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// PodMetrics runs the per-cluster instant queries (CPU rate,
// working-set memory, cpu/mem limits) and merges them by
// (namespace, pod). Exactly four queries per cluster regardless
// of pod count — never a query per pod (audit §4).
func (s *Service) PodMetrics(ctx context.Context, c ClusterConfig) ([]PodRow, error) {
	type acc struct{ cpu, mem, cpuLim, memLim, cpuReq, memReq, netIn, netOut float64 }
	byKey := map[string]*acc{}
	get := func(m map[string]string) *acc {
		k := m["namespace"] + "\x00" + m["pod"]
		a := byKey[k]
		if a == nil {
			a = &acc{}
			byKey[k] = a
		}
		return a
	}

	// CPU + memory are mandatory; limits are best-effort (clusters
	// without kube-state-metrics simply leave Pct at 0 — the
	// HostRow.MemPct contract).
	cpuSeries, err := s.doQuery(ctx, c, "/api/v1/query",
		url.Values{"query": {podCPUQuery(c.NamespaceFilter)}})
	if err != nil {
		return nil, err
	}
	memSeries, err := s.doQuery(ctx, c, "/api/v1/query",
		url.Values{"query": {podMemQuery(c.NamespaceFilter)}})
	if err != nil {
		return nil, err
	}
	for _, ser := range cpuSeries {
		if v, ok := sampleValue(ser.Value); ok {
			get(ser.Metric).cpu = v
		}
	}
	for _, ser := range memSeries {
		if v, ok := sampleValue(ser.Value); ok {
			get(ser.Metric).mem = v
		}
	}
	for _, lim := range []struct {
		query string
		set   func(*acc, float64)
	}{
		{podLimitQuery("cpu", c.NamespaceFilter), func(a *acc, v float64) { a.cpuLim = v }},
		{podLimitQuery("memory", c.NamespaceFilter), func(a *acc, v float64) { a.memLim = v }},
		// v0.8.580 — request axis, same best-effort contract:
		// cluster başına sabit 6 sorgu, hâlâ pod sayısından bağımsız.
		{podRequestQuery("cpu", c.NamespaceFilter), func(a *acc, v float64) { a.cpuReq = v }},
		{podRequestQuery("memory", c.NamespaceFilter), func(a *acc, v float64) { a.memReq = v }},
		// v0.9.9 — network (best-effort; cluster başına sabit 8 sorgu oldu).
		{podNetQuery("receive", c.NamespaceFilter), func(a *acc, v float64) { a.netIn = v }},
		{podNetQuery("transmit", c.NamespaceFilter), func(a *acc, v float64) { a.netOut = v }},
	} {
		series, err := s.doQuery(ctx, c, "/api/v1/query", url.Values{"query": {lim.query}})
		if err != nil {
			continue // best-effort — limits absent on many stacks
		}
		for _, ser := range series {
			if v, ok := sampleValue(ser.Value); ok {
				lim.set(get(ser.Metric), v)
			}
		}
	}

	out := make([]PodRow, 0, len(byKey))
	for k, a := range byKey {
		ns, pod, _ := strings.Cut(k, "\x00")
		// Limit-only keys (limit configured, pod idle enough that
		// cpu/mem series absent) are noise — skip.
		if a.cpu == 0 && a.mem == 0 {
			continue
		}
		row := PodRow{Cluster: c.Name, Namespace: ns, Pod: pod,
			CPUCores: a.cpu, MemBytes: a.mem}
		if a.cpuLim > 0 {
			row.CPUPct = clampPct(a.cpu / a.cpuLim * 100)
		}
		if a.memLim > 0 {
			row.MemPct = clampPct(a.mem / a.memLim * 100)
		}
		// Request oranı bilerek clamp'siz: >100 aşımın kendisi sinyal.
		if a.cpuReq > 0 {
			row.CPUPctOfReq = a.cpu / a.cpuReq * 100
		}
		if a.memReq > 0 {
			row.MemPctOfReq = a.mem / a.memReq * 100
		}
		// v0.9.3 — ham değerler threshold çizgileri için satıra iner.
		row.CPULimitCores, row.MemLimitBytes = a.cpuLim, a.memLim
		row.CPURequestCores, row.MemRequestBytes = a.cpuReq, a.memReq
		row.NetInBps, row.NetOutBps = a.netIn, a.netOut
		out = append(out, row)
	}
	return out, nil
}

// ClusterSummary — genel görünüm kartının verisi (v0.8.586,
// redesign audit §3.1). Her alan kendi sorgusundan BEST-EFFORT
// dolar: token tenancy-port'a bağlıysa node ailesi boş kalır ama
// pod sayısı yine gelir (kısmi kart > hata kartı). Dört sorgunun
// DÖRDÜ de başarısızsa cluster erişilemez sayılır ve hata döner.
type ClusterSummary struct {
	Cluster      string  `json:"cluster"`
	Nodes        int     `json:"nodes,omitempty"`
	Pods         int     `json:"pods,omitempty"`
	CPUUsedCores float64 `json:"cpuUsedCores,omitempty"`
	MemUsedBytes float64 `json:"memUsedBytes,omitempty"`
	// v0.9.9 — cluster toplam network hızı (node-exporter, lo hariç).
	// Best-effort: seri yoksa 0 kalır, UI kartı hiç render etmez.
	NetInBps  float64 `json:"netInBps,omitempty"`
	NetOutBps float64 `json:"netOutBps,omitempty"`
}

// Summary — kart başına sabit 4 skaler sorgu (topk'li vektör yok;
// pod sayısı topk kesmesiz TAM).
func (s *Service) Summary(ctx context.Context, c ClusterConfig) (ClusterSummary, error) {
	out := ClusterSummary{Cluster: c.Name}
	okCount := 0
	var lastErr error
	scalar := func(q string) (float64, bool) {
		series, err := s.doQuery(ctx, c, "/api/v1/query", url.Values{"query": {q}})
		if err != nil {
			lastErr = err
			return 0, false
		}
		okCount++
		if len(series) == 0 {
			return 0, true // sorgu çalıştı, seri yok (0 kabul)
		}
		v, ok := sampleValue(series[0].Value)
		return v, ok
	}
	if v, ok := scalar(summaryNodeCountQuery); ok {
		out.Nodes = int(v)
	}
	if v, ok := scalar(summaryPodCountQuery(c.NamespaceFilter)); ok {
		out.Pods = int(v)
	}
	if v, ok := scalar(summaryCPUUsedQuery); ok {
		out.CPUUsedCores = v
	}
	if v, ok := scalar(summaryMemUsedQuery); ok {
		out.MemUsedBytes = v
	}
	if v, ok := scalar(summaryNetQuery("receive")); ok {
		out.NetInBps = v
	}
	if v, ok := scalar(summaryNetQuery("transmit")); ok {
		out.NetOutBps = v
	}
	if okCount == 0 && lastErr != nil {
		return ClusterSummary{}, lastErr
	}
	return out, nil
}

// NamespaceRow — bir namespace'in rollup satırı (v0.8.588, redesign
// audit §3.3). Ayrı sorgudan gelir — pod listesinin topk kesmesinden
// ETKİLENMEZ (toplamlar tam).
type NamespaceRow struct {
	Cluster   string  `json:"cluster"`
	Namespace string  `json:"namespace"`
	Pods      int     `json:"pods,omitempty"`
	CPUCores  float64 `json:"cpuCores"`
	MemBytes  float64 `json:"memBytes"`
}

// NamespaceMetrics — cpu+mem zorunlu (2 sorgu), pod sayısı
// best-effort (1 sorgu; aynı metrik ailesi, pratikte hep döner).
func (s *Service) NamespaceMetrics(ctx context.Context, c ClusterConfig) ([]NamespaceRow, error) {
	type acc struct {
		cpu, mem float64
		pods     float64
	}
	byNS := map[string]*acc{}
	get := func(m map[string]string) *acc {
		k := m["namespace"]
		a := byNS[k]
		if a == nil {
			a = &acc{}
			byNS[k] = a
		}
		return a
	}
	for _, q := range []struct {
		query string
		set   func(*acc, float64)
	}{
		{nsCPUQuery(c.NamespaceFilter), func(a *acc, v float64) { a.cpu = v }},
		{nsMemQuery(c.NamespaceFilter), func(a *acc, v float64) { a.mem = v }},
	} {
		series, err := s.doQuery(ctx, c, "/api/v1/query", url.Values{"query": {q.query}})
		if err != nil {
			return nil, err
		}
		for _, ser := range series {
			if v, ok := sampleValue(ser.Value); ok {
				q.set(get(ser.Metric), v)
			}
		}
	}
	if series, err := s.doQuery(ctx, c, "/api/v1/query",
		url.Values{"query": {nsPodCountQuery(c.NamespaceFilter)}}); err == nil {
		for _, ser := range series {
			if v, ok := sampleValue(ser.Value); ok {
				get(ser.Metric).pods = v
			}
		}
	}
	out := make([]NamespaceRow, 0, len(byNS))
	for ns, a := range byNS {
		if ns == "" || (a.cpu == 0 && a.mem == 0) {
			continue // count-only anahtarlar gürültü (kurulu eleme sözleşmesi)
		}
		out = append(out, NamespaceRow{
			Cluster: c.Name, Namespace: ns,
			Pods: int(a.pods), CPUCores: a.cpu, MemBytes: a.mem,
		})
	}
	return out, nil
}

// DeploymentRow — bir namespace içindeki iş yükü (Deployment/STS/DS)
// rollup satırı (v0.9.22). PodNames pod tablosunun ?deployment=
// süzgecini besler (istemci üyelikle süzer — ad-önek sezgiseli
// değil, gerçek eşleme).
type DeploymentRow struct {
	Cluster    string   `json:"cluster"`
	Namespace  string   `json:"namespace"`
	Deployment string   `json:"deployment"`
	Pods       int      `json:"pods"`
	CPUCores   float64  `json:"cpuCores"`
	MemBytes   float64  `json:"memBytes"`
	PodNames   []string `json:"podNames"`
}

// unassignedWorkload — eşlenemeyen pod'ların toplandığı satır adı.
const unassignedWorkload = "(unassigned)"

// DeploymentMetrics — namespace'in pod başına cpu/mem'ini (zorunlu 2
// sorgu) kube-state-metrics owner eşlemesiyle (best-effort 2 sorgu)
// iş yüküne toplar. Fallback zinciri (deployment audit uyarlaması —
// probe yerine runtime): tam join → rs-hash soyma → pod-adı sezgiseli
// → "(unassigned)".
func (s *Service) DeploymentMetrics(ctx context.Context, c ClusterConfig, namespace string) ([]DeploymentRow, error) {
	// Zorunlu: pod başına cpu/mem (mevcut multi-pod sorguları).
	params := func(q string) url.Values { return url.Values{"query": {q}} }
	cpuSeries, err := s.doQuery(ctx, c, "/api/v1/query", params(nsPodsCPUTrendQuery(namespace)))
	if err != nil {
		return nil, err
	}
	memSeries, err := s.doQuery(ctx, c, "/api/v1/query", params(nsPodsMemTrendQuery(namespace)))
	if err != nil {
		return nil, err
	}

	// Best-effort eşleme aileleri.
	podToRS := map[string]string{}
	if series, err := s.doQuery(ctx, c, "/api/v1/query", params(nsPodOwnerQuery(namespace))); err == nil {
		for _, ser := range series {
			if p, rs := ser.Metric["pod"], ser.Metric["owner_name"]; p != "" && rs != "" {
				podToRS[p] = rs
			}
		}
	}
	rsToDeploy := map[string]string{}
	if series, err := s.doQuery(ctx, c, "/api/v1/query", params(nsReplicaSetOwnerQuery(namespace))); err == nil {
		for _, ser := range series {
			if rs, d := ser.Metric["replicaset"], ser.Metric["owner_name"]; rs != "" && d != "" {
				rsToDeploy[rs] = d
			}
		}
	}

	workloadOf := func(pod string) string {
		if rs, ok := podToRS[pod]; ok {
			if d, ok2 := rsToDeploy[rs]; ok2 {
				return d // tam join
			}
			// rs bilinen ama deploy ailesi yok: rs-hash'i soy.
			if i := strings.LastIndex(rs, "-"); i > 0 && isReplicaSetHash(rs[i+1:]) {
				return rs[:i]
			}
			return rs
		}
		if w := stripPodSuffixes(pod); w != pod || !strings.Contains(pod, "-") {
			return w
		}
		return unassignedWorkload
	}

	type acc struct {
		cpu, mem float64
		pods     []string
	}
	byWorkload := map[string]*acc{}
	touch := func(pod string) *acc {
		w := workloadOf(pod)
		a := byWorkload[w]
		if a == nil {
			a = &acc{}
			byWorkload[w] = a
		}
		return a
	}
	seenPod := map[string]bool{}
	for _, ser := range cpuSeries {
		pod := ser.Metric["pod"]
		if pod == "" {
			continue
		}
		if v, ok := sampleValue(ser.Value); ok {
			a := touch(pod)
			a.cpu += v
			if !seenPod[pod] {
				a.pods = append(a.pods, pod)
				seenPod[pod] = true
			}
		}
	}
	for _, ser := range memSeries {
		pod := ser.Metric["pod"]
		if pod == "" {
			continue
		}
		if v, ok := sampleValue(ser.Value); ok {
			a := touch(pod)
			a.mem += v
			if !seenPod[pod] {
				a.pods = append(a.pods, pod)
				seenPod[pod] = true
			}
		}
	}

	out := make([]DeploymentRow, 0, len(byWorkload))
	for w, a := range byWorkload {
		sort.Strings(a.pods)
		out = append(out, DeploymentRow{
			Cluster: c.Name, Namespace: namespace, Deployment: w,
			Pods: len(a.pods), CPUCores: a.cpu, MemBytes: a.mem,
			PodNames: a.pods,
		})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].CPUCores != out[j].CPUCores {
			return out[i].CPUCores > out[j].CPUCores
		}
		return out[i].Deployment < out[j].Deployment
	})
	return out, nil
}

// NodeRow — bir node'un anlık CPU/memory kullanımı (v0.8.582,
// audit: clusters-node-metrics-audit.md §3). Node = kube_node_info
// eşleşirse gerçek node adı, yoksa instance (ip:port). Pct'ler
// kendi paydalarına oran: CPUPct çekirdek sayısına (best-effort —
// 0 = bilinmiyor), MemPct MemTotal'a (zorunlu aileden, hep dolu).
type NodeRow struct {
	Cluster  string  `json:"cluster"`
	Node     string  `json:"node"`
	CPUCores float64 `json:"cpuCores"`
	MemBytes float64 `json:"memBytes"`
	CPUPct   float64 `json:"cpuPct,omitempty"`
	MemPct   float64 `json:"memPct,omitempty"`
	// v0.9.9 — node network hızı (node-exporter, lo hariç; best-effort).
	NetInBps  float64 `json:"netInBps,omitempty"`
	NetOutBps float64 `json:"netOutBps,omitempty"`
}

// instanceHost — "10.0.1.5:9100" → "10.0.1.5" (kube_node_info
// internal_ip join anahtarı). IPv6 "[::1]:9100" köşeli ayracını da
// soyar; port'suz değer olduğu gibi döner.
func instanceHost(inst string) string {
	// v0.9.19 (self-review fix) — port ayrımı kolon SAYISIYLA:
	// tek ':' = host:port (soy), '[...]:port' = köşeli IPv6 (soy),
	// çoklu ':' ayraçsız = ÇIPLAK IPv6 ('fe80::1') — dokunma. Eski
	// kod son grubu kırpıp ('fe80:') kube_node_info join'ini sessizce
	// bozuyordu; "son grup rakam mı" sezgiseli de IPv6'da yanılır.
	if strings.HasPrefix(inst, "[") {
		if i := strings.LastIndex(inst, "]:"); i > 0 {
			inst = inst[:i+1]
		}
	} else if strings.Count(inst, ":") == 1 {
		inst = inst[:strings.Index(inst, ":")]
	}
	return strings.Trim(inst, "[]")
}

// NodeMetrics — PodMetrics'in node-scope aynası: 3 zorunlu sorgu
// (cpu used, mem total, mem avail) + 2 best-effort (çekirdek
// sayısı, kube_node_info ad güzelleştirmesi). Sabit 5 sorgu/cluster.
func (s *Service) NodeMetrics(ctx context.Context, c ClusterConfig) ([]NodeRow, error) {
	type acc struct{ cpuUsed, memTotal, memAvail, cores, netIn, netOut float64 }
	byInst := map[string]*acc{}
	get := func(m map[string]string) *acc {
		k := m["instance"]
		a := byInst[k]
		if a == nil {
			a = &acc{}
			byInst[k] = a
		}
		return a
	}

	for _, q := range []struct {
		query string
		set   func(*acc, float64)
	}{
		{nodeCPUQuery(), func(a *acc, v float64) { a.cpuUsed = v }},
		{nodeMemTotalQuery(), func(a *acc, v float64) { a.memTotal = v }},
		{nodeMemAvailQuery(), func(a *acc, v float64) { a.memAvail = v }},
	} {
		series, err := s.doQuery(ctx, c, "/api/v1/query", url.Values{"query": {q.query}})
		if err != nil {
			return nil, err
		}
		for _, ser := range series {
			if v, ok := sampleValue(ser.Value); ok {
				q.set(get(ser.Metric), v)
			}
		}
	}
	// Best-effort: çekirdek sayısı (CPU% paydası).
	if series, err := s.doQuery(ctx, c, "/api/v1/query",
		url.Values{"query": {nodeCPUCountQuery()}}); err == nil {
		for _, ser := range series {
			if v, ok := sampleValue(ser.Value); ok {
				get(ser.Metric).cores = v
			}
		}
	}
	// v0.9.9 — best-effort: network hızları.
	for _, nq := range []struct {
		dir string
		set func(*acc, float64)
	}{
		{"receive", func(a *acc, v float64) { a.netIn = v }},
		{"transmit", func(a *acc, v float64) { a.netOut = v }},
	} {
		if series, err := s.doQuery(ctx, c, "/api/v1/query",
			url.Values{"query": {nodeNetQuery(nq.dir)}}); err == nil {
			for _, ser := range series {
				if v, ok := sampleValue(ser.Value); ok {
					nq.set(get(ser.Metric), v)
				}
			}
		}
	}
	// Best-effort: internal_ip → node adı.
	names := map[string]string{}
	if series, err := s.doQuery(ctx, c, "/api/v1/query",
		url.Values{"query": {nodeInfoQuery}}); err == nil {
		for _, ser := range series {
			if ip, node := ser.Metric["internal_ip"], ser.Metric["node"]; ip != "" && node != "" {
				names[ip] = node
			}
		}
	}

	out := make([]NodeRow, 0, len(byInst))
	for inst, a := range byInst {
		// Yalnız best-effort serisi taşıyan anahtarlar gürültü
		// (PodMetrics'in limit-only eleme sözleşmesi).
		if a.cpuUsed == 0 && a.memTotal == 0 {
			continue
		}
		name := inst
		if pretty := names[instanceHost(inst)]; pretty != "" {
			name = pretty
		}
		row := NodeRow{Cluster: c.Name, Node: name, CPUCores: a.cpuUsed,
			NetInBps: a.netIn, NetOutBps: a.netOut}
		if a.memTotal > 0 {
			row.MemBytes = a.memTotal - a.memAvail
			row.MemPct = clampPct(row.MemBytes / a.memTotal * 100)
		}
		if a.cores > 0 {
			row.CPUPct = clampPct(a.cpuUsed / a.cores * 100)
		}
		out = append(out, row)
	}
	return out, nil
}

// PodTrend returns per-bucket CPU + memory for ONE pod (drawer
// path — bounded by construction). Bucket = adaptif step
// (stepForWindow, v0.9.26): dar pencerede 15s'e kadar iner.
func (s *Service) PodTrend(ctx context.Context, c ClusterConfig, namespace, pod string, from, to time.Time) ([]TrendPoint, error) {
	return s.rangeTrend(ctx, c,
		singlePodCPUQuery(namespace, pod), singlePodMemQuery(namespace, pod), from, to)
}

// NamespaceTrend — PodTrend'in namespace-scoped aynası (v0.9.2):
// aynı dakika-bucket sözleşmesi, pod pini yok — namespace toplamı.
func (s *Service) NamespaceTrend(ctx context.Context, c ClusterConfig, namespace string, from, to time.Time) ([]TrendPoint, error) {
	return s.rangeTrend(ctx, c,
		singleNamespaceCPUQuery(namespace), singleNamespaceMemQuery(namespace), from, to)
}

// NetTrendPoint — cluster network throughput trendi (v0.9.9):
// dakika bucket'ında in/out byte/s.
type NetTrendPoint struct {
	Bucket int64   `json:"bucket"`
	InBps  float64 `json:"inBps"`
	OutBps float64 `json:"outBps"`
}

// NetworkTrend — cluster toplam ağ hızının dakika-bucket trendi
// (Overview throughput grafiği). rangeTrend'in net karşılığı; iki
// sorgu da zorunlu (grafiğin kendisi bu — best-effort'luk üst
// katmanda: seri boşsa UI grafiği hiç göstermez).
func (s *Service) NetworkTrend(ctx context.Context, c ClusterConfig, from, to time.Time) ([]NetTrendPoint, error) {
	// v0.9.26 — adaptif step, TABAN 15s; bucket rounding da bu
	// step'e bağlanır (aksi halde 60s yuvarlaması saniye-altı
	// çözünürlüğü çöpe atardı).
	step := stepForWindow(from, to)
	params := func(q string) url.Values {
		return url.Values{
			"query": {q},
			"start": {fmt.Sprintf("%d", from.Unix())},
			"end":   {fmt.Sprintf("%d", to.Unix())},
			"step":  {fmt.Sprintf("%d", step)},
		}
	}
	inSeries, err := s.doQuery(ctx, c, "/api/v1/query_range",
		params(summaryNetQuery("receive")))
	if err != nil {
		return nil, err
	}
	outSeries, err := s.doQuery(ctx, c, "/api/v1/query_range",
		params(summaryNetQuery("transmit")))
	if err != nil {
		return nil, err
	}
	byBucket := map[int64]*NetTrendPoint{}
	collect := func(series []promSeries, set func(*NetTrendPoint, float64)) {
		for _, ser := range series {
			for _, pair := range ser.Values {
				v, ts, ok := samplePair(pair)
				if !ok {
					continue
				}
				b := ts - ts%int64(step)
				tp := byBucket[b]
				if tp == nil {
					tp = &NetTrendPoint{Bucket: b}
					byBucket[b] = tp
				}
				set(tp, v)
			}
		}
	}
	collect(inSeries, func(tp *NetTrendPoint, v float64) { tp.InBps = v })
	collect(outSeries, func(tp *NetTrendPoint, v float64) { tp.OutBps = v })
	out := make([]NetTrendPoint, 0, len(byBucket))
	for _, tp := range byBucket {
		out = append(out, *tp)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Bucket < out[j].Bucket })
	return out, nil
}

// maxTrendSeries — multi-pod grafikte seri tavanı. v0.9.21: 10→8 —
// MultiLineChart foldTopN(n=8) 9-10. serileri "other"a katlıyordu,
// "Top 10 of N" başlığı grafikle çelişiyordu (self-review); tavan
// grafiğin gerçekte gösterdiği sayıya sabitlendi.
const maxTrendSeries = 8

// NamespacePodsTrend — namespace'in pod başına dakika-bucket
// trendleri (v0.9.3). Sorgu topk'siz (adım-başına set kayması
// kırar — promql.go notu); top-10 seçimi ortalama CPU'ya göre
// Go'da, cpu+mem AYNI pod setine filtrelenir. İkinci dönüş: kesme
// öncesi toplam pod sayısı ("top 10 of N" etiketi için).
func (s *Service) NamespacePodsTrend(ctx context.Context, c ClusterConfig, namespace string, from, to time.Time) ([]PodSeriesTrend, int, error) {
	// v0.9.26 — adaptif step, TABAN 15s; bucket rounding da bu
	// step'e bağlanır (aksi halde 60s yuvarlaması saniye-altı
	// çözünürlüğü çöpe atardı).
	step := stepForWindow(from, to)
	params := func(q string) url.Values {
		return url.Values{
			"query": {q},
			"start": {fmt.Sprintf("%d", from.Unix())},
			"end":   {fmt.Sprintf("%d", to.Unix())},
			"step":  {fmt.Sprintf("%d", step)},
		}
	}
	cpuSeries, err := s.doQuery(ctx, c, "/api/v1/query_range",
		params(nsPodsCPUTrendQuery(namespace)))
	if err != nil {
		return nil, 0, err
	}
	memSeries, err := s.doQuery(ctx, c, "/api/v1/query_range",
		params(nsPodsMemTrendQuery(namespace)))
	if err != nil {
		return nil, 0, err
	}

	type podAcc struct {
		byBucket map[int64]*TrendPoint
		cpuSum   float64
		cpuN     int
	}
	byPod := map[string]*podAcc{}
	get := func(pod string) *podAcc {
		a := byPod[pod]
		if a == nil {
			a = &podAcc{byBucket: map[int64]*TrendPoint{}}
			byPod[pod] = a
		}
		return a
	}
	point := func(a *podAcc, ts int64) *TrendPoint {
		b := ts - ts%int64(step)
		tp := a.byBucket[b]
		if tp == nil {
			tp = &TrendPoint{Bucket: b}
			a.byBucket[b] = tp
		}
		return tp
	}
	for _, ser := range cpuSeries {
		a := get(ser.Metric["pod"])
		for _, pair := range ser.Values {
			if v, ts, ok := samplePair(pair); ok {
				point(a, ts).CPUCores = v
				a.cpuSum += v
				a.cpuN++
			}
		}
	}
	for _, ser := range memSeries {
		a := get(ser.Metric["pod"])
		for _, pair := range ser.Values {
			if v, ts, ok := samplePair(pair); ok {
				point(a, ts).MemBytes = v
			}
		}
	}

	// Ortalama CPU'ya göre sırala, top-N kes (deterministik: eşitlikte
	// pod adı asc).
	type ranked struct {
		pod  string
		mean float64
	}
	all := make([]ranked, 0, len(byPod))
	for pod, a := range byPod {
		if pod == "" {
			continue
		}
		mean := 0.0
		if a.cpuN > 0 {
			mean = a.cpuSum / float64(a.cpuN)
		}
		all = append(all, ranked{pod, mean})
	}
	sort.Slice(all, func(i, j int) bool {
		if all[i].mean != all[j].mean {
			return all[i].mean > all[j].mean
		}
		return all[i].pod < all[j].pod
	})
	total := len(all)
	if len(all) > maxTrendSeries {
		all = all[:maxTrendSeries]
	}

	out := make([]PodSeriesTrend, 0, len(all))
	for _, r := range all {
		a := byPod[r.pod]
		trend := make([]TrendPoint, 0, len(a.byBucket))
		for _, tp := range a.byBucket {
			trend = append(trend, *tp)
		}
		sortTrend(trend)
		out = append(out, PodSeriesTrend{Pod: r.pod, Trend: trend})
	}
	return out, total, nil
}

// stepForWindow — Thanos range-query step'i (v0.9.26): pencere
// genişledikçe kabalaşan adaptif merdiven, TABAN 15s. 15s, OpenShift
// user-workload-monitoring'in TİPİK scrape interval'i — altına inmek
// Prometheus'un örneklemediği noktalar için interpolasyon/tekrar
// üretir, o yüzden taban. Bu bir VARSAYIM: 30s-scrape'li bir cluster'da
// 15s step gereksiz interpolasyona yol açar (Thanos scrape interval'ini
// sorgu-anında güvenilir vermediğinden per-cluster doğrulama yok;
// gerekirse ClusterConfig'e opsiyonel scrapeIntervalSec alanı eklenir).
// ClickHouse span-metrik 10s tier'ıyla KARIŞTIRILMAZ — ayrı dünya.
// Nokta bütçesi ≤~360/seri (tüm trend uçları ≤6h clamp'li):
//
//	≤1h→15s(240) · ≤3h→30s(360) · ≤6h→60s(360) · ≤24h→300s(288)
//	· ≤7d→1800s(336) · else→3600s.
func stepForWindow(from, to time.Time) int {
	span := to.Sub(from).Seconds()
	switch {
	case span <= 3600:
		return 15
	case span <= 3*3600:
		return 30
	case span <= 6*3600:
		return 60
	case span <= 24*3600:
		return 300
	case span <= 7*24*3600:
		return 1800
	default:
		// >7g: bütçeyi (≤480 nokta) GARANTİLE — sabit 3600s 30g'de
		// 720 nokta patlatırdı (ClickHouse audit "Delik 2"). Saat
		// katına yuvarlanmış dinamik step; tam sayı aritmetiği.
		const budget = 480
		mult := (int(span) + budget*3600 - 1) / (budget * 3600)
		return mult * 3600
	}
}

// rangeTrend — iki range-query'yi (cpu, mem) dakika bucket'larında
// birleştiren ortak yol; Pod/NamespaceTrend'in tek gövdesi.
func (s *Service) rangeTrend(ctx context.Context, c ClusterConfig, cpuQ, memQ string, from, to time.Time) ([]TrendPoint, error) {
	// v0.9.26 — adaptif step, TABAN 15s; bucket rounding da bu
	// step'e bağlanır (aksi halde 60s yuvarlaması saniye-altı
	// çözünürlüğü çöpe atardı).
	step := stepForWindow(from, to)
	params := func(q string) url.Values {
		return url.Values{
			"query": {q},
			"start": {fmt.Sprintf("%d", from.Unix())},
			"end":   {fmt.Sprintf("%d", to.Unix())},
			"step":  {fmt.Sprintf("%d", step)},
		}
	}
	cpuSeries, err := s.doQuery(ctx, c, "/api/v1/query_range", params(cpuQ))
	if err != nil {
		return nil, err
	}
	memSeries, err := s.doQuery(ctx, c, "/api/v1/query_range", params(memQ))
	if err != nil {
		return nil, err
	}
	byBucket := map[int64]*TrendPoint{}
	collect := func(series []promSeries, set func(*TrendPoint, float64)) {
		for _, ser := range series {
			for _, pair := range ser.Values {
				v, ts, ok := samplePair(pair)
				if !ok {
					continue
				}
				b := ts - ts%int64(step)
				tp := byBucket[b]
				if tp == nil {
					tp = &TrendPoint{Bucket: b}
					byBucket[b] = tp
				}
				set(tp, v)
			}
		}
	}
	collect(cpuSeries, func(tp *TrendPoint, v float64) { tp.CPUCores = v })
	collect(memSeries, func(tp *TrendPoint, v float64) { tp.MemBytes = v })
	out := make([]TrendPoint, 0, len(byBucket))
	for _, tp := range byBucket {
		out = append(out, *tp)
	}
	sortTrend(out)
	return out, nil
}

func clampPct(v float64) float64 {
	if v < 0 {
		return 0
	}
	if v > 100 {
		return 100
	}
	return v
}

func sortTrend(t []TrendPoint) {
	for i := 1; i < len(t); i++ {
		for j := i; j > 0 && t[j].Bucket < t[j-1].Bucket; j-- {
			t[j], t[j-1] = t[j-1], t[j]
		}
	}
}
