package oracle

// mapping.go — v0.10.600 (Oracle Aşama 2, audit §5). SAF: Oracle satırı
// (kolon adı → hücre) → chstore.OracleErrorRow. Hiç I/O yok; poller
// (Aşama 2 devamı) bunu çağırır, testler doğrudan pinler.
//
// Sözleşmeler:
//   - Kolon eşlemesi AYARDAN: DefaultColumns() audit §5'in listesi; kaynak
//     başına `Columns{alan: KOLON}` geçersiz kılar, "" = "bu alan tabloda
//     yok" (alan atlanır). TimestampColumn/TypeColumn (Aşama 1) aynı yerden
//     beslenir — iki ayrı gerçek yok.
//   - Zaman: MCA_ERR_TIMESTAMP çoğu kurulumda düz TIMESTAMP (dilimsiz).
//     Sürücü onu bir time.Time'a koyar ama hangi Location'la koyduğu
//     GÜVENİLMEZ; dilimsiz kipte DUVAR SAATİ bileşenleri alınıp kaynağın
//     Timezone'unda (varsayılan Europe/Istanbul) yeniden yorumlanır.
//     TimestampHasZone=true ise sürücünün verdiği an olduğu gibi UTC'ye
//     çevrilir (TIMESTAMP WITH TIME ZONE). Yanlış kip = sabit 3 saat kayma;
//     Settings testinin geniş-pencere ipucu (emptyProbeHint) bunu yakalar.
//   - trace_id yazma anında normalize (audit §5 tuzağı): 32 hex, küçük
//     harf, tire/0x soyulur; geçmeyen değer BOŞ bırakılır, SAYILIR ve ham
//     hâli attribute olarak KALIR (kaybolmaz, yalnız pivotlanmaz).
//   - Tüketilmeyen her kolon attr_keys/attr_values'a verbatim (boş hücreler
//     atlanır — boş attribute bilgi taşımaz). Anahtarlar sıralı → row_id
//     harita sırasından bağımsız.
//   - row_id: FNV-1a 64 (source_id + zaman + tüm alanlar + ekstralar).
//     Timezone değişirse aynı Oracle satırı yeni kimlik alır — bilinçli:
//     eski satırlar TTL ile gider, yeni kip doğru zamanla yazar.
//
// tzdata gömülü: alpine imajında zoneinfo yok; `time/tzdata` olmadan
// LoadLocation("Europe/Istanbul") prod'da düşer, lokalde geçerdi.

import (
	"fmt"
	"hash/fnv"
	"sort"
	"strconv"
	"strings"
	"time"
	_ "time/tzdata"

	"github.com/cilcenk/coremetry/internal/chstore"
)

// DefaultTimezone — dilimsiz TIMESTAMP'in yorumlandığı yer (operatör 2026-09-10:
// ayarlanabilir, varsayılan İstanbul).
const DefaultTimezone = "Europe/Istanbul"

// Alan anahtarları — SourceConfig.Columns'ın SOL tarafı (ayar JSON'unda
// görünür, FE eşleme formu bunları listeler).
const (
	FieldTimestamp    = "timestamp"
	FieldSeverity     = "severity"
	FieldMessage      = "message"
	FieldTraceID      = "traceId"
	FieldHost         = "host"
	FieldInstance     = "instance"
	FieldService      = "service"
	FieldCode         = "code"
	FieldExternalCode = "externalCode"
	FieldType         = "type"
	FieldChannel      = "channel"
	FieldTask         = "task"
	FieldRequestID    = "requestId"
	FieldCustomerID   = "customerId"
	FieldTellerID     = "tellerId"
	FieldLocation     = "location"
)

// fieldOrder — row_id ve doğrulama için SABİT sıra (harita sırası değil).
var fieldOrder = []string{
	FieldTimestamp, FieldSeverity, FieldMessage, FieldTraceID, FieldHost, FieldInstance,
	FieldService, FieldCode, FieldExternalCode, FieldType, FieldChannel, FieldTask,
	FieldRequestID, FieldCustomerID, FieldTellerID, FieldLocation,
}

