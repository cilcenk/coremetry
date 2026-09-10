package appschema

import (
	"reflect"
	"strings"
	"testing"
)

// appschema_test.go — v0.10.115. SQLCODE'lu hatada ŞEMA KANITI.
//
// Operatör: "SQLCODE içeren DB hatalarında hedef kolonun tipi/uzunluğu
// bilinmediği için model 'muhtemelen telefon numarası' gibi tahmin
// yürütüyor." Üç saf parça: katalog CSV'si, hata sinyali, SQL hedefleri.

const db2CSV = `TABSCHEMA,TABNAME,COLNAME,TYPENAME,LENGTH,SCALE,NULLS
SHOP,ORDER_PHONE,CUSTOMER_NO,DECIMAL,15,0,N
SHOP,ORDER_PHONE,PHONE,VARCHAR,10,0,N
SHOP,ORDER_PHONE,NOTE,VARCHAR,200,0,Y
SHOP,CUSTOMER,CUSTOMER_NO,DECIMAL,15,0,N
`

func TestParseCSVDialects(t *testing.T) {
	cases := []struct {
		name string
		csv  string
	}{
		{"db2 syscat", db2CSV},
		{"oracle all_tab_columns ; ayraç", "OWNER;TABLE_NAME;COLUMN_NAME;DATA_TYPE;DATA_LENGTH;DATA_SCALE;NULLABLE\nSHOP;ORDER_PHONE;CUSTOMER_NO;NUMBER;15;0;N\nSHOP;ORDER_PHONE;PHONE;VARCHAR2;10;;N\nSHOP;ORDER_PHONE;NOTE;VARCHAR2;200;;Y\nSHOP;CUSTOMER;CUSTOMER_NO;NUMBER;15;0;N\n"},
		{"information_schema sekme + BOM + küçük harf", "\uFEFFtable_schema\ttable_name\tcolumn_name\tdata_type\tcharacter_maximum_length\tnumeric_scale\tis_nullable\nbsa\torder_phone\tcustomer_no\tnumeric\t15\t0\tNO\nbsa\torder_phone\tphone\tcharacter varying\t10\t\tNO\nbsa\torder_phone\tnote\tcharacter varying\t200\t\tYES\nbsa\tcustomer\tcustomer_no\tnumeric\t15\t0\tNO\n"},
		{"el yazımı", "table,column,type,length,nullable\nORDER_PHONE,CUSTOMER_NO,DECIMAL,15,N\nORDER_PHONE,PHONE,VARCHAR,10,N\nORDER_PHONE,NOTE,VARCHAR,200,Y\nCUSTOMER,CUSTOMER_NO,DECIMAL,15,N\n"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			cat, err := ParseCSV(strings.NewReader(c.csv))
			if err != nil {
				t.Fatal(err)
			}
			tb, cols := cat.Count()
			if tb != 2 || cols != 4 {
				t.Fatalf("tablo=%d kolon=%d, istenen 2/4: %+v", tb, cols, cat.Tables)
			}
			tel := cat.Tables["ORDER_PHONE"][1]
			if tel.Name != "PHONE" || tel.Length != 10 || tel.Nullable {
				t.Errorf("PHONE: %+v", tel)
			}
			if cat.Tables["ORDER_PHONE"][2].Nullable != true {
				t.Error("NOTE NULL olmalı")
			}
		})
	}
	for _, bad := range []string{"", "a,b\n1,2\n", "table,column\nT,C\n"} {
		if _, err := ParseCSV(strings.NewReader(bad)); err == nil {
			t.Errorf("bozuk CSV kabul edildi: %q", bad)
		}
	}
}

