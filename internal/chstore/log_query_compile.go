package chstore

import (
	"log"
	"strings"
	"sync"

	"github.com/cilcenk/coremetry/internal/logql"
)

// log_query_compile.go — /logs arama metninin logql AST'sinden ClickHouse
// yüklemine derlenmesi (v0.10.279, docs/audit/log-search.md B1).
//
// v0.9.1385'e kadar arama metni gövdede alt-dize olarak aranıyordu; alan
// yazımı (`service.name:"x"`) yapısal olarak 0 satır dönüyordu
// (logstore/query_syntax.go ölçümü). Artık üç CH okuma yolu — liste
// (logsWhere), histogram ve FieldStats (logstore/clickhouse.go) — aynı
// derlenmiş yüklemi kullanır: bir alan adı bir yolda kolona bağlanıp
// ötekinde gövdede aranırsa v0.9.1385'in "dolu histogram, boş tablo"
// çelişkisi geri gelir.
//
// Alan çözümü ES'in expandShorthand takma-ad tablosuyla AYNI kısa adları
// tanır (level/service/trace/span/message/pod/namespace/cluster/host/env);
// tanınmayan alan attr_keys→res_keys sırasıyla res-array lookup'tır
// (FieldStats'ın v0.8.400 sözleşmesi).
//
// Sözdizimi hatasında (kapanmamış tırnak, dengesiz parantez) yüklem eski
// alt-dize aramasına DÜŞER — operatörün yarım yazdığı bir sorgu boş sayfa
// değil, eskisi gibi metin araması görür. Düşüş sessiz değil: sorgu başına
// bir kez loglanır; API katmanı hatayı ayrıca yüzeye çıkarır
// (LogQuerySyntaxError).

// Logs tablosunun cluster zinciri logstore'da da tanımlı (chLogsClusterExpr) — iki tanım ANLAMCA
// aynı kalmak zorunda; logstore tarafındaki TestLogsChainsMatchChstore
// bunu boşluk-normalize ederek pinler.
const LogsClusterChainSQL = `coalesce(
			nullIf(res_values[indexOf(res_keys, 'k8s.cluster.name')], ''),
			nullIf(res_values[indexOf(res_keys, 'openshift.cluster.name')], ''),
			nullIf(res_values[indexOf(res_keys, 'cluster')], ''),
			'')`

// Namespace: identity.go'nun TEK sözlüğü (nsIdentityKeys → namespaceExpr;
// TestNoHandWrittenNamespaceChain üçüncü bir zinciri yasaklar). Histogram
// kırılımının kendi zinciri (logstore chLogsNamespaceExpr) bilinçli olarak
// ayrı kaldı — birleştirme ayrı dilim; v0.10.279 yalnız FİLTRE tarafını
// identity sözlüğüne bağlar.

// LogsEnvChainSQL / LogsPodChainSQL — dışa açık takma adlar (logstore pin
// testi için); tanım repo.go'da.
const (
	LogsEnvChainSQL = logsEnvChainSQL
	LogsPodChainSQL = logsPodChainSQL
)

const logsAttrLookupSQL = `coalesce(
			nullIf(attr_values[indexOf(attr_keys, ?)], ''),
			nullIf(res_values[indexOf(res_keys, ?)], ''),
			'')`

const logsSeverityTextSQL = "if(severity_text != '', severity_text, toString(severity_num))"

// logQueryTarget — logql.Target'ın logs tablosu uygulaması.
type logQueryTarget struct{}

// LogQueryTarget — dışa açık örnek (logstore testleri + api sözdizimi
// denetimi için).
var LogQueryTarget logql.Target = logQueryTarget{}

