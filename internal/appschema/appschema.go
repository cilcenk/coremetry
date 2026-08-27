// Package appschema — uygulama veritabanı kataloğunun SALT-OKUNUR anlık
// görüntüsü ve SQL hatası → şema kanıtı çevirisi (v0.10.115, spec
// docs/plans/spec-ai-evidence-2026-08-28.md Dilim D).
//
// # Neden anlık görüntü, neden canlı bağlantı değil (bu dilimde)
//
// SQLCODE=-302 / SQLSTATE=22001 gibi hatalarda model hedef kolonun
// tipini/uzunluğunu bilmediği için "muhtemelen telefon numarası" diye
// tahmin yürütüyordu. Kanıt kolon tanımıdır: `INT_TFRAUD.TELNO
// VARCHAR(10) NOT NULL`. Operatörün hedefi DB2 görünüyor (JDBC mesaj
// kalıbı) ve DB2'nin cgo'suz Go sürücüsü yok; tek-binary/air-gapped
// imaja IBM clidriver sokmak ayrı bir karar. Sürücüyü prod `db_system`
// teyidinden önce eklemek kör bahis olurdu. Bu yüzden katalog, operatörün
// KENDİ tarafında koşturduğu salt-okunur bir SELECT'in (SnapshotSQL)
// CSV çıktısıdır; system_settings["schema_catalog"] altında durur ve
// bellekte tutulur. Canlı bağlayıcı aynı Catalog arayüzünün ikinci
// uygulaması olarak flavor teyidiyle gelir.
//
// Paket SAFTIR: ağ yok, saat yok (ImportedAt çağırandan gelir). Her
// karar tablo-testli (appschema_test.go).
package appschema

import (
	"encoding/csv"
	"fmt"
	"io"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"
)

// Column — bir kolon tanımı. Table ŞEMASIZ ve BÜYÜK harf (DB2/Oracle
// kataloğu zaten büyük yazar; MyBatis SQL'i genelde büyük harf).
type Column struct {
	Table    string `json:"table"`
	Name     string `json:"name"`
	Type     string `json:"type"`
	Length   int    `json:"length,omitempty"`
	Scale    int    `json:"scale,omitempty"`
	Nullable bool   `json:"nullable"`
	Schema   string `json:"schema,omitempty"`
}

// Catalog — bellekteki anlık görüntü. Tables anahtarı büyük-harf tablo
// adı (şemasız); aynı tablo iki şemada varsa ikisi de aynı listeye
// düşer ve Schema alanı ayırt eder.
type Catalog struct {
	Tables     map[string][]Column `json:"tables"`
	ImportedAt int64               `json:"importedAt"` // unix ms; 0 = hiç
	Source     string              `json:"source,omitempty"`
	Flavor     string              `json:"flavor,omitempty"`
}

// Summary — GET yanıtı: içerik değil, sayı.
type Summary struct {
	Tables      int               `json:"tables"`
	Columns     int               `json:"columns"`
	ImportedAt  int64             `json:"importedAt"`
	Source      string            `json:"source,omitempty"`
	Flavor      string            `json:"flavor,omitempty"`
	SnapshotSQL map[string]string `json:"snapshotSql"`
}

// Empty — katalog yüklü mü?
func (c Catalog) Empty() bool { return len(c.Tables) == 0 }

// Count — tablo ve kolon sayısı.
func (c Catalog) Count() (tables, columns int) {
	for _, cols := range c.Tables {
		tables++
		columns += len(cols)
	}
	return
}

// Summarize — Catalog → Summary (SnapshotSQL dahil).
func (c Catalog) Summarize() Summary {
	t, n := c.Count()
	return Summary{Tables: t, Columns: n, ImportedAt: c.ImportedAt, Source: c.Source, Flavor: c.Flavor, SnapshotSQL: SnapshotSQL()}
}

// ── CSV ────────────────────────────────────────────────────────────────

