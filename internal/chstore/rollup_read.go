package chstore

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"
)

// rollup_read.go — rollup okuma katmanı (Aşama 2, docs/rollup-design.md).
// PickRollup'un seçtiği tablodan RED serisi okur; iki quantile modu:
// tdigest (dar aile) ve 20'lik eksponansiyel bucket kestirimi (geniş aile,
// tasarım §3 formülü). Tablolar migrations/0001-0002 ELLE uygulanana kadar
// yoktur — probe, yokluğu ErrRollupTablesMissing olarak DÜRÜSTÇE bildirir
// (endpoint 424 döner; boot'a ve ingest'e hiçbir etkisi yoktur — otomatik
// migrate BİLİNÇLİ yok: DDL ON CLUSTER/Replicated/spans_local sahipliği
// operatörde, v0.8.185 dersi).

// ErrRollupTablesMissing — migrations uygulanmadan endpoint çağrıldı.
var ErrRollupTablesMissing = fmt.Errorf("rollup tabloları bulunamadı — migrations/0001-0002 bu ClickHouse'a uygulanmamış")

// rollupProbe — tablo varlığı önbelleği: var olan tablo bir daha sorulmaz,
// yokluk 60s'de bir yeniden denenir (her istekte system.tables sorgusu yok).
var rollupProbe = struct {
	mu      sync.Mutex
	seen    map[string]bool
	checked map[string]time.Time
}{seen: map[string]bool{}, checked: map[string]time.Time{}}

func (s *Store) rollupTableExists(ctx context.Context, table string) (bool, error) {
	rollupProbe.mu.Lock()
	if rollupProbe.seen[table] {
		rollupProbe.mu.Unlock()
		return true, nil
	}
	if t, ok := rollupProbe.checked[table]; ok && time.Since(t) < time.Minute {
		rollupProbe.mu.Unlock()
		return false, nil
	}
	rollupProbe.mu.Unlock()

	var n uint64
	row := s.conn.QueryRow(ctx,
		`SELECT count() FROM system.tables WHERE database = currentDatabase() AND name = ?`, table)
	if err := row.Scan(&n); err != nil {
		return false, err
	}
	rollupProbe.mu.Lock()
	rollupProbe.checked[table] = time.Now()
	if n > 0 {
		rollupProbe.seen[table] = true
	}
	rollupProbe.mu.Unlock()
	return n > 0, nil
}

// RollupTableReady — dış katman (route) için probe: tablo var mı?
// serveCached ÖNCESİ çağrılır ki 424 cevabı cache'e yazılmasın.
func (s *Store) RollupTableReady(ctx context.Context, table string) (bool, error) {
	return s.rollupTableExists(ctx, table)
}

// RollupSeriesFilter — okuma filtreleri. Tüm alanlar opsiyonel eşitlik;
// GroupBy tek boyut ("" = toplam seri). Değerler DAİMA bind-arg; kolon
// adları rollupCols whitelist'inden çözülür (enterpolasyon yok).
type RollupSeriesFilter struct {
	Service  string
	Kind     string
	Status   string
	Endpoint string
	Channel  string
	Function string
	GroupBy  string
	// MaxGroups: GroupBy'lı sorguda tutulacak en kalabalık grup sayısı;
	// 0 → 20. Kesme yanıtta truncated=true olarak İFŞA edilir.
	MaxGroups int
}

var rollupCols = map[string]string{
	"service_name": "service_name", "span_kind": "span_kind", "status_code": "status_code",
	"endpoint": "endpoint", "channel_code": "channel_code", "function_code": "function_code",
}

// RollupPoint — tek zaman kovası (bucket başı, unix saniye).
type RollupPoint struct {
	TS     int64   `json:"ts"`
	Calls  uint64  `json:"calls"`
	Errors uint64  `json:"errors"`
	AvgMs  float64 `json:"avgMs"`
	P50Ms  float64 `json:"p50Ms"`
	P95Ms  float64 `json:"p95Ms"`
	P99Ms  float64 `json:"p99Ms"`
}

type RollupSeries struct {
	Group  string        `json:"group,omitempty"`
	Points []RollupPoint `json:"points"`
}

