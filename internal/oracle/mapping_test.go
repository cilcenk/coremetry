package oracle

// mapping_test.go — v0.10.600 (Oracle Aşama 2, audit §5 + test listesi
// "mapping_test.go: eşleme, trace_id normalize, TZ"). Saf; Oracle yok.
//
// Sözleşmeler: audit §5 kolon listesi birebir · dilimsiz TIMESTAMP kaynağın
// diliminde yorumlanır (İstanbul 12:00 → 09:00Z), dilimli kipte an korunur ·
// trace_id normalize (büyük harf/tire/0x → 32 hex; geçersiz → boş + sayaç +
// ham attribute) · severity OTel eşlemesi, boş → ERROR · tüketilmeyen kolon
// verbatim attribute, boş hücre atlanır, sıralı · row_id harita sırasından
// bağımsız, içerikle değişir · Columns geçersiz kılma büyük/küçük harf
// duyarsız, "" alanı kapatır, timestamp kapatılamaz, bilinmeyen alan/kötü
// identifier/kötü TZ hata.

import (
	"strings"
	"testing"
	"time"
)

func baseSrc() SourceConfig {
	return SourceConfig{ID: "o-11111111", Name: "prod-eu"}
}

func sampleRow() map[string]any {
	return map[string]any{
		"ERR_TIMESTAMP":     time.Date(2026, 9, 10, 12, 0, 0, 500, time.UTC), // sürücü UTC etiketiyle duvar saati verir
		"ERR_SEVERITY":      "E",
		"ERR_MESSAGE":       "ORA-01555: snapshot too old",
		"ERR_TRACEID":       "4BF92F35-77B3-4DA6-A3CE-929D0E0E4736",
		"ERR_HOSTNAME":      "app-01",
		"ERR_INSTANCE_ID":   "shop-payment-7f9c",
		"ERR_SERVICE":       "PAY_TRANSFER",
		"ERR_CODE":          "REDACTED",
		"ERR_EXTERNAL_CODE": "X-12",
		"ERR_TYPE":          "T",
		"ERR_CHANNELCODE":   "MOB",
		"ERR_TASKCODE":      "TASK9",
		"ERR_REQUESTID":     "req-42",
		"ERR_CUSTOMERID":    "c-1",
		"ERR_TELLERID":      "",
		"ERR_LOCATION":      "ist",
		"ERR_SQLTEXT":       "select 1",
		"ERR_EMPTY":         nil,
		"ERR_NUM":           int64(7),
	}
}

func TestDefaultColumnsMatchAudit(t *testing.T) {
	want := map[string]string{
		"timestamp": "ERR_TIMESTAMP", "severity": "ERR_SEVERITY", "message": "ERR_MESSAGE",
		"traceId": "ERR_TRACEID", "host": "ERR_HOSTNAME", "instance": "ERR_INSTANCE_ID",
		"service": "ERR_SERVICE", "code": "ERR_CODE", "externalCode": "ERR_EXTERNAL_CODE",
		"type": "ERR_TYPE", "channel": "ERR_CHANNELCODE", "task": "ERR_TASKCODE",
		"requestId": "ERR_REQUESTID", "customerId": "ERR_CUSTOMERID", "tellerId": "ERR_TELLERID",
		"location": "ERR_LOCATION",
	}
	got := DefaultColumns()
	if len(got) != len(want) || len(fieldOrder) != len(want) {
		t.Fatalf("alan sayısı: got %d, fieldOrder %d, want %d", len(got), len(fieldOrder), len(want))
	}
	for f, c := range want {
		if got[f] != c {
			t.Errorf("%s: %q, want %q", f, got[f], c)
		}
	}
}

func TestMapNaiveTimestampInSourceTimezone(t *testing.T) {
	m, err := NewMapper(baseSrc()) // Timezone boş → Europe/Istanbul
	if err != nil {
		t.Fatal(err)
	}
	r, ok, bad := m.Map(sampleRow())
	if !ok || bad {
		t.Fatalf("ok=%v bad=%v", ok, bad)
	}
	// İstanbul 12:00 (UTC+3, DST yok) → 09:00Z; nanosaniye korunur.
	if want := time.Date(2026, 9, 10, 9, 0, 0, 500, time.UTC); !r.Time.Equal(want) {
		t.Errorf("time = %v, want %v", r.Time, want)
	}
	if r.Time.Location() != time.UTC {
		t.Errorf("CH'ye UTC gitmeli, %v", r.Time.Location())
	}
}

