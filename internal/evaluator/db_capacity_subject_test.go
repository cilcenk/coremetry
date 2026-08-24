package evaluator

import (
	"os"
	"strings"
	"testing"

	"github.com/cilcenk/coremetry/internal/chstore"
)

// db_capacity_subject_test.go — v0.9.1338 (entity-model Faz 4b).
//
// ORİJİNAL SEMPTOM: bir Oracle kapasite problemi `problems.service`
// kolonuna `corebank-scan.prod` yazıyordu (v0.9.402'den beri). O değer
// hiçbir servis kataloğunda ve hiçbir `spans.service_name`de yok, ama UI
// onu servis sözleşmesiyle okuyup `/service?name=corebank-scan.prod`
// linki kuruyordu — link açılıyor, sayfa BOŞ. v0.9.402 birleşik adı
// söktüğü için semptom "kırık ad" olmaktan çıkmış, "geçerli görünen
// çıkmaz link"e dönüşmüştü; yani kusur GİZLENDİ, çözülmedi.

// TestCapacitySubjectTypesTheDBInstance — özne + tür ÇİFTİ, tablo-güdümlü.
//
// İkisi TEK fonksiyondan çıkıyor çünkü ayrıldıklarında ayrışıyorlar: db
// biçiminde bir dizge yazıp Kind'ı 'service' bırakan bir dal, satırı hem
// çıkmaz link hem çözümlenemez ad yapardı (v0.9.1029'un topoloji
// tarafında ölçtüğü kusur).
func TestCapacitySubjectTypesTheDBInstance(t *testing.T) {
	cases := []struct {
		name        string
		dbsys       string
		instance    string
		wantSubject string
		wantKind    string
	}{
		// Kataloğun ÜÇ farklı dbsys'i — hepsi büyük harfli yazılmış
		// (reason dizgisi için) ve hepsi küçük harfe inmeli.
		{"oracle", "ORACLE", "corebank-scan.prod",
			"db:oracle@corebank-scan.prod", chstore.ProblemKindDB},
		{"postgres", "POSTGRES", "pg-primary.prod",
			"db:postgres@pg-primary.prod", chstore.ProblemKindDB},
		{"redis", "REDIS", "cache-01",
			"db:redis@cache-01", chstore.ProblemKindDB},

		// ── ÇÖZÜLMEMİŞ DAL — bayt-bayt v0.9.402 davranışı ──────────────
		// Kırmızı çizgi: kodlanamayan bir girdi HAM instance'ı ve
		// 'service' türünü verir, yani bugünkü satırın birebir aynısı.
		// Bir satır ASLA düşmez ve ASLA yanlış özneye yazılmaz.
		{"instance yok", "ORACLE", "", "", chstore.ProblemKindService},
		{"dbsys yok", "", "corebank-scan.prod",
			"corebank-scan.prod", chstore.ProblemKindService},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			subject, kind := capacitySubject(tc.dbsys, tc.instance)
			if subject != tc.wantSubject {
				t.Errorf("capacitySubject(%q,%q) öznesi %q, %q bekleniyordu",
					tc.dbsys, tc.instance, subject, tc.wantSubject)
			}
			if kind != tc.wantKind {
				t.Errorf("capacitySubject(%q,%q) türü %q, %q bekleniyordu",
					tc.dbsys, tc.instance, kind, tc.wantKind)
			}
			// Kodlandıysa GERİ çözülebilmeli — özne yazılabilir ama
			// okunamaz olsaydı, /inbox onu yine servis sanardı.
			if kind == chstore.ProblemKindDB {
				sys, inst, ok := chstore.ParseDBSubjectID(subject)
				if !ok || sys != strings.ToLower(tc.dbsys) || inst != tc.instance {
					t.Errorf("özne geri çözülemedi: (%q,%q,%v)", sys, inst, ok)
				}
			}
		})
	}
}

// TestEveryCapacityCheckProducesATypedSubject — KATALOG kapsaması.
//
// Yukarıdaki tablo üç dbsys örnekliyor; bu test kataloğun HEPSİNİ geziyor.
// Neden ayrı: yeni bir capacityCheck (MongoDB, MSSQL…) boş bir dbsys ile
// eklenirse tablo testi susardı ve o ailenin problemleri sessizce
// servis-özneli yazılmaya devam ederdi.
func TestEveryCapacityCheckProducesATypedSubject(t *testing.T) {
	if len(capacityChecks) == 0 {
		t.Fatal("capacityChecks boş — kapsama iddiası anlamsız")
	}
	for _, c := range capacityChecks {
		subject, kind := capacitySubject(c.dbsys, "some-instance.prod")
		if kind != chstore.ProblemKindDB {
			t.Errorf("check %q (dbsys=%q) türsüz özne üretiyor (kind=%q) — "+
				"dbsys boş bırakılmış olabilir; o satırın problemleri "+
				"/service?name= çıkmaz linkine geri döner", c.id, c.dbsys, kind)
		}
		if !strings.HasPrefix(subject, "db:") {
			t.Errorf("check %q öznesi db: öneki taşımıyor: %q", c.id, subject)
		}
	}
}

// TestCapacityRefreshCarriesTheSubjectForward — AÇIK satırların onarımı.
//
// Bu kapı olmadan, prod'un HÂLİHAZIRDA AÇIK db-capacity problemleri ham
// instance adında ve kind='service'te takılı kalırdı: yeni özne yalnız
// bir sonraki AÇILIŞTA görünürdü ve %87'de park etmiş bir tablespace
// problemi günlerce açık kalabilir (v0.8.309'un ölçtüğü senaryo).
//
// Kaynak taraması, çünkü onarım noktası bir if/else değil bir ATAMA
// ÇİFTİ: refresh kolunda Service ve Kind AYNI turda yazılmalı. Yalnız
// birini yazmak, satırı db biçiminde ama servis türünde bırakırdı.
func TestCapacityRefreshCarriesTheSubjectForward(t *testing.T) {
	b, err := os.ReadFile("db_capacity.go")
	if err != nil {
		t.Fatalf("db_capacity.go okunamadı: %v", err)
	}
	src := string(b)
	if !strings.Contains(src, "existing.Service = service") {
		t.Error("refresh kolu Service'i ileri taşımıyor — v0.9.402'nin " +
			"self-heal'i kaybolmuş")
	}
	if !strings.Contains(src, "existing.Kind = kind") {
		t.Error("refresh kolu Kind'ı ileri taşımıyor — AÇIK problemler db " +
			"biçiminde bir özne taşıyıp kind='service' kalır, yani /inbox " +
			"onları yine servis sanar ve çıkmaz link kurar")
	}
	// Açılış kolu da türü yazmalı.
	if !strings.Contains(src, "Kind:        kind") {
		t.Error("açılış kolu Problem.Kind'ı set etmiyor")
	}
	// Ve İKİSİ de AYNI çiftten gelmeli — ayrı çağrılar ayrışabilir.
	if strings.Count(src, "capacitySubject(c.dbsys, s.Instance)") != 1 {
		t.Error("özne çifti tek bir capacitySubject çağrısından gelmiyor — " +
			"iki ayrı çağrı, iki dalda ayrışabilen özne demek")
	}
}
