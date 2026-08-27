package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/cilcenk/coremetry/internal/appschema"
	"github.com/cilcenk/coremetry/internal/copilot"
	"github.com/cilcenk/coremetry/internal/devops"
)

// schema_catalog_test.go — v0.10.115. SQLCODE'LU HATADA ŞEMA UÇTAN UCA.
//
// Operatör spec'i: "SQLCODE'lu örnek bir hatanın uçtan uca bağlam
// kurulumu (sahte katalog ile)". Sahte katalog + sahte sağlayıcı:
// prompt'ta kolon tanımı, ai_calls kopyasında yalnız özet, span'da sayı.

const testDB2Stack = "" +
	"org.springframework.dao.DataIntegrityViolationException: PreparedStatementCallback; SQL []; DB2 SQL Error: SQLCODE=-302, SQLSTATE=22001, SQLERRMC=null, DRIVER=4.26.14\n" +
	"\tat com.acme.billing.CardService.savePhone(CardService.java:29)\n" +
	"Caused by: com.ibm.db2.jcc.am.SqlDataException: DB2 SQL Error: SQLCODE=-302, SQLSTATE=22001\n"

func schemaServer(t *testing.T, fp *fakeProvider, rec copilot.Recorder) *Server {
	t.Helper()
	s := codeServer(t, fp, rec)
	cat, err := appschema.ParseCSV(strings.NewReader(
		"TABSCHEMA,TABNAME,COLNAME,TYPENAME,LENGTH,SCALE,NULLS\n" +
			"BSA,INT_TFRAUD,MUSTERI_NO,DECIMAL,15,0,N\n" +
			"BSA,INT_TFRAUD,TELNO,VARCHAR,10,0,N\n" +
			"BSA,INT_TFRAUD,ACIKLAMA,VARCHAR,200,0,Y\n"))
	if err != nil {
		t.Fatal(err)
	}
	cat.ImportedAt = 1_787_875_200_000 // 2026-08-28 00:00 UTC
	s.schema = appschema.NewService()
	s.schema.Set(cat)
	return s
}

func TestSchemaEvidenceEndToEnd(t *testing.T) {
	fp := newFakeProvider(t, false)
	rec := newCapRecorder()
	s := schemaServer(t, fp, rec)

	// (1) Kanıt kurulumu: hata metni sinyali + hata span'ının SQL'i.
	se := s.buildSchemaEvidence(testDB2Stack,
		[]string{"INSERT INTO BSA.INT_TFRAUD (MUSTERI_NO, TELNO) VALUES (?, ?)"}, nil)
	if !se.Signal || se.Columns != 2 {
		t.Fatalf("kanıt: signal=%v columns=%d\n%s", se.Signal, se.Columns, se.Block)
	}
	for _, want := range []string{"ŞEMA BAĞLAMI", "2026-08-28", "SQLCODE -302, SQLSTATE 22001", "INT_TFRAUD.TELNO VARCHAR(10) NOT NULL", "INT_TFRAUD.MUSTERI_NO DECIMAL(15) NOT NULL"} {
		if !strings.Contains(se.Block, want) {
			t.Errorf("blokta yok: %q\n%s", want, se.Block)
		}
	}
	if strings.Contains(se.Block, "ACIKLAMA") {
		t.Error("INSERT listesinde olmayan kolon gönderildi")
	}
	if se.Summary != "\n\n[şema: INT_TFRAUD · 2 kolon]" {
		t.Errorf("maske özeti: %q", se.Summary)
	}

	// (2) Kod bağlamı + şema → prompt: kod bloğu ÖNCE, şema sonra; kayıt
	// kopyasında kolon tanımı YOK, özet var.
	cc := devops.CodeContext{Repo: "core-service", Outcome: devops.CodeOK,
		Windows: []devops.CodeWindow{{Path: "/src/CardService.java", Frame: "com.acme.billing.CardService.savePhone(CardService.java:29)",
			Line: 29, FromLine: 27, ToLine: 31, Content: "29| repo.savePhone(customer, phoneWithCountryCode); // SECRET_MARK"}}}
	r := httptest.NewRequest(http.MethodPost, "/api/copilot/explain-exception/fp1", nil)
	if _, err := s.copilotExplainEvidence(r, copilot.SystemPromptException(), copilot.SystemPromptExceptionWithCode(), "Exception GRUBU: x", cc, se); err != nil {
		t.Fatal(err)
	}
	sent := fp.sent()[0]
	ci, si := strings.Index(sent, "KOD BAĞLAMI (depo"), strings.Index(sent, "ŞEMA BAĞLAMI")
	if ci < 0 || si < 0 || si < ci {
		t.Fatalf("sıra kod > şema değil (kod=%d şema=%d)", ci, si)
	}
	if !strings.Contains(sent, "INT_TFRAUD.TELNO VARCHAR(10) NOT NULL") {
		t.Fatal("kolon tanımı modele gitmedi")
	}
	sample := rec.wait(t, 1)[0].PromptSample
	if strings.Contains(sample, "VARCHAR(10)") || strings.Contains(sample, "SECRET_MARK") {
		t.Fatalf("kayıt kopyası kolon tanımı/kod taşıyor:\n%s", sample)
	}
	if !strings.Contains(sample, "[şema: INT_TFRAUD · 2 kolon]") || !strings.Contains(sample, "[kod: core-service/src/CardService.java:27-31") {
		t.Fatalf("kayıt özetleri eksik:\n%s", sample)
	}

	// (3) Kod çözülemedi ama şema var → MissingBlock + şema birlikte.
	fp2 := newFakeProvider(t, false)
	s2 := schemaServer(t, fp2, newCapRecorder())
	miss := devops.CodeContext{Repo: "core-service", Reason: "ağaçta eşleşen dosya yok", Outcome: devops.CodeTreeMiss}
	if _, err := s2.copilotExplainEvidence(r, copilot.SystemPromptException(), copilot.SystemPromptExceptionWithCode(), "Exception GRUBU: x", miss, se); err != nil {
		t.Fatal(err)
	}
	if p := fp2.sent()[0]; !strings.Contains(p, "KOD BAĞLAMI İSTENDİ — ÇÖZÜLEMEDİ") || !strings.Contains(p, "INT_TFRAUD.TELNO VARCHAR(10)") {
		t.Fatalf("kodsuz yolda şema düştü:\n%s", p)
	}
}

