package chstore

import (
	"os"
	"strings"
	"testing"
)

// problem_subject_test.go — v0.9.1338 (entity-model Faz 4b).
//
// ORİJİNAL SEMPTOM: `problems.service` sorgusuz bir servis adı sayılıyordu
// ve bu HİÇBİR ZAMAN doğru değildi. db_capacity.go v0.9.402'den beri oraya
// bir receiver instance adı (`corebank-scan.prod`) yazıyor; o değer hiçbir
// servis kataloğunda, hiçbir `spans.service_name`de yok. Otuz küsur okuma
// yolu onu servis sanıp `/service?name=corebank-scan.prod` linki kuruyordu
// — HATA DEĞİL, CEVAPSIZLIK. Sayfa açılıyor, boş görünüyor.
//
// Bu dosya öznenin TÜRÜNÜ ve KODLAMASINI pinliyor.

// TestDBSubjectIDRoundTrip — kodlama/çözme çifti, tablo-güdümlü.
//
// En kritik satırlar boş-bileşen satırları: DBSubjectID boş dönünce
// çağıran HAM instance'a düşer, yani ÇÖZÜLMEMİŞ DAL bayt-bayt
// v0.9.402'nin çıktısını verir. O kırmızı çizgi burada duruyor.
func TestDBSubjectIDRoundTrip(t *testing.T) {
	cases := []struct {
		name       string
		system     string
		instance   string
		wantID     string
		wantSys    string
		wantInst   string
		wantParsed bool
	}{
		{"oracle receiver instance", "ORACLE", "corebank-scan.prod",
			"db:oracle@corebank-scan.prod", "oracle", "corebank-scan.prod", true},
		// dbsys BÜYÜK harfle geliyor (capacityCheck.dbsys reason dizgisi
		// için öyle yazılmış), span/topoloji tarafı KÜÇÜK. Normalize
		// edilmezse aynı veritabanı iki ayrı özne olurdu.
		{"already lowercase", "oracle", "corebank-dg.prod",
			"db:oracle@corebank-dg.prod", "oracle", "corebank-dg.prod", true},
		{"mixed case + boşluk", "  PostGres  ", " pg-01 ",
			"db:postgres@pg-01", "postgres", "pg-01", true},
		// Instance'ta '@' — ayırıcı İLK '@'; fazlası instance'a kalır ve
		// gidiş-dönüş bozulmaz.
		{"instance contains @", "mysql", "db@shard-3",
			"db:mysql@db@shard-3", "mysql", "db@shard-3", true},
		// ── kodlanamayan durumlar: ID boş → çağıran ham değere düşer ──
		{"boş system", "", "corebank-scan.prod", "", "", "", false},
		{"boş instance", "ORACLE", "", "", "", "", false},
		{"yalnız boşluk", "   ", "   ", "", "", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := DBSubjectID(tc.system, tc.instance)
			if got != tc.wantID {
				t.Fatalf("DBSubjectID(%q,%q) = %q, %q bekleniyordu",
					tc.system, tc.instance, got, tc.wantID)
			}
			sys, inst, ok := ParseDBSubjectID(got)
			if ok != tc.wantParsed {
				t.Fatalf("ParseDBSubjectID(%q) ok=%v, %v bekleniyordu", got, ok, tc.wantParsed)
			}
			if sys != tc.wantSys || inst != tc.wantInst {
				t.Fatalf("ParseDBSubjectID(%q) = (%q,%q), (%q,%q) bekleniyordu",
					got, sys, inst, tc.wantSys, tc.wantInst)
			}
		})
	}
}

