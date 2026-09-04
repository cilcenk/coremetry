package chstore

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// trace_identity_first.go — v0.10.342 (operatör: "function_id kolonu benim
// servislerim için bir nevi trace id; Traces arama kutusuna function_id
// yazsam ilgili trace'i getirsin — trace id veya function_id gibi").
//
// Bugünkü arama zaten attr_values dizisini tarıyor, yani değer EŞLEŞİR —
// ama servissiz/geniş pencerede GROUP BY trace_id tam taramasıyla: prod'da
// bütçeye takılır, pencere yarılanır, "boş" görünür. Kimlik değeri ise
// nokta-aramasıdır ve indeksli yolu vardır: terfi kolonu + set(0) (function_id
// 0013, channel_code/function_code) ya da attr_kvh bloom (v0.10.299).
//
// KİMLİK-ÖNCE aday daraltma (trace_error_first.go deseni): arama terimi TEK
// parça ve kimlik gibiyse (≥8 karakter, boşluksuz, joker yok) önce
// span-kapsamlı tüm terfi/facet anahtarlarında EŞİTLİK aranır (tek sorgu,
// OR; hangi anahtarın tuttuğu multiIf ile geri döner), bulunan trace id'leri
// aşama 1/2'nin WHERE'ine `trace_id IN (…)` olarak girer (idx_trace bloom).
// 32-hex terim doğrudan trace id sayılır (sorgu yok). Hiç eşleşme yoksa
// hiçbir şey değişmez: alt-dize araması eskisi gibi koşar — kimlik yolu
// bir hızlandırıcıdır, kapı değil; hatası da listeyi düşürmez (loglanır).

const identityTokenMinLen = 8

// IdentityHit — OUT param (RankedWithin deseni): kimlik yolu denendi mi,
// hangi anahtarlar, hangisi tuttu, kaç trace.
type IdentityHit struct {
	Keys       []string `json:"keys"`
	MatchedKey string   `json:"matchedKey,omitempty"`
	Hits       int      `json:"hits"`
	Bounded    bool     `json:"bounded,omitempty"`
	TraceID    bool     `json:"traceId,omitempty"`
}

// identityToken — SAF: arama terimi kimlik adayı mı. Tek parça, ≥8,
// yalnız [A-Za-z0-9._:-]; joker/tırnak/boşluk → "".
func identityToken(search string) string {
	t := strings.TrimSpace(search)
	if len(t) < identityTokenMinLen || strings.ContainsAny(t, " \t\n\"'*%") {
		return ""
	}
	for _, r := range t {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '.', r == '_', r == ':', r == '-':
		default:
			return ""
		}
	}
	return t
}

func isHex32(s string) bool {
	if len(s) != 32 {
		return false
	}
	for _, r := range s {
		if !((r >= '0' && r <= '9') || (r >= 'a' && r <= 'f') || (r >= 'A' && r <= 'F')) {
			return false
		}
	}
	return true
}

// identityFirstEligible — SAF: kimlik gibi terim + başka id daraltması yok.
func identityFirstEligible(f TraceFilter) bool {
	return identityToken(f.Search) != "" && f.TraceID == "" && len(f.TraceIDs) == 0 && len(f.CandidateIDs) == 0
}

// identityKeys — span-kapsamlı terfi + facet anahtarları (yazımlarıyla),
// tanım sırasında, tekil. Resource kapsamı (k8s.*) kimlik değildir.
func identityKeys() []string {
	seen := map[string]bool{}
	var out []string
	for _, a := range allPromotedAttrs() {
		if a.res {
			continue
		}
		for _, k := range a.keys {
			if !seen[k] {
				seen[k] = true
				out = append(out, k)
			}
		}
	}
	return out
}

// identityFirstQuery — SAF: aday sorgusu. SELECT'te multiIf (hangi anahtar),
// WHERE'de OR; her anahtar FilterExpr.SQL() ile derlenir (terfi kolonu /
// kvh / dizi — filtre çipiyle aynı yol). args: SELECT → WHERE → LIMIT.
func identityFirstQuery(f TraceFilter, keys []string, token, clusterExpr string, budget int) (string, []any, bool) {
	base := errorFirstFilter(f)
	base.Search = ""
	wc := buildGetTracesWhere(base, clusterExpr)
	var parts []string
	var partArgs []any
	var multi []string
	var multiArgs []any
	for _, k := range keys {
		sql, args, err := (FilterExpr{Key: k, Op: "=", Values: []string{token}}).SQL()
		if err != nil || sql == "" {
			continue
		}
		parts = append(parts, "("+sql+")")
		partArgs = append(partArgs, args...)
		multi = append(multi, sql, "'"+strings.ReplaceAll(k, "'", "\\'")+"'")
		multiArgs = append(multiArgs, args...)
	}
	if len(parts) == 0 {
		return "", nil, false
	}
	wc.add("("+strings.Join(parts, " OR ")+")", partArgs...)
	sql := `
		SELECT trace_id, multiIf(` + strings.Join(multi, ", ") + `, '') AS k
		FROM spans ` + wc.sql() + `
		ORDER BY time DESC
		LIMIT ?
		SETTINGS max_execution_time = 10,
		         distributed_product_mode = 'global'`
	args := append(append(append([]any{}, multiArgs...), wc.args...), budget)
	return sql, args, true
}

// identityFirstCandidates — (idler, hit, hata). 32-hex → trace id, sorgusuz.
func (s *Store) identityFirstCandidates(ctx context.Context, f TraceFilter) ([]string, IdentityHit, error) {
	token := identityToken(f.Search)
	hit := IdentityHit{Keys: identityKeys()}
	if isHex32(token) {
		hit.TraceID, hit.MatchedKey, hit.Hits = true, "trace_id", 1
		return []string{strings.ToLower(token)}, hit, nil
	}
	budget := traceStage2MaxIDs * errorFirstOverfetch
	sql, args, ok := identityFirstQuery(f, hit.Keys, token, s.clusterExpr(), budget)
	if !ok {
		return nil, hit, nil
	}
	t0 := time.Now()
	rows, err := s.telemetryReadConn().Query(ctx, sql, args...)
	if err != nil {
		f.Explain.step("identity-first", sql, args, t0, 0, err)
		return nil, hit, fmt.Errorf("identity-first candidates: %w", err)
	}
	defer rows.Close()
	seen := make(map[string]struct{}, 64)
	ids := make([]string, 0, 64)
	keyCount := map[string]int{}
	for rows.Next() {
		var id, k string
		if err := rows.Scan(&id, &k); err != nil {
			return nil, hit, err
		}
		if _, dup := seen[id]; dup {
			continue
		}
		seen[id] = struct{}{}
		ids = append(ids, id)
		keyCount[k]++
		if len(ids) >= traceStage2MaxIDs {
			break
		}
	}
	f.Explain.step("identity-first", sql, args, t0, len(ids), rows.Err())
	if err := rows.Err(); err != nil {
		return nil, hit, err
	}
	hit.Hits = len(ids)
	hit.Bounded = len(ids) >= traceStage2MaxIDs
	best := 0
	for k, n := range keyCount {
		if n > best && k != "" {
			best, hit.MatchedKey = n, k
		}
	}
	return ids, hit, nil
}
