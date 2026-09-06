package chstore

// attr_discovery.go — v0.10.472 (CoSRE Telemetry Agent Faz 3, F3-1; audit G4/G5):
// asistanın "hangi attribute anahtarı bu değeri taşıyor?" sorusu için iki
// okuma. Model anahtar UYDURMASIN: önce kapsamlı örneklem (5000 span; anahtar
// + örnek değerler), sonra değer probu — tipli/terfi kolon varsa eşitlik,
// dizi yolunda kvh bloom (v0.10.300) varsa hash+eşitlik; ikisi de yoksa
// yalnız örneklem kanıtı (cevap "sample" der, kesin sayı iddia etmez).
// Her sorgu zaman sınırlı + LIMIT + max_execution_time.

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// AttrScope — örneklem/prob kapsamı; en az bir alan dolu olmalı (filo geneli
// dizi taraması bilerek yok).
type AttrScope struct {
	Service   string
	Namespace string   // k8s_namespace terfi kolonu
	Clusters  []string // span `cluster` değerleri
}

func (s AttrScope) empty() bool { return s.Service == "" && s.Namespace == "" && len(s.Clusters) == 0 }

// scopeWhere — "AND …" parçası + argümanlar (zaman ayrı). Saf.
func scopeWhere(s AttrScope) (string, []any) {
	var parts []string
	var args []any
	if s.Service != "" {
		parts = append(parts, "service_name = ?")
		args = append(args, s.Service)
	}
	if s.Namespace != "" {
		parts = append(parts, "k8s_namespace = ?")
		args = append(args, s.Namespace)
	}
	if len(s.Clusters) > 0 {
		parts = append(parts, "cluster IN (?)")
		args = append(args, s.Clusters)
	}
	if len(parts) == 0 {
		return "", nil
	}
	return " AND " + strings.Join(parts, " AND "), args
}

// scopedAttrsSQL — GetServiceAttrs'ın kapsam-genel şekli. Saf; tablo-testli.
//
// v0.10.488 (Astra bulgusu #3): LIMIT artık SPAN sayar — en içteki alt sorgu
// önce N span seçer, arrayJoin ONUN üstünde koşar. Eski şekil arrayJoin'den
// SONRA LIMIT koyuyordu: 5000 "span" aslında 5000 (anahtar,değer) çiftiydi
// (~125-250 span) ve API bunu "5000 span" diye ilan ediyordu. Sıra: servis
// kapsamında ORDER BY time DESC (PK: service_name, time — ucuz, gerçekten
// "en yeni"); namespace/cluster kapsamında sıralama yok (tarama sırası;
// çağıran sample_order="unordered" der).
func scopedAttrsSQL(keysCol, valsCol string, scope AttrScope) (string, error) {
	w, _ := scopeWhere(scope)
	if w == "" {
		return "", fmt.Errorf("attr discovery: kapsam boş (servis / namespace / cluster gerekir)")
	}
	order := ""
	if scope.Service != "" {
		order = "\n\t\t\t    ORDER BY time DESC"
	}
	return `
			SELECT k, count() AS occurrences,
			       arrayDistinct(groupArray(?)(v)) AS sample_values
			FROM (
			  SELECT ` + keysCol + `[idx] AS k,
			         ` + valsCol + `[idx] AS v
			  FROM (
			    SELECT ` + keysCol + `, ` + valsCol + `,
			           arrayJoin(range(1, length(` + keysCol + `) + 1)) AS idx
			    FROM (
			      SELECT ` + keysCol + `, ` + valsCol + `
			      FROM spans
			      WHERE time >= ? AND time <= ?` + w + order + `
			      LIMIT ?
			    )
			  )
			  WHERE k != '' AND v != ''
			)
			GROUP BY k
			ORDER BY occurrences DESC
			LIMIT ?
			SETTINGS max_execution_time = 10`, nil
}

// SampleOrder — çağıranın ilan ettiği örneklem sırası.
func (s AttrScope) SampleOrder() string {
	if s.Service != "" {
		return "recent"
	}
	return "unordered"
}

// ScopedAttrsInnerLimit — örneklem SPAN sayısı (LIMIT'in birimi span; v0.10.488).
const ScopedAttrsInnerLimit = 5000

