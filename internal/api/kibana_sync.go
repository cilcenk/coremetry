package api

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/cilcenk/coremetry/internal/auth"
	"github.com/cilcenk/coremetry/internal/chstore"
)

// kibanaSavedSearch — minimal subset of Kibana's saved-search
// export format (v8 .ndjson). Coremetry generates these on
// export and parses them on import; the round-trip is lossy by
// design (Coremetry saved_views are just {name, page,
// queryString}, while Kibana's shape has columns/sort/etc. we
// don't have a use for). v0.5.467.
//
// Fields chosen to match what Kibana's importer accepts —
// missing optional fields default sensibly when uploaded.
type kibanaSavedSearch struct {
	ID         string                 `json:"id"`
	Type       string                 `json:"type"`
	Attributes kibanaSavedAttributes  `json:"attributes"`
	References []any                  `json:"references"`
	Version    string                 `json:"version,omitempty"`
}

type kibanaSavedAttributes struct {
	Title                 string `json:"title"`
	Description           string `json:"description"`
	Columns               []string `json:"columns"`
	Sort                  [][]any  `json:"sort"`
	KibanaSavedObjectMeta map[string]any `json:"kibanaSavedObjectMeta"`
}

// exportSavedViewsToKibana streams an NDJSON file containing all
// the requester's logs-page saved views as Kibana saved
// searches. The KQL query is composed from the view's URL
// queryString: known filter keys (service, traceId, severity)
// become structured KQL clauses; the free-text `search` param
// is dropped in as-is. v0.5.467.
func (s *Server) exportSavedViewsToKibana(w http.ResponseWriter, r *http.Request) {
	claims := auth.FromContext(r.Context())
	ownerID := ""
	if claims != nil {
		ownerID = claims.UserID
	}
	views, err := s.store.ListSavedViews(r.Context(), ownerID, "logs")
	if err != nil {
		writeErr(w, err)
		return
	}
	w.Header().Set("Content-Type", "application/x-ndjson")
	w.Header().Set("Content-Disposition",
		fmt.Sprintf(`attachment; filename="coremetry-logs-views-%s.ndjson"`,
			time.Now().Format("20060102-150405")))
	enc := json.NewEncoder(w)
	for _, v := range views {
		ss := buildKibanaSavedSearch(v)
		if err := enc.Encode(ss); err != nil {
			return
		}
	}
}

// importSavedViewsFromKibana accepts an NDJSON body (a Kibana
// .ndjson export). For each `type:search` document, extracts
// the title + KQL query, creates a Coremetry saved_view scoped
// to /logs. Skips entries it can't parse — surfaces a per-line
// error tally so the operator knows what landed.
//
// Mapping is faithful but lossy: column / sort / filter shape
// from Kibana doesn't survive; only the title + the KQL query
// string become a Coremetry filter.
func (s *Server) importSavedViewsFromKibana(w http.ResponseWriter, r *http.Request) {
	claims := auth.FromContext(r.Context())
	ownerID := ""
	if claims != nil {
		ownerID = claims.UserID
	}
	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "read body: "+err.Error(), http.StatusBadRequest)
		return
	}
	defer r.Body.Close()
	type imported struct {
		Imported int      `json:"imported"`
		Skipped  int      `json:"skipped"`
		Errors   []string `json:"errors,omitempty"`
	}
	out := imported{}
	for _, line := range strings.Split(strings.TrimSpace(string(body)), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var ss kibanaSavedSearch
		if err := json.Unmarshal([]byte(line), &ss); err != nil {
			out.Skipped++
			out.Errors = append(out.Errors, "parse: "+err.Error())
			continue
		}
		if ss.Type != "search" {
			out.Skipped++
			continue
		}
		title := strings.TrimSpace(ss.Attributes.Title)
		if title == "" {
			out.Skipped++
			out.Errors = append(out.Errors, "missing title")
			continue
		}
		kql := extractKibanaKQL(ss)
		// Coremetry queryString — put the whole KQL string in
		// the `search` param. Frontend's /logs renders it
		// faithfully (KQL is the search box's native syntax).
		qs := url.Values{}
		if kql != "" {
			qs.Set("search", kql)
		}
		view := chstore.SavedView{
			OwnerID:     ownerID,
			Name:        title,
			Page:        "logs",
			QueryString: qs.Encode(),
		}
		if err := s.store.UpsertSavedView(r.Context(), view); err != nil {
			out.Skipped++
			out.Errors = append(out.Errors, "save "+title+": "+err.Error())
			continue
		}
		out.Imported++
	}
	s.audit(r, "saved_view.import_kibana", "saved_view", "",
		fmt.Sprintf(`{"imported":%d,"skipped":%d}`, out.Imported, out.Skipped))
	writeJSON(w, out)
}

