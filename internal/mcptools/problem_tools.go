package mcptools

// problem_tools.go — v0.10.555 (CoSRE v2 Faz 4a, docs/audit/cosre-agent-v2.md
// §Faz 4 madde 2-3): problem-tarafı sinyal araçları + yetenek probu.
//
//   get_problem              — tekil Problem (çekmece paritesi), açıklama kırpılı
//   get_correlation_evidence — hipotezin DeepEvidence ALT alanları bölüm bölüm
//                              (bugüne dek tek JSON kolonu, alt-sorgulanmıyordu)
//   similar_problems         — aynı (servis, kural) anahtarında çözülmüş geçmiş
//                              (FindSimilarResolvedProblems; uç/tool yoktu)
//   get_capabilities         — entity / rollouts / Thanos / metrik deposu / log
//                              deposu / RAG / model — agent ucuz yola düşsün
//
// Hepsi salt-okunur, tek ön-hesaplanmış okuma; viewer tabanı. Kanıt listeleri
// bölüm başına tavanlı (context penceresi), bilinmeyen bölüm HATA (sessiz boş
// yok).

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/cilcenk/coremetry/internal/chstore"
	"github.com/cilcenk/coremetry/internal/mcp"
)

const (
	problemDescriptionMax = 600
	evidenceSectionCap    = 10
	similarProblemsMax    = 20
)

type getProblemArgs struct {
	ProblemID string `json:"problem_id"`
}

// truncateRunes — bağlam bütçesi; kırpıldıysa "…" ekler.
func truncateRunes(s string, max int) string {
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max]) + "…"
}

// problemView — get_problem / similar_problems ortak zarf satırı (sınırlı).
func problemView(p chstore.Problem) map[string]any {
	out := map[string]any{
		"id": p.ID, "ruleId": p.RuleID, "ruleName": p.RuleName, "severity": p.Severity,
		"service": p.Service, "kind": p.Kind, "metric": p.Metric, "value": p.Value,
		"threshold": p.Threshold, "status": p.Status, "startedAt": p.StartedAt,
		"description": truncateRunes(p.Description, problemDescriptionMax),
	}
	if p.Comparator != "" {
		out["comparator"] = p.Comparator
	}
	if p.Pod != "" {
		out["pod"] = p.Pod
	}
	if p.Assignee != "" {
		out["assignee"] = p.Assignee
	}
	if p.ResolvedAt != nil {
		out["resolvedAt"] = *p.ResolvedAt
		if *p.ResolvedAt > p.StartedAt {
			out["durationS"] = (*p.ResolvedAt - p.StartedAt) / 1e9
		}
	}
	if p.RunbookURL != "" {
		out["runbookUrl"] = p.RunbookURL
	}
	if len(p.Clusters) > 0 {
		out["clusters"] = p.Clusters
	}
	if p.OwnerTeam != "" {
		out["ownerTeam"] = p.OwnerTeam
	}
	if p.SRETeam != "" {
		out["sreTeam"] = p.SRETeam
	}
	if p.RecentDeploy != nil {
		out["recentDeploy"] = p.RecentDeploy
	}
	return out
}

func getProblemTool(d Deps) mcp.Tool {
	return mcp.Tool{
		Name:             "get_problem",
		ShortDescription: "Tek Problem'in tam kaydı (id): özne, ölçü/eşik, durum, atanan, süre, deploy.",
		Description:      "Return ONE Problem by id with every field the problem drawer shows: subject (service or DB subject + kind), rule, metric/value/threshold/comparator, status, pod, assignee, startedAt/resolvedAt (+durationS when resolved), runbook URL, clusters, owner/SRE teams and the recent deploy the opener recorded. Use after list_problems (or when the user pastes a problem id / link) before get_problem_root_cause or get_correlation_evidence. Read-only, one lookup. Returns found=false when the id does not exist. The description is truncated to 600 characters.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"problem_id": map[string]any{"type": "string", "description": "The Problem id (the 'id' field from list_problems). Required."},
			},
			"required": []string{"problem_id"},
		},
		Handler: func(ctx context.Context, raw json.RawMessage) (any, error) {
			var a getProblemArgs
			if len(raw) > 0 {
				if err := json.Unmarshal(raw, &a); err != nil {
					return nil, fmt.Errorf("decode args: %w", err)
				}
			}
			a.ProblemID = strings.TrimSpace(a.ProblemID)
			if a.ProblemID == "" {
				return nil, fmt.Errorf("problem_id is required")
			}
			p, err := d.Store.GetProblem(ctx, a.ProblemID)
			if err != nil {
				return nil, err
			}
			if p == nil || p.ID == "" {
				return map[string]any{"problemId": a.ProblemID, "found": false, "note": "No problem with this id — it may have been purged, or the id came from another install."}, nil
			}
			out := problemView(*p)
			out["found"] = true
			if h, herr := d.Store.GetHypothesis(ctx, "problem", p.ID); herr == nil && h != nil {
				out["hypothesisComputed"] = true
				out["topSuspect"] = h.TopSuspect
				out["confidence"] = h.Confidence
			} else {
				out["hypothesisComputed"] = false
			}
			return out, nil
		},
	}
}

