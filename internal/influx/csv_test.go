package influx

import (
	"strings"
	"testing"
)

// csv_test.go — v0.10.222 (Influx D1, docs/audit/influx-integration.md §2).
//
// Sözleşme: InfluxDB 2.x /api/v2/query'nin ANNOTATED CSV cevabı (dialect
// annotations: group/datatype/default) tablo tablo satırlara çözülür.
//   • `#…` ile başlayan satırlar açıklama — atlanır (datatype bilgisi
//     tüketiciye bırakılır; değerler ham string kalır).
//   • İlk hücre annotation sütunu (başlıkta boş ad) — atlanır.
//   • Boş satır tablo sınırı; sonraki başlık satırı yeni kolon kümesi.
//   • `error` + `reference` kolonlu tablo Influx'un hata zarfı → error.
//   • Boş gövde → 0 kayıt, hata YOK (grubu olmayan sum() böyle döner;
//     memory: boş küme kaybolur, sıfır olmaz — burada kayıp bilinçli).

const sampleCSV = `#group,false,false,true,true,false,false,true,true
#datatype,string,long,dateTime:RFC3339,dateTime:RFC3339,dateTime:RFC3339,double,string,string
#default,_result,,,,,,,
,result,table,_start,_stop,_time,_value,OPERATIONCODE,ERRORCODE
,,0,2026-09-01T10:00:00Z,2026-09-01T10:02:00Z,2026-09-01T10:02:00Z,42,OP1,E1
,,0,2026-09-01T10:00:00Z,2026-09-01T10:02:00Z,2026-09-01T10:02:00Z,7,OP1,E2

#group,false,false,true,true,false,false,true,true
#datatype,string,long,dateTime:RFC3339,dateTime:RFC3339,dateTime:RFC3339,double,string,string
#default,_result,,,,,,,
,result,table,_start,_stop,_time,_value,OPERATIONCODE,ERRORCODE
,,1,2026-09-01T10:00:00Z,2026-09-01T10:02:00Z,2026-09-01T10:02:00Z,3,OP2,E1
`

func TestParseAnnotatedCSV_TwoTables(t *testing.T) {
	recs, err := ParseAnnotatedCSV(strings.NewReader(sampleCSV))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(recs) != 3 {
		t.Fatalf("want 3 records, got %d: %+v", len(recs), recs)
	}
	if recs[0].Values["OPERATIONCODE"] != "OP1" || recs[0].Values["_value"] != "42" {
		t.Fatalf("record 0 wrong: %+v", recs[0].Values)
	}
	if recs[2].Table != 1 || recs[2].Values["ERRORCODE"] != "E1" || recs[2].Values["OPERATIONCODE"] != "OP2" {
		t.Fatalf("record 2 (second table) wrong: %+v", recs[2])
	}
	if _, has := recs[0].Values[""]; has {
		t.Fatalf("annotation column must not leak as an empty-named key: %+v", recs[0].Values)
	}
	if recs[0].Values["_time"] != "2026-09-01T10:02:00Z" {
		t.Fatalf("_time kept raw RFC3339: %q", recs[0].Values["_time"])
	}
}

func TestParseAnnotatedCSV_ErrorEnvelope(t *testing.T) {
	body := `#datatype,string,string
#group,true,true
#default,,
,error,reference
,"compilation failed: error at @3:5-3:6: invalid expression",897
`
	_, err := ParseAnnotatedCSV(strings.NewReader(body))
	if err == nil || !strings.Contains(err.Error(), "compilation failed") {
		t.Fatalf("Influx error envelope must surface as an error; got %v", err)
	}
}

func TestParseAnnotatedCSV_EmptyAndGarbage(t *testing.T) {
	recs, err := ParseAnnotatedCSV(strings.NewReader(""))
	if err != nil || len(recs) != 0 {
		t.Fatalf("empty body → 0 records, nil error; got %d, %v", len(recs), err)
	}
	// Başlıksız veri satırı: boğulmak yerine atlanır (kısmi/bozuk cevap).
	recs, err = ParseAnnotatedCSV(strings.NewReader(",,0,1,2\n"))
	if err != nil || len(recs) != 0 {
		t.Fatalf("headerless rows are skipped; got %d, %v", len(recs), err)
	}
	// Kolon sayısı başlıktan kısa satır: eksikler boş string.
	recs, err = ParseAnnotatedCSV(strings.NewReader(",result,table,_value,X\n,,0,5\n"))
	if err != nil || len(recs) != 1 || recs[0].Values["X"] != "" || recs[0].Values["_value"] != "5" {
		t.Fatalf("short row pads with empty; got %+v, %v", recs, err)
	}
}