func TestMapZonedTimestampKeptAsInstant(t *testing.T) {
	src := baseSrc()
	src.TimestampHasZone = true
	// Kaynak dilimi UTC, değer +03:00: dilimli kipte AN korunur (09:00Z);
	// dilimsiz kip yanlışlıkla uygulansaydı duvar saati UTC'de yorumlanır ve
	// 12:00Z çıkardı — iki yol AYRIŞIR (ilk yazım İstanbul/İstanbul'du, ayrışmıyordu).
	src.Timezone = "UTC"
	m, err := NewMapper(src)
	if err != nil {
		t.Fatal(err)
	}
	row := sampleRow()
	ist := time.FixedZone("IST", 3*3600)
	row["ERR_TIMESTAMP"] = time.Date(2026, 9, 10, 12, 0, 0, 0, ist)
	r, ok, _ := m.Map(row)
	if !ok {
		t.Fatal("ok=false")
	}
	if want := time.Date(2026, 9, 10, 9, 0, 0, 0, time.UTC); !r.Time.Equal(want) {
		t.Errorf("time = %v, want %v", r.Time, want)
	}
}

func TestMapTimezoneOverrideAndStringTimestamps(t *testing.T) {
	src := baseSrc()
	src.Timezone = "UTC"
	m, err := NewMapper(src)
	if err != nil {
		t.Fatal(err)
	}
	cases := map[string]time.Time{
		"2026-09-10T12:00:00.25":      time.Date(2026, 9, 10, 12, 0, 0, 250_000_000, time.UTC),
		"2026-09-10 12:00:00":         time.Date(2026, 9, 10, 12, 0, 0, 0, time.UTC),
		"2026-09-10T12:00:00+03:00":   time.Date(2026, 9, 10, 9, 0, 0, 0, time.UTC), // dilimli metin dilimine uyar
		"2026-09-10T12:00:00.000001Z": time.Date(2026, 9, 10, 12, 0, 0, 1000, time.UTC),
	}
	for in, want := range cases {
		row := sampleRow()
		row["ERR_TIMESTAMP"] = in
		r, ok, _ := m.Map(row)
		if !ok {
			t.Errorf("%q: ok=false", in)
			continue
		}
		if !r.Time.Equal(want) {
			t.Errorf("%q: %v, want %v", in, r.Time, want)
		}
	}
	for _, in := range []any{"", "dün", nil, time.Time{}, 12345, "2026-13-40 99:00:00"} {
		row := sampleRow()
		row["ERR_TIMESTAMP"] = in
		if _, ok, _ := m.Map(row); ok {
			t.Errorf("%v: zamansız satır DÜŞMELİ", in)
		}
	}
	delete(sampleRow(), "ERR_TIMESTAMP")
	row := sampleRow()
	delete(row, "ERR_TIMESTAMP")
	if _, ok, _ := m.Map(row); ok {
		t.Error("timestamp kolonu yoksa satır düşmeli")
	}
}

func TestMapFieldsAndExtras(t *testing.T) {
	m, _ := NewMapper(baseSrc())
	r, ok, bad := m.Map(sampleRow())
	if !ok || bad {
		t.Fatalf("ok=%v bad=%v", ok, bad)
	}
	if r.SourceID != "o-11111111" {
		t.Errorf("source_id %q", r.SourceID)
	}
	if r.TraceID != "4bf92f3577b34da6a3ce929d0e0e4736" {
		t.Errorf("trace_id normalize: %q", r.TraceID)
	}
	if r.SeverityNum != 17 || r.SeverityText != "ERROR" {
		t.Errorf("severity E → 17/ERROR, got %d/%s", r.SeverityNum, r.SeverityText)
	}
	if r.Body != "ORA-01555: snapshot too old" || r.OperationCode != "PAY_TRANSFER" || r.ErrorCode != "REDACTED" ||
		r.ExternalCode != "X-12" || r.ErrorType != "T" || r.ChannelCode != "MOB" || r.TaskCode != "TASK9" ||
		r.RequestID != "req-42" || r.CustomerID != "c-1" || r.TellerID != "" || r.Location != "ist" ||
		r.HostName != "app-01" || r.InstanceID != "shop-payment-7f9c" || r.SpanID != "" {
		t.Errorf("alanlar: %+v", r)
	}
	// Ekstralar: tüketilmeyen ve boş olmayan kolonlar, sıralı, verbatim ad.
	if strings.Join(r.AttrKeys, ",") != "ERR_NUM,ERR_SQLTEXT" {
		t.Errorf("attr_keys %v", r.AttrKeys)
	}
	if strings.Join(r.AttrValues, ",") != "7,select 1" {
		t.Errorf("attr_values %v", r.AttrValues)
	}
}

