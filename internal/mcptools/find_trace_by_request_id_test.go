package mcptools

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/cilcenk/coremetry/internal/logstore"
	"github.com/cilcenk/coremetry/internal/reqid"
)

// find_trace_by_request_id_test.go — v0.9.1142.
//
// TÜM DEĞERLER SENTETİK (fonksiyon kodu "ABCD001", müşteri
// "0000000042") — depo bir kurum/müşteri değeri taşımaz.
const (
	synthReqID   = "ABCD0010599310513000000004220260817093440812086"
	synthTraceID = "9fc37145182089354c2c20a1c63e0817"
	synthSpanID  = "00f067aa0ba902b7"
)

func synthResolution(t *testing.T) reqid.Resolution {
	t.Helper()
	id, ok := reqid.Parse(synthReqID, reqid.Location(""))
	if !ok {
		t.Fatalf("sentetik kimlik ayrıştırılamadı: %q", synthReqID)
	}
	return reqid.Resolution{ID: id}
}

// Gövde sözleşmesi: pencere HER İKİ hâlde de ifşa edilir, bulunamamak
// dürüst kanıttır, çoklu trace tek trace gibi sunulmaz.
func TestRequestIDLookupPayload(t *testing.T) {
	base := synthResolution(t)

	t.Run("bulunamadı — dürüst kanıt, pencere yankısı", func(t *testing.T) {
		body := requestIDLookupPayload(base)
		if body["found"] != false {
			t.Fatalf("found = %v, beklenen false", body["found"])
		}
		if _, ok := body["trace_id"]; ok {
			t.Error("bulunamayan çözümlemede trace_id alanı var — uydurmaya davet")
		}
		for _, k := range []string{"window_from", "window_to", "request_time", "reasons", "note"} {
			if body[k] == nil {
				t.Errorf("%s alanı yok — pencere/gerekçe ifşa edilmeli", k)
			}
		}
		reasons, _ := body["reasons"].([]string)
		if len(reasons) < 3 {
			t.Fatalf("gerekçe sayısı %d — en azından kopyalama/pencere/gövde-alanı", len(reasons))
		}
		joined := strings.ToLower(strings.Join(reasons, " "))
		// Pencere KİMLİKTEN geliyor: modelin "range_s'i büyüt" diye
		// olmayan bir arg'ı denemesini önleyen cümle burada.
		if !strings.Contains(joined, "window") {
			t.Error("gerekçeler pencereyi anmıyor")
		}
		if !strings.Contains(joined, "body") {
			t.Error("gerekçeler 'id log gövdesinde olmayabilir' sınırını anmıyor — ES query_string default_field body")
		}
	})

	t.Run("pencere kimliğin damgası ± 10dk", func(t *testing.T) {
		body := requestIDLookupPayload(base)
		from, to := base.ID.Window()
		if body["window_from"] != reqid.FmtLocal(from) || body["window_to"] != reqid.FmtLocal(to) {
			t.Fatalf("pencere yankısı yanlış: %v → %v", body["window_from"], body["window_to"])
		}
		if body["request_time"] != reqid.FmtLocal(base.ID.TS) {
			t.Fatalf("request_time = %v", body["request_time"])
		}
	})

	t.Run("çözüldü — trace + sonraki adım", func(t *testing.T) {
		res := base
		res.TraceID, res.SpanID, res.Service = synthTraceID, synthSpanID, "svc-a"
		res.MatchedLogs, res.DistinctTraces = 3, 1
		body := requestIDLookupPayload(res)
		if body["found"] != true || body["trace_id"] != synthTraceID {
			t.Fatalf("gövde: %+v", body)
		}
		if body["span_id"] != synthSpanID || body["service"] != "svc-a" {
			t.Fatalf("log satırının kanıtı taşınmadı: %+v", body)
		}
		if body["matched_logs"] != 3 {
			t.Fatalf("matched_logs = %v", body["matched_logs"])
		}
		if _, ok := body["ambiguous_note"]; ok {
			t.Error("tek trace'te belirsizlik notu var")
		}
		next, _ := body["next"].(string)
		if !strings.Contains(next, "get_trace") {
			t.Errorf("sonraki adım get_trace'i göstermiyor: %q", next)
		}
	})

	t.Run("çoklu trace tek trace gibi sunulmaz", func(t *testing.T) {
		res := base
		res.TraceID, res.MatchedLogs, res.DistinctTraces = synthTraceID, 4, 2
		body := requestIDLookupPayload(res)
		if body["distinct_traces"] != 2 {
			t.Fatalf("distinct_traces = %v", body["distinct_traces"])
		}
		if body["ambiguous_note"] == nil {
			t.Error("2 farklı trace'te belirsizlik notu yok")
		}
	})

	t.Run("kısmi cevap ifşa edilir", func(t *testing.T) {
		res := base
		res.Partial = true
		body := requestIDLookupPayload(res)
		if body["partial"] != true || body["partial_note"] == nil {
			t.Fatalf("kısmi cevap yutuldu: %+v", body)
		}
	})

	t.Run("kimliğin segmentleri okunur hâlde", func(t *testing.T) {
		body := requestIDLookupPayload(base)
		parsed, _ := body["parsed"].(map[string]any)
		if parsed == nil {
			t.Fatal("parsed bloğu yok")
		}
		for k, want := range map[string]string{
			"function_code": "ABCD001",
			"channel_code":  "059931",
			"sub_code":      "0513",
			"customer_no":   "0000000042",
			"sequence":      "086",
		} {
			if parsed[k] != want {
				t.Errorf("parsed[%s] = %v, beklenen %q", k, parsed[k], want)
			}
		}
	})
}