// GetScopedAttrs — kapsamdaki span örnekleminden anahtar + örnek değerler
// (span ve resource kapsamı ayrı satırlar).
func (s *Store) GetScopedAttrs(ctx context.Context, scope AttrScope, from, to time.Time, topPerScope, sampleLimit int) ([]ServiceAttrRow, error) {
	if scope.empty() {
		return nil, fmt.Errorf("attr discovery: kapsam boş (servis / namespace / cluster gerekir)")
	}
	if topPerScope <= 0 || topPerScope > 200 {
		topPerScope = 50
	}
	if sampleLimit <= 0 || sampleLimit > 50 {
		sampleLimit = 10
	}
	_, sargs := scopeWhere(scope)
	out := make([]ServiceAttrRow, 0, topPerScope*2)
	for _, q := range []struct{ scope, keys, vals string }{{"span", "attr_keys", "attr_values"}, {"resource", "res_keys", "res_values"}} {
		sql, err := scopedAttrsSQL(q.keys, q.vals, scope)
		if err != nil {
			return nil, err
		}
		args := append([]any{sampleLimit, from, to}, sargs...)
		args = append(args, ScopedAttrsInnerLimit, topPerScope)
		rows, err := s.telemetryReadConn().Query(ctx, sql, args...) // v0.10.488 (Astra #9) — telemetri okuma havuzu
		if err != nil {
			return nil, fmt.Errorf("scoped attrs: %w", err)
		}
		for rows.Next() {
			var r ServiceAttrRow
			r.Scope = q.scope
			if err := rows.Scan(&r.Key, &r.Occurrences, &r.SampleValues); err != nil {
				rows.Close()
				return nil, err
			}
			out = append(out, r)
		}
		rows.Close()
	}
	return out, nil
}

// AttrKeyColumn — anahtar için TİPLİ ya da TERFİ kolon (varsa): filtre
// derleyicisinin (filterexpr.go) gördüğü haritanın dışa açık, prob amaçlı
// yarısı. Türetilmiş ifadeler (cluster, duration_ms) kolon SAYILMAZ.
func AttrKeyColumn(key string) (col string, resourceScope bool, ok bool) {
	k := strings.TrimPrefix(strings.TrimPrefix(key, "span."), "resource.")
	if c, found := wellKnown[k]; found && !strings.ContainsAny(c, "( ") {
		return c, false, true
	}
	if c, found := wellKnownResource[k]; found && !strings.ContainsAny(c, "( ") {
		return c, true, true
	}
	for _, p := range allPromotedAttrs() {
		for _, pk := range p.keys {
			if pk == k {
				return p.col, p.res, true
			}
		}
	}
	return "", false, false
}

// AttrProbeBasis — probun dayanağı.
const (
	ProbeColumn = "column" // tipli/terfi kolonda eşitlik (kesin)
	ProbeKVH    = "kvh"    // kvh bloom + eşitlik (kesin, indeksli)
	ProbeNone   = ""       // prob yok (yalnız örneklem)
)

// attrValueProbeSQL — kesin eşitlik sayımı; kolon ya da kvh. Saf.
func attrValueProbeSQL(key, value string, scope AttrScope, kvh bool) (sql string, args []any, basis string, ok bool) {
	w, sargs := scopeWhere(scope)
	if w == "" {
		return "", nil, ProbeNone, false
	}
	if col, res, found := AttrKeyColumn(key); found {
		_ = res
		return `SELECT count() FROM spans WHERE time >= ? AND time <= ?` + w + ` AND ` + col + ` = ?
			SETTINGS max_execution_time = 3`, append(append([]any{}, sargs...), value), ProbeColumn, true
	}
	if !kvh {
		return "", nil, ProbeNone, false
	}
	k := strings.TrimPrefix(key, "span.")
	keysCol, valsCol, kvhCol := "attr_keys", "attr_values", "attr_kvh"
	if strings.HasPrefix(key, "resource.") {
		k = strings.TrimPrefix(key, "resource.")
		keysCol, valsCol, kvhCol = "res_keys", "res_values", "res_kvh"
	}
	return `SELECT count() FROM spans WHERE time >= ? AND time <= ?` + w + `
			AND has(` + kvhCol + `, ` + AttrKVHashSQL + `) AND ` + valsCol + `[indexOf(` + keysCol + `, ?)] = ?
			SETTINGS max_execution_time = 3`, append(append([]any{}, sargs...), k, value, k, value), ProbeKVH, true
}

// AttrValueProbe — kesin eşitlik sayımı (kolon ya da kvh); prob yoksa
// basis "" ve count 0 (çağıran örneklem kanıtıyla yetinir).
func (s *Store) AttrValueProbe(ctx context.Context, scope AttrScope, key, value string, from, to time.Time) (count uint64, basis string, err error) {
	sql, args, basis, ok := attrValueProbeSQL(key, value, scope, AttrIndexAvailable())
	if !ok {
		return 0, ProbeNone, nil
	}
	all := append([]any{from, to}, args...)
	if err := s.telemetryReadConn().QueryRow(ctx, sql, all...).Scan(&count); err != nil {
		return 0, basis, fmt.Errorf("attr probe: %w", err)
	}
	return count, basis, nil
}