// csvHeaderAliases — üç katalog lehçesi tek başlık sözlüğüne iner. DB2
// SYSCAT.COLUMNS, Oracle ALL_TAB_COLUMNS, information_schema.columns ve
// düz "table,column,type,length,scale,nullable" el yazımı.
var csvHeaderAliases = map[string]string{
	"table": "table", "table_name": "table", "tabname": "table", "tablename": "table",
	"schema": "schema", "table_schema": "schema", "tabschema": "schema", "owner": "schema",
	"column": "column", "column_name": "column", "colname": "column", "name": "column",
	"type": "type", "data_type": "type", "typename": "type", "coltype": "type",
	"length": "length", "data_length": "length", "character_maximum_length": "length", "char_length": "length",
	"scale": "scale", "data_scale": "scale", "numeric_scale": "scale",
	"nullable": "nullable", "is_nullable": "nullable", "nulls": "nullable",
}

// ParseCSV — başlıklı CSV → Catalog. Ayraç virgül ya da noktalı virgül
// ya da sekme (ilk satırdan sezilir). Zorunlu başlıklar: table, column,
// type. Boş satırlar atlanır; tablo/kolon adları büyük harfe çevrilir.
func ParseCSV(r io.Reader) (Catalog, error) {
	raw, err := io.ReadAll(io.LimitReader(r, 64<<20))
	if err != nil {
		return Catalog{}, err
	}
	text := strings.TrimPrefix(string(raw), "\uFEFF")
	if strings.TrimSpace(text) == "" {
		return Catalog{}, fmt.Errorf("boş CSV")
	}
	first := text
	if i := strings.IndexByte(text, '\n'); i >= 0 {
		first = text[:i]
	}
	sep := ','
	switch {
	case strings.Count(first, ";") > strings.Count(first, ","):
		sep = ';'
	case strings.Count(first, "\t") > strings.Count(first, ","):
		sep = '\t'
	}
	cr := csv.NewReader(strings.NewReader(text))
	cr.Comma = sep
	cr.FieldsPerRecord = -1
	cr.LazyQuotes = true
	// Sekme ayraçta TrimLeadingSpace KAPALI: encoding/csv boşluk sayılan
	// ayracı da kırpar ve "\t\t" (boş scale) çökerek NULLS kolonunu bir
	// sola kaydırırdı — TELNO NULL görünürdü.
	cr.TrimLeadingSpace = sep != '\t'
	rows, err := cr.ReadAll()
	if err != nil {
		return Catalog{}, fmt.Errorf("CSV ayrıştırma: %w", err)
	}
	if len(rows) < 2 {
		return Catalog{}, fmt.Errorf("başlık + en az bir satır gerekli")
	}
	idx := map[string]int{}
	for i, h := range rows[0] {
		h = strings.ToLower(strings.TrimSpace(h))
		if k, ok := csvHeaderAliases[h]; ok {
			if _, dup := idx[k]; !dup {
				idx[k] = i
			}
		}
	}
	for _, need := range []string{"table", "column", "type"} {
		if _, ok := idx[need]; !ok {
			return Catalog{}, fmt.Errorf("başlıkta %q (ya da eşdeğeri) yok — SnapshotSQL çıktısı beklenir", need)
		}
	}
	cat := Catalog{Tables: map[string][]Column{}}
	get := func(row []string, k string) string {
		i, ok := idx[k]
		if !ok || i >= len(row) {
			return ""
		}
		return strings.TrimSpace(row[i])
	}
	for _, row := range rows[1:] {
		if len(row) == 0 || strings.TrimSpace(strings.Join(row, "")) == "" {
			continue
		}
		tbl := strings.ToUpper(get(row, "table"))
		col := strings.ToUpper(get(row, "column"))
		if tbl == "" || col == "" {
			continue
		}
		c := Column{
			Table: tbl, Name: col, Type: strings.ToUpper(get(row, "type")),
			Schema: strings.ToUpper(get(row, "schema")),
			Length: atoi(get(row, "length")), Scale: atoi(get(row, "scale")),
			Nullable: parseNullable(get(row, "nullable")),
		}
		cat.Tables[tbl] = append(cat.Tables[tbl], c)
	}
	if cat.Empty() {
		return Catalog{}, fmt.Errorf("hiç kolon satırı okunamadı")
	}
	return cat, nil
}

func atoi(s string) int {
	n, _ := strconv.Atoi(strings.TrimSpace(s))
	return n
}

// parseNullable — Y/N, YES/NO, TRUE/FALSE, 1/0; boş = NULL kabul (DB2
// NULLS kolonu her satırda dolu; information_schema IS_NULLABLE YES/NO).
func parseNullable(s string) bool {
	switch strings.ToUpper(strings.TrimSpace(s)) {
	case "N", "NO", "FALSE", "0", "NOT NULL":
		return false
	}
	return true
}

