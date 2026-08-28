package thanos

import (
	"encoding/json"
	"strings"
	"testing"
)

// v0.8.575 — PromQL builder + sample-decode contracts for the
// /clusters surface (audit §4). Table-driven per CLAUDE.md #11.

func TestPodQueriesCarryCardinalityShields(t *testing.T) {
	cases := []struct {
		name     string
		query    string
		wantSubs []string
	}{
		{"cpu with ns filter", podCPUQuery("^app-", ""), []string{
			`topk(500,`, `sum by (namespace, pod)`,
			`rate(container_cpu_usage_seconds_total{container!="",pod!="",namespace=~"^app-"}[5m])`,
		}},
		{"cpu without ns filter", podCPUQuery("", ""), []string{
			`topk(500,`, `container_cpu_usage_seconds_total{container!="",pod!=""}`,
		}},
		{"mem with ns filter", podMemQuery("payments", ""), []string{
			`topk(500,`, `container_memory_working_set_bytes{container!="",pod!="",namespace=~"payments"}`,
		}},
		{"cpu limits", podLimitQuery("cpu", "", ""), []string{
			`kube_pod_container_resource_limits{resource="cpu",pod!=""}`,
		}},
		{"memory limits with ns", podLimitQuery("memory", "^x$", ""), []string{
			`resource="memory"`, `namespace=~"^x$"`,
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			for _, sub := range c.wantSubs {
				if !strings.Contains(c.query, sub) {
					t.Fatalf("query %q missing %q", c.query, sub)
				}
			}
		})
	}
}

// v0.8.580 — request ekseni: podRequestQuery podLimitQuery'nin
// birebir kardeşi kalmalı (aynı gruplandırma + kalkanlar), yalnız
// metrik adı değişir.
func TestPodRequestQuery(t *testing.T) {
	q := podRequestQuery("cpu", "^app-", "")
	for _, sub := range []string{
		`kube_pod_container_resource_requests{resource="cpu",pod!="",namespace=~"^app-"}`,
		`sum by (namespace, pod)`,
	} {
		if !strings.Contains(q, sub) {
			t.Fatalf("query %q missing %q", q, sub)
		}
	}
	if strings.Contains(q, "resource_limits") {
		t.Fatal("request query must not touch the limits metric")
	}
}

// Quote/backslash injection in a namespace filter or pod name must
// not be able to break out of the label-matcher string literal.
func TestEscapeLabelValue(t *testing.T) {
	cases := []struct{ in, want string }{
		{`plain`, `plain`},
		{`a"b`, `a\"b`},
		{`a\b`, `a\\b`},
		{`a\"b`, `a\\\"b`},
	}
	for _, c := range cases {
		if got := escapeLabelValue(c.in); got != c.want {
			t.Fatalf("escapeLabelValue(%q) = %q, want %q", c.in, got, c.want)
		}
	}
	q := singlePodCPUQuery("ns", `pod"}[5m])) or vector(1`)
	if strings.Contains(q, `pod"}[5m])) or vector(1"`) {
		t.Fatalf("injection survived unescaped: %s", q)
	}
}

func TestSinglePodQueriesPinBothLabels(t *testing.T) {
	q := singlePodMemQuery("payments", "api-7d9f-x2")
	for _, sub := range []string{`namespace="payments"`, `pod="api-7d9f-x2"`} {
		if !strings.Contains(q, sub) {
			t.Fatalf("query %q missing %q", q, sub)
		}
	}
	if strings.Contains(q, "topk") {
		t.Fatal("single-pod query must not carry topk")
	}
}

func rawPair(t *testing.T, js string) []json.RawMessage {
	t.Helper()
	var pair []json.RawMessage
	if err := json.Unmarshal([]byte(js), &pair); err != nil {
		t.Fatalf("fixture: %v", err)
	}
	return pair
}

