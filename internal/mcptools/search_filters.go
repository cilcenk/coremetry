package mcptools

// search_filters.go — v0.10.473 (CoSRE Telemetry Agent Faz 3, F3-2; audit
// G6): search_traces'in attribute SÜZGEÇ + KAPSAM + ZORUNLU KAPI yarısı.
// Saf parçalar burada (test edilir); handler search_traces.go'da.
//
// Kapı (audit §2.3 strateji — CHANNEL_CODE tuzağı): herhangi bir attribute
// süzgeci MV hızlı yolunu kapatır ve ham GROUP BY trace_id'ye düşer; bu
// yüzden (1) süzgeç varsa KAPSAM zorunlu (servis / namespace / cluster),
// (2) pencere: indeksli anahtar (tipli/terfi kolon ya da kvh) ≤ 6 saat,
// indekssiz anahtar ≤ 1 saat, süzgeçsiz eski tavan; (3) süre sıralaması +
// attribute süzgeci ≤ 1 saat (sayfa saçılması), (4) satır ≤ 50 süzgeçli.
// Her kelepçe cevapta İLAN edilir (window_clamped_reason) — sessiz daraltma yok.

import (
	"encoding/json"
	"fmt"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/cilcenk/coremetry/internal/chstore"
)

// SearchFilter — tool'un süzgeç girdisi (LLM dostu op adları).
type SearchFilter struct {
	Key    string   `json:"key"`
	Op     string   `json:"op,omitempty"` // eq | ne | in | contains | prefix | regex | exists | not_exists
	Value  string   `json:"value,omitempty"`
	Values []string `json:"values,omitempty"`
}

const (
	searchWindowIndexedMax   = 6 * 3600
	searchWindowUnindexedMax = 3600
	searchFilteredLimitMax   = 50
)

// toFilterExpr — LLM op'u → FilterExpr op'u. prefix → çapalı regex
// (LIKE %v% alt-dize olduğu için); contains → LIKE. Saf.
func toFilterExpr(f SearchFilter) (chstore.FilterExpr, error) {
	key := strings.TrimSpace(f.Key)
	if key == "" {
		return chstore.FilterExpr{}, fmt.Errorf("filter key boş")
	}
	op := strings.ToLower(strings.TrimSpace(f.Op))
	if op == "" {
		op = "eq"
	}
	vals := f.Values
	if len(vals) == 0 && f.Value != "" {
		vals = []string{f.Value}
	}
	need := func() error {
		if len(vals) == 0 || vals[0] == "" {
			return fmt.Errorf("filter %s (%s): değer gerekli", key, op)
		}
		return nil
	}
	switch op {
	case "eq", "=":
		if err := need(); err != nil {
			return chstore.FilterExpr{}, err
		}
		return chstore.FilterExpr{Key: key, Op: "=", Values: vals[:1]}, nil
	case "ne", "!=":
		if err := need(); err != nil {
			return chstore.FilterExpr{}, err
		}
		return chstore.FilterExpr{Key: key, Op: "!=", Values: vals[:1]}, nil
	case "in":
		if err := need(); err != nil {
			return chstore.FilterExpr{}, err
		}
		return chstore.FilterExpr{Key: key, Op: "IN", Values: vals}, nil
	case "contains", "like":
		if err := need(); err != nil {
			return chstore.FilterExpr{}, err
		}
		return chstore.FilterExpr{Key: key, Op: "LIKE", Values: vals[:1]}, nil
	case "prefix":
		if err := need(); err != nil {
			return chstore.FilterExpr{}, err
		}
		return chstore.FilterExpr{Key: key, Op: "=~", Values: []string{regexp.QuoteMeta(vals[0]) + ".*"}}, nil
	case "regex", "=~":
		if err := need(); err != nil {
			return chstore.FilterExpr{}, err
		}
		if _, err := regexp.Compile(vals[0]); err != nil {
			return chstore.FilterExpr{}, fmt.Errorf("filter %s: geçersiz regex: %v", key, err)
		}
		return chstore.FilterExpr{Key: key, Op: "=~", Values: vals[:1]}, nil
	case "exists":
		return chstore.FilterExpr{Key: key, Op: "EXISTS"}, nil
	case "not_exists":
		return chstore.FilterExpr{Key: key, Op: "NOT EXISTS"}, nil
	}
	return chstore.FilterExpr{}, fmt.Errorf("filter %s: bilinmeyen op %q (eq|ne|in|contains|prefix|regex|exists)", key, f.Op)
}