// evidenceSections — get_correlation_evidence bölümleri (enum; sıra sabit).
var evidenceSections = []string{"checked", "exceptions", "templates", "slowOps", "business", "external", "traceIds", "affectedPods", "logSignatures", "rollouts", "heap", "gcPause", "runtime"}

type getCorrelationEvidenceArgs struct {
	ProblemID string   `json:"problem_id"`
	Sections  []string `json:"sections,omitempty"`
}

// capSlice — bölüm tavanı; kırpıldıysa toplamı da söyler.
func capSlice[T any](in []T) ([]T, int) {
	if len(in) > evidenceSectionCap {
		return in[:evidenceSectionCap], len(in)
	}
	return in, len(in)
}

// evidenceSection — SAF: bir bölümün (kırpılmış) değeri + toplam sayısı. ok=false
// = bilinmeyen bölüm adı.
func evidenceSection(de *chstore.DeepEvidence, name string) (val any, total int, ok bool) {
	if de == nil {
		de = &chstore.DeepEvidence{}
	}
	switch name {
	case "checked":
		v, n := capSlice(de.Checked)
		return v, n, true
	case "exceptions":
		v, n := capSlice(de.Exceptions)
		return v, n, true
	case "templates":
		v, n := capSlice(de.Templates)
		return v, n, true
	case "slowOps":
		v, n := capSlice(de.SlowOps)
		return v, n, true
	case "business":
		if de.Business == nil {
			return map[string][]chstore.BusinessSlice{}, 0, true
		}
		return de.Business, len(de.Business), true
	case "external":
		if de.External == nil {
			return nil, 0, true
		}
		return de.External, 1, true
	case "traceIds":
		v, n := capSlice(de.TraceIDs)
		return v, n, true
	case "affectedPods":
		v, n := capSlice(de.AffectedPods)
		return v, n, true
	case "logSignatures":
		v, n := capSlice(de.LogSignatures)
		return v, n, true
	case "rollouts":
		v, n := capSlice(de.Rollouts)
		return v, n, true
	case "heap":
		v, n := capSlice(de.Heap)
		return v, n, true
	case "gcPause":
		v, n := capSlice(de.GCPause)
		return v, n, true
	case "runtime":
		if de.Runtime == nil {
			return nil, 0, true
		}
		return de.Runtime, 1, true
	}
	return nil, 0, false
}

func getCorrelationEvidenceTool(d Deps) mcp.Tool {
	return mcp.Tool{
		Name:             "get_correlation_evidence",
		ShortDescription: "Problem hipotezinin kanıt bölümleri (checked, exceptions, traceIds, rollouts…).",
		Description:      "Return the correlation worker's DEEP EVIDENCE for a Problem, section by section, so you can cite exactly what was inspected instead of re-querying: `checked` (every signal family the worker looked at, found=true/false — found=false is NOT evidence for a cause), `exceptions` (new/spiking exception groups), `templates` (log templates that changed), `slowOps` (operations that slowed), `business` (breakdown by channel/function code), `external` (external-source anomaly, e.g. an Oracle error-table source, with trace pivots), `traceIds` (exemplar traces), `affectedPods`, `logSignatures`, `rollouts` (K8s rollouts near the onset with match reason), `heap`/`gcPause`/`runtime` (JVM). Use after get_problem_root_cause when you need the underlying facts, or to build an evidence block for the user. Each list is capped at 10 items; `totals` carries the uncapped counts. computed=false when no hypothesis exists yet; sections is optional (default: all non-empty).",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"problem_id": map[string]any{"type": "string", "description": "The Problem id. Required."},
				"sections": map[string]any{
					"type":        "array",
					"items":       map[string]any{"type": "string", "enum": evidenceSections},
					"description": "Which evidence sections to return. Omit for all non-empty sections. Unknown names are an error.",
				},
			},
			"required": []string{"problem_id"},
		},
		Handler: func(ctx context.Context, raw json.RawMessage) (any, error) {
			var a getCorrelationEvidenceArgs
			if len(raw) > 0 {
				if err := json.Unmarshal(raw, &a); err != nil {
					return nil, fmt.Errorf("decode args: %w", err)
				}
			}
			a.ProblemID = strings.TrimSpace(a.ProblemID)
			if a.ProblemID == "" {
				return nil, fmt.Errorf("problem_id is required")
			}
			for _, sname := range a.Sections {
				if _, _, ok := evidenceSection(nil, sname); !ok {
					return nil, fmt.Errorf("unknown section %q (known: %s)", sname, strings.Join(evidenceSections, ", "))
				}
			}
			h, err := d.Store.GetHypothesis(ctx, "problem", a.ProblemID)
			if err != nil {
				return nil, err
			}
			if h == nil {
				return map[string]any{"problemId": a.ProblemID, "computed": false, "note": "No hypothesis synthesized yet — the correlator runs shortly after a problem opens; retry later."}, nil
			}
			want := a.Sections
			all := len(want) == 0
			if all {
				want = evidenceSections
			}
			sections := map[string]any{}
			totals := map[string]int{}
			for _, sname := range want {
				v, n, _ := evidenceSection(h.Deep, sname)
				if all && n == 0 {
					continue
				}
				sections[sname] = v
				totals[sname] = n
			}
			return map[string]any{
				"problemId": a.ProblemID, "computed": true, "service": h.Service,
				"topSuspect": h.TopSuspect, "confidence": h.Confidence, "computedAt": h.ComputedAt,
				"deep": h.Deep != nil, "sections": sections, "totals": totals,
				"sectionCap": evidenceSectionCap,
			}, nil
		},
	}
}