func TestSamplePairDecoding(t *testing.T) {
	cases := []struct {
		name   string
		js     string
		wantV  float64
		wantTS int64
		wantOK bool
	}{
		{"normal", `[1784271068.123, "0.25"]`, 0.25, 1784271068, true},
		{"integer ts", `[1784271068, "1073741824"]`, 1 << 30, 1784271068, true},
		{"NaN dropped", `[1784271068, "NaN"]`, 0, 0, false},
		{"+Inf dropped", `[1784271068, "+Inf"]`, 0, 0, false},
		{"non-numeric dropped", `[1784271068, "abc"]`, 0, 0, false},
		{"short pair dropped", `[1784271068]`, 0, 0, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			v, ts, ok := samplePair(rawPair(t, c.js))
			if ok != c.wantOK || v != c.wantV || ts != c.wantTS {
				t.Fatalf("samplePair(%s) = (%v,%v,%v), want (%v,%v,%v)",
					c.js, v, ts, ok, c.wantV, c.wantTS, c.wantOK)
			}
		})
	}
}

// v0.9.50 (design handoff §8) — deployment-kapsamlı trend builder:
// pod regex'i "<deploy>-.*" önekli, deploy adı regex-meta + label
// kaçışlı; byPod topk'siz sum by (pod) (v0.9.3 adım-kayması notu).
func TestDeployTrendQuery(t *testing.T) {
	cases := []struct {
		name string
		q    string
		want []string
	}{
		{"cpu total", deployTrendQuery("payments", "api-gw", "cpu", false),
			[]string{`sum(rate(container_cpu_usage_seconds_total{`, `namespace="payments"`, `pod=~"api-gw-.*"`}},
		{"mem byPod", deployTrendQuery("payments", "api-gw", "mem", true),
			[]string{`sum by (pod) (container_memory_working_set_bytes{`, `pod=~"api-gw-.*"`}},
		{"regex meta kaçışı", deployTrendQuery("ns", "svc.v2", "cpu", false),
			[]string{`pod=~"svc\\.v2-.*"`}},
	}
	for _, c := range cases {
		for _, w := range c.want {
			if !strings.Contains(c.q, w) {
				t.Errorf("%s: %q içinde %q yok", c.name, c.q, w)
			}
		}
	}
	for _, q := range []string{
		deployTrendQuery("ns", "d", "cpu", true),
		deployTrendQuery("ns", "d", "mem", true),
	} {
		if strings.Contains(q, "topk") {
			t.Errorf("byPod sorgusu topk içermemeli (adım-kayması): %q", q)
		}
	}
}