func TestSQLErrorSignalTable(t *testing.T) {
	cases := []struct {
		text     string
		vendor   string
		code     string
		state    string
		hintPart string
		ok       bool
	}{
		{"com.ibm.db2.jcc.am.SqlDataException: DB2 SQL Error: SQLCODE=-302, SQLSTATE=22001, SQLERRMC=null, DRIVER=4.26.14", "db2", "-302", "22001", "sığmıyor", true},
		{"DB2 SQL Error: SQLCODE=-407, SQLSTATE=23502", "db2", "-407", "23502", "NOT NULL", true},
		{"DB2 SQL Error: SQLCODE=-9999, SQLSTATE=22001", "db2", "-9999", "22001", "uzunluğunu aşıyor", true},
		{"java.sql.SQLException: ORA-12899: value too large for column \"SHOP\".\"ORDER_PHONE\".\"PHONE\" (actual: 11, maximum: 10)", "oracle", "ORA-12899", "", "uzunluğunu aşıyor", true},
		{"org.postgresql.util.PSQLException: ERROR: value too long for type character varying(10) (SQLSTATE 22001)", "postgres", "", "22001", "uzunluğunu", true},
		{"SQLState: 23505 duplicate key", "postgres", "", "23505", "unique", true},
		{"java.lang.NullPointerException at X", "", "", "", "", false},
	}
	for _, c := range cases {
		sig, ok := SQLErrorSignal(c.text)
		if ok != c.ok {
			t.Errorf("%q: ok=%v", c.text, ok)
			continue
		}
		if !ok {
			continue
		}
		if sig.Vendor != c.vendor || sig.SQLCode != c.code || sig.SQLState != c.state || !strings.Contains(sig.Hint, c.hintPart) {
			t.Errorf("%q → %+v", c.text, sig)
		}
	}
}

func TestTargetsOfTable(t *testing.T) {
	cases := []struct {
		name string
		sql  string
		want Targets
	}{
		{"insert kolon listesi", "INSERT INTO SHOP.ORDER_PHONE (CUSTOMER_NO, PHONE, NOTE) VALUES (?, ?, ?)", Targets{Tables: []string{"ORDER_PHONE"}, Columns: []string{"CUSTOMER_NO", "PHONE", "NOTE"}}},
		{"update set", "update order_phone set phone = #{phone}, note=#{a} where customer_no = ?", Targets{Tables: []string{"ORDER_PHONE"}, Columns: []string{"PHONE", "NOTE"}}},
		{"select join", "SELECT t.PHONE FROM ORDER_PHONE t JOIN CUSTOMER m ON m.CUSTOMER_NO = t.CUSTOMER_NO WHERE t.PHONE = ?", Targets{Tables: []string{"ORDER_PHONE", "CUSTOMER"}}},
		{"delete", "DELETE FROM ORDER_PHONE WHERE CUSTOMER_NO = ?", Targets{Tables: []string{"ORDER_PHONE"}}},
		{"mapper bloğu satır numaralı + XML + CDATA", "11| <select id=\"orderPhoneSelect\">\n12|   SELECT <include refid=\"cols\"/>\n13|     FROM ORDER_PHONE\n14|    WHERE PHONE = <![CDATA[ #{phone} ]]>\n15| </select>", Targets{Tables: []string{"ORDER_PHONE"}}},
		{"boş", "   ", Targets{}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := TargetsOf(c.sql)
			if !reflect.DeepEqual(got, c.want) {
				t.Fatalf("got %+v want %+v", got, c.want)
			}
		})
	}
}

