package mcptools

// build_link.go — v0.10.475 (CoSRE Telemetry Agent Faz 3, F3-4; audit G8 +
// kabul 6): UI deep-link üretici. Sözleşme audit §2.4'ten (sayfa → parametre
// adları); model URL uydurmasın, bu tool'a sorsun. Pencere daima
// `range=custom:<fromMs>-<toMs>` (ms) — link tıklandığında AYNI pencere;
// göreli preset yalnız açıkça istenirse. Yalnız uygulama-köklü yol döner.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"

	"github.com/cilcenk/coremetry/internal/chstore"
	"github.com/cilcenk/coremetry/internal/mcp"
)

type buildLinkArgs struct {
	Page       string         `json:"page"`
	Service    string         `json:"service,omitempty"`
	Namespace  string         `json:"namespace,omitempty"`
	Cluster    string         `json:"cluster,omitempty"`
	Workload   string         `json:"workload,omitempty"`
	Pod        string         `json:"pod,omitempty"`
	TraceID    string         `json:"trace_id,omitempty"`
	SpanID     string         `json:"span_id,omitempty"`
	Env        string         `json:"env,omitempty"`
	Search     string         `json:"search,omitempty"`
	Query      string         `json:"query,omitempty"` // logs KQL
	Filters    []SearchFilter `json:"filters,omitempty"`
	ErrorsOnly bool           `json:"errors_only,omitempty"`
	MinMs      float64        `json:"min_ms,omitempty"`
	MaxMs      float64        `json:"max_ms,omitempty"`
	Sort       string         `json:"sort,omitempty"`
	GroupBy    string         `json:"group_by,omitempty"`
	GroupAttr  string         `json:"group_attr,omitempty"`
	Severity   int            `json:"severity,omitempty"` // logs: OTel sayı tabanı (17 = error)
	Tab        string         `json:"tab,omitempty"`
	RangeS     int            `json:"range_s,omitempty"`
	Preset     string         `json:"preset,omitempty"` // "30m" gibi göreli; boş = mutlak pencere
}

var buildLinkPages = map[string]bool{"traces": true, "trace": true, "logs": true, "service": true, "pod": true, "clusters": true, "entity": true, "endpoints": true, "problems": true}

// linkClusterID — cluster ref → Remote Cluster id (yoksa verilen değer).
func linkClusterID(d Deps, ref string) (id, name string) {
	if cs, ok := clustersFor(d, ref); ok && len(cs) == 1 {
		return cs[0].ID, cs[0].Name
	}
	return ref, ref
}