// ── Hata sinyali ────────────────────────────────────────────────────────

// Signal — hata metninden çıkarılan DB hata kimliği + insan okunur ipucu.
type Signal struct {
	Vendor   string `json:"vendor"` // db2 | oracle | postgres | mssql | generic
	SQLCode  string `json:"sqlCode,omitempty"`
	SQLState string `json:"sqlState,omitempty"`
	Hint     string `json:"hint,omitempty"`
}

var (
	sqlCodeRe  = regexp.MustCompile(`(?i)SQLCODE\s*[=:]\s*(-?\d+)`)
	sqlStateRe = regexp.MustCompile(`(?i)SQLSTATE\s*[=:]\s*([0-9A-Z]{5})`)
	oraRe      = regexp.MustCompile(`\bORA-(\d{5})\b`)
	pgStateRe  = regexp.MustCompile(`(?i)\bSQLState:\s*([0-9A-Z]{5})|ERROR:\s+[^\n]*\(SQLSTATE\s+([0-9A-Z]{5})\)`)
)

// sqlStateHints — SQLSTATE sınıfları (vendor-bağımsız).
var sqlStateHints = map[string]string{
	"22001": "değer kolon uzunluğunu aşıyor (string truncation) — hedef kolonun UZUNLUĞU belirleyici",
	"22003": "sayısal değer kolonun hassasiyetine sığmıyor (overflow) — PRECISION/SCALE belirleyici",
	"22007": "geçersiz tarih/zaman biçimi",
	"22018": "karakter → sayısal dönüşüm hatası",
	"23502": "NOT NULL kolona NULL yazılıyor",
	"23505": "tekil anahtar/unique ihlali",
	"23503": "yabancı anahtar ihlali",
	"42703": "kolon adı bulunamadı",
	"42704": "tablo/nesne bulunamadı",
	"42S02": "tablo bulunamadı",
	"40001": "deadlock / serialization hatası",
	"57014": "sorgu iptal edildi (timeout)",
	"08001": "bağlantı kurulamadı",
}

// db2Hints — DB2 SQLCODE (negatif) ipuçları.
var db2Hints = map[string]string{
	"-302": "giriş değeri hedef kolona sığmıyor ya da dönüşemiyor (uzunluk/tip) — hedef kolon tanımı belirleyici",
	"-303": "dönüş değeri hedef değişkene uyumsuz tip",
	"-305": "NULL değer, gösterge değişkeni yok",
	"-407": "NOT NULL kolona NULL yazılıyor",
	"-433": "değer kolon uzunluğunu aşıyor (truncation)",
	"-530": "yabancı anahtar ihlali",
	"-803": "tekil indeks ihlali (duplicate)",
	"-204": "tablo/nesne tanımsız",
	"-206": "kolon tanımsız",
	"-911": "deadlock ya da timeout — işlem geri alındı",
	"-913": "deadlock ya da timeout",
	"-904": "kaynak kullanılamıyor",
	"-811": "birden çok satır döndü (tekil bekleniyordu)",
}

// oraHints — Oracle ORA-xxxxx ipuçları.
var oraHints = map[string]string{
	"00001": "tekil kısıt ihlali (unique)",
	"01400": "NOT NULL kolona NULL yazılıyor",
	"01438": "sayısal değer kolonun precision'ını aşıyor",
	"12899": "değer kolon uzunluğunu aşıyor (truncation) — hedef kolonun UZUNLUĞU belirleyici",
	"00904": "geçersiz kolon adı",
	"00942": "tablo bulunamadı",
	"01722": "geçersiz sayı (dönüşüm)",
	"00060": "deadlock",
	"01017": "kimlik doğrulama hatası",
}

