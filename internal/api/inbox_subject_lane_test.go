package api

import (
	"strings"
	"testing"
)

// v0.9.1342 — /inbox'ın DB özne şeridi (operatör kararı: db problemleri
// servis problemleriyle öncelik sırasında YARIŞMASIN).

func TestNormalizeInboxSubject(t *testing.T) {
	tests := []struct{ in, want string }{
		{"", inboxSubjectService},
		{"service", inboxSubjectService},
		{"db", inboxSubjectDB},
		{" db ", inboxSubjectDB},
		// Bilinmeyen → varsayılan ŞERİT, db DEĞİL. Elle düzenlenmiş bir
		// link operatörü tanımadığı bir şeride düşürmemeli.
		{"queue", inboxSubjectService},
		{"DB", inboxSubjectService},
		{"db,service", inboxSubjectService},
	}
	for _, tc := range tests {
		if got := normalizeInboxSubject(tc.in); got != tc.want {
			t.Errorf("normalizeInboxSubject(%q) = %q, beklenen %q", tc.in, got, tc.want)
		}
	}
}

// ŞERİT CACHE ANAHTARINA GİRMELİ — ve `kind` onun yerine geçemez.
//
// db şeridi kinds'i ["problem"]e ZORLUYOR. Yani servis şeridinde yalnız
// "Problems" türünü seçen bir operatör ile db şeridindeki operatör AYNI
// kind dizisini üretir; şerit anahtarda olmasaydı ikisi tek cache
// girdisini paylaşır ve biri diğerinin satırlarını görürdü — v0.5.187
// çapraz-zehirlenmesinin birebir şekli.
func TestInboxListKeyCarriesSubject(t *testing.T) {
	only := []string{"problem"}
	svc := inboxListKey("open", "", "", "", "", "", "", 200, "priority", "desc", 0,
		only, inboxPriosAll, inboxSubjectService)
	db := inboxListKey("open", "", "", "", "", "", "", 200, "priority", "desc", 0,
		only, inboxPriosAll, inboxSubjectDB)
	if svc == db {
		t.Fatal("şerit cache anahtarını değiştirmiyor — aynı kind seçimiyle iki şerit " +
			"TEK girdiyi paylaşır ve biri diğerinin satırlarını servis eder (v0.5.187)")
	}
	if !strings.Contains(db, "subject=db") {
		t.Errorf("anahtarda şerit alanı yok: %s", db)
	}
	// Gövde şekli değişti (dbSubjectCount) → sürüm damgası ilerlemeli,
	// yoksa yükseltme öncesi cache'lenmiş bir gövde yeni sözleşmeymiş
	// gibi deserialize edilir.
	if !strings.HasPrefix(svc, "inbox:v7:") {
		t.Errorf("anahtar sürümü ilerlememiş: %s", svc)
	}
}

// Şeridin uçtan uca bağlandığının kapısı. Saf fonksiyonlar doğru
// olabilir ama HİÇ ÇAĞRILMIYORSA şerit yoktur.
func TestInboxSubjectLaneIsWired(t *testing.T) {
	src := readSrc(t, "inbox.go")
	for _, want := range []struct{ name, frag string }{
		{"param okunuyor", `subject := normalizeInboxSubject(q.Get("subject"))`},
		// KOŞULUN KENDİSİ pinli, yalnız gövdesi değil. Mutasyon testi
		// gösterdi ki `if subject == inboxSubjectDB {` → `if false {`
		// hiçbir kapıyı ısırmıyordu: zorlama satırı kaynakta DURUYOR,
		// yalnız ölü bir dalın içinde. Kaynak taraması canlılığı
		// kanıtlayamaz, ama koşulu pinlemek bu şekli kapatır.
		{"zorlama canlı bir dalda", "if subject == inboxSubjectDB {"},
		// DB özneli satır YALNIZ problems kaynağında var. Sayfanın tür
		// facet varsayılanı ['exception'] — zorlanmasa db şeridi HİÇ
		// problem çekmez ve BOŞ açılırdı.
		{"db şeridinde tür zorlanıyor", `kinds = []string{"problem"}`},
		{"şerit ProblemFilter'a iniyor", "SubjectKind: subject,"},
		{"şerit anahtarda", "subject)"},
		{"db sayısı gövdede", `"dbSubjectCount": subjectCounts[inboxSubjectDB],`},
	} {
		if !strings.Contains(src, want.frag) {
			t.Errorf("%s: %q bulunamadı — şerit yarım bağlanmış", want.name, want.frag)
		}
	}
	// Sayı `counts` sözlüğüne YAZILMAMALI: orası kind/prio evreni.
	// İkisini tek haritada karıştırmak okuyucuya hangi evrene baktığını
	// söyleyemez hâle getirir.
	if strings.Contains(src, `counts["db"]`) {
		t.Error("şerit sayısı kind/prio sözlüğüne yazılmış — iki ayrı evren tek haritada")
	}
}

// DB şeridinde tür facet'inin zorlanması, ÖNCE cache anahtarı kurulmalı.
// Sonra zorlansaydı iki farklı istek (kind=exception&subject=db ile
// kind=problem&subject=db) FARKLI anahtar üretip AYNI cevabı döndürürdü —
// zararsız ama cache'i ikiye böler; daha kötüsü, zorlama anahtardan sonra
// gelirse `narrowed` hesabı da yanlış türden okur.
func TestInboxSubjectForcesKindBeforeTheCacheKey(t *testing.T) {
	src := readSrc(t, "inbox.go")
	force := strings.Index(src, `kinds = []string{"problem"}`)
	key := strings.Index(src, "cacheKey := inboxListKey(")
	if force < 0 || key < 0 {
		t.Fatal("şerit zorlaması ya da cache anahtarı bulunamadı")
	}
	if force > key {
		t.Error("tür zorlaması cache anahtarından SONRA — anahtar, sunucunun " +
			"gerçekte kullandığı tür kümesini yansıtmaz")
	}
}