func TestLookupAndPromptSection(t *testing.T) {
	cat, _ := ParseCSV(strings.NewReader(db2CSV))
	cols, dropped := Lookup(cat, TargetsOf("INSERT INTO ORDER_PHONE (CUSTOMER_NO, PHONE) VALUES (?, ?)"))
	if len(cols) != 2 || dropped != 0 || cols[1].Name != "PHONE" {
		t.Fatalf("lookup: %+v dropped=%d", cols, dropped)
	}
	// Kolon listesi tabloya ait değilse tablonun tamamı (yanlış eşleşme
	// yerine tam kanıt).
	cols, _ = Lookup(cat, Targets{Tables: []string{"customer"}, Columns: []string{"PHONE"}})
	if len(cols) != 1 || cols[0].Table != "CUSTOMER" {
		t.Errorf("küçük-harf tablo/yabancı kolon: %+v", cols)
	}
	if c, _ := Lookup(cat, Targets{Tables: []string{"YOK"}}); c != nil {
		t.Error("olmayan tablo kolon üretti")
	}
	sig := Signal{Vendor: "db2", SQLCode: "-302", SQLState: "22001", Hint: "değer kolon uzunluğunu aşıyor"}
	cols, _ = Lookup(cat, TargetsOf("INSERT INTO ORDER_PHONE (CUSTOMER_NO, PHONE, NOTE) VALUES (?, ?, ?)"))
	block, shown := PromptSection(&sig, cols, 0, "2026-08-28", 0)
	for _, want := range []string{"ŞEMA BAĞLAMI", "2026-08-28", "SQLCODE -302, SQLSTATE 22001 — değer kolon uzunluğunu aşıyor",
		"ORDER_PHONE.PHONE VARCHAR(10) NOT NULL", "ORDER_PHONE.CUSTOMER_NO DECIMAL(15) NOT NULL", "ORDER_PHONE.NOTE VARCHAR(200) NULL"} {
		if !strings.Contains(block, want) {
			t.Errorf("eksik: %q\n%s", want, block)
		}
	}
	if shown != 3 {
		t.Errorf("shown=%d", shown)
	}
	// Bütçe: yalnız ilk satır sığar → kırpma notu.
	small, shown := PromptSection(&sig, cols, 0, "", 260)
	if shown >= 3 || !strings.Contains(small, "[şema kırpıldı:") {
		t.Errorf("kırpma bildirilmedi: shown=%d\n%s", shown, small)
	}
	// Sinyal var, tablo katalogda yok → dürüst cümle.
	none, _ := PromptSection(&sig, nil, 0, "", 0)
	if !strings.Contains(none, "katalogda bulunamadı") {
		t.Errorf("tablo yokken cümle yok: %s", none)
	}
	if b, _ := PromptSection(nil, nil, 0, "", 0); b != "" {
		t.Error("hiçbir şey yokken blok üretildi")
	}
	if m := MaskSummary(cols, 3); m != "\n\n[şema: ORDER_PHONE · 3 kolon]" {
		t.Errorf("maske özeti: %q", m)
	}
	if len(SnapshotSQL()) != 5 || !strings.Contains(SnapshotSQL()["db2"], "SYSCAT.COLUMNS") {
		t.Error("SnapshotSQL eksik")
	}
	for f, q := range SnapshotSQL() {
		if !strings.HasPrefix(strings.TrimSpace(q), "SELECT") {
			t.Errorf("%s sorgusu SELECT değil", f)
		}
	}
}

func TestLookupCap(t *testing.T) {
	var sb strings.Builder
	sb.WriteString("table,column,type,length,nullable\n")
	for i := 0; i < 60; i++ {
		sb.WriteString("BIG,C" + strings.Repeat("X", i%3) + string(rune('A'+i%26)) + string(rune('0'+i/26)) + ",VARCHAR,10,N\n")
	}
	cat, err := ParseCSV(strings.NewReader(sb.String()))
	if err != nil {
		t.Fatal(err)
	}
	cols, dropped := Lookup(cat, Targets{Tables: []string{"BIG"}})
	if len(cols) != maxColumnsPerLookup || dropped != 60-maxColumnsPerLookup {
		t.Errorf("tavan: %d gösterildi, %d düştü", len(cols), dropped)
	}
	block, _ := PromptSection(nil, cols, dropped, "", 0)
	if !strings.Contains(block, "[şema kırpıldı: 20 kolon gösterilmedi]") {
		t.Errorf("düşen sayısı notta yok:\n%s", block[len(block)-80:])
	}
}