// TestParseDBSubjectIDRejectsNonDB — bir SERVİS adı asla db öznesi gibi
// çözülmemeli.
//
// Negatif kontrol NEDEN gerekli: parse'ı "'@' içeriyorsa böl" diye yazmak
// cazip. O hâlde `checkout@v2` gibi bir servis adı — ya da queue/ext
// önekli bir topoloji düğümü — sessizce db öznesi olur ve okuma yolu onu
// bir veritabanı sanar. Kapı öneki ZORUNLU tutuyor.
func TestParseDBSubjectIDRejectsNonDB(t *testing.T) {
	for _, in := range []string{
		"",                        // boş
		"checkout",                // düz servis
		"checkout@v2",             // '@' taşıyan servis adı
		"queue:kafka:api.usage",   // kuyruk düğümü
		"ext:stripe",              // dış düğüm
		"database-router",         // 'db' ile başlıyor ama önek DEĞİL
		"db:oracle",               // '@' yok → instance yok
		"db:@corebank-scan.prod",  // system yarısı boş
		"db:oracle@",              // instance yarısı boş
		"corebank-scan.prod",      // v0.9.402'nin bugünkü ham değeri
		"DB:oracle@corebank.prod", // önek büyük harf — sözlükte yok
	} {
		if _, _, ok := ParseDBSubjectID(in); ok {
			t.Errorf("ParseDBSubjectID(%q) db öznesi saydı — servis adları ve "+
				"queue/ext düğümleri bu daldan GEÇMEMELİ", in)
		}
	}
}

// TestProblemSubjectKindNormalises — boş = service, üçüncü dal YOK.
//
// Boş İKİ yoldan gelir ve ikisi de "servis" demek: (a) kolonu ekleyen
// boot'un probe'u false okur ve SELECT kolonu atlar (iki-boot sözleşmesi,
// v0.9.614), (b) v0.9.1338 öncesi yazılmış 4800+ satır.
func TestProblemSubjectKindNormalises(t *testing.T) {
	cases := map[string]string{
		"":                 ProblemKindService,
		ProblemKindService: ProblemKindService,
		ProblemKindDB:      ProblemKindDB,
		// Tanınmayan bir değer AYNEN geçer: uydurmak, gelecekte eklenecek
		// bir türü sessizce servise çevirmek olurdu.
		"queue": "queue",
	}
	for in, want := range cases {
		if got := ProblemSubjectKind(in); got != want {
			t.Errorf("ProblemSubjectKind(%q) = %q, %q bekleniyordu", in, got, want)
		}
	}
}

// TestDBSubjectPrefixComesFromTheSharedVocabulary — İKİZ-YAZIM KAPISI.
//
// identity.go'nun tek kuralı: bir kimlik zincirini İKİNCİ KEZ YAZMA.
// DBSubjectID'nin `db:` öneki TopologyNodeIDPrefixes'ten okunmalı, kaynak
// metninde tırnak içinde `"db:"` olarak TEKRAR EDİLMEMELİ. Tekrar
// edilirse sözlük değiştiğinde (v0.9.1029 bunu bir kez yaşadı) özne
// biçimi düğüm biçiminden sessizce ayrışır.
func TestDBSubjectPrefixComesFromTheSharedVocabulary(t *testing.T) {
	b, err := os.ReadFile("identity.go")
	if err != nil {
		t.Fatalf("identity.go okunamadı: %v", err)
	}
	src := string(b)

	// Sözlükteki TEK meşru yazım: TopologyNodeIDPrefixes girdisi.
	if got := strings.Count(src, `"db:"`); got != 1 {
		t.Errorf(`identity.go'da "db:" %d kez yazılmış, 1 bekleniyordu `+
			`(yalnız TopologyNodeIDPrefixes girdisi). DBSubjectID öneki `+
			`dbNodePrefix() üzerinden sözlükten OKUMALI`, got)
	}
	// Ve kodlayıcı gerçekten oradan okuyor.
	if !strings.Contains(src, "return dbNodePrefix() + system") {
		t.Error("DBSubjectID öneki dbNodePrefix()'ten almıyor — sözlük tek " +
			"kaynak olmaktan çıkmış olabilir")
	}
	// Çözücü de öneki elle soymamalı: TopologyNodeIdentity ZATEN kind'ı
	// ve soyulmuş adı BİRLİKTE döndürüyor (v0.9.1029: ayrı türetilen kind
	// ile ad ayrışıyor).
	if !strings.Contains(src, "kind, rest := TopologyNodeIdentity(id)") {
		t.Error("ParseDBSubjectID öneki TopologyNodeIdentity ile çözmüyor — " +
			"elle TrimPrefix, kind ile adın ayrışma sınıfını geri getirir")
	}
}

