package api

// oracle_logs_routes.go — v0.10.602 (Oracle Aşama 2 dilim 4, audit §4
// Seçenek A: "Oracle satırları Trace Logs sekmesine ÜÇÜNCÜ dizi olarak
// frontend'de birleşir"). api.go BÜYÜMEZ — route_registry defteri.
//
// Yol /api/oracle/ERRORS, /logs değil: satırlar bir hata tablosunun kayıtları
// (dürüst ad) — ve FE'nin traceLogsLinkGate kapısı `/logs?` yazımını önekten
// bağımsız yasaklar; kapıya muafiyet değil, doğru ad.
//
//	GET /api/oracle/errors?trace_id=&from=&to=&limit=
//
// Rol kapısı YOK: salt-okunur drill-down, viewer trace'in Oracle satırlarını
// GÖRMELİ (küresel middleware kimliksizi zaten 401 yapar). from/to ZORUNLU
// (unix ns): pencere Trace sayfasının span-ankrajlı penceresi
// (traceLogWindow); now()'a düşen bir varsayılan eski trace'i sessizce boş
// gösterirdi (v0.5.223 sınıfı). Etkin Oracle kaynağı yoksa CH'ye HİÇ
// gidilmez: enabled:false + boş liste — FE bunu "kaynak yok" diye okur,
// "satır yok" diye değil.
//
// Satır şekli LogRow'un JSON ikizi (frontend/src/lib/types.ts) + origin:
// "oracle" — <LogTable> aynı satır tipini çizer, ikinci görüntüleyici yok.
// Tipli kolonlar audit §5'in HEDEF anahtarlarıyla attribute'a açılır
// (operation.code, error.code, …); tüketilmeyen Oracle kolonları verbatim.
// serviceName = Oracle KAYNAĞININ adı (ERR_SERVICE bir operasyon kodu,
// servis adı DEĞİL — audit §5). id = row_id'nin 53-bit kesimi: JS güvenli
// aralık, satır başına kararlı; span-event'lerin negatif dizisiyle çakışmaz.

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/cilcenk/coremetry/internal/chstore"
)

func init() { registerRoutesExtra("oracle-logs", (*Server).registerOracleLogRoutes) }

func (s *Server) registerOracleLogRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/oracle/errors", s.getOracleTraceLogs)
}

type oracleLogRow struct {
	ID                 int64             `json:"id"`
	Timestamp          int64             `json:"timestamp"`
	Severity           uint8             `json:"severity"`
	SeverityText       string            `json:"severityText"`
	Body               string            `json:"body"`
	ServiceName        string            `json:"serviceName"`
	TraceID            string            `json:"traceId"`
	SpanID             string            `json:"spanId"`
	Attributes         map[string]string `json:"attributes"`
	ResourceAttributes map[string]string `json:"resourceAttributes"`
	Origin             string            `json:"origin"`
}

type oracleLogsResponse struct {
	Enabled bool           `json:"enabled"`
	Logs    []oracleLogRow `json:"logs"`
	Total   int            `json:"total"`
}

const (
	oracleLogsDefaultLimit = 200
	oracleLogsMaxLimit     = 1000
)

// oracleLogsKey — SAF: trace normalize EDİLMİŞ gelir; limit anahtarda
// (cevabın uzunluğunu değiştirir), pencere 30 s grid'de.
func oracleLogsKey(traceID string, from, to time.Time, limit int) string {
	return fmt.Sprintf("oracle-logs:t=%s:lim=%d:w=%s", traceID, limit, cacheBucket(from, to))
}

// oracleLogRowFromStore — SAF: CH satırı → LogRow ikizi. Boş tipli alan
// attribute ÜRETMEZ (boş attribute bilgi taşımaz); ekstralar verbatim.
func oracleLogRowFromStore(r chstore.OracleErrorRow, sourceName string) oracleLogRow {
	attrs := map[string]string{}
	put := func(k, v string) {
		if v != "" {
			attrs[k] = v
		}
	}
	put("operation.code", r.OperationCode)
	put("error.code", r.ErrorCode)
	put("error.external_code", r.ExternalCode)
	put("error.type", r.ErrorType)
	put("channel.code", r.ChannelCode)
	put("task.code", r.TaskCode)
	put("request.id", r.RequestID)
	put("customer.id", r.CustomerID)
	put("teller.id", r.TellerID)
	put("location", r.Location)
	for i, k := range r.AttrKeys {
		if i < len(r.AttrValues) && r.AttrValues[i] != "" {
			attrs[k] = r.AttrValues[i]
		}
	}
	res := map[string]string{"oracle.source": sourceName, "oracle.source_id": r.SourceID}
	if r.HostName != "" {
		res["host.name"] = r.HostName
	}
	if r.InstanceID != "" {
		// O4 açık: instance_id'nin service.instance.id mi k8s.pod.name mi olduğu
		// görülmeden karar verilmez → anlam yüklemeyen anahtar.
		res["oracle.instance_id"] = r.InstanceID
	}
	name := sourceName
	if name == "" {
		name = r.SourceID
	}
	return oracleLogRow{
		ID:                 int64(r.RowID & (1<<53 - 1)),
		Timestamp:          r.Time.UnixNano(),
		Severity:           r.SeverityNum,
		SeverityText:       r.SeverityText,
		Body:               r.Body,
		ServiceName:        name,
		TraceID:            r.TraceID,
		SpanID:             r.SpanID,
		Attributes:         attrs,
		ResourceAttributes: res,
		Origin:             "oracle",
	}
}

// oracleSourceNames — id → ad; ad yoksa id düşer (satır adsız kalmaz).
func (s *Server) oracleSourceNames() map[string]string {
	out := map[string]string{}
	if s.oracle == nil {
		return out
	}
	for _, src := range s.oracle.CurrentSettings().Sources {
		out[src.ID] = src.Name
	}
	return out
}

func (s *Server) getOracleTraceLogs(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	traceID := strings.ToLower(strings.TrimSpace(q.Get("trace_id")))
	if traceID == "" {
		writeJSONError(w, http.StatusBadRequest, "trace_id parametresi zorunlu")
		return
	}
	from, to := parseTime(q.Get("from")), parseTime(q.Get("to"))
	if from.IsZero() || to.IsZero() || !to.After(from) {
		writeJSONError(w, http.StatusBadRequest, "from/to (unix ns) zorunlu ve from < to olmalı")
		return
	}
	limit := parseInt(q.Get("limit"), oracleLogsDefaultLimit)
	if limit <= 0 || limit > oracleLogsMaxLimit {
		limit = oracleLogsDefaultLimit
	}
	if s.oracle == nil || !s.oracle.HasEnabledSources() {
		writeJSON(w, oracleLogsResponse{Enabled: false, Logs: []oracleLogRow{}})
		return
	}
	names := s.oracleSourceNames()
	key := oracleLogsKey(traceID, from, to, limit)
	s.serveCached(w, r, key, 30*time.Second, func(ctx context.Context) (any, error) {
		rows, err := s.store.OracleErrorsByTrace(ctx, traceID, from, to, limit)
		if err != nil {
			return nil, err
		}
		out := make([]oracleLogRow, 0, len(rows))
		for _, row := range rows {
			out = append(out, oracleLogRowFromStore(row, names[row.SourceID]))
		}
		return oracleLogsResponse{Enabled: true, Logs: out, Total: len(out)}, nil
	})
}