// buildLink — SAF (cluster çözümü çağıranda). Sayfa sözleşmesi (audit §2.4):
//
//	traces:   service, search, filters(JSON), hasError, minMs/maxMs, env, sort/order, view=aggregate+groupBy/groupAttr, range
//	trace:    id, span, tab, range
//	logs:     q, service, cluster, severity, traceId, spanId, filters?, range, env
//	service:  name (kanonik), tab, range, env
//	pod:      cluster(id), namespace, pod, service, range
//	clusters: cluster(id), ns=<id>|<namespace>, section, service, range
//	entity:   id
//	endpoints: service, search, range
//	problems: service (pencere okunmaz)
func buildLink(a buildLinkArgs, clusterID string, filters []chstore.FilterExpr, rangeParam string) (string, error) {
	page := strings.ToLower(strings.TrimSpace(a.Page))
	if !buildLinkPages[page] {
		return "", fmt.Errorf("page %q: traces | trace | logs | service | pod | clusters | entity | endpoints | problems", a.Page)
	}
	q := url.Values{}
	set := func(k, v string) {
		if v != "" {
			q.Set(k, v)
		}
	}
	withRange := true
	path := "/" + page
	switch page {
	case "traces":
		set("service", a.Service)
		set("search", a.Search)
		if len(filters) > 0 {
			b, _ := json.Marshal(filters)
			q.Set("filters", string(b))
		}
		if a.ErrorsOnly {
			q.Set("hasError", "true")
		}
		if a.MinMs > 0 {
			q.Set("minMs", fmt.Sprintf("%g", a.MinMs))
		}
		if a.MaxMs > 0 {
			q.Set("maxMs", fmt.Sprintf("%g", a.MaxMs))
		}
		set("env", a.Env)
		set("cluster", clusterID)
		if a.GroupBy != "" {
			q.Set("view", "aggregate")
			q.Set("groupBy", a.GroupBy)
			set("groupAttr", a.GroupAttr)
		}
		if a.Sort == "duration" || a.Sort == "time" {
			q.Set("sort", a.Sort)
			q.Set("order", "desc")
		}
	case "trace":
		if a.TraceID == "" {
			return "", fmt.Errorf("trace sayfası trace_id ister")
		}
		q.Set("id", a.TraceID)
		set("span", a.SpanID)
		set("tab", a.Tab)
	case "logs":
		set("q", a.Query)
		set("service", a.Service)
		set("cluster", clusterID)
		if a.Severity > 0 {
			q.Set("severity", fmt.Sprintf("%d", a.Severity))
		} else if a.ErrorsOnly {
			q.Set("severity", "17")
		}
		set("traceId", a.TraceID)
		set("spanId", a.SpanID)
		set("env", a.Env)
	case "service":
		if a.Service == "" {
			return "", fmt.Errorf("service sayfası service ister")
		}
		q.Set("name", a.Service)
		set("tab", a.Tab)
		set("env", a.Env)
	case "pod":
		if a.Pod == "" {
			return "", fmt.Errorf("pod sayfası pod ister (cluster + namespace ile)")
		}
		set("cluster", clusterID)
		set("namespace", a.Namespace)
		q.Set("pod", a.Pod)
		set("service", a.Service)
	case "clusters":
		set("cluster", clusterID)
		if a.Namespace != "" && clusterID != "" {
			q.Set("ns", clusterID+"|"+a.Namespace)
		}
		set("section", a.Tab)
		set("service", a.Service)
	case "entity":
		if a.Workload != "" && a.Namespace != "" && clusterID != "" {
			return "", fmt.Errorf("entity id'si tür/kind ister — workload için clusters sayfasını kullan (page=clusters, namespace)")
		}
		return "", fmt.Errorf("entity sayfası entity id ister (pod:<cid>/<ns>/<pod> gibi); list_pods/list_workloads id döndürür")
	case "endpoints":
		set("service", a.Service)
		set("search", a.Search)
	case "problems":
		set("service", a.Service)
		withRange = false
	}
	if withRange && rangeParam != "" {
		q.Set("range", rangeParam)
	}
	enc := q.Encode()
	if enc == "" {
		return path, nil
	}
	return path + "?" + enc, nil
}

var buildLinkPresets = map[string]bool{"5m": true, "15m": true, "30m": true, "1h": true, "3h": true, "6h": true, "12h": true, "24h": true, "2d": true, "7d": true, "30d": true}