// filterIndexed — anahtar tipli/terfi kolona düşüyor mu ya da kvh var mı.
func filterIndexed(key string, kvh bool) bool {
	if _, _, ok := chstore.AttrKeyColumn(key); ok {
		return true
	}
	return kvh
}

// searchGate — kapı kararı: pencere kelepçesi (saniye) + gerekçe + limit.
type searchGate struct {
	RangeS int
	Reason string
	Limit  int
}

// applySearchGate — SAF. hasScope: servis/namespace/cluster verildi mi.
func applySearchGate(rangeS, limit int, filters []chstore.FilterExpr, hasScope bool, sortDuration, kvh bool) (searchGate, error) {
	g := searchGate{RangeS: rangeS, Limit: limit}
	if len(filters) == 0 {
		return g, nil
	}
	if !hasScope {
		return g, fmt.Errorf("attribute süzgeci kapsam ister: service, namespace ya da cluster ver (önce resolve_entity) — filo geneli attribute taraması yavaş yol")
	}
	max := searchWindowIndexedMax
	reason := "attribute süzgeci: pencere ≤ 6 saat"
	for _, f := range filters {
		if !filterIndexed(f.Key, kvh) {
			max, reason = searchWindowUnindexedMax, "indekssiz attribute anahtarı ("+f.Key+"): pencere ≤ 1 saat (describe_attributes indexed=false)"
			break
		}
	}
	if sortDuration && max > searchWindowUnindexedMax {
		max, reason = searchWindowUnindexedMax, "süre sıralaması + attribute süzgeci: pencere ≤ 1 saat (sayfa saçılması)"
	}
	if g.RangeS > max {
		g.RangeS, g.Reason = max, reason
	}
	if g.Limit > searchFilteredLimitMax {
		g.Limit = searchFilteredLimitMax
	}
	return g, nil
}

// tracesDeepLink — /traces sözleşmesi (audit §2.4): service, filters JSON,
// hasError=true, sort/order, minMs/maxMs, env, cluster, range=custom:ms-ms.
func tracesDeepLink(f chstore.TraceFilter, from, to time.Time) string {
	q := url.Values{}
	if f.Service != "" {
		q.Set("service", f.Service)
	}
	if f.Search != "" {
		q.Set("search", f.Search)
	}
	if len(f.Filters) > 0 {
		b, _ := json.Marshal(f.Filters)
		q.Set("filters", string(b))
	}
	if f.HasError {
		q.Set("hasError", "true")
	}
	if f.RootOnly {
		q.Set("rootOnly", "true")
	}
	if f.MinMs > 0 {
		q.Set("minMs", strconv.FormatFloat(f.MinMs, 'f', -1, 64))
	}
	if f.MaxMs > 0 {
		q.Set("maxMs", strconv.FormatFloat(f.MaxMs, 'f', -1, 64))
	}
	if f.Env != "" {
		q.Set("env", f.Env)
	}
	if f.Cluster != "" {
		q.Set("cluster", f.Cluster)
	}
	if f.Sort != "" {
		q.Set("sort", f.Sort)
	}
	q.Set("order", "desc")
	q.Set("range", fmt.Sprintf("custom:%d-%d", from.UnixMilli(), to.UnixMilli()))
	return "/traces?" + q.Encode()
}
