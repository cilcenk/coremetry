package thanos

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

// PromQL builders for the /clusters surface. One query per
// (cluster, signal) — grouped by (namespace, pod), NEVER a query
// per pod (audit §4). All list queries wear two cardinality
// shields: the per-cluster namespace regex and a topk cap.
//
// Metric names are the platform-monitoring (cAdvisor +
// kube-state-metrics) conventions:
//   container_cpu_usage_seconds_total      — CPU, counter (cores·s)
//   container_memory_working_set_bytes     — memory, gauge (bytes)
//   kube_pod_container_resource_limits     — limits (kube-state-metrics)
// Their availability on a given Thanos tenancy is VERIFIED at
// integration time (audit §4/§8 — count() probe), not assumed;
// limits are best-effort at query time.

// podListLimit caps the list-view result set per cluster — the
// clampLimit spirit applied to PromQL. maxSeriesParsed backstops
// it on the parse side.
const podListLimit = 500

// escapeLabelValue escapes a value for use inside a PromQL label
// matcher string: backslashes first, then quotes. The namespace
// filter is a REGEX by contract, so regex metacharacters are the
// operator's to use — only string-literal framing is escaped.
func escapeLabelValue(v string) string {
	v = strings.ReplaceAll(v, `\`, `\\`)
	v = strings.ReplaceAll(v, `"`, `\"`)
	return v
}

// nsMatcher renders the optional namespace=~ conjunct. Empty
// filter → empty string (all namespaces; topk still caps).
func nsMatcher(nsFilter string) string {
	if nsFilter == "" {
		return ""
	}
	return fmt.Sprintf(`,namespace=~"%s"`, escapeLabelValue(nsFilter))
}

// podCPUQuery — per-pod CPU in cores: 5m rate over the cAdvisor
// counter, container!="" drops the pause/aggregate rows,
// pod!="" drops node-level series.
func podCPUQuery(nsFilter string) string {
	return fmt.Sprintf(
		`topk(%d, sum by (namespace, pod) (rate(container_cpu_usage_seconds_total{container!="",pod!=""%s}[5m])))`,
		podListLimit, nsMatcher(nsFilter))
}

// podMemQuery — per-pod working-set bytes (the OOM-relevant
// number, matching `kubectl top pod`).
func podMemQuery(nsFilter string) string {
	return fmt.Sprintf(
		`topk(%d, sum by (namespace, pod) (container_memory_working_set_bytes{container!="",pod!=""%s}))`,
		podListLimit, nsMatcher(nsFilter))
}

// podLimitQuery — per-pod resource limits from kube-state-metrics.
// resource is "cpu" (cores) or "memory" (bytes). Best-effort: the
// caller tolerates this series being entirely absent.
func podLimitQuery(resource, nsFilter string) string {
	return fmt.Sprintf(
		`sum by (namespace, pod) (kube_pod_container_resource_limits{resource="%s",pod!=""%s})`,
		escapeLabelValue(resource), nsMatcher(nsFilter))
}

// podRequestQuery — podLimitQuery's sibling for resource REQUESTS
// (v0.8.580, audit: clusters-requests-nodes-audit.md §3). Same
// best-effort contract; the two percentages answer different
// questions — limit = throttle/OOM proximity, request =
// provisioning accuracy — so both ride the row.
func podRequestQuery(resource, nsFilter string) string {
	return fmt.Sprintf(
		`sum by (namespace, pod) (kube_pod_container_resource_requests{resource="%s",pod!=""%s})`,
		escapeLabelValue(resource), nsMatcher(nsFilter))
}

// singlePodCPUQuery / singlePodMemQuery — the drawer's range-query
// variants, pinned to one (namespace, pod). No topk: one pod by
// construction.
func singlePodCPUQuery(namespace, pod string) string {
	return fmt.Sprintf(
		`sum(rate(container_cpu_usage_seconds_total{container!="",namespace="%s",pod="%s"}[5m]))`,
		escapeLabelValue(namespace), escapeLabelValue(pod))
}

func singlePodMemQuery(namespace, pod string) string {
	return fmt.Sprintf(
		`sum(container_memory_working_set_bytes{container!="",namespace="%s",pod="%s"})`,
		escapeLabelValue(namespace), escapeLabelValue(pod))
}

// singleNamespaceCPUQuery / singleNamespaceMemQuery — namespace-trend
// drawer'ının range-query'leri (v0.9.2, namespace-trend audit §3):
// singlePod* sorgularının pod pini kalkmış aynası — namespace'in TÜM
// pod'larının toplamı tek seri olarak döner.
func singleNamespaceCPUQuery(namespace string) string {
	return fmt.Sprintf(
		`sum(rate(container_cpu_usage_seconds_total{container!="",pod!="",namespace="%s"}[5m]))`,
		escapeLabelValue(namespace))
}

func singleNamespaceMemQuery(namespace string) string {
	return fmt.Sprintf(
		`sum(container_memory_working_set_bytes{container!="",pod!="",namespace="%s"})`,
		escapeLabelValue(namespace))
}

// ── node-scope queries (v0.8.582, audit: clusters-node-metrics-
// audit.md §2) ──────────────────────────────────────────────────
//
// Dar kapsam: CPU / memory / sayı — hepsi node-exporter ailesinden,
// kube-state-metrics'e ZORUNLU bağımlılık yok (MemPct paydası
// MemTotal, aynı aile). Satır anahtarı `instance`; kube_node_info
// yalnız görünen adı güzelleştiren best-effort join. nsMatcher
// UYGULANMAZ (node namespace'siz); topk kalkanı kalır — node
// ölçeğinde neredeyse hiç tetiklenmez ama bedava ve deseni tekdüze
// tutar. Cluster başına sabit 5 sorgu, node sayısından bağımsız.

// nodeCPUQuery — kullanılan çekirdek (idle-dışı cpu-saniye oranı).
func nodeCPUQuery() string {
	return fmt.Sprintf(
		`topk(%d, sum by (instance) (rate(node_cpu_seconds_total{mode!="idle"}[5m])))`,
		podListLimit)
}

// nodeMemTotalQuery / nodeMemAvailQuery — used = Total − Avail;
// MemPct = used/Total. Payda zorunlu aileden geldiği için memory
// yüzdesi best-effort'a MUHTAÇ DEĞİL.
func nodeMemTotalQuery() string {
	return fmt.Sprintf(
		`topk(%d, sum by (instance) (node_memory_MemTotal_bytes))`, podListLimit)
}

func nodeMemAvailQuery() string {
	return fmt.Sprintf(
		`topk(%d, sum by (instance) (node_memory_MemAvailable_bytes))`, podListLimit)
}

// nodeCPUCountQuery — çekirdek sayısı (CPU% paydası): her çekirdek
// tam olarak bir idle serisi taşır. BEST-EFFORT: dönmezse CPUPct 0
// kalır (HostRow.MemPct "0 = bilinmiyor" sözleşmesi).
func nodeCPUCountQuery() string {
	return fmt.Sprintf(
		`topk(%d, count by (instance) (node_cpu_seconds_total{mode="idle"}))`, podListLimit)
}

// nodeInfoQuery — instance→node adı güzelleştirmesi (BEST-EFFORT):
// kube_node_info'nun internal_ip label'ı, instance'ın port'suz
// haliyle eşleşir. kube-state-metrics yoksa satır adı instance kalır.
const nodeInfoQuery = `kube_node_info`

// ── cluster-summary queries (v0.8.586, redesign audit §3.1) ─────
//
// Genel görünüm kartları için SKALER cevaplı sorgular — topk'li tam
// vektörler değil (kart için yüzlerce KB pod listesi çekilmez; pod
// SAYISI da topk kesmesine uğramadan TAM sayılır). Tek örneklemli
// vektör döner (metric{} boş); parser ilk örneği okur.

// summaryNodeCountQuery — çekirdek-idle serisi taşıyan instance sayısı.
const summaryNodeCountQuery = `count(count by (instance) (node_cpu_seconds_total{mode="idle"}))`

// summaryPodCountQuery — nsFilter'a tabi TAM pod sayısı.
func summaryPodCountQuery(nsFilter string) string {
	return fmt.Sprintf(
		`count(count by (namespace, pod) (container_cpu_usage_seconds_total{container!="",pod!=""%s}))`,
		nsMatcher(nsFilter))
}

// summaryCPUUsedQuery / summaryMemUsedQuery — cluster toplamı,
// node-exporter ailesinden (sistem yükü dahil — Nodes bölümüyle
// tutarlı; nsFilter pod sayısını daraltır, node toplamını değil).
const summaryCPUUsedQuery = `sum(rate(node_cpu_seconds_total{mode!="idle"}[5m]))`
const summaryMemUsedQuery = `sum(node_memory_MemTotal_bytes) - sum(node_memory_MemAvailable_bytes)`

// ── namespace-rollup queries (v0.8.588, redesign audit §3.3) ────
//
// Rollup AYRI sorgudur, topk(500)'lük pod listesinden client-side
// türetilmez: kesilmiş listeden toplanan namespace toplamı SESSİZCE
// eksik kalırdı. Satır sayısı = namespace sayısı (≤yüzler); topk
// kalkanı yine bedava tekdüzelik olarak durur.

func nsCPUQuery(nsFilter string) string {
	return fmt.Sprintf(
		`topk(%d, sum by (namespace) (rate(container_cpu_usage_seconds_total{container!="",pod!=""%s}[5m])))`,
		podListLimit, nsMatcher(nsFilter))
}

func nsMemQuery(nsFilter string) string {
	return fmt.Sprintf(
		`topk(%d, sum by (namespace) (container_memory_working_set_bytes{container!="",pod!=""%s}))`,
		podListLimit, nsMatcher(nsFilter))
}

// nsPodCountQuery — namespace başına TAM pod sayısı (iç count
// pod'ları teker sayar, dış count namespace'e toplar).
func nsPodCountQuery(nsFilter string) string {
	return fmt.Sprintf(
		`topk(%d, count by (namespace) (count by (namespace, pod) (container_cpu_usage_seconds_total{container!="",pod!=""%s})))`,
		podListLimit, nsMatcher(nsFilter))
}

// ── sample decoding ─────────────────────────────────────────────

// sampleValue decodes an instant-vector sample pair [ts, "v"] and
// returns the value. Prometheus encodes sample values as STRINGS
// ("NaN" and "+Inf" are legal) — non-finite parses are dropped by
// the ok=false return, mirroring sanitizeFloats' JSON discipline.
func sampleValue(pair []json.RawMessage) (float64, bool) {
	v, _, ok := samplePair(pair)
	return v, ok
}

// samplePair decodes [ts, "v"] into (value, unix-second ts).
func samplePair(pair []json.RawMessage) (float64, int64, bool) {
	if len(pair) != 2 {
		return 0, 0, false
	}
	// Timestamp arrives as a JSON number (possibly fractional).
	var tsf float64
	if err := json.Unmarshal(pair[0], &tsf); err != nil {
		return 0, 0, false
	}
	var vs string
	if err := json.Unmarshal(pair[1], &vs); err != nil {
		return 0, 0, false
	}
	v, err := strconv.ParseFloat(vs, 64)
	if err != nil || v != v || v > 1e308 || v < -1e308 { // NaN / ±Inf guard
		return 0, 0, false
	}
	return v, int64(tsf), true
}