// TestJMXTrendQuery (v0.9.140, auto-discovery v0.9.144) — Service→Infra
// JBoss/JVM JMX sorgu şekli. Selector kube-prometheus/cAdvisor düzlemi
// (operatör 2026-07-21): container=~".*",namespace,pod=~"<deploy>-.*",
// group by pod. Metrik adı HAM (keşfedilmiş); sayaç (_total/_sum) rate.
func TestJMXTrendQuery(t *testing.T) {
	cases := []struct {
		name string
		q    string
		want []string
	}{
		{"gauge byPod", jmxTrendQuery("prod", "app", "jvm_memory_bytes_used", true, ""),
			[]string{`sum by (pod) (jvm_memory_bytes_used{`, `container=~".*"`, `namespace="prod"`, `pod=~"app-.*"`}},
		{"gauge total", jmxTrendQuery("prod", "app", "jvm_threads_current", false, ""),
			[]string{`sum(jvm_threads_current{`, `pod=~"app-.*"`}},
		{"counter _sum rate", jmxTrendQuery("prod", "app", "jvm_gc_collection_seconds_sum", true, ""),
			[]string{`rate(jvm_gc_collection_seconds_sum{`, `[5m])`, `sum by (pod)`}},
		{"counter _total rate", jmxTrendQuery("prod", "app", "jvm_classes_loaded_total", true, ""),
			[]string{`rate(jvm_classes_loaded_total{`, `[5m])`}},
		{"jboss off → data_source+xa grouping", jmxTrendQuery("prod", "app", "jboss_pool_in_use_count", false, ""),
			[]string{`sum by (data_source, xa_data_source) (jboss_pool_in_use_count{`}},
		{"jboss on → pod+data_source+xa grouping", jmxTrendQuery("prod", "app", "jboss_pool_in_use_count", true, ""),
			[]string{`sum by (pod, data_source, xa_data_source) (`}},
		{"jvm → pod grouping", jmxTrendQuery("prod", "app", "jvm_memory_bytes_used", true, ""),
			[]string{`sum by (pod) (`}},
		{"regex meta kaçışı", jmxTrendQuery("ns", "svc.v2", "jvm_threads_current", true, ""),
			[]string{`pod=~"svc\\.v2-.*"`}},
		// v0.9.149 — podFilter dolu: selector deploy-prefix yerine o tek
		// pod'a daralır (Grafana $pod); değer kaçışlanır.
		{"pod filter → tek pod", jmxTrendQuery("prod", "app", "jvm_memory_bytes_used", true, "app-7d9f-x2"),
			[]string{`pod="app-7d9f-x2"`}},
		{"pod filter enjeksiyon kaçışı", jmxTrendQuery("prod", "app", "jvm_threads_current", false, `p"} or vector(1`),
			[]string{`pod="p\"} or vector(1"`}},
	}
	// jmxGrouping: jboss_ regular+XA (data_source+xa_data_source, XA'lar
	// kaybolmasın); jvm_ pod. off/on doğru byClause.
	if b, _ := jmxGrouping("jboss_pool_in_use_count", false); b != "data_source, xa_data_source" {
		t.Errorf("jboss off grouping yanlış: %q", b)
	}
	if b, _ := jmxGrouping("jboss_pool_in_use_count", true); b != "pod, data_source, xa_data_source" {
		t.Errorf("jboss on grouping yanlış: %q", b)
	}
	if b, _ := jmxGrouping("jvm_threads_current", true); b != "pod" {
		t.Errorf("jvm grouping yanlış: %q", b)
	}
	for _, c := range cases {
		for _, w := range c.want {
			if !strings.Contains(c.q, w) {
				t.Errorf("%s: %q içinde %q yok", c.name, c.q, w)
			}
		}
	}
	// jvm_gc..._count GAUGE kalmalı (jboss "_count" gauge'dur, rate DEĞİL).
	if q := jmxTrendQuery("ns", "d", "jboss_pool_in_use_count", true, ""); strings.Contains(q, "rate(") {
		t.Errorf("_count gauge olmalı, rate'lenmemeli: %q", q)
	}
	// Discovery sorgusu __name__ filtresi + selector taşır.
	if d := jmxDiscoveryQuery("prod", "app"); !strings.Contains(d, `count by (__name__)`) ||
		!strings.Contains(d, `__name__=~"(jvm|jboss)_.*"`) || !strings.Contains(d, `pod=~"app-.*"`) {
		t.Errorf("jmxDiscoveryQuery yanlış: %q", d)
	}
	// ValidJMXMetric: yalnız jvm_/jboss_ + [a-z0-9_]; enjeksiyon reddedilir.
	for _, ok := range []string{"jvm_memory_bytes_used", "jboss_pool_in_use_count"} {
		if !ValidJMXMetric(ok) {
			t.Errorf("ValidJMXMetric(%q) true olmalı", ok)
		}
	}
	for _, bad := range []string{"", "cpu", "container_cpu_usage", "jvm_x} or vector(1)", "jvm-dash", "JVM_UPPER", "jvm_x{a=1}"} {
		if ValidJMXMetric(bad) {
			t.Errorf("ValidJMXMetric(%q) false olmalı (enjeksiyon/kapı)", bad)
		}
	}
}