func buildKibanaSavedSearch(v chstore.SavedView) kibanaSavedSearch {
	kql := buildKQLFromQueryString(v.QueryString)
	source := map[string]any{
		"query":  map[string]any{"language": "kuery", "query": kql},
		"filter": []any{},
	}
	sourceJSON, _ := json.Marshal(source)
	return kibanaSavedSearch{
		ID:   "coremetry-" + shortHexID(8),
		Type: "search",
		Attributes: kibanaSavedAttributes{
			Title:       v.Name,
			Description: "Imported from Coremetry — saved view on /logs",
			Columns:     []string{"_source"},
			Sort:        [][]any{{"@timestamp", "desc"}},
			KibanaSavedObjectMeta: map[string]any{
				"searchSourceJSON": string(sourceJSON),
			},
		},
		References: []any{},
		Version:    "WzEsMV0=", // generic version stamp Kibana accepts
	}
}

// buildKQLFromQueryString maps a Coremetry queryString
// (?service=X&search=Y&severity=N&traceId=Z) onto a single KQL
// expression. Known filter keys become structured clauses;
// free-text `search` slots in as-is so any KQL the operator
// already wrote round-trips intact.
func buildKQLFromQueryString(qs string) string {
	q, err := url.ParseQuery(qs)
	if err != nil {
		return ""
	}
	parts := []string{}
	if svc := q.Get("service"); svc != "" {
		parts = append(parts, `service.name:"`+kqlEscape(svc)+`"`)
	}
	if tid := q.Get("traceId"); tid != "" {
		parts = append(parts, `trace.id:"`+kqlEscape(tid)+`"`)
	}
	if sev := q.Get("severity"); sev != "" && sev != "0" {
		parts = append(parts, `severity_num>=`+sev)
	}
	if search := q.Get("search"); search != "" {
		// Operator's free-form KQL — wrap if it contains AND/OR
		// to keep precedence stable when concatenated.
		if strings.Contains(search, " AND ") || strings.Contains(search, " OR ") {
			parts = append(parts, "("+search+")")
		} else {
			parts = append(parts, search)
		}
	}
	return strings.Join(parts, " AND ")
}

// extractKibanaKQL pulls the KQL query out of a Kibana saved-
// search doc. The query lives in attributes.kibanaSavedObjectMeta
// .searchSourceJSON as a JSON-encoded string inside another
// JSON string — Kibana double-encodes deliberately to keep
// schema migrations simple.
func extractKibanaKQL(ss kibanaSavedSearch) string {
	meta := ss.Attributes.KibanaSavedObjectMeta
	if meta == nil {
		return ""
	}
	rawAny, ok := meta["searchSourceJSON"]
	if !ok {
		return ""
	}
	raw, ok := rawAny.(string)
	if !ok || raw == "" {
		return ""
	}
	var inner struct {
		Query struct {
			Query    string `json:"query"`
			Language string `json:"language"`
		} `json:"query"`
	}
	if err := json.Unmarshal([]byte(raw), &inner); err != nil {
		return ""
	}
	return inner.Query.Query
}

func kqlEscape(s string) string {
	return strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(s)
}

func shortHexID(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
