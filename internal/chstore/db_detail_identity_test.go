package chstore

import (
	"strings"
	"testing"
)

// db_detail_identity_test.go — v0.9.821 REGRESYON.
//
// BUG: /databases satır kimliği (system, instance, db_name) ÜÇLÜSÜYDÜ
// ama çekmece yalnız (system, instance) soruyordu. Bir host'ta birden
// çok veritabanı olan her kurulumda — Oracle SID'leri, PostgreSQL
// şemaları, MSSQL DB'leri — hangi satıra tıklanırsa tıklansın AYNI
// çekmece açılıyordu ve o host'un TÜM veritabanlarının toplamını
// gösteriyordu. Satır "COREBANK · 4.200 sorgu" derken çekmece 31.000
// gösteriyordu; ikisi de "doğru" görünüyor, hiçbir şey çelişkiyi
// söylemiyordu.
//
// Bu dosya kimliğin uçtan uca taşındığını sabitler.

func TestDBDetailNameFilter(t *testing.T) {
	cases := []struct {
		name     string
		expr     string
		dbName   string
		wantSQL  string
		wantArgs int
	}{
		{
			name: "boş dbName → yüklem YOK (eski davranış: tüm db'ler)",
			expr: "db_name", dbName: "",
			wantSQL: "", wantArgs: 0,
		},
		{
			name: "MV yolu kolon adıyla",
			expr: "db_name", dbName: "COREBANK",
			wantSQL: " AND db_name = ?", wantArgs: 1,
		},
		{
			name: "ham spans yolu dbNameExpr ile",
			expr: dbNameExpr, dbName: "COREBANK",
			wantSQL: " AND " + dbNameExpr + " = ?", wantArgs: 1,
		},
		{
			name: "'default' de gerçek bir değer — filtrelenebilmeli",
			expr: "db_name", dbName: "default",
			wantSQL: " AND db_name = ?", wantArgs: 1,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			sql, args := dbDetailNameFilter(c.expr, c.dbName)
			if sql != c.wantSQL {
				t.Fatalf("sql = %q, want %q", sql, c.wantSQL)
			}
			if len(args) != c.wantArgs {
				t.Fatalf("args = %v, want %d adet", args, c.wantArgs)
			}
			if c.wantArgs == 1 && args[0] != c.dbName {
				t.Fatalf("args[0] = %v, want %q", args[0], c.dbName)
			}
		})
	}
}

// İKİ DB, AYNI INSTANCE — ayrışmalarını sağlayan tek şey db_name
// yüklemi. Yüklem düşerse iki çağrı BİREBİR aynı SQL'i üretir, yani
// bug geri gelmiş demektir.
func TestDBDetailTwoDatabasesOneInstanceDiverge(t *testing.T) {
	a, aArgs := dbDetailNameFilter("db_name", "COREBANK")
	b, bArgs := dbDetailNameFilter("db_name", "CARDS")
	if a != b {
		t.Fatalf("SQL metni db'ye göre değişmemeli (yalnız arg değişir): %q vs %q", a, b)
	}
	if a == "" {
		t.Fatalf("yüklem hiç üretilmedi — iki db aynı sorguya düşer (v0.9.821 bug'ı)")
	}
	if aArgs[0] == bArgs[0] {
		t.Fatalf("iki farklı db aynı arg'ı üretti: %v", aArgs[0])
	}
}

// dbNameExpr, MV'nin db_name üretimiyle BİREBİR aynı olmalı: farklı
// yazılırsa ham yol aynı mantıksal veritabanını farklı adla çözer ve
// çekmecenin iki yarısı çelişir (dbInstanceExpr / v0.9.274 dersi).
func TestDBNameExprMatchesMVDefinition(t *testing.T) {
	if !strings.Contains(dbNameExpr, "attr_keys, 'db.name'") {
		t.Fatalf("dbNameExpr db.name attr'ını okumuyor: %q", dbNameExpr)
	}
	if !strings.Contains(dbNameExpr, "'default'") {
		t.Fatalf("dbNameExpr 'default' düşüşünü taşımıyor — MV taşıyor, iki yol ayrışır: %q", dbNameExpr)
	}
	if strings.Contains(dbNameExpr, "peer_service") {
		t.Fatalf("dbNameExpr instance zincirine bulaşmış: %q", dbNameExpr)
	}
}

// dbTopCallersSQL — ALFABETİK KESİM YOK (v0.9.821 / messaging v0.9.813
// emsali). Eski hâl `ORDER BY db_system, instance, db_name, c DESC
// LIMIT 2000` idi: sıralama önce KİMLİĞE göre, kesme global 2000'de —
// yani adı alfabenin sonunda kalan her db'nin çağıranları tamamen
// düşüyor ve tabloda "—" görünüyordu ("bu db'yi kimse çağırmıyor" diye
// okunan bir boşluk).
func TestDBTopCallersSQLUsesLimitByGroup(t *testing.T) {
	if !strings.Contains(dbTopCallersSQL, "LIMIT 5 BY db_system, instance, db_name") {
		t.Fatalf("LIMIT n BY yok — kesme grup başına değil:\n%s", dbTopCallersSQL)
	}
	if !strings.Contains(dbTopCallersSQL, "ORDER BY c DESC") {
		t.Fatalf("ORDER BY saf hacim değil:\n%s", dbTopCallersSQL)
	}
	// Kimlik alanları ORDER BY'da OLMAMALI — alfabetik kesimin imzası bu.
	orderIdx := strings.Index(dbTopCallersSQL, "ORDER BY")
	limitIdx := strings.Index(dbTopCallersSQL, "LIMIT")
	if orderIdx < 0 || limitIdx < 0 {
		t.Fatal("ORDER BY / LIMIT bulunamadı")
	}
	orderClause := dbTopCallersSQL[orderIdx:limitIdx]
	for _, ident := range []string{"db_system", "instance", "db_name"} {
		if strings.Contains(orderClause, ident) {
			t.Fatalf("ORDER BY hâlâ kimliğe göre sıralıyor (%s) — alfabetik kesim geri geldi:\n%s",
				ident, orderClause)
		}
	}
	// Dıştaki tavan satır tavanı × çağıran sayısı ile tam oturmalı,
	// yoksa tel-bayt tavanı bazı db'leri ayrım gözetmeden keser.
	if !strings.Contains(dbTopCallersSQL, "LIMIT 25000") {
		t.Fatalf("dış tavan 5000×5 ile oturmuyor:\n%s", dbTopCallersSQL)
	}
	if !strings.Contains(dbTopCallersSQL, "max_execution_time") {
		t.Fatal("max_execution_time yok")
	}
}

// dbOverviewCapped — tavana DAYANMAK ilan edilmeli.
func TestDBOverviewCapped(t *testing.T) {
	if dbOverviewCapped(dbOverviewRowLimit - 1) {
		t.Fatal("tavanın altında capped işaretlendi")
	}
	if !dbOverviewCapped(dbOverviewRowLimit) {
		t.Fatal("tam tavanda capped değil — CH tam LIMIT döndüğünde fazlası var mı BİLİNMEZ")
	}
	if !dbOverviewCapped(dbOverviewRowLimit + 10) {
		t.Fatal("tavanın üstünde capped değil")
	}
	if dbOverviewCapped(0) {
		t.Fatal("boş sonuç capped işaretlendi")
	}
}