// v0.9.534 — HAProxy router trend sorguları. Şekil operatörün prod
// Grafana panosundan birebir doğrulandı (2026-08-02): code etiketi
// SINIF-şekilli ("2xx"), kapsam exported_namespace + route!="".
// Gecikmede >0 süzgeci pano hilesi (trafiksiz backend 0 basar);
// yanıt oranında >0 BİLİNÇLİ yok — sıfır oran dürüst veridir, seriyi
// pencere ortasında düşürmek sahte boşluk yaratır.
func TestHaproxyTrendQuery(t *testing.T) {
	cases := []struct{ name, ns, kind, want string }{
		{"2xx", "mobile-bff-prod", "2xx",
			`sum by (route) (rate(haproxy_backend_http_responses_total{code="2xx",exported_namespace="mobile-bff-prod",route!=""}[5m]))`},
		{"5xx", "mobile-bff-prod", "5xx",
			`sum by (route) (rate(haproxy_backend_http_responses_total{code="5xx",exported_namespace="mobile-bff-prod",route!=""}[5m]))`},
		{"latency", "callcenter", "latency",
			`avg by (route) (haproxy_backend_http_average_response_latency_milliseconds{exported_namespace="callcenter",route!=""} > 0)`},
		{"bilinmeyen tür gecikmeye düşer", "ns", "garip",
			`avg by (route) (haproxy_backend_http_average_response_latency_milliseconds{exported_namespace="ns",route!=""} > 0)`},
		// Etiket kaçışı: namespace operatör girdisi değil (katalogdan)
		// ama yine de kaçışlı gitmeli — tırnak içeren ad PromQL kırar.
		{"kaçış", `a"b`, "2xx",
			`sum by (route) (rate(haproxy_backend_http_responses_total{code="2xx",exported_namespace="a\"b",route!=""}[5m]))`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := haproxyTrendQuery(c.ns, c.kind); got != c.want {
				t.Errorf("haproxyTrendQuery(%q,%q) =\n  %s\nbeklenen\n  %s", c.ns, c.kind, got, c.want)
			}
		})
	}
}

// v0.9.536 — hedefli pod seçicisi. Operator-reported: envanter topk(500)
// cluster GENELİNDE işliyordu; 0.001 core'luk BFF pod'ları büyük prod
// cluster'ında ilk 500'e giremiyor, istemci eşleştirmesi hiç gelmeyen
// pod'u eşleştiremiyordu ("No pods matched … across 14 Thanos clusters").
// podRe doluyken topk servisin KENDİ pod'ları içinde işler.
func TestPodMatcher(t *testing.T) {
	// Boş = eski davranış BAYT-BAYT: /clusters sayfası değişmemeli.
	if got := podMatcher(""); got != `pod!=""` {
		t.Errorf("boş podRe eski seçiciyi vermeli, got %s", got)
	}
	// Dolu = tam-eşleşen regex (PromQL =~ zaten tam eşler).
	want := `pod=~"(mobile-overview-bff-prod|mobile-overview-bff)-.*"`
	if got := podMatcher(`(mobile-overview-bff-prod|mobile-overview-bff)-.*`); got != want {
		t.Errorf("podMatcher = %s, beklenen %s", got, want)
	}
	// Tırnak kaçışı — değer PromQL string'ine gömülür.
	if got := podMatcher(`a"b`); got != `pod=~"a\"b"` {
		t.Errorf("tırnak kaçışı bozuk: %s", got)
	}
}

func TestPodQueriesCarryPodRe(t *testing.T) {
	const re = `(svc)-.*`
	for name, q := range map[string]string{
		"cpu": podCPUQuery("", re),
		"mem": podMemQuery("", re),
		"lim": podLimitQuery("cpu", "", re),
		"req": podRequestQuery("memory", "", re),
		"net": podNetQuery("receive", "", re),
	} {
		if !strings.Contains(q, `pod=~"(svc)-.*"`) {
			t.Errorf("%s sorgusu podRe taşımıyor: %s", name, q)
		}
		if strings.Contains(q, `pod!=""`) {
			t.Errorf("%s sorgusunda eski seçici kalmış: %s", name, q)
		}
	}
	// podRe boşken beş sorgu da eski seçiciyle.
	for name, q := range map[string]string{
		"cpu": podCPUQuery("", ""), "mem": podMemQuery("", ""),
		"lim": podLimitQuery("cpu", "", ""), "req": podRequestQuery("memory", "", ""),
		"net": podNetQuery("receive", "", ""),
	} {
		if !strings.Contains(q, `pod!=""`) {
			t.Errorf("%s boş podRe'de eski seçiciyi kaybetmiş: %s", name, q)
		}
	}
}

