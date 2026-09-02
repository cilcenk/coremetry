package logstore

import (
	"context"
	"time"
)

// clickhouse_fields.go — v0.10.280 (log-search audit §2.2 / Dilim 1b):
// ClickHouse arka ucunda alan keşfi. Öncesinde CH "No mapping discovery on
// this backend" diyordu ve alan paneli boştu; v0.10.279 alan yazımını
// derlediğinden beri panelin CH'de de dolu olması gerekiyor — aksi hâlde
// operatör hangi adın kabul edildiğini dışarıdan bilmek zorunda.
//
// İki kaynak: (1) SABİT şema — logql hedefinin kolona bağladığı kanonik
// adlar (service.name/level/…; TestCHFixedFieldsResolveToColumns bunu
// pinler); (2) ÖRNEKLENMİŞ anahtarlar — son 1 saatten en fazla 200k
// satırın attr/res anahtarları, görülme sayısına göre. Tam tarama YOK
// (CLAUDE.md: LIMIT + zaman sınırı + max_execution_time).

const chLogsKeySampleSQL = `
	SELECT k, count() AS c
	FROM (
		SELECT arrayJoin(arrayConcat(attr_keys, res_keys)) AS k
		FROM logs
		WHERE time >= ? AND time < ?
		LIMIT 200000
	)
	WHERE k != ''
	GROUP BY k
	ORDER BY c DESC, k ASC
	LIMIT ?
	SETTINGS max_execution_time = 5`

// chLogsFixedFields — kolon-bağlı kanonik adlar ve tipleri (ES mapping
// tipleriyle aynı sözlük: keyword/text/long).
var chLogsFixedFields = []struct{ Name, Type string }{
	{"service.name", "keyword"},
	{"level", "keyword"},
	{"severity_num", "long"},
	{"message", "text"},
	{"trace_id", "keyword"},
	{"span_id", "keyword"},
	{"host.name", "keyword"},
}

// ListFieldsBounded — boundedFielder (api_logs.go getLogsFields) sözleşmesi;
// ES ile aynı şekil. Örnekleme hatası paneli boşaltmaz: sabit alanlar
// yine döner, hata log'lanır.
func (s *CHStore) ListFieldsBounded(ctx context.Context) (ListFieldsResult, error) {
	fields := make([]string, 0, listFieldsMax)
	types := make(map[string]string, listFieldsMax)
	for _, f := range chLogsFixedFields {
		fields = append(fields, f.Name)
		types[f.Name] = f.Type
	}
	to := time.Now()
	from := to.Add(-1 * time.Hour)
	rows, err := s.store.Conn().Query(ctx, chLogsKeySampleSQL, from, to, listFieldsMax-len(fields))
	if err != nil {
		return ListFieldsResult{Fields: fields, Total: len(fields), Types: types}, err
	}
	defer rows.Close()
	for rows.Next() {
		var k string
		var c uint64
		if err := rows.Scan(&k, &c); err != nil {
			return ListFieldsResult{Fields: fields, Total: len(fields), Types: types}, err
		}
		if _, dup := types[k]; dup {
			continue
		}
		fields = append(fields, k)
		types[k] = "keyword" // attr/res değerleri String; tam eşleşme
	}
	return ListFieldsResult{Fields: fields, Total: len(fields), Types: types}, nil
}
