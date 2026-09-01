package chstore

// external_seasonal.go — dış (Influx) seri için mevsimsel baseline okuması
// (v0.10.231, Influx D6 / audit Faz 2).
//
// anomaly.fetchAllSeasonal'ın metric_points ikizi: bir kaynak+sorgunun
// BÜTÜN serileri için tek geçişte "aynı gün-sınıfı, aynı saat dilimi
// (± yarıçap, gece yarısı sarmalı)" dakikalık değerleri. Anahtar
// (service_name, metric) ORDER BY önekiyle sınırlı; zaman sınırlı WHERE +
// LIMIT BY + LIMIT + max_execution_time (CLAUDE.md CH bounds). Gün sınıfı
// ve hedef saniye çağırandan gelir (anomaly.dayClass / dakika-of-day) —
// chstore takvim kuralı bilmez, SQL şekli saf ve testli.

import (
	"context"
	"fmt"
	"strings"
	"time"
)

const (
	externalSeasonalPerKey = 2000
	externalSeasonalTotal  = 400000
	// ExternalSeasonalKeySep — GroupKey birleştirme ayırıcısı; çağıran kendi
	// serilerini aynı ayırıcıyla anahtarlar.
	ExternalSeasonalKeySep = "\x1f"
)

// ExternalSeasonalReq — bkz. dosya başlığı. Class/TargetSod/RadiusSec
// çağıranın takvim kararı; Cutoff/Upper zaman sınırı.
type ExternalSeasonalReq struct {
	Metric, Service string
	GroupBy         []string
	Cutoff, Upper   time.Time
	Class           string // weekday | saturday | sunday
	TargetSod       int    // hedef dakikanın gün-içi saniyesi (UTC)
	RadiusSec       int    // ± pencere yarı-genişliği (saniye)
}

// externalSeasonalSQL — SAF: SQL şekli (external_seasonal_test.go pinler).
func externalSeasonalSQL(groupSelect string) string {
	const sodExpr = "(toHour(time, 'UTC') * 3600 + toMinute(time, 'UTC') * 60)"
	const classExpr = "multiIf(toDayOfWeek(time, 0, 'UTC') = 6, 'saturday', toDayOfWeek(time, 0, 'UTC') = 7, 'sunday', 'weekday')"
	return fmt.Sprintf(`
		SELECT %[1]s AS key, toUnixTimestamp(toStartOfMinute(time)) AS t, max(value) AS v
		FROM metric_points
		WHERE service_name = ? AND metric = ?
		  AND time >= ? AND time < ?
		  AND %[3]s = ?
		  AND least(abs(%[2]s - ?), 86400 - abs(%[2]s - ?)) <= ?
		GROUP BY key, t
		ORDER BY key, t
		LIMIT %[4]d BY key
		LIMIT %[5]d
		SETTINGS max_execution_time = 10`, groupSelect, sodExpr, classExpr, externalSeasonalPerKey, externalSeasonalTotal)
}

// externalSeasonalArgs — SAF: bind sırası SQL'deki ? sırasıyla birebir
// (grup ifadelerinin argümanları önce — SELECT listesi WHERE'den önce
// bağlanır).
func externalSeasonalArgs(req ExternalSeasonalReq, groupArgs []any) []any {
	args := append([]any{}, groupArgs...)
	return append(args, req.Service, req.Metric, req.Cutoff, req.Upper, req.Class, req.TargetSod, req.TargetSod, req.RadiusSec)
}

// ExternalSeasonal — anahtar → aynı-dilim değerleri (zaman sırasıyla) ve
// anahtar → görülen gün kümesi (çağıran gün-çeşitliliği kapısını uygular).
func (s *Store) ExternalSeasonal(ctx context.Context, req ExternalSeasonalReq) (map[string][]float64, map[string]map[int64]struct{}, error) {
	groupSelect := "[]::Array(String)"
	var groupArgs []any
	if len(req.GroupBy) > 0 {
		parts := make([]string, len(req.GroupBy))
		for i, k := range req.GroupBy {
			expr, args := groupKeyExprMetric(k)
			parts[i] = expr
			groupArgs = append(groupArgs, args...)
		}
		groupSelect = "[" + strings.Join(parts, ", ") + "]"
	}
	rows, err := s.telemetryReadConn().Query(ctx, externalSeasonalSQL(groupSelect), externalSeasonalArgs(req, groupArgs)...)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	out := map[string][]float64{}
	days := map[string]map[int64]struct{}{}
	for rows.Next() {
		var key []string
		var t uint32
		var v float64
		if err := rows.Scan(&key, &t, &v); err != nil {
			return nil, nil, err
		}
		k := strings.Join(key, ExternalSeasonalKeySep)
		out[k] = append(out[k], v)
		if days[k] == nil {
			days[k] = map[int64]struct{}{}
		}
		days[k][int64(t)/86400] = struct{}{}
	}
	return out, days, rows.Err()
}