func TestFindTraceByRequestIDSchema(t *testing.T) {
	tool := toolByName(t, ToolList(Deps{}), "find_trace_by_request_id")
	props := schemaProps(t, tool)
	rid, ok := props["request_id"].(map[string]any)
	if !ok || rid["type"] != "string" {
		t.Fatalf("request_id property eksik/yanlış tip: %v", props["request_id"])
	}
	req, _ := tool.InputSchema["required"].([]string)
	if len(req) != 1 || req[0] != "request_id" {
		t.Fatalf("required = %v, beklenen [request_id]", tool.InputSchema["required"])
	}
	// Salt-okunur → viewer tabanı.
	if tool.MinRole != "" {
		t.Errorf("MinRole = %q — okuma salt-okunur ve REST eşleri viewer'a açık", tool.MinRole)
	}
	d := tool.Description
	// Açıklama SÖZLEŞME: pencerenin kimlikten geldiğini ve tek arama
	// olduğunu söylemek zorunda, yoksa model pencere tahmin etmeye
	// ya da tekrar tekrar aramaya başlar.
	for _, want := range []string{"window", "±10", "found=false"} {
		if !strings.Contains(d, want) {
			t.Errorf("açıklama %q içermiyor", want)
		}
	}
	// Şemada range_s YOK; açıklama bunu AÇIKÇA söylemeli. Sessiz yokluk
	// modeli arg'ı denemeye (ve yok sayıldığını sanmaya) bırakırdı.
	if _, ok := props["range_s"]; ok {
		t.Error("range_s property eklendi — pencere kimliğin damgasından geliyor")
	}
	if !strings.Contains(d, "no range_s argument") {
		t.Error("açıklama 'no range_s argument' demiyor — pencerenin kimlikten geldiği söylenmeli")
	}
}

// Handler ŞEKİL hatasını BULUNAMAMADAN ayırır: şekil hatası çağıranın
// düzeltebileceği bir hata, bulunamama dürüst kanıt (find_trace_by_span
// emsali). Ayrım kaybolursa model ya döngüye girer ya kesin konuşur.
func TestFindTraceByRequestIDHandlerShapeErrors(t *testing.T) {
	ls := &stubLogStore{page: &logstore.Page{}}
	tool := findTraceByRequestIDTool(Deps{LogStore: ls})
	cases := []struct {
		name string
		arg  string
		want string
	}{
		{"boş", "", "zorunlu"},
		{"32-hex trace id", synthTraceID, "get_trace"},
		{"16-hex span id", synthSpanID, "find_trace_by_span"},
		{"biçimsiz", "ABC-123", "yapılandırılmış biçime uymuyor"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			raw, _ := json.Marshal(findTraceByRequestIDArgs{RequestID: c.arg})
			_, err := tool.Handler(context.Background(), raw)
			if err == nil {
				t.Fatalf("şekil hatası kabul edildi: %q", c.arg)
			}
			if !strings.Contains(err.Error(), c.want) {
				t.Fatalf("hata mesajı %q, %q beklenmişti", err.Error(), c.want)
			}
			if ls.gotFilter.Search != "" {
				t.Fatal("şekil hatasında ARAMA yapıldı — tarama öncesi elenmeli")
			}
		})
	}
}

