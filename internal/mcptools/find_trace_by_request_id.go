package mcptools

// find_trace_by_request_id (v0.9.1142) — YAPILANDIRILMIŞ request kimliği
// → trace. Katalogdaki üçüncü "yapıştırılan kimlik" girişi.
//
// find_trace_by_span'in (v0.9.1141) kardeşi ve gerekçesi birebir aynı:
// operatörün elindeki kimlikle katalogdaki hiçbir tool çalışmıyorsa model
// ya ad uydurur ya "bilgi yok" der. Operatörün kurumsal sistemlerinde
// ELDEKİ kimlik bir span/trace id DEĞİL, sabit yapılı bir istek
// numarasıdır — ve o numara kendi TARİH+SAATİNİ taşıyor.
//
// PENCERE KİMLİĞİN KENDİSİNDEN GELİYOR ve bu yüzden `range_s` arg'ı YOK:
// koysaydık ya yok sayılan bir arg olurdu ya da modeli kimliğin zaten
// söylediği pencereyi tahmin etmeye davet ederdi (list_operations'ın
// pencere-yok kararıyla aynı doktrin). Efektif pencere window_from /
// window_to ile MUTLAK olarak ifşa ediliyor — göreli bir "window_s"
// burada yalan söylerdi, çünkü pencere ŞİMDİye değil kimliğin damgasına
// çapalı (feedback-cosrechart-anchors-to-now sınıfı).
//
// ENV ARG KARARI (v0.8.398 sözleşmesi): YOK. İki bağımsız sebep — (1) bu
// kimlik-çapalı bir nokta aramasıdır (find_trace_by_span emsali), (2)
// okuma logstore.Search üzerinden gidiyor ve log tarafı env boyutunu
// uygulayamıyor (search_logs de bu yüzden env'siz; env-ayrımı Faz 4).
// Uygulanamayacak bir filtre vaat etmiyoruz.
//
// MinRole "" (viewer tabanı): salt-okunur, ve REST eşleri viewer'a açık
// (GET /api/logs kapısız, GET /api/traces kapısız). Uygulama içi sohbet
// hızlı yolu da (copilot_guided.go) tam olarak bu okumayı yapıyor.
//
// GÜVENLİK: bu dosyada gerçek bir kurum/müşteri değeri YOK; şema
// açıklamalarındaki örnek şekil sentetik.

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/cilcenk/coremetry/internal/mcp"
	"github.com/cilcenk/coremetry/internal/reqid"
)

// requestIDLookupPayload — çözümleme → tool gövdesi. SAF, tablo testli.
//
// BULUNAMAMAK HATA DEĞİL (find_trace_by_span/guidedSpanBundle emsali):
// hata döndürsek model ya döngüye girer ya "trace yok" diye kesin
// konuşur. Dürüst kanıt found=false + pencere + olası nedenlerdir.
//
// Pencere HER İKİ hâlde de gövdede: operatörün "ben o kadar da eski bir
// istek sormadım" diyebilmesi için aranan aralığı görmesi gerekiyor.
func requestIDLookupPayload(res reqid.Resolution) map[string]any {
	from, to := res.ID.Window()
	body := map[string]any{
		"request_id":  res.ID.Raw,
		"window_from": reqid.FmtLocal(from),
		"window_to":   reqid.FmtLocal(to),
		// request_time — kimliğin İÇİNDEKİ damga. Model tarih aritmetiği
		// yapmasın; okunmuş hâli burada.
		"request_time": reqid.FmtLocal(res.ID.TS),
		"parsed": map[string]any{
			"function_code": res.ID.FuncCode,
			"channel_code":  res.ID.Channel,
			"sub_code":      res.ID.SubCode,
			"customer_no":   res.ID.CustomerNo,
			"sequence":      res.ID.Salt,
		},
		"matched_logs": res.MatchedLogs,
	}
	// Kısmi cevap: "bulunamadı" KESİN bir yokluk değil.
	if res.Partial {
		body["partial"] = true
		body["partial_note"] = "The log backend returned a PARTIAL result (soft timeout / failed shards). A found=false here is not proof the request is absent."
	}
	if res.TraceID == "" {
		body["found"] = false
		body["reasons"] = []string{
			"the request id was copied wrong (it must be the full fixed-width string, no separators)",
			"the log line carrying this id has NO trace context (no trace_id) — the request was logged by a component that is not traced",
			"the id is not in the log BODY: free-text log search matches the message body, so an id stored only in a structured field will not match here — use the external log bridge link instead",
			"the request is outside the searched window (the window comes from the id's own timestamp, ±10 minutes)",
			"the logs aged out of retention",
		}
		body["note"] = "Say this plainly to the operator and state the window you searched; do NOT invent a trace id, spans or timings."
		return body
	}
	body["found"] = true
	body["trace_id"] = res.TraceID
	if res.SpanID != "" {
		body["span_id"] = res.SpanID
	}
	if res.Service != "" {
		body["service"] = res.Service
	}
	// DistinctTraces > 1: aynı kimlik birden çok trace'e dokunmuş
	// (yeniden deneme, asenkron devam). Tek trace gibi sunulmamalı.
	body["distinct_traces"] = res.DistinctTraces
	if res.DistinctTraces > 1 {
		body["ambiguous_note"] = fmt.Sprintf(
			"%d DIFFERENT traces carry this request id in the window (retry / async continuation). trace_id is the newest one — say so instead of implying it is the only one.",
			res.DistinctTraces)
	}
	body["next"] = "get_trace ile waterfall'ı al; get_logs_for_trace ile aynı isteğin tüm log satırlarını gör."
	return body
}

