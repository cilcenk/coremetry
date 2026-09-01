package influx

// csv.go — InfluxDB 2.x annotated CSV çözücüsü (v0.10.222, audit K2).
//
// /api/v2/query, dialect.annotations = [group, datatype, default] ile
// şu biçimi döner:
//
//	#group,false,false,true,...
//	#datatype,string,long,dateTime:RFC3339,...
//	#default,_result,,,...
//	,result,table,_start,_stop,_time,_value,OPERATIONCODE,ERRORCODE
//	,,0,2026-…,2026-…,2026-…,42,OP1,E1
//	<boş satır>
//	#group,...            ← sonraki tablo, kendi başlığıyla
//
// encoding/csv boş satırı KAYIT OLARAK VERMEZ; tablo sınırı bu yüzden
// `#` açıklama satırından ve başlık-benzeri satırdan tanınır. Değerler
// ham string kalır (datatype tüketiciye: poller _value'yu float,
// _time'ı RFC3339 çözer). İlk sütun annotation sütunu (başlıkta boş ad)
// — kayda girmez. Hata zarfı (`error`,`reference` kolonlu tablo) Go
// hatasına çevrilir. influxdb-client-go bu işi ~6 transitif bağımlılıkla
// yapıyor; burası 80 satır + golden test (csv_test.go).

import (
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"strings"
)

// Record — bir sonuç satırı; Table Influx'un tablo sırası (0-tabanlı,
// başlık başına artar).
type Record struct {
	Table  int
	Values map[string]string
}

// ParseAnnotatedCSV — gövdeyi kayıtlara çözer. Boş gövde → 0 kayıt, hata
// yok (grubu olmayan sum() böyle döner; poller boş kümeyi "0" saymaz —
// bkz. audit R3, pad dedektörde).
func ParseAnnotatedCSV(r io.Reader) ([]Record, error) {
	cr := csv.NewReader(r)
	cr.FieldsPerRecord = -1
	cr.LazyQuotes = true
	out := []Record{}
	var header []string
	table := -1
	afterData := false
	for {
		rec, err := cr.Read()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("influx csv: %w", err)
		}
		if len(rec) == 0 {
			continue
		}
		if strings.HasPrefix(rec[0], "#") {
			// Açıklama satırı: veri gördükten sonra geliyorsa yeni tablo.
			if afterData {
				header = nil
				afterData = false
			}
			continue
		}
		if header == nil || (afterData && looksLikeHeader(rec)) {
			if !looksLikeHeader(rec) {
				continue // başlıksız/bozuk satır — boğulma, atla
			}
			header = rec
			table++
			afterData = false
			continue
		}
		afterData = true
		if ei, ri := indexOf(header, "error"), indexOf(header, "reference"); ei >= 0 && ri >= 0 {
			msg := ""
			if ei < len(rec) {
				msg = strings.TrimSpace(rec[ei])
			}
			if msg == "" {
				msg = "bilinmeyen Influx hatası"
			}
			return nil, fmt.Errorf("influx: %s", msg)
		}
		vals := make(map[string]string, len(header))
		for i, name := range header {
			if name == "" {
				continue
			}
			v := ""
			if i < len(rec) {
				v = rec[i]
			}
			vals[name] = v
		}
		out = append(out, Record{Table: table, Values: vals})
	}
	return out, nil
}

// looksLikeHeader — Influx başlıkları daima `result`+`table` (sonuç) ya da
// `error`+`reference` (hata zarfı) kolonlarını taşır.
func looksLikeHeader(rec []string) bool {
	return (indexOf(rec, "result") >= 0 && indexOf(rec, "table") >= 0) ||
		(indexOf(rec, "error") >= 0 && indexOf(rec, "reference") >= 0)
}

func indexOf(fields []string, name string) int {
	for i, f := range fields {
		if f == name {
			return i
		}
	}
	return -1
}