// Çözümleme yolu: pencere kimlikten kurulur, tek arama yapılır ve
// bulunamama HATA DEĞİL.
func TestFindTraceByRequestIDHandlerResolves(t *testing.T) {
	id, _ := reqid.Parse(synthReqID, reqid.Location(""))
	ls := &stubLogStore{page: &logstore.Page{Logs: []*logstore.LogRecord{
		{TraceID: synthTraceID, SpanID: synthSpanID, ServiceName: "svc-a", Timestamp: 7},
	}}}
	tool := findTraceByRequestIDTool(Deps{LogStore: ls})
	raw, _ := json.Marshal(findTraceByRequestIDArgs{RequestID: "  " + synthReqID + "  "})
	out, err := tool.Handler(context.Background(), raw)
	if err != nil {
		t.Fatalf("hata: %v", err)
	}
	body, _ := out.(map[string]any)
	if body["found"] != true || body["trace_id"] != synthTraceID {
		t.Fatalf("gövde: %+v", body)
	}
	if ls.gotFilter.Search != synthReqID {
		t.Fatalf("aranan metin %q — trim'lenmiş ORİJİNAL token olmalı", ls.gotFilter.Search)
	}
	wantFrom, wantTo := id.Window()
	if !ls.gotFilter.From.Equal(wantFrom) || !ls.gotFilter.To.Equal(wantTo) {
		t.Fatalf("pencere kimlikten gelmiyor: %s → %s", ls.gotFilter.From, ls.gotFilter.To)
	}
	if ls.gotFilter.Limit > 20 {
		t.Fatalf("LIMIT %d — küçük kalmalı", ls.gotFilter.Limit)
	}

	// Cümlenin içine gömülü kimlik de kabul edilir (model bazen
	// operatörün mesajını olduğu gibi geçiriyor).
	raw2, _ := json.Marshal(findTraceByRequestIDArgs{RequestID: "şu isteğe ne oldu " + synthReqID + " ?"})
	if _, err := tool.Handler(context.Background(), raw2); err != nil {
		t.Fatalf("gömülü kimlik reddedildi: %v", err)
	}

	// Eşleşme yok → found=false, HATA DEĞİL.
	ls.page = &logstore.Page{}
	out3, err := tool.Handler(context.Background(), raw)
	if err != nil {
		t.Fatalf("bulunamamak hata döndürdü: %v", err)
	}
	if body3, _ := out3.(map[string]any); body3["found"] != false {
		t.Fatalf("gövde: %+v", body3)
	}
}

// KAYNAK PİNİ: handler gerçekten saf üreticiden dönmeli (discovery.go
// emsali) ve arama SONRASINDA hata üretmemeli — "bulunamadı = hata"
// mutasyonunun imzası budur.
func TestFindTraceByRequestIDReturnsViaPurePayload(t *testing.T) {
	b, err := os.ReadFile("find_trace_by_request_id.go")
	if err != nil {
		t.Fatalf("kaynak okunamadı: %v", err)
	}
	src := stripLineComments(string(b))
	i := strings.Index(src, "reqid.Resolve(ctx")
	if i < 0 {
		t.Fatal("reqid.Resolve çağrısı yok — tool başka bir okumaya mı bağlandı?")
	}
	tail := src[i:]
	if !strings.Contains(tail, "requestIDLookupPayload(") {
		t.Fatal("arama sonrası gövde requestIDLookupPayload'dan geçmiyor")
	}
	if strings.Contains(tail, "Errorf") {
		t.Error("arama SONRASINDA hata üretiliyor — bulunamamak dürüst kanıttır (found=false)")
	}
	// Pencere kimlikten: rangeWindow (ŞİMDİye çapalı) bu tool'da
	// kullanılmamalı, yoksa geçmiş bir isteğin penceresi sessizce
	// bugüne kayar.
	if strings.Contains(src, "rangeWindow(") {
		t.Error("rangeWindow kullanılmış — pencere kimliğin damgasına çapalı olmalı, ŞİMDİye değil")
	}
}
