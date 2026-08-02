// v0.9.564 regresyon testi — preset tohumlama, sürüm okunamadığında
// YIKICI yola girmemeli.
//
// Bug: `storedVersion, _ := s.GetSetting(...)` hatayı yutuyordu.
// GetSetting'in sözleşmesi kayıt-yok için (nil,nil), GERÇEK hata için
// (nil,err) döner — ama `_` ikisini de current="" yapıyordu.
//
// Sonuç canlı bir VERİ KAYBI yoluydu: presetVersion hiç değişmemiş
// olsa bile, boot anında geçici bir ClickHouse arızası current=""
// üretiyor, o da presetVersion'a eşit olmadığı için
// `ALTER TABLE dashboards DELETE WHERE id LIKE 'preset-%'` dalına
// giriliyordu. Operatörün düzenlemeleri, kimse bir şey bumplamadan
// yok oluyordu.
//
// Store'a CH bağlantısı gerektiği için kaynak sözleşmesi sabitleniyor
// (repoda emsali var: snapshot_batch_test.go, enrich_chain_test.go).
package chstore

import (
	"os"
	"strings"
	"testing"
)

func TestSeedPresetDashboardsDoesNotSwallowVersionError(t *testing.T) {
	b, err := os.ReadFile("dashboard_presets.go")
	if err != nil {
		t.Fatalf("kaynak okunamadı: %v", err)
	}
	// YORUMLAR ATILIR. Düzeltmenin kendi yorumu eski kodu ALINTILIYOR
	// ("Öncesi `storedVersion, _ := …` idi") ve ham metin taraması onu
	// gerçek kod sanıp yanlış alarm veriyordu. Bir regresyon testi,
	// düzeltmenin AÇIKLAMASINI ihlal saymamalı.
	src := stripGoLineComments(string(b))

	if strings.Contains(src, `storedVersion, _ := s.GetSetting(`) {
		t.Error("sürüm okuma hatası YİNE yutuluyor — boot anındaki geçici bir " +
			"ClickHouse arızası tüm preset dashboard'ları siler ve operatörün " +
			"düzenlemeleri kimse bir şey bumplamadan yok olur")
	}

	i := strings.Index(src, "func (s *Store) SeedPresetDashboards(")
	if i < 0 {
		t.Fatal("SeedPresetDashboards bulunamadı")
	}
	body := src[i:]
	if end := strings.Index(body, "\n}\n"); end > 0 {
		body = body[:end]
	}

	// Sürüm okuması, silme kararından ÖNCE hata dönmeli.
	iRead := strings.Index(body, "GetSetting(ctx, \"preset_dashboards_version\")")
	iGuard := strings.Index(body, "return fmt.Errorf(\"read preset dashboards version")
	if iRead < 0 || iGuard < 0 {
		t.Fatal("sürüm okuma + hata kapısı bulunamadı")
	}
	if iGuard < iRead {
		t.Error("hata kapısı okumadan ÖNCE — sıra anlamsız")
	}

	// Yıkıcı DELETE hâlâ var (davranış kaldırılmadı, yalnız kapıya bağlandı)
	// ve hata kapısından SONRA gelmeli.
	iDelete := strings.Index(body, "DELETE WHERE id LIKE 'preset-%'")
	if iDelete >= 0 && iDelete < iGuard {
		t.Error("DELETE, sürüm hata kapısından ÖNCE koşuyor — okuyamadığımız " +
			"bir sürüm yüzünden silmeye başlarız")
	}
}

// stripGoLineComments — satır yorumlarını çıkarır (kaba ama bu tarama
// için yeterli: string literal içinde "//" geçen bir satır burada yok).
func stripGoLineComments(src string) string {
	var out []string
	for _, line := range strings.Split(src, "\n") {
		if i := strings.Index(line, "//"); i >= 0 {
			line = line[:i]
		}
		out = append(out, line)
	}
	return strings.Join(out, "\n")
}
