package chstore

import (
	"context"
	"fmt"
	"regexp"
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
// hangi anahtarlar (indeksli), hangileri atlandı (indeks yok → tam tarama
// olurdu), hangisi tuttu, kaç trace, hata.
type IdentityHit struct {
	Keys       []string `json:"keys"`
	Skipped    []string `json:"skipped,omitempty"`
	MatchedKey string   `json:"matchedKey,omitempty"`
	Hits       int      `json:"hits"`
	Bounded    bool     `json:"bounded,omitempty"`
	TraceID    bool     `json:"traceId,omitempty"`
	Error      string   `json:"error,omitempty"`
	// v0.10.344 — kimlik değerinin içindeki zaman (yyyyMMddHHmmss, operatör:
	// function_id "2026 09 04 153319" taşıyor). Varsa arama seçili aralığa
	// değil bu zamanın ±12 saatine bakar (saat dilimi bilinmediği için geniş)
	// ve kimlik bulunamazsa alt-dize taramasına DÜŞÜLMEZ (id'nin zamanı
	// belli, tam tarama yalnız bellek yakar — prod 3.73 GiB olayı).
	AnchorMs     int64 `json:"anchorMs,omitempty"`
	WindowFromNs int64 `json:"windowFromNs,omitempty"`
	WindowToNs   int64 `json:"windowToNs,omitempty"`
}

// identityAnchorRe — 14 haneli yyyyMMddHHmmss (2000-2099).
var identityAnchorRe = regexp.MustCompile(`20\d{2}(0[1-9]|1[0-2])(0[1-9]|[12]\d|3[01])([01]\d|2[0-3])[0-5]\d[0-5]\d`)

// identityAnchor — SAF: kimlik değerinin içindeki ilk geçerli zaman (UTC
// olarak yorumlanır; dilim belirsizliğini pencere genişliği karşılar).
func identityAnchor(token string) (time.Time, bool) {
	m := identityAnchorRe.FindString(token)
	if m == "" {
		return time.Time{}, false
	}
	t, err := time.Parse("20060102150405", m)
	if err != nil {
		return time.Time{}, false
	}
	return t.UTC(), true
}

// identityAnchorHalfWindow — çapanın iki yanı: en uzak saat dilimi ±12 s.
const identityAnchorHalfWindow = 12 * time.Hour

// identityWindow — SAF: çapa varsa ±12 s, yoksa seçili aralık.
func identityWindow(token string, from, to time.Time) (time.Time, time.Time, bool) {
	if a, ok := identityAnchor(token); ok {
		return a.Add(-identityAnchorHalfWindow), a.Add(identityAnchorHalfWindow), true
	}
	return from, to, false
}

// identityStopsFallback — SAF: çapalı kimlik bulunamadıysa alt-dize
// taramasına düşülmez (cevap BOŞ ve dürüst); çapasızda eski davranış.
func identityStopsFallback(hit IdentityHit, hits int) bool {
	return hit.AnchorMs != 0 && hits == 0 && hit.Error == ""
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

// identityKey — bir yazım ve derlenmiş yüklemi. indexed=false → dizi yolu
// (WHERE eşitliği, bellek hafif ama doğrusal tarama): v0.10.344'ten beri
// indekslilerden SONRA ve yalnız çapa/seçili pencerede denenir; `Skipped`
// yalnız hiç derlenemeyeni taşır.
//
// v0.10.343 — Operator-reported (prod 342): kimlik araması bulamadı. İlk
// sürüm tüm yazımları TEK OR'da birleştiriyordu; haritada olmayan yazımlar
// (`FUNCTION_ID`, `CHANNEL_CODE`) dizi yoluna derleniyor, OR indeksi
// öldürüyor, 10 sn'de zaman aşımı → sessizce alt-dize aramasına düşüş →
// tam tarama → boş. Şimdi: yalnız indeksli yüklemler (terfi kolonu set(0)
// ya da attr_kvh bloom), anahtar başına AYRI sorgu, ilk tutan kazanır.
type identityKey struct {
	key     string
	sql     string
	args    []any
	indexed bool
}

// identityKeys — span-kapsamlı terfi + facet yazımları, derlenmiş; aynı
// kolona derlenen yazımlar tekilleşir (function_id/FUNCTION_ID → tek sorgu).
func identityKeys(token string) []identityKey {
	pm := promotedCols()
	kvh := AttrIndexAvailable()
	seenSQL := map[string]bool{}
	var out []identityKey
	for _, a := range allPromotedAttrs() {
		if a.res {
			continue
		}
		for _, k := range a.keys {
			sql, args, err := (FilterExpr{Key: k, Op: "=", Values: []string{token}}).SQL()
			if err != nil || sql == "" || seenSQL[sql] {
				continue
			}
			seenSQL[sql] = true
			_, promoted := pm[k]
			out = append(out, identityKey{key: k, sql: sql, args: args, indexed: promoted || kvh})
		}
	}
	return out
}

// identityFirstQuery — SAF: TEK anahtarın aday sorgusu (pencere + servis +
// env/cluster + eşitlik; çip/arama/hata sızmaz). args: WHERE → LIMIT.
// from/to: çapalı pencere ya da seçili aralık (identityWindow).
func identityFirstQuery(f TraceFilter, k identityKey, clusterExpr string, budget int, from, to time.Time) (string, []any) {
	base := errorFirstFilter(f)
	base.Search = ""
	base.From, base.To = from, to
	wc := buildGetTracesWhere(base, clusterExpr)
	wc.add(k.sql, k.args...)
	sql := `
		SELECT trace_id
		FROM spans ` + wc.sql() + `
		ORDER BY time DESC
		LIMIT ?
		SETTINGS max_execution_time = 5,
		         distributed_product_mode = 'global'`
	return sql, append(append([]any{}, wc.args...), budget)
}

// identityFirstCandidates — (idler, hit, hata). 32-hex → trace id, sorgusuz.
// Anahtarlar sırayla; ilk tutan kazanır. Hata bir anahtarda olursa kalanlar
// denenmez (aynı bütçeyi yakmasın), hit.Error söyler.
func (s *Store) identityFirstCandidates(ctx context.Context, f TraceFilter) ([]string, IdentityHit, error) {
	token := identityToken(f.Search)
	hit := IdentityHit{Keys: []string{}}
	if isHex32(token) {
		hit.TraceID, hit.MatchedKey, hit.Hits = true, "trace_id", 1
		hit.Keys = []string{"trace_id"}
		return []string{strings.ToLower(token)}, hit, nil
	}
	budget := traceStage2MaxIDs * errorFirstOverfetch
	from, to, anchored := identityWindow(token, f.From, f.To)
	if anchored {
		a, _ := identityAnchor(token)
		hit.AnchorMs = a.UnixMilli()
	}
	hit.WindowFromNs, hit.WindowToNs = from.UnixNano(), to.UnixNano()
	// İndeksliler önce (ucuz), dizi yolu sonra (doğrusal ama bellek hafif —
	// çip yolunun bugün yaptığı tarama). v0.10.344.
	keys := identityKeys(token)
	ordered := make([]identityKey, 0, len(keys))
	for _, k := range keys {
		if k.indexed {
			ordered = append(ordered, k)
		}
	}
	for _, k := range keys {
		if !k.indexed {
			ordered = append(ordered, k)
		}
	}
	for _, k := range ordered {
		hit.Keys = append(hit.Keys, k.key)
		sql, args := identityFirstQuery(f, k, s.clusterExpr(), budget, from, to)
		t0 := time.Now()
		rows, err := s.telemetryReadConn().Query(ctx, sql, args...)
		if err != nil {
			f.Explain.step("identity-first:"+k.key, sql, args, t0, 0, err)
			hit.Error = k.key + ": " + err.Error()
			return nil, hit, fmt.Errorf("identity-first %s: %w", k.key, err)
		}
		seen := make(map[string]struct{}, 64)
		ids := make([]string, 0, 64)
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				rows.Close()
				return nil, hit, err
			}
			if _, dup := seen[id]; dup {
				continue
			}
			seen[id] = struct{}{}
			ids = append(ids, id)
			if len(ids) >= traceStage2MaxIDs {
				break
			}
		}
		rerr := rows.Err()
		rows.Close()
		f.Explain.step("identity-first:"+k.key, sql, args, t0, len(ids), rerr)
		if rerr != nil {
			hit.Error = k.key + ": " + rerr.Error()
			return nil, hit, rerr
		}
		if len(ids) > 0 {
			hit.MatchedKey, hit.Hits, hit.Bounded = k.key, len(ids), len(ids) >= traceStage2MaxIDs
			return ids, hit, nil
		}
	}
	return nil, hit, nil
}