// DefaultColumns — audit §5 tablosu birebir. Her çağrı yeni harita (çağıran
// değiştirebilir).
func DefaultColumns() map[string]string {
	return map[string]string{
		FieldTimestamp:    DefaultTimestampColumn,
		FieldSeverity:     "MCA_ERR_SEVERITY",
		FieldMessage:      "MCA_ERR_MESSAGE",
		FieldTraceID:      "MCA_ERR_TRACEID",
		FieldHost:         "MCA_ERR_HOSTNAME",
		FieldInstance:     "MCA_ERR_INSTANCE_ID",
		FieldService:      "MCA_ERR_SERVICE",
		FieldCode:         "MCA_ERR_CODE",
		FieldExternalCode: "MCA_ERR_EXTERNAL_CODE",
		FieldType:         DefaultTypeColumn,
		FieldChannel:      "MCA_ERR_CHANNELCODE",
		FieldTask:         "MCA_ERR_TASKCODE",
		FieldRequestID:    "MCA_ERR_REQUESTID",
		FieldCustomerID:   "MCA_ERR_CUSTOMERID",
		FieldTellerID:     "MCA_ERR_TELLERID",
		FieldLocation:     "MCA_ERR_LOCATION",
	}
}

// ResolveColumns — SAF: varsayılan + Aşama 1 kolonları + Columns geçersiz
// kılmaları. Bilinmeyen alan anahtarı ya da identifier olmayan kolon adı
// HATA (ayar kaydında yakalanır, poller'da değil). "" değer alanı KAPATIR.
// timestamp kapatılamaz — zamanı olmayan satır yazılamaz.
func ResolveColumns(src SourceConfig) (map[string]string, error) {
	cols := DefaultColumns()
	if src.TimestampColumn != "" {
		cols[FieldTimestamp] = src.TimestampColumn
	}
	if src.TypeColumn != "" {
		cols[FieldType] = src.TypeColumn
	}
	for field, col := range src.Columns {
		if _, known := cols[field]; !known {
			return nil, fmt.Errorf("columns: bilinmeyen alan %q (geçerli: %s)", field, strings.Join(fieldOrder, ", "))
		}
		cols[field] = strings.TrimSpace(col)
	}
	if cols[FieldTimestamp] == "" {
		return nil, fmt.Errorf("columns: %s kapatılamaz", FieldTimestamp)
	}
	for _, field := range fieldOrder {
		if c := cols[field]; c != "" && !identRe.MatchString(c) {
			return nil, fmt.Errorf("columns.%s: Oracle identifier'ı olmalı: %q", field, c)
		}
	}
	return cols, nil
}

// ResolveLocation — SAF: Timezone boşsa DefaultTimezone; yüklenemeyen ad HATA.
func ResolveLocation(src SourceConfig) (*time.Location, error) {
	name := strings.TrimSpace(src.Timezone)
	if name == "" {
		name = DefaultTimezone
	}
	loc, err := time.LoadLocation(name)
	if err != nil {
		return nil, fmt.Errorf("timezone %q: %v", name, err)
	}
	return loc, nil
}

// MapStats — bir partinin eşleme özeti; durum satırına ve log'a gider.
// NoTimestamp satır DÜŞER (zamansız satır yazılamaz); BadTraceID satır
// KALIR (trace pivotu olmadan).
type MapStats struct {
	Rows        int
	Mapped      int
	NoTimestamp int
	BadTraceID  int
}

// Mapper — bir kaynağın çözülmüş eşlemesi. NewMapper ayarı bir kez çözer;
// Map/MapAll saf.
type Mapper struct {
	sourceID string
	loc      *time.Location
	hasZone  bool
	cols     map[string]string // alan → KOLON (boş = kapalı)
	byCol    map[string]string // upper(kolon) → alan
}

func NewMapper(src SourceConfig) (*Mapper, error) {
	cols, err := ResolveColumns(src)
	if err != nil {
		return nil, err
	}
	loc, err := ResolveLocation(src)
	if err != nil {
		return nil, err
	}
	m := &Mapper{sourceID: src.ID, loc: loc, hasZone: src.TimestampHasZone, cols: cols, byCol: map[string]string{}}
	for field, col := range cols {
		if col != "" {
			m.byCol[strings.ToUpper(col)] = field
		}
	}
	return m, nil
}

