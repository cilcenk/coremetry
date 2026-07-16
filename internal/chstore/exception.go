package chstore

import (
	"context"
	"time"
)

// v0.8.494 — exception hattı iki kaynaktan beslenir (operatör isteği:
// "error.type kritik önem arz ediyor"):
//
//  1. exception EVENT'i olan span'ler (klasik yol — tip/mesaj/stack
//     events JSON'ından),
//  2. exception event'i OLMAYAN ama span-seviyesi `error.type`
//     attribute'u taşıyan HATA span'leri (OTel stable semconv).
//     HTTP/gRPC client instrumentation'ları DNS/connect sınıfı
//     hatalarda (java.net.UnknownHostException…) çoğunlukla event
//     yazmaz, yalnız error.type koyar — bu span'ler önceden triage'da
//     tamamen görünmezdi.
//
// Üç fragment TEK tanımdır; beş sorgu sitesi (GetExceptions,
// RefreshExceptionGroups, GetExceptionGroupSamples, occurrencesQuery,
// EndpointTopExceptions) bunları paylaşır ki tip çözümü ile eşleme
// asla birbirinden sapmasın.
const (
	// exMatchPred — bir span'i exception hattına sokan koşul.
	exMatchPred = `(events LIKE '%"exception"%' OR (status_code = 'error' AND has(attr_keys, 'error.type')))`
	// exFirstEvent — span'in İLK exception event'i, dizideki KONUMDAN
	// bağımsız (v0.8.563). Eski ifadeler $[0]'ı okuyordu: exception,
	// span'in ikinci event'iyse (önünde retry/log event'i olan
	// instrumentation'lar) tip/mesaj/stack BOŞ çıkıyor ve satır ''
	// grubuna düşüyordu — LIKE onu hatta sokuyor ama $[0] yanlış
	// event'e bakıyordu. arrayFirst eşleşme bulamazsa '' döner,
	// JSON_VALUE('') de '' — kabul edilmiş-ama-event'siz satırların
	// (LIKE'ın nadir yalancı pozitifi) duruşu değişmez. Beş sorgu
	// sitesi ve 3 stacktrace okuması bu TEK fragmenti kullanır.
	// Canlı ölçüm: arrayFirst yolu $[0]'dan yavaş DEĞİL (6h exception
	// satırlarında 0.41s vs 0.68s).
	exFirstEvent = `arrayFirst(x -> JSONExtractString(x, 'name') = 'exception', JSONExtractArrayRaw(events))`
	// exTypeExpr — grup tipi: event tipi öncelikli (en zengin),
	// yoksa error.type attribute'u. multiIf dalları eager değerlenir;
	// arrayElement 0-index'te '' döndürdüğünden has() yalancı olsa da
	// güvenlidir.
	exTypeExpr = `multiIf(events LIKE '%"exception"%', coalesce(JSON_VALUE(` + exFirstEvent + `, '$.attributes."exception.type"'), '<unknown>'), has(attr_keys, 'error.type'), attr_values[indexOf(attr_keys, 'error.type')], '<unknown>')`
	// exMsgExpr — mesaj: event mesajı, attr-doğumlu grupta status_msg.
	exMsgExpr = `if(events LIKE '%"exception"%', coalesce(JSON_VALUE(` + exFirstEvent + `, '$.attributes."exception.message"'), ''), status_msg)`
	// exStackExpr — stacktrace, aynı ilk-exception-event'ten. Eskiden
	// üç sitede kopya-yapıştır $[0] ifadesiydi; tek tanım.
	exStackExpr = `coalesce(JSON_VALUE(` + exFirstEvent + `, '$.attributes."exception.stacktrace"'), '')`
)

type ExceptionFilter struct {
	Service  string
	GroupBy  string // "type" | "type-service" | "full"  (default: "type-service")
	From, To time.Time
	Limit    int
}

// GetExceptions returns OTel `exception` events grouped by (type, message,
// service) with totals and a sample trace/span pointer for drill-down.
//
// We dig the events JSON column with JSON_VALUE — slower than dedicated
// columns, but the volume of error spans is small relative to the total.
func (s *Store) GetExceptions(ctx context.Context, f ExceptionFilter) ([]ExceptionRow, error) {
	// v0.8.454 — pencere zorunlu: sıfır from/to varsayılan 1 saate iner
	// (boundWindow). Penceresiz çağrı tüm span tarihçesinde JSON kazıyordu.
	f.From, f.To = boundWindow(f.From, f.To, time.Hour)
	var wc whereClause
	wc.add("time >= ?", f.From)
	wc.add("time <= ?", f.To)
	if f.Service != "" {
		wc.add("service_name = ?", f.Service)
	}
	wc.add(exMatchPred)
	if f.Limit == 0 {
		f.Limit = 100
	}

	// Choose grouping. anyIf makes ungrouped fields show *some* value.
	var groupCols, selectMsg, selectSvc string
	switch f.GroupBy {
	case "type":
		groupCols = "ex_type"
		selectMsg = "any(ex_msg)         AS ex_msg"
		selectSvc = "any(service_name)   AS svc"
	case "full":
		groupCols = "ex_type, ex_msg, service_name"
		selectMsg = "ex_msg"
		selectSvc = "service_name        AS svc"
	default: // "type-service"
		groupCols = "ex_type, service_name"
		selectMsg = "any(ex_msg)         AS ex_msg"
		selectSvc = "service_name        AS svc"
	}

	// Pull exception fields directly from the events JSON.
	rows, err := s.conn.Query(ctx, `
		WITH src AS (
		  SELECT
		    `+exTypeExpr+` AS ex_type,
		    `+exMsgExpr+`  AS ex_msg,
		    service_name, time, trace_id, span_id
		  FROM spans `+wc.sql()+`
		)
		SELECT ex_type, `+selectMsg+`, `+selectSvc+`,
		       count() AS cnt,
		       toUnixTimestamp64Nano(max(time)) AS last_seen,
		       argMax(trace_id, time) AS sample_trace,
		       argMax(span_id,  time) AS sample_span
		FROM src
		GROUP BY `+groupCols+`
		ORDER BY cnt DESC
		LIMIT ?
		SETTINGS max_execution_time = 15`, append(wc.args, f.Limit)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ExceptionRow
	for rows.Next() {
		var r ExceptionRow
		if err := rows.Scan(&r.Type, &r.Message, &r.Service, &r.Count,
			&r.LastSeen, &r.SampleTraceID, &r.SampleSpanID); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