// SQLErrorSignal — metinde DB hata kimliği var mı? Saf; tablo-testli.
// Öncelik: DB2 (SQLCODE+SQLSTATE) → Oracle (ORA-) → SQLSTATE tek başına.
func SQLErrorSignal(text string) (Signal, bool) {
	if strings.TrimSpace(text) == "" {
		return Signal{}, false
	}
	var sig Signal
	if m := sqlCodeRe.FindStringSubmatch(text); m != nil {
		sig.Vendor, sig.SQLCode = "db2", m[1]
		if s := sqlStateRe.FindStringSubmatch(text); s != nil {
			sig.SQLState = strings.ToUpper(s[1])
		}
		if h, ok := db2Hints[sig.SQLCode]; ok {
			sig.Hint = h
		} else if h, ok := sqlStateHints[sig.SQLState]; ok {
			sig.Hint = h
		}
		return sig, true
	}
	if m := oraRe.FindStringSubmatch(text); m != nil {
		sig.Vendor, sig.SQLCode = "oracle", "ORA-"+m[1]
		if h, ok := oraHints[m[1]]; ok {
			sig.Hint = h
		}
		return sig, true
	}
	if m := pgStateRe.FindStringSubmatch(text); m != nil {
		st := m[1]
		if st == "" {
			st = m[2]
		}
		sig.Vendor, sig.SQLState = "postgres", strings.ToUpper(st)
		sig.Hint = sqlStateHints[sig.SQLState]
		return sig, true
	}
	if s := sqlStateRe.FindStringSubmatch(text); s != nil {
		sig.Vendor, sig.SQLState = "generic", strings.ToUpper(s[1])
		sig.Hint = sqlStateHints[sig.SQLState]
		return sig, true
	}
	return Signal{}, false
}

// ── SQL hedefleri ───────────────────────────────────────────────────────

// Targets — bir SQL ifadesinin dokunduğu tablolar ve (biliniyorsa)
// yazılan kolonlar.
type Targets struct {
	Tables  []string // büyük harf, şemasız, ilk görülme sırası
	Columns []string // INSERT kolon listesi / UPDATE SET hedefleri
}

var (
	insertRe   = regexp.MustCompile(`(?is)\bINSERT\s+INTO\s+([A-Za-z_][\w$#.]*)\s*(?:\(([^)]*)\))?`)
	updateRe   = regexp.MustCompile(`(?is)\bUPDATE\s+([A-Za-z_][\w$#.]*)\s+SET\s+(.*?)(?:\bWHERE\b|$)`)
	deleteRe   = regexp.MustCompile(`(?is)\bDELETE\s+FROM\s+([A-Za-z_][\w$#.]*)`)
	mergeRe    = regexp.MustCompile(`(?is)\bMERGE\s+INTO\s+([A-Za-z_][\w$#.]*)`)
	fromJoinRe = regexp.MustCompile(`(?is)\b(?:FROM|JOIN)\s+([A-Za-z_][\w$#.]*)`)
	setColRe   = regexp.MustCompile(`(?i)([A-Za-z_][\w$#]*)\s*=`)
	xmlTagRe   = regexp.MustCompile(`(?s)<[^>]+>`)
	lineNumRe  = regexp.MustCompile(`(?m)^\s*\d+\|\s?`)
)

// TargetsOf — SQL (ya da satır-numaralı/XML sarılı mapper bloğu) →
// hedefler. Saf; tablo-testli. Bind işaretleri (?, #{x}, :x) önemsiz.
// Alt sorgulardaki FROM'lar da tablo sayılır (kanıt olarak zararsız).
func TargetsOf(sql string) Targets {
	var t Targets
	if strings.TrimSpace(sql) == "" {
		return t
	}
	s := lineNumRe.ReplaceAllString(sql, "")
	s = strings.ReplaceAll(s, "<![CDATA[", " ")
	s = strings.ReplaceAll(s, "]]>", " ")
	s = xmlTagRe.ReplaceAllString(s, " ")
	seen := map[string]bool{}
	add := func(name string) {
		name = strings.ToUpper(strings.TrimSpace(name))
		if i := strings.LastIndex(name, "."); i >= 0 {
			name = name[i+1:]
		}
		if name == "" || seen[name] || isSQLKeyword(name) {
			return
		}
		seen[name] = true
		t.Tables = append(t.Tables, name)
	}
	colSeen := map[string]bool{}
	addCol := func(name string) {
		name = strings.ToUpper(strings.TrimSpace(name))
		if i := strings.LastIndex(name, "."); i >= 0 {
			name = name[i+1:]
		}
		if name == "" || colSeen[name] {
			return
		}
		colSeen[name] = true
		t.Columns = append(t.Columns, name)
	}
	if m := insertRe.FindStringSubmatch(s); m != nil {
		add(m[1])
		for _, c := range strings.Split(m[2], ",") {
			if strings.TrimSpace(c) != "" {
				addCol(c)
			}
		}
	}
	if m := updateRe.FindStringSubmatch(s); m != nil {
		add(m[1])
		for _, sm := range setColRe.FindAllStringSubmatch(m[2], -1) {
			addCol(sm[1])
		}
	}
	if m := deleteRe.FindStringSubmatch(s); m != nil {
		add(m[1])
	}
	if m := mergeRe.FindStringSubmatch(s); m != nil {
		add(m[1])
	}
	for _, m := range fromJoinRe.FindAllStringSubmatch(s, -1) {
		add(m[1])
	}
	return t
}

