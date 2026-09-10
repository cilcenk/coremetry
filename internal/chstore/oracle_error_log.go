package chstore

// oracle_error_log.go — v0.10.599 (Oracle Aşama 2, audit §4 Seçenek A).
//
// Oracle MCA_TERROR_LOG satırlarının CH'deki evi. NEDEN kendi tablosu, NEDEN
// `logs` değil (audit §4): `logs` düz MergeTree — tekillik anahtarı yok,
// aynı satırı iki kez yazmak iki satır + aynı `id` üretir; "kaynak" kolonu
// da yok. Poller'ın overlap penceresi (aynı satırı iki tikte görmesi)
// idempotent yazım İSTER → ReplacingMergeTree(version), dedup anahtarı
// (source_id, time, row_id). row_id eşlemeden gelen içerik hash'i
// (internal/oracle/mapping.go); time ORDER BY'da olduğu için PARTITION BY
// toYYYYMM(time) Kural P1'i ihlal etmez (partition_dedup_test.go).
//
// highVolumeTables'a GİRMEZ: hacim günde binlerce satır, shard'lamak
// anlamsız; küme kipinde adaptDDL state tablosu yolunu uygular (ON CLUSTER +
// Replicated, tek replikasyon grubu). LowCardinality yalnız taksonomi
// kolonlarında — kod listeleri sınırlı (audit §6.1 gruplama anahtarı);
// external_code / task_code ÖLÇÜLMEDİ → düz String (/clickhouse-schema C3).
//
// Okuma tek şekil: trace_id + zaman penceresi (Trace Logs sekmesi). Tablo
// küçük ama sözleşme aynı: FINAL + zaman sınırı + LIMIT + max_execution_time.
// TTL 30 gün sabit (logs varsayılanıyla aynı); retention.* vidasına
// bağlanması ayrı dilim.

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// OracleErrorRow — bir Oracle hata satırının CH satırı. Tipli kolonlar audit
// §5 eşlemesinin sol tarafı; eşlemede tüketilmeyen HER kolon attr_keys /
// attr_values'a verbatim gider (tam fidelity duruşu).
type OracleErrorRow struct {
	SourceID string
	Time     time.Time
	RowID    uint64

	SeverityNum  uint8
	SeverityText string
	Body         string
	TraceID      string
	SpanID       string

	HostName      string
	InstanceID    string
	OperationCode string
	ErrorCode     string
	ExternalCode  string
	ErrorType     string
	ChannelCode   string
	TaskCode      string
	RequestID     string
	CustomerID    string
	TellerID      string
	Location      string

	AttrKeys   []string
	AttrValues []string
}

const oracleErrorLogDDL = `CREATE TABLE IF NOT EXISTS oracle_error_log (
			source_id      LowCardinality(String),
			time           DateTime64(9) CODEC(Delta, ZSTD(3)),
			row_id         UInt64,
			severity_num   UInt8,
			severity_text  LowCardinality(String),
			body           String CODEC(ZSTD(3)),
			trace_id       String,
			span_id        String,
			host_name      LowCardinality(String),
			instance_id    String,
			operation_code LowCardinality(String),
			error_code     LowCardinality(String),
			external_code  String,
			error_type     LowCardinality(String),
			channel_code   LowCardinality(String),
			task_code      String,
			request_id     String,
			customer_id    String,
			teller_id      String,
			location       String,
			attr_keys      Array(String) CODEC(ZSTD(3)),
			attr_values    Array(String) CODEC(ZSTD(3)),
			version        UInt64 DEFAULT toUnixTimestamp64Nano(now64(9)),
			INDEX idx_oracle_trace trace_id TYPE bloom_filter(0.01) GRANULARITY 4
		) ENGINE = ReplacingMergeTree(version)
		PARTITION BY toYYYYMM(time)
		ORDER BY (source_id, time, row_id)
		TTL toDate(time) + INTERVAL 30 DAY`

// oracleErrorLogColumns — INSERT kolon listesi; `version` DEFAULT'a bırakılır.
// Sıra Append ile BİREBİR (aynı-tipli komşu takası hiçbir testi kırmaz —
// /otlp-converter §6 uyarısı; oracle_error_log_test.go sırayı pinler).
const oracleErrorLogColumns = "source_id, time, row_id, severity_num, severity_text, body, trace_id, span_id, " +
	"host_name, instance_id, operation_code, error_code, external_code, error_type, channel_code, task_code, " +
	"request_id, customer_id, teller_id, location, attr_keys, attr_values"