type similarProblemsArgs struct {
	ProblemID string `json:"problem_id,omitempty"`
	Service   string `json:"service,omitempty"`
	RuleID    string `json:"rule_id,omitempty"`
	Limit     int    `json:"limit,omitempty"`
}

func similarProblemsTool(d Deps) mcp.Tool {
	return mcp.Tool{
		Name:             "similar_problems",
		ShortDescription: "Aynı servis+kural anahtarında çözülmüş geçmiş problemler.",
		Description:      "List past RESOLVED problems with the same symptom key — (service, rule) — newest first, so you can answer 'has this happened before, how long did it last, who handled it'. Give either problem_id (the key is read from that problem) or service + rule_id. Each row carries startedAt/resolvedAt/durationS, assignee, teams and the recent deploy recorded at the time; use get_problem_root_cause on a past id for its cause. Read-only, one lookup; limit default 5, max 20. Note: the key is exact (same service AND same rule) — a different rule on the same service is not 'similar' here.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"problem_id": map[string]any{"type": "string", "description": "A Problem id; its (service, rule) key is used. Optional when service + rule_id are given."},
				"service":    map[string]any{"type": "string", "description": "Subject (service name or DB subject id). Used with rule_id when problem_id is omitted."},
				"rule_id":    map[string]any{"type": "string", "description": "Rule id (the 'ruleId' field of a problem)."},
				"limit":      map[string]any{"type": "integer", "minimum": 1, "maximum": similarProblemsMax, "description": "Rows. Default 5, max 20."},
			},
		},
		Handler: func(ctx context.Context, raw json.RawMessage) (any, error) {
			var a similarProblemsArgs
			if len(raw) > 0 {
				if err := json.Unmarshal(raw, &a); err != nil {
					return nil, fmt.Errorf("decode args: %w", err)
				}
			}
			service, ruleID := strings.TrimSpace(a.Service), strings.TrimSpace(a.RuleID)
			anchor := strings.TrimSpace(a.ProblemID)
			if anchor != "" {
				p, err := d.Store.GetProblem(ctx, anchor)
				if err != nil {
					return nil, err
				}
				if p == nil || p.ID == "" {
					return map[string]any{"problemId": anchor, "found": false, "rows": []any{}, "count": 0}, nil
				}
				service, ruleID = p.Service, p.RuleID
			}
			if service == "" || ruleID == "" {
				return nil, fmt.Errorf("give problem_id, or service and rule_id")
			}
			limit := clampLimit(a.Limit, 5, similarProblemsMax)
			rows, err := d.Store.FindSimilarResolvedProblems(ctx, service, ruleID, limit)
			if err != nil {
				return nil, err
			}
			out := make([]map[string]any, 0, len(rows))
			for _, p := range rows {
				if anchor != "" && p.ID == anchor {
					continue
				}
				out = append(out, problemView(p))
			}
			return map[string]any{
				"service": service, "ruleId": ruleID, "rows": out, "count": len(out), "limit": limit,
				"note": "Key is exact (service AND rule); only status=resolved rows.",
			}, nil
		},
	}
}

// Capability — get_capabilities satırı. Enabled=false ise Reason okunur.
type Capability struct {
	Enabled bool   `json:"enabled"`
	Detail  string `json:"detail,omitempty"`
	Reason  string `json:"reason,omitempty"`
}

