package api

import (
	"bytes"
	"encoding/json"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"
)

// config_iox_test.go — configTables ↔ canlı CH şeması kolon-uyum pinleri.
//
// v0.9.1291 — Dynatrace-parite raporu EK A4
// (docs/plans/dynatrace-parity-2026-08-21.md) bir DR boşluğu ölçtü:
// config export/import katalogu runbooks tablosunu DIŞARIDA bırakıyordu.
// Belirti sessiz: export dosyası hatasız üretiliyor, import hatasız
// koşuyor, ama temiz kuruluma taşınan kurulumda operatörün yazdığı her
// prosedür (runbook tanımları, adımlar, bildirim ayarları) yok oluyor.
// Hiçbir katman şikâyet etmiyor çünkü eksik olan bir satır değil, bir
// TABLO — dump döngüsü onu hiç ziyaret etmiyor.
//
// Buradaki iki test iki ayrı şeyi pinler:
//
//  1. Kapsam: runbooks configTables içinde. Listeden çıkarınca kızarır.
//  2. Kolon uyumu: configTables listesindeki HER tablonun store.go
//     DDL indeki HER kolonu, dump → JSON → import yolundan aynen geri
//     döner. dumpConfigTable/loadConfigTable jenerik olduğu için
//     katalogla şema arasında derleyici bağı YOK; ileride bir
//     ALTER ADD COLUMN coerceForCHType in modellemediği bir tip
//     getirirse (Map/Tuple/Decimal…) bu test kızarır. Aksi hâlde import
//     o kolonu sessizce yanlış tiple yazar.
//
// Beklenti tablosu (sampleForCHType) bilerek config_iox.go dan bağımsız
// yazıldı — pinin değeri iki tarafın BİRBİRİNDEN habersiz olmasında.

const storeDDLPath = "../chstore/store.go"