func TestMapBadTraceIDKeptAsAttribute(t *testing.T) {
	m, _ := NewMapper(baseSrc())
	for _, in := range []string{"abc", "4bf92f3577b34da6a3ce929d0e0e473", "zzf92f3577b34da6a3ce929d0e0e4736", "0x4bf92f3577b34da6a3ce929d0e0e47361"} {
		row := sampleRow()
		row["ERR_TRACEID"] = in
		r, ok, bad := m.Map(row)
		if !ok || !bad {
			t.Errorf("%q: ok=%v bad=%v (geçersiz id satırı düşürmez, işaretler)", in, ok, bad)
			continue
		}
		if r.TraceID != "" {
			t.Errorf("%q: trace_id boş olmalı, %q", in, r.TraceID)
		}
		found := false
		for i, k := range r.AttrKeys {
			if k == "ERR_TRACEID" && r.AttrValues[i] == in {
				found = true
			}
		}
		if !found {
			t.Errorf("%q: ham değer attribute olarak kalmalı: %v", in, r.AttrKeys)
		}
	}
	// Boş id: geçerli (pivotsuz), sayılmaz, attribute yok.
	row := sampleRow()
	row["ERR_TRACEID"] = "  "
	r, ok, bad := m.Map(row)
	if !ok || bad || r.TraceID != "" {
		t.Errorf("boş id: ok=%v bad=%v id=%q", ok, bad, r.TraceID)
	}
	for _, k := range r.AttrKeys {
		if k == "ERR_TRACEID" {
			t.Error("boş id attribute'a girmez")
		}
	}
	// 0x öneki + tire kabul.
	row["ERR_TRACEID"] = "0x4BF92F35-77B3-4DA6-A3CE-929D0E0E4736"
	if r, _, bad := m.Map(row); bad || r.TraceID != "4bf92f3577b34da6a3ce929d0e0e4736" {
		t.Errorf("0x+tire: bad=%v id=%q", bad, r.TraceID)
	}
}

func TestMapSeverity(t *testing.T) {
	cases := []struct {
		in   string
		num  uint8
		text string
	}{
		{"", 17, "ERROR"}, {"E", 17, "ERROR"}, {"error", 17, "ERROR"}, {"ERR", 17, "ERROR"},
		{"W", 13, "WARN"}, {"warning", 13, "WARN"}, {"I", 9, "INFO"}, {"D", 5, "DEBUG"},
		{"F", 21, "FATAL"}, {"CRITICAL", 21, "FATAL"}, {"SEVERE", 21, "FATAL"},
		{"17", 17, "ERROR"}, {"13", 13, "WARN"}, {"24", 24, "FATAL"}, {"1", 1, "TRACE"},
		{"0", 17, "0"}, {"25", 17, "25"}, {"PANIC", 17, "PANIC"},
	}
	for _, c := range cases {
		n, tx := mapSeverity(c.in)
		if n != c.num || tx != c.text {
			t.Errorf("%q: %d/%s, want %d/%s", c.in, n, tx, c.num, c.text)
		}
	}
}

