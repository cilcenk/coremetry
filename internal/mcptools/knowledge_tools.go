package mcptools

// knowledge_tools.go — v0.10.559 (CoSRE v2 Faz 5, docs/audit/cosre-agent-v2.md kabul
// 5: "runbook'ta ne yazıyor?" → bilgi tabanı + kaynak). İki salt-okunur araç:
//
//   get_runbook      — problem / kural / servis / runbook id'den runbook'u çöz ve
//                      İÇERİĞİNİ ver (başlık, açıklama, adımlar; ≤4 k rune). Kural
//                      EnrichProblemsWithRunbooks ile aynı: kural runbookUrl →
//                      self-health → servis metadata. URL bu kurulumun /runbook
//                      sayfasıysa içerik okunur; dış URL ise yalnız adres döner.
//   search_knowledge — LEXICAL arama (embedding şart değil): runbook başlık/
//                      açıklama/etiket/adım + RAG belge parçaları
//                      (TopKRagChunksByContent, hasTokenCaseInsensitive, ≤20).
//                      Her satır kaynak + bağlantı taşır; skor = eşleşen terim
//                      oranı (0-1). Sonuç yoksa dürüst boş.
//
// Bağlam bütçesi: runbook adım metinleri 600 rune, parça 500 rune; en çok 10 satır.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"sort"
	"strings"
	"unicode"

	"github.com/cilcenk/coremetry/internal/chstore"
	"github.com/cilcenk/coremetry/internal/mcp"
)

const (
	runbookBodyMax     = 4000
	runbookStepMax     = 600
	knowledgeSnippet   = 500
	knowledgeMaxRows   = 10
	knowledgeMaxTerms  = 12
	knowledgeMinTermRn = 3
)

var knowledgeStop = map[string]bool{"the": true, "and": true, "for": true, "with": true, "this": true, "that": true, "var": true, "ile": true, "için": true, "bir": true, "olan": true, "nedir": true, "nasıl": true, "neden": true, "runbook": true}

// knowledgeTerms — SAF: küçük harf, harf/rakam dışı ayırıcı, ≥3 rune, stop
// sözcükler ve tekrarlar dışarı, ≤12 terim (chstore.ragQueryTerms ile aynı ruh).
func knowledgeTerms(q string) []string {
	seen := map[string]bool{}
	var out []string
	for _, tok := range strings.FieldsFunc(strings.ToLower(q), func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) }) {
		if len([]rune(tok)) < knowledgeMinTermRn || knowledgeStop[tok] || seen[tok] {
			continue
		}
		seen[tok] = true
		out = append(out, tok)
		if len(out) >= knowledgeMaxTerms {
			break
		}
	}
	return out
}

// runbookText — arama gövdesi: başlık + açıklama + etiketler + adım başlık/talimat.
func runbookText(rb chstore.Runbook) string {
	var b strings.Builder
	b.WriteString(rb.Title)
	b.WriteString("\n")
	b.WriteString(rb.Description)
	b.WriteString("\n")
	b.WriteString(strings.Join(rb.Labels, " "))
	for _, st := range rb.Steps {
		b.WriteString("\n")
		b.WriteString(st.Title)
		b.WriteString("\n")
		b.WriteString(st.Instructions)
	}
	return b.String()
}

// lexicalScore — SAF: eşleşen terim / toplam terim; başlık eşleşmesi +0.25 (tavan 1).
func lexicalScore(terms []string, title, body string) float64 {
	if len(terms) == 0 {
		return 0
	}
	lb, lt := strings.ToLower(body), strings.ToLower(title)
	hit, titleHit := 0, false
	for _, t := range terms {
		if strings.Contains(lb, t) {
			hit++
		}
		if strings.Contains(lt, t) {
			titleHit = true
		}
	}
	if hit == 0 {
		return 0
	}
	s := float64(hit) / float64(len(terms))
	if titleHit {
		s += 0.25
	}
	if s > 1 {
		s = 1
	}
	return s
}

// snippetAround — SAF: ilk eşleşen terimin çevresinden ≤max rune kesit.
func snippetAround(body string, terms []string, max int) string {
	r := []rune(body)
	if len(r) <= max {
		return strings.TrimSpace(body)
	}
	lb := strings.ToLower(body)
	start := 0
	for _, t := range terms {
		if i := strings.Index(lb, t); i >= 0 {
			start = len([]rune(lb[:i])) - max/3
			break
		}
	}
	if start < 0 {
		start = 0
	}
	end := start + max
	if end > len(r) {
		end = len(r)
		start = end - max
	}
	out := strings.TrimSpace(string(r[start:end]))
	if start > 0 {
		out = "…" + out
	}
	if end < len(r) {
		out += "…"
	}
	return out
}

// runbookIDFromURL — SAF: bu kurulumun /runbook?id=… ya da /runbook(s)/<id>
// adresinden id; dış URL → "".
func runbookIDFromURL(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	p := strings.TrimSuffix(u.Path, "/")
	if p != "/runbook" && p != "/runbooks" && !strings.HasPrefix(p, "/runbook/") && !strings.HasPrefix(p, "/runbooks/") {
		return ""
	}
	if id := u.Query().Get("id"); id != "" {
		return id
	}
	if id := u.Query().Get("runbook"); id != "" {
		return id
	}
	if i := strings.LastIndex(p, "/"); i >= 0 && (strings.HasPrefix(p, "/runbook/") || strings.HasPrefix(p, "/runbooks/")) {
		return p[i+1:]
	}
	return ""
}