// Map — tek satır. ok=false → satır düşer (zaman yok/çözülemedi).
// badTrace → trace_id geçersizdi, boş yazıldı, ham değer attribute'ta.
func (m *Mapper) Map(row map[string]any) (r chstore.OracleErrorRow, ok bool, badTrace bool) {
	// Sürücü kolon adlarını büyük harf döndürür; ayar herhangi bir yazımda
	// olabilir → tek tarafta normalize.
	vals := map[string]any{}
	for k, v := range row {
		vals[strings.ToUpper(strings.TrimSpace(k))] = v
	}
	get := func(field string) (string, bool) {
		col := m.cols[field]
		if col == "" {
			return "", false
		}
		v, present := vals[strings.ToUpper(col)]
		if !present {
			return "", false
		}
		return cellString(v), true
	}

	tsRaw, present := vals[strings.ToUpper(m.cols[FieldTimestamp])]
	if !present {
		return r, false, false
	}
	ts, tok := m.parseTime(tsRaw)
	if !tok {
		return r, false, false
	}

	r.SourceID = m.sourceID
	r.Time = ts
	sevRaw, _ := get(FieldSeverity)
	r.SeverityNum, r.SeverityText = mapSeverity(sevRaw)
	r.Body, _ = get(FieldMessage)
	extras := map[string]string{}
	if traceRaw, has := get(FieldTraceID); has {
		id, valid := normalizeTraceID(traceRaw)
		r.TraceID = id
		if !valid {
			badTrace = true
			extras[m.cols[FieldTraceID]] = strings.TrimSpace(traceRaw)
		}
	}
	r.HostName, _ = get(FieldHost)
	r.InstanceID, _ = get(FieldInstance)
	r.OperationCode, _ = get(FieldService)
	r.ErrorCode, _ = get(FieldCode)
	r.ExternalCode, _ = get(FieldExternalCode)
	r.ErrorType, _ = get(FieldType)
	r.ChannelCode, _ = get(FieldChannel)
	r.TaskCode, _ = get(FieldTask)
	r.RequestID, _ = get(FieldRequestID)
	r.CustomerID, _ = get(FieldCustomerID)
	r.TellerID, _ = get(FieldTellerID)
	r.Location, _ = get(FieldLocation)

	for k, v := range vals {
		if _, consumed := m.byCol[k]; consumed {
			continue
		}
		if s := cellString(v); s != "" {
			extras[k] = s
		}
	}
	keys := make([]string, 0, len(extras))
	for k := range extras {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	r.AttrKeys = keys
	r.AttrValues = make([]string, len(keys))
	for i, k := range keys {
		r.AttrValues[i] = extras[k]
	}
	r.RowID = rowID(r)
	return r, true, badTrace
}

// MapAll — parti; istatistik satır sayımlarıdır.
func (m *Mapper) MapAll(rows []map[string]any) ([]chstore.OracleErrorRow, MapStats) {
	out := make([]chstore.OracleErrorRow, 0, len(rows))
	st := MapStats{Rows: len(rows)}
	for _, row := range rows {
		r, ok, bad := m.Map(row)
		if !ok {
			st.NoTimestamp++
			continue
		}
		if bad {
			st.BadTraceID++
		}
		st.Mapped++
		out = append(out, r)
	}
	return out, st
}

// timeLayouts — sürücü zamanı time.Time verir; string yalnız yedek
// (test fixture'ları, CLOB'a yazılmış zamanlar). Dilimli düzenler önce.
var timeLayouts = []struct {
	layout string
	zoned  bool
}{
	{time.RFC3339Nano, true},
	{"2006-01-02T15:04:05.999999999", false},
	{"2006-01-02 15:04:05.999999999", false},
	{"2006-01-02 15:04:05", false},
}

func (m *Mapper) parseTime(v any) (time.Time, bool) {
	switch t := v.(type) {
	case time.Time:
		if t.IsZero() {
			return time.Time{}, false
		}
		return m.localize(t), true
	case string:
		s := strings.TrimSpace(t)
		if s == "" {
			return time.Time{}, false
		}
		for _, l := range timeLayouts {
			if l.zoned {
				if p, err := time.Parse(l.layout, s); err == nil {
					return p.UTC(), true
				}
				continue
			}
			if p, err := time.ParseInLocation(l.layout, s, m.loc); err == nil {
				return p.UTC(), true
			}
		}
		return time.Time{}, false
	case []byte:
		return m.parseTime(string(t))
	default:
		return time.Time{}, false
	}
}

// localize — dilimsiz kipte duvar saati bileşenleri kaynağın diliminde
// yeniden yorumlanır; dilimli kipte an olduğu gibi UTC'ye.
func (m *Mapper) localize(t time.Time) time.Time {
	if m.hasZone {
		return t.UTC()
	}
	return time.Date(t.Year(), t.Month(), t.Day(), t.Hour(), t.Minute(), t.Second(), t.Nanosecond(), m.loc).UTC()
}

// cellString — SAF: hücre → dize; KIRPMA YOK (formatCell'in 200'lük kırpması
// yalnız Settings örneği içindir; burada tam fidelity).
func cellString(v any) string {
	switch t := v.(type) {
	case nil:
		return ""
	case string:
		return t
	case []byte:
		return string(t)
	case time.Time:
		return t.UTC().Format(time.RFC3339Nano)
	case float64:
		return strconv.FormatFloat(t, 'f', -1, 64)
	case float32:
		return strconv.FormatFloat(float64(t), 'f', -1, 32)
	case int64:
		return strconv.FormatInt(t, 10)
	case int:
		return strconv.Itoa(t)
	case bool:
		return strconv.FormatBool(t)
	default:
		return fmt.Sprint(t)
	}
}

// normalizeTraceID — logstore.normalizeHexID(v, 32) ile aynı sözleşme (o
// paket-içi; kopya bilinçli, iki paket birbirini import etmez). Boş = "yok"
// (geçerli, pivotsuz); dolu ama 32 hex'e inmeyen = GEÇERSİZ.
func normalizeTraceID(v string) (string, bool) {
	s := strings.TrimSpace(v)
	if s == "" {
		return "", true
	}
	s = strings.ToLower(s)
	s = strings.TrimPrefix(s, "0x")
	s = strings.ReplaceAll(s, "-", "")
	if len(s) != 32 {
		return "", false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return "", false
		}
	}
	return s, true
}