// capabilitiesFrom — SAF: Deps'teki kapanışlardan yetenek haritası. nil kapanış =
// bilinmiyor (ilan edilir; sessizce "kapalı" denmez).
func capabilitiesFrom(d Deps) map[string]Capability {
	caps := map[string]Capability{}
	if d.EntityEnabled != nil {
		if d.EntityEnabled() {
			caps["entity"] = Capability{Enabled: true, Detail: "list_namespaces / list_workloads / list_pods / resolve_entity"}
		} else {
			caps["entity"] = Capability{Enabled: false, Reason: "entity layer flag off (Settings → Entities)"}
		}
	} else {
		caps["entity"] = Capability{Enabled: false, Reason: "unknown (no probe wired)"}
	}
	if d.RolloutsEnabled != nil {
		if d.RolloutsEnabled() {
			caps["rollouts"] = Capability{Enabled: true, Detail: "list_deployments includes K8s rollouts"}
		} else {
			caps["rollouts"] = Capability{Enabled: false, Reason: "rollouts flag off (Settings → Rollouts) — list_deployments returns inferred deploys only"}
		}
	} else {
		caps["rollouts"] = Capability{Enabled: false, Reason: "unknown (no probe wired)"}
	}
	if d.Clusters != nil {
		refs := d.Clusters()
		names := make([]string, 0, len(refs))
		for _, r := range refs {
			names = append(names, r.Name)
		}
		sort.Strings(names)
		if len(refs) > 0 {
			caps["thanos"] = Capability{Enabled: true, Detail: fmt.Sprintf("%d remote cluster(s): %s", len(refs), strings.Join(names, ", "))}
		} else {
			caps["thanos"] = Capability{Enabled: false, Reason: "no enabled Remote Cluster (Settings → Remote clusters) — pod/node/cluster metrics unavailable"}
		}
	} else {
		caps["thanos"] = Capability{Enabled: false, Reason: "unknown (no probe wired)"}
	}
	if d.MetricsName != nil {
		n := d.MetricsName()
		caps["metrics"] = Capability{Enabled: true, Detail: "query_metric / list_metric_names read " + n}
		caps["victoriametrics"] = Capability{Enabled: n == "vm", Detail: "metric source: " + n}
	} else {
		caps["metrics"] = Capability{Enabled: true, Detail: "query_metric reads ClickHouse metric_points (no VM probe wired)"}
	}
	if d.LogStore != nil {
		caps["logs"] = Capability{Enabled: true, Detail: "backend " + d.LogStore.Backend()}
	} else {
		caps["logs"] = Capability{Enabled: false, Reason: "no log store"}
	}
	if d.RAGReady != nil {
		if d.RAGReady() {
			caps["rag"] = Capability{Enabled: true, Detail: "document RAG ready (search_knowledge planned)"}
		} else {
			caps["rag"] = Capability{Enabled: false, Reason: "RAG not configured or index not ready"}
		}
	} else {
		caps["rag"] = Capability{Enabled: false, Reason: "unknown (no probe wired)"}
	}
	if d.CopilotModel != nil {
		if m := d.CopilotModel(); m != "" {
			caps["copilot"] = Capability{Enabled: true, Detail: "model " + m}
		} else {
			caps["copilot"] = Capability{Enabled: false, Reason: "AI provider not configured"}
		}
	}
	caps["chatContext"] = Capability{Enabled: d.CtxGet != nil, Reason: map[bool]string{true: "", false: "no conversation (external MCP client)"}[d.CtxGet != nil]}
	return caps
}

func getCapabilitiesTool(d Deps) mcp.Tool {
	return mcp.Tool{
		Name:             "get_capabilities",
		ShortDescription: "Açık katmanlar (entity, rollouts, Thanos, VM, log, RAG, model); kapalıyı çağırma.",
		Description:      "Return which optional signal layers are enabled on THIS install so you can pick tools that will answer instead of returning disabled/empty: `entity` (namespace/workload/pod catalogue), `rollouts` (K8s rollout history in list_deployments), `thanos` (remote clusters → pod/node/cluster metrics), `metrics` + `victoriametrics` (which store query_metric reads), `logs` (backend), `rag` (document knowledge), `copilot` (model), `chatContext` (conversation memory available). Each entry has enabled plus a reason when disabled or unknown. Zero cost (no query) — call once at the start of an investigation, not per step.",
		InputSchema:      map[string]any{"type": "object", "properties": map[string]any{}},
		Handler: func(ctx context.Context, raw json.RawMessage) (any, error) {
			caps := capabilitiesFrom(d)
			enabled := make([]string, 0, len(caps))
			disabled := make([]string, 0, len(caps))
			for k, c := range caps {
				if c.Enabled {
					enabled = append(enabled, k)
				} else {
					disabled = append(disabled, k)
				}
			}
			sort.Strings(enabled)
			sort.Strings(disabled)
			return map[string]any{"capabilities": caps, "enabled": enabled, "disabled": disabled}, nil
		},
	}
}