// runbookView — SAF: içerik zarfı (tavanlı).
func runbookView(rb chstore.Runbook) map[string]any {
	steps := make([]map[string]any, 0, len(rb.Steps))
	for _, st := range rb.Steps {
		row := map[string]any{"order": st.Order, "kind": st.Kind, "title": st.Title}
		if st.Instructions != "" {
			row["instructions"] = truncateRunes(st.Instructions, runbookStepMax)
		}
		if st.Expected != "" {
			row["expected"] = truncateRunes(st.Expected, runbookStepMax)
		}
		steps = append(steps, row)
	}
	return map[string]any{
		"id": rb.ID, "title": rb.Title, "enabled": rb.Enabled, "labels": rb.Labels,
		"description": truncateRunes(rb.Description, runbookBodyMax), "steps": steps, "stepCount": len(rb.Steps),
		"href": "/runbook?id=" + url.QueryEscape(rb.ID), "updatedAt": rb.UpdatedAt,
	}
}

type getRunbookArgs struct {
	RunbookID string `json:"runbook_id,omitempty"`
	ProblemID string `json:"problem_id,omitempty"`
	RuleID    string `json:"rule_id,omitempty"`
	Service   string `json:"service,omitempty"`
}

// resolveRunbookURL — kural → servis metadata (EnrichProblemsWithRunbooks sırası).
func resolveRunbookURL(ctx context.Context, d Deps, ruleID, service string) (u, via string) {
	if ruleID != "" {
		if rules, err := d.Store.ListAlertRules(ctx); err == nil {
			for _, r := range rules {
				if r.ID == ruleID && r.RunbookURL != "" {
					return r.RunbookURL, "rule"
				}
			}
		}
	}
	if service != "" {
		if mds, err := d.Store.ListServiceMetadata(ctx); err == nil {
			if md, ok := mds[service]; ok && md.RunbookURL != "" {
				return md.RunbookURL, "service"
			}
		}
	}
	return "", ""
}

func getRunbookTool(d Deps) mcp.Tool {
	return mcp.Tool{
		Name:             "get_runbook",
		ShortDescription: "Problem/kural/servis/id'den runbook içeriği; dış URL'de adres.",
		Description:      "Resolve and return the runbook for a Problem, an alert rule, a service, or a runbook id — the same precedence the problem drawer uses (rule runbookUrl → service metadata runbookUrl). When the URL points at this install's runbook page the CONTENT is returned (title, description, ordered steps with instructions/expected outcome, labels, href); when it is an external URL only the address is returned (found=true, external=true) — do not invent its contents. Use when the user asks 'what does the runbook say / how do we handle this'. Read-only, one or two lookups. found=false when nothing is attached.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"runbook_id": map[string]any{"type": "string", "description": "Runbook id (from search_knowledge or the /runbooks page)."},
				"problem_id": map[string]any{"type": "string", "description": "Resolve via the problem's rule and service."},
				"rule_id":    map[string]any{"type": "string", "description": "Alert rule id."},
				"service":    map[string]any{"type": "string", "description": "Service name (service metadata runbookUrl)."},
			},
		},
		Handler: func(ctx context.Context, raw json.RawMessage) (any, error) {
			var a getRunbookArgs
			if len(raw) > 0 {
				if err := json.Unmarshal(raw, &a); err != nil {
					return nil, fmt.Errorf("decode args: %w", err)
				}
			}
			id, ruleID, service := strings.TrimSpace(a.RunbookID), strings.TrimSpace(a.RuleID), strings.TrimSpace(a.Service)
			via := "runbook_id"
			if id == "" {
				if pid := strings.TrimSpace(a.ProblemID); pid != "" {
					p, err := d.Store.GetProblem(ctx, pid)
					if err != nil {
						return nil, err
					}
					if p == nil || p.ID == "" {
						return map[string]any{"found": false, "note": "problem not found"}, nil
					}
					ruleID, service = p.RuleID, p.Service
					if p.RunbookURL != "" {
						if rid := runbookIDFromURL(p.RunbookURL); rid != "" {
							id, via = rid, "problem"
						} else {
							return map[string]any{"found": true, "external": true, "url": p.RunbookURL, "via": "problem"}, nil
						}
					}
				}
			}
			if id == "" {
				if ruleID == "" && service == "" {
					return nil, fmt.Errorf("give runbook_id, problem_id, rule_id or service")
				}
				u, v := resolveRunbookURL(ctx, d, ruleID, service)
				if u == "" {
					return map[string]any{"found": false, "ruleId": ruleID, "service": service, "note": "No runbook attached to this rule or service (Alerts → rule runbook URL; Services → metadata)."}, nil
				}
				rid := runbookIDFromURL(u)
				if rid == "" {
					return map[string]any{"found": true, "external": true, "url": u, "via": v}, nil
				}
				id, via = rid, v
			}
			rb, err := d.Store.GetRunbook(ctx, id)
			if err != nil {
				return nil, err
			}
			if rb == nil || rb.ID == "" {
				return map[string]any{"found": false, "runbookId": id, "via": via, "note": "runbook id not found (deleted?)"}, nil
			}
			out := runbookView(*rb)
			out["found"], out["external"], out["via"] = true, false, via
			return out, nil
		},
	}
}