// mapSeverity — OTel log severity (1..24) eşlemesi. Boş → ERROR (bu bir hata
// tablosu; sessiz INFO yanlış olurdu). Sayı 1..24 → olduğu gibi. Bilinmeyen
// metin → numara ERROR, metin HAM (bilgi kaybolmaz).
func mapSeverity(raw string) (uint8, string) {
	u := strings.ToUpper(strings.TrimSpace(raw))
	if u == "" {
		return 17, "ERROR"
	}
	if n, err := strconv.Atoi(u); err == nil && n >= 1 && n <= 24 {
		return uint8(n), severityText(n)
	}
	switch u {
	case "FATAL", "CRITICAL", "F", "C", "SEVERE":
		return 21, "FATAL"
	case "ERROR", "ERR", "E":
		return 17, "ERROR"
	case "WARN", "WARNING", "W":
		return 13, "WARN"
	case "INFO", "INFORMATION", "I":
		return 9, "INFO"
	case "DEBUG", "D", "TRACE", "T":
		return 5, "DEBUG"
	}
	return 17, u
}

func severityText(n int) string {
	switch {
	case n >= 21:
		return "FATAL"
	case n >= 17:
		return "ERROR"
	case n >= 13:
		return "WARN"
	case n >= 9:
		return "INFO"
	case n >= 5:
		return "DEBUG"
	default:
		return "TRACE"
	}
}

// rowID — içerik hash'i; dedup anahtarının üçüncü bileşeni. Alan sırası
// SABİT, ekstralar sıralı → aynı Oracle satırı her tikte aynı kimlik.
func rowID(r chstore.OracleErrorRow) uint64 {
	h := fnv.New64a()
	w := func(s string) {
		_, _ = h.Write([]byte(s))
		_, _ = h.Write([]byte{0})
	}
	w(r.SourceID)
	w(strconv.FormatInt(r.Time.UnixNano(), 10))
	w(r.SeverityText)
	w(r.Body)
	w(r.TraceID)
	w(r.HostName)
	w(r.InstanceID)
	w(r.OperationCode)
	w(r.ErrorCode)
	w(r.ExternalCode)
	w(r.ErrorType)
	w(r.ChannelCode)
	w(r.TaskCode)
	w(r.RequestID)
	w(r.CustomerID)
	w(r.TellerID)
	w(r.Location)
	for i, k := range r.AttrKeys {
		w(k)
		w(r.AttrValues[i])
	}
	return h.Sum64()
}