func TestRowIDStableAndContentSensitive(t *testing.T) {
	m, _ := NewMapper(baseSrc())
	a, _, _ := m.Map(sampleRow())
	b, _, _ := m.Map(sampleRow())
	if a.RowID == 0 || a.RowID != b.RowID {
		t.Fatalf("aynı satır aynı kimlik: %d vs %d", a.RowID, b.RowID)
	}
	row := sampleRow()
	row["ERR_MESSAGE"] = "ORA-01555: snapshot too old!"
	c, _, _ := m.Map(row)
	if c.RowID == a.RowID {
		t.Error("gövde değişince kimlik değişmeli")
	}
	row = sampleRow()
	row["ERR_SQLTEXT"] = "select 2"
	d, _, _ := m.Map(row)
	if d.RowID == a.RowID {
		t.Error("ekstra attribute değişince kimlik değişmeli")
	}
	row = sampleRow()
	row["ERR_TIMESTAMP"] = time.Date(2026, 9, 10, 12, 0, 1, 0, time.UTC)
	e, _, _ := m.Map(row)
	if e.RowID == a.RowID {
		t.Error("zaman değişince kimlik değişmeli")
	}
	// Farklı kaynak, aynı içerik → farklı kimlik (iki Oracle'dan aynı satır).
	src := baseSrc()
	src.ID = "o-22222222"
	m2, _ := NewMapper(src)
	f, _, _ := m2.Map(sampleRow())
	if f.RowID == a.RowID {
		t.Error("kaynak kimliğe girer")
	}
}

func TestColumnsOverrideCaseInsensitiveAndDisable(t *testing.T) {
	src := baseSrc()
	src.TimestampColumn = "ERR_TS"
	src.Columns = map[string]string{"traceId": "trace_ref", "tellerId": "", "message": "MSG"}
	m, err := NewMapper(src)
	if err != nil {
		t.Fatal(err)
	}
	row := map[string]any{
		"ERR_TS":            time.Date(2026, 9, 10, 12, 0, 0, 0, time.UTC),
		"TRACE_REF":         "4bf92f3577b34da6a3ce929d0e0e4736",
		"MSG":               "m",
		"ERR_TELLERID":  "t-9", // alan kapalı → tüketilmez → attribute'a düşer
		"ERR_MESSAGE":   "eski kolon, artık ekstra",
		"ERR_TIMESTAMP": "2026-01-01 00:00:00", // artık ekstra
	}
	r, ok, bad := m.Map(row)
	if !ok || bad {
		t.Fatalf("ok=%v bad=%v", ok, bad)
	}
	if r.TraceID != "4bf92f3577b34da6a3ce929d0e0e4736" || r.Body != "m" || r.TellerID != "" {
		t.Errorf("override: %+v", r)
	}
	if strings.Join(r.AttrKeys, ",") != "ERR_MESSAGE,ERR_TELLERID,ERR_TIMESTAMP" {
		t.Errorf("kapalı/eski kolonlar ekstra: %v", r.AttrKeys)
	}
}

func TestNewMapperRejectsBadConfig(t *testing.T) {
	bad := []SourceConfig{
		{ID: "o-1", Columns: map[string]string{"nope": "X"}},
		{ID: "o-1", Columns: map[string]string{"code": "1BAD"}},
		{ID: "o-1", Columns: map[string]string{"code": "A;B"}},
		{ID: "o-1", Columns: map[string]string{"timestamp": ""}},
		{ID: "o-1", Timezone: "Mars/Olympus"},
		{ID: "o-1", TimestampColumn: "bad-name"},
	}
	for i, src := range bad {
		if _, err := NewMapper(src); err == nil {
			t.Errorf("#%d: hata bekleniyordu: %+v", i, src)
		}
	}
	if _, err := NewMapper(SourceConfig{ID: "o-1", Timezone: "America/New_York", Columns: map[string]string{"location": "LOC$1"}}); err != nil {
		t.Errorf("geçerli ayar reddedildi: %v", err)
	}
}

func TestMapAllStats(t *testing.T) {
	m, _ := NewMapper(baseSrc())
	r1 := sampleRow()
	r2 := sampleRow()
	r2["ERR_TRACEID"] = "garbage"
	r3 := sampleRow()
	delete(r3, "ERR_TIMESTAMP")
	rows, st := m.MapAll([]map[string]any{r1, r2, r3})
	if len(rows) != 2 || st.Rows != 3 || st.Mapped != 2 || st.NoTimestamp != 1 || st.BadTraceID != 1 {
		t.Errorf("rows=%d stats=%+v", len(rows), st)
	}
	rows, st = m.MapAll(nil)
	if len(rows) != 0 || st.Rows != 0 {
		t.Errorf("boş parti: %d %+v", len(rows), st)
	}
}