// v0.9.546 — ağ trendi (operatör: "jvm metrikleri yoksa cpu memory
// NETWORK grafikleri gözükebilir"). Ağ, pod envanterinde anlık kolon
// olarak zaten vardı; trend hâli eksikti.
//
// KRİTİK fark: cAdvisor ağ sayaçlarını pod'un ALTYAPI (pause)
// container'ına yazar, yani container adı BOŞTUR. CPU/Mem'in
// container!="" seçicisi kullanılsaydı ağ serisi HİÇ dönmezdi —
// sessizce boş grafik.
func TestDeployTrendQueryNetwork(t *testing.T) {
	in := deployTrendQuery("mobile-bff-prod", "mobile-overview-bff", "netin", true)
	if !strings.Contains(in, "container_network_receive_bytes_total") {
		t.Errorf("netin receive sayacını kullanmalı:\n%s", in)
	}
	if strings.Contains(in, `container!=""`) {
		t.Errorf("ağ seçicisinde container!=\"\" OLMAMALI — pause container'ın adı boş, seri hiç dönmez:\n%s", in)
	}
	out := deployTrendQuery("ns", "dep", "netout", false)
	if !strings.Contains(out, "container_network_transmit_bytes_total") {
		t.Errorf("netout transmit sayacını kullanmalı:\n%s", out)
	}
	// Pod seçicisi ve namespace yine kapsamda.
	if !strings.Contains(in, `namespace="mobile-bff-prod"`) || !strings.Contains(in, `pod=~"mobile-overview-bff-.*"`) {
		t.Errorf("kapsam kaybolmuş:\n%s", in)
	}
	// byPod=true → pod başına grup.
	if !strings.Contains(in, "sum by (pod)") {
		t.Errorf("byPod pod başına gruplamalı:\n%s", in)
	}
	// CPU/Mem dalları BOZULMAMALI (container!="" onlarda kalmalı).
	cpu := deployTrendQuery("ns", "dep", "cpu", false)
	if !strings.Contains(cpu, `container!=""`) {
		t.Errorf("CPU dalı container filtresini kaybetmiş:\n%s", cpu)
	}
}

// v0.9.1276 (Dynatrace-parite #5) — son-sonlanma sorgusu. İki
// sözleşme kilitli:
//  1. `== 1` filtresi — KSM son sebep DIŞINDAKİ reason serilerini de
//     0 DEĞERLE basar; filtre düşerse 0'lı seri "sebep" sanılır ve
//     yanlış rozet basılır (örn. OOMKilled=1 varken Completed=0).
//  2. reason etiketi gruplamada — düşerse sebep adı kaybolur ve
//     birleştirme yapacak bir şey kalmaz.
func TestPodLastTermQuery(t *testing.T) {
	for _, c := range []struct {
		name     string
		query    string
		wantSubs []string
	}{
		{"ns filtresiz", podLastTermQuery("", ""), []string{
			`max by (namespace, pod, reason)`,
			`kube_pod_container_status_last_terminated_reason{pod!=""}`,
			`== 1`,
		}},
		{"ns filtreli", podLastTermQuery("^app-", ""), []string{
			`kube_pod_container_status_last_terminated_reason{pod!="",namespace=~"^app-"}`,
			`== 1`,
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			for _, sub := range c.wantSubs {
				if !strings.Contains(c.query, sub) {
					t.Fatalf("query %q missing %q", c.query, sub)
				}
			}
		})
	}
	// Restart sayacıyla karıştırılmamalı — ayrı metrik, ayrı eksen.
	if strings.Contains(podLastTermQuery("", ""), "restarts_total") {
		t.Fatal("son-sonlanma sorgusu restart sayacına dokunmamalı")
	}
}

