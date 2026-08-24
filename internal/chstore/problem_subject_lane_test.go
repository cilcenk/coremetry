package chstore

import (
	"os"
	"strings"
	"testing"
)

// v0.9.1342 — /inbox'ın db özne şeridi.
//
// ORİJİNAL RİSK (v0.9.1338'in mutasyon bulgusu M13): `problems.kind`
// kolonunun DEFAULT'unu `''` yapmak DAVRANIŞSAL olarak ısırmıyordu,
// çünkü Go okuma yolu boş değeri ProblemSubjectKind ile 'service'e
// çeviriyor. Şerit ise bir SQL sorgusu — `WHERE kind = 'service'` boş
// default'la 4800+ geçmiş satırı SESSİZCE YANLIŞ ŞERİDE bırakırdı.
// TestProblemKindColumnDefaultsToService o default'u pinliyor; buradaki
// testler şeridin O GARANTİYE dayandığını, kendi Go normalizasyonuyla
// aynı sınıfı yeniden gizlemediğini pinliyor.

func TestProblemSubjectConjunct(t *testing.T) {
	tests := []struct {
		name       string
		subject    string
		hasKindCol bool
		wantSQL    string
		wantArg    any
		wantOK     bool
	}{
		{"kısıt yok", "", true, "", nil, false},
		{"kısıt yok, kolon da yok", "", false, "", nil, false},
		{"servis şeridi", ProblemKindService, true, "kind = ?", ProblemKindService, true},
		{"db şeridi", ProblemKindDB, true, "kind = ?", ProblemKindDB, true},
		// İKİ-BOOT: kolonu EKLEYEN boot probe'u false okur.
		{"kolon yok, servis şeridi daraltılmaz", ProblemKindService, false, "", nil, false},
		{"kolon yok, db şeridi SIFIR satır", ProblemKindDB, false, "1 = 0", nil, true},
		// Bilinmeyen değer FİLTRELENİR, geçirilmez. Sessizce düşen bir
		// allowlist tüm satırları döndürürdü (v0.9.1300 sınıfı).
		{"bilinmeyen özne türü sıfır satır", "queue", true, "kind = ?", "queue", true},
		{"bilinmeyen + kolon yok", "queue", false, "1 = 0", nil, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			sql, arg, ok := problemSubjectConjunct(tc.subject, tc.hasKindCol)
			if ok != tc.wantOK || sql != tc.wantSQL || arg != tc.wantArg {
				t.Errorf("problemSubjectConjunct(%q, %v) = (%q, %v, %v), beklenen (%q, %v, %v)",
					tc.subject, tc.hasKindCol, sql, arg, ok, tc.wantSQL, tc.wantArg, tc.wantOK)
			}
		})
	}
}

// Şerit CH'nin DEFAULT'una GÜVENİR — ve o güveni GİZLEYECEK bir ifade
// yazmamalıdır.
//
// "kind BOŞ DİZE ya da service" biçiminde bir OR yazmak testleri yeşil
// tutar ama
// TestProblemKindColumnDefaultsToService'in koruduğu şeyi ANLAMSIZ kılar:
// default bozulsa bile şerit çalışmaya devam eder, yani kapı çürür ve
// kimse fark etmez. Aynı şekilde ProblemSubjectKind'i WHERE üretirken
// çağırmak normalizasyonu SQL'e taşıma denemesidir ve boş dizeyi
// 'service'e çevirip aynı gizlemeyi yapar.
func TestSubjectLaneDoesNotHideTheColumnDefault(t *testing.T) {
	src := readLaneSrc(t, "problem_subject_lane.go")
	for _, bad := range []string{
		`kind = ''`,
		`kind IN (`,
		`ProblemSubjectKind(subjectKind)`,
	} {
		if strings.Contains(src, bad) {
			t.Errorf("problemSubjectConjunct çevresinde %q var — bu ifade CH'nin "+
				"DEFAULT 'service' garantisini GİZLER; garanti bozulsa bile şerit "+
				"çalışır görünür ve TestProblemKindColumnDefaultsToService anlamsızlaşır", bad)
		}
	}
	// POZİTİF KONTROL: yukarıdaki üç dize yokluğu, dosyanın BOŞ olmasıyla
	// da sağlanırdı. Aranan ifadenin gerçekten orada olduğunu doğrula.
	if !strings.Contains(src, `return "kind = ?", ProblemKindService, true`) {
		t.Error("servis şeridi artık `kind = ?` üretmiyor — kapı boşa bakıyor")
	}
	// Ve gerekçe dosyada YAZILI olmalı: bir sonraki okuyucu `kind = ''`
	// eklemeyi "daha güvenli" sanabilir.
	if !strings.Contains(src, "TestProblemKindColumnDefaultsToService") {
		t.Error("şeridin hangi teste dayandığı dosyada yazmıyor")
	}
}

