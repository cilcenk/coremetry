package thanos

import (
	"strings"
	"testing"
)

// v0.10.128 — cluster matcher enjeksiyonu (design §1.1): TEK Thanos
// Querier'ın önünde N cluster varken her sorgu `<label>="<value>"`
// taşımalı; aksi hâlde pod/node/deployment tabloları cluster'ları
// karıştırır (keşif raporu engel #1). Şablon başına elle matcher
// eklemek yerine ifade düzeyinde enjeksiyon: her vektör seçicisine
// (süslü parantezli ya da çıplak metrik adı) matcher eklenir.
//
// Sözleşme: label boşsa ifade DEĞİŞMEZ (eski davranış). Fonksiyon adları,
// by/without/on/ignoring/group_* etiket listeleri, dize sabitleri,
// süreler ([5m]) ve sayılar metrik sanılmaz.

func TestWithClusterMatcherGolden(t *testing.T) {
	cases := []struct{ name, in, want string }{
		{"boş label → değişmez", `up`, `up`},
		{"çıplak metrik", `kube_node_info`, `kube_node_info{cluster="p1"}`},
		{"süslü parantez", `kube_pod_owner{owner_kind="ReplicaSet",pod!=""}`, `kube_pod_owner{cluster="p1",owner_kind="ReplicaSet",pod!=""}`},
		{"boş süslü parantez", `{__name__=~"(jvm|jboss)_.*"}`, `{cluster="p1",__name__=~"(jvm|jboss)_.*"}`},
		{"rate + süre", `sum by (namespace, pod) (rate(container_cpu_usage_seconds_total{container!=""}[5m]))`,
			`sum by (namespace, pod) (rate(container_cpu_usage_seconds_total{cluster="p1",container!=""}[5m]))`},
		{"çıplak metrik + süre", `rate(node_network_receive_bytes_total[5m])`, `rate(node_network_receive_bytes_total{cluster="p1"}[5m])`},
		{"by listesi metrik değil", `topk(500, sum by (instance) (node_memory_MemTotal_bytes))`,
			`topk(500, sum by (instance) (node_memory_MemTotal_bytes{cluster="p1"}))`},
		{"aritmetik iki seçici", `sum(node_memory_MemTotal_bytes) - sum(node_memory_MemAvailable_bytes)`,
			`sum(node_memory_MemTotal_bytes{cluster="p1"}) - sum(node_memory_MemAvailable_bytes{cluster="p1"})`},
		{"karşılaştırma + sayı", `max by (namespace, pod, reason) (kube_pod_container_status_last_terminated_reason{pod!=""} == 1)`,
			`max by (namespace, pod, reason) (kube_pod_container_status_last_terminated_reason{cluster="p1",pod!=""} == 1)`},
		{"time() fonksiyonu", `time() - ALERTS_FOR_STATE{alertstate="firing"}`, `time() - ALERTS_FOR_STATE{cluster="p1",alertstate="firing"}`},
		{"on/group_left etiket listeleri", `a * on (namespace, pod) group_left(node) kube_pod_info`,
			`a{cluster="p1"} * on (namespace, pod) group_left(node) kube_pod_info{cluster="p1"}`},
		{"dize içindeki süslü parantez", `x{foo="{bar}"}`, `x{cluster="p1",foo="{bar}"}`},
		{"kaçışlı tırnak", `x{re=~"a\"b"}`, `x{cluster="p1",re=~"a\"b"}`},
		{"offset", `x offset 5m`, `x{cluster="p1"} offset 5m`},
		{"etiket değeri kaçışlanır", `x`, `x{cluster="p\"1"}`},
		{"bool karşılaştırma", `x > bool 0`, `x{cluster="p1"} > bool 0`},
		{"and/or/unless", `a and b or c unless d`, `a{cluster="p1"} and b{cluster="p1"} or c{cluster="p1"} unless d{cluster="p1"}`},
		{"count by (__name__)", `count by (__name__) ({__name__=~"jvm_.*",namespace="n"})`,
			`count by (__name__) ({cluster="p1",__name__=~"jvm_.*",namespace="n"})`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			label, value := "cluster", "p1"
			if c.name == "boş label → değişmez" {
				label = ""
			}
			if c.name == "etiket değeri kaçışlanır" {
				value = `p"1`
			}
			got := withClusterMatcher(c.in, label, value)
			if got != c.want {
				t.Fatalf("\n girdi: %s\n alınan: %s\n beklenen: %s", c.in, got, c.want)
			}
		})
	}
}