func isSQLKeyword(s string) bool {
	switch s {
	case "SELECT", "WHERE", "VALUES", "SET", "DUAL", "AND", "OR", "NOT", "NULL", "INTO", "AS", "ON":
		return true
	}
	return false
}

// ── Katalog sorgusu + prompt bölümü ────────────────────────────────────

// maxColumnsPerLookup — bir açıklamaya giren en fazla kolon; ötesi
// PromptSection'da "kırpıldı" notuyla düşer.
const maxColumnsPerLookup = 40

// Lookup — hedef tablolar (ve varsa hedef kolonlar) için katalog
// satırları. Kolon listesi verildiyse yalnız onlar (tablo başına), yoksa
// tablonun tamamı; toplam maxColumnsPerLookup ile sınırlı, kalan sayı
// ikinci dönüş değeri. Tablo adı BÜYÜK-harf/şemasız eşleşir.
func Lookup(cat Catalog, tg Targets) ([]Column, int) {
	if cat.Empty() || len(tg.Tables) == 0 {
		return nil, 0
	}
	want := map[string]bool{}
	for _, c := range tg.Columns {
		want[c] = true
	}
	var out []Column
	dropped := 0
	for _, tbl := range tg.Tables {
		cols, ok := cat.Tables[strings.ToUpper(tbl)]
		if !ok {
			continue
		}
		var pick []Column
		if len(want) > 0 {
			for _, c := range cols {
				if want[c.Name] {
					pick = append(pick, c)
				}
			}
			if len(pick) == 0 { // kolon listesi bu tabloya ait değil → tamamı
				pick = cols
			}
		} else {
			pick = cols
		}
		for _, c := range pick {
			if len(out) >= maxColumnsPerLookup {
				dropped++
				continue
			}
			out = append(out, c)
		}
	}
	return out, dropped
}

// TypeLabel — `VARCHAR(10)` / `DECIMAL(15,2)` / `INTEGER`.
func (c Column) TypeLabel() string {
	switch {
	case c.Length > 0 && c.Scale > 0:
		return fmt.Sprintf("%s(%d,%d)", c.Type, c.Length, c.Scale)
	case c.Length > 0:
		return fmt.Sprintf("%s(%d)", c.Type, c.Length)
	}
	return c.Type
}

// PromptSection — modele giden şema bloğu. Boş kolon listesi ve sinyal
// yoksa "". Rune bütçesi aşılınca satırlar düşer ve bu SÖYLENİR
// ("[şema kırpıldı: N kolon gösterilmedi]") — operatör direktifi:
// kırpma modele bildirilir. Dönen ikinci değer gösterilen kolon sayısı.
func PromptSection(sig *Signal, cols []Column, dropped int, importedAt string, maxRunes int) (string, int) {
	if len(cols) == 0 && sig == nil {
		return "", 0
	}
	var b strings.Builder
	b.WriteString("\n\nŞEMA BAĞLAMI (uygulama DB kataloğu, salt-okunur anlık görüntü")
	if importedAt != "" {
		b.WriteString(" " + importedAt)
	}
	b.WriteString("). Kolon tanımları GERÇEKTİR; hedef kolonu tahmin etme, buradan oku:")
	if sig != nil && (sig.SQLCode != "" || sig.SQLState != "") {
		b.WriteString("\nHata sinyali: ")
		if sig.SQLCode != "" {
			b.WriteString("SQLCODE " + sig.SQLCode)
		}
		if sig.SQLState != "" {
			if sig.SQLCode != "" {
				b.WriteString(", ")
			}
			b.WriteString("SQLSTATE " + sig.SQLState)
		}
		if sig.Hint != "" {
			b.WriteString(" — " + sig.Hint)
		}
	}
	shown := 0
	used := utf8.RuneCountInString(b.String())
	for i, c := range cols {
		line := fmt.Sprintf("\n%s.%s %s %s", c.Table, c.Name, c.TypeLabel(), map[bool]string{true: "NULL", false: "NOT NULL"}[c.Nullable])
		n := utf8.RuneCountInString(line)
		if maxRunes > 0 && used+n > maxRunes {
			dropped += len(cols) - i
			break
		}
		b.WriteString(line)
		used += n
		shown++
	}
	if len(cols) == 0 {
		b.WriteString("\n(hedef tablo katalogda bulunamadı — kolon tanımı yok; tipi/uzunluğu tahmin etme)")
	}
	if dropped > 0 {
		fmt.Fprintf(&b, "\n[şema kırpıldı: %d kolon gösterilmedi]", dropped)
	}
	return b.String(), shown
}