func (logQueryTarget) Resolve(field string) logql.FieldRef {
	switch strings.ToLower(field) {
	case "service", "service.name", "service_name", "servicename":
		return logql.FieldRef{Kind: logql.FieldString, Expr: "service_name"}
	case "level", "severity", "log.level", "severity_text", "severitytext":
		return logql.FieldRef{Kind: logql.FieldFold, Expr: logsSeverityTextSQL,
			ExistsExpr: "(severity_text != '' OR severity_num != 0)"}
	case "severity_num", "severity_number", "severitynumber":
		return logql.FieldRef{Kind: logql.FieldNumeric, Expr: "severity_num",
			ExistsExpr: "(severity_num != 0)"}
	case "trace", "trace_id", "traceid", "trace.id":
		return logql.FieldRef{Kind: logql.FieldID, Expr: "trace_id"}
	case "span", "span_id", "spanid", "span.id":
		return logql.FieldRef{Kind: logql.FieldID, Expr: "span_id"}
	case "message", "body", "msg", "log.message":
		return logql.FieldRef{Kind: logql.FieldBody, Expr: "body"}
	case "host", "host.name", "hostname", "host_name":
		return logql.FieldRef{Kind: logql.FieldString, Expr: "host_name"}
	case "pod", "k8s.pod.name", "kubernetes.pod.name", "kubernetes.pod_name", "pod_name":
		return logql.FieldRef{Kind: logql.FieldString, Expr: logsPodChainSQL}
	case "namespace", "k8s.namespace.name", "kubernetes.namespace.name", "kubernetes.namespace_name", "kubernetes.namespace":
		return logql.FieldRef{Kind: logql.FieldString, Expr: namespaceExpr()}
	case "cluster", "k8s.cluster.name", "openshift.cluster.name", "kubernetes.cluster.name", "kubernetes.cluster_name":
		return logql.FieldRef{Kind: logql.FieldString, Expr: LogsClusterChainSQL}
	case "env", "environment", "deployment.environment", "deployment.environment.name":
		return logql.FieldRef{Kind: logql.FieldString, Expr: logsEnvChainSQL}
	}
	return logql.FieldRef{
		Kind: logql.FieldString,
		Expr: logsAttrLookupSQL, Args: []any{field, field},
		ExistsExpr: "(has(attr_keys, ?) OR has(res_keys, ?))", ExistsArgs: []any{field, field},
	}
}

// IDColumns — v0.8.521 sözleşmesi: çıplak 32/16-hex serbest metin trace_id /
// span_id kolonlarına da bakar. Hex tanımı TEK yerde (IsBareHexID).
func (logQueryTarget) IDColumns(term string) []string {
	if IsBareHexID(term) {
		return []string{"trace_id", "span_id"}
	}
	return nil
}

// LogQuerySyntaxError — arama metni ayrışmıyorsa hata (API 400 mesajı);
// boş/ayrışan metinde nil.
func LogQuerySyntaxError(search string) error {
	_, err := logql.Parse(search)
	return err
}

var (
	logQueryWarnMu   sync.Mutex
	logQueryWarnSeen = map[string]struct{}{}
)

// LogSearchConjunct — arama metninin WHERE parçası ve argümanları.
// Ayrışmayan metinde eski alt-dize yüklemine düşer (bir kez loglanır).
func LogSearchConjunct(search string) (string, []any) {
	if strings.TrimSpace(search) == "" {
		return "", nil
	}
	e, err := logql.Parse(search)
	if err != nil {
		logQueryWarnMu.Lock()
		if _, seen := logQueryWarnSeen[search]; !seen {
			if len(logQueryWarnSeen) > 256 { // sınırsız büyümesin
				logQueryWarnSeen = map[string]struct{}{}
			}
			logQueryWarnSeen[search] = struct{}{}
			log.Printf("[logs] arama sözdizimi: %v — %q gövdede alt-dize olarak aranıyor", err, search)
		}
		logQueryWarnMu.Unlock()
		return logSearchConjunct(search)
	}
	sql, args := logql.Compile(e, LogQueryTarget)
	if sql == "" {
		return "", nil
	}
	return sql, args
}
