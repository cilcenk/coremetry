// traces_extras.go — the /api/traces?traceIds= phase-2-only path + the
// shared extraAttrs parsing (FAZ 2, docs/audit/traces-attribute-columns.md
// §6). Lives OUTSIDE api.go by operator constraint (api.go must not grow;
// the extraAttrs parse moved here from getTraces/exportTracesCSV for a net
// shrink).
//
// No separate route is registered: `traceIds` is a param-branch of the
// existing GET /api/traces contract (the frontend's enrichment call rides
// the same client method family), and Go 1.22's ServeMux panics on a second
// "GET /api/traces" pattern — so getTraces delegates here first via
// serveTracesExtras and the api.go route table is untouched.
//
// Contract:
//
//	GET /api/traces?traceIds=<id,id,…>&extraAttrs=<k,k…>&from=<ns>&to=<ns>
//	→ 200 {"extras": {"<traceId>": {"<key>": "<value>", …}, …}}
//
// When traceIds is present, phase-1 (the list query) is SKIPPED entirely:
// only the bounded phase-2 (Store.TraceExtras — time-bounded WHERE + id
// IN-list + LIMIT) runs. from/to are REQUIRED — they are the phase-2 time
// bound; the client sends the visible rows' real min/max timestamps and the
// store pads `to` by the +5m slack. Without traceIds the request falls
// through to the normal getTraces flow, so the existing API contract
// (optional extraAttrs, extras inside each TraceRow) is unchanged.
package api

import (
	"context"
	"fmt"
	"hash/fnv"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"
)

// tracesExtrasMaxIDs caps the traceIds list. The UI page is 50 rows; 200
// leaves headroom for a denser client without letting a hostile URL inflate
// the IN-list (the CSV-export path never goes through here — its extras run
// inside GetTraces with the export row set).
const tracesExtrasMaxIDs = 200

// parseExtraAttrs extracts the comma-separated attribute-key list requested
// by the /traces UI's column manager. Strict allow-list on characters
// (alphanumeric + . _ -) so even though the value flows into CH as a `?`
// param, no surprises end up in user-visible output. Capped at 8 columns to
// keep the SELECT projection bounded. Shared by getTraces, exportTracesCSV
// and the traceIds path so the three surfaces can't drift.
func parseExtraAttrs(q url.Values) []string {
	raw := q.Get("extraAttrs")
	if raw == "" {
		return nil
	}
	var out []string
	for _, k := range strings.Split(raw, ",") {
		k = strings.TrimSpace(k)
		if k == "" || !isSafeAttrKey(k) {
			continue
		}
		out = append(out, k)
		if len(out) >= 8 {
			break
		}
	}
	return out
}

// isTraceIDHex reports whether s is a full 32-char lowercase-hex trace id —
// the only shape the idx_trace bloom index serves (v0.9.82 precedent).
func isTraceIDHex(s string) bool {
	if len(s) != 32 {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

// tracesExtrasKey builds the cache key for the traceIds path. The id set
// and attr set fold in via a SORTED + FNV-64a digest with NUL separators
// (the v0.5.187 rule — never a length-only digest: two different id sets of
// equal size must never collide), plus the raw from/to strings so a window
// shift misses. Every input the handler reads is in the key.
func tracesExtrasKey(ids, attrs []string, from, to string) string {
	h := fnv.New64a()
	sortedIDs := append([]string(nil), ids...)
	sort.Strings(sortedIDs)
	for _, id := range sortedIDs {
		h.Write([]byte(id))
		h.Write([]byte{0})
	}
	h.Write([]byte{1})
	sortedAttrs := append([]string(nil), attrs...)
	sort.Strings(sortedAttrs)
	for _, a := range sortedAttrs {
		h.Write([]byte(a))
		h.Write([]byte{0})
	}
	return fmt.Sprintf("traces-extras:%x:%s:%s", h.Sum64(), from, to)
}

// serveTracesExtras handles GET /api/traces when the traceIds param is
// present (see the file header for the contract). Returns false — without
// touching the ResponseWriter — when the param is absent so getTraces
// continues with the normal list flow.
func (s *Server) serveTracesExtras(w http.ResponseWriter, r *http.Request) bool {
	q := r.URL.Query()
	raw := strings.TrimSpace(q.Get("traceIds"))
	if raw == "" {
		return false
	}
	attrs := parseExtraAttrs(q)
	if len(attrs) == 0 {
		http.Error(w, "traceIds requires a non-empty extraAttrs list", http.StatusBadRequest)
		return true
	}
	// from/to are the phase-2 time bound — the whole point of FAZ 2 is that
	// this query is never unbounded, so refusing is correct, not pedantic.
	from, to := parseTime(q.Get("from")), parseTime(q.Get("to"))
	if from.IsZero() || to.IsZero() || to.Before(from) {
		http.Error(w, "traceIds requires valid from/to bounds", http.StatusBadRequest)
		return true
	}
	// v0.9.195 review-fix — genişlik clampi: elle yazılmış from=0 tarzı bir
	// URL faz-2'yi retention-genişliğinde koşturabilirdi (tam da FAZ 2'nin
	// kapattığı sınıf). Sessizce kırpıyoruz (400 değil): seyrek veride bir
	// sayfanın satırları meşru olarak günlere yayılabilir; 35g tavanı her
	// UI penceresinin ve retention'ın üstünde, absürt istekleri sınıflar.
	from = clampExtrasFrom(from, to)
	var ids []string
	for _, id := range strings.Split(raw, ",") {
		id = strings.ToLower(strings.TrimSpace(id))
		if !isTraceIDHex(id) {
			continue
		}
		ids = append(ids, id)
		if len(ids) >= tracesExtrasMaxIDs {
			break
		}
	}
	if len(ids) == 0 {
		http.Error(w, "traceIds contains no valid 32-hex trace ids", http.StatusBadRequest)
		return true
	}
	// Short TTL matching the trace-list cache: concurrent operators (or a
	// re-render) asking for the same page+columns coalesce via singleflight.
	key := tracesExtrasKey(ids, attrs, q.Get("from"), q.Get("to"))
	s.serveCached(w, r, key, 20*time.Second, func(ctx context.Context) (any, error) {
		extras, err := s.store.TraceExtras(ctx, ids, attrs, from, to)
		if err != nil {
			return nil, err
		}
		return map[string]any{"extras": extras}, nil
	})
	return true
}

// clampExtrasFrom bounds the phase-2 window width (v0.9.195 review-fix).
// The FE derives from/to from the visible rows' real timestamps — minutes
// wide in practice, days wide at most on a sparse page. A handcrafted
// from=0 URL would otherwise run phase-2 retention-wide, the exact class
// FAZ 2 exists to eliminate. 35 days sits above every UI range and the
// span retention ceiling, so no legitimate caller is affected.
func clampExtrasFrom(from, to time.Time) time.Time {
	const maxExtrasWindow = 35 * 24 * time.Hour
	if floor := to.Add(-maxExtrasWindow); from.Before(floor) {
		return floor
	}
	return from
}