// MaskSummary — ai_calls kopyasına giden özet: kolon tanımları değil,
// yalnız sayı ve tablolar. `[şema: T1, T2 · N kolon]`.
func MaskSummary(cols []Column, shown int) string {
	if shown == 0 && len(cols) == 0 {
		return ""
	}
	seen := map[string]bool{}
	var tables []string
	for _, c := range cols {
		if !seen[c.Table] {
			seen[c.Table] = true
			tables = append(tables, c.Table)
		}
	}
	sort.Strings(tables)
	return fmt.Sprintf("\n\n[şema: %s · %d kolon]", strings.Join(tables, ", "), shown)
}

// SnapshotSQL — operatörün kendi tarafında koşturacağı SALT-OKUNUR
// katalog sorguları; çıktı CSV olarak yüklenir. Hepsi yalnız SELECT.
func SnapshotSQL() map[string]string {
	return map[string]string{
		"db2": `SELECT TABSCHEMA AS schema, TABNAME AS table, COLNAME AS column, TYPENAME AS type, LENGTH AS length, SCALE AS scale, NULLS AS nullable
FROM SYSCAT.COLUMNS
WHERE TABSCHEMA NOT LIKE 'SYS%'
ORDER BY TABSCHEMA, TABNAME, COLNO`,
		"oracle": `SELECT OWNER AS schema, TABLE_NAME AS "table", COLUMN_NAME AS "column", DATA_TYPE AS type, DATA_LENGTH AS length, DATA_SCALE AS scale, NULLABLE AS nullable
FROM ALL_TAB_COLUMNS
WHERE OWNER NOT IN ('SYS','SYSTEM')
ORDER BY OWNER, TABLE_NAME, COLUMN_ID`,
		"postgres": `SELECT table_schema AS schema, table_name AS "table", column_name AS "column", data_type AS type, COALESCE(character_maximum_length, numeric_precision) AS length, numeric_scale AS scale, is_nullable AS nullable
FROM information_schema.columns
WHERE table_schema NOT IN ('pg_catalog','information_schema')
ORDER BY table_schema, table_name, ordinal_position`,
		"mysql": `SELECT TABLE_SCHEMA AS ` + "`schema`" + `, TABLE_NAME AS ` + "`table`" + `, COLUMN_NAME AS ` + "`column`" + `, DATA_TYPE AS type, COALESCE(CHARACTER_MAXIMUM_LENGTH, NUMERIC_PRECISION) AS length, NUMERIC_SCALE AS scale, IS_NULLABLE AS nullable
FROM information_schema.COLUMNS
WHERE TABLE_SCHEMA NOT IN ('mysql','information_schema','performance_schema','sys')
ORDER BY TABLE_SCHEMA, TABLE_NAME, ORDINAL_POSITION`,
		"mssql": `SELECT TABLE_SCHEMA AS [schema], TABLE_NAME AS [table], COLUMN_NAME AS [column], DATA_TYPE AS type, COALESCE(CHARACTER_MAXIMUM_LENGTH, NUMERIC_PRECISION) AS length, NUMERIC_SCALE AS scale, IS_NULLABLE AS nullable
FROM INFORMATION_SCHEMA.COLUMNS
ORDER BY TABLE_SCHEMA, TABLE_NAME, ORDINAL_POSITION`,
	}
}