func TestSchemaEvidenceDegradesHonestly(t *testing.T) {
	fp := newFakeProvider(t, false)
	s := schemaServer(t, fp, newCapRecorder())
	// Sinyal var, tablo katalogda yok → dürüst cümle, kolon yok.
	se := s.buildSchemaEvidence("DB2 SQL Error: SQLCODE=-803, SQLSTATE=23505", []string{"INSERT INTO YOK_TABLO (A) VALUES (?)"}, nil)
	if !se.Signal || se.Columns != 0 || !strings.Contains(se.Block, "katalogda bulunamadı") || !strings.Contains(se.Block, "tekil indeks") {
		t.Errorf("tablo yok: %+v", se)
	}
	// Sinyal yok, SQL yok → hiç blok yok (DB dışı hatada gürültü olmasın).
	if se := s.buildSchemaEvidence("java.lang.NullPointerException", nil, nil); se.Block != "" || se.Summary != "" {
		t.Errorf("DB dışı hatada blok üretildi: %+v", se)
	}
	// Sinyal yok ama mapper bloğu katalogda bir tabloya işaret ediyor →
	// kolonlar gider (sorgu hatası SQLCODE taşımadan da olabilir).
	se = s.buildSchemaEvidence("java.sql.SQLException: timeout", nil,
		[]string{"11| <select id=\"q\">\n12| SELECT TELNO FROM INT_TFRAUD WHERE MUSTERI_NO = #{m}\n13| </select>"})
	if se.Columns != 3 || strings.Contains(se.Block, "Hata sinyali") {
		t.Errorf("mapper hedefi: %+v", se)
	}
	// Katalog yok + sinyal yok → boş; katalog yok + sinyal var → yalnız sinyal.
	s.schema = appschema.NewService()
	if se := s.buildSchemaEvidence("x", []string{"INSERT INTO T (A) VALUES (?)"}, nil); se.Block != "" {
		t.Error("katalogsuz + sinyalsiz blok")
	}
	if se := s.buildSchemaEvidence("ORA-12899: value too large", nil, nil); !se.Signal || !strings.Contains(se.Block, "ORA-12899") {
		t.Errorf("katalogsuz sinyal: %+v", se)
	}
}

func TestSchemaCatalogRoutes(t *testing.T) {
	s := &Server{schema: appschema.NewService()}
	// GET boş: özet + snapshotSql dolu.
	rr := httptest.NewRecorder()
	s.getSchemaCatalog(rr, httptest.NewRequest(http.MethodGet, "/api/settings/schema-catalog", nil))
	if rr.Code != 200 || !strings.Contains(rr.Body.String(), `"snapshotSql"`) || !strings.Contains(rr.Body.String(), "SYSCAT.COLUMNS") {
		t.Fatalf("GET: %d %s", rr.Code, rr.Body.String()[:120])
	}
	// PUT bozuk CSV → 400 (kayıt denenmez; store nil güvenli).
	rr = httptest.NewRecorder()
	s.putSchemaCatalog(rr, httptest.NewRequest(http.MethodPut, "/api/settings/schema-catalog", strings.NewReader(`{"csv":"a,b\n1,2\n"}`)))
	if rr.Code != http.StatusBadRequest || !strings.Contains(rr.Body.String(), "CSV") {
		t.Fatalf("bozuk CSV: %d %s", rr.Code, rr.Body.String())
	}
}