// GROUP BY'da 0 satırlı bir grup HİÇ SATIR ÜRETMEZ (2026-08-23 dersi):
// hiç db problemi yoksa sorgu 'db' anahtarını DÖNDÜRMEZ ve harita eksik
// kalır. Frontend'de eksik anahtar `undefined` olur ve çip "NaN" basar.
// İki anahtar ÖNCEDEN tohumlanmalı.
func TestCountProblemsBySubjectSeedsBothBuckets(t *testing.T) {
	src := readLaneSrc(t, "problem_subject_lane.go")
	want := "out := map[string]uint64{ProblemKindService: 0, ProblemKindDB: 0}"
	if !strings.Contains(src, want) {
		t.Errorf("CountProblemsBySubject haritayı iki anahtarla tohumlamıyor (%q yok) — "+
			"0 satırlı grup HİÇ satır üretmez, eksik anahtar 'yok' ile 'ölçülemedi'yi "+
			"ayırt edilemez kılar", want)
	}
	// Hata dalı da tohumlu haritayı döndürmeli, nil değil: FE `.db`
	// okuyunca çökmesin.
	if strings.Contains(src, "return nil, err") {
		t.Error("CountProblemsBySubject hata dalında nil harita döndürüyor — " +
			"okuyucu eksik anahtarla karşılaşır")
	}
}

// ListProblems şeridi SQL'e indirmeli, Go'ya DEĞİL. Go'da daraltmak
// v0.9.322 sınıfıdır: LIMIT gösterilmeyecek satırlara harcanır.
func TestSubjectLaneNarrowsInSQLBeforeTheLimit(t *testing.T) {
	src := readLaneSrc(t, "problem.go")
	i := strings.Index(src, "problemSubjectConjunct(f.SubjectKind")
	if i < 0 {
		t.Fatal("ListProblems özne şeridini uygulamıyor")
	}
	rest := src[i:]
	limitAt := strings.Index(rest, "LIMIT ?")
	if limitAt < 0 {
		t.Fatal("ListProblems'in LIMIT'i bulunamadı")
	}
	// Conjunct, wc'ye LIMIT ifadesinden ÖNCE eklenmeli.
	if add := strings.Index(rest, "wc.add(sql"); add < 0 || add > limitAt {
		t.Error("özne şeridi WHERE'e LIMIT'ten sonra ekleniyor — daraltma taramayı " +
			"küçültmez, yalnız sonucu keser (v0.9.322)")
	}
}

func readLaneSrc(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(name)
	if err != nil {
		t.Fatalf("%s okunamadı: %v", name, err)
	}
	return string(b)
}

// TestProblemServicesConjunct — v0.9.1345. Services daraltmasının db
// istisnası.
//
// NEDEN VAR: Services listesi katalogdan SERVİS ADLARIYLA kuruluyor
// (servicesForTeam). Bir db öznesinin `service` alanı bir DBSubjectID ve
// o listede ASLA olamaz. v0.9.1345 db problemlerine sahiplik verdiğine
// göre, istisna olmadan ürün KENDİ KENDİYLE ÇELİŞİRDİ: satırın çipi
// "core-banking" yazarken owner=core-banking süzgeci onu SESSİZCE
// gizlerdi.
//
// İKİ-BOOT sözleşmesi problemSubjectConjunct ile AYNI: kolonu EKLEYEN
// boot probe'u false okur ve o boot'ta db özneli SATIR da yoktur
// (db_capacity.go kolon yokken kind yazmaz), yani istisnayı hiç yazmamak
// DOĞRU cevaptır — var olmayan bir kolona sorgu göndermek değil.
func TestProblemServicesConjunct(t *testing.T) {
	tests := []struct {
		name    string
		n       int
		allowDB bool
		hasKind bool
		want    string
	}{
		{"bayraksız: bugünkü davranış BAYT-BAYT", 3, false, true,
			"service IN (?,?,?)"},
		{"bayraksız + kolon yok: yine bugünkü", 3, false, false,
			"service IN (?,?,?)"},
		{"bayraklı: db öznelerine kaçış kapısı", 3, true, true,
			"(service IN (?,?,?) OR kind = 'db')"},
		{"bayraklı ama kolon YOK: istisna YAZILMAZ (iki-boot)", 3, true, false,
			"service IN (?,?,?)"},
		{"boş küme, bayraksız: hiçbir şey", 0, false, true,
			"1 = 0"},
		{"boş küme, bayraksız, kolon yok: hiçbir şey", 0, false, false,
			"1 = 0"},
		// Takımın HİÇ servisi yokken SADECE veritabanı sahipliği olması
		// mümkün bir hâl. `1 = 0` yazmak o satırları da öldürürdü.
		{"boş küme, bayraklı: YALNIZ db özneleri", 0, true, true,
			"kind = 'db'"},
		{"boş küme, bayraklı, kolon yok: hiçbir şey", 0, true, false,
			"1 = 0"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := problemServicesConjunct(tc.n, tc.allowDB, tc.hasKind)
			if got != tc.want {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
		})
	}
}

// TestProblemServicesConjunctNeverReturnsUnconstrained — kaçış kapısı
// asla "kısıt yok"a dönüşmemeli.
//
// Çağıran bu fonksiyonu YALNIZ f.Services != nil iken çağırır, yani her
// dal bir DARALTMA yazmak zorunda. Boş dize dönen bir dal, wc.add'e boş
// bir yüklem verip sorguyu SESSİZCE filtresiz bırakırdı — v0.8.310'un
// "empty slice = no constraint" tuzağının aynısı.
func TestProblemServicesConjunctNeverReturnsUnconstrained(t *testing.T) {
	for _, n := range []int{0, 1, 5} {
		for _, allowDB := range []bool{false, true} {
			for _, hasKind := range []bool{false, true} {
				got := problemServicesConjunct(n, allowDB, hasKind)
				if strings.TrimSpace(got) == "" {
					t.Fatalf("n=%d allowDB=%v hasKind=%v → BOŞ yüklem; "+
						"sayfa sessizce filtresiz döner", n, allowDB, hasKind)
				}
			}
		}
	}
}