type searchKnowledgeArgs struct {
	Query  string `json:"query"`
	Source string `json:"source,omitempty"`
	Limit  int    `json:"limit,omitempty"`
}

type knowledgeRow struct {
	Source  string  `json:"source"` // runbook | document
	Title   string  `json:"title"`
	Snippet string  `json:"snippet"`
	Href    string  `json:"href,omitempty"`
	ID      string  `json:"id,omitempty"`
	Score   float64 `json:"score"`
}

// rankRunbooks — SAF: lexical skor, azalan, sıfırlar dışarı.
func rankRunbooks(books []chstore.Runbook, terms []string) []knowledgeRow {
	var out []knowledgeRow
	for _, rb := range books {
		if !rb.Enabled {
			continue
		}
		body := runbookText(rb)
		sc := lexicalScore(terms, rb.Title, body)
		if sc == 0 {
			continue
		}
		out = append(out, knowledgeRow{Source: "runbook", Title: rb.Title, ID: rb.ID, Score: sc,
			Snippet: snippetAround(body, terms, knowledgeSnippet), Href: "/runbook?id=" + url.QueryEscape(rb.ID)})
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Score > out[j].Score })
	return out
}

func searchKnowledgeTool(d Deps) mcp.Tool {
	return mcp.Tool{
		Name:             "search_knowledge",
		ShortDescription: "Runbook ve belgelerde lexical arama; kaynak, link, skor döner.",
		Description:      "Lexical (keyword) search across this install's knowledge: runbooks (title, description, labels, step text) and RAG documents (uploaded/URL chunks, token match — no embedding required). Each row carries source (runbook|document), title, a 500-char snippet around the first match, href and a 0-1 score (matched-term ratio; runbook title match +0.25). Use for 'is there a runbook / doc about X' before answering from memory; follow a runbook row with get_runbook(runbook_id). Bounded: ≤10 rows, ≤12 query terms, documents capped at 20 chunks server-side. Empty result means no matching text — say so.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"query":  map[string]any{"type": "string", "description": "Free text; terms ≥3 chars are matched case-insensitively. Required."},
				"source": map[string]any{"type": "string", "enum": []string{"all", "runbook", "document"}, "description": "Restrict to runbooks or documents. Default all."},
				"limit":  map[string]any{"type": "integer", "minimum": 1, "maximum": knowledgeMaxRows, "description": "Rows. Default 5, max 10."},
			},
			"required": []string{"query"},
		},
		Handler: func(ctx context.Context, raw json.RawMessage) (any, error) {
			var a searchKnowledgeArgs
			if len(raw) > 0 {
				if err := json.Unmarshal(raw, &a); err != nil {
					return nil, fmt.Errorf("decode args: %w", err)
				}
			}
			terms := knowledgeTerms(a.Query)
			if len(terms) == 0 {
				return nil, fmt.Errorf("query needs at least one term of 3+ characters")
			}
			src := strings.ToLower(strings.TrimSpace(a.Source))
			if src != "" && src != "all" && src != "runbook" && src != "document" {
				return nil, fmt.Errorf("source must be all | runbook | document")
			}
			limit := clampLimit(a.Limit, 5, knowledgeMaxRows)
			var rows []knowledgeRow
			var notes []string
			if src == "" || src == "all" || src == "runbook" {
				books, err := d.Store.ListRunbooks(ctx)
				if err != nil {
					notes = append(notes, "runbooks: "+err.Error())
				} else {
					rows = append(rows, rankRunbooks(books, terms)...)
				}
			}
			if src == "" || src == "all" || src == "document" {
				hits, err := d.Store.TopKRagChunksByContent(ctx, strings.Join(terms, " "), limit)
				if err != nil {
					notes = append(notes, "documents: "+err.Error())
				}
				for _, h := range hits {
					href := "/settings?tab=rag"
					if h.SourceRef != "" {
						href = h.SourceRef
					}
					rows = append(rows, knowledgeRow{Source: "document", Title: h.DocName, ID: h.DocID, Score: h.Score,
						Snippet: snippetAround(h.Content, terms, knowledgeSnippet), Href: href})
				}
			}
			sort.SliceStable(rows, func(i, j int) bool { return rows[i].Score > rows[j].Score })
			total := len(rows)
			if len(rows) > limit {
				rows = rows[:limit]
			}
			if rows == nil {
				rows = []knowledgeRow{}
			}
			return map[string]any{"query": a.Query, "terms": terms, "rows": rows, "count": len(rows), "total": total, "notes": notes,
				"note": "Lexical match only (no semantic ranking); a runbook row → get_runbook(runbook_id) for full steps."}, nil
		},
	}
}