// InsertOracleErrors — batch yazım; boş dilim no-op. Yeniden koşum güvenli:
// aynı (source_id, time, row_id) FINAL'da tek satıra iner.
func (s *Store) InsertOracleErrors(ctx context.Context, rows []OracleErrorRow) error {
	if len(rows) == 0 {
		return nil
	}
	batch, err := s.conn.PrepareBatch(ctx, "INSERT INTO oracle_error_log ("+oracleErrorLogColumns+")")
	if err != nil {
		return err
	}
	for _, r := range rows {
		keys, vals := r.AttrKeys, r.AttrValues
		if keys == nil {
			keys = []string{}
		}
		if vals == nil {
			vals = []string{}
		}
		if err := batch.Append(
			r.SourceID, r.Time, r.RowID, r.SeverityNum, r.SeverityText, r.Body, r.TraceID, r.SpanID,
			r.HostName, r.InstanceID, r.OperationCode, r.ErrorCode, r.ExternalCode, r.ErrorType, r.ChannelCode, r.TaskCode,
			r.RequestID, r.CustomerID, r.TellerID, r.Location, keys, vals,
		); err != nil {
			return err
		}
	}
	return batch.Send()
}

const (
	oracleErrorsDefaultLimit = 200
	oracleErrorsMaxLimit     = 1000
	oracleErrorsMaxExecSec   = 5
)

// clampOracleErrorsLimit — SAF: 0/negatif → varsayılan, tavan 1000.
func clampOracleErrorsLimit(limit int) int {
	if limit <= 0 {
		return oracleErrorsDefaultLimit
	}
	if limit > oracleErrorsMaxLimit {
		return oracleErrorsMaxLimit
	}
	return limit
}

// oracleErrorsByTraceSQL — SAF: testin pinlediği okuma metni. trace_id
// normalize EDİLMİŞ gelir (yazma anında da normalize ediliyor; iki taraf
// aynı biçimde buluşur). Zaman sınırı ZORUNLU: pencere Trace sayfasının
// span-ankrajlı penceresi (traceLogWindow), now() değil.
func oracleErrorsByTraceSQL(limit int) string {
	return fmt.Sprintf(`SELECT source_id, time, row_id, severity_num, severity_text, body, trace_id, span_id,
			host_name, instance_id, operation_code, error_code, external_code, error_type, channel_code, task_code,
			request_id, customer_id, teller_id, location, attr_keys, attr_values
		FROM oracle_error_log FINAL
		WHERE trace_id = ? AND time >= ? AND time < ?
		ORDER BY time
		LIMIT %d
		SETTINGS max_execution_time = %d`, clampOracleErrorsLimit(limit), oracleErrorsMaxExecSec)
}

// OracleErrorsByTrace — bir trace'in Oracle satırları, zaman penceresi
// içinde, en fazla limit. Boş/geçersiz trace id → boş dilim, sorgu YOK.
func (s *Store) OracleErrorsByTrace(ctx context.Context, traceID string, from, to time.Time, limit int) ([]OracleErrorRow, error) {
	traceID = normalizeLogID(traceID)
	if traceID == "" || !to.After(from) {
		return []OracleErrorRow{}, nil
	}
	rows, err := s.conn.Query(ctx, oracleErrorsByTraceSQL(limit), traceID, from, to)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []OracleErrorRow{}
	for rows.Next() {
		var r OracleErrorRow
		if err := rows.Scan(
			&r.SourceID, &r.Time, &r.RowID, &r.SeverityNum, &r.SeverityText, &r.Body, &r.TraceID, &r.SpanID,
			&r.HostName, &r.InstanceID, &r.OperationCode, &r.ErrorCode, &r.ExternalCode, &r.ErrorType, &r.ChannelCode, &r.TaskCode,
			&r.RequestID, &r.CustomerID, &r.TellerID, &r.Location, &r.AttrKeys, &r.AttrValues,
		); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// oracleErrorLogColumnCount — DDL'deki kolon sayısı (version + INDEX hariç)
// ile INSERT listesinin eşleştiğini testin kanıtlaması için.
func oracleErrorLogColumnCount() int {
	return len(strings.Split(oracleErrorLogColumns, ","))
}