func TestConfigTablesIncludesRunbooks(t *testing.T) {
	found := false
	for _, tb := range configTables {
		if tb == "runbooks" {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("runbooks configTables içinde değil: config export/import onu atlar, " +
			"temiz kuruluma taşınan kurulumda operatörün yazdığı tüm prosedürler kaybolur (v0.9.1291, EK A4)")
	}
}

// TestChSchemaColumnsParsesStoreDDL — gidiş-dönüş testinin POZİTİF
// KONTROLÜ. O test kolonlar üzerinde döner; ayrıştırıcı hiçbir kolon
// bulamasa bile döngü boş koşup yeşil kalabilirdi. Burada iki yolu da
// isimle pinliyoruz:
//
//	runbooks         → CREATE TABLE gövdesinin okunduğunu (v0.9.1291 in
//	                   fiilen taşıdığı bilgi: steps_json + labels)
//	service_metadata → sonradan gelen ALTER ADD COLUMN satırlarının da
//	                   birleştirildiğini (namespace yalnız ALTER da var)
func TestChSchemaColumnsParsesStoreDDL(t *testing.T) {
	ddl := readStoreDDL(t)

	rb := chSchemaColumns(t, ddl, "runbooks")
	for col, want := range map[string]string{
		"id":              "String",
		"steps_json":      "String",
		"labels":          "Array(LowCardinality(String))",
		"notify_channels": "Array(LowCardinality(String))",
		"created_at":      "DateTime64(9)",
		"version":         "UInt64",
	} {
		if got := rb[col]; got != want {
			t.Errorf("runbooks.%s: DDL den %q çıktı, %q bekleniyordu", col, got, want)
		}
	}

	sm := chSchemaColumns(t, ddl, "service_metadata")
	if got := sm["namespace"]; got != "String" {
		t.Errorf("service_metadata.namespace: %q çıktı — bu kolon yalnız "+
			"ALTER ADD COLUMN ile geliyor, ayrıştırıcı ALTER kolunu kaçırmış olabilir", got)
	}
}

// TestConfigTablesColumnRoundTrip — katalogdaki her tablonun her kolonu
// için dump → JSON → import gidiş-dönüşünün değeri ve Go tipini koruduğunu
// doğrular. Ayrıca her tablonun version kolonu olduğunu pinler:
// importConfig un merge semantiği tamamen ReplacingMergeTree(version) e
// dayanıyor, versionsuz bir tablo katalogda sessizce yanlış davranır.
func TestConfigTablesColumnRoundTrip(t *testing.T) {
	ddl := readStoreDDL(t)

	for _, table := range configTables {
		cols := chSchemaColumns(t, ddl, table)
		if len(cols) == 0 {
			t.Errorf("configTables[%q]: store.go içinde CREATE TABLE bulunamadı — "+
				"tablo adı yanlış yazılmış ya da DDL başka dosyaya taşınmış olabilir", table)
			continue
		}
		if _, ok := cols["version"]; !ok {
			t.Errorf("%s: version kolonu yok — importConfig merge semantiği "+
				"ReplacingMergeTree(version) e dayanıyor", table)
		}

		for col, chType := range cols {
			want, ok := sampleForCHType(t, table, col, chType)
			if !ok {
				continue
			}

			// dumpConfigTable yolu: sürücü scan değeri → JSON-güvenli değer.
			raw, err := json.Marshal(map[string]any{col: jsonifyConfigValue(want)})
			if err != nil {
				t.Errorf("%s.%s (%s): dump çıktısı JSON e çevrilemedi: %v", table, col, chType, err)
				continue
			}

			// importConfig yolu: UseNumber ile çöz (UInt64 hassasiyeti),
			// sonra kolon tipine geri zorla.
			dec := json.NewDecoder(bytes.NewReader(raw))
			dec.UseNumber()
			var back map[string]any
			if err := dec.Decode(&back); err != nil {
				t.Errorf("%s.%s (%s): import tarafı JSON i çözemedi: %v", table, col, chType, err)
				continue
			}
			got, err := coerceForCHType(back[col], chType)
			if err != nil {
				t.Errorf("%s.%s (%s): coerceForCHType hata verdi: %v", table, col, chType, err)
				continue
			}
			if !sameConfigValue(want, got) {
				t.Errorf("%s.%s (%s): gidiş-dönüş değeri bozdu: %#v (%T) → %#v (%T)",
					table, col, chType, want, want, got, got)
			}

			// Eski bir export dosyası bu kolonu hiç taşımıyorsa
			// loadConfigTable typed zero koyar; tipi sürücünün beklediğiyle
			// aynı olmazsa batch pozisyonu tutar ama insert tip hatası verir.
			if z := zeroForCHType(chType); reflect.TypeOf(z) != reflect.TypeOf(want) {
				t.Errorf("%s.%s (%s): zeroForCHType %T döndürdü, sürücü %T bekliyor",
					table, col, chType, z, want)
			}
		}
	}
}

func readStoreDDL(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile(storeDDLPath)
	if err != nil {
		t.Fatalf("%s okunamadı: %v", storeDDLPath, err)
	}
	return string(b)
}

// chSchemaColumns — store.go kaynağından <table> için (kolon → CH tipi).
// CREATE TABLE gövdelerini ve sonradan gelen ALTER ADD COLUMN satırlarını
// BİRLEŞTİRİR; aynı tablo için birden fazla CREATE bloğu varsa (kurulum
// tarihçesi) hepsi taranır.
func chSchemaColumns(t *testing.T, ddl, table string) map[string]string {
	t.Helper()
	out := map[string]string{}

	marker := "CREATE TABLE IF NOT EXISTS " + table + " ("
	rest := ddl
	for {
		i := strings.Index(rest, marker)
		if i < 0 {
			break
		}
		rest = rest[i+len(marker):]
		for _, ln := range strings.Split(rest, "\n") {
			ln = strings.TrimSpace(stripSQLLineComment(ln))
			ln = strings.TrimSpace(strings.TrimSuffix(ln, ","))
			if ln == "" {
				continue
			}
			if strings.HasPrefix(ln, ")") {
				break
			}
			f := strings.Fields(ln)
			if len(f) < 2 {
				continue
			}
			switch strings.ToUpper(f[0]) {
			case "INDEX", "PROJECTION", "CONSTRAINT", "PRIMARY", "ORDER",
				"PARTITION", "TTL", "SETTINGS", "ENGINE":
				continue
			}
			if typ := chTypeOf(strings.TrimSpace(ln[len(f[0]):])); typ != "" {
				out[f[0]] = typ
			}
		}
	}

	alter := "ALTER TABLE " + table + " ADD COLUMN IF NOT EXISTS "
	rest = ddl
	for {
		i := strings.Index(rest, alter)
		if i < 0 {
			break
		}
		rest = rest[i+len(alter):]
		tail := rest
		if j := strings.IndexAny(tail, "\n`"); j >= 0 {
			tail = tail[:j]
		}
		tail = strings.TrimSpace(stripSQLLineComment(tail))
		f := strings.Fields(tail)
		if len(f) < 2 {
			continue
		}
		if typ := chTypeOf(strings.TrimSpace(tail[len(f[0]):])); typ != "" {
			out[f[0]] = typ
		}
	}

	return out
}

// stripSQLLineComment — satır sonundaki -- yorumunu atar, tırnak içindeki
// tireleri korur.
func stripSQLLineComment(ln string) string {
	inQuote := false
	for i := 0; i+1 < len(ln); i++ {
		if ln[i] == '\'' {
			inQuote = !inQuote
			continue
		}
		if !inQuote && ln[i] == '-' && ln[i+1] == '-' {
			return ln[:i]
		}
	}
	return ln
}

// chTypeOf — kolon adından sonraki metinden tip ifadesini ayırır. Parantez
// derinliği sayıldığı için Array(LowCardinality(String)) ve Map(A, B) gibi
// içinde boşluk/virgül taşıyan tipler bütün olarak çıkar; DEFAULT / CODEC /
// MATERIALIZED kuyrukları düşer.
func chTypeOf(rest string) string {
	depth := 0
	for i := 0; i < len(rest); i++ {
		switch rest[i] {
		case '(':
			depth++
		case ')':
			depth--
		case ' ', '\t', ',':
			if depth == 0 {
				return strings.TrimSpace(rest[:i])
			}
		}
	}
	return strings.TrimSpace(rest)
}

func stripCHWrappers(s string) string {
	s = strings.TrimSpace(s)
	for {
		switch {
		case strings.HasPrefix(s, "LowCardinality(") && strings.HasSuffix(s, ")"):
			s = strings.TrimSpace(s[len("LowCardinality(") : len(s)-1])
		case strings.HasPrefix(s, "Nullable(") && strings.HasSuffix(s, ")"):
			s = strings.TrimSpace(s[len("Nullable(") : len(s)-1])
		default:
			return s
		}
	}
}

// sampleForCHType — clickhouse-go sürücüsünün bu kolon için üreteceği Go
// değerinin temsilcisi. Modellenmeyen bir tip görürse testi düşürür: yeni
// bir kolon tipini sessizce geçirmek, import un o kolonu bozuk yazması
// demek.
func sampleForCHType(t *testing.T, table, col, chType string) (any, bool) {
	t.Helper()
	base := stripCHWrappers(chType)

	if strings.HasPrefix(base, "Array(") && strings.HasSuffix(base, ")") {
		switch stripCHWrappers(base[len("Array(") : len(base)-1]) {
		case "String":
			return []string{"alpha", "beta"}, true
		case "UInt64":
			return []uint64{1, 1755900000000000001}, true
		case "Float64":
			return []float64{1.5, -2.25}, true
		}
		t.Errorf("%s.%s: dizi tipi %q config export/import yolunda modellenmiyor — "+
			"coerceForCHType Array dalına eklenmeli", table, col, chType)
		return nil, false
	}

	switch {
	case base == "String", base == "UUID",
		strings.HasPrefix(base, "FixedString"), strings.HasPrefix(base, "Enum"):
		return "sample-" + col, true
	case strings.HasPrefix(base, "DateTime"), strings.HasPrefix(base, "Date"):
		return time.Date(2026, 8, 23, 11, 22, 33, 123456789, time.UTC), true
	case base == "Bool":
		return true, true
	case base == "UInt8":
		return uint8(7), true
	case base == "UInt16":
		return uint16(4242), true
	case base == "UInt32":
		return uint32(300000), true
	case base == "UInt64":
		// 2^53 üstü: JS güvenli tamsayı tavanını aşan version kolonları
		// float e düşerse gidiş-dönüş burada kırılır.
		return uint64(1755900000000000001), true
	case base == "Int8":
		return int8(-7), true
	case base == "Int16":
		return int16(-4242), true
	case base == "Int32":
		return int32(-300000), true
	case base == "Int64":
		return int64(-1755900000000000001), true
	case base == "Float32":
		return float32(1.5), true
	case base == "Float64":
		return float64(-2.25), true
	}

	t.Errorf("%s.%s: CH tipi %q config export/import yolunda modellenmiyor — "+
		"coerceForCHType + zeroForCHType genişletilmeli, yoksa import bu kolonu bozuk yazar",
		table, col, chType)
	return nil, false
}

func sameConfigValue(want, got any) bool {
	if wt, ok := want.(time.Time); ok {
		gt, ok2 := got.(time.Time)
		return ok2 && wt.Equal(gt)
	}
	return reflect.DeepEqual(want, got)
}
