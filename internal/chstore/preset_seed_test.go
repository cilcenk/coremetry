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

// v0.9.568 — paket yükseltmesinde operatörün düzenlemeleri korunmalı.
//
// Öncesinde `DELETE WHERE id LIKE 'preset-%'` doğrudan çalışıyordu ve
// bir preset üzerinde yapılan her düzenleme paket yükseltmesinde yok
// oluyordu. Düzenleme AYNI `preset-` ID'sinin ÜSTÜNDE yaşıyor (frontend
// updateDashboard(id, …) ile aynı ID'ye yazıyor), yani dosyanın eski
// yorumundaki "kopyası yeni ID altında yaşar" varsayımı yanlıştı.

func TestPresetWasCustomised(t *testing.T) {
	// Tohumlama anında UpsertDashboard hem CreatedAt hem UpdatedAt'i
	// AYNI `now` değerine set eder — eşitlik "dokunulmadı" demektir.
	if presetWasCustomised(Dashboard{CreatedAt: 100, UpdatedAt: 100}) {
		t.Error("taze tohumlanmış satır 'dokunulmuş' sayıldı — her bumpta " +
			"gereksiz kopya birikir")
	}
	// Operatör kaydettiğinde CreatedAt korunur, UpdatedAt yenilenir.
	if !presetWasCustomised(Dashboard{CreatedAt: 100, UpdatedAt: 500}) {
		t.Error("düzenlenmiş satır 'dokunulmamış' sayıldı — operatörün emeği " +
			"paket yükseltmesinde SİLİNİR")
	}
}

func TestCustomisedPresetIDDropsPresetPrefix(t *testing.T) {
	got := customisedPresetID("preset-apm-overview")
	if got != "custom-apm-overview" {
		t.Errorf("customisedPresetID = %q, beklenen custom-apm-overview", got)
	}
	// ASIL İDDİA: yeni kimlik `preset-` ÖNEKİ TAŞIMAMALI. Taşısaydı bir
	// sonraki paket yükseltmesinde o da silinirdi — kurtardığımızı
	// bir sonraki bumpta kaybederdik.
	if strings.HasPrefix(got, "preset-") {
		t.Errorf("korunan kimlik hâlâ preset- önekli (%q) — bir sonraki "+
			"yükseltmede yine silinir", got)
	}
}

func TestSeedPreservesBeforeDeleting(t *testing.T) {
	b, err := os.ReadFile("dashboard_presets.go")
	if err != nil {
		t.Fatalf("kaynak okunamadı: %v", err)
	}
	src := stripGoLineComments(string(b))

	iPreserve := strings.Index(src, "preserveCustomisedPresets(ctx)")
	iDelete := strings.Index(src, "DELETE WHERE id LIKE 'preset-%'")
	if iPreserve < 0 {
		t.Fatal("koruma adımı yok — düzenlemeler silinir")
	}
	if iDelete < 0 {
		t.Fatal("silme adımı bulunamadı")
	}
	if iPreserve > iDelete {
		t.Error("koruma SİLMEDEN SONRA çalışıyor — kurtaracak bir şey kalmaz")
	}
	// Koruma başarısızsa silme YAPILMAMALI.
	between := src[iPreserve:iDelete]
	if !strings.Contains(between, "return fmt.Errorf(\"preserve customised presets") {
		t.Error("koruma hatası silmeyi durdurmuyor — kurtarma başarısızken " +
			"yine de siliyoruz")
	}
}