func buildLinkTool(d Deps) mcp.Tool {
	return mcp.Tool{
		Name:             "build_link",
		ShortDescription: "Coremetry UI deep-link'i üretir (traces / trace / logs / service / pod / clusters / endpoints / problems) — süzgeç + pencere önceden dolu. Cevabın sonuna KOY; URL uydurma.",
		Description: "Build a Coremetry UI deep-link with the filters, scope and time window pre-filled so the operator continues in the product with one click. " +
			"Pages: traces (service, search, filters[], errors_only, min_ms/max_ms, env, cluster, sort, group_by/group_attr → aggregate view), trace (trace_id, span_id, tab), " +
			"logs (query=KQL, service, cluster, severity or errors_only, trace_id/span_id, env), service (service, tab), pod (cluster, namespace, pod), clusters (cluster, namespace, " +
			"tab=overview|pods|nodes), endpoints (service, search), problems (service). The window is written as range=custom:<fromMs>-<toMs> from range_s (default 1800) — " +
			"or a preset when `preset` is given (5m…30d). Returns a root-relative href; never invent URLs yourself. End every answer with the link.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"page":        map[string]any{"type": "string", "enum": []string{"traces", "trace", "logs", "service", "pod", "clusters", "endpoints", "problems"}},
				"service":     map[string]any{"type": "string"},
				"namespace":   map[string]any{"type": "string"},
				"cluster":     map[string]any{"type": "string", "description": "Remote Cluster id / name / span value."},
				"pod":         map[string]any{"type": "string"},
				"trace_id":    map[string]any{"type": "string"},
				"span_id":     map[string]any{"type": "string"},
				"env":         map[string]any{"type": "string"},
				"search":      map[string]any{"type": "string", "description": "traces/endpoints free text."},
				"query":       map[string]any{"type": "string", "description": "logs KQL."},
				"filters":     map[string]any{"type": "array", "description": "traces: attribute filters as in search_traces.", "items": map[string]any{"type": "object"}},
				"errors_only": map[string]any{"type": "boolean"},
				"min_ms":      map[string]any{"type": "number"},
				"max_ms":      map[string]any{"type": "number"},
				"sort":        map[string]any{"type": "string", "enum": []string{"time", "duration"}},
				"group_by":    map[string]any{"type": "string", "description": "traces aggregate view: operation | service | status | attr."},
				"group_attr":  map[string]any{"type": "string"},
				"severity":    map[string]any{"type": "integer", "description": "logs: OTel severity floor (17 = error)."},
				"tab":         map[string]any{"type": "string", "description": "service: overview|operations|logs|topology|infra|pods; trace: trace|logs; clusters: overview|pods|nodes."},
				"range_s":     map[string]any{"type": "integer", "minimum": 60, "maximum": 2592000, "description": "Window seconds ending at the chat anchor. Default 1800."},
				"preset":      map[string]any{"type": "string", "description": "Relative preset instead of an absolute window: 5m,15m,30m,1h,3h,6h,12h,24h,2d,7d,30d."},
			},
			"required": []string{"page"},
		},
		MinRole: "",
		Handler: func(ctx context.Context, raw json.RawMessage) (any, error) {
			var a buildLinkArgs
			if len(raw) > 0 {
				if err := json.Unmarshal(raw, &a); err != nil {
					return nil, fmt.Errorf("decode args: %w", err)
				}
			}
			var filters []chstore.FilterExpr
			for _, sf := range a.Filters {
				fe, err := toFilterExpr(sf)
				if err != nil {
					return nil, err
				}
				filters = append(filters, fe)
			}
			if a.Namespace != "" && strings.EqualFold(strings.TrimSpace(a.Page), "traces") {
				filters = append(filters, chstore.FilterExpr{Key: "k8s.namespace.name", Op: "=", Values: []string{strings.TrimSpace(a.Namespace)}})
			}
			if err := chstore.ValidateFilters(filters); err != nil {
				return nil, fmt.Errorf("filters: %w", err)
			}
			clusterID := ""
			if c := strings.TrimSpace(a.Cluster); c != "" {
				clusterID, _ = linkClusterID(d, c)
			}
			rangeParam := ""
			if p := strings.TrimSpace(a.Preset); p != "" {
				if !buildLinkPresets[p] {
					return nil, fmt.Errorf("preset %q: 5m,15m,30m,1h,3h,6h,12h,24h,2d,7d,30d", p)
				}
				rangeParam = p
			} else {
				from, to := rangeWindow(ctx, a.RangeS)
				rangeParam = fmt.Sprintf("custom:%d-%d", from.UnixMilli(), to.UnixMilli())
			}
			href, err := buildLink(a, clusterID, filters, rangeParam)
			if err != nil {
				return nil, err
			}
			return map[string]any{"href": href, "page": strings.ToLower(strings.TrimSpace(a.Page)), "range": rangeParam}, nil
		},
	}
}
