package thanos

import (
	"encoding/json"
	"fmt"
	"regexp"
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

// ── table enrichment queries (v0.9.37, design handoff B4) ───────
// Hepsi best-effort (kube-state-metrics yoksa alan boş, UI hücresi
// "—" / rozet gizli).

// Pod fazı (Pods tab Status): kube_pod_status_phase==1 → (ns,pod,phase).
func podPhaseQuery(nsFilter string) string {
	return fmt.Sprintf(`kube_pod_status_phase{pod!=""%s} == 1`, nsMatcher(nsFilter))
}

// Pod restart sayısı (Pods tab Restarts): container restart toplamı.
func podRestartsQuery(nsFilter string) string {
	return fmt.Sprintf(
		`sum by (namespace, pod) (kube_pod_container_status_restarts_total{pod!=""%s})`,
		nsMatcher(nsFilter))
}

// Node rolü (heatmap dot + Nodes tab): kube_node_role → (node,role).
const nodeRoleQuery = `kube_node_role`

// Namespace restart toplamı + failing pod sayısı (Namespaces tab).
func nsRestartsQuery(nsFilter string) string {
	return fmt.Sprintf(
		`sum by (namespace) (kube_pod_container_status_restarts_total{pod!=""%s})`,
		nsMatcher(nsFilter))
}

func nsFailingQuery(nsFilter string) string {
	return fmt.Sprintf(
		`count by (namespace) (kube_pod_status_phase{phase=~"Failed|Unknown"%s} == 1)`,
		nsMatcher(nsFilter))
}

// ── summary enrichment queries (v0.9.30, design handoff B1) ─────
// Hepsi BEST-EFFORT skaler (kube-state-metrics / Alertmanager rule
// serileri yoksa 0 kalır, UI ilgili kartı/bölümü render etmez).

// Kapasite (CPU% / Mem% paydası): kube_node_status_capacity.
const summaryCPUCapacityQuery = `sum(kube_node_status_capacity{resource="cpu"})`
const summaryMemCapacityQuery = `sum(kube_node_status_capacity{resource="memory"})`

// Pod fazı (donut + KPI): kube_pod_status_phase == 1. nsFilter'a
// tabi (pod sayısı semantiğiyle tutarlı).
func summaryPodPhaseQuery(phase, nsFilter string) string {
	return fmt.Sprintf(`count(kube_pod_status_phase{phase="%s"%s} == 1)`,
		escapeLabelValue(phase), nsMatcher(nsFilter))
}

// Firing alert sayısı (banner + KPI): ALERTS metriği (cluster-wide).
func summaryAlertCountQuery(severity string) string {
	return fmt.Sprintf(`count(ALERTS{severity="%s",alertstate="firing"})`,
		escapeLabelValue(severity))
}

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

// ── resource trend queries (v0.9.35, design handoff B2) ─────────
// Overview CPU/Mem area chart'ları. Total = tek seri (cluster
// toplamı); byNode = per-instance (top-N Go'da). node-exporter
// ailesi (lo/idle hariç); metrik yoksa boş → UI grafiği gizler.

func resourceTrendQuery(metric string, byNode bool) string {
	var expr string
	switch metric {
	case "mem":
		expr = `(node_memory_MemTotal_bytes - node_memory_MemAvailable_bytes)`
	default: // cpu
		expr = `rate(node_cpu_seconds_total{mode!="idle"}[5m])`
	}
	if byNode {
		return fmt.Sprintf(`topk(%d, sum by (instance) (%s))`, maxTrendSeries, expr)
	}
	return "sum(" + expr + ")"
}

// ── multi-pod trend queries (v0.9.3, trend-upgrade audit §2.3) ──
//
// topk BİLEREK YOK: query_range'te topk her adım için ayrı
// değerlendirilir — adımlar arasında pod seti değişir ve seriler
// kırılır. Bunun yerine sum by (pod) ham döner (maxSeriesParsed
// 1000 + 8MB gövde kalkanları), top-N seçimi Go'da ortalama CPU'ya
// göre TUTARLI tek sette yapılır (cpu ve mem aynı pod setine
// filtrelenir).
func nsPodsCPUTrendQuery(namespace string) string {
	return fmt.Sprintf(
		`sum by (pod) (rate(container_cpu_usage_seconds_total{container!="",pod!="",namespace="%s"}[5m]))`,
		escapeLabelValue(namespace))
}

func nsPodsMemTrendQuery(namespace string) string {
	return fmt.Sprintf(
		`sum by (pod) (container_memory_working_set_bytes{container!="",pod!="",namespace="%s"})`,
		escapeLabelValue(namespace))
}

// ── network queries (v0.9.9, tabbed-detail audit L2) ────────────
//
// İki aile, hepsi BEST-EFFORT (probe operatörde — kod hiçbirinin
// varlığını varsaymaz; seri yoksa alan 0 kalır ve UI hiç göstermez):
//   pod'lar:  container_network_{receive,transmit}_bytes_total
//             (cAdvisor; pod+namespace label'lı, container label'ı
//             taşımaz — pod!="" yeter)
//   node/cluster: node_network_*_bytes_total{device!="lo"}
//             (node-exporter; loopback dışlanır)

func podNetQuery(direction, nsFilter string) string {
	return fmt.Sprintf(
		`topk(%d, sum by (namespace, pod) (rate(container_network_%s_bytes_total{pod!=""%s}[5m])))`,
		podListLimit, escapeLabelValue(direction), nsMatcher(nsFilter))
}

func nodeNetQuery(direction string) string {
	return fmt.Sprintf(
		`topk(%d, sum by (instance) (rate(node_network_%s_bytes_total{device!="lo"}[5m])))`,
		podListLimit, escapeLabelValue(direction))
}

// summaryNetQuery — cluster toplamı (kart + throughput grafiği aynı
// node-exporter ailesinden: hostNetwork pod'ları da kapsar).
func summaryNetQuery(direction string) string {
	return fmt.Sprintf(
		`sum(rate(node_network_%s_bytes_total{device!="lo"}[5m]))`,
		escapeLabelValue(direction))
}

// ── deployment-rollup queries (v0.9.22, deployment audit §3) ────
//
// Pod→Deployment eşlemesi kube-state-metrics'in İKİ ailesinden
// Go'da join'lenir (PromQL label_replace cambazlığı yerine —
// NamespacePodsTrend'in Go-tarafı top-N emsali): kube_pod_owner
// pod→ReplicaSet, kube_replicaset_owner ReplicaSet→Deployment.
// İkisi de BEST-EFFORT: aileler yoksa ad-sezgiseli fallback
// (stripPodSuffixes), o da tutmazsa satır "(unassigned)" altında
// toplanır — katman hiç veri bulamazsa UI kademeyi göstermez.

func nsPodOwnerQuery(namespace string) string {
	return fmt.Sprintf(
		`kube_pod_owner{owner_kind="ReplicaSet",pod!="",namespace="%s"}`,
		escapeLabelValue(namespace))
}

func nsReplicaSetOwnerQuery(namespace string) string {
	return fmt.Sprintf(
		`kube_replicaset_owner{owner_kind="Deployment",namespace="%s"}`,
		escapeLabelValue(namespace))
}

// ── deployment replicas/status (v0.9.39, design handoff §5) ─────────
// kube-state-metrics aileleri; best-effort — aile yoksa satırın
// Status'u boş kalır ve UI '—' basar. max by: HA'lı KSM çiftlerinin
// (iki replika aynı seriyi basar) dublikasyonunu düzler.

func nsDeployDesiredQuery(namespace string) string {
	return fmt.Sprintf(
		`max by (deployment) (kube_deployment_spec_replicas{namespace="%s"})`,
		escapeLabelValue(namespace))
}

func nsDeployReadyQuery(namespace string) string {
	return fmt.Sprintf(
		`max by (deployment) (kube_deployment_status_replicas_ready{namespace="%s"})`,
		escapeLabelValue(namespace))
}

// nsDeployAvailFalseQuery — Available koşulu status="false" değerinde
// 1 olan (yani kubelet'in "kapasite altında" dediği) deployment'lar.
func nsDeployAvailFalseQuery(namespace string) string {
	return fmt.Sprintf(
		`max by (deployment) (kube_deployment_status_condition{condition="Available",status="false",namespace="%s"} == 1)`,
		escapeLabelValue(namespace))
}

// ── deployment-scoped trend queries (v0.9.50, design handoff §8) ────
// Servis → Infrastructure sekmesinin CPU/Mem grafiği. Pod eşleşmesi
// README sözleşmesi: pod adı "<deploy>-" öneklidir (Deployment→RS→Pod
// adlandırması). Deploy adı =~ matcher'ına girdiğinden önce regex
// meta'ları, sonra label kaçışı uygulanır. topk BİLEREK YOK (v0.9.3
// notu: query_range'te topk adım-başına ayrı değerlendirilir, set
// kayması serileri kırar) — top-N seçimi Go'da.

func deployTrendQuery(namespace, deploy, metric string, byPod bool) string {
	sel := fmt.Sprintf(`container!="",namespace="%s",pod=~"%s-.*"`,
		escapeLabelValue(namespace), escapeLabelValue(regexp.QuoteMeta(deploy)))
	var expr string
	switch metric {
	case "mem":
		expr = fmt.Sprintf(`container_memory_working_set_bytes{%s}`, sel)
	default: // cpu
		expr = fmt.Sprintf(`rate(container_cpu_usage_seconds_total{%s}[5m])`, sel)
	}
	if byPod {
		return fmt.Sprintf(`sum by (pod) (%s)`, expr)
	}
	return "sum(" + expr + ")"
}

// ── JMX/JVM discovery + trend (v0.9.140, auto-discovery v0.9.144) ───────
// Operatör: Thanos'ta servisin jvm_/jboss_ metriklerini AUTO-DISCOVER edip
// Infrastructure sekmesi altında göster. SABİT metrik-adı listesi YOK —
// __name__=~"(jvm|jboss)_.*" keşfi + metrik başına generic trend. SELECTOR
// (operatör 2026-07-21, kesin): kube-prometheus/cAdvisor düzlemi
// (deployTrendQuery ile aynı): container=~".*",namespace="<ns>",
// pod=~"<deploy>-.*". Ham metrik adı DIŞARIDAN gelir → jmxMetricNameRe ile
// doğrulanır (PromQL enjeksiyonuna karşı; yalnız jvm_/jboss_ + [a-z0-9_]).

var jmxMetricNameRe = regexp.MustCompile(`^(jvm|jboss)_[a-z0-9_]+$`)

// ValidJMXMetric — ham metrik adının güvenli JMX adı olup olmadığı.
func ValidJMXMetric(m string) bool { return jmxMetricNameRe.MatchString(m) }

// jmxSelector — servisin JMX serilerinin cAdvisor-düzlem selector'ı.
func jmxSelector(namespace, deploy string) string {
	return fmt.Sprintf(`container=~".*",namespace="%s",pod=~"%s-.*"`,
		escapeLabelValue(namespace), escapeLabelValue(regexp.QuoteMeta(deploy)))
}

// jmxDiscoveryQuery — servisin taşıdığı jvm_/jboss_ metrik ADLARINI say
// (count by (__name__)). Boş dönerse cluster'da servisin JMX'i yok.
func jmxDiscoveryQuery(namespace, deploy string) string {
	return fmt.Sprintf(`count by (__name__) ({__name__=~"(jvm|jboss)_.*",%s})`,
		jmxSelector(namespace, deploy))
}

// jmxGrouping — (metric, byPod) → PromQL "sum by (...)" grouping ifadesi ve
// seri adının okunacağı label listesi (v0.9.145/146). jboss_ datasource
// metrikleri HER ZAMAN data_source taşır (operatör: bir projede 5-10+
// datasource); toggle POD boyutunu EKLER:
//   jboss + off (By datasource) → by (data_source)       ad = data_source
//   jboss + on  (By pod)        → by (pod, data_source)   ad = "data_source · pod"
//   jvm   + off (Total)         → sum()                   ad = ""
//   jvm   + on  (By pod)        → by (pod)                ad = pod
// byClause boş = total (sum). nameLabels boş = tek toplam seri.
func jmxGrouping(metric string, byPod bool) (byClause string, nameLabels []string) {
	if strings.HasPrefix(metric, "jboss_") {
		// WildFly regular datasource'u `data_source`, XA datasource'u
		// `xa_data_source` label'ında taşır (operatör: XA'lar bulunmuyordu,
		// sadece SQL geliyordu). İKİSİNİ birden grupla; ad non-empty olanı
		// (coalesce) alır — böylece regular + XA aynı panelde ayrı seriler.
		if byPod {
			return "pod, data_source, xa_data_source", []string{"data_source", "xa_data_source", "pod"}
		}
		return "data_source, xa_data_source", []string{"data_source", "xa_data_source"}
	}
	if byPod {
		return "pod", []string{"pod"}
	}
	return "", nil
}

// jmxTrendQuery — keşfedilen bir metriğin trendi. Sayaç (_total/_sum)
// rate'lenir, gerisi gauge; grouping jmxGrouping'e göre.
func jmxTrendQuery(namespace, deploy, metric string, byPod bool) string {
	sel := jmxSelector(namespace, deploy)
	var expr string
	if strings.HasSuffix(metric, "_total") || strings.HasSuffix(metric, "_sum") {
		expr = fmt.Sprintf(`rate(%s{%s}[5m])`, metric, sel)
	} else {
		expr = fmt.Sprintf(`%s{%s}`, metric, sel)
	}
	if byClause, _ := jmxGrouping(metric, byPod); byClause != "" {
		return fmt.Sprintf(`sum by (%s) (%s)`, byClause, expr)
	}
	return "sum(" + expr + ")"
}

// stripPodSuffixes — eşleme aileleri yokken pod adından iş yükü adı
// sezgiseli: Deployment pod'u <ad>-<rs-hash 8-10 hex>-<5 rasgele>,
// StatefulSet <ad>-<N>, DaemonSet <ad>-<5 rasgele>. Son segment
// rasgele/sayıysa soyulur; kalan son segment rs-hash'e benziyorsa o
// da soyulur. Tutmayan ada dokunulmaz ("" değil — bilinçli).
func stripPodSuffixes(pod string) string {
	segs := strings.Split(pod, "-")
	if len(segs) < 2 {
		return pod
	}
	last := segs[len(segs)-1]
	if isPodRandomSuffix(last) || isAllDigits(last) {
		segs = segs[:len(segs)-1]
		if len(segs) >= 2 && isReplicaSetHash(segs[len(segs)-1]) {
			segs = segs[:len(segs)-1]
		}
		return strings.Join(segs, "-")
	}
	return pod
}

func isAllDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

// isPodRandomSuffix — k8s'in 5 karakterlik rand suffix alfabesi
// (rakam + bcdfghjklmnpqrstvwxz; sesli harf yok).
func isPodRandomSuffix(s string) bool {
	if len(s) != 5 {
		return false
	}
	for _, c := range s {
		if !strings.ContainsRune("0123456789bcdfghjklmnpqrstvwxz", c) {
			return false
		}
	}
	return true
}

func isReplicaSetHash(s string) bool {
	if len(s) < 8 || len(s) > 10 {
		return false
	}
	for _, c := range s {
		if !strings.ContainsRune("0123456789abcdef", c) {
			return false
		}
	}
	return true
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