// TestProblemKindColumnDefaultsToService — DDL KAPISI.
//
// Mutasyon taramasında (v0.9.1338) bulunan GERÇEK boşluk: `DEFAULT
// 'service'` yerine boş-dize varsayılanı yazmak HİÇBİR testi kırmıyordu. Go
// okuma yolu boşu normalize ettiği için ürün "bugün doğru" görünürdü —
// ama varsayılan SQL tarafında da okunuyor: kolon üstünde bir gün
// `WHERE kind = 'service'` / `WHERE kind != 'service'` yazan her yüzey
// (Faz 4b'nin /inbox ayrı-şerit sorgusu tam olarak bu) 4800+ geçmiş
// satırı SESSİZCE yanlış şeride koyardı. Normalizasyon Go'da tek yerde;
// varsayılan CH'de tek yerde; ikisi AYNI değeri söylemek zorunda.
//
// Ayrıca ALTER'in İDEMPOTENT ve LowCardinality olduğunu da pinliyor:
// `ADD COLUMN` (IF NOT EXISTS'siz) küme kipinde ikinci boot'ta patlar,
// düz `String` ise 4800 satırlık bir kolonda gereksiz yer kaplar.
func TestProblemKindColumnDefaultsToService(t *testing.T) {
	b, err := os.ReadFile("store.go")
	if err != nil {
		t.Fatalf("store.go okunamadı: %v", err)
	}
	src := string(b)

	const want = "ALTER TABLE problems ADD COLUMN IF NOT EXISTS kind " +
		"LowCardinality(String) DEFAULT '" + ProblemKindService + "'"
	if !strings.Contains(src, want) {
		t.Errorf("problems.kind ALTER'ı beklenen biçimde değil.\nBEKLENEN: %s\n"+
			"Varsayılan ProblemKindService (%q) OLMAK ZORUNDA: Go tarafındaki\n"+
			"ProblemSubjectKind normalizasyonu ile CH tarafındaki DEFAULT aynı\n"+
			"cevabı vermezse, SQL'de kind'a bakan her yüzey (Faz 4b /inbox\n"+
			"şeridi) geçmiş satırları yanlış şeride koyar.", want, ProblemKindService)
	}
	// Probe'suz bir SELECT/INSERT v0.8.185/186 sınıfını geri getirir.
	if !strings.Contains(src, "SELECT kind FROM problems LIMIT 1") {
		t.Error("kind kolonu için boot probe'u yok — ertelenmiş DDL kipinde " +
			"(v0.9.614) koşulsuz projeksiyon her problem okumasını düşürür")
	}
}

// TestDBSubjectIDIsNotAHatAJoinKey — ÖLÇÜLMÜŞ UYARININ kapısı.
//
// Biçim topoloji düğümüyle aynı (`db:<system>@<instance>`) ama KİMLİK
// UZAYI aynı DEĞİL: bu ID'nin instance'ı HAT B'den (receiver metriği,
// `instance` attr'ı), topolojininki HAT A'dan (span, dbInstanceExpr →
// ilk basamak peer_service) geliyor. Lokal ölçüm (2026-08-24, 24s):
// receiver 'corebank-scan.prod' vs MV instance 'oracle', KESİŞİM 0 SATIR.
//
// Bu test kodu değil, UYARIYI pinliyor — çünkü kaybolduğu an bir
// sonraki okuyucu iki tarafı ad eşitliğiyle JOIN eder ve hiçbir şey
// patlamaz, yalnız boş döner (v0.9.973'ün teşhis ettiği sınıf).
func TestDBSubjectIDIsNotAHatAJoinKey(t *testing.T) {
	b, err := os.ReadFile("identity.go")
	if err != nil {
		t.Fatalf("identity.go okunamadı: %v", err)
	}
	src := string(b)
	for _, must := range []string{
		"KESİŞİMİ",             // ölçümün kendisi
		"0 satır",              // ölçülen sayı
		"JOIN ETME",            // kuralın kendisi
		"db_caller_summary_5m", // hangi tabloya join edilmemeli
	} {
		if !strings.Contains(src, must) {
			t.Errorf("DBSubjectID'nin kimlik-uzayı uyarısından %q kaybolmuş — "+
				"o uyarı olmadan bir sonraki okuyucu HAT A ile HAT B'yi ad "+
				"eşitliğiyle birleştirir ve sessizce 0 satır alır", must)
		}
	}
}