// rollupREDSQL — okuma SQL'ini kurar. Saf (Store'suz) — rollup_read_test.go
// üretilen SQL'in bound/LIMIT/settings disiplinini ve iki quantile modunu
// pinler. Dönen args sırası SQL'deki placeholder sırasıyla birebir.
func rollupREDSQL(plan RollupPlan, f RollupSeriesFilter, from, to time.Time) (string, []any, error) {
	where := []string{"ts >= ?", "ts < ?"}
	args := []any{from, to}
	addEq := func(dim, val string) {
		if val != "" {
			where = append(where, rollupCols[dim]+" = ?")
			args = append(args, val)
		}
	}
	addEq("service_name", f.Service)
	addEq("span_kind", f.Kind)
	addEq("status_code", f.Status)
	addEq("endpoint", f.Endpoint)
	addEq("channel_code", f.Channel)
	addEq("function_code", f.Function)
	whereSQL := strings.Join(where, " AND ")

	grpSel := "'' AS grp"
	grpBy := ""
	if f.GroupBy != "" {
		col, ok := rollupCols[f.GroupBy]
		if !ok {
			return "", nil, fmt.Errorf("rollup: bilinmeyen groupBy %q", f.GroupBy)
		}
		grpSel = "toString(" + col + ") AS grp"
		grpBy = ", grp"
	}
	maxGroups := f.MaxGroups
	if maxGroups <= 0 {
		maxGroups = 20
	}

	// GroupBy'da grup kümesi en kalabalık (maxGroups+1) grupla sınırlanır —
	// +1, kesmenin olduğunu yanıtta truncated olarak söyleyebilmek için.
	topFilter := ""
	if f.GroupBy != "" {
		col := rollupCols[f.GroupBy]
		topFilter = fmt.Sprintf(` AND %s IN (
			SELECT %s FROM %s WHERE %s
			GROUP BY %s ORDER BY sum(span_count) DESC LIMIT %d)`,
			col, col, plan.Table, whereSQL, col, maxGroups+1)
		args = append(args, args[:len(args)]...) // iç sorgu aynı bind'ları SQL sırasında tekrar ister
	}

	inner := fmt.Sprintf(`
		SELECT
			toUnixTimestamp(toStartOfInterval(ts, INTERVAL %d SECOND)) AS tb,
			%s,
			sum(span_count)   AS calls,
			sum(error_count)  AS errs,
			sum(duration_sum) AS dur_sum,
			%%s
		FROM %s
		WHERE %s%s
		GROUP BY tb%s`,
		plan.StepSeconds, grpSel, plan.Table, whereSQL, topFilter, grpBy)

	var sql string
	switch plan.QuantileMode {
	case "tdigest":
		sql = fmt.Sprintf(inner,
			"arrayMap(x -> x / 1e6, quantilesTDigestMerge(0.5, 0.95, 0.99)(q_state)) AS qs")
	case "buckets":
		// İç katman bucket dizisini birleştirir; dış katman tasarım §3'ün
		// log-lineer kestirimini uygular. arrayFirstIndex tekrarları CH'nin
		// ortak-alt-ifade eliminasyonuna bırakıldı (tuple-alias hack'inden
		// daha taşınabilir).
		in := fmt.Sprintf(inner, "sumForEachMerge(lat_buckets) AS b")
		sql = `
		SELECT tb, grp, calls, errs, dur_sum,
			arrayMap(q -> if(toFloat64(arrayCumSum(b)[20]) = 0, 0.,
				exp2(arrayFirstIndex(c -> c >= greatest(1., q * arrayCumSum(b)[20]), arrayCumSum(b)) - 1)
				* (1 + (greatest(1., q * arrayCumSum(b)[20])
					- if(arrayFirstIndex(c -> c >= greatest(1., q * arrayCumSum(b)[20]), arrayCumSum(b)) <= 1, 0,
						arrayCumSum(b)[arrayFirstIndex(c -> c >= greatest(1., q * arrayCumSum(b)[20]), arrayCumSum(b)) - 1]))
					/ greatest(1, b[arrayFirstIndex(c -> c >= greatest(1., q * arrayCumSum(b)[20]), arrayCumSum(b))]))
			), [0.5, 0.95, 0.99]) AS qs
		FROM (` + in + `)`
	default:
		return "", nil, fmt.Errorf("rollup: bilinmeyen quantile modu %q", plan.QuantileMode)
	}

	sql += `
		ORDER BY grp, tb
		LIMIT 250000
		SETTINGS max_execution_time = 15`
	return sql, args, nil
}

// QueryRollupRED — plan.Table'dan step'e katlanmış RED serisi.
// İkinci dönüş: GroupBy kesmesi oldu mu (sessiz kesme yok — yanıt söyler).
func (s *Store) QueryRollupRED(ctx context.Context, plan RollupPlan, f RollupSeriesFilter, from, to time.Time) ([]RollupSeries, bool, error) {
	ok, err := s.rollupTableExists(ctx, plan.Table)
	if err != nil {
		return nil, false, err
	}
	if !ok {
		return nil, false, ErrRollupTablesMissing
	}
	sql, args, err := rollupREDSQL(plan, f, from, to)
	if err != nil {
		return nil, false, err
	}
	rows, err := s.conn.Query(ctx, sql, args...)
	if err != nil {
		return nil, false, fmt.Errorf("rollup read %s: %w", plan.Table, err)
	}
	defer rows.Close()

	maxGroups := f.MaxGroups
	if maxGroups <= 0 {
		maxGroups = 20
	}
	byGroup := map[string]*RollupSeries{}
	order := []string{}
	for rows.Next() {
		var tb int64
		var grp string
		var calls, errs, durSum uint64
		var qs []float64
		if err := rows.Scan(&tb, &grp, &calls, &errs, &durSum, &qs); err != nil {
			return nil, false, err
		}
		p := RollupPoint{TS: tb, Calls: calls, Errors: errs}
		if calls > 0 {
			p.AvgMs = float64(durSum) / float64(calls) / 1e6
		}
		if len(qs) == 3 {
			p.P50Ms, p.P95Ms, p.P99Ms = qs[0], qs[1], qs[2]
		}
		sr := byGroup[grp]
		if sr == nil {
			sr = &RollupSeries{Group: grp}
			byGroup[grp] = sr
			order = append(order, grp)
		}
		sr.Points = append(sr.Points, p)
	}
	if err := rows.Err(); err != nil {
		return nil, false, err
	}

	truncated := false
	if f.GroupBy != "" && len(order) > maxGroups {
		order = order[:maxGroups]
		truncated = true
	}
	out := make([]RollupSeries, 0, len(order))
	for _, g := range order {
		out = append(out, *byGroup[g])
	}
	return out, truncated, nil
}
