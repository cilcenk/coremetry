package api

import "testing"

// db_detail_key_test.go — v0.9.821 REGRESYON.
//
// Çekmece kimliği (system, instance, db_name) üçlüsüne çıktı. Anahtar
// dbName'i TAŞIMAZSA düzeltilen yalan CACHE KATMANINDA yaşamaya devam
// eder: aynı host'un ilk açılan veritabanının çekmecesi 30 sn boyunca
// diğerlerine de servis edilir ve operatör iki farklı satıra tıklayıp
// aynı sayıları görür — tam olarak kaldırdığımız davranış.
//
// Ayrıca alan sınırı belirsizliği (v0.5.187 sınıfının string hâli):
// düz `:` birleştirmede "a:b" + "c" ile "a" + "b:c" AYNI anahtara
// düşerdi.

func TestDBDetailKeyIncludesDBName(t *testing.T) {
	const bucket = "1000_2000"
	a := dbDetailKey("oracle", "db-host-1", "COREBANK", bucket)
	b := dbDetailKey("oracle", "db-host-1", "CARDS", bucket)
	if a == b {
		t.Fatalf("aynı instance'ın iki veritabanı AYNI anahtara düştü: %s", a)
	}
	// Boş dbName (eski derin link) da ayrı bir slot olmalı: "tüm
	// veritabanları" farklı bir sorudur.
	c := dbDetailKey("oracle", "db-host-1", "", bucket)
	if c == a || c == b {
		t.Fatalf("boş dbName tek bir db'nin slotuyla çakıştı")
	}
}

func TestDBDetailKeyNoFieldBoundaryCollision(t *testing.T) {
	const bucket = "1000_2000"
	// Sınır kaydırma denemeleri — hiçbiri çakışmamalı.
	pairs := [][3]string{
		{"oracle", "a:b", "c"},
		{"oracle", "a", "b:c"},
		{"oracle:a", "b", "c"},
		{"oracle", "ab", "c"},
		{"oracle", "a", "bc"},
	}
	seen := map[string][3]string{}
	for _, p := range pairs {
		k := dbDetailKey(p[0], p[1], p[2], bucket)
		if prev, dup := seen[k]; dup {
			t.Fatalf("anahtar çakışması: %v ile %v aynı anahtarı üretti (%s)", prev, p, k)
		}
		seen[k] = p
	}
}

func TestDBDetailKeyIsWindowScoped(t *testing.T) {
	a := dbDetailKey("oracle", "h", "COREBANK", "1000_2000")
	b := dbDetailKey("oracle", "h", "COREBANK", "3000_4000")
	if a == b {
		t.Fatal("pencere anahtara girmiyor — başka bir aralığın çekmecesi servis edilir")
	}
}

func TestDBDetailKeyIsVersioned(t *testing.T) {
	// v2 öneki ŞART: yuvarlanan deploy sırasında eski pod'un (system,
	// instance) anahtarlı payload'ı yeni pod tarafından okunursa,
	// db_name'siz bir çekmece db_name'li bir satırın altında servis
	// edilir — düzeltmenin kendisi 30 sn boyunca görünmez olurdu.
	k := dbDetailKey("oracle", "h", "COREBANK", "1000_2000")
	if len(k) < 12 || k[:13] != "db-detail:v2:" {
		t.Fatalf("anahtar sürümlenmemiş: %s", k)
	}
}
