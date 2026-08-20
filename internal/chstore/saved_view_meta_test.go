package chstore

// v0.9.1192 — "Geçmiş" listesinin gövdesiz projeksiyonu.
//
// Eski yol (ListSavedViews + Go süzgeci) dört garantiyi Go'da veriyordu;
// bedeli 50 thread'lik bir liste için 64 KB'a kadar blob × 200 satırı
// CH'den taşımaktı. Garantiler SQL'e taşındı — bu dosya onların ORADA
// olduğunu pinler, çünkü artık başka hiçbir katman vermiyor.

import (
	"strings"
	"testing"
)

func TestSavedViewMetaSQL(t *testing.T) {
	for _, want := range []string{
		// ReplacingMergeTree okuma disiplini.
		"FROM saved_views FINAL",
		// Tombstone: name='' soft-delete işaretidir, listeye giremez.
		"name != ''",
		// SAHİPLİK TAM EŞİTLİK. ListSavedViews'un "OR owner_id = ''"
		// takım kovası BURADA OLAMAZ: ai-chat kişiseldir ve o OR, bir
		// kullanıcının thread listesine paylaşımlı kovayı akıtırdı.
		"owner_id = ?",
		// Tavan "en yenileri tut" demek — sıra + LIMIT birlikte.
		"ORDER BY created_at DESC",
		"LIMIT ?",
		"max_execution_time = 10",
		// Üç küçük alan CH tarafında: telin taşıdığı tek gövde bunlar.
		"JSONExtractInt(query_string, 'updatedAt')",
		"JSONLength(query_string, 'messages')",
		"JSONExtractString(query_string, 'subject')",
	} {
		if !strings.Contains(savedViewMetaSQL, want) {
			t.Errorf("savedViewMetaSQL %q içermeli:\n%s", want, savedViewMetaSQL)
		}
	}
	if strings.Contains(savedViewMetaSQL, "owner_id = ''") ||
		strings.Contains(savedViewMetaSQL, `OR owner_id`) {
		t.Error("takım kovası sızmış — kişisel projeksiyon paylaşımlı satır okuyamaz")
	}
	// Gövdenin KENDİSİ tele binemez: SELECT listesi query_string'i ancak
	// JSON fonksiyonlarının İÇİNDE görebilir.
	sel := savedViewMetaSQL[:strings.Index(savedViewMetaSQL, "FROM")]
	if n := strings.Count(sel, "query_string"); n != 3 {
		t.Errorf("SELECT'te query_string %d kez geçiyor, üçü de JSON fonksiyonu "+
			"içinde olmalı — çıplak bir geçiş 64 KB gövdeyi geri getirir", n)
	}
}

// TestListSavedViewMetaRequiresOwner — boş sahip SORGU KOŞMADAN hata.
//
// owner_id = '' CH'de takım kovasıdır; boş ownerID'yle koşan bir sorgu
// paylaşımlı satırları "benim thread'lerim" diye listelerdi. nil-conn'lu
// Store{} kanıtı: ad reddedilirse bağlantıya hiç dokunulmaz — geçerli
// sahiple çağırsak nil conn panik verirdi.
func TestListSavedViewMetaRequiresOwner(t *testing.T) {
	s := &Store{}
	for _, owner := range []string{"", "   ", "\t"} {
		if _, err := s.ListSavedViewMeta(nil, owner, "ai-chat", 50); err == nil {
			t.Errorf("ownerID=%q sorgusuz reddedilmeliydi", owner)
		}
	}
}