type findTraceByRequestIDArgs struct {
	RequestID string `json:"request_id"`
}

func findTraceByRequestIDTool(d Deps) mcp.Tool {
	return mcp.Tool{
		Name: "find_trace_by_request_id",
		Description: "Resolve a STRUCTURED enterprise request id — the fixed-width string the operator's own systems stamp on every request — " +
			"to the trace that served it. This is the pasted-id entry point for ids that are NOT trace/span ids: the layout is " +
			"[function code 7][channel 6][sub-code 4][customer no 10][date YYYYMMDD][time HHMMSSsss][sequence digits], e.g. the synthetic " +
			"ABCD0010599310513000000004220260817093440812086. " +
			"The id CARRIES its own timestamp, so there is no range_s argument: the search window is derived from the id (±10 minutes around its " +
			"embedded time, read in the deployment's configured local timezone) and echoed back as window_from / window_to. " +
			"COST: one single log search inside that narrow window — never widen it by calling repeatedly. " +
			"HOW IT RESOLVES: it searches the LOGS for the id and reads the trace context of the matching line, so it only works when the id appears " +
			"in the log body. Not finding it is NOT an error: you get found=false plus the likely reasons. partial=true means the log backend timed " +
			"out or lost shards, so found=false is not proof of absence. " +
			"Never invent a trace id, a customer or a timing when found=false.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"request_id": map[string]any{
					"type": "string",
					"description": "The request id EXACTLY as the operator pasted it — one token, no spaces or separators, at least 47 characters. " +
						"A 32-hex string is a TRACE id (pass it to get_trace) and a 16-hex string is a SPAN id (use find_trace_by_span).",
				},
			},
			"required": []string{"request_id"},
		},
		// Salt-okunur; REST eşleri (GET /api/logs, GET /api/traces) viewer'a
		// açık ve uygulama içi sohbet hızlı yolu aynı okumayı yapıyor.
		MinRole: "",
		Handler: func(ctx context.Context, raw json.RawMessage) (any, error) {
			var a findTraceByRequestIDArgs
			if len(raw) > 0 {
				if err := json.Unmarshal(raw, &a); err != nil {
					return nil, fmt.Errorf("decode args: %w", err)
				}
			}
			token := strings.TrimSpace(a.RequestID)
			if token == "" {
				return nil, fmt.Errorf("request_id zorunlu")
			}
			if d.LogStore == nil {
				return nil, fmt.Errorf("log backend not configured")
			}
			// ŞEKİL hatası ile BULUNAMAMA farklı şeyler (find_trace_by_span
			// emsali): şekil hatası tarama bile yapmadan self-correct
			// edilebilir bir çağıran hatası, bulunamama dürüst kanıt.
			// Nil kontrolü AÇIK: nil bir *chstore.Store arayüze
			// sarıldığında arayüz nil OLMAZ ve çağrı panikler.
			var sr reqid.SettingReader
			if d.Store != nil {
				sr = d.Store
			}
			loc := reqid.LocationFrom(ctx, sr)
			id, ok := reqid.Parse(token, loc)
			if !ok {
				// Metnin İÇİNE gömülü kimliği de kabul et: model bazen
				// operatörün cümlesini olduğu gibi geçiriyor.
				if found, fok := reqid.Find(token, loc); fok {
					id, ok = found, true
				}
			}
			if !ok {
				if isHexLen(strings.ToLower(token), 32) {
					return nil, fmt.Errorf("%q 32 hex, yani bir TRACE id — doğrudan get_trace'e geçir", token)
				}
				if isHexLen(strings.ToLower(token), 16) {
					return nil, fmt.Errorf("%q 16 hex, yani bir SPAN id — find_trace_by_span kullan", token)
				}
				return nil, fmt.Errorf("request_id yapılandırılmış biçime uymuyor (%d karakter): "+
					"[fonksiyon 7 alnum][kanal 6][alt kod 4][müşteri no 10][tarih YYYYMMDD][saat HHMMSSsss][sequence ≥3 rakam], "+
					"en az %d karakter; tarih/saat alanları da geçerli olmalı", len(token), reqid.MinLen)
			}
			res, err := reqid.Resolve(ctx, d.LogStore, id)
			if err != nil {
				return nil, err
			}
			return requestIDLookupPayload(res), nil
		},
	}
}