// v0.10.135 — konteyner durum sorguları: tek pod TAM eşleşmeli (regex/topk
// yok), reason serileri `== 1` filtreli, etiket değerleri kaçışlı.
func TestContainerStatusQueries(t *testing.T) {
	all := []string{
		containerReadyQuery("pay", "api-1"), containerRestartsQuery("pay", "api-1"),
		containerWaitingQuery("pay", "api-1"), containerLastTermQuery("pay", "api-1"),
	}
	for _, q := range all {
		if !strings.Contains(q, `namespace="pay"`) || !strings.Contains(q, `pod="api-1"`) {
			t.Fatalf("tam eşleşme yok: %s", q)
		}
		if strings.Contains(q, "=~") || strings.Contains(q, "topk") {
			t.Fatalf("tek pod sorgusunda regex/topk olmamalı: %s", q)
		}
	}
	for _, q := range all[2:] {
		if !strings.HasSuffix(q, " == 1") {
			t.Fatalf("reason serisi == 1 filtreli olmalı: %s", q)
		}
	}
	if q := containerReadyQuery(`a"b`, `p\q`); !strings.Contains(q, `namespace="a\"b"`) || !strings.Contains(q, `pod="p\\q"`) {
		t.Fatalf("etiket kaçışı: %s", q)
	}
}

// v0.10.136 — pod adı regex'i: kaçış, çevreleme, 200 tavanı + ilan, boş.
func TestPodNamesRegex(t *testing.T) {
	re, tr := PodNamesRegex([]string{"api-1", "db.0", ""})
	if tr || re != `^(api-1|db\.0)$` {
		t.Fatalf("beklenmeyen: %q truncated=%v", re, tr)
	}
	many := make([]string, 250)
	for i := range many {
		many[i] = "p" + strings.Repeat("a", i%7)
	}
	re, tr = PodNamesRegex(many)
	if !tr || strings.Count(re, "|") != 199 {
		t.Fatalf("200 tavanı: truncated=%v ayraç=%d", tr, strings.Count(re, "|"))
	}
	if re, tr := PodNamesRegex(nil); re != "" || tr {
		t.Fatalf("boş liste: %q %v", re, tr)
	}
}

// v0.10.136 (inceleme) — uzunluk tavanı: 100 × 60 karakterlik ad 4000'i aşar →
// kesilir + ilan; hedefli kipte faz/restart/sebep de podRe taşır.
func TestPodNamesRegexLengthCapAndTargetedStatus(t *testing.T) {
	long := make([]string, 100)
	for i := range long {
		long[i] = strings.Repeat("x", 55) + strings.Repeat("y", i%5)
	}
	re, tr := PodNamesRegex(long)
	if !tr || len(re) > podNamesRegexMaxLen+16 {
		t.Fatalf("uzunluk tavanı: truncated=%v len=%d", tr, len(re))
	}
	for _, q := range []string{podPhaseQuery("", "^(a|b)$"), podRestartsQuery("", "^(a|b)$"), podLastTermQuery("", "^(a|b)$")} {
		if !strings.Contains(q, `pod=~"^(a|b)$"`) || strings.Contains(q, `pod!=""`) {
			t.Fatalf("hedefli durum sorgusu podRe taşımalı: %s", q)
		}
	}
	if q := podPhaseQuery("", ""); !strings.Contains(q, `pod!=""`) {
		t.Fatalf("boş podRe = cluster geneli: %s", q)
	}
}

// v0.10.142 — tek node trendi: topk YOK, instance seçicisi port'a toleranslı ve kaçışlı.
func TestNodeResourceTrendQuery(t *testing.T) {
	q := nodeResourceTrendQuery("cpu", "10.0.1.5")
	if strings.Contains(q, "topk") || !strings.Contains(q, `instance=~"^10\\.0\\.1\\.5(:\\d+)?$"`) || !strings.Contains(q, "sum by (instance)") {
		t.Fatalf("cpu: %s", q)
	}
	m := nodeResourceTrendQuery("mem", "worker-3")
	if strings.Count(m, `instance=~"^worker-3(:\\d+)?$"`) != 2 || !strings.Contains(m, "node_memory_MemAvailable_bytes") {
		t.Fatalf("mem: %s", m)
	}
}