// Her PromQL şablonu enjeksiyondan geçince çıplak metrik kalmamalı ve
// her seçici matcher taşımalı. Şablon listesi promql.go'nun tamamı —
// yeni şablon eklenince buraya da eklenir (bareSelectorCount kapısı
// eksik olanı yakalar).
func TestEveryPromQLTemplateGetsClusterMatcher(t *testing.T) {
	templates := map[string]string{
		"podCPUQuery":               podCPUQuery("ns.*", ""),
		"podMemQuery":               podMemQuery("", "api-.*"),
		"podLimitQuery":             podLimitQuery("cpu", "ns", ""),
		"podRequestQuery":           podRequestQuery("memory", "", ""),
		"singlePodCPUQuery":         singlePodCPUQuery("ns", "pod"),
		"singlePodMemQuery":         singlePodMemQuery("ns", "pod"),
		"singleNamespaceCPUQuery":   singleNamespaceCPUQuery("ns"),
		"singleNamespaceMemQuery":   singleNamespaceMemQuery("ns"),
		"nodeCPUQuery":              nodeCPUQuery(),
		"nodeMemTotalQuery":         nodeMemTotalQuery(),
		"nodeMemAvailQuery":         nodeMemAvailQuery(),
		"nodeCPUCountQuery":         nodeCPUCountQuery(),
		"nodeInfoQuery":             nodeInfoQuery,
		"summaryNodeCountQuery":     summaryNodeCountQuery,
		"summaryPodCountQuery":      summaryPodCountQuery("ns"),
		"summaryCPUUsedQuery":       summaryCPUUsedQuery,
		"summaryMemUsedQuery":       summaryMemUsedQuery,
		"podPhaseQuery":             podPhaseQuery(""),
		"podRestartsQuery":          podRestartsQuery("ns"),
		"podLastTermQuery":          podLastTermQuery(""),
		"nodeRoleQuery":             nodeRoleQuery,
		"nsRestartsQuery":           nsRestartsQuery(""),
		"nsFailingQuery":            nsFailingQuery(""),
		"summaryCPUCapacityQuery":   summaryCPUCapacityQuery,
		"summaryMemCapacityQuery":   summaryMemCapacityQuery,
		"summaryPodPhaseQuery":      summaryPodPhaseQuery("Running", ""),
		"summaryAlertCountQuery":    summaryAlertCountQuery("critical"),
		"nsCPUQuery":                nsCPUQuery(""),
		"nsMemQuery":                nsMemQuery(""),
		"nsPodCountQuery":           nsPodCountQuery(""),
		"resourceTrendQuery/node":   resourceTrendQuery("cpu", true),
		"resourceTrendQuery/all":    resourceTrendQuery("memory", false),
		"nsPodsCPUTrendQuery":       nsPodsCPUTrendQuery("ns"),
		"nsPodsMemTrendQuery":       nsPodsMemTrendQuery("ns"),
		"podNetQuery":               podNetQuery("receive", "", ""),
		"nodeNetQuery":              nodeNetQuery("transmit"),
		"summaryNetQuery":           summaryNetQuery("receive"),
		"nsPodOwnerQuery":           nsPodOwnerQuery("ns"),
		"nsReplicaSetOwnerQuery":    nsReplicaSetOwnerQuery("ns"),
		"nsDeployDesiredQuery":      nsDeployDesiredQuery("ns"),
		"nsDeployReadyQuery":        nsDeployReadyQuery("ns"),
		"nsDeployAvailFalseQuery":   nsDeployAvailFalseQuery("ns"),
		"deployTrendQuery/byPod":    deployTrendQuery("ns", "dep", "cpu", true),
		"deployTrendQuery/sum":      deployTrendQuery("ns", "dep", "memory", false),
		"haproxyTrendQuery/rps":     haproxyTrendQuery("ns", "5xx"),
		"haproxyTrendQuery/latency": haproxyTrendQuery("ns", "latency"),
		"jmxDiscoveryQuery":         jmxDiscoveryQuery("ns", "dep"),
		"jmxTrendQuery":             jmxTrendQuery("ns", "dep", "jvm_memory_used_bytes", true, "pod-1"),
	}
	for name, expr := range templates {
		t.Run(name, func(t *testing.T) {
			if expr == "" {
				t.Skip("şablon bu argümanlarla boş dönüyor")
			}
			got := withClusterMatcher(expr, "cluster", "p1")
			if n := bareSelectorCount(got); n != 0 {
				t.Fatalf("enjeksiyondan sonra %d çıplak metrik kaldı:\n%s\n→ %s", n, expr, got)
			}
			// Her süslü parantez grubu matcher taşır.
			if strings.Count(got, `cluster="p1"`) != strings.Count(got, "{") {
				t.Fatalf("her seçici matcher taşımalı (%d matcher / %d seçici):\n%s", strings.Count(got, `cluster="p1"`), strings.Count(got, "{"), got)
			}
			// Enjeksiyon idempotent değil ama tekrar edilmemeli — yeniden
			// koşum ikinci matcher eklerse çağıran çift geçirmiş demektir.
			if withClusterMatcher(got, "cluster", "p1") == got {
				t.Fatal("ikinci geçiş aynı çıktıyı verdi — enjeksiyon hiç olmamış gibi")
			}
		})
	}
}
